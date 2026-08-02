package controller

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	i18n "github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

func GetAllLogs(c *gin.Context) {
	pageInfo := common.GetPageQuery(c)
	logType, _ := strconv.Atoi(c.Query("type"))
	startTimestamp, _ := strconv.ParseInt(c.Query("start_timestamp"), 10, 64)
	endTimestamp, _ := strconv.ParseInt(c.Query("end_timestamp"), 10, 64)
	username := c.Query("username")
	tokenName := c.Query("token_name")
	modelName := c.Query("model_name")
	channel, _ := strconv.Atoi(c.Query("channel"))
	group := c.Query("group")
	requestId := c.Query("request_id")
	upstreamRequestId := c.Query("upstream_request_id")
	logs, total, err := model.GetAllLogs(logType, startTimestamp, endTimestamp, modelName, username, tokenName, pageInfo.GetStartIdx(), pageInfo.GetPageSize(), channel, group, requestId, upstreamRequestId)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	pageInfo.SetTotal(int(total))
	pageInfo.SetItems(logs)
	common.ApiSuccess(c, pageInfo)
	return
}

func GetUserLogs(c *gin.Context) {
	pageInfo := common.GetPageQuery(c)
	userId := c.GetInt("id")
	logType, _ := strconv.Atoi(c.Query("type"))
	startTimestamp, _ := strconv.ParseInt(c.Query("start_timestamp"), 10, 64)
	endTimestamp, _ := strconv.ParseInt(c.Query("end_timestamp"), 10, 64)
	tokenName := c.Query("token_name")
	modelName := c.Query("model_name")
	group := c.Query("group")
	requestId := c.Query("request_id")
	upstreamRequestId := c.Query("upstream_request_id")
	logs, total, err := model.GetUserLogs(userId, logType, startTimestamp, endTimestamp, modelName, tokenName, pageInfo.GetStartIdx(), pageInfo.GetPageSize(), group, requestId, upstreamRequestId)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	pageInfo.SetTotal(int(total))
	pageInfo.SetItems(logs)
	common.ApiSuccess(c, pageInfo)
	return
}

// Deprecated: SearchAllLogs 已废弃，前端未使用该接口。
func SearchAllLogs(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"success": false,
		"message": "该接口已废弃",
	})
}

// Deprecated: SearchUserLogs 已废弃，前端未使用该接口。
func SearchUserLogs(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"success": false,
		"message": "该接口已废弃",
	})
}

func GetLogByKey(c *gin.Context) {
	tokenId := c.GetInt("token_id")
	if tokenId == 0 {
		c.JSON(200, gin.H{
			"success": false,
			"message": "无效的令牌",
		})
		return
	}
	logs, err := model.GetLogByTokenId(tokenId)
	if err != nil {
		c.JSON(200, gin.H{
			"success": false,
			"message": err.Error(),
		})
		return
	}
	c.JSON(200, gin.H{
		"success": true,
		"message": "",
		"data":    logs,
	})
}

func GetLogsStat(c *gin.Context) {
	logType, _ := strconv.Atoi(c.Query("type"))
	startTimestamp, _ := strconv.ParseInt(c.Query("start_timestamp"), 10, 64)
	endTimestamp, _ := strconv.ParseInt(c.Query("end_timestamp"), 10, 64)
	tokenName := c.Query("token_name")
	username := c.Query("username")
	modelName := c.Query("model_name")
	channel, _ := strconv.Atoi(c.Query("channel"))
	group := c.Query("group")
	stat, err := model.SumUsedQuota(logType, startTimestamp, endTimestamp, modelName, username, tokenName, channel, group)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	//tokenNum := model.SumUsedToken(logType, startTimestamp, endTimestamp, modelName, username, "")
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"quota": stat.Quota,
			"rpm":   stat.Rpm,
			"tpm":   stat.Tpm,
		},
	})
	return
}

func GetLogsSelfStat(c *gin.Context) {
	username := c.GetString("username")
	logType, _ := strconv.Atoi(c.Query("type"))
	startTimestamp, _ := strconv.ParseInt(c.Query("start_timestamp"), 10, 64)
	endTimestamp, _ := strconv.ParseInt(c.Query("end_timestamp"), 10, 64)
	tokenName := c.Query("token_name")
	modelName := c.Query("model_name")
	channel, _ := strconv.Atoi(c.Query("channel"))
	group := c.Query("group")
	quotaNum, err := model.SumUsedQuota(logType, startTimestamp, endTimestamp, modelName, username, tokenName, channel, group)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	//tokenNum := model.SumUsedToken(logType, startTimestamp, endTimestamp, modelName, username, tokenName)
	c.JSON(200, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"quota": quotaNum.Quota,
			"rpm":   quotaNum.Rpm,
			"tpm":   quotaNum.Tpm,
			//"token": tokenNum,
		},
	})
	return
}

const logExportMaxCount = 100_000

// The export is a customer-facing reconciliation statement, so it carries only
// what a customer needs to verify a charge. Channel, IP and request content are
// deliberately absent: they expose upstream routing and the customer's own
// traffic, neither of which belongs on an invoice.
//
// Every row is self-checking:
//
//	input + cache_read + cache_write + output + other = total
//
// Prices are per 1M tokens, in the currency the site displays elsewhere, so the
// figures match what the customer sees in the web UI.
var logCSVHeader = []string{
	"time", "request_id", "username", "token_name", "model", "billing_mode",
	"input_tokens", "input_price_per_1m", "input_amount",
	"cache_read_tokens", "cache_read_price_per_1m", "cache_read_amount",
	"cache_write_tokens", "cache_write_price_per_1m", "cache_write_amount",
	"output_tokens", "output_price_per_1m", "output_amount",
	"other_amount", "total_amount", "currency", "exchange_rate",
}

// logExportTimeZone is the fixed UTC+8 zone used to render exported timestamps.
var logExportTimeZone = time.FixedZone("UTC+8", 8*60*60)

// tokensPerPriceUnit is the token count prices are quoted per, matching how
// upstream providers publish their price lists.
var tokensPerPriceUnit = decimal.NewFromInt(1_000_000)

// exportCurrency resolves the currency the statement is denominated in and the
// USD conversion rate, mirroring how the site displays money everywhere else so
// a statement reconciles against the web UI. A site displaying raw tokens has no
// currency to bill in, so it falls back to USD.
func exportCurrency() (string, decimal.Decimal) {
	rate := decimal.NewFromFloat(operation_setting.GetUsdToCurrencyRate(operation_setting.USDExchangeRate))
	switch operation_setting.GetQuotaDisplayType() {
	case operation_setting.QuotaDisplayTypeCNY:
		return "CNY", rate
	case operation_setting.QuotaDisplayTypeCustom:
		return operation_setting.GetCurrencySymbol(), rate
	default:
		return "USD", decimal.NewFromInt(1)
	}
}

// logBillingColumnCount is the number of columns produced by logBillingColumns:
// the billing mode, four token/price/amount triples, and the other and total
// amounts.
const logBillingColumnCount = 15

// Column offsets within the slice logBillingColumns returns.
const (
	colBillingMode = iota
	colInputTokens
	colInputPrice
	colInputAmount
	colCacheReadTokens
	colCacheReadPrice
	colCacheReadAmount
	colCacheWriteTokens
	colCacheWritePrice
	colCacheWriteAmount
	colOutputTokens
	colOutputPrice
	colOutputAmount
	colOtherAmount
	colTotalAmount
)

// logBillingColumns splits a log's charge into the components a customer can
// verify, using the ratios the billing pipeline snapshotted at request time
// (see service/log_info_generate.go). Reading the snapshot rather than current
// settings keeps historical statements stable when model ratios later change.
//
// The split follows service/text_quota.go. Two details matter for a statement:
//
//   - What counts as an input token depends on the upstream's usage semantics.
//     OpenAI reports cached tokens inside prompt_tokens, Anthropic reports them
//     alongside. Exporting prompt_tokens raw would mean the column's meaning
//     changed from row to row, so it is normalised here into the tokens actually
//     charged at the input price.
//   - other_amount is derived by subtraction, not by adding up the remaining fee
//     types. Image, audio, tool-call surcharges, per-call pricing and settlement
//     rounding all land there automatically, which keeps the row's components
//     summing to the authoritative total no matter what produced the charge.
//
// When the ratios are missing — old logs, or per-call pricing where token ratios
// do not apply — the breakdown is left empty and the whole charge is reported as
// other_amount. An empty cell means "not applicable here", never a measured zero.
func logBillingColumns(l *model.Log, rate decimal.Decimal) []string {
	cols := make([]string, logBillingColumnCount)
	totalQuota := decimal.NewFromInt(int64(l.Quota))
	cols[colTotalAmount] = quotaToAmount(totalQuota, rate)

	other := map[string]any{}
	if l.Other != "" {
		if parsed, err := common.StrToMap(l.Other); err == nil && parsed != nil {
			other = parsed
		}
	}
	number := func(key string) (decimal.Decimal, bool) {
		value, ok := other[key].(float64)
		if !ok {
			return decimal.Zero, false
		}
		return decimal.NewFromFloat(value), true
	}

	perCall := false
	if modelPrice, ok := number("model_price"); ok && modelPrice.IsPositive() {
		perCall = true
	}
	cols[colBillingMode] = "per_token"
	if perCall {
		cols[colBillingMode] = "per_call"
	}

	modelRatio, hasModelRatio := number("model_ratio")
	groupRatio, hasGroupRatio := number("group_ratio")
	if perCall || !hasModelRatio || !hasGroupRatio {
		cols[colOtherAmount] = quotaToAmount(totalQuota, rate)
		return cols
	}
	// quotaPerInputToken is what one plain input token costs; every other
	// component is that price scaled by its own ratio.
	quotaPerInputToken := modelRatio.Mul(groupRatio)

	cacheReadTokens, _ := number("cache_tokens")
	cacheReadRatio, hasCacheReadRatio := number("cache_ratio")
	writeTokens, _ := number("cache_creation_tokens")
	writeRatio, hasWriteRatio := number("cache_creation_ratio")
	imageTokens, _ := number("image_output")
	audioTokens, _ := number("audio_input_token_count")

	// Anthropic reports cache traffic outside prompt_tokens; OpenAI reports it
	// inside. Image and audio tokens are always carved out of prompt_tokens.
	inputTokens := decimal.NewFromInt(int64(l.PromptTokens))
	if _, isClaude := other["claude"].(bool); !isClaude {
		inputTokens = inputTokens.Sub(cacheReadTokens).Sub(writeTokens)
	}
	inputTokens = inputTokens.Sub(imageTokens).Sub(audioTokens)
	if inputTokens.IsNegative() {
		inputTokens = decimal.Zero
	}

	inputQuota := inputTokens.Mul(quotaPerInputToken)
	cols[colInputTokens] = inputTokens.String()
	cols[colInputPrice] = pricePerUnit(decimal.NewFromInt(1), quotaPerInputToken, rate)
	cols[colInputAmount] = quotaToAmount(inputQuota, rate)

	var cacheReadQuota decimal.Decimal
	if hasCacheReadRatio && cacheReadTokens.IsPositive() {
		cacheReadQuota = cacheReadTokens.Mul(cacheReadRatio).Mul(quotaPerInputToken)
		cols[colCacheReadTokens] = cacheReadTokens.String()
		cols[colCacheReadPrice] = pricePerUnit(cacheReadRatio, quotaPerInputToken, rate)
		cols[colCacheReadAmount] = quotaToAmount(cacheReadQuota, rate)
	}

	var cacheWriteQuota decimal.Decimal
	if hasWriteRatio && writeTokens.IsPositive() {
		// Claude prices 5m and 1h cache writes differently, so the row reports
		// the blended rate the customer actually paid across both buckets.
		tokens5m, _ := number("cache_creation_tokens_5m")
		tokens1h, _ := number("cache_creation_tokens_1h")
		weighted := writeTokens.Mul(writeRatio)
		if tokens5m.IsPositive() || tokens1h.IsPositive() {
			ratio5m, _ := number("cache_creation_ratio_5m")
			ratio1h, _ := number("cache_creation_ratio_1h")
			remaining := writeTokens.Sub(tokens5m).Sub(tokens1h)
			if remaining.IsNegative() {
				remaining = decimal.Zero
			}
			weighted = remaining.Mul(writeRatio).
				Add(tokens5m.Mul(ratio5m)).
				Add(tokens1h.Mul(ratio1h))
		}
		cacheWriteQuota = weighted.Mul(quotaPerInputToken)
		cols[colCacheWriteTokens] = writeTokens.String()
		cols[colCacheWritePrice] = pricePerUnit(weighted.Div(writeTokens), quotaPerInputToken, rate)
		cols[colCacheWriteAmount] = quotaToAmount(cacheWriteQuota, rate)
	}

	outputTokens := decimal.NewFromInt(int64(l.CompletionTokens))
	completionRatio, hasCompletionRatio := number("completion_ratio")
	var outputQuota decimal.Decimal
	if hasCompletionRatio {
		outputQuota = outputTokens.Mul(completionRatio).Mul(quotaPerInputToken)
		cols[colOutputTokens] = outputTokens.String()
		cols[colOutputPrice] = pricePerUnit(completionRatio, quotaPerInputToken, rate)
		cols[colOutputAmount] = quotaToAmount(outputQuota, rate)
	}

	accounted := inputQuota.Add(cacheReadQuota).Add(cacheWriteQuota).Add(outputQuota)
	cols[colOtherAmount] = quotaToAmount(totalQuota.Sub(accounted), rate)
	return cols
}

// pricePerUnit renders what one million tokens of a component cost, where ratio
// is the component's multiplier over the plain input price.
func pricePerUnit(ratio, quotaPerInputToken, rate decimal.Decimal) string {
	return quotaToAmount(ratio.Mul(quotaPerInputToken).Mul(tokensPerPriceUnit), rate)
}

// quotaToAmount converts a quota amount into the statement's currency.
func quotaToAmount(quota, rate decimal.Decimal) string {
	return quota.Div(decimal.NewFromFloat(common.QuotaPerUnit)).Mul(rate).StringFixed(6)
}

func logToCSVRow(l *model.Log, currency string, rate decimal.Decimal) []string {
	row := []string{
		time.Unix(l.CreatedAt, 0).In(logExportTimeZone).Format("2006-01-02 15:04:05"),
		l.RequestId,
		l.Username,
		l.TokenName,
		l.ModelName,
	}
	row = append(row, logBillingColumns(l, rate)...)
	// The rate is repeated on every row so a single row is enough to re-derive
	// the amounts, without the reader having to look elsewhere for the basis.
	return append(row, currency, rate.String())
}

func ExportAllLogs(c *gin.Context) {
	logType, _ := strconv.Atoi(c.Query("type"))
	startTimestamp, _ := strconv.ParseInt(c.Query("start_timestamp"), 10, 64)
	endTimestamp, _ := strconv.ParseInt(c.Query("end_timestamp"), 10, 64)
	username := c.Query("username")
	tokenName := c.Query("token_name")
	modelName := c.Query("model_name")
	channel, _ := strconv.Atoi(c.Query("channel"))
	group := c.Query("group")
	requestId := c.Query("request_id")
	upstreamRequestId := c.Query("upstream_request_id")

	total, err := model.CountAllLogsForExport(logType, startTimestamp, endTimestamp, modelName, username, tokenName, channel, group, requestId, upstreamRequestId)
	if err != nil {
		common.ApiErrorI18n(c, i18n.MsgDatabaseError)
		return
	}
	if total > logExportMaxCount {
		common.ApiErrorI18n(c, i18n.MsgLogExportTooMany)
		return
	}

	filename := fmt.Sprintf("statement_%s.csv", time.Now().In(logExportTimeZone).Format("20060102_150405"))
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	c.Header("Transfer-Encoding", "chunked")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)

	// Excel assumes the system codepage for CSV unless the file starts with a
	// UTF-8 BOM, which would garble non-ASCII usernames and model names in the
	// spreadsheet a customer actually opens.
	_, _ = c.Writer.WriteString("\ufeff")

	w := csv.NewWriter(c.Writer)
	_ = w.Write(logCSVHeader)

	// Resolve the currency once so every row in a file shares one basis, even if
	// an admin changes the rate while the export is still streaming.
	currency, rate := exportCurrency()

	err = model.StreamAllLogsForExport(
		logType, startTimestamp, endTimestamp,
		modelName, username, tokenName,
		channel, group, requestId, upstreamRequestId,
		1000,
		func(logs []*model.Log) error {
			for _, l := range logs {
				if err := w.Write(logToCSVRow(l, currency, rate)); err != nil {
					return err
				}
			}
			w.Flush()
			c.Writer.Flush()
			return w.Error()
		},
	)
	if err != nil {
		common.SysError("log export stream error: " + err.Error())
	}
}
