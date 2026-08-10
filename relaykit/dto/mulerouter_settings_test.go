package dto

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func floatPtr(v float64) *float64 { return &v }

// wan2.2-i2v-spicy: upstream charges $0.02/s at 480p and $0.04/s at 720p, so a
// base price of one 5s/480p clip times these multipliers must reproduce the
// upstream price exactly for every accepted combination.
func videoRoute() MuleRouterRoute {
	return MuleRouterRoute{
		Vendor: "carrothub",
		Model:  "wan2.2-i2v-spicy",
		BillingVars: []MuleRouterBillingVar{
			{Name: "seconds", Source: "duration", Kind: MuleRouterVarKindInt, Enum: []float64{5, 8}, Default: "5", Divisor: 5},
			{Name: "resolution", Source: "resolution", Kind: MuleRouterVarKindEnum, Values: map[string]float64{"480p": 1, "720p": 2}, Default: "480p"},
		},
	}
}

func TestEvaluateBillingMultipliers(t *testing.T) {
	route := videoRoute()

	testCases := []struct {
		name   string
		params map[string]any
		want   map[string]float64
	}{
		{
			name:   "defaults apply when fields are omitted",
			params: map[string]any{"prompt": "a", "image": "https://example.com/a.png"},
			want:   map[string]float64{"seconds": 1, "resolution": 1},
		},
		{
			name:   "longest and largest accepted combination",
			params: map[string]any{"duration": float64(8), "resolution": "720p"},
			want:   map[string]float64{"seconds": 1.6, "resolution": 2},
		},
		{
			name:   "resolution alone",
			params: map[string]any{"resolution": "720p"},
			want:   map[string]float64{"seconds": 1, "resolution": 2},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ratios, err := route.EvaluateBilling(tc.params)
			require.NoError(t, err)
			assert.Equal(t, tc.want, ratios)
		})
	}
}

func TestEvaluateBillingRejectsOutOfRangeValues(t *testing.T) {
	route := videoRoute()

	testCases := []struct {
		name   string
		params map[string]any
	}{
		{name: "duration outside the enum", params: map[string]any{"duration": float64(9)}},
		{name: "duration far above the enum", params: map[string]any{"duration": float64(100000)}},
		{name: "negative duration", params: map[string]any{"duration": float64(-5)}},
		{name: "duration as a huge unsigned-looking number", params: map[string]any{"duration": float64(18446744073686646784)}},
		{name: "unknown resolution", params: map[string]any{"resolution": "4k"}},
		{name: "duration is not a number", params: map[string]any{"duration": "eight"}},
		{name: "duration is fractional", params: map[string]any{"duration": 5.5}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ratios, err := route.EvaluateBilling(tc.params)
			require.Error(t, err)
			assert.Nil(t, ratios)
		})
	}
}

// An upstream pricing dimension the administrator has not configured must not
// reach quota calculation as an unbounded multiplier.
func TestEvaluateBillingRejectsUndeclaredCostParams(t *testing.T) {
	route := MuleRouterRoute{Vendor: "carrothub", Model: "z-image-spicy"}

	_, err := route.EvaluateBilling(map[string]any{"prompt": "a", "n": float64(9999)})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "n")

	// Fields that do not scale cost stay pass-through.
	ratios, err := route.EvaluateBilling(map[string]any{"prompt": "a", "seed": float64(0), "negative_prompt": "b"})
	require.NoError(t, err)
	assert.Empty(t, ratios)
}

func TestEvaluateBillingValidatesOnlyOnce(t *testing.T) {
	// A declared field is bounds-checked, not treated as an undeclared cost
	// param, no matter which name it is sourced from.
	route := MuleRouterRoute{
		Vendor: "carrothub",
		Model:  "z-image-spicy",
		BillingVars: []MuleRouterBillingVar{
			{Name: "width", Kind: MuleRouterVarKindInt, Min: floatPtr(256), Max: floatPtr(1536), Default: "1024"},
		},
	}

	ratios, err := route.EvaluateBilling(map[string]any{"width": float64(1536)})
	require.NoError(t, err)
	assert.Empty(t, ratios, "divisor 0 means bounds-checked but not priced")

	_, err = route.EvaluateBilling(map[string]any{"width": float64(4096)})
	require.Error(t, err)
}

func TestMuleRouterConfigValidate(t *testing.T) {
	testCases := []struct {
		name    string
		config  MuleRouterConfig
		wantErr string
	}{
		{
			name:   "valid video route",
			config: MuleRouterConfig{Routes: []MuleRouterRoute{videoRoute()}},
		},
		{
			name:    "no routes",
			config:  MuleRouterConfig{},
			wantErr: "at least one route",
		},
		{
			name: "duplicate model names",
			config: MuleRouterConfig{Routes: []MuleRouterRoute{
				{Vendor: "carrothub", Model: "z-image-spicy"},
				{Vendor: "carrothub", Model: "z-image-spicy", Action: "generation"},
			}},
			wantErr: "duplicates the model",
		},
		{
			name: "model name containing a slash would break path parsing",
			config: MuleRouterConfig{Routes: []MuleRouterRoute{
				{Vendor: "carrothub", Model: "z-image/spicy"},
			}},
			wantErr: "must not contain",
		},
		{
			name: "multiplier without an upper bound",
			config: MuleRouterConfig{Routes: []MuleRouterRoute{{
				Vendor: "carrothub", Model: "wan2.2-i2v-spicy",
				BillingVars: []MuleRouterBillingVar{
					{Name: "seconds", Source: "duration", Kind: MuleRouterVarKindInt, Min: floatPtr(1), Default: "5", Divisor: 5},
				},
			}}},
			wantErr: "must declare max or enum",
		},
		{
			name: "default outside its own bounds",
			config: MuleRouterConfig{Routes: []MuleRouterRoute{{
				Vendor: "carrothub", Model: "wan2.2-i2v-spicy",
				BillingVars: []MuleRouterBillingVar{
					{Name: "seconds", Source: "duration", Kind: MuleRouterVarKindInt, Enum: []float64{5, 8}, Default: "6", Divisor: 5},
				},
			}}},
			wantErr: "default is invalid",
		},
		{
			name: "enum ratio must be positive",
			config: MuleRouterConfig{Routes: []MuleRouterRoute{{
				Vendor: "carrothub", Model: "wan2.2-i2v-spicy",
				BillingVars: []MuleRouterBillingVar{
					{Name: "resolution", Kind: MuleRouterVarKindEnum, Values: map[string]float64{"480p": 0}, Default: "480p"},
				},
			}}},
			wantErr: "positive finite number",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.config.Validate()
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestMuleRouterModelNameAndUpstreamPath(t *testing.T) {
	route := MuleRouterRoute{Vendor: "carrothub", Model: "wan2.2-i2v-spicy"}
	assert.Equal(t, "carrothub/wan2.2-i2v-spicy/generation", route.ModelName())

	path, ok := MuleRouterUpstreamPath(route.ModelName())
	require.True(t, ok)
	assert.Equal(t, "/vendors/carrothub/v1/wan2.2-i2v-spicy/generation", path)

	_, ok = MuleRouterUpstreamPath("carrothub/wan2.2-i2v-spicy")
	assert.False(t, ok, "a model name without an action cannot address an endpoint")
}

func TestFindRouteByModelName(t *testing.T) {
	config := MuleRouterConfig{Routes: []MuleRouterRoute{
		videoRoute(),
		{Vendor: "carrothub", Model: "z-image-spicy"},
	}}

	route, ok := config.FindRouteByModelName("carrothub/z-image-spicy/generation")
	require.True(t, ok)
	assert.Equal(t, "z-image-spicy", route.Model)

	_, ok = config.FindRouteByModelName("carrothub/qwen-image-edit-spicy/generation")
	assert.False(t, ok, "unlisted models must not resolve")
}
