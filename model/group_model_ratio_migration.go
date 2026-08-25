package model

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"gorm.io/gorm"
)

const (
	groupModelRatioSemanticsKey        = "GroupModelRatioSemantics"
	groupModelRatioCoefficientSemantic = "coefficient"
)

// ErrGroupModelRatioMigrationUnsafe 表示迁移在「存量配置非空」的情况下失败，
// 此时继续运行会按错误的价格计费，调用方必须让启动失败而不是记一条日志了事：
//   - 换算没写成功：进程会用新语义解释旧的绝对倍率，超收（旧值 5 会被当成 5 倍）；
//   - 换算写成功但标记没写成功：下次启动会再除一次全局倍率，少收一个数量级。
//
// 存量配置为空时上述两种后果都不存在（没有条目可解释错，标记缺失只是下次重试），
// 因此不包装成这个错误，避免为一次瞬时数据库故障挡住启动。
var ErrGroupModelRatioMigrationUnsafe = errors.New("group model ratio semantics migration left an unsafe state")

// MigrateGroupModelRatioToCoefficient 把 GroupModelRatio 从「覆盖即最终价」迁移到
// 「该模型在该分组的分组倍率」语义。
//
// 旧语义：最终倍率 = 覆盖值（分组倍率被归一为 1）。
// 新语义：最终倍率 = 全局模型倍率 × 该值（该值取代分组倍率）。
// 因此等价换算是 新值 = 旧值 / 全局模型倍率，与分组倍率无关。
//
// 无法换算的条目会被丢弃并记入系统日志：
//   - 模型没有配置全局倍率、或全局倍率为 0：新语义无法表达原来的价格，
//     保留原值会按错误的价格计费（原值会被当成系数）。
//   - 模型按固定价或表达式计费：旧语义下这类条目本来就不生效（旧的 ModelPriceHelper
//     在读取覆盖值之前就已经沿固定价/表达式分支返回），而新语义对这两类模型同样生效，
//     保留会让这些分组在升级后突然变价。
//
// 必须在选项加载进内存之后调用（依赖 ratio_setting.GetModelRatio 的模型名归一与实际配置），
// 且只在主节点执行；通过 GroupModelRatioSemantics 选项标记，重复调用是安全的。
//
// 失败时若存量配置非空，返回的错误会包装 ErrGroupModelRatioMigrationUnsafe，
// 调用方必须终止启动，见该变量的说明。
func MigrateGroupModelRatioToCoefficient() error {
	existing := ratio_setting.GetGroupModelRatioCopy()
	// 有存量配置时，任何一步失败都会留下会错误计费的状态
	fail := func(err error) error {
		if len(existing) == 0 {
			return err
		}
		return fmt.Errorf("%w: %v", ErrGroupModelRatioMigrationUnsafe, err)
	}

	if DB == nil {
		return fail(errors.New("database is not initialized"))
	}

	var marker Option
	err := DB.Where(&Option{Key: groupModelRatioSemanticsKey}).First(&marker).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return fail(fmt.Errorf("read option %s: %w", groupModelRatioSemanticsKey, err))
	}
	if err == nil && marker.Value == groupModelRatioCoefficientSemantic {
		return nil
	}

	migrated, unpriced, ineffective := migrateGroupModelRatioTable(existing)

	if len(unpriced) > 0 {
		sort.Strings(unpriced)
		common.SysError(fmt.Sprintf(
			"分组模型倍率迁移：以下条目没有对应的全局模型倍率，无法换算成系数，已被移除，请重新配置（格式 分组/模型=原倍率）：%s",
			strings.Join(unpriced, ", "),
		))
	}
	if len(ineffective) > 0 {
		sort.Strings(ineffective)
		common.SysError(fmt.Sprintf(
			"分组模型倍率迁移：以下条目配在固定价或表达式计费模型上，旧版本中本来就不生效，已被移除；"+
				"新版本对这两类模型同样生效，如需折扣请按系数重新配置（格式 分组/模型=原倍率）：%s",
			strings.Join(ineffective, ", "),
		))
	}

	encoded, err := common.Marshal(migrated)
	if err != nil {
		return fail(fmt.Errorf("marshal migrated group model ratio: %w", err))
	}
	if err := UpdateOption("GroupModelRatio", string(encoded)); err != nil {
		return fail(fmt.Errorf("write migrated group model ratio: %w", err))
	}
	if err := UpdateOption(groupModelRatioSemanticsKey, groupModelRatioCoefficientSemantic); err != nil {
		return fail(fmt.Errorf("write option %s: %w", groupModelRatioSemanticsKey, err))
	}

	common.SysLog(fmt.Sprintf(
		"分组模型倍率已迁移为系数语义：%d 个分组，%d 个条目被移除",
		len(migrated), len(unpriced)+len(ineffective),
	))
	return nil
}

// migrateGroupModelRatioTable 换算整张表，返回新表以及两类被丢弃条目的
// "分组/模型=原值" 描述：没有全局倍率的、以及旧语义下本就不生效的。
func migrateGroupModelRatioTable(table map[string]map[string]float64) (
	migrated map[string]map[string]float64, unpriced []string, ineffective []string,
) {
	migrated = make(map[string]map[string]float64, len(table))

	for group, models := range table {
		converted := make(map[string]float64, len(models))
		for modelName, oldRatio := range models {
			entry := fmt.Sprintf("%s/%s=%g", group, modelName, oldRatio)
			// 判定顺序与 ModelPriceHelper 一致：表达式计费优先于固定价。旧代码在这两条
			// 分支上都会在读取覆盖值之前返回，所以这些条目从未生效过，原样换算会让它们
			// 在新语义下突然生效并改价——丢弃才能保持价格不变。
			if billing_setting.GetBillingMode(modelName) == billing_setting.BillingModeTieredExpr ||
				isFixedPricedModel(modelName) {
				ineffective = append(ineffective, entry)
				continue
			}
			// 免费保持免费：0 在两种语义下都表示不收费。
			if oldRatio == 0 {
				converted[modelName] = 0
				continue
			}
			base, ok, _ := ratio_setting.GetModelRatio(modelName)
			if !ok || base <= 0 {
				unpriced = append(unpriced, entry)
				continue
			}
			coefficient := oldRatio / base
			if math.IsNaN(coefficient) || math.IsInf(coefficient, 0) {
				unpriced = append(unpriced, entry)
				continue
			}
			converted[modelName] = coefficient
		}
		if len(converted) > 0 {
			migrated[group] = converted
		}
	}

	return migrated, unpriced, ineffective
}

// isFixedPricedModel 与 ModelPriceHelperPerCall 的固定价判定保持一致。
func isFixedPricedModel(modelName string) bool {
	if _, ok := ratio_setting.GetModelPrice(modelName, false); ok {
		return true
	}
	_, ok := ratio_setting.GetDefaultModelPriceMap()[modelName]
	return ok
}
