package model

import (
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMigrateMuleRouterChannelTypeMovesOnlyMuleRouterChannels 锁定迁移的判定边界：类型 61 只有带
// mulerouter 配置的渠道是 MuleRouter，上游的 Task Plugin 渠道同样是 61，必须原样保留；任务的
// platform 跟着渠道走，其他渠道的任务不受影响。
func TestMigrateMuleRouterChannelTypeMovesOnlyMuleRouterChannels(t *testing.T) {
	db := useFrontendOptionMigrationDB(t)
	require.NoError(t, db.AutoMigrate(&Channel{}, &Task{}))

	muleRouter := &Channel{Id: 1, Type: 61, Name: "mulerouter", OtherSettings: `{"mulerouter":{"routes":[]}}`}
	taskPlugin := &Channel{Id: 2, Type: 61, Name: "task-plugin", OtherSettings: `{}`}
	openAI := &Channel{Id: 3, Type: constant.ChannelTypeOpenAI, Name: "openai", OtherSettings: `{"mulerouter":{"routes":[]}}`}
	require.NoError(t, db.Create([]*Channel{muleRouter, taskPlugin, openAI}).Error)
	require.NoError(t, db.Create([]*Task{
		{TaskID: "task_mule", ChannelId: 1, Platform: "61"},
		{TaskID: "task_plugin", ChannelId: 2, Platform: "61"},
	}).Error)

	require.NoError(t, MigrateMuleRouterChannelType())
	// 第二次执行不应再改动任何数据
	require.NoError(t, MigrateMuleRouterChannelType())

	channelType := func(id int) int {
		var channel Channel
		require.NoError(t, db.Select("type").First(&channel, id).Error)
		return channel.Type
	}
	assert.Equal(t, constant.ChannelTypeMuleRouter, channelType(1))
	assert.Equal(t, constant.ChannelTypeTaskPlugin, channelType(2))
	assert.Equal(t, constant.ChannelTypeOpenAI, channelType(3))

	taskPlatform := func(taskID string) constant.TaskPlatform {
		var task Task
		require.NoError(t, db.Where("task_id = ?", taskID).First(&task).Error)
		return task.Platform
	}
	assert.Equal(t, constant.TaskPlatform("64"), taskPlatform("task_mule"))
	assert.Equal(t, constant.TaskPlatform("61"), taskPlatform("task_plugin"))
}
