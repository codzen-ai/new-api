package controller

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// columnIndex returns the position of a column in logCSVHeader, failing the
// test if the column is missing so the assertions below stay self-describing.
func columnIndex(t *testing.T, name string) int {
	t.Helper()
	for i, h := range logCSVHeader {
		if h == name {
			return i
		}
	}
	require.Failf(t, "column not found", "logCSVHeader has no %q column", name)
	return -1
}

// TestLogToCSVRowMoneyColumns locks the billing invariant that the exported
// usd/cny columns are derived from quota via QuotaPerUnit and the
// system-configured USDExchangeRate (not the hard-coded default), and that the
// timestamp is rendered in UTC.
func TestLogToCSVRowMoneyColumns(t *testing.T) {
	original := operation_setting.USDExchangeRate
	t.Cleanup(func() { operation_setting.USDExchangeRate = original })

	usdIdx := columnIndex(t, "usd")
	cnyIdx := columnIndex(t, "cny")
	createdIdx := columnIndex(t, "created_at")

	tests := []struct {
		name        string
		quota       int
		rate        float64
		expectedUSD string
		expectedCNY string
	}{
		{name: "one dollar fifty at rate 7.3", quota: 750000, rate: 7.3, expectedUSD: "1.500000", expectedCNY: "10.950000"},
		{name: "configured rate is used not default", quota: 500000, rate: 2.0, expectedUSD: "1.000000", expectedCNY: "2.000000"},
		{name: "zero quota", quota: 0, rate: 7.3, expectedUSD: "0.000000", expectedCNY: "0.000000"},
	}

	require.Equal(t, 500*1000.0, common.QuotaPerUnit, "QuotaPerUnit drives the expected money values in this test")

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			operation_setting.USDExchangeRate = tc.rate

			log := &model.Log{CreatedAt: 1700000000, Quota: tc.quota}
			row := logToCSVRow(log)

			require.Len(t, row, len(logCSVHeader), "row width must match header width")
			assert.Equal(t, tc.expectedUSD, row[usdIdx])
			assert.Equal(t, tc.expectedCNY, row[cnyIdx])
			// 1700000000 is 2023-11-14 22:13:20 UTC, i.e. 2023-11-15 06:13:20 at UTC+8.
			assert.Equal(t, "2023-11-15 06:13:20", row[createdIdx])
		})
	}
}

// TestLogToCSVRowBillingColumns verifies the billing breakdown columns are
// parsed out of Log.Other, and that rows without parseable billing info (system
// logs, malformed JSON) leave those columns blank instead of emitting zeros
// that would read as "free".
func TestLogToCSVRowBillingColumns(t *testing.T) {
	cacheTokensIdx := columnIndex(t, "cache_tokens")
	cacheCreationIdx := columnIndex(t, "cache_creation_tokens")
	modelRatioIdx := columnIndex(t, "model_ratio")
	groupRatioIdx := columnIndex(t, "group_ratio")
	cacheRatioIdx := columnIndex(t, "cache_ratio")
	modelPriceIdx := columnIndex(t, "model_price")
	billingModeIdx := columnIndex(t, "billing_mode")

	t.Run("populated other", func(t *testing.T) {
		other := `{"cache_tokens":128,"cache_creation_tokens":64,"model_ratio":2.5,"group_ratio":1,"completion_ratio":3,"cache_ratio":0.5,"model_price":-1,"billing_mode":"tiered_expr"}`
		row := logToCSVRow(&model.Log{Other: other})

		require.Len(t, row, len(logCSVHeader))
		assert.Equal(t, "128", row[cacheTokensIdx])
		assert.Equal(t, "64", row[cacheCreationIdx])
		assert.Equal(t, "2.5", row[modelRatioIdx])
		assert.Equal(t, "1", row[groupRatioIdx])
		assert.Equal(t, "0.5", row[cacheRatioIdx])
		assert.Equal(t, "-1", row[modelPriceIdx])
		assert.Equal(t, "tiered_expr", row[billingModeIdx])
	})

	t.Run("empty other leaves billing columns blank", func(t *testing.T) {
		row := logToCSVRow(&model.Log{Other: ""})

		assert.Equal(t, "", row[cacheTokensIdx])
		assert.Equal(t, "", row[modelRatioIdx])
		assert.Equal(t, "", row[billingModeIdx])
	})

	t.Run("malformed other leaves billing columns blank", func(t *testing.T) {
		row := logToCSVRow(&model.Log{Other: "not json"})

		assert.Equal(t, "", row[cacheTokensIdx])
		assert.Equal(t, "", row[modelRatioIdx])
	})
}
