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

// TestLogCacheColumnsSpend locks the cache spend recomputation against the
// billing formula in service/text_quota.go: cache tokens are charged at
// cache_ratio × model_ratio × group_ratio, Claude splits cache writes into
// 5m/1h buckets with their own ratios, and the saving reflects what those cache
// reads would have cost at the full input price.
func TestLogCacheColumnsSpend(t *testing.T) {
	require.Equal(t, 500*1000.0, common.QuotaPerUnit, "QuotaPerUnit drives the expected dollar values in this test")

	readIdx := columnIndex(t, "cache_read_usd") - cacheColumnOffset(t)
	writeIdx := columnIndex(t, "cache_write_usd") - cacheColumnOffset(t)
	savingIdx := columnIndex(t, "cache_saving_usd") - cacheColumnOffset(t)
	semanticIdx := columnIndex(t, "usage_semantic") - cacheColumnOffset(t)

	tests := []struct {
		name           string
		other          string
		expectedRead   string
		expectedWrite  string
		expectedSaving string
		expectedSemant string
	}{
		{
			// 500000 cache tokens at cache_ratio 0.1 and ratio 1 => 50000 quota => $0.10.
			// Saving: 500000 × (1 − 0.1) × 1 = 450000 quota => $0.90.
			name:           "openai cache read and write",
			other:          `{"model_ratio":1,"group_ratio":1,"cache_tokens":500000,"cache_ratio":0.1,"cache_creation_tokens":100000,"cache_creation_ratio":1.25}`,
			expectedRead:   "0.100000",
			expectedWrite:  "0.250000",
			expectedSaving: "0.900000",
			expectedSemant: "openai",
		},
		{
			// Claude splits writes: remaining 100000 at 1.25, 200000 at 1.25 (5m),
			// 300000 at 2.0 (1h) => 125000 + 250000 + 600000 = 975000 quota => $1.95.
			name:           "claude splits cache creation buckets",
			other:          `{"claude":true,"model_ratio":1,"group_ratio":1,"cache_tokens":0,"cache_ratio":0.1,"cache_creation_tokens":600000,"cache_creation_ratio":1.25,"cache_creation_tokens_5m":200000,"cache_creation_ratio_5m":1.25,"cache_creation_tokens_1h":300000,"cache_creation_ratio_1h":2.0}`,
			expectedRead:   "0.000000",
			expectedWrite:  "1.950000",
			expectedSaving: "0.000000",
			expectedSemant: "anthropic",
		},
		{
			// group_ratio must be applied on top of model_ratio.
			name:           "group ratio applies to cache spend",
			other:          `{"model_ratio":2,"group_ratio":0.5,"cache_tokens":500000,"cache_ratio":0.5}`,
			expectedRead:   "0.500000",
			expectedWrite:  "",
			expectedSaving: "0.500000",
			expectedSemant: "openai",
		},
		{
			// cache_ratio 1 means caching bought nothing, so the saving is zero.
			name:           "no saving when cache ratio is one",
			other:          `{"model_ratio":1,"group_ratio":1,"cache_tokens":500000,"cache_ratio":1}`,
			expectedRead:   "1.000000",
			expectedWrite:  "",
			expectedSaving: "0.000000",
			expectedSemant: "openai",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cols := logCacheColumns(tc.other)

			require.Len(t, cols, logCacheColumnCount)
			assert.Equal(t, tc.expectedRead, cols[readIdx], "cache_read_usd")
			assert.Equal(t, tc.expectedWrite, cols[writeIdx], "cache_write_usd")
			assert.Equal(t, tc.expectedSaving, cols[savingIdx], "cache_saving_usd")
			assert.Equal(t, tc.expectedSemant, cols[semanticIdx], "usage_semantic")
		})
	}
}

// TestLogCacheColumnsPerCallBilling locks the rule that per-call pricing
// (model_price > 0) never reports cache ratios or recomputed spend, because
// token ratios do not participate in that billing path, while the cache usage
// that actually occurred is still exported.
func TestLogCacheColumnsPerCallBilling(t *testing.T) {
	cols := logCacheColumns(`{"model_price":0.05,"model_ratio":1,"group_ratio":1,"cache_tokens":500000,"cache_ratio":0.1,"cache_creation_tokens":100000,"cache_creation_ratio":1.25}`)

	require.Len(t, cols, logCacheColumnCount)
	offset := cacheColumnOffset(t)
	assert.Equal(t, "500000", cols[columnIndex(t, "cache_tokens")-offset], "usage is still reported")
	assert.Equal(t, "100000", cols[columnIndex(t, "cache_creation_tokens")-offset])
	for _, name := range []string{
		"cache_ratio", "cache_creation_ratio", "cache_creation_ratio_5m", "cache_creation_ratio_1h",
		"cache_read_usd", "cache_write_usd", "cache_saving_usd",
	} {
		assert.Empty(t, cols[columnIndex(t, name)-offset], "%s must stay empty for per-call billing", name)
	}
}

// TestLogCacheColumnsMissingData locks that absent or unusable payloads produce
// empty cells rather than a fabricated "0", and never change the row width.
func TestLogCacheColumnsMissingData(t *testing.T) {
	offset := cacheColumnOffset(t)
	cacheTokensIdx := columnIndex(t, "cache_tokens") - offset
	readIdx := columnIndex(t, "cache_read_usd") - offset

	tests := []struct {
		name  string
		other string
	}{
		{name: "empty payload", other: ""},
		{name: "malformed json", other: `{"model_ratio":`},
		{name: "cache fields absent", other: `{"model_ratio":1,"group_ratio":1}`},
		{name: "ratios present but base ratios missing", other: `{"cache_tokens":500000,"cache_ratio":0.1}`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cols := logCacheColumns(tc.other)

			require.Len(t, cols, logCacheColumnCount)
			assert.Empty(t, cols[readIdx], "cache_read_usd must be empty, not zero")
			if tc.name == "cache fields absent" || tc.name == "empty payload" || tc.name == "malformed json" {
				assert.Empty(t, cols[cacheTokensIdx], "cache_tokens must be empty when not reported")
			}
		})
	}
}

// TestLogToCSVRowIncludesCacheColumns locks that the cache columns are wired
// into the exported row at the positions the header declares, and that content
// stays last so a long body cannot push the numeric columns out of view.
func TestLogToCSVRowIncludesCacheColumns(t *testing.T) {
	log := &model.Log{
		CreatedAt: 1700000000,
		Quota:     500000,
		Content:   "some content",
		Other:     `{"model_ratio":1,"group_ratio":1,"cache_tokens":500000,"cache_ratio":0.1}`,
	}

	row := logToCSVRow(log)

	require.Len(t, row, len(logCSVHeader), "row width must match header width")
	assert.Equal(t, "openai", row[columnIndex(t, "usage_semantic")])
	assert.Equal(t, "500000", row[columnIndex(t, "cache_tokens")])
	assert.Equal(t, "0.100000", row[columnIndex(t, "cache_read_usd")])
	assert.Equal(t, "0.900000", row[columnIndex(t, "cache_saving_usd")])
	assert.Equal(t, "some content", row[columnIndex(t, "content")])
	assert.Equal(t, "content", logCSVHeader[len(logCSVHeader)-1], "content must remain the last column")
}

// cacheColumnOffset returns where the cache columns start in logCSVHeader, so
// tests can index logCacheColumns results by column name.
func cacheColumnOffset(t *testing.T) int {
	t.Helper()
	return columnIndex(t, "usage_semantic")
}
