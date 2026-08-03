package billingexpr_test

import (
	"testing"

	"github.com/QuantumNous/new-api/pkg/billingexpr"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSplitCostRecoversPrices covers the split a customer statement quotes its
// per-component prices from: each dimension must be charged its own
// coefficient, including inside a tier where that coefficient appears nowhere
// on its own.
func TestSplitCostRecoversPrices(t *testing.T) {
	tests := []struct {
		name          string
		expr          string
		params        billingexpr.TokenParams
		expectedTier  string
		expectedCosts map[string]float64
	}{
		{
			name:          "flat prices",
			expr:          `p * 3 + c * 15 + cr * 0.3 + cc * 3.75`,
			params:        billingexpr.TokenParams{P: 1000, C: 500, CR: 2000, CC: 400},
			expectedCosts: map[string]float64{"p": 3000, "c": 7500, "cr": 600, "cc": 1500},
		},
		{
			name:          "the matched tier prices every dimension",
			expr:          `len <= 200000 ? tier("standard", p * 3 + c * 15) : tier("long_context", p * 6 + c * 22.5)`,
			params:        billingexpr.TokenParams{P: 300000, C: 1000, Len: 300000},
			expectedTier:  "long_context",
			expectedCosts: map[string]float64{"p": 1800000, "c": 22500},
		},
		{
			// An absent dimension is not a dimension that cost nothing, so a
			// statement can tell "not priced here" from "priced at zero".
			name:          "unreferenced and unused dimensions are absent",
			expr:          `p * 3 + c * 15 + cr * 0.3`,
			params:        billingexpr.TokenParams{P: 1000, C: 500, CC: 400},
			expectedCosts: map[string]float64{"p": 3000, "c": 7500},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			split, err := billingexpr.SplitCost(tc.expr, tc.params)

			require.NoError(t, err)
			assert.Equal(t, tc.expectedTier, split.Tier)
			require.Len(t, split.Costs, len(tc.expectedCosts))
			total := 0.0
			for name, expected := range tc.expectedCosts {
				assert.InDeltaf(t, expected, split.Costs[name], 1e-6, "cost of %q", name)
				total += expected
			}
			assert.InDelta(t, total, split.Total, 1e-6, "the components must account for the whole cost")
		})
	}
}

// TestSplitCostRejectsUnseparableExpressions guards the assumption the split
// rests on. Removing a dimension must not reprice the rest, or the components
// would be quoted from two different price lists and no longer add up to what
// was charged.
func TestSplitCostRejectsUnseparableExpressions(t *testing.T) {
	tests := []struct {
		name   string
		expr   string
		params billingexpr.TokenParams
	}{
		{
			name:   "tier condition reads the prompt tokens being removed",
			expr:   `p <= 200000 ? tier("standard", p * 1.5 + c * 7.5) : tier("long_context", p * 3.0 + c * 11.25)`,
			params: billingexpr.TokenParams{P: 300000, C: 5000},
		},
		{
			name:   "a dimension that discounts the total",
			expr:   `p * 3 - cr * 0.3`,
			params: billingexpr.TokenParams{P: 1000, CR: 500},
		},
		{
			name:   "expression does not compile",
			expr:   `p * `,
			params: billingexpr.TokenParams{P: 1000},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := billingexpr.SplitCost(tc.expr, tc.params)

			assert.Error(t, err)
		})
	}
}
