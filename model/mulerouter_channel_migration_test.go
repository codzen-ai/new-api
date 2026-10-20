package model

import (
	"fmt"
	"os"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	// Registers the built-in task plugins, so the migration checks channels
	// against the endpoint table the mulerouter plugin actually loads.
	_ "github.com/QuantumNous/new-api/plugins"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// The route table of the Go MuleRouter channel on staging (channel 87).
const muleRouterStagingRoutes = `{"routes":[
	{"vendor":"carrothub","model":"qwen-image-edit-spicy"},
	{"vendor":"carrothub","model":"z-image-spicy","billing_vars":[
		{"name":"prompt_extend","kind":"enum","values":{"true":1.076923,"false":1},"default":"true"},
		{"name":"width","kind":"int","min":256,"max":1536,"default":"1024"},
		{"name":"height","kind":"int","min":256,"max":1536,"default":"1536"}]},
	{"vendor":"carrothub","model":"wan2.2-i2v-spicy","billing_vars":[
		{"name":"seconds","source":"duration","kind":"int","enum":[5,8],"default":"5","divisor":1},
		{"name":"resolution","kind":"enum","values":{"480p":1,"720p":2},"default":"480p"}]}]}`

// useMuleRouterMigrationPostgres points DB at an empty schema of the
// PostgreSQL database named by TEST_POSTGRES_DSN.
func useMuleRouterMigrationPostgres(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	// One connection, so the search_path below applies to every statement.
	sqlDB.SetMaxOpenConns(1)
	const schema = "mulerouter_channel_migration_test"
	require.NoError(t, db.Exec("DROP SCHEMA IF EXISTS "+schema+" CASCADE").Error)
	require.NoError(t, db.Exec("CREATE SCHEMA "+schema).Error)
	require.NoError(t, db.Exec("SET search_path TO "+schema).Error)
	require.NoError(t, db.AutoMigrate(&Channel{}, &Task{}))

	previousDB, previousType := DB, common.MainDatabaseType()
	DB = db
	common.SetMainDatabaseType(common.DatabaseTypePostgreSQL)
	t.Cleanup(func() {
		DB = previousDB
		common.SetMainDatabaseType(previousType)
		_ = db.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE").Error
		_ = sqlDB.Close()
	})
	return db
}

func muleRouterSettings(routes string) string {
	return fmt.Sprintf(`{"disable_task_polling_sleep":true,"mulerouter":%s}`, routes)
}

// TestMigrateMuleRouterChannelsToTaskPlugin runs the migration on a database
// in the shape production upgrades from: a Go MuleRouter channel (type 64), a
// legacy type-61 channel still carrying its route table, an upstream Task
// Plugin channel (type 61, no route table), channels whose route tables the
// plugin cannot serve at the same price, and tasks of each.
func TestMigrateMuleRouterChannelsToTaskPlugin(t *testing.T) {
	db := useMuleRouterMigrationPostgres(t)

	setting := `{"proxy":"http://proxy:8080","http_protocol":"http1"}`
	mapping := `{"z-image-spicy":"carrothub/z-image-spicy/generation"}`
	weight, priority := uint(7), int64(3)
	baseURL := "https://api.mulerouter.ai"
	soraSetting := `{"task_plugin_key":"sora"}`
	undeclared := `{"routes":[
		{"vendor":"carrothub","model":"qwen-image-edit-spicy"},
		{"vendor":"carrothub","model":"not-in-the-plugin","action":"edit"}]}`
	repriced := `{"routes":[{"vendor":"carrothub","model":"wan2.2-i2v-spicy","billing_vars":[
		{"name":"seconds","source":"duration","kind":"int","enum":[5,8],"default":"5","divisor":1},
		{"name":"resolution","kind":"enum","values":{"480p":1,"720p":3},"default":"480p"}]}]}`
	// The plugin serves every endpoint in its table on every channel, while the
	// Go adaptor serves only the channel's own routes. A channel whose models
	// reach, through model_mapping as the relay resolves it, an endpoint the
	// plugin serves but its route table lacks would start serving (and
	// charging for) that endpoint, so it stays on the Go adaptor.
	zImageOnly := `{"routes":[{"vendor":"carrothub","model":"z-image-spicy","billing_vars":[
		{"name":"prompt_extend","kind":"enum","values":{"true":1.076923,"false":1},"default":"true"},
		{"name":"width","kind":"int","min":256,"max":1536,"default":"1024"},
		{"name":"height","kind":"int","min":256,"max":1536,"default":"1536"}]}]}`
	chainedMapping := `{"z-image":"carrothub/z-image-spicy/generation","edit":"edit-alias","edit-alias":"carrothub/qwen-image-edit-spicy/generation"}`
	// The relay maps a reasoning-suffixed name through its base name's entry.
	suffixMapping := `{"video":"carrothub/wan2.2-i2v-spicy/generation"}`
	// None of these reaches an endpoint outside the route table: "other" ends on
	// a name neither implementation serves, "unlisted" is not one of the
	// channel's models, and "loop" is a cycle the relay refuses.
	harmlessMapping := `{"z-image":"carrothub/z-image-spicy/generation","other":"acme/unknown/generation",
		"unlisted":"carrothub/qwen-image-edit-spicy/generation",
		"loop":"carrothub/wan2.2-i2v-spicy/generation","carrothub/wan2.2-i2v-spicy/generation":"loop"}`
	channels := []*Channel{
		{
			Id: 1, Type: constant.ChannelTypeMuleRouter, Name: "mulerouter", Key: "sk-mule", Status: common.ChannelStatusEnabled,
			BaseURL: &baseURL, Models: "z-image-spicy,wan2.2-i2v-spicy", Group: "default,vip", ModelMapping: &mapping,
			Weight: &weight, Priority: &priority, Setting: &setting, OtherSettings: muleRouterSettings(muleRouterStagingRoutes),
		},
		{Id: 2, Type: 61, Name: "legacy mulerouter", Key: "sk-legacy", OtherSettings: `{"mulerouter":` + muleRouterStagingRoutes + `}`},
		{Id: 3, Type: constant.ChannelTypeTaskPlugin, Name: "sora", Key: "sk-sora", Setting: &soraSetting, OtherSettings: `{}`},
		{Id: 4, Type: constant.ChannelTypeMuleRouter, Name: "undeclared endpoint", Key: "sk-4", OtherSettings: muleRouterSettings(undeclared)},
		{Id: 5, Type: constant.ChannelTypeMuleRouter, Name: "repriced endpoint", Key: "sk-5", OtherSettings: muleRouterSettings(repriced)},
		{Id: 6, Type: 61, Name: "legacy repriced", Key: "sk-6", OtherSettings: muleRouterSettings(repriced)},
		{
			Id: 7, Type: constant.ChannelTypeMuleRouter, Name: "chained mapping outside the route table", Key: "sk-7",
			Models: "z-image,edit", ModelMapping: &chainedMapping, OtherSettings: muleRouterSettings(zImageOnly),
		},
		{
			Id: 8, Type: 61, Name: "legacy unmapped model outside the route table", Key: "sk-8",
			Models: "carrothub/z-image-spicy/generation,carrothub/wan2.2-i2v-spicy/generation", OtherSettings: muleRouterSettings(zImageOnly),
		},
		{
			Id: 9, Type: constant.ChannelTypeMuleRouter, Name: "suffixed model outside the route table", Key: "sk-9",
			Models: "video@thinking:on", ModelMapping: &suffixMapping, OtherSettings: muleRouterSettings(zImageOnly),
		},
		{
			Id: 10, Type: constant.ChannelTypeMuleRouter, Name: "mapping reaches nothing outside the route table", Key: "sk-10",
			Models: "z-image,other,loop,spare", ModelMapping: &harmlessMapping, OtherSettings: muleRouterSettings(zImageOnly),
		},
	}
	require.NoError(t, db.Create(channels).Error)
	before := map[int]Channel{}
	for _, channel := range channels {
		var row Channel
		require.NoError(t, db.First(&row, channel.Id).Error)
		before[channel.Id] = row
	}
	tasks := []*Task{
		{TaskID: "task_go_in_flight", ChannelId: 1, Platform: "64", Status: TaskStatusInProgress},
		{TaskID: "task_go_finished", ChannelId: 1, Platform: "64", Status: TaskStatusSuccess},
		{TaskID: "task_go_before_type_move", ChannelId: 1, Platform: "61", Status: TaskStatusFailure},
		{TaskID: "task_legacy", ChannelId: 2, Platform: "61", Status: TaskStatusSubmitted},
		{TaskID: "task_sora", ChannelId: 3, Platform: "sora", Status: TaskStatusInProgress},
		{TaskID: "task_sora_legacy_platform", ChannelId: 3, Platform: "61", Status: TaskStatusSuccess},
		{TaskID: "task_undeclared", ChannelId: 4, Platform: "64", Status: TaskStatusInProgress},
		{TaskID: "task_repriced", ChannelId: 5, Platform: "64", Status: TaskStatusInProgress},
		{TaskID: "task_legacy_repriced", ChannelId: 6, Platform: "61", Status: TaskStatusInProgress},
		{TaskID: "task_chained_mapping", ChannelId: 7, Platform: "64", Status: TaskStatusInProgress},
		{TaskID: "task_legacy_outside_route_table", ChannelId: 8, Platform: "61", Status: TaskStatusInProgress},
		{TaskID: "task_suffixed_model", ChannelId: 9, Platform: "64", Status: TaskStatusInProgress},
		{TaskID: "task_harmless_mapping", ChannelId: 10, Platform: "64", Status: TaskStatusInProgress},
	}
	require.NoError(t, db.Create(tasks).Error)

	type channelState struct {
		Type          int
		Setting       string
		OtherSettings string
	}
	movedSettings := `{"disable_task_polling_sleep":true}`
	expectedChannels := map[int]channelState{
		1: {constant.ChannelTypeTaskPlugin, `{"proxy":"http://proxy:8080","http_protocol":"http1","task_plugin_key":"mulerouter"}`, movedSettings},
		2: {constant.ChannelTypeTaskPlugin, `{"task_plugin_key":"mulerouter"}`, `{}`},
		3: {constant.ChannelTypeTaskPlugin, soraSetting, `{}`},
		4: {constant.ChannelTypeMuleRouter, "", muleRouterSettings(undeclared)},
		5: {constant.ChannelTypeMuleRouter, "", muleRouterSettings(repriced)},
		// The plugin cannot serve it, so it goes where the Go adaptor serves it.
		6:  {constant.ChannelTypeMuleRouter, "", muleRouterSettings(repriced)},
		7:  {constant.ChannelTypeMuleRouter, "", muleRouterSettings(zImageOnly)},
		8:  {constant.ChannelTypeMuleRouter, "", muleRouterSettings(zImageOnly)},
		9:  {constant.ChannelTypeMuleRouter, "", muleRouterSettings(zImageOnly)},
		10: {constant.ChannelTypeTaskPlugin, `{"task_plugin_key":"mulerouter"}`, movedSettings},
	}
	expectedPlatforms := map[string]constant.TaskPlatform{
		"task_go_in_flight":               "mulerouter",
		"task_go_finished":                "mulerouter",
		"task_go_before_type_move":        "mulerouter",
		"task_legacy":                     "mulerouter",
		"task_sora":                       "sora",
		"task_sora_legacy_platform":       "61",
		"task_undeclared":                 "64",
		"task_repriced":                   "64",
		"task_legacy_repriced":            "64",
		"task_chained_mapping":            "64",
		"task_legacy_outside_route_table": "64",
		"task_suffixed_model":             "64",
		"task_harmless_mapping":           "mulerouter",
	}

	for run := 1; run <= 2; run++ {
		require.NoError(t, MigrateMuleRouterChannelsToTaskPlugin(), "run %d", run)

		for id, expected := range expectedChannels {
			var row Channel
			require.NoError(t, db.First(&row, id).Error)
			assert.Equal(t, expected.Type, row.Type, "run %d channel %d", run, id)
			if expected.Setting == "" {
				assert.Nil(t, row.Setting, "run %d channel %d", run, id)
			} else {
				require.NotNil(t, row.Setting, "run %d channel %d", run, id)
				assert.JSONEq(t, expected.Setting, *row.Setting, "run %d channel %d", run, id)
			}
			assert.JSONEq(t, expected.OtherSettings, row.OtherSettings, "run %d channel %d", run, id)
			unchanged := before[id]
			unchanged.Type, unchanged.Setting, unchanged.OtherSettings = row.Type, row.Setting, row.OtherSettings
			assert.Equal(t, unchanged, row, "run %d channel %d keeps every other field", run, id)
		}
		for taskID, platform := range expectedPlatforms {
			var task Task
			require.NoError(t, db.Where("task_id = ?", taskID).First(&task).Error)
			assert.Equal(t, platform, task.Platform, "run %d task %s", run, taskID)
		}
	}
}
