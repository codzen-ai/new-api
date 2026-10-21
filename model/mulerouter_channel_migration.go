package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	hostreasoning "github.com/QuantumNous/new-api/setting/reasoning"
	"gorm.io/gorm"
)

// muleRouterPluginKey is the built-in task plugin that serves MuleRouter
// endpoints on type-61 Task Plugin channels.
const muleRouterPluginKey = "mulerouter"

const (
	// goMuleRouterChannelType is the channel type the removed MuleRouter Go
	// adaptor served. constant/channel.go keeps the value reserved.
	goMuleRouterChannelType = 64
	// goMuleRouterBaseURL is the address the Go adaptor used for a channel
	// without a Base URL; the mulerouter plugin declares it as its baseUrl.
	goMuleRouterBaseURL = "https://api.mulerouter.ai"
	// muleRouterDefaultAction is the action of a route that names none.
	muleRouterDefaultAction = "generation"
)

// muleRouterConfig is the MuleRouter route table a Go adaptor channel stored
// under "mulerouter" in its settings column.
type muleRouterConfig struct {
	Routes []muleRouterRoute `json:"routes,omitempty"`
}

// muleRouterRoute is one MuleRouter endpoint of a channel's route table.
type muleRouterRoute struct {
	Vendor      string                 `json:"vendor"`
	Model       string                 `json:"model"`
	Action      string                 `json:"action,omitempty"`
	BillingVars []muleRouterBillingVar `json:"billing_vars,omitempty"`
}

// muleRouterBillingVar bounds one request field of an endpoint and prices it.
// The channel's billing_vars and the plugin's muleRouterEndpoints export share
// this shape, so the two are compared field by field.
type muleRouterBillingVar struct {
	Name    string             `json:"name"`
	Source  string             `json:"source,omitempty"`
	Kind    string             `json:"kind"`
	Default string             `json:"default"`
	Min     *float64           `json:"min,omitempty"`
	Max     *float64           `json:"max,omitempty"`
	Enum    []float64          `json:"enum,omitempty"`
	Values  map[string]float64 `json:"values,omitempty"`
	Divisor float64            `json:"divisor,omitempty"`
}

// modelName is the endpoint's MuleRouter model name,
// "{vendor}/{model}/{action}".
func (r *muleRouterRoute) modelName() string {
	action := strings.TrimSpace(r.Action)
	if action == "" {
		action = muleRouterDefaultAction
	}
	return fmt.Sprintf("%s/%s/%s", strings.TrimSpace(r.Vendor), strings.TrimSpace(r.Model), action)
}

// channelMuleRouterConfig returns the MuleRouter route table stored on the
// channel, or nil when it carries none or its settings cannot be read.
func channelMuleRouterConfig(channel *Channel) *muleRouterConfig {
	var settings struct {
		MuleRouter *muleRouterConfig `json:"mulerouter"`
	}
	if channel.OtherSettings == "" || common.UnmarshalJsonStr(channel.OtherSettings, &settings) != nil {
		return nil
	}
	return settings.MuleRouter
}

// MuleRouter channels had type 61 before upstream assigned it to the Task
// Plugin channel (constant.ChannelTypeTaskPlugin). A type-61 channel is a
// legacy MuleRouter channel only when it still carries a MuleRouter route
// table; upstream Task Plugin channels have none.
//
// The Go adaptor's tasks carry its channel type as their platform, or the
// legacy type 61 when they were submitted before the type moved.
var (
	goMuleRouterTaskPlatform     = constant.TaskPlatform(strconv.Itoa(goMuleRouterChannelType))
	legacyMuleRouterTaskPlatform = constant.TaskPlatform(strconv.Itoa(constant.ChannelTypeTaskPlugin))
)

// MigrateMuleRouterChannelsToTaskPlugin moves every MuleRouter channel (type
// 64, or a legacy type 61 carrying a MuleRouter route table) onto the
// mulerouter task plugin: the channel becomes a type-61 Task Plugin channel
// bound to the plugin, its route table is removed, and its tasks are handed to
// the plugin, all in one transaction per channel. A channel without a Base URL
// gets the address the Go adaptor used for it, the MuleRouter channel type's
// default, because a plugin channel sends requests only to the address stored
// on the channel. No other channel field changes.
//
// A channel moves only when every MuleRouter endpoint in its route table is
// declared by the plugin with exactly the same billing fields, judged against
// the table the loaded plugin exports. Any other channel is left on type 64,
// where the removed Go adaptor served it, and logged with the endpoints that do
// not conform; nothing serves type 64 any more, so such a channel fails its
// requests until an administrator fixes it.
//
// A moved channel no longer carries a route table, so it is never selected
// again and running this on every start is safe. It must run on the master
// node only, before the channel cache loads.
func MigrateMuleRouterChannelsToTaskPlugin() error {
	var channels []Channel
	if err := DB.Select("id", "type", "base_url", "models", "model_mapping", "setting", "settings").
		Where("type IN ?", []int{goMuleRouterChannelType, constant.ChannelTypeTaskPlugin}).
		Order("id").Find(&channels).Error; err != nil {
		return err
	}
	channels = slices.DeleteFunc(channels, func(channel Channel) bool {
		return channel.Type == constant.ChannelTypeTaskPlugin && channelMuleRouterConfig(&channel) == nil
	})
	if len(channels) == 0 {
		return nil
	}

	endpoints, loadErr := loadMuleRouterPluginEndpoints()
	var migrated, skipped []int
	var migratedTasks int64
	for i := range channels {
		channel := &channels[i]
		config := channelMuleRouterConfig(channel)
		var problems []string
		switch {
		case loadErr != nil:
			problems = []string{loadErr.Error()}
		case config == nil || len(config.Routes) == 0:
			problems = []string{"the channel configures no MuleRouter endpoint"}
		default:
			for j := range config.Routes {
				if problem := muleRouterEndpointMismatch(&config.Routes[j], endpoints); problem != "" {
					problems = append(problems, problem)
				}
			}
			if outside := muleRouterEndpointsOutsideRouteTable(channel, config, endpoints); len(outside) > 0 {
				problems = append(problems, fmt.Sprintf("the channel's models and model_mapping reach %s, which the plugin serves but the route table lacks",
					strings.Join(outside, ", ")))
			}
		}
		if len(problems) > 0 {
			common.SysError(common.LogText("MuleRouter channel #%d stays on type 64, which nothing serves: %s", channel.Id, strings.Join(problems, "; ")))
			if err := keepMuleRouterChannelOnGoAdaptor(channel); err != nil {
				return err
			}
			skipped = append(skipped, channel.Id)
			continue
		}
		tasks, err := moveMuleRouterChannelToTaskPlugin(channel)
		if err != nil {
			return fmt.Errorf("migrate MuleRouter channel #%d: %w", channel.Id, err)
		}
		migrated = append(migrated, channel.Id)
		migratedTasks += tasks
	}
	common.SysLog(common.LogText("moved %d MuleRouter channel(s) %v and %d task(s) onto the %s task plugin; %d channel(s) %v stay on type 64, which nothing serves",
		len(migrated), migrated, migratedTasks, muleRouterPluginKey, len(skipped), skipped))
	return nil
}

// loadMuleRouterPluginEndpoints returns the MuleRouter endpoints the loaded
// mulerouter plugin declares, keyed by MuleRouter model name, with their
// billing fields.
//
// It reads the plugin's muleRouterEndpoints export, which is not part of the
// plugin API: the name and shape ({<model name>: {billing: [billing_vars]}})
// are a contract between this migration and plugins/tasks/mulerouter/plugin.js.
// A missing or renamed export fails here and keeps every channel on the Go
// adaptor (fail closed).
func loadMuleRouterPluginEndpoints() (map[string][]muleRouterBillingVar, error) {
	plugin, ok := jsplugin.DefaultRegistry.Generation().Get(muleRouterPluginKey)
	if !ok {
		return nil, errors.New("the mulerouter task plugin is not loaded")
	}
	exported, err := plugin.Engine.Export(context.Background(), "muleRouterEndpoints")
	if err != nil {
		return nil, fmt.Errorf("read the mulerouter plugin endpoint table: %w", err)
	}
	encoded, err := common.Marshal(exported)
	if err != nil {
		return nil, fmt.Errorf("read the mulerouter plugin endpoint table: %w", err)
	}
	var table map[string]struct {
		Billing []muleRouterBillingVar `json:"billing"`
	}
	if err := common.Unmarshal(encoded, &table); err != nil {
		return nil, fmt.Errorf("read the mulerouter plugin endpoint table: %w", err)
	}
	endpoints := make(map[string][]muleRouterBillingVar, len(table))
	for name, endpoint := range table {
		endpoints[name] = endpoint.Billing
	}
	return endpoints, nil
}

// muleRouterEndpointMismatch explains why the plugin cannot serve a channel's
// MuleRouter endpoint at the same price, or returns "" when it can: the plugin
// must declare the endpoint, and its billing fields must bound and price every
// request field exactly as the channel's billing_vars do.
func muleRouterEndpointMismatch(route *muleRouterRoute, endpoints map[string][]muleRouterBillingVar) string {
	name := route.modelName()
	billing, declared := endpoints[name]
	if !declared {
		return name + " is not declared by the plugin"
	}
	normalize := func(vars []muleRouterBillingVar) map[string]muleRouterBillingVar {
		normalized := make(map[string]muleRouterBillingVar, len(vars))
		for _, v := range vars {
			v.Name = strings.TrimSpace(v.Name)
			v.Source = strings.TrimSpace(v.Source)
			if v.Source == "" {
				v.Source = v.Name
			}
			if len(v.Enum) == 0 {
				v.Enum = nil
			}
			if len(v.Values) == 0 {
				v.Values = nil
			}
			normalized[v.Name] = v
		}
		return normalized
	}
	channelVars, pluginVars := normalize(route.BillingVars), normalize(billing)
	if len(channelVars) != len(route.BillingVars) || len(pluginVars) != len(billing) {
		return name + " declares a billing field twice"
	}
	fields := maps.Clone(channelVars)
	maps.Copy(fields, pluginVars)
	var differing []string
	for _, field := range slices.Sorted(maps.Keys(fields)) {
		channelVar, onChannel := channelVars[field]
		pluginVar, onPlugin := pluginVars[field]
		if !onChannel || !onPlugin || !reflect.DeepEqual(channelVar, pluginVar) {
			differing = append(differing, field)
		}
	}
	if len(differing) > 0 {
		return fmt.Sprintf("%s billing field(s) %s differ from the plugin", name, strings.Join(differing, ", "))
	}
	return ""
}

// muleRouterEndpointsOutsideRouteTable returns, sorted, the plugin endpoints a
// request for one of the channel's models reaches through its model_mapping
// although the channel's route table lacks them. The Go adaptor refuses such a
// request, while the plugin serves every endpoint in its table, so moving the
// channel would make it serve and charge for them.
//
// The mapping is followed as the relay's ModelMappedHelper does, through
// followChannelModelMapping with each hop's reasoning-suffix fallback filled
// in first. A mapping the relay cannot read or that cycles fails every request
// on both implementations, so it reaches nothing. Names neither in the route
// table nor in the plugin table are refused by both and are not reported.
func muleRouterEndpointsOutsideRouteTable(channel *Channel, config *muleRouterConfig, endpoints map[string][]muleRouterBillingVar) []string {
	modelMap := map[string]string{}
	if mapping := channel.GetModelMapping(); mapping != "" && mapping != "{}" {
		if err := common.UnmarshalJsonStr(mapping, &modelMap); err != nil {
			return nil
		}
	}
	models := channel.GetModels()
	hops := slices.Concat(models, slices.Collect(maps.Values(modelMap)))
	for _, hop := range hops {
		if modelMap[hop] == "" {
			modelMap[hop] = modelMap[hostreasoning.BaseModelName(hop)]
		}
	}
	var outside []string
	for _, name := range models {
		upstream, cyclic := followChannelModelMapping(modelMap, name)
		if cyclic {
			continue
		}
		if _, served := endpoints[upstream]; !served {
			continue
		}
		routed := slices.ContainsFunc(config.Routes, func(route muleRouterRoute) bool { return route.modelName() == strings.TrimSpace(upstream) })
		if !routed && !slices.Contains(outside, upstream) {
			outside = append(outside, upstream)
		}
	}
	slices.Sort(outside)
	return outside
}

// moveMuleRouterChannelToTaskPlugin binds the channel to the mulerouter
// plugin, drops its route table, fills in an empty Base URL and hands its tasks
// to the plugin, returning the number of tasks moved.
func moveMuleRouterChannelToTaskPlugin(channel *Channel) (int64, error) {
	setting := map[string]json.RawMessage{}
	if channel.Setting != nil && strings.TrimSpace(*channel.Setting) != "" {
		if err := common.UnmarshalJsonStr(*channel.Setting, &setting); err != nil {
			return 0, fmt.Errorf("read channel setting: %w", err)
		}
	}
	setting["task_plugin_key"] = json.RawMessage(strconv.Quote(muleRouterPluginKey))
	encodedSetting, err := common.Marshal(setting)
	if err != nil {
		return 0, err
	}
	otherSettings := map[string]json.RawMessage{}
	if err := common.UnmarshalJsonStr(channel.OtherSettings, &otherSettings); err != nil {
		return 0, fmt.Errorf("read channel settings: %w", err)
	}
	delete(otherSettings, "mulerouter")
	encodedOtherSettings, err := common.Marshal(otherSettings)
	if err != nil {
		return 0, err
	}

	updates := map[string]any{
		"type":     constant.ChannelTypeTaskPlugin,
		"setting":  string(encodedSetting),
		"settings": string(encodedOtherSettings),
	}
	// The mulerouter plugin declares the same address as its baseUrl.
	if channel.BaseURL == nil || strings.TrimSpace(*channel.BaseURL) == "" {
		updates["base_url"] = goMuleRouterBaseURL
	}

	var movedTasks int64
	err = DB.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&Channel{}).Where("id = ? AND type = ?", channel.Id, channel.Type).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("the channel changed while it was being migrated")
		}
		result = tx.Model(&Task{}).
			Where("channel_id = ? AND platform IN ?", channel.Id, []constant.TaskPlatform{goMuleRouterTaskPlatform, legacyMuleRouterTaskPlatform}).
			Update("platform", constant.TaskPlatform(muleRouterPluginKey))
		movedTasks = result.RowsAffected
		return result.Error
	})
	return movedTasks, err
}

// keepMuleRouterChannelOnGoAdaptor leaves a channel the plugin cannot serve
// on type 64, the type of the removed Go adaptor, so it is never mistaken for
// a Task Plugin channel. A type-64 channel is already there; a legacy type-61
// channel moves to type 64 with its tasks.
func keepMuleRouterChannelOnGoAdaptor(channel *Channel) error {
	if channel.Type != constant.ChannelTypeTaskPlugin {
		return nil
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&Channel{}).Where("id = ? AND type = ?", channel.Id, constant.ChannelTypeTaskPlugin).
			Update("type", goMuleRouterChannelType).Error; err != nil {
			return err
		}
		return tx.Model(&Task{}).
			Where("channel_id = ? AND platform = ?", channel.Id, legacyMuleRouterTaskPlatform).
			Update("platform", goMuleRouterTaskPlatform).Error
	})
}
