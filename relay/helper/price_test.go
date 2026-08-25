package helper

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/config"
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

func TestModelPriceHelperTieredPreConsumeMaxTokensFallback(t *testing.T) {
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
		"billing_setting.billing_expr":    `{"tiered-fallback-model":"tier(\"base\", p * 3 + c * 15)"}`,
		"group_ratio_setting.group_ratio": `{"default":1,"free":0}`,
	}))

	const promptTokens = 1000

	cases := []struct {
		name      string
		group     string
		maxTokens int
		expected  int
	}{
		{
			// max_tokens omitted in a paid group -> fall back to 8192 completion tokens.
			// p*3 + c*15 = 1000*3 + 8192*15 = 125880 -> /1e6 * 500000 = 62940
			name:      "non-free group falls back to 8192 completion tokens",
			group:     "default",
			maxTokens: 0,
			expected:  62940,
		},
		{
			// explicit max_tokens is used verbatim, no fallback.
			// 1000*3 + 100*15 = 4500 -> /1e6 * 500000 = 2250
			name:      "explicit max_tokens is used verbatim",
			group:     "default",
			maxTokens: 100,
			expected:  2250,
		},
		{
			// free group (ratio 0) stays zero; fallback is gated on non-zero group ratio.
			name:      "free group stays zero without fallback",
			group:     "free",
			maxTokens: 0,
			expected:  0,
		},
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

			priceData, err := ModelPriceHelper(ctx, info, promptTokens, &types.TokenCountMeta{MaxTokens: tc.maxTokens})
			require.NoError(t, err)
			require.Equal(t, tc.expected, priceData.QuotaToPreConsume)
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

	// 表达式在 p=1000 / c=100 下算出 1000*3 + 100*15 = 4500 → /1e6 * 500000 = 2250（分组倍率前）
	const quotaBeforeGroup = 2250

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
		// 预扣 = 2250 × 0.5 = 1125；若分组倍率仍叠加则为 900
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
		assert.Equal(t, 1800, priceData.QuotaToPreConsume)
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
