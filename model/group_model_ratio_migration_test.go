package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// useGroupModelRatioMigrationState 准备一个内存库并显式设置迁移依赖的倍率/价格状态。
func useGroupModelRatioMigrationState(t *testing.T, modelRatio, groupModelRatio string) *gorm.DB {
	return useGroupModelRatioMigrationStateWithPrices(t, modelRatio, `{}`, groupModelRatio)
}

func useGroupModelRatioMigrationStateWithPrices(t *testing.T, modelRatio, modelPrice, groupModelRatio string) *gorm.DB {
	t.Helper()
	db := useFrontendOptionMigrationDB(t)

	origModelRatio := ratio_setting.ModelRatio2JSONString()
	origModelPrice := ratio_setting.ModelPrice2JSONString()
	origGroupModelRatio := ratio_setting.GroupModelRatio2JSONString()
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(origModelRatio))
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(origModelPrice))
		require.NoError(t, ratio_setting.UpdateGroupModelRatioByJSONString(origGroupModelRatio))
	})
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(modelRatio))
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(modelPrice))
	require.NoError(t, ratio_setting.UpdateGroupModelRatioByJSONString(groupModelRatio))

	common.OptionMap = map[string]string{}
	return db
}

// TestMigrateGroupModelRatioConvertsAbsoluteRatiosToCoefficients 锁定语义迁移的换算规则：
// 旧值是该分组下的最终模型倍率，新值是取代分组倍率的系数，因此 新值 = 旧值 / 全局模型倍率。
// 换算必须与分组倍率无关——旧语义下分组倍率对命中的模型本来就不生效。
func TestMigrateGroupModelRatioConvertsAbsoluteRatiosToCoefficients(t *testing.T) {
	db := useGroupModelRatioMigrationState(t,
		`{"gpt-4o":10,"claude":8}`,
		`{"vip":{"gpt-4o":5,"claude":8},"svip":{"gpt-4o":0}}`,
	)

	require.NoError(t, MigrateGroupModelRatioToCoefficient())

	// 5 / 10 = 0.5（原来最终倍率 5，现在 10 × 0.5 = 5）；8 / 8 = 1（等于不打折）
	assert.JSONEq(t,
		`{"vip":{"gpt-4o":0.5,"claude":1},"svip":{"gpt-4o":0}}`,
		requireOptionValue(t, db, "GroupModelRatio"),
	)
	assert.Equal(t, groupModelRatioCoefficientSemantic,
		requireOptionValue(t, db, groupModelRatioSemanticsKey))

	// 迁移结果同时写回内存配置
	ratio, ok := ratio_setting.GetGroupModelRatio("vip", "gpt-4o")
	require.True(t, ok)
	assert.Equal(t, 0.5, ratio)
}

// TestMigrateGroupModelRatioDropsUnconvertibleEntries 没有全局模型倍率的条目在新语义下
// 无法表达原价格（系数乘不到任何基准上），必须丢弃而不是原样保留——保留会让原来的
// 绝对倍率被当成系数，价格差出数量级。
func TestMigrateGroupModelRatioDropsUnconvertibleEntries(t *testing.T) {
	db := useGroupModelRatioMigrationState(t,
		`{"gpt-4o":10,"zero-ratio-model":0}`,
		`{"vip":{"gpt-4o":5,"no-global-model":3,"zero-ratio-model":2},"only-bad":{"no-global-model":3}}`,
	)

	require.NoError(t, MigrateGroupModelRatioToCoefficient())

	// 只保留可换算的条目；整组都无法换算时该分组也一并移除
	assert.JSONEq(t, `{"vip":{"gpt-4o":0.5}}`, requireOptionValue(t, db, "GroupModelRatio"))
}

// TestMigrateGroupModelRatioIsIdempotent 迁移是不可判别的（迁移后的 0.5 和管理员手写的 0.5
// 无法区分），所以必须靠标记位保证只跑一次，重复启动不能把系数再除一次。
func TestMigrateGroupModelRatioIsIdempotent(t *testing.T) {
	db := useGroupModelRatioMigrationState(t,
		`{"gpt-4o":10}`,
		`{"vip":{"gpt-4o":5}}`,
	)

	require.NoError(t, MigrateGroupModelRatioToCoefficient())
	assert.JSONEq(t, `{"vip":{"gpt-4o":0.5}}`, requireOptionValue(t, db, "GroupModelRatio"))

	require.NoError(t, MigrateGroupModelRatioToCoefficient())
	assert.JSONEq(t, `{"vip":{"gpt-4o":0.5}}`, requireOptionValue(t, db, "GroupModelRatio"))
}

// TestMigrateGroupModelRatioMarksEmptyConfig 新装或没配过分组模型倍率的站点也要写标记，
// 否则第一次配好之后重启会被当成旧数据再除一遍。
func TestMigrateGroupModelRatioMarksEmptyConfig(t *testing.T) {
	db := useGroupModelRatioMigrationState(t, `{"gpt-4o":10}`, `{}`)

	require.NoError(t, MigrateGroupModelRatioToCoefficient())

	assert.JSONEq(t, `{}`, requireOptionValue(t, db, "GroupModelRatio"))
	assert.Equal(t, groupModelRatioCoefficientSemantic,
		requireOptionValue(t, db, groupModelRatioSemanticsKey))
}

// TestMigrateGroupModelRatioDropsFixedPricedEntries 固定价模型上的旧条目在旧版本里是
// 静默失效的，而新版本对固定价模型同样生效。直接换算会让这些分组在升级后突然变价，
// 所以必须丢弃——包括「同时配了固定价和全局倍率」这种能换算但不该换算的情况。
func TestMigrateGroupModelRatioDropsFixedPricedEntries(t *testing.T) {
	db := useGroupModelRatioMigrationStateWithPrices(t,
		`{"gpt-4o":10,"both-model":10}`,
		`{"draw-model":0.04,"both-model":0.04}`,
		`{"vip":{"gpt-4o":5,"draw-model":3,"both-model":5}}`,
	)

	require.NoError(t, MigrateGroupModelRatioToCoefficient())

	assert.JSONEq(t, `{"vip":{"gpt-4o":0.5}}`, requireOptionValue(t, db, "GroupModelRatio"))
}

// TestMigrateGroupModelRatioSignalsUnsafeFailure 迁移失败时，「有存量配置」和「没有存量配置」
// 必须是两种不同的错误：前者继续运行会错误计费（旧绝对值被当成系数超收，或标记缺失导致
// 下次启动再除一次少收），调用方必须终止启动；后者只是没什么可迁移的，不该为此挡住启动。
func TestMigrateGroupModelRatioSignalsUnsafeFailure(t *testing.T) {
	t.Run("有存量配置：错误可识别为不安全", func(t *testing.T) {
		db := useGroupModelRatioMigrationState(t, `{"gpt-4o":10}`, `{"vip":{"gpt-4o":5}}`)
		require.NoError(t, db.Migrator().DropTable(&Option{}))

		err := MigrateGroupModelRatioToCoefficient()
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrGroupModelRatioMigrationUnsafe)
	})

	t.Run("没有存量配置：普通错误，不阻断启动", func(t *testing.T) {
		db := useGroupModelRatioMigrationState(t, `{"gpt-4o":10}`, `{}`)
		require.NoError(t, db.Migrator().DropTable(&Option{}))

		err := MigrateGroupModelRatioToCoefficient()
		require.Error(t, err)
		assert.NotErrorIs(t, err, ErrGroupModelRatioMigrationUnsafe)
	})
}
