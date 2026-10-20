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
	"github.com/QuantumNous/new-api/relaykit/dto"
	"gorm.io/gorm"
)

// muleRouterPluginKey is the built-in task plugin that serves MuleRouter
// endpoints on type-61 Task Plugin channels.
const muleRouterPluginKey = "mulerouter"

// legacyMuleRouterChannelType is the type MuleRouter channels had before
// upstream assigned 61 to the Task Plugin channel. A type-61 channel is a
// legacy MuleRouter channel only when it still carries a MuleRouter route
// table; upstream Task Plugin channels have none.
const legacyMuleRouterChannelType = 61

// MigrateMuleRouterChannelsToTaskPlugin moves every MuleRouter channel (type
// 64, or a legacy type 61 carrying a MuleRouter route table) onto the
// mulerouter task plugin: the channel becomes a type-61 Task Plugin channel
// bound to the plugin, its route table is removed, and its tasks are handed to
// the plugin, all in one transaction per channel. No other channel field
// changes.
//
// A channel moves only when every MuleRouter endpoint in its route table is
// declared by the plugin with exactly the same billing fields, judged against
// the table the loaded plugin exports. Any other channel is left to the Go
// adaptor on type 64 and logged with the endpoints that do not conform.
//
// A moved channel no longer carries a route table, so it is never selected
// again and running this on every start is safe. It must run on the master
// node only, before the channel cache loads.
func MigrateMuleRouterChannelsToTaskPlugin() error {
	var channels []Channel
	if err := DB.Select("id", "type", "setting", "settings").
		Where("type IN ?", []int{constant.ChannelTypeMuleRouter, legacyMuleRouterChannelType}).
		Order("id").Find(&channels).Error; err != nil {
		return err
	}
	channels = slices.DeleteFunc(channels, func(channel Channel) bool {
		return channel.Type == legacyMuleRouterChannelType && channel.GetOtherSettings().MuleRouter == nil
	})
	if len(channels) == 0 {
		return nil
	}

	endpoints, loadErr := loadMuleRouterPluginEndpoints()
	var migrated, skipped []int
	var migratedTasks int64
	for i := range channels {
		channel := &channels[i]
		config := channel.GetOtherSettings().MuleRouter
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
		}
		if len(problems) > 0 {
			common.SysError(common.LogText("MuleRouter channel #%d stays on the Go adaptor: %s", channel.Id, strings.Join(problems, "; ")))
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
	common.SysLog(common.LogText("moved %d MuleRouter channel(s) %v and %d task(s) onto the %s task plugin; %d channel(s) %v stay on the Go adaptor",
		len(migrated), migrated, migratedTasks, muleRouterPluginKey, len(skipped), skipped))
	return nil
}

// loadMuleRouterPluginEndpoints returns the MuleRouter endpoints the loaded
// mulerouter plugin declares, keyed by MuleRouter model name, with their
// billing fields.
func loadMuleRouterPluginEndpoints() (map[string][]dto.MuleRouterBillingVar, error) {
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
		Billing []dto.MuleRouterBillingVar `json:"billing"`
	}
	if err := common.Unmarshal(encoded, &table); err != nil {
		return nil, fmt.Errorf("read the mulerouter plugin endpoint table: %w", err)
	}
	endpoints := make(map[string][]dto.MuleRouterBillingVar, len(table))
	for name, endpoint := range table {
		endpoints[name] = endpoint.Billing
	}
	return endpoints, nil
}

// muleRouterEndpointMismatch explains why the plugin cannot serve a channel's
// MuleRouter endpoint at the same price, or returns "" when it can: the plugin
// must declare the endpoint, and its billing fields must bound and price every
// request field exactly as the channel's billing_vars do.
func muleRouterEndpointMismatch(route *dto.MuleRouterRoute, endpoints map[string][]dto.MuleRouterBillingVar) string {
	name := route.ModelName()
	billing, declared := endpoints[name]
	if !declared {
		return name + " is not declared by the plugin"
	}
	normalize := func(vars []dto.MuleRouterBillingVar) map[string]dto.MuleRouterBillingVar {
		normalized := make(map[string]dto.MuleRouterBillingVar, len(vars))
		for _, v := range vars {
			v.Name = strings.TrimSpace(v.Name)
			v.Source = v.SourceKey()
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

// moveMuleRouterChannelToTaskPlugin binds the channel to the mulerouter
// plugin, drops its route table and hands its tasks to the plugin, returning
// the number of tasks moved.
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

	var movedTasks int64
	err = DB.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&Channel{}).Where("id = ? AND type = ?", channel.Id, channel.Type).Updates(map[string]any{
			"type":     constant.ChannelTypeTaskPlugin,
			"setting":  string(encodedSetting),
			"settings": string(encodedOtherSettings),
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("the channel changed while it was being migrated")
		}
		// The Go adaptor's tasks carry its channel type, or the legacy type
		// when they were submitted before the type moved.
		goPlatforms := []constant.TaskPlatform{
			constant.TaskPlatform(strconv.Itoa(constant.ChannelTypeMuleRouter)),
			constant.TaskPlatform(strconv.Itoa(legacyMuleRouterChannelType)),
		}
		result = tx.Model(&Task{}).
			Where("channel_id = ? AND platform IN ?", channel.Id, goPlatforms).
			Update("platform", constant.TaskPlatform(muleRouterPluginKey))
		movedTasks = result.RowsAffected
		return result.Error
	})
	return movedTasks, err
}

// keepMuleRouterChannelOnGoAdaptor leaves a channel the plugin cannot serve
// with the Go adaptor. A type-64 channel is already there; a legacy type-61
// channel moves to type 64 with its tasks, or nothing would serve it.
func keepMuleRouterChannelOnGoAdaptor(channel *Channel) error {
	if channel.Type != legacyMuleRouterChannelType {
		return nil
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&Channel{}).Where("id = ? AND type = ?", channel.Id, legacyMuleRouterChannelType).
			Update("type", constant.ChannelTypeMuleRouter).Error; err != nil {
			return err
		}
		return tx.Model(&Task{}).
			Where("channel_id = ? AND platform = ?", channel.Id, constant.TaskPlatform(strconv.Itoa(legacyMuleRouterChannelType))).
			Update("platform", constant.TaskPlatform(strconv.Itoa(constant.ChannelTypeMuleRouter))).Error
	})
}
