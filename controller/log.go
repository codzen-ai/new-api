package controller

import (
	"encoding/base64"
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	i18n "github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
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
//
// A charge can also span several rows. An asynchronous task holds an estimate on
// submit and settles the difference when it finishes, so entry_type says what a
// row is and task_id groups the rows of one task; the amounts are signed, so
// summing total_amount over the group — or over the whole file — gives what was
// actually charged.
var logCSVHeader = []string{
	"time", "request_id", "username", "token_name", "model", "task_id", "entry_type", "billing_mode", "tier",
	"input_tokens", "input_price_per_1m", "input_amount",
	"cache_read_tokens", "cache_read_price_per_1m", "cache_read_amount",
	"cache_write_tokens", "cache_write_price_per_1m", "cache_write_amount",
	"output_tokens", "output_price_per_1m", "output_amount",
	"other_amount", "calculation", "total_amount", "currency", "exchange_rate",
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
// the task id, entry type, billing mode and tier, four token/price/amount
// triples, the other and total amounts, and the arithmetic tying them together.
const logBillingColumnCount = 19

// Column offsets within the slice logBillingColumns returns.
const (
	colTaskId = iota
	colEntryType
	colBillingMode
	colTier
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
	colCalculation
	colTotalAmount
)

// What a row is within its charge. A plain request is one consume row and needs
// no explanation; a task splits into a hold taken on submit and the settlement
// that corrects it minutes later, and those two only make sense as a pair.
const (
	entryTypeConsume     = "consume"
	entryTypePrepaidHold = "prepaid_hold"
	entryTypeSettlement  = "settlement"
	entryTypeRefund      = "refund"
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
	// A refund returns money, so it enters the statement as a negative amount.
	// The log stores every quota as a magnitude, which would otherwise make a
	// settled task read as if it had been charged twice.
	totalQuota := decimal.NewFromInt(int64(l.Quota))
	if l.Type == model.LogTypeRefund {
		totalQuota = totalQuota.Neg()
	}
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

	taskId, _ := other["task_id"].(string)
	cols[colTaskId] = spreadsheetSafe(taskId)

	modelPrice, hasModelPrice := number("model_price")
	perCall := hasModelPrice && modelPrice.IsPositive()
	cols[colBillingMode] = "per_token"
	if perCall {
		cols[colBillingMode] = "per_call"
	}

	cols[colEntryType] = logEntryType(l, other, perCall)
	if settlementBillingColumns(cols, l, other, totalQuota, rate) {
		return cols
	}
	if perCall {
		cols[colOtherAmount] = quotaToAmount(totalQuota, rate)
		return cols
	}

	// Which tier priced the request is a fact the log recorded, so it is
	// reported whether or not the breakdown below could be recovered — it is
	// the answer to "why is this row's unit price different from that one's".
	// Ratio-priced models have no tiers and leave the cell empty.
	if mode, _ := other["billing_mode"].(string); mode == "tiered_expr" {
		tier, _ := other["matched_tier"].(string)
		cols[colTier] = spreadsheetSafe(tier)
	}

	if tieredBillingColumns(cols, l, other, totalQuota, rate) {
		return cols
	}

	modelRatio, hasModelRatio := number("model_ratio")
	groupRatio, hasGroupRatio := number("group_ratio")
	// A non-positive model ratio is not a free request priced at zero, it is a
	// request the ratios do not describe — expression pricing snapshots the
	// ratio as 0. Quoting 0.00 per million there would read as "these tokens
	// were free" while the whole charge sat unexplained in other_amount.
	if !hasModelRatio || !hasGroupRatio || !modelRatio.IsPositive() {
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

// logEntryType names what a row is within the charge it belongs to. A plain
// request is a single consume row, but an asynchronous task (video generation
// and the like) charges an estimate when it is submitted and corrects it with a
// second row minutes later, once the upstream reports what was actually used.
// Without this column a statement reader cannot tell the estimate apart from the
// correction, and reads a task's hold as its price.
//
// Per-call tasks are excluded: a fixed price is final at submit time and never
// gets a second row, so that charge is an ordinary consume entry.
func logEntryType(l *model.Log, other map[string]any, perCall bool) string {
	if l.Type == model.LogTypeRefund {
		return entryTypeRefund
	}
	if _, hasHold := other["pre_consumed_quota"]; hasHold {
		return entryTypeSettlement
	}
	if isTask, _ := other["is_task"].(bool); isTask && !perCall {
		return entryTypePrepaidHold
	}
	return entryTypeConsume
}

// settlementBillingColumns fills the breakdown for the rows a two-step charge
// produces — the hold taken when an asynchronous task is submitted, and the
// settlement or refund that corrects it. Neither row is a priced request on its
// own, so neither can be itemised the way logBillingColumns itemises a request.
//
// A settlement that knows the usage it was computed from states it: the amount
// finally settled goes on the output line at the price it works out to, and the
// remainder is the hold this row reverses — negative, which is exactly why the
// rows of one task sum to what the task cost. A hold, or a settlement whose
// usage the upstream never returned, has nothing to price against and reports
// its amount whole instead of quoting a unit price against zero tokens.
//
// Returns false for an ordinary consume row, leaving cols to the caller.
func settlementBillingColumns(cols []string, l *model.Log, other map[string]any, totalQuota, rate decimal.Decimal) bool {
	switch cols[colEntryType] {
	case entryTypePrepaidHold, entryTypeSettlement, entryTypeRefund:
	default:
		return false
	}

	settledQuota, hasSettled := other["actual_quota"].(float64)
	tokens := decimal.NewFromInt(int64(l.CompletionTokens))
	if !hasSettled || settledQuota <= 0 || !tokens.IsPositive() {
		cols[colOtherAmount] = quotaToAmount(totalQuota, rate)
		return true
	}

	settled := decimal.NewFromFloat(settledQuota)
	cols[colOutputTokens] = tokens.String()
	cols[colOutputPrice] = quotaToAmount(settled.Div(tokens).Mul(tokensPerPriceUnit), rate)
	cols[colOutputAmount] = quotaToAmount(settled, rate)
	cols[colOtherAmount] = quotaToAmount(totalQuota.Sub(settled), rate)
	return true
}

// tieredBillingColumns fills the breakdown for a log priced by a billing
// expression (see pkg/billingexpr/expr.md) rather than by model ratios. Those
// requests snapshot no usable ratios, so without this the entire charge would
// land in other_amount with nothing for the customer to check it against.
//
// The log carries the expression that priced the request, so the split is
// recovered by replaying it: pkg/billingexpr attributes the cost to each token
// dimension, and the per-million price follows from the dimension's own cost.
// Replaying reads the frozen expression from the log, never current settings,
// so a later price change cannot alter a statement already sent.
//
// A replay can be incomplete — an expression may branch on request headers or
// body fields this function has no access to, or price a dimension the consume
// log never recorded. Both would understate a component's tokens and overstate
// its cost, so the replay is published only when it lands in the tier that was
// actually billed and costs no more than the charge on record. Otherwise the
// row falls back to reporting the whole charge as other_amount, which is
// unhelpful but never wrong.
//
// Returns false when the log is not expression-priced or the replay could not
// be trusted, leaving cols untouched for the caller's ratio-based path.
func tieredBillingColumns(cols []string, l *model.Log, other map[string]any, totalQuota, rate decimal.Decimal) bool {
	if mode, _ := other["billing_mode"].(string); mode != "tiered_expr" {
		return false
	}
	encoded, _ := other["expr_b64"].(string)
	exprStr, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(exprStr) == 0 {
		return false
	}
	groupRatio, ok := other["group_ratio"].(float64)
	if !ok || groupRatio <= 0 {
		return false
	}

	usedVars := billingexpr.UsedVars(string(exprStr))
	// Image and audio output tokens are not recorded on a consume log. An
	// expression pricing them separately means the completion total on record
	// still contains them, so neither the output line nor the remainder can be
	// stated correctly and the row is left to the fallback.
	if usedVars["img_o"] || usedVars["ao"] {
		return false
	}

	params := tieredTokenParams(l, other, usedVars)
	split, err := billingexpr.SplitCost(string(exprStr), params)
	if err != nil {
		return false
	}
	// The tier settlement recorded is the one the customer was charged under.
	// Its absence means settlement never got a price out of the expression and
	// charged the pre-consumed estimate instead, so the tokens on the row do
	// not explain the charge and must not be dressed up as if they did.
	tier, settled := other["matched_tier"].(string)
	if !settled || tier != split.Tier {
		return false
	}

	// v1 expression coefficients are prices per million tokens, so a cost
	// becomes quota the same way settlement converts it.
	quotaPerUnit := decimal.NewFromFloat(common.QuotaPerUnit).Mul(decimal.NewFromFloat(groupRatio)).Div(tokensPerPriceUnit)
	// Settlement rounds the total to whole quota, so a replay may legitimately
	// land within one unit of the charge; anything above that is a replay that
	// priced something the request never paid for.
	if decimal.NewFromFloat(split.Total).Mul(quotaPerUnit).Sub(totalQuota).GreaterThan(decimal.NewFromInt(1)) {
		return false
	}

	components := []struct {
		tokensCol, priceCol, amountCol int
		tokens                         decimal.Decimal
		cost                           float64
	}{
		{colInputTokens, colInputPrice, colInputAmount, decimal.NewFromFloat(params.P), split.Costs["p"]},
		{colCacheReadTokens, colCacheReadPrice, colCacheReadAmount, decimal.NewFromFloat(params.CR), split.Costs["cr"]},
		// A Claude request can create 5m and 1h cache entries at once; the row
		// reports them as one line at the blended price actually paid.
		{colCacheWriteTokens, colCacheWritePrice, colCacheWriteAmount, decimal.NewFromFloat(params.CC + params.CC1h), split.Costs["cc"] + split.Costs["cc1h"]},
		{colOutputTokens, colOutputPrice, colOutputAmount, decimal.NewFromFloat(params.C), split.Costs["c"]},
	}
	accounted := decimal.Zero
	for _, component := range components {
		if !component.tokens.IsPositive() {
			continue
		}
		quota := decimal.NewFromFloat(component.cost).Mul(quotaPerUnit)
		accounted = accounted.Add(quota)
		cols[component.tokensCol] = component.tokens.String()
		cols[component.priceCol] = quotaToAmount(quota.Div(component.tokens).Mul(tokensPerPriceUnit), rate)
		cols[component.amountCol] = quotaToAmount(quota, rate)
	}
	cols[colOtherAmount] = quotaToAmount(totalQuota.Sub(accounted), rate)
	return true
}

// tieredTokenParams rebuilds the token dimensions an expression was evaluated
// against from what the consume log kept. It mirrors
// service.BuildTieredTokenParams, including how a dimension the expression
// prices separately is carved out of the prompt total, so a replay sees the
// same numbers settlement did. The caller has already established that the
// expression prices nothing the log failed to record.
func tieredTokenParams(l *model.Log, other map[string]any, usedVars map[string]bool) billingexpr.TokenParams {
	tokens := func(key string) float64 {
		value, _ := other[key].(float64)
		if value < 0 {
			return 0
		}
		return value
	}

	params := billingexpr.TokenParams{
		P:   float64(l.PromptTokens),
		C:   float64(l.CompletionTokens),
		CR:  tokens("cache_tokens"),
		Img: tokens("image_output"),
		AI:  tokens("audio_input_token_count"),
	}

	_, isClaude := other["claude"].(bool)
	if isClaude {
		// Anthropic splits cache creation by TTL and reports cache traffic
		// outside the prompt total, so the context length has to be summed up.
		params.CC = tokens("cache_creation_tokens_5m")
		params.CC1h = tokens("cache_creation_tokens_1h")
		params.Len = params.P + params.CR + params.CC + params.CC1h
		return params
	}

	// Everything else reports cache, image and audio traffic inside the prompt
	// total, so a dimension the expression prices on its own comes back out.
	params.CC = tokens("cache_creation_tokens")
	params.Len = params.P
	if usedVars["cr"] {
		params.P -= params.CR
	}
	if usedVars["cc"] {
		params.P -= params.CC
	}
	if usedVars["img"] {
		params.P -= params.Img
	}
	if usedVars["ai"] {
		params.P -= params.AI
	}
	// OpenAI reports unadjusted cache prefix counts, so the carved dimensions
	// can exceed the prompt total; settlement clamps at zero and so does this.
	if params.P < 0 {
		params.P = 0
	}
	return params
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

// billingCalculation writes out the arithmetic behind a row's charge, so a
// customer sees where the number came from without having to read a guide or
// rebuild the formula in a spreadsheet. It is deliberately plain text rather
// than a spreadsheet formula: a statement is a record of what was charged, and
// a live formula would recompute — and silently disagree with itself — the
// moment a recipient sorted, filtered or edited the sheet.
//
// Every number in it appears verbatim in another column of the same row, so a
// customer can trace each term back to the cell it came from.
//
// Returns empty for a row with no itemisation to explain, matching the empty
// cells the rest of that row already carries.
func billingCalculation(cols []string) string {
	terms := make([]string, 0, 5)
	for _, component := range [][2]int{
		{colInputTokens, colInputPrice},
		{colCacheReadTokens, colCacheReadPrice},
		{colCacheWriteTokens, colCacheWritePrice},
		{colOutputTokens, colOutputPrice},
	} {
		if cols[component[0]] == "" {
			continue
		}
		terms = append(terms, fmt.Sprintf("%s×%s÷1M", cols[component[0]], cols[component[1]]))
	}
	if len(terms) == 0 {
		return ""
	}

	// The remainder is already an amount, so it joins as a plain addend. It is
	// left out when it rounds away to nothing, which is the common case: a row
	// reading "+ 0.000000" would invite the question the column exists to
	// answer.
	other := cols[colOtherAmount]
	switch {
	case other == "" || other == "0.000000" || other == "-0.000000":
	case strings.HasPrefix(other, "-"):
		terms = append(terms, "- "+strings.TrimPrefix(other, "-"))
	default:
		terms = append(terms, "+ "+other)
	}

	// The leading terms are joined with the operator the trailing ones carry
	// themselves, since a negative remainder subtracts rather than adds.
	joined := terms[0]
	for _, term := range terms[1:] {
		if strings.HasPrefix(term, "- ") || strings.HasPrefix(term, "+ ") {
			joined += " " + term
			continue
		}
		joined += " + " + term
	}
	return joined + " = " + cols[colTotalAmount]
}

// spreadsheetSafe neutralises a text field a spreadsheet would otherwise run as
// a formula. Names are chosen by users, so a token named "=1+1" — or worse —
// must reach the customer as the text it is. The leading apostrophe marks the
// cell as text in Excel and Sheets without being displayed; other readers show
// it literally, which is the lesser evil against a statement that executes
// something on open.
func spreadsheetSafe(value string) string {
	if value == "" {
		return value
	}
	switch value[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + value
	}
	return value
}

func logToCSVRow(l *model.Log, currency string, rate decimal.Decimal) []string {
	row := []string{
		time.Unix(l.CreatedAt, 0).In(logExportTimeZone).Format("2006-01-02 15:04:05"),
		l.RequestId,
		spreadsheetSafe(l.Username),
		spreadsheetSafe(l.TokenName),
		spreadsheetSafe(l.ModelName),
	}
	billing := logBillingColumns(l, rate)
	billing[colCalculation] = billingCalculation(billing)
	row = append(row, billing...)
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

	// A statement has to be net. An asynchronous task charges an estimate when it
	// is submitted and settles the difference minutes later, so consume rows on
	// their own bill every video at its estimate — which for a short clip is
	// several times what it cost. The refunds that offset them are therefore
	// exported alongside, carrying a negative amount, and entry_type tells the
	// reader which row is which. This is the one place the file deliberately
	// holds more than the log view on screen.
	logTypes := []int{logType}
	switch logType {
	case model.LogTypeUnknown:
		logTypes = nil
	case model.LogTypeConsume:
		logTypes = []int{model.LogTypeConsume, model.LogTypeRefund}
	}

	total, err := model.CountAllLogsForExport(logTypes, startTimestamp, endTimestamp, modelName, username, tokenName, channel, group, requestId, upstreamRequestId)
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
		logTypes, startTimestamp, endTimestamp,
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
