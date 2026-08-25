package helper

import (
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/reasoning"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	hostreasoning "github.com/QuantumNous/new-api/setting/reasoning"
	hosttypes "github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

func modelPriceNotConfiguredError(c *gin.Context, modelName string, userId int) error {
	key := i18n.MsgRelayModelPriceNotConfigured
	if model.IsAdmin(userId) {
		key = i18n.MsgRelayModelPriceNotConfiguredAdmin
	}
	return errors.New(i18n.T(c, key, map[string]any{"Model": modelName}))
}

// HandleGroupRatio checks for "auto_group" in the context and updates the group ratio and relayInfo.UsingGroup if present
func HandleGroupRatio(ctx *gin.Context, relayInfo *relaycommon.RelayInfo) hosttypes.GroupRatioInfo {
	groupRatioInfo := hosttypes.GroupRatioInfo{
		GroupRatio:        1.0, // default ratio
		GroupSpecialRatio: -1,
	}

	// check auto group
	autoGroup, exists := ctx.Get("auto_group")
	if exists {
		logger.LogDebug(ctx, "final group: %s", autoGroup)
		relayInfo.UsingGroup = autoGroup.(string)
	}

	// check user group special ratio
	userGroupRatio, ok := ratio_setting.GetGroupGroupRatio(relayInfo.UserGroup, relayInfo.UsingGroup)
	if ok {
		// user group special ratio
		groupRatioInfo.GroupSpecialRatio = userGroupRatio
		groupRatioInfo.GroupRatio = userGroupRatio
		groupRatioInfo.HasSpecialRatio = true
	} else {
		// normal group ratio
		groupRatioInfo.GroupRatio = ratio_setting.GetGroupRatio(relayInfo.UsingGroup)
	}

	return groupRatioInfo
}

// ResolveGroupRatioInfo 解析计费分组对该模型生效的分组倍率：命中分组模型倍率时，该倍率就是
// 这个模型在这个分组的分组倍率，取代 GroupRatio 与分组间覆盖。模型自身的定价方式不受影响——
// 按量倍率模型仍乘全局模型倍率、固定价模型仍乘固定价、表达式模型仍按表达式求值，
// 三类计费方式因此共用同一套语义，配置层不需要区分模型的计费方式。
func ResolveGroupRatioInfo(c *gin.Context, info *relaycommon.RelayInfo, modelName string) hosttypes.GroupRatioInfo {
	groupRatioInfo := HandleGroupRatio(c, info)
	newGroupRatio, overridden := ratio_setting.ResolveGroupModelGroupRatio(info.UsingGroup, modelName, groupRatioInfo.GroupRatio)
	if !overridden {
		return groupRatioInfo
	}
	groupRatioInfo.GroupRatio = newGroupRatio
	// 命中即取代分组倍率：分组间覆盖同时失效，否则会继续叠乘
	groupRatioInfo.GroupSpecialRatio = -1
	groupRatioInfo.HasSpecialRatio = false
	return groupRatioInfo
}

// RefreshGroupPricingForSelectedGroup 在每次选定渠道后重新解析与计费分组相关的定价。
// 预扣费只算一次，但结算读的是 PriceData，而 auto 分组重试会把计费分组换掉，
// 所以分组模型倍率必须跟着最终分组走，否则会出现「旧分组的倍率 × 新分组的分组倍率」。
func RefreshGroupPricingForSelectedGroup(c *gin.Context, info *relaycommon.RelayInfo) {
	// 与 ModelPriceHelper 一致：按计费模型名解析（按次计费路径不设 BillingModelName，即原始模型名）
	info.PriceData.GroupRatioInfo = ResolveGroupRatioInfo(c, info, info.GetBillingModelName())
}

func ModelPriceHelper(c *gin.Context, info *relaycommon.RelayInfo, promptTokens int, meta *types.TokenCountMeta) (hosttypes.PriceData, error) {
	if info != nil {
		if matched := resolveBillingModelName(info.GetOriginModelName()); matched != "" && matched != info.OriginModelName {
			info.BillingModelName = matched
		}
	}
	billingModelName := info.GetBillingModelName()
	modelPrice, usePrice := ratio_setting.GetModelPrice(billingModelName, false)

	// 分组模型倍率：命中则该值取代分组倍率，与模型走哪种计费方式无关
	groupRatioInfo := ResolveGroupRatioInfo(c, info, billingModelName)

	// Check if this model uses tiered_expr billing
	if billing_setting.GetBillingMode(billingModelName) == billing_setting.BillingModeTieredExpr {
		return modelPriceHelperTiered(c, info, billingModelName, promptTokens, groupRatioInfo)
	}

	var preConsumedQuota int
	var modelRatio float64
	var completionRatio float64
	var cacheRatio float64
	var imageRatio float64
	var cacheCreationRatio float64
	var cacheCreationRatio5m float64
	var cacheCreationRatio1h float64
	var cacheCreationRatio12h float64
	var audioRatio float64
	var audioCompletionRatio float64
	var freeModel bool
	if !usePrice {
		preConsumeMultiplier, err := operation_setting.InputPreConsumeMultiplier()
		if err != nil {
			return hosttypes.PriceData{}, err
		}
		preConsumedTokens := float64(promptTokens) * preConsumeMultiplier
		var success bool
		var matchName string
		modelRatio, success, matchName = ratio_setting.GetModelRatio(billingModelName)
		if !success {
			acceptUnsetRatio := false
			if info.UserSetting.AcceptUnsetRatioModel {
				acceptUnsetRatio = true
			}
			if !acceptUnsetRatio {
				return hosttypes.PriceData{}, modelPriceNotConfiguredError(c, matchName, info.UserId)
			}
		}
		completionRatio = ratio_setting.GetCompletionRatio(billingModelName)
		cacheRatio, _ = ratio_setting.GetCacheRatio(billingModelName)
		cacheCreationRatio, _ = ratio_setting.GetCreateCacheRatio(billingModelName)
		cacheCreationRatio5m = cacheCreationRatio
		// 固定1h和5min缓存写入价格的比例
		cacheCreationRatio1h = cacheCreationRatio * hosttypes.ClaudeCacheCreation1hMultiplier
		cacheCreationRatio12h = cacheCreationRatio * hosttypes.OpenAICacheCreation12hMultiplier
		imageRatio, _ = ratio_setting.GetImageRatio(billingModelName)
		audioRatio = ratio_setting.GetAudioRatio(billingModelName)
		audioCompletionRatio = ratio_setting.GetAudioCompletionRatio(billingModelName)
		ratio := modelRatio * groupRatioInfo.GroupRatio
		quota, err := common.QuotaFromFloatStrict(preConsumedTokens * ratio)
		if err != nil {
			return hosttypes.PriceData{}, err
		}
		preConsumedQuota = quota
		if _, image := info.Request.(*dto.ImageRequest); image {
			info.ImageQuotaBeforeGroup = preConsumedTokens * modelRatio
		}
	} else {
		if meta.ImagePriceRatio != 0 {
			modelPrice = modelPrice * meta.ImagePriceRatio
		}
		if _, image := info.Request.(*dto.ImageRequest); image {
			info.ImageQuotaBeforeGroup = modelPrice * common.QuotaPerUnit
		}
	}

	// check if free model pre-consume is disabled
	if !operation_setting.GetQuotaSetting().EnableFreeModelPreConsume {
		// if model price or ratio is 0, do not pre-consume quota
		if groupRatioInfo.GroupRatio == 0 {
			preConsumedQuota = 0
			freeModel = true
		} else if usePrice {
			if modelPrice == 0 {
				preConsumedQuota = 0
				freeModel = true
			}
		} else {
			if modelRatio == 0 {
				preConsumedQuota = 0
				freeModel = true
			}
		}
	}

	priceData := hosttypes.PriceData{
		FreeModel:             freeModel,
		ModelPrice:            modelPrice,
		ModelRatio:            modelRatio,
		CompletionRatio:       completionRatio,
		GroupRatioInfo:        groupRatioInfo,
		UsePrice:              usePrice,
		CacheRatio:            cacheRatio,
		ImageRatio:            imageRatio,
		AudioRatio:            audioRatio,
		AudioCompletionRatio:  audioCompletionRatio,
		CacheCreationRatio:    cacheCreationRatio,
		CacheCreation5mRatio:  cacheCreationRatio5m,
		CacheCreation1hRatio:  cacheCreationRatio1h,
		CacheCreation12hRatio: cacheCreationRatio12h,
		QuotaToPreConsume:     preConsumedQuota,
	}
	if usePrice {
		for name, ratio := range meta.BillingRatios {
			priceData.AddOtherRatio(name, ratio)
		}
	}
	if request, image := info.Request.(*dto.ImageRequest); image {
		count, err := request.ImageCount(false)
		if err != nil {
			return hosttypes.PriceData{}, err
		}
		if usePrice {
			priceData.AddOtherRatio("n", float64(count))
		}
		if !usePrice {
			quota, err := common.QuotaFromFloatStrict(priceData.ApplyOtherRatiosToFloat(info.ImageQuotaBeforeGroup * groupRatioInfo.GroupRatio))
			if err != nil {
				return hosttypes.PriceData{}, err
			}
			priceData.QuotaToPreConsume = quota
		}
	}
	if usePrice {
		quotaToPreConsume := priceData.ApplyOtherRatiosToFloat(modelPrice * common.QuotaPerUnit * groupRatioInfo.GroupRatio)
		quota, err := common.QuotaFromFloatStrict(quotaToPreConsume)
		if err != nil {
			return hosttypes.PriceData{}, err
		}
		priceData.QuotaToPreConsume = quota
	}

	if common.DebugEnabled {
		logger.LogDebug(c, "model_price_helper result: %s", priceData.ToSetting())
	}
	info.PriceData = priceData
	return priceData, nil
}

// ModelPriceHelperPerCall 按次/按量计费的 PriceHelper (MJ、Task)
func ModelPriceHelperPerCall(c *gin.Context, info *relaycommon.RelayInfo) (hosttypes.PriceData, error) {
	// 分组模型倍率：命中则该值取代分组倍率，与模型走哪种计费方式无关
	groupRatioInfo := ResolveGroupRatioInfo(c, info, info.OriginModelName)

	modelPrice, success := ratio_setting.GetModelPrice(info.OriginModelName, true)
	usePrice := success
	var modelRatio float64

	if !success {
		defaultPrice, ok := ratio_setting.GetDefaultModelPriceMap()[info.OriginModelName]
		if ok {
			modelPrice = defaultPrice
			usePrice = true
		} else {
			var ratioSuccess bool
			var matchName string
			modelRatio, ratioSuccess, matchName = ratio_setting.GetModelRatio(info.OriginModelName)
			acceptUnsetRatio := false
			if info.UserSetting.AcceptUnsetRatioModel {
				acceptUnsetRatio = true
			}
			if !ratioSuccess && !acceptUnsetRatio {
				return hosttypes.PriceData{}, modelPriceNotConfiguredError(c, matchName, info.UserId)
			}
		}
	}

	var quota int
	freeModel := false

	if usePrice {
		var err error
		quota, err = common.QuotaFromFloatStrict(modelPrice * common.QuotaPerUnit * groupRatioInfo.GroupRatio)
		if err != nil {
			return hosttypes.PriceData{}, err
		}
		if !operation_setting.GetQuotaSetting().EnableFreeModelPreConsume {
			if groupRatioInfo.GroupRatio == 0 || modelPrice == 0 {
				quota = 0
				freeModel = true
			}
		}
	} else {
		// 按量计费：以模型倍率的一半作为预扣额度
		var err error
		quota, err = common.QuotaFromFloatStrict(modelRatio / 2 * common.QuotaPerUnit * groupRatioInfo.GroupRatio)
		if err != nil {
			return hosttypes.PriceData{}, err
		}
		modelPrice = -1
		if !operation_setting.GetQuotaSetting().EnableFreeModelPreConsume {
			if groupRatioInfo.GroupRatio == 0 || modelRatio == 0 {
				quota = 0
				freeModel = true
			}
		}
	}

	priceData := hosttypes.PriceData{
		FreeModel:      freeModel,
		ModelPrice:     modelPrice,
		ModelRatio:     modelRatio,
		UsePrice:       usePrice,
		Quota:          quota,
		GroupRatioInfo: groupRatioInfo,
	}
	return priceData, nil
}

// HasModelBillingConfig 判断模型是否已配置计费方式。
// 用于 ListModels 过滤和 Gemini 非思考变体探测。
// 分组模型倍率不参与判定：它只取代分组倍率，不给模型定价，
// 所以没有全局倍率的模型不会因为配了分组模型倍率就变成可计费。
func HasModelBillingConfig(modelName string) bool {
	// 表达式计费的模型只由表达式定价：全局倍率不能让一个没有表达式的模型变成可计费，
	// 否则它会进入模型列表，实际调用时却在计费阶段报错。
	if billing_setting.GetBillingMode(modelName) == billing_setting.BillingModeTieredExpr {
		expr, ok := billing_setting.GetBillingExpr(modelName)
		return ok && strings.TrimSpace(expr) != ""
	}
	if _, ok := ratio_setting.GetModelPrice(modelName, false); ok {
		return true
	}
	_, ok, _ := ratio_setting.GetModelRatio(modelName)
	return ok
}

// HasPriceOrRatioEntry reports whether name has a configured price, ratio, or
// tiered billing-mode entry after a single wildcard normalization. Self-use
// fallback does not count as a configured ratio.
func HasPriceOrRatioEntry(name string) bool {
	formatted := ratio_setting.FormatMatchingModelName(name)
	if _, ok := ratio_setting.GetModelPrice(formatted, false); ok {
		return true
	}
	if ratio_setting.HasConfiguredModelRatio(formatted) {
		return true
	}
	return billing_setting.GetBillingMode(formatted) == billing_setting.BillingModeTieredExpr
}

func resolveBillingModelName(origin string) string {
	var candidates []string
	if !reasoning.ParseModelModifiers(origin).HasModifiers() {
		candidates = append(candidates, origin)
	}
	candidates = append(candidates, hostreasoning.CanonicalBillingModelNames(origin)...)
	base := hostreasoning.BaseModelName(origin)
	// DeepSeek thinking tails (-none/-low/-max) bill at the base model's price,
	// with or without @ modifiers after them.
	if deepSeekBase, _, _, ok := reasoning.ParseDeepSeekV4ThinkingSuffix(base); ok && !model_setting.ShouldPreserveThinkingSuffix(base) {
		candidates = append(candidates, deepSeekBase)
	}
	candidates = append(candidates, base)

	seen := make(map[string]struct{}, len(candidates))
	matched := ""
	for _, name := range candidates {
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		if HasPriceOrRatioEntry(name) {
			matched = name
			break
		}
	}
	if matched == "" {
		matched = base
	}
	return matched
}

func modelPriceHelperTiered(c *gin.Context, info *relaycommon.RelayInfo, billingModelName string, promptTokens int, groupRatioInfo hosttypes.GroupRatioInfo) (hosttypes.PriceData, error) {
	exprStr, ok := billing_setting.GetBillingExpr(billingModelName)
	if !ok {
		return hosttypes.PriceData{}, fmt.Errorf("model %s is configured as tiered_expr but has no billing expression", billingModelName)
	}
	exprHash := billingexpr.ExprHashString(exprStr)
	usedVars := billingexpr.UsedVarsByHash(exprStr, exprHash)
	if usedVars["u"] {
		return hosttypes.PriceData{}, errors.New(i18n.T(c, i18n.MsgRelayTaskUsagePriceUnsupported, map[string]any{"Model": billingModelName}))
	}
	fixedPricing := billingexpr.UsesFixedPricingByHash(exprStr, exprHash)
	value, _ := c.Get(jsplugin.ContextKeyRelayEndpoint)
	if endpoint, ok := value.(jsplugin.PinnedEndpoint); ok && !fixedPricing {
		for _, candidate := range endpoint.Candidates {
			pluginKey := candidate.Plugin.Meta.Key
			if _, overridden := billing_setting.GetPluginBillingExpr(pluginKey, endpoint.Model); overridden {
				continue
			}
			if _, overridden := billing_setting.GetPluginBillingExpr(pluginKey, endpoint.MappedModel); endpoint.MappedModel != "" && overridden {
				continue
			}
			taskBillingModel := endpoint.Model
			if billing_setting.GetBillingMode(taskBillingModel) != billing_setting.BillingModeTieredExpr {
				taskBillingModel = endpoint.MappedModel
			}
			// Task expressions return USD; ordinary expressions return USD *
			// 1M. Even a constant without u() cannot safely serve both contracts.
			if taskBillingModel == billingModelName {
				return hosttypes.PriceData{}, errors.New(i18n.T(c, i18n.MsgRelaySharedTaskPriceNeedsOverride, map[string]any{"Model": billingModelName, "Plugin": pluginKey}))
			}
		}
	}
	if info.RelayFormat == types.RelayFormatOpenAIRealtime && fixedPricing {
		return hosttypes.PriceData{}, fmt.Errorf("fixed pricing is not supported for Realtime requests")
	}

	preConsumeMultiplier, err := operation_setting.InputPreConsumeMultiplier()
	if err != nil {
		return hosttypes.PriceData{}, err
	}

	requestInput, err := ResolveIncomingBillingExprRequestInput(c, info)
	if err != nil {
		return hosttypes.PriceData{}, err
	}
	if usedVars["image_count"] {
		requestInput, err = ResolveImageBillingRequestInput(c, info, requestInput)
		if err != nil {
			return hosttypes.PriceData{}, err
		}
	}

	rawCost, trace, err := billingexpr.RunExprByHashWithRequest(exprStr, exprHash, billingexpr.TokenParams{
		P:   float64(promptTokens),
		C:   0,
		Len: float64(promptTokens),
	}, requestInput)
	if err != nil {
		return hosttypes.PriceData{}, fmt.Errorf("model %s tiered expr run failed: %w", billingModelName, err)
	}

	// Expression coefficients are $/1M tokens prices; convert to quota the same way per-call billing does.
	quotaBeforeGroup := rawCost / 1_000_000 * common.QuotaPerUnit
	// Scale the reservation, preserving the expression's context-length tier
	// and leaving actual settlement and fixed request prices unchanged.
	if trace.BillingUnit != billingexpr.BillingUnitRequest {
		quotaBeforeGroup *= preConsumeMultiplier
	}
	preConsumedQuota, err := billingexpr.QuotaRoundStrict(quotaBeforeGroup * groupRatioInfo.GroupRatio)
	if err != nil {
		return hosttypes.PriceData{}, err
	}

	freeModel := false
	if !operation_setting.GetQuotaSetting().EnableFreeModelPreConsume {
		if groupRatioInfo.GroupRatio == 0 {
			preConsumedQuota = 0
			freeModel = true
		}
	}

	snapshot := &billingexpr.BillingSnapshot{
		EstimatedImageCount:       trace.ImageCount,
		BillingMode:               billing_setting.BillingModeTieredExpr,
		ModelName:                 billingModelName,
		ExprString:                exprStr,
		ExprHash:                  exprHash,
		GroupRatio:                groupRatioInfo.GroupRatio,
		EstimatedPromptTokens:     promptTokens,
		EstimatedCompletionTokens: 0,
		PreConsumeMultiplier:      preConsumeMultiplier,
		EstimatedQuotaBeforeGroup: quotaBeforeGroup,
		EstimatedQuotaAfterGroup:  preConsumedQuota,
		EstimatedTier:             trace.MatchedTier,
		EstimatedBillingUnit:      trace.BillingUnit,
		EstimatedFixedPrice:       trace.FixedPrice,
		QuotaPerUnit:              common.QuotaPerUnit,
		ExprVersion:               billingexpr.ExprVersion(exprStr),
	}
	info.TieredBillingSnapshot = snapshot
	info.BillingRequestInput = &requestInput

	priceData := hosttypes.PriceData{
		FreeModel:         freeModel,
		GroupRatioInfo:    groupRatioInfo,
		QuotaToPreConsume: preConsumedQuota,
	}

	logger.LogDebug(c, "model_price_helper_tiered result: model=%s preConsume=%d quotaBeforeGroup=%.2f groupRatio=%.2f tier=%s", billingModelName, preConsumedQuota, quotaBeforeGroup, groupRatioInfo.GroupRatio, trace.MatchedTier)

	info.PriceData = priceData
	return priceData, nil
}
