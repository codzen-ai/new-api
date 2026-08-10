package controller

import (
	"encoding/base64"
	"encoding/csv"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
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

// tieredOther builds the other JSON an expression-priced consume log carries:
// the frozen expression, the tier it matched and the group ratio settlement
// applied. An expression with no tier() call still records an empty matched
// tier, which is what marks the charge as one the expression actually priced.
// extra is spliced in for the token counts a case needs.
func tieredOther(expr, matchedTier, extra string) string {
	other := fmt.Sprintf(`{"billing_mode":"tiered_expr","expr_b64":%q,"matched_tier":%q,"group_ratio":1,"model_ratio":0,"completion_ratio":0`,
		base64.StdEncoding.EncodeToString([]byte(expr)), matchedTier)
	if extra != "" {
		other += "," + extra
	}
	return other + "}"
}

// TestLogBillingColumnsExpressionPricing covers models priced by a billing
// expression rather than by model ratios. Those logs snapshot a zero model
// ratio, so before the expression was replayed the whole charge landed in
// other_amount with no prices for the customer to verify. The token counts and
// charges here are real gpt-5.5 and claude-opus rows from a production
// statement.
func TestLogBillingColumnsExpressionPricing(t *testing.T) {
	require.Equal(t, 500*1000.0, common.QuotaPerUnit, "QuotaPerUnit drives the expected amounts in this test")
	useCurrency(t, operation_setting.QuotaDisplayTypeUSD, 7)

	tests := []struct {
		name          string
		log           *model.Log
		expected      map[string]string
		expectedOther float64
	}{
		{
			// 98053 × $5/1M + 360 × $30/1M = $0.501065, charged as 250533 quota.
			name: "openai semantics without cache",
			log: &model.Log{
				PromptTokens: 98053, CompletionTokens: 360, Quota: 250533,
				Other: tieredOther("p * 5 + c * 30 + cr * 0.5", "", ""),
			},
			expected: map[string]string{
				"input_tokens": "98053", "input_price_per_1m": "5.000000", "input_amount": "0.490265",
				"output_tokens": "360", "output_price_per_1m": "30.000000", "output_amount": "0.010800",
			},
			expectedOther: 0.000001,
		},
		{
			// 1725 × $5 + 97664 × $0.5 + 288 × $30 per 1M = $0.066098.
			name: "cache read is carved out of the prompt total",
			log: &model.Log{
				PromptTokens: 99389, CompletionTokens: 288, Quota: 33049,
				Other: tieredOther("p * 5 + c * 30 + cr * 0.5", "", `"cache_tokens":97664`),
			},
			expected: map[string]string{
				"input_tokens": "1725", "input_price_per_1m": "5.000000", "input_amount": "0.008625",
				"cache_read_tokens": "97664", "cache_read_price_per_1m": "0.500000", "cache_read_amount": "0.048832",
				"output_tokens": "288", "output_price_per_1m": "30.000000", "output_amount": "0.008640",
			},
			expectedOther: 0.000001,
		},
		{
			// Anthropic reports cache creation alongside the prompt total:
			// 2 × $5 + 83917 × $6.25 + 1018 × $25 per 1M = $0.549941.
			name: "anthropic semantics with cache write",
			log: &model.Log{
				PromptTokens: 2, CompletionTokens: 1018, Quota: 274971,
				Other: tieredOther("p * 5 + c * 25 + cr * 0.5 + cc * 6.25", "",
					`"claude":true,"cache_creation_tokens":83917,"cache_creation_tokens_5m":83917`),
			},
			expected: map[string]string{
				"input_tokens": "2", "input_price_per_1m": "5.000000",
				"cache_write_tokens": "83917", "cache_write_price_per_1m": "6.250000", "cache_write_amount": "0.524481",
				"output_tokens": "1018", "output_price_per_1m": "25.000000", "output_amount": "0.025450",
			},
			expectedOther: 0.000001,
		},
		{
			// The prices a tier expression quotes are the matched tier's, which
			// no single coefficient in the expression source carries.
			name: "long context tier prices the row",
			log: &model.Log{
				PromptTokens: 300000, CompletionTokens: 1000, Quota: 911250,
				Other: tieredOther(`len <= 200000 ? tier("standard", p * 3 + c * 15) : tier("long_context", p * 6 + c * 22.5)`,
					"long_context", ""),
			},
			expected: map[string]string{
				"input_tokens": "300000", "input_price_per_1m": "6.000000", "input_amount": "1.800000",
				"output_tokens": "1000", "output_price_per_1m": "22.500000", "output_amount": "0.022500",
			},
			expectedOther: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			row := logToCSVRow(tc.log, "USD", decimal.NewFromInt(1))

			for column, expected := range tc.expected {
				assert.Equalf(t, expected, row[columnIndex(t, column)], "column %s", column)
			}
			assert.Equal(t, "per_token", row[columnIndex(t, "billing_mode")])
			assert.InDelta(t, tc.expectedOther, amountAt(t, row, "other_amount"), 1e-9,
				"other_amount must only absorb settlement rounding here")
			components := amountAt(t, row, "input_amount") +
				amountAt(t, row, "cache_read_amount") +
				amountAt(t, row, "cache_write_amount") +
				amountAt(t, row, "output_amount") +
				amountAt(t, row, "other_amount")
			assert.InDelta(t, amountAt(t, row, "total_amount"), components, 1e-9,
				"components must add up to the charged total")
		})
	}
}

// TestLogBillingColumnsExpressionFallback keeps a replay the exporter cannot
// stand behind out of a customer's statement. Reporting the charge whole is
// unhelpful, but quoting prices derived from the wrong tier, or from tokens the
// log never recorded, would be wrong — and wrong is what a statement cannot be.
func TestLogBillingColumnsExpressionFallback(t *testing.T) {
	useCurrency(t, operation_setting.QuotaDisplayTypeUSD, 7)

	tiered := `len <= 200000 ? tier("standard", p * 3 + c * 15) : tier("long_context", p * 6 + c * 22.5)`

	tests := []struct {
		name string
		log  *model.Log
	}{
		{
			// Header- and body-driven tiers cannot be replayed from a log, so a
			// replay landing outside the billed tier is discarded.
			name: "replay lands in a different tier than the one billed",
			log: &model.Log{
				PromptTokens: 300000, CompletionTokens: 1000, Quota: 456750,
				Other: tieredOther(tiered, "standard", ""),
			},
		},
		{
			// Audio output tokens never reach a consume log, so the completion
			// total cannot be carved up and the replay overprices the row.
			name: "expression prices a dimension the log never recorded",
			log: &model.Log{
				PromptTokens: 98053, CompletionTokens: 360, Quota: 250533,
				Other: tieredOther("p * 5 + c * 30 + ao * 100", "", ""),
			},
		},
		{
			name: "expression is unparseable",
			log: &model.Log{
				PromptTokens: 100, CompletionTokens: 10, Quota: 500,
				Other: tieredOther("p * ", "", ""),
			},
		},
		{
			// No matched tier means settlement could not price the request and
			// charged the pre-consumed estimate, which these tokens do not
			// explain however the expression is replayed.
			name: "settlement never priced the request with the expression",
			log: &model.Log{
				PromptTokens: 98053, CompletionTokens: 360, Quota: 250533,
				Other: `{"billing_mode":"tiered_expr","expr_b64":"cCAqIDUgKyBjICogMzA=","group_ratio":1,"model_ratio":0}`,
			},
		},
		{
			name: "group ratio is missing",
			log: &model.Log{
				PromptTokens: 100, CompletionTokens: 10, Quota: 500,
				Other: `{"billing_mode":"tiered_expr","expr_b64":"cCAqIDU=","model_ratio":0}`,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			row := logToCSVRow(tc.log, "USD", decimal.NewFromInt(1))

			for _, column := range []string{
				"input_tokens", "input_price_per_1m", "input_amount",
				"cache_read_tokens", "cache_read_price_per_1m", "cache_read_amount",
				"cache_write_tokens", "cache_write_price_per_1m", "cache_write_amount",
				"output_tokens", "output_price_per_1m", "output_amount",
			} {
				assert.Emptyf(t, row[columnIndex(t, column)], "%s must stay empty when the split cannot be trusted", column)
			}
			assert.InDelta(t, float64(tc.log.Quota)/common.QuotaPerUnit, amountAt(t, row, "other_amount"), 1e-9,
				"the whole charge must be reported rather than a breakdown that does not hold")
		})
	}
}

// TestLogBillingColumnsRejectsZeroModelRatio locks the fix for statements that
// quoted 0.00 per million on every token of an expression-priced row whose
// replay failed. A zero ratio means the ratios do not describe this charge, and
// an empty cell says that; a zero price says the tokens were free.
func TestLogBillingColumnsRejectsZeroModelRatio(t *testing.T) {
	useCurrency(t, operation_setting.QuotaDisplayTypeUSD, 7)

	log := &model.Log{
		PromptTokens: 98053, CompletionTokens: 360, Quota: 250533,
		Other: `{"model_ratio":0,"group_ratio":1,"completion_ratio":0,"cache_tokens":0,"cache_ratio":0}`,
	}

	row := logToCSVRow(log, "USD", decimal.NewFromInt(1))

	assert.Empty(t, row[columnIndex(t, "input_price_per_1m")])
	assert.Empty(t, row[columnIndex(t, "output_price_per_1m")])
	assert.InDelta(t, float64(log.Quota)/common.QuotaPerUnit, amountAt(t, row, "other_amount"), 1e-9)
}

// TestBillingCalculationExplainsTheCharge locks the column a customer reads
// instead of rebuilding the arithmetic themselves: every term must trace back
// to another cell on the same row, and the line must end at the charge.
func TestBillingCalculationExplainsTheCharge(t *testing.T) {
	useCurrency(t, operation_setting.QuotaDisplayTypeCNY, 7)

	tests := []struct {
		name     string
		log      *model.Log
		expected string
	}{
		{
			name: "ratio priced row with cache read",
			log: &model.Log{
				PromptTokens: 108899, CompletionTokens: 1291, Quota: 19450,
				Other: `{"model_ratio":0.875,"group_ratio":1,"completion_ratio":8,"cache_tokens":107776,"cache_ratio":0.1}`,
			},
			expected: "1123×12.250000÷1M + 107776×1.225000÷1M + 1291×98.000000÷1M = 0.272300",
		},
		{
			name: "expression priced row keeps a rounding remainder visible",
			log: &model.Log{
				PromptTokens: 99389, CompletionTokens: 288, Quota: 33049,
				Other: tieredOther("p * 5 + c * 30 + cr * 0.5", "", `"cache_tokens":97664`),
			},
			expected: "1725×35.000000÷1M + 97664×3.500000÷1M + 288×210.000000÷1M + 0.000007 = 0.462686",
		},
		{
			name: "cache write row",
			log: &model.Log{
				PromptTokens: 2, CompletionTokens: 1018, Quota: 274971,
				Other: tieredOther("p * 5 + c * 25 + cr * 0.5 + cc * 6.25", "",
					`"claude":true,"cache_creation_tokens":83917,"cache_creation_tokens_5m":83917`),
			},
			expected: "2×35.000000÷1M + 83917×43.750000÷1M + 1018×175.000000÷1M + 0.000005 = 3.849594",
		},
		{
			// Settlement can round a hair above the components, and the column
			// has to say so rather than quietly drop the difference.
			name: "negative remainder subtracts",
			log: &model.Log{
				PromptTokens: 1000000, CompletionTokens: 0, Quota: 874999,
				Other: `{"model_ratio":0.875,"group_ratio":1,"completion_ratio":8}`,
			},
			expected: "1000000×12.250000÷1M + 0×98.000000÷1M - 0.000014 = 12.249986",
		},
		{
			name: "per-call row has no arithmetic to show",
			log: &model.Log{
				PromptTokens: 10, CompletionTokens: 0, Quota: 25000,
				Other: `{"model_price":0.05,"group_ratio":1}`,
			},
			expected: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			row := logToCSVRow(tc.log, "CNY", decimal.NewFromInt(7))

			assert.Equal(t, tc.expected, row[columnIndex(t, "calculation")])
		})
	}
}

// TestLogToCSVRowNeutralisesSpreadsheetFormulas keeps a name a user chose from
// being executed by the spreadsheet the statement is opened in, while leaving
// the amounts as numbers the sheet can still add up.
func TestLogToCSVRowNeutralisesSpreadsheetFormulas(t *testing.T) {
	useCurrency(t, operation_setting.QuotaDisplayTypeUSD, 7)

	log := &model.Log{
		Username: "=1+1", TokenName: "@SUM(A1)", ModelName: "-gpt", RequestId: "req-1",
		PromptTokens: 1000000, CompletionTokens: 0, Quota: 874999,
		Other: `{"model_ratio":0.875,"group_ratio":1,"completion_ratio":8}`,
	}

	row := logToCSVRow(log, "USD", decimal.NewFromInt(1))

	assert.Equal(t, "'=1+1", row[columnIndex(t, "username")])
	assert.Equal(t, "'@SUM(A1)", row[columnIndex(t, "token_name")])
	assert.Equal(t, "'-gpt", row[columnIndex(t, "model")])
	assert.Equal(t, "req-1", row[columnIndex(t, "request_id")])
	// A negative amount is a number, not a formula: escaping it would stop the
	// column from summing in the sheet the customer reconciles in.
	assert.Equal(t, "-0.000002", row[columnIndex(t, "other_amount")])
}

// TestLogBillingColumnsReportsTier answers the question a customer asks when
// two rows of the same model quote different unit prices. The tier is a fact
// settlement recorded, so it is reported even for a row whose breakdown could
// not be recovered, and it stays empty for models that have no tiers at all.
func TestLogBillingColumnsReportsTier(t *testing.T) {
	useCurrency(t, operation_setting.QuotaDisplayTypeUSD, 7)

	tiered := `len <= 200000 ? tier("standard", p * 3 + c * 15) : tier("long_context", p * 6 + c * 22.5)`

	tests := []struct {
		name         string
		log          *model.Log
		expectedTier string
	}{
		{
			name: "the billed tier is named",
			log: &model.Log{
				PromptTokens: 300000, CompletionTokens: 1000, Quota: 911250,
				Other: tieredOther(tiered, "long_context", ""),
			},
			expectedTier: "long_context",
		},
		{
			// The breakdown falls back here, but the tier is still on record.
			name: "a row that could not be split still names its tier",
			log: &model.Log{
				PromptTokens: 98053, CompletionTokens: 360, Quota: 250533,
				Other: tieredOther("p * 5 + c * 30 + ao * 100", "base", ""),
			},
			expectedTier: "base",
		},
		{
			name: "an expression with no tiers has nothing to name",
			log: &model.Log{
				PromptTokens: 98053, CompletionTokens: 360, Quota: 250533,
				Other: tieredOther("p * 5 + c * 30", "", ""),
			},
			expectedTier: "",
		},
		{
			name: "ratio priced models have no tiers",
			log: &model.Log{
				PromptTokens: 1000, CompletionTokens: 500, Quota: 5000,
				Other: `{"model_ratio":2,"group_ratio":1,"completion_ratio":3}`,
			},
			expectedTier: "",
		},
		{
			name: "per-call pricing has no tiers",
			log: &model.Log{
				PromptTokens: 10, CompletionTokens: 0, Quota: 25000,
				Other: `{"model_price":0.05,"group_ratio":1}`,
			},
			expectedTier: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			row := logToCSVRow(tc.log, "USD", decimal.NewFromInt(1))

			assert.Equal(t, tc.expectedTier, row[columnIndex(t, "tier")])
		})
	}
}

// TestVideoTaskRowsNetToTheCharge is the invariant a video statement rests on.
// An asynchronous task holds an estimate on submit and settles the difference
// minutes later, so its charge is spread over two rows; adding them up has to
// land on what the task actually cost, in both directions. The figures are the
// 480p case from guides/seedance-billing.md.
func TestVideoTaskRowsNetToTheCharge(t *testing.T) {
	require.Equal(t, 500*1000.0, common.QuotaPerUnit, "QuotaPerUnit drives the expected amounts in this test")
	useCurrency(t, operation_setting.QuotaDisplayTypeUSD, 7)

	const holdQuota, tokens = 750_000, 100_858
	const requestId, taskId = "202608022213205551212", "task_To0oAtJorNK2Zm8Wg9hYvMTMMHUx"

	tests := []struct {
		name              string
		settlementType    int
		settlementQuota   int
		actualQuota       int
		expectedEntryType string
	}{
		{
			name:              "the upstream used less than the hold",
			settlementType:    model.LogTypeRefund,
			settlementQuota:   598_086,
			actualQuota:       151_914,
			expectedEntryType: entryTypeRefund,
		},
		{
			name:              "the upstream used more than the hold",
			settlementType:    model.LogTypeConsume,
			settlementQuota:   150_000,
			actualQuota:       900_000,
			expectedEntryType: entryTypeSettlement,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hold := logToCSVRow(&model.Log{
				Type: model.LogTypeConsume, Quota: holdQuota,
				RequestId: requestId, ModelName: "doubao-seedance-2-0-260128",
				Other: fmt.Sprintf(`{"is_task":true,"task_id":%q,"model_price":0,"model_ratio":3,"group_ratio":1}`, taskId),
			}, "USD", decimal.NewFromInt(1))

			settlement := logToCSVRow(&model.Log{
				Type: tc.settlementType, Quota: tc.settlementQuota, CompletionTokens: tokens,
				RequestId: requestId, ModelName: "doubao-seedance-2-0-260128",
				Other: fmt.Sprintf(`{"task_id":%q,"model_ratio":3,"group_ratio":1,"pre_consumed_quota":%d,"actual_quota":%d}`,
					taskId, holdQuota, tc.actualQuota),
			}, "USD", decimal.NewFromInt(1))

			// Both rows carry the identifiers that make them one charge.
			for _, row := range [][]string{hold, settlement} {
				assert.Equal(t, requestId, row[columnIndex(t, "request_id")])
				assert.Equal(t, taskId, row[columnIndex(t, "task_id")])
			}

			// The hold is an estimate taken before any usage was reported, so it
			// states no tokens and no unit price — quoting one against zero tokens
			// is how a 4.9× overstatement used to read as an itemised charge.
			assert.Equal(t, entryTypePrepaidHold, hold[columnIndex(t, "entry_type")])
			assert.Empty(t, hold[columnIndex(t, "input_tokens")])
			assert.Empty(t, hold[columnIndex(t, "output_tokens")])
			assert.Empty(t, hold[columnIndex(t, "calculation")])
			assert.InDelta(t, float64(holdQuota)/common.QuotaPerUnit, amountAt(t, hold, "total_amount"), 1e-9)
			assert.InDelta(t, amountAt(t, hold, "total_amount"), amountAt(t, hold, "other_amount"), 1e-9)

			// The settlement states the usage it priced, and its remainder is the
			// hold it reverses — which is why the pair nets to the charge.
			assert.Equal(t, tc.expectedEntryType, settlement[columnIndex(t, "entry_type")])
			assert.Equal(t, strconv.Itoa(tokens), settlement[columnIndex(t, "output_tokens")])
			assert.InDelta(t, float64(tc.actualQuota)/common.QuotaPerUnit, amountAt(t, settlement, "output_amount"), 1e-9)
			assert.InDelta(t, -float64(holdQuota)/common.QuotaPerUnit, amountAt(t, settlement, "other_amount"), 1e-9)
			assert.NotEmpty(t, settlement[columnIndex(t, "calculation")])

			// A refund returns money, so it has to enter the statement negative.
			expectedSettlement := float64(tc.settlementQuota) / common.QuotaPerUnit
			if tc.settlementType == model.LogTypeRefund {
				expectedSettlement = -expectedSettlement
			}
			assert.InDelta(t, expectedSettlement, amountAt(t, settlement, "total_amount"), 1e-9)

			components := amountAt(t, settlement, "output_amount") + amountAt(t, settlement, "other_amount")
			assert.InDelta(t, amountAt(t, settlement, "total_amount"), components, 1e-9,
				"components must add up to the row's amount")

			assert.InDelta(t, float64(tc.actualQuota)/common.QuotaPerUnit,
				amountAt(t, hold, "total_amount")+amountAt(t, settlement, "total_amount"), 1e-9,
				"the task's rows must net to what the task cost")
		})
	}
}

// TestLogEntryTypeNamesTheRow covers the column that tells an estimate apart from
// the charge that replaced it. Getting this wrong in either direction misreports
// money: a hold read as a charge overstates the bill, and a fixed-price task read
// as a hold implies a correction that never comes.
func TestLogEntryTypeNamesTheRow(t *testing.T) {
	useCurrency(t, operation_setting.QuotaDisplayTypeUSD, 7)

	tests := []struct {
		name                string
		log                 *model.Log
		expectedEntryType   string
		expectedBillingMode string
	}{
		{
			name:                "a plain request is one charge",
			log:                 &model.Log{Type: model.LogTypeConsume, PromptTokens: 1000, CompletionTokens: 500, Quota: 5000, Other: `{"model_ratio":2,"group_ratio":1,"completion_ratio":3}`},
			expectedEntryType:   entryTypeConsume,
			expectedBillingMode: "per_token",
		},
		{
			name:                "a per-token task holds an estimate",
			log:                 &model.Log{Type: model.LogTypeConsume, Quota: 750000, Other: `{"is_task":true,"task_id":"task_a","model_price":0,"model_ratio":3,"group_ratio":1}`},
			expectedEntryType:   entryTypePrepaidHold,
			expectedBillingMode: "per_token",
		},
		{
			// A fixed price is final at submit time and never gets a second row.
			name:                "a per-call task is charged outright",
			log:                 &model.Log{Type: model.LogTypeConsume, Quota: 25000, Other: `{"is_task":true,"task_id":"task_b","model_price":0.05,"group_ratio":1}`},
			expectedEntryType:   entryTypeConsume,
			expectedBillingMode: "per_call",
		},
		{
			name:                "an under-estimated task settles the difference",
			log:                 &model.Log{Type: model.LogTypeConsume, Quota: 150000, CompletionTokens: 300000, Other: `{"task_id":"task_c","model_ratio":3,"group_ratio":1,"pre_consumed_quota":750000,"actual_quota":900000}`},
			expectedEntryType:   entryTypeSettlement,
			expectedBillingMode: "per_token",
		},
		{
			name:                "a failed task refunds its hold",
			log:                 &model.Log{Type: model.LogTypeRefund, Quota: 750000, Other: `{"task_id":"task_d","model_ratio":3,"group_ratio":1,"reason":"upstream error"}`},
			expectedEntryType:   entryTypeRefund,
			expectedBillingMode: "per_token",
		},
		{
			name:                "a failed per-call task refunds its price",
			log:                 &model.Log{Type: model.LogTypeRefund, Quota: 25000, Other: `{"task_id":"task_e","model_price":0.05,"group_ratio":1,"reason":"构图失败"}`},
			expectedEntryType:   entryTypeRefund,
			expectedBillingMode: "per_call",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			row := logToCSVRow(tc.log, "USD", decimal.NewFromInt(1))

			assert.Equal(t, tc.expectedEntryType, row[columnIndex(t, "entry_type")])
			assert.Equal(t, tc.expectedBillingMode, row[columnIndex(t, "billing_mode")])

			// Whatever the row is, its components still have to add up to it.
			components := amountAt(t, row, "input_amount") +
				amountAt(t, row, "cache_read_amount") +
				amountAt(t, row, "cache_write_amount") +
				amountAt(t, row, "output_amount") +
				amountAt(t, row, "other_amount")
			assert.InDelta(t, amountAt(t, row, "total_amount"), components, 1e-9)

			expectedTotal := float64(tc.log.Quota) / common.QuotaPerUnit
			if tc.log.Type == model.LogTypeRefund {
				expectedTotal = -expectedTotal
			}
			assert.InDelta(t, expectedTotal, amountAt(t, row, "total_amount"), 1e-9)
		})
	}
}

// openLogExportTestDB points the log queries at a private in-memory database for
// the duration of one test.
func openLogExportTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	gin.SetMode(gin.TestMode)
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	common.RedisEnabled = false

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Log{}))

	originalDB, originalLogDB := model.DB, model.LOG_DB
	model.DB, model.LOG_DB = db, db
	t.Cleanup(func() {
		model.DB, model.LOG_DB = originalDB, originalLogDB
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})

	return db
}

// TestExportAllLogsCarriesTheRefundsThatOffsetConsumption locks the reason a
// statement is exported by type rather than filtered like the log view: an
// asynchronous task is charged in two records, and consume rows alone bill every
// video at the estimate taken before it ran. Nothing else may come along for the
// ride — a top-up in the file would read as consumption.
func TestExportAllLogsCarriesTheRefundsThatOffsetConsumption(t *testing.T) {
	require.Equal(t, 500*1000.0, common.QuotaPerUnit, "QuotaPerUnit drives the expected amounts in this test")
	useCurrency(t, operation_setting.QuotaDisplayTypeUSD, 7)
	db := openLogExportTestDB(t)

	const holdQuota, refundQuota, actualQuota, textQuota = 750_000, 598_086, 151_914, 5_000
	taskOther := `{"is_task":true,"task_id":"task_a","model_price":0,"model_ratio":3,"group_ratio":1}`
	settlementOther := fmt.Sprintf(`{"task_id":"task_a","model_ratio":3,"group_ratio":1,"pre_consumed_quota":%d,"actual_quota":%d}`, holdQuota, actualQuota)

	for _, l := range []*model.Log{
		{Type: model.LogTypeConsume, Username: "u", ModelName: "gpt-5.5", Quota: textQuota, PromptTokens: 1000, CompletionTokens: 500, CreatedAt: 1785680000, RequestId: "req-text", Other: `{"model_ratio":2,"group_ratio":1,"completion_ratio":3}`},
		{Type: model.LogTypeConsume, Username: "u", ModelName: "doubao-seedance-2-0", Quota: holdQuota, CreatedAt: 1785680006, RequestId: "req-video", Other: taskOther},
		{Type: model.LogTypeRefund, Username: "u", ModelName: "doubao-seedance-2-0", Quota: refundQuota, CompletionTokens: 100858, CreatedAt: 1785680214, RequestId: "req-video", Other: settlementOther},
		{Type: model.LogTypeTopup, Username: "u", Quota: 1_000_000, CreatedAt: 1785680300, RequestId: "req-topup"},
		{Type: model.LogTypeError, Username: "u", ModelName: "gpt-5.5", CreatedAt: 1785680400, RequestId: "req-error"},
	} {
		require.NoError(t, db.Create(l).Error)
	}

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/log/export/csv?type=2", nil)

	ExportAllLogs(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	// Excel needs the UTF-8 BOM the exporter writes; the parser must not see it.
	rows, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(recorder.Body.String(), "\ufeff"))).ReadAll()
	require.NoError(t, err)
	require.Equal(t, logCSVHeader, rows[0])

	total := 0.0
	var entryTypes, requestIds []string
	for _, row := range rows[1:] {
		total += amountAt(t, row, "total_amount")
		entryTypes = append(entryTypes, row[columnIndex(t, "entry_type")])
		requestIds = append(requestIds, row[columnIndex(t, "request_id")])
	}

	assert.ElementsMatch(t, []string{entryTypeConsume, entryTypePrepaidHold, entryTypeRefund}, entryTypes,
		"the task's hold and its refund both belong on the statement")
	assert.NotContains(t, requestIds, "req-topup", "a top-up would read as consumption")
	assert.NotContains(t, requestIds, "req-error", "an error log is not a charge")
	assert.InDelta(t, float64(textQuota+actualQuota)/common.QuotaPerUnit, total, 1e-9,
		"the file must sum to what was charged, not to the estimates")
}
