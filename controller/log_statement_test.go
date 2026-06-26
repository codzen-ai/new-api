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
