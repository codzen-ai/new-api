package helper

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestModelPriceHelperAppliesGroupModelRatio 端到端验证分组模型倍率的注入：
// 命中时该倍率取代分组倍率（不与之叠乘），模型倍率仍取全局值，
// 预扣额度体现 modelRatio×分组模型倍率×tokens。
func TestModelPriceHelperAppliesGroupModelRatio(t *testing.T) {
	gin.SetMode(gin.TestMode)

	origModelRatio := ratio_setting.ModelRatio2JSONString()
	origGroupRatio := ratio_setting.GroupRatio2JSONString()
	origGroupModelRatio := ratio_setting.GroupModelRatio2JSONString()
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(origModelRatio))
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(origGroupRatio))
		require.NoError(t, ratio_setting.UpdateGroupModelRatioByJSONString(origGroupModelRatio))
	})

	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"gmr-test-model":10,"gmr-plain-model":10}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"gmr-vip":0.8}`))
	require.NoError(t, ratio_setting.UpdateGroupModelRatioByJSONString(`{"gmr-vip":{"gmr-test-model":0.5}}`))

	newContext := func(model string) (*gin.Context, *relaycommon.RelayInfo) {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		req.Header.Set("Content-Type", "application/json")
		ctx.Request = req
		info := &relaycommon.RelayInfo{
			OriginModelName: model,
			UserGroup:       "gmr-vip",
			UsingGroup:      "gmr-vip",
		}
		return ctx, info
	}

	t.Run("命中：取代分组倍率(0.8)，不与之叠乘", func(t *testing.T) {
		ctx, info := newContext("gmr-test-model")
		priceData, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{})
		require.NoError(t, err)

		require.Equal(t, float64(10), priceData.ModelRatio)
		require.Equal(t, 0.5, priceData.GroupRatioInfo.GroupRatio)
		// 预扣 = tokens(1000) × modelRatio(10) × 0.5 = 5000
		// 若与分组倍率叠乘则为 4000
		require.Equal(t, 5000, priceData.QuotaToPreConsume)
	})

	t.Run("未命中：回退全局倍率 × 分组倍率", func(t *testing.T) {
		ctx, info := newContext("gmr-plain-model")
		priceData, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{})
		require.NoError(t, err)

		require.Equal(t, float64(10), priceData.ModelRatio)
		require.Equal(t, float64(0.8), priceData.GroupRatioInfo.GroupRatio)
		// 预扣 = tokens(1000) × modelRatio(10) × groupRatio(0.8) = 8000
		require.Equal(t, 8000, priceData.QuotaToPreConsume)
	})
}

// TestModelPriceHelperFixedPriceAppliesGroupModelRatio 固定价（按次计费）模型与按量、
// 阶梯模型共用同一套分组模型倍率语义：系数取代分组倍率，乘在固定价上。
// 这是「配置层不需要区分模型计费方式」的最后一块，漏掉它就又回到静默失效。
func TestModelPriceHelperFixedPriceAppliesGroupModelRatio(t *testing.T) {
	gin.SetMode(gin.TestMode)

	origModelPrice := ratio_setting.ModelPrice2JSONString()
	origGroupRatio := ratio_setting.GroupRatio2JSONString()
	origGroupModelRatio := ratio_setting.GroupModelRatio2JSONString()
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(origModelPrice))
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(origGroupRatio))
		require.NoError(t, ratio_setting.UpdateGroupModelRatioByJSONString(origGroupModelRatio))
	})

	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"gmr-fixed-model":0.04,"gmr-fixed-plain":0.04}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"gmr-fixed-group":0.8}`))
	require.NoError(t, ratio_setting.UpdateGroupModelRatioByJSONString(`{"gmr-fixed-group":{"gmr-fixed-model":0.5}}`))

	newContext := func(model string) (*gin.Context, *relaycommon.RelayInfo) {
		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
		ctx.Set("group", "gmr-fixed-group")
		return ctx, &relaycommon.RelayInfo{
			OriginModelName: model,
			UserGroup:       "gmr-fixed-group",
			UsingGroup:      "gmr-fixed-group",
		}
	}

	t.Run("同步预扣：系数取代分组倍率", func(t *testing.T) {
		ctx, info := newContext("gmr-fixed-model")
		priceData, err := ModelPriceHelper(ctx, info, 0, &types.TokenCountMeta{})
		require.NoError(t, err)

		require.True(t, priceData.UsePrice)
		require.Equal(t, 0.5, priceData.GroupRatioInfo.GroupRatio)
		// 0.04 × 500000 × 0.5 = 10000；分组倍率生效则是 16000
		require.Equal(t, 10000, priceData.QuotaToPreConsume)
	})

	t.Run("同步预扣未命中：回退分组倍率", func(t *testing.T) {
		ctx, info := newContext("gmr-fixed-plain")
		priceData, err := ModelPriceHelper(ctx, info, 0, &types.TokenCountMeta{})
		require.NoError(t, err)

		require.Equal(t, 0.8, priceData.GroupRatioInfo.GroupRatio)
		require.Equal(t, 16000, priceData.QuotaToPreConsume)
	})

	t.Run("按次预扣（MJ / 任务）同样生效", func(t *testing.T) {
		ctx, info := newContext("gmr-fixed-model")
		priceData, err := ModelPriceHelperPerCall(ctx, info)
		require.NoError(t, err)

		require.True(t, priceData.UsePrice)
		require.Equal(t, 0.5, priceData.GroupRatioInfo.GroupRatio)
		require.Equal(t, 10000, priceData.Quota)
	})
}

func TestModelPriceHelperTieredUsesPreloadedRequestInput(t *testing.T) {
	gin.SetMode(gin.TestMode)

	saved := map[string]string{}
	require.NoError(t, config.GlobalConfig.SaveToDB(func(key, value string) error {
		saved[key] = value
		return nil
	}))
	t.Cleanup(func() {
		require.NoError(t, config.GlobalConfig.LoadFromDB(saved))
	})

	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		"billing_setting.billing_mode": `{"tiered-test-model":"tiered_expr"}`,
		"billing_setting.billing_expr": `{"tiered-test-model":"param(\"stream\") == true ? tier(\"stream\", p * 3) : tier(\"base\", p * 2)"}`,
	}))

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/api/channel/test/1", nil)
	req.Body = nil
	req.ContentLength = 0
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req
	ctx.Set("group", "default")

	info := &relaycommon.RelayInfo{
		OriginModelName: "tiered-test-model",
		UserGroup:       "default",
		UsingGroup:      "default",
		RequestHeaders:  map[string]string{"Content-Type": "application/json"},
		BillingRequestInput: &billingexpr.RequestInput{
			Headers: map[string]string{"Content-Type": "application/json"},
			Body:    []byte(`{"stream":true}`),
		},
	}

	priceData, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{
		BillingRatios: map[string]float64{"n": 3},
	})
	require.NoError(t, err)
	require.Equal(t, 1500, priceData.QuotaToPreConsume)
	require.NotNil(t, info.TieredBillingSnapshot)
	require.Equal(t, "stream", info.TieredBillingSnapshot.EstimatedTier)
	require.Equal(t, billing_setting.BillingModeTieredExpr, info.TieredBillingSnapshot.BillingMode)
	require.Equal(t, common.QuotaPerUnit, info.TieredBillingSnapshot.QuotaPerUnit)
}

func TestFixedPricePreConsumeAndRealtimeRejection(t *testing.T) {
	saved := map[string]string{}
	require.NoError(t, config.GlobalConfig.SaveToDB(func(key, value string) error {
		saved[key] = value
		return nil
	}))
	t.Cleanup(func() { require.NoError(t, config.GlobalConfig.LoadFromDB(saved)) })
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		"billing_setting.billing_mode":    `{"fixed-test":"tiered_expr"}`,
		"billing_setting.billing_expr":    `{"fixed-test":"len <= 32000 ? tier(\"short\", fixed(0.01)) : tier(\"long\", p * 2)"}`,
		"group_ratio_setting.group_ratio": `{"default":1.5}`,
	}))
	for _, tc := range []struct {
		name      string
		format    types.RelayFormat
		prompt    int
		wantError bool
	}{
		{"HTTP charges once", types.RelayFormatOpenAI, 0, false},
		{"Realtime rejects even unselected fixed branch", types.RelayFormatOpenAIRealtime, 50000, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			info := &relaycommon.RelayInfo{OriginModelName: "fixed-test", UserGroup: "default", UsingGroup: "default", RelayFormat: tc.format, BillingRequestInput: &billingexpr.RequestInput{}}
			price, err := ModelPriceHelper(ctx, info, tc.prompt, &types.TokenCountMeta{})
			if tc.wantError {
				require.ErrorContains(t, err, "Realtime")
				assert.Nil(t, info.TieredBillingSnapshot)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, 7500, price.QuotaToPreConsume)
			require.NotNil(t, info.TieredBillingSnapshot)
			assert.Equal(t, billingexpr.BillingUnitRequest, info.TieredBillingSnapshot.EstimatedBillingUnit)
			require.NotNil(t, info.TieredBillingSnapshot.EstimatedFixedPrice)
			assert.Equal(t, 0.01, *info.TieredBillingSnapshot.EstimatedFixedPrice)
		})
	}
}

func TestModelPriceHelperTieredInputPreConsumeMultiplier(t *testing.T) {
	gin.SetMode(gin.TestMode)

	saved := map[string]string{}
	require.NoError(t, config.GlobalConfig.SaveToDB(func(key, value string) error {
		saved[key] = value
		return nil
	}))
	t.Cleanup(func() {
		require.NoError(t, config.GlobalConfig.LoadFromDB(saved))
	})

	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		"billing_setting.billing_mode":    `{"tiered-fallback-model":"tiered_expr"}`,
		"billing_setting.billing_expr":    `{"tiered-fallback-model":"len <= 1200 ? tier(\"base\", p * 3 + c * 15) : tier(\"long\", p * 30 + c * 150)"}`,
		"group_ratio_setting.group_ratio": `{"default":1,"free":0}`,
	}))

	cases := []struct {
		name       string
		group      string
		prompt     int
		maxTokens  int
		multiplier float64
		expected   int
	}{
		{"default uses only input", "default", 1000, 0, 1, 1500},
		{"explicit output limit does not increase reservation", "default", 1000, 100, 1, 1500},
		{"fraction below one", "default", 1000, 0, 0.5, 750},
		{"fraction above one preserves context tier", "default", 1000, 0, 2.5, 3750},
		{"small input has no token floor", "default", 100, 0, 1, 150},
		{"zero input", "default", 0, 100, 1, 0},
		{"free group", "free", 1000, 0, 2.5, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			req.Header.Set("Content-Type", "application/json")
			ctx.Request = req
			ctx.Set("group", tc.group)

			info := &relaycommon.RelayInfo{
				OriginModelName: "tiered-fallback-model",
				UserGroup:       tc.group,
				UsingGroup:      tc.group,
				RequestHeaders:  map[string]string{"Content-Type": "application/json"},
				BillingRequestInput: &billingexpr.RequestInput{
					Headers: map[string]string{"Content-Type": "application/json"},
					Body:    []byte(`{}`),
				},
			}

			operation_setting.GetQuotaSetting().PreConsumeMultiplier = tc.multiplier
			priceData, err := ModelPriceHelper(ctx, info, tc.prompt, &types.TokenCountMeta{MaxTokens: tc.maxTokens})
			require.NoError(t, err)
			assert.Equal(t, tc.expected, priceData.QuotaToPreConsume)
			require.NotNil(t, info.TieredBillingSnapshot)
			actual, err := billingexpr.ComputeTieredQuotaWithRequest(info.TieredBillingSnapshot, billingexpr.TokenParams{P: 1000, C: 100, Len: 1000}, *info.BillingRequestInput)
			require.NoError(t, err)
			wantActual := 2250
			if tc.group == "free" {
				wantActual = 0
			}
			assert.Equal(t, wantActual, actual.ActualQuotaAfterGroup, "reservation multiplier must not change settlement")
		})
	}
}

func TestModelPriceHelperTieredRejectsPreConsumeOverflow(t *testing.T) {
	gin.SetMode(gin.TestMode)

	saved := map[string]string{}
	require.NoError(t, config.GlobalConfig.SaveToDB(func(key, value string) error {
		saved[key] = value
		return nil
	}))
	t.Cleanup(func() {
		require.NoError(t, config.GlobalConfig.LoadFromDB(saved))
	})

	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		"billing_setting.billing_mode":    `{"tiered-overflow-model":"tiered_expr"}`,
		"billing_setting.billing_expr":    `{"tiered-overflow-model":"tier(\"overflow\", p * 100000000000000000)"}`,
		"group_ratio_setting.group_ratio": `{"default":1}`,
	}))

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Set("group", "default")
	info := &relaycommon.RelayInfo{
		OriginModelName: "tiered-overflow-model",
		UserGroup:       "default",
		UsingGroup:      "default",
		BillingRequestInput: &billingexpr.RequestInput{
			Body: []byte(`{}`),
		},
	}

	_, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{})

	var clamp *common.QuotaClamp
	require.ErrorAs(t, err, &clamp)
	require.Equal(t, "QuotaRound", clamp.Op)
	require.Equal(t, common.QuotaClampOverflow, clamp.Kind)
}

func TestModelPriceHelperRequestBillingRatiosOnlyApplyToFixedPrice(t *testing.T) {
	gin.SetMode(gin.TestMode)
	savedModelPrices := ratio_setting.ModelPrice2JSONString()
	savedModelRatios := ratio_setting.ModelRatio2JSONString()
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(savedModelPrices))
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(savedModelRatios))
	})

	modelPrices, err := common.Marshal(map[string]float64{
		"fixed-image-price":      0.04,
		"fractional-image-price": 0.0000012,
		"overflow-image-price":   float64(common.MaxQuota) / common.QuotaPerUnit / 2,
	})
	require.NoError(t, err)
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(string(modelPrices)))
	modelRatios, err := common.Marshal(map[string]float64{"ratio-image-price": 15})
	require.NoError(t, err)
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(string(modelRatios)))

	tests := []struct {
		name           string
		model          string
		wantQuota      int
		wantUsePrice   bool
		wantImageCount bool
	}{
		{
			name:           "fixed price applies image count",
			model:          "fixed-image-price",
			wantQuota:      180000,
			wantUsePrice:   true,
			wantImageCount: true,
		},
		{
			name:         "ratio price ignores request billing ratios",
			model:        "ratio-image-price",
			wantQuota:    15000,
			wantUsePrice: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Set("group", "default")
			info := &relaycommon.RelayInfo{
				OriginModelName: tt.model,
				UserGroup:       "default",
				UsingGroup:      "default",
			}
			meta := &types.TokenCountMeta{
				ImagePriceRatio: 3,
				BillingRatios:   map[string]float64{"n": 3},
			}

			priceData, err := ModelPriceHelper(ctx, info, 1000, meta)

			require.NoError(t, err)
			require.Equal(t, tt.wantQuota, priceData.QuotaToPreConsume)
			require.Equal(t, tt.wantUsePrice, priceData.UsePrice)
			require.Equal(t, tt.wantImageCount, priceData.HasOtherRatio("n"))
			require.Equal(t, priceData.OtherRatios(), info.PriceData.OtherRatios())
		})
	}

	newInfo := func(model string) (*gin.Context, *relaycommon.RelayInfo) {
		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx.Set("group", "default")
		return ctx, &relaycommon.RelayInfo{
			OriginModelName: model,
			UserGroup:       "default",
			UsingGroup:      "default",
		}
	}
	meta := &types.TokenCountMeta{BillingRatios: map[string]float64{"n": 3}}

	ctx, info := newInfo("fractional-image-price")
	priceData, err := ModelPriceHelper(ctx, info, 0, meta)
	require.NoError(t, err)
	// 0.0000012 * 500000 * 3 = 1.8, then truncate once to 1.
	require.Equal(t, 1, priceData.QuotaToPreConsume)

	ctx, info = newInfo("overflow-image-price")
	_, err = ModelPriceHelper(ctx, info, 0, meta)
	var clamp *common.QuotaClamp
	require.ErrorAs(t, err, &clamp)
	require.Equal(t, "QuotaFromFloat", clamp.Op)
	require.Equal(t, common.QuotaClampOverflow, clamp.Kind)
	require.Nil(t, info.Billing)
}

// Pricing identity is resolved once in ModelPriceHelper via the candidate
// ladder: raw name (only when it has no @ modifiers) → canonical
// base@effort:E@thinking:S → base@thinking:S → base. Each level is looked up
// after FormatMatchingModelName wildcard normalization. A hit on the raw
// gemini-2.5-flash-thinking-* wildcard must keep the client origin as the
// consume-log name.
func TestModelPriceHelperUsesSuffixedOriginLikeMain(t *testing.T) {
	gin.SetMode(gin.TestMode)

	savedRatios := ratio_setting.ModelRatio2JSONString()
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(savedRatios))
	})
	ratios := ratio_setting.GetModelRatioCopy()
	ratios["gemini-2.5-flash"] = 0.15
	ratios["gemini-2.5-flash-thinking-*"] = 0.075
	ratioJSON, err := common.Marshal(ratios)
	require.NoError(t, err)
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(string(ratioJSON)))

	oldSelfUse := operation_setting.SelfUseModeEnabled
	operation_setting.SelfUseModeEnabled = true
	t.Cleanup(func() { operation_setting.SelfUseModeEnabled = oldSelfUse })

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Set("group", "default")

	suffixed := &relaycommon.RelayInfo{
		OriginModelName: "gemini-2.5-flash-thinking-8192",
		UserGroup:       "default",
		UsingGroup:      "default",
	}
	suffixedPrice, err := ModelPriceHelper(ctx, suffixed, 1000, &types.TokenCountMeta{})
	require.NoError(t, err)
	assert.Empty(t, suffixed.BillingModelName)
	assert.Equal(t, "gemini-2.5-flash-thinking-8192", suffixed.GetBillingModelName())
	assert.Equal(t, 0.075, suffixedPrice.ModelRatio)

	geminiSettings := model_setting.GetGeminiSettings()
	oldThinking := geminiSettings.ThinkingAdapterEnabled
	geminiSettings.ThinkingAdapterEnabled = true
	t.Cleanup(func() { geminiSettings.ThinkingAdapterEnabled = oldThinking })

	adapterOn := &relaycommon.RelayInfo{
		OriginModelName: "gemini-2.5-flash-thinking-8192",
		UserGroup:       "default",
		UsingGroup:      "default",
	}
	adapterOnPrice, err := ModelPriceHelper(ctx, adapterOn, 1000, &types.TokenCountMeta{})
	require.NoError(t, err)
	assert.Empty(t, adapterOn.BillingModelName)
	assert.Equal(t, "gemini-2.5-flash-thinking-8192", adapterOn.GetBillingModelName())
	assert.Equal(t, 0.075, adapterOnPrice.ModelRatio)

	base := &relaycommon.RelayInfo{
		OriginModelName: "gemini-2.5-flash",
		UserGroup:       "default",
		UsingGroup:      "default",
	}
	basePrice, err := ModelPriceHelper(ctx, base, 1000, &types.TokenCountMeta{})
	require.NoError(t, err)
	assert.Empty(t, base.BillingModelName)
	assert.Equal(t, "gemini-2.5-flash", base.GetBillingModelName())
	assert.Equal(t, 0.15, basePrice.ModelRatio)
}

func TestModelPriceHelperHonorsCustomClaudeThinkingAlias(t *testing.T) {
	gin.SetMode(gin.TestMode)

	savedRatios := ratio_setting.ModelRatio2JSONString()
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(savedRatios))
	})
	ratios := ratio_setting.GetModelRatioCopy()
	ratios["claude-3-7-sonnet"] = 1.5
	ratios["claude-3-7-sonnet-thinking"] = 3.0
	ratioJSON, err := common.Marshal(ratios)
	require.NoError(t, err)
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(string(ratioJSON)))

	oldSelfUse := operation_setting.SelfUseModeEnabled
	operation_setting.SelfUseModeEnabled = false
	t.Cleanup(func() { operation_setting.SelfUseModeEnabled = oldSelfUse })

	claudeSettings := model_setting.GetClaudeSettings()
	oldThinking := claudeSettings.ThinkingAdapterEnabled
	claudeSettings.ThinkingAdapterEnabled = true
	t.Cleanup(func() { claudeSettings.ThinkingAdapterEnabled = oldThinking })

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Set("group", "default")
	info := &relaycommon.RelayInfo{
		OriginModelName: "claude-3-7-sonnet-thinking",
		UserGroup:       "default",
		UsingGroup:      "default",
	}
	priceData, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{})
	require.NoError(t, err)
	assert.Empty(t, info.BillingModelName)
	assert.Equal(t, "claude-3-7-sonnet-thinking", info.GetBillingModelName())
	assert.Equal(t, 3.0, priceData.ModelRatio)
}

func TestModelPriceHelperCanonicalBillingLadder(t *testing.T) {
	gin.SetMode(gin.TestMode)

	savedRatios := ratio_setting.ModelRatio2JSONString()
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(savedRatios))
	})
	oldSelfUse := operation_setting.SelfUseModeEnabled
	operation_setting.SelfUseModeEnabled = false
	t.Cleanup(func() { operation_setting.SelfUseModeEnabled = oldSelfUse })

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Set("group", "default")

	t.Run("level2 full form", func(t *testing.T) {
		ratios := ratio_setting.GetModelRatioCopy()
		delete(ratios, "qwen3-max")
		ratios["qwen3-max@effort:high@thinking:on"] = 4.0
		ratios["qwen3-max@thinking:on"] = 3.0
		ratioJSON, err := common.Marshal(ratios)
		require.NoError(t, err)
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(string(ratioJSON)))

		info := &relaycommon.RelayInfo{
			OriginModelName: "qwen3-max@thinking:on@effort:high@temperature:0.2",
			UserGroup:       "default",
			UsingGroup:      "default",
		}
		priceData, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{})
		require.NoError(t, err)
		assert.Equal(t, "qwen3-max@effort:high@thinking:on", info.BillingModelName)
		assert.Equal(t, 4.0, priceData.ModelRatio)
	})

	t.Run("level3 thinking form shuffled budget", func(t *testing.T) {
		ratios := ratio_setting.GetModelRatioCopy()
		delete(ratios, "qwen3-max")
		delete(ratios, "qwen3-max@effort:high@thinking:on")
		ratios["qwen3-max@thinking:on"] = 3.0
		ratioJSON, err := common.Marshal(ratios)
		require.NoError(t, err)
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(string(ratioJSON)))

		info := &relaycommon.RelayInfo{
			OriginModelName: "qwen3-max@temperature:0.3@thinking:8192",
			UserGroup:       "default",
			UsingGroup:      "default",
		}
		priceData, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{})
		require.NoError(t, err)
		assert.Equal(t, "qwen3-max@thinking:on", info.BillingModelName)
		assert.Equal(t, 3.0, priceData.ModelRatio)
	})

	t.Run("level4 base fallback", func(t *testing.T) {
		ratios := ratio_setting.GetModelRatioCopy()
		delete(ratios, "qwen3-max@thinking:on")
		delete(ratios, "qwen3-max@effort:high@thinking:on")
		ratios["qwen3-max"] = 1.25
		ratioJSON, err := common.Marshal(ratios)
		require.NoError(t, err)
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(string(ratioJSON)))

		info := &relaycommon.RelayInfo{
			OriginModelName: "qwen3-max@thinking:off",
			UserGroup:       "default",
			UsingGroup:      "default",
		}
		priceData, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{})
		require.NoError(t, err)
		assert.Equal(t, "qwen3-max", info.BillingModelName)
		assert.Equal(t, 1.25, priceData.ModelRatio)
	})

	t.Run("thinking minus one bills as on", func(t *testing.T) {
		ratios := ratio_setting.GetModelRatioCopy()
		ratios["qwen3-max@thinking:on"] = 3.0
		ratioJSON, err := common.Marshal(ratios)
		require.NoError(t, err)
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(string(ratioJSON)))

		info := &relaycommon.RelayInfo{
			OriginModelName: "qwen3-max@thinking:-1",
			UserGroup:       "default",
			UsingGroup:      "default",
		}
		priceData, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{})
		require.NoError(t, err)
		assert.Equal(t, "qwen3-max@thinking:on", info.BillingModelName)
		assert.Equal(t, 3.0, priceData.ModelRatio)
	})
}

func TestModelPriceHelperMigratesLegacyGeminiWildcardToCanonical(t *testing.T) {
	gin.SetMode(gin.TestMode)

	savedRatios := ratio_setting.ModelRatio2JSONString()
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(savedRatios))
	})
	ratios := ratio_setting.GetModelRatioCopy()
	delete(ratios, "gemini-2.5-flash-thinking-*")
	ratios["gemini-2.5-flash"] = 0.15
	ratios["gemini-2.5-flash@thinking:on"] = 0.09
	ratioJSON, err := common.Marshal(ratios)
	require.NoError(t, err)
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(string(ratioJSON)))

	oldSelfUse := operation_setting.SelfUseModeEnabled
	operation_setting.SelfUseModeEnabled = false
	t.Cleanup(func() { operation_setting.SelfUseModeEnabled = oldSelfUse })

	geminiSettings := model_setting.GetGeminiSettings()
	oldThinking := geminiSettings.ThinkingAdapterEnabled
	geminiSettings.ThinkingAdapterEnabled = true
	t.Cleanup(func() { geminiSettings.ThinkingAdapterEnabled = oldThinking })

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Set("group", "default")
	info := &relaycommon.RelayInfo{
		OriginModelName: "gemini-2.5-flash-thinking-8192",
		UserGroup:       "default",
		UsingGroup:      "default",
	}
	priceData, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{})
	require.NoError(t, err)
	assert.Equal(t, "gemini-2.5-flash@thinking:on", info.BillingModelName)
	assert.Equal(t, 0.09, priceData.ModelRatio)
}

func TestModelPriceHelperModifierNameFallsBackToBase(t *testing.T) {
	gin.SetMode(gin.TestMode)

	savedRatios := ratio_setting.ModelRatio2JSONString()
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(savedRatios))
	})
	ratios := ratio_setting.GetModelRatioCopy()
	ratios["qwen3.8-max"] = 2.0
	ratioJSON, err := common.Marshal(ratios)
	require.NoError(t, err)
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(string(ratioJSON)))

	oldSelfUse := operation_setting.SelfUseModeEnabled
	operation_setting.SelfUseModeEnabled = false
	t.Cleanup(func() { operation_setting.SelfUseModeEnabled = oldSelfUse })

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Set("group", "default")
	info := &relaycommon.RelayInfo{
		OriginModelName: "qwen3.8-max@thinking:on@temperature:0.2",
		UserGroup:       "default",
		UsingGroup:      "default",
	}
	priceData, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{})
	require.NoError(t, err)
	assert.Equal(t, "qwen3.8-max", info.BillingModelName)
	assert.Equal(t, 2.0, priceData.ModelRatio)
}

func TestModelPriceHelperExemptAtNameBillsVerbatim(t *testing.T) {
	gin.SetMode(gin.TestMode)

	settings := model_setting.GetGlobalSettings()
	originalBlacklist := append([]string(nil), settings.ThinkingModelBlacklist...)
	t.Cleanup(func() { settings.ThinkingModelBlacklist = originalBlacklist })
	settings.ThinkingModelBlacklist = append(originalBlacklist, "re:.*@sha256:.*")

	savedRatios := ratio_setting.ModelRatio2JSONString()
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(savedRatios))
	})
	ratios := ratio_setting.GetModelRatioCopy()
	ratios["opaque"] = 1.0
	ratios["opaque@sha256:deadbeef"] = 7.0
	ratioJSON, err := common.Marshal(ratios)
	require.NoError(t, err)
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(string(ratioJSON)))

	oldSelfUse := operation_setting.SelfUseModeEnabled
	operation_setting.SelfUseModeEnabled = false
	t.Cleanup(func() { operation_setting.SelfUseModeEnabled = oldSelfUse })

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Set("group", "default")
	info := &relaycommon.RelayInfo{
		OriginModelName: "opaque@sha256:deadbeef",
		UserGroup:       "default",
		UsingGroup:      "default",
	}
	priceData, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{})
	require.NoError(t, err)
	assert.Empty(t, info.BillingModelName)
	assert.Equal(t, "opaque@sha256:deadbeef", info.GetBillingModelName())
	assert.Equal(t, 7.0, priceData.ModelRatio)
}

func TestModelPriceHelperPreservesGpt51CodexMaxIdentity(t *testing.T) {
	gin.SetMode(gin.TestMode)

	savedRatios := ratio_setting.ModelRatio2JSONString()
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(savedRatios))
	})
	ratios := ratio_setting.GetModelRatioCopy()
	ratios["gpt-5.1-codex-max"] = 1.75
	ratios["gpt-5.1-codex"] = 9.9
	ratioJSON, err := common.Marshal(ratios)
	require.NoError(t, err)
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(string(ratioJSON)))

	oldSelfUse := operation_setting.SelfUseModeEnabled
	operation_setting.SelfUseModeEnabled = false
	t.Cleanup(func() { operation_setting.SelfUseModeEnabled = oldSelfUse })

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Set("group", "default")
	info := &relaycommon.RelayInfo{
		OriginModelName: "gpt-5.1-codex-max",
		UserGroup:       "default",
		UsingGroup:      "default",
	}
	priceData, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{})
	require.NoError(t, err)
	assert.Empty(t, info.BillingModelName)
	assert.Equal(t, "gpt-5.1-codex-max", info.GetBillingModelName())
	assert.Equal(t, 1.75, priceData.ModelRatio)
}

// DeepSeek thinking tails and @ modifiers bill at the base model's built-in
// expression; a name on the thinking-suffix blacklist stays opaque and needs
// its own price.
func TestModelPriceHelperDeepSeekThinkingTailsBillAtBase(t *testing.T) {
	gin.SetMode(gin.TestMode)
	require.NoError(t, i18n.Init())
	saved := map[string]string{}
	require.NoError(t, config.GlobalConfig.SaveToDB(func(key, value string) error {
		saved[key] = value
		return nil
	}))
	t.Cleanup(func() { require.NoError(t, config.GlobalConfig.LoadFromDB(saved)) })
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		"billing_setting.billing_mode": `{}`,
		"billing_setting.billing_expr": `{}`,
	}))
	oldSelfUse := operation_setting.SelfUseModeEnabled
	operation_setting.SelfUseModeEnabled = false
	t.Cleanup(func() { operation_setting.SelfUseModeEnabled = oldSelfUse })
	settings := model_setting.GetGlobalSettings()
	originalBlacklist := append([]string(nil), settings.ThinkingModelBlacklist...)
	t.Cleanup(func() { settings.ThinkingModelBlacklist = originalBlacklist })
	settings.ThinkingModelBlacklist = append(originalBlacklist, "deepseek-v4-pro-max")

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Set("group", "default")
	for origin, base := range map[string]string{
		"deepseek-flash-none":           "deepseek-flash",
		"deepseek-flash-low":            "deepseek-flash",
		"deepseek-flash-max":            "deepseek-flash",
		"deepseek-v4-pro-low":           "deepseek-v4-pro",
		"deepseek-flash@effort:max":     "deepseek-flash",
		"deepseek-flash-max@effort:low": "deepseek-flash",
		"deepseek-v4-pro@thinking:off":  "deepseek-v4-pro",
	} {
		info := &relaycommon.RelayInfo{
			OriginModelName: origin, UserGroup: "default", UsingGroup: "default",
			BillingRequestInput: &billingexpr.RequestInput{Body: []byte(`{}`)},
		}
		_, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{})
		require.NoError(t, err, origin)
		assert.Equal(t, base, info.BillingModelName, origin)
		expression, _ := billing_setting.GetBuiltinBillingExpr(base)
		require.NotNil(t, info.TieredBillingSnapshot, origin)
		assert.Equal(t, expression, info.TieredBillingSnapshot.ExprString, origin)
	}

	info := &relaycommon.RelayInfo{OriginModelName: "deepseek-v4-pro-max", UserGroup: "default", UsingGroup: "default"}
	_, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{})
	require.Error(t, err)
	assert.Equal(t, "deepseek-v4-pro-max", info.GetBillingModelName())
}

func TestModelPriceHelperNativeGeminiNoThinkingDoesNotAliasBillingModel(t *testing.T) {
	gin.SetMode(gin.TestMode)

	savedRatios := ratio_setting.ModelRatio2JSONString()
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(savedRatios))
	})
	ratios := ratio_setting.GetModelRatioCopy()
	ratios["gemini-3-pro"] = 1.25
	ratioJSON, err := common.Marshal(ratios)
	require.NoError(t, err)
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(string(ratioJSON)))

	oldSelfUse := operation_setting.SelfUseModeEnabled
	operation_setting.SelfUseModeEnabled = true
	t.Cleanup(func() { operation_setting.SelfUseModeEnabled = oldSelfUse })

	geminiSettings := model_setting.GetGeminiSettings()
	oldThinking := geminiSettings.ThinkingAdapterEnabled
	geminiSettings.ThinkingAdapterEnabled = true
	t.Cleanup(func() { geminiSettings.ThinkingAdapterEnabled = oldThinking })

	budget := 0
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Set("group", "default")
	info := &relaycommon.RelayInfo{
		OriginModelName: "gemini-3-pro",
		UserGroup:       "default",
		UsingGroup:      "default",
		Request: &dto.GeminiChatRequest{
			GenerationConfig: dto.GeminiChatGenerationConfig{
				ThinkingConfig: &dto.GeminiThinkingConfig{
					ThinkingBudget: &budget,
				},
			},
		},
	}

	priceData, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{})
	require.NoError(t, err)
	assert.Empty(t, info.BillingModelName)
	assert.Equal(t, "gemini-3-pro", info.GetBillingModelName())
	assert.Equal(t, 1.25, priceData.ModelRatio)
	assert.NotEqual(t, 37.5, priceData.ModelRatio)
}

func TestInputPreConsumeMultiplierLegacyAndRequestPrices(t *testing.T) {
	previous := config.GlobalConfig.ExportAllConfigs()
	previousRatios, previousPrices := ratio_setting.ModelRatio2JSONString(), ratio_setting.ModelPrice2JSONString()
	t.Cleanup(func() {
		require.NoError(t, config.GlobalConfig.LoadFromDB(previous))
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(previousRatios))
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(previousPrices))
	})
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"legacy-input-policy":1.5}`))
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"request-input-policy":0.01}`))
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		"billing_setting.billing_mode":    `{"fixed-input-policy":"tiered_expr","image-input-policy":"tiered_expr"}`,
		"billing_setting.billing_expr":    `{"fixed-input-policy":"tier(\"request\", fixed(0.01))","image-input-policy":"tier(\"image\", p * 3) * image_count"}`,
		"group_ratio_setting.group_ratio": `{"default":1}`,
	}))
	for _, tc := range []struct {
		model        string
		multiplier   float64
		prompt, want int
	}{
		{"legacy-input-policy", 0.5, 100, 75},
		{"legacy-input-policy", 2.5, 100, 375},
		{"legacy-input-policy", 1, 0, 0},
		{"request-input-policy", 2.5, 100, 5000},
		{"fixed-input-policy", 2.5, 100, 5000},
		{"image-input-policy", 2.5, 100, 375},
	} {
		t.Run(tc.model+"/"+fmt.Sprint(tc.multiplier, "/", tc.prompt), func(t *testing.T) {
			operation_setting.GetQuotaSetting().PreConsumeMultiplier = tc.multiplier
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			info := &relaycommon.RelayInfo{OriginModelName: tc.model, UserGroup: "default", UsingGroup: "default", BillingRequestInput: &billingexpr.RequestInput{}}
			price, err := ModelPriceHelper(ctx, info, tc.prompt, &types.TokenCountMeta{MaxTokens: 10000})
			require.NoError(t, err)
			assert.Equal(t, tc.want, price.QuotaToPreConsume)
			if tc.model == "image-input-policy" {
				reservation := &priceTestReservation{held: price.QuotaToPreConsume}
				info.Billing = reservation
				operation_setting.GetQuotaSetting().PreConsumeMultiplier = 10
				require.Nil(t, service.PrepareImageBillingForRequest(ctx, info, 2))
				assert.Equal(t, 750, reservation.held, "retry must reuse the original fractional multiplier")
			}
		})
	}
}

// priceTestReservation observes the reservation requested before image submission.
type priceTestReservation struct{ held int }

func (s *priceTestReservation) Settle(int) error         { return nil }
func (s *priceTestReservation) Refund(*gin.Context)      {}
func (s *priceTestReservation) NeedsRefund() bool        { return false }
func (s *priceTestReservation) GetPreConsumedQuota() int { return s.held }
func (s *priceTestReservation) Reserve(quota int) error  { s.held = max(s.held, quota); return nil }

func (s *priceTestReservation) ReserveBeforeSubmit(quota int) error { return s.Reserve(quota) }

// TestHasModelBillingConfigIgnoresGroupModelRatio 保证分组模型倍率不给模型定价：
// 它只取代分组倍率，所以没有全局倍率的模型不能因为配了分组模型倍率就被判为可计费，
// 否则它会进入模型列表，实际调用时却因为没有价格而报错。
func TestHasModelBillingConfigIgnoresGroupModelRatio(t *testing.T) {
	origModelRatio := ratio_setting.ModelRatio2JSONString()
	origModelPrice := ratio_setting.ModelPrice2JSONString()
	origGroupModelRatio := ratio_setting.GroupModelRatio2JSONString()
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(origModelRatio))
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(origModelPrice))
		require.NoError(t, ratio_setting.UpdateGroupModelRatioByJSONString(origGroupModelRatio))
	})

	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"hmbc-global-model":10}`))
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{}`))
	require.NoError(t, ratio_setting.UpdateGroupModelRatioByJSONString(`{"hmbc-vip":{"hmbc-ratio-only-model":0.5}}`))

	cases := []struct {
		name  string
		model string
		want  bool
	}{
		{"有全局倍率", "hmbc-global-model", true},
		{"只有分组模型倍率，没有全局倍率", "hmbc-ratio-only-model", false},
		{"什么都没配", "hmbc-unconfigured", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, HasModelBillingConfig(tc.model))
		})
	}
}

// TestHasModelBillingConfigTieredNeedsExpression 保证表达式计费的模型只由表达式定价：
// 一个 billing_mode=tiered_expr 但没有表达式的模型，不能因为倍率类配置就被判为可计费，
// 否则它会进入模型列表，实际调用时在 modelPriceHelperTiered 报错。
func TestHasModelBillingConfigTieredNeedsExpression(t *testing.T) {
	saved := map[string]string{}
	require.NoError(t, config.GlobalConfig.SaveToDB(func(key, value string) error {
		saved[key] = value
		return nil
	}))
	t.Cleanup(func() {
		require.NoError(t, config.GlobalConfig.LoadFromDB(saved))
	})

	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		"billing_setting.billing_mode":          `{"hmbc-tiered-ok":"tiered_expr","hmbc-tiered-no-expr":"tiered_expr"}`,
		"billing_setting.billing_expr":          `{"hmbc-tiered-ok":"tier(\"base\", p * 3)","hmbc-tiered-no-expr":"  "}`,
		"group_ratio_setting.group_model_ratio": `{"hmbc-vip":{"hmbc-tiered-no-expr":0.5}}`,
	}))
	origModelRatio := ratio_setting.ModelRatio2JSONString()
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(origModelRatio))
	})
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"hmbc-tiered-no-expr":10}`))

	assert.True(t, HasModelBillingConfig("hmbc-tiered-ok"))
	assert.False(t, HasModelBillingConfig("hmbc-tiered-no-expr"),
		"没有表达式的 tiered_expr 模型不可计费，全局倍率和分组模型倍率都不能让它变成可计费")
}

// TestModelPriceHelperTieredAppliesGroupModelRatio 端到端验证阶梯（tiered_expr���模型
// 与按量倍率模型共用同一套分组模型倍率语义：命中时该倍率取代分组倍率（含分组间覆盖），
// 表达式本身照常按阶梯计算。
func TestModelPriceHelperTieredAppliesGroupModelRatio(t *testing.T) {
	gin.SetMode(gin.TestMode)

	saved := map[string]string{}
	require.NoError(t, config.GlobalConfig.SaveToDB(func(key, value string) error {
		saved[key] = value
		return nil
	}))
	t.Cleanup(func() {
		require.NoError(t, config.GlobalConfig.LoadFromDB(saved))
	})

	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		"billing_setting.billing_mode":          `{"gmer-model":"tiered_expr","gmer-plain-model":"tiered_expr"}`,
		"billing_setting.billing_expr":          `{"gmer-model":"tier(\"base\", p * 3 + c * 15)","gmer-plain-model":"tier(\"base\", p * 3 + c * 15)"}`,
		"group_ratio_setting.group_ratio":       `{"gmer-vip":0.8,"gmer-special":0.8}`,
		"group_ratio_setting.group_group_ratio": `{"gmer-special":{"gmer-special":0.1}}`,
		"group_ratio_setting.group_model_ratio": `{"gmer-vip":{"gmer-model":0.5},"gmer-special":{"gmer-model":0.5}}`,
	}))

	// 预扣只按输入估算（c=0，默认预扣系数 1）：1000*3 = 3000 → /1e6 * 500000 = 1500（分组倍率前）
	const quotaBeforeGroup = 1500

	newContext := func(model, group string) (*gin.Context, *relaycommon.RelayInfo) {
		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		ctx.Set("group", group)
		return ctx, &relaycommon.RelayInfo{
			OriginModelName: model,
			UserGroup:       group,
			UsingGroup:      group,
			BillingRequestInput: &billingexpr.RequestInput{
				Body: []byte(`{}`),
			},
		}
	}

	t.Run("命中：倍率取代分组倍率，阶梯结构不变", func(t *testing.T) {
		ctx, info := newContext("gmer-model", "gmer-vip")
		priceData, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{MaxTokens: 100})
		require.NoError(t, err)

		assert.Equal(t, 0.5, priceData.GroupRatioInfo.GroupRatio)
		// 预扣 = 1500 × 0.5 = 750；若分组倍率仍叠加则为 600
		assert.Equal(t, quotaBeforeGroup/2, priceData.QuotaToPreConsume)
		require.NotNil(t, info.TieredBillingSnapshot)
		assert.Equal(t, 0.5, info.TieredBillingSnapshot.GroupRatio)
		assert.Equal(t, float64(quotaBeforeGroup), info.TieredBillingSnapshot.EstimatedQuotaBeforeGroup)
	})

	t.Run("未命中：回退分组倍率", func(t *testing.T) {
		ctx, info := newContext("gmer-plain-model", "gmer-vip")
		priceData, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{MaxTokens: 100})
		require.NoError(t, err)

		assert.Equal(t, 0.8, priceData.GroupRatioInfo.GroupRatio)
		assert.Equal(t, 1200, priceData.QuotaToPreConsume)
	})

	t.Run("命中时分组间覆盖也失效", func(t *testing.T) {
		ctx, info := newContext("gmer-model", "gmer-special")
		priceData, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{MaxTokens: 100})
		require.NoError(t, err)

		// 分组间覆盖 0.1 不参与，最终仍是 0.5
		assert.Equal(t, 0.5, priceData.GroupRatioInfo.GroupRatio)
		assert.False(t, priceData.GroupRatioInfo.HasSpecialRatio)
		assert.Equal(t, quotaBeforeGroup/2, priceData.QuotaToPreConsume)
	})
}

// TestRefreshGroupPricingFollowsSelectedGroup 锁定 auto 分组重试的计费正确性：
// 计费分组换掉之后，分组模型倍率必须跟着新分组重新解析，不能留着旧分组的值，
// 也不能让新分组的分组倍率把它顶掉。
func TestRefreshGroupPricingFollowsSelectedGroup(t *testing.T) {
	gin.SetMode(gin.TestMode)

	saved := map[string]string{}
	require.NoError(t, config.GlobalConfig.SaveToDB(func(key, value string) error {
		saved[key] = value
		return nil
	}))
	t.Cleanup(func() {
		require.NoError(t, config.GlobalConfig.LoadFromDB(saved))
	})

	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		"billing_setting.billing_mode":          `{"rgp-expr-model":"tiered_expr"}`,
		"billing_setting.billing_expr":          `{"rgp-expr-model":"tier(\"base\", p * 3 + c * 15)"}`,
		"group_ratio_setting.group_ratio":       `{"rgp-a":0.8,"rgp-b":0.5,"rgp-c":0.9}`,
		"group_ratio_setting.group_model_ratio": `{"rgp-a":{"rgp-ratio-model":0.5,"rgp-expr-model":0.2},"rgp-b":{"rgp-ratio-model":0.3}}`,
	}))

	origModelRatio := ratio_setting.ModelRatio2JSONString()
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(origModelRatio))
	})
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"rgp-ratio-model":10}`))

	newContext := func(model, group string) (*gin.Context, *relaycommon.RelayInfo) {
		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		ctx.Set("group", group)
		ctx.Set("auto_group", group)
		return ctx, &relaycommon.RelayInfo{
			OriginModelName: model,
			UserGroup:       group,
			UsingGroup:      group,
			BillingRequestInput: &billingexpr.RequestInput{
				Body: []byte(`{}`),
			},
		}
	}

	t.Run("按量倍率：换组后按新分组的分组模型倍率计费", func(t *testing.T) {
		ctx, info := newContext("rgp-ratio-model", "rgp-a")
		priceData, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{})
		require.NoError(t, err)
		require.Equal(t, float64(10), priceData.ModelRatio)
		require.Equal(t, 0.5, priceData.GroupRatioInfo.GroupRatio)

		// auto 分组重试切到 rgp-b（分组模型倍率 0.3）
		ctx.Set("auto_group", "rgp-b")
		RefreshGroupPricingForSelectedGroup(ctx, info)

		assert.Equal(t, "rgp-b", info.UsingGroup)
		assert.Equal(t, float64(10), info.PriceData.ModelRatio)
		// 取 0.3 而不是 rgp-a 的 0.5，也不是 rgp-b 的分组倍率 0.5
		assert.Equal(t, 0.3, info.PriceData.GroupRatioInfo.GroupRatio)
	})

	t.Run("按量倍率：换到没有分组模型倍率的分组时回退分组倍率", func(t *testing.T) {
		ctx, info := newContext("rgp-ratio-model", "rgp-a")
		_, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{})
		require.NoError(t, err)

		ctx.Set("auto_group", "rgp-c")
		RefreshGroupPricingForSelectedGroup(ctx, info)

		assert.Equal(t, float64(10), info.PriceData.ModelRatio)
		assert.Equal(t, 0.9, info.PriceData.GroupRatioInfo.GroupRatio)
	})

	t.Run("阶梯计费：换到没有分组模型倍率的分组时回退分组倍率", func(t *testing.T) {
		ctx, info := newContext("rgp-expr-model", "rgp-a")
		priceData, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{MaxTokens: 100})
		require.NoError(t, err)
		require.Equal(t, 0.2, priceData.GroupRatioInfo.GroupRatio)

		ctx.Set("auto_group", "rgp-b")
		RefreshGroupPricingForSelectedGroup(ctx, info)

		assert.Equal(t, 0.5, info.PriceData.GroupRatioInfo.GroupRatio)
	})
}
