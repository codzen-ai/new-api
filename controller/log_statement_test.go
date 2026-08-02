package controller

import (
	"strconv"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/shopspring/decimal"
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

// amountAt reads a money column as a float so sums can be compared without
// depending on the string formatting.
func amountAt(t *testing.T, row []string, column string) float64 {
	t.Helper()
	raw := row[columnIndex(t, column)]
	if raw == "" {
		return 0
	}
	value, err := strconv.ParseFloat(raw, 64)
	require.NoErrorf(t, err, "column %q is not a number: %q", column, raw)
	return value
}

// useCurrency points the export at a currency for the duration of a test.
func useCurrency(t *testing.T, displayType string, usdToCny float64) {
	t.Helper()
	general := operation_setting.GetGeneralSetting()
	originalType, originalRate := general.QuotaDisplayType, operation_setting.USDExchangeRate
	t.Cleanup(func() {
		general.QuotaDisplayType = originalType
		operation_setting.USDExchangeRate = originalRate
	})
	general.QuotaDisplayType = displayType
	operation_setting.USDExchangeRate = usdToCny
}

// TestExportCurrencyFollowsSiteDisplay locks the statement to the same currency
// and rate the site shows users elsewhere: a CNY site bills in CNY at the
// configured exchange rate, and a USD site never applies a rate at all.
func TestExportCurrencyFollowsSiteDisplay(t *testing.T) {
	tests := []struct {
		name             string
		displayType      string
		usdToCny         float64
		expectedCurrency string
		expectedRate     string
	}{
		{name: "CNY site uses the configured rate", displayType: operation_setting.QuotaDisplayTypeCNY, usdToCny: 7, expectedCurrency: "CNY", expectedRate: "7"},
		{name: "CNY site honours a changed rate", displayType: operation_setting.QuotaDisplayTypeCNY, usdToCny: 7.3, expectedCurrency: "CNY", expectedRate: "7.3"},
		{name: "USD site does not convert", displayType: operation_setting.QuotaDisplayTypeUSD, usdToCny: 7, expectedCurrency: "USD", expectedRate: "1"},
		{name: "token display falls back to USD", displayType: operation_setting.QuotaDisplayTypeTokens, usdToCny: 7, expectedCurrency: "USD", expectedRate: "1"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			useCurrency(t, tc.displayType, tc.usdToCny)

			currency, rate := exportCurrency()

			assert.Equal(t, tc.expectedCurrency, currency)
			assert.Equal(t, tc.expectedRate, rate.String())
		})
	}
}

// TestLogBillingColumnsSumToTotal is the invariant the whole statement rests on:
// a customer must be able to add up the components of a row and land exactly on
// the amount they were charged. It has to hold for every shape of charge,
// including ones the breakdown cannot itemise, because a statement that silently
// loses money is worse than one that reports a lump sum.
func TestLogBillingColumnsSumToTotal(t *testing.T) {
	require.Equal(t, 500*1000.0, common.QuotaPerUnit, "QuotaPerUnit drives the expected amounts in this test")
	useCurrency(t, operation_setting.QuotaDisplayTypeUSD, 7)

	tests := []struct {
		name string
		log  *model.Log
	}{
		{
			name: "openai semantics with cache read and write",
			log: &model.Log{
				PromptTokens: 1000, CompletionTokens: 500, Quota: 4850,
				Other: `{"model_ratio":2,"group_ratio":1,"completion_ratio":3,` +
					`"cache_tokens":200,"cache_ratio":0.5,` +
					`"cache_creation_tokens":100,"cache_creation_ratio":1.25}`,
			},
		},
		{
			name: "anthropic semantics with split cache writes",
			log: &model.Log{
				PromptTokens: 700, CompletionTokens: 500, Quota: 5100,
				Other: `{"claude":true,"model_ratio":2,"group_ratio":1,"completion_ratio":3,` +
					`"cache_tokens":200,"cache_ratio":0.5,` +
					`"cache_creation_tokens":100,"cache_creation_ratio":1.25,` +
					`"cache_creation_tokens_5m":60,"cache_creation_ratio_5m":1.25,` +
					`"cache_creation_tokens_1h":40,"cache_creation_ratio_1h":2}`,
			},
		},
		{
			name: "per-call pricing cannot be itemised",
			log: &model.Log{
				PromptTokens: 10, CompletionTokens: 0, Quota: 25000,
				Other: `{"model_price":0.05,"group_ratio":1}`,
			},
		},
		{
			name: "legacy log without ratios",
			log:  &model.Log{PromptTokens: 100, CompletionTokens: 50, Quota: 900},
		},
		{
			name: "image tokens land in other rather than skewing input",
			log: &model.Log{
				PromptTokens: 1000, CompletionTokens: 0, Quota: 2600,
				Other: `{"model_ratio":2,"group_ratio":1,"completion_ratio":1,` +
					`"image":true,"image_output":200,"image_ratio":3}`,
			},
		},
		{
			name: "settlement rounding is absorbed by other",
			log: &model.Log{
				PromptTokens: 333, CompletionTokens: 111, Quota: 1000,
				Other: `{"model_ratio":1.5,"group_ratio":0.9,"completion_ratio":2.7}`,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			row := logToCSVRow(tc.log, "USD", decimal.NewFromInt(1))

			require.Len(t, row, len(logCSVHeader), "row width must match header width")
			components := amountAt(t, row, "input_amount") +
				amountAt(t, row, "cache_read_amount") +
				amountAt(t, row, "cache_write_amount") +
				amountAt(t, row, "output_amount") +
				amountAt(t, row, "other_amount")
			assert.InDelta(t, amountAt(t, row, "total_amount"), components, 1e-9,
				"components must add up to the charged total")
			assert.InDelta(t, float64(tc.log.Quota)/common.QuotaPerUnit, amountAt(t, row, "total_amount"), 1e-9,
				"total must come from the recorded quota")
		})
	}
}

// TestLogBillingColumnsInputTokensNormalised locks the fix for the column whose
// meaning used to change per row: OpenAI reports cached tokens inside
// prompt_tokens while Anthropic reports them alongside, so the exported input
// count must be the tokens actually charged at the input price either way.
func TestLogBillingColumnsInputTokensNormalised(t *testing.T) {
	useCurrency(t, operation_setting.QuotaDisplayTypeUSD, 7)

	ratios := `"model_ratio":2,"group_ratio":1,"completion_ratio":1,` +
		`"cache_tokens":200,"cache_ratio":0.5,"cache_creation_tokens":100,"cache_creation_ratio":1.25`

	tests := []struct {
		name           string
		log            *model.Log
		expectedTokens string
	}{
		{
			name:           "openai prompt tokens include cache traffic",
			log:            &model.Log{PromptTokens: 1000, Other: `{` + ratios + `}`},
			expectedTokens: "700",
		},
		{
			name:           "anthropic prompt tokens exclude cache traffic",
			log:            &model.Log{PromptTokens: 700, Other: `{"claude":true,` + ratios + `}`},
			expectedTokens: "700",
		},
		{
			name:           "image tokens are carved out of the input count",
			log:            &model.Log{PromptTokens: 1000, Other: `{"model_ratio":2,"group_ratio":1,"image_output":250,"image_ratio":3}`},
			expectedTokens: "750",
		},
		{
			name:           "audio tokens are carved out of the input count",
			log:            &model.Log{PromptTokens: 1000, Other: `{"model_ratio":2,"group_ratio":1,"audio_input_seperate_price":true,"audio_input_token_count":400,"audio_input_price":1}`},
			expectedTokens: "600",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			row := logToCSVRow(tc.log, "USD", decimal.NewFromInt(1))

			assert.Equal(t, tc.expectedTokens, row[columnIndex(t, "input_tokens")])
		})
	}
}

// TestLogBillingColumnsPrices locks the per-million prices a customer checks
// against a published price list, including the blended cache-write price that
// Claude's separate 5m and 1h buckets produce.
func TestLogBillingColumnsPrices(t *testing.T) {
	useCurrency(t, operation_setting.QuotaDisplayTypeCNY, 7)

	// model_ratio 2 × group_ratio 1 = 4 USD per 1M input tokens at
	// QuotaPerUnit 500000, which is 28 CNY at the configured rate of 7.
	log := &model.Log{
		PromptTokens: 700, CompletionTokens: 500, Quota: 5100,
		Other: `{"claude":true,"model_ratio":2,"group_ratio":1,"completion_ratio":3,` +
			`"cache_tokens":200,"cache_ratio":0.5,` +
			`"cache_creation_tokens":100,"cache_creation_ratio":1.25,` +
			`"cache_creation_tokens_5m":60,"cache_creation_ratio_5m":1.25,` +
			`"cache_creation_tokens_1h":40,"cache_creation_ratio_1h":2}`,
	}

	row := logToCSVRow(log, "CNY", decimal.NewFromInt(7))

	assert.Equal(t, "28.000000", row[columnIndex(t, "input_price_per_1m")])
	assert.Equal(t, "14.000000", row[columnIndex(t, "cache_read_price_per_1m")])
	assert.Equal(t, "84.000000", row[columnIndex(t, "output_price_per_1m")])
	// 60 tokens at 1.25 and 40 at 2 blend to 1.55, i.e. 43.4 CNY per 1M.
	assert.Equal(t, "43.400000", row[columnIndex(t, "cache_write_price_per_1m")])
	assert.Equal(t, "CNY", row[columnIndex(t, "currency")])
	assert.Equal(t, "7", row[columnIndex(t, "exchange_rate")])
}

// TestLogBillingColumnsOmitsUnreportedFields keeps an empty cell meaning "the
// provider never reported this", so a customer never reads a blank as a
// measured zero.
func TestLogBillingColumnsOmitsUnreportedFields(t *testing.T) {
	useCurrency(t, operation_setting.QuotaDisplayTypeUSD, 7)

	log := &model.Log{
		PromptTokens: 1000, CompletionTokens: 500, Quota: 5000,
		Other: `{"model_ratio":2,"group_ratio":1,"completion_ratio":3}`,
	}

	row := logToCSVRow(log, "USD", decimal.NewFromInt(1))

	for _, column := range []string{
		"cache_read_tokens", "cache_read_price_per_1m", "cache_read_amount",
		"cache_write_tokens", "cache_write_price_per_1m", "cache_write_amount",
	} {
		assert.Emptyf(t, row[columnIndex(t, column)], "%s must stay empty when no cache was reported", column)
	}
	assert.Equal(t, "per_token", row[columnIndex(t, "billing_mode")])
}

// TestLogToCSVRowIdentityColumns covers the non-money columns: timestamps are
// rendered at UTC+8, and nothing that identifies upstream routing or the
// customer's own traffic reaches a statement.
func TestLogToCSVRowIdentityColumns(t *testing.T) {
	useCurrency(t, operation_setting.QuotaDisplayTypeUSD, 7)

	log := &model.Log{
		CreatedAt: 1700000000,
		RequestId: "req-1", Username: "alice", TokenName: "prod", ModelName: "gpt-4o",
		ChannelId: 7, ChannelName: "azure", Ip: "203.0.113.9", Content: "prompt text",
	}

	row := logToCSVRow(log, "USD", decimal.NewFromInt(1))

	// 1700000000 is 2023-11-14 22:13:20 UTC, i.e. 2023-11-15 06:13:20 at UTC+8.
	assert.Equal(t, "2023-11-15 06:13:20", row[columnIndex(t, "time")])
	assert.Equal(t, "req-1", row[columnIndex(t, "request_id")])
	assert.Equal(t, "alice", row[columnIndex(t, "username")])
	assert.Equal(t, "gpt-4o", row[columnIndex(t, "model")])
	for _, leaked := range []string{"azure", "203.0.113.9", "prompt text", "7"} {
		assert.NotContainsf(t, row, leaked, "%q must not reach a customer statement", leaked)
	}
}
