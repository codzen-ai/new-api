package model

import (
	"fmt"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"gorm.io/gorm"
)

// legacyMuleRouterChannelType 是 MuleRouter 渠道迁移前使用的类型值。上游后来把 61 分配给了
// Task Plugin，同一个值在两边含义不同，所以存量 MuleRouter 渠道必须改到新的类型值上。
const legacyMuleRouterChannelType = 61

// MigrateMuleRouterChannelType 把存量 MuleRouter 渠道从类型 61 改为 constant.ChannelTypeMuleRouter，
// 并把这些渠道的任务 platform 一并改掉，未完成任务的轮询与查询才能找回 MuleRouter 适配器。
//
// 类型 61 的渠道只有带 mulerouter 路由表配置的才是 MuleRouter：上游的 Task Plugin 渠道没有这项配置，
// 不会被误改。迁移完成后不再有符合条件的行，因此每次启动重复执行是安全的，不需要额外的完成标记。
//
// 必须在渠道缓存加载之前、且只在主节点执行。
func MigrateMuleRouterChannelType() error {
	var channels []Channel
	if err := DB.Select("id", "settings").Where("type = ?", legacyMuleRouterChannelType).Find(&channels).Error; err != nil {
		return err
	}
	var ids []int
	for i := range channels {
		if channels[i].GetOtherSettings().MuleRouter != nil {
			ids = append(ids, channels[i].Id)
		}
	}
	if len(ids) == 0 {
		return nil
	}

	legacyPlatform := constant.TaskPlatform(strconv.Itoa(legacyMuleRouterChannelType))
	platform := constant.TaskPlatform(strconv.Itoa(constant.ChannelTypeMuleRouter))
	var migratedTasks int64
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&Channel{}).
			Where("id IN ? AND type = ?", ids, legacyMuleRouterChannelType).
			Update("type", constant.ChannelTypeMuleRouter).Error; err != nil {
			return err
		}
		result := tx.Model(&Task{}).
			Where("channel_id IN ? AND platform = ?", ids, legacyPlatform).
			Update("platform", platform)
		migratedTasks = result.RowsAffected
		return result.Error
	})
	if err != nil {
		return err
	}
	common.SysLog(fmt.Sprintf("migrated %d MuleRouter channel(s) %v and %d task(s) from type %d to %d", len(ids), ids, migratedTasks, legacyMuleRouterChannelType, constant.ChannelTypeMuleRouter))
	return nil
}
