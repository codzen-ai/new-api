package controller

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

// logFilterFromQuery reads the log search shared by the log list and the log
// export. Username and channel are read only for administrators.
func logFilterFromQuery(c *gin.Context, admin bool) model.LogFilter {
	logType, _ := strconv.Atoi(c.Query("type"))
	startTimestamp, _ := strconv.ParseInt(c.Query("start_timestamp"), 10, 64)
	endTimestamp, _ := strconv.ParseInt(c.Query("end_timestamp"), 10, 64)
	filter := model.LogFilter{
		Type:              logType,
		StartTimestamp:    startTimestamp,
		EndTimestamp:      endTimestamp,
		ModelName:         c.Query("model_name"),
		TokenName:         c.Query("token_name"),
		Group:             c.Query("group"),
		RequestId:         c.Query("request_id"),
		UpstreamRequestId: c.Query("upstream_request_id"),
	}
	if admin {
		filter.Username = c.Query("username")
		filter.Channel, _ = strconv.Atoi(c.Query("channel"))
	}
	return filter
}

// limitLogTimeRange returns the start of a log or dashboard search kept within
// the longest log time range. A missing start reaches back the whole range, and
// time after now does not count toward it.
func limitLogTimeRange(startTimestamp int64, endTimestamp int64) (int64, error) {
	maxRange := operation_setting.GetLogSetting().MaxRangeSeconds()
	end := common.GetTimestamp()
	if endTimestamp != 0 {
		end = min(end, endTimestamp)
	}
	if startTimestamp == 0 {
		return end - maxRange, nil
	}
	if end-startTimestamp > maxRange {
		return 0, common.NewMessage("The time range cannot exceed {{days}} days", map[string]any{"days": maxRange / (24 * 60 * 60)})
	}
	return startTimestamp, nil
}

func GetAllLogs(c *gin.Context) {
	pageInfo := common.GetPageQuery(c)
	logs, total, err := model.GetAllLogs(logFilterFromQuery(c, true), pageInfo.GetStartIdx(), pageInfo.GetPageSize())
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if c.GetInt("role") < common.RoleRootUser {
		model.FormatAdminLogs(logs)
	} else {
		model.FormatRootLogs(logs)
	}
	pageInfo.SetTotal(int(total))
	pageInfo.SetItems(logs)
	common.ApiSuccess(c, pageInfo)
	return
}

func GetUserLogs(c *gin.Context) {
	pageInfo := common.GetPageQuery(c)
	filter := logFilterFromQuery(c, false)
	if c.GetInt("role") < common.RoleAdminUser {
		startTimestamp, err := limitLogTimeRange(filter.StartTimestamp, filter.EndTimestamp)
		if err != nil {
			common.ApiError(c, err)
			return
		}
		filter.StartTimestamp = startTimestamp
	}
	logs, total, err := model.GetUserLogs(c.GetInt("id"), filter, pageInfo.GetStartIdx(), pageInfo.GetPageSize())
	if err != nil {
		common.ApiError(c, err)
		return
	}
	pageInfo.SetTotal(int(total))
	pageInfo.SetItems(logs)
	common.ApiSuccess(c, pageInfo)
	return
}

// ExportAllLogs streams every user's logs matching the search.
func ExportAllLogs(c *gin.Context) {
	exportLogs(c, true)
}

// ExportUserLogs streams the caller's own logs matching the search.
func ExportUserLogs(c *gin.Context) {
	if c.GetInt("role") < common.RoleAdminUser && !operation_setting.GetLogSetting().UserExportEnabled {
		common.ApiErrorT(c, "Log export is disabled")
		return
	}
	exportLogs(c, false)
}

// exportLogs writes one JSON log per line, newest first, and ends with an API
// response object: success with the log count, or the failure. Each user runs
// one export at a time, and every export stays within the longest log time
// range.
func exportLogs(c *gin.Context, all bool) {
	filter := logFilterFromQuery(c, all)
	startTimestamp, err := limitLogTimeRange(filter.StartTimestamp, filter.EndTimestamp)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	filter.StartTimestamp = startTimestamp

	userId := c.GetInt("id")
	release, ok, err := service.AcquireLogExportLock(userId)
	if err != nil {
		common.SysError(common.LogText("failed to lock log export: %s", err.Error()))
		common.ApiErrorT(c, "Failed to export logs")
		return
	}
	if !ok {
		common.ApiErrorT(c, "Another log export is running. Try again after it finishes.")
		return
	}
	defer release()

	c.Header("Content-Type", "application/x-ndjson; charset=utf-8")
	c.Header("Cache-Control", "no-store")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)

	count := 0
	writeLog := func(log *model.Log) error {
		line, err := common.Marshal(log)
		if err != nil {
			return err
		}
		if _, err := c.Writer.Write(append(line, '\n')); err != nil {
			return err
		}
		count++
		return nil
	}
	ctx := c.Request.Context()
	if all {
		err = model.StreamAllLogs(ctx, filter, c.GetInt("role"), writeLog)
	} else {
		err = model.StreamUserLogs(ctx, userId, filter, writeLog)
	}
	if ctx.Err() != nil {
		return
	}
	// The status is already sent, so the result object becomes the last line.
	if err != nil {
		common.SysError(common.LogText("failed to export logs: %s", err.Error()))
		common.ApiErrorT(c, "Failed to export logs")
		return
	}
	common.ApiSuccess(c, gin.H{"count": count})
}

// Deprecated: SearchAllLogs 已废弃，前端未使用该接口。
func SearchAllLogs(c *gin.Context) {
	common.ApiErrorT(c, "This API is deprecated")
}

// Deprecated: SearchUserLogs 已废弃，前端未使用该接口。
func SearchUserLogs(c *gin.Context) {
	common.ApiErrorT(c, "This API is deprecated")
}

func GetLogByKey(c *gin.Context) {
	tokenId := c.GetInt("token_id")
	if tokenId == 0 {
		common.ApiErrorI18n(c, i18n.MsgTokenInvalid)
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
	if c.GetInt("role") < common.RoleAdminUser {
		var err error
		if startTimestamp, err = limitLogTimeRange(startTimestamp, endTimestamp); err != nil {
			common.ApiError(c, err)
			return
		}
	}
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

var logCSVHeader = []string{
	"id", "created_at", "type", "username", "token_name", "model_name",
	"channel", "channel_name", "prompt_tokens", "completion_tokens",
	"quota", "usd", "cny", "use_time", "is_stream", "token_id", "group", "ip",
	"request_id", "usage_semantic",
	"cache_tokens", "cache_creation_tokens", "cache_creation_tokens_5m", "cache_creation_tokens_1h",
	"cache_ratio", "cache_creation_ratio", "cache_creation_ratio_5m", "cache_creation_ratio_1h",
	"cache_read_usd", "cache_write_usd", "cache_saving_usd",
	"content",
}

// logCacheColumnCount is the number of columns produced by logCacheColumns,
// covering the usage semantic marker, cache token counts, cache ratios and the
// recomputed cache spend.
const logCacheColumnCount = 12

// logExportTimeZone is the fixed UTC+8 zone used to render exported timestamps.
var logExportTimeZone = time.FixedZone("UTC+8", 8*60*60)

// logCacheColumns derives the cache usage, cache ratios and cache spend columns
// from a log's Other payload, which is where the billing pipeline records them
// (see service/log_info_generate.go).
//
// The ratios stored there are a snapshot taken at billing time, so recomputing
// spend from them stays correct even after the model ratios are later changed.
// Spend follows service/text_quota.go: cache tokens are charged at
// cache_ratio × model_ratio × group_ratio, and Claude splits cache writes into
// 5m/1h buckets with their own ratios. group_ratio is the ratio billing
// actually applied; user_group_ratio is display-only and must not be used here.
//
// Missing values are rendered as an empty string rather than "0", so that a
// field the provider never reported is not mistaken for a measured zero. Callers
// get exactly logCacheColumnCount values regardless of the payload.
func logCacheColumns(otherStr string) []string {
	cols := make([]string, logCacheColumnCount)
	if otherStr == "" {
		return cols
	}
	other, err := common.StrToMap(otherStr)
	if err != nil || other == nil {
		return cols
	}

	number := func(key string) (decimal.Decimal, bool) {
		value, ok := other[key].(float64)
		if !ok {
			return decimal.Zero, false
		}
		return decimal.NewFromFloat(value), true
	}
	tokens := func(key string) string {
		value, ok := number(key)
		if !ok {
			return ""
		}
		return value.String()
	}

	isClaude, _ := other["claude"].(bool)
	if isClaude {
		cols[0] = "anthropic"
	} else {
		cols[0] = "openai"
	}

	cols[1] = tokens("cache_tokens")
	cols[2] = tokens("cache_creation_tokens")
	cols[3] = tokens("cache_creation_tokens_5m")
	cols[4] = tokens("cache_creation_tokens_1h")

	// Per-call pricing ignores token ratios entirely, so emitting ratios or a
	// recomputed spend for those rows would be fabricated. Usage above still
	// reflects the cache traffic that really happened.
	if modelPrice, ok := number("model_price"); ok && modelPrice.IsPositive() {
		return cols
	}

	cols[5] = tokens("cache_ratio")
	cols[6] = tokens("cache_creation_ratio")
	cols[7] = tokens("cache_creation_ratio_5m")
	cols[8] = tokens("cache_creation_ratio_1h")

	modelRatio, hasModelRatio := number("model_ratio")
	groupRatio, hasGroupRatio := number("group_ratio")
	if !hasModelRatio || !hasGroupRatio {
		return cols
	}
	ratio := modelRatio.Mul(groupRatio)

	cacheTokens, hasCacheTokens := number("cache_tokens")
	cacheRatio, hasCacheRatio := number("cache_ratio")
	if hasCacheTokens && hasCacheRatio {
		cols[9] = quotaToUSDString(cacheTokens.Mul(cacheRatio).Mul(ratio))
		saving := cacheTokens.Mul(decimal.NewFromInt(1).Sub(cacheRatio)).Mul(ratio)
		cols[11] = quotaToUSDString(saving)
	}

	creationTokens, hasCreationTokens := number("cache_creation_tokens")
	creationRatio, hasCreationRatio := number("cache_creation_ratio")
	tokens5m, has5m := number("cache_creation_tokens_5m")
	tokens1h, has1h := number("cache_creation_tokens_1h")
	if hasCreationTokens && hasCreationRatio {
		var writeQuota decimal.Decimal
		if isClaude && (has5m || has1h) {
			ratio5m, _ := number("cache_creation_ratio_5m")
			ratio1h, _ := number("cache_creation_ratio_1h")
			remaining := creationTokens.Sub(tokens5m).Sub(tokens1h)
			if remaining.IsNegative() {
				remaining = decimal.Zero
			}
			writeQuota = remaining.Mul(creationRatio).
				Add(tokens5m.Mul(ratio5m)).
				Add(tokens1h.Mul(ratio1h))
		} else {
			writeQuota = creationTokens.Mul(creationRatio)
		}
		cols[10] = quotaToUSDString(writeQuota.Mul(ratio))
	}

	return cols
}

// quotaToUSDString converts a quota amount to the dollar figure shown in the
// export, matching the precision of the existing usd column.
func quotaToUSDString(quota decimal.Decimal) string {
	usd := quota.Div(decimal.NewFromFloat(common.QuotaPerUnit))
	return usd.StringFixed(6)
}

func logToCSVRow(l *model.Log) []string {
	isStream := "false"
	if l.IsStream {
		isStream = "true"
	}
	usd := float64(l.Quota) / common.QuotaPerUnit
	cny := usd * operation_setting.USDExchangeRate
	row := []string{
		strconv.Itoa(l.Id),
		time.Unix(l.CreatedAt, 0).In(logExportTimeZone).Format("2006-01-02 15:04:05"),
		strconv.Itoa(l.Type),
		l.Username,
		l.TokenName,
		l.ModelName,
		strconv.Itoa(l.ChannelId),
		l.ChannelName,
		strconv.Itoa(l.PromptTokens),
		strconv.Itoa(l.CompletionTokens),
		strconv.Itoa(l.Quota),
		strconv.FormatFloat(usd, 'f', 6, 64),
		strconv.FormatFloat(cny, 'f', 6, 64),
		strconv.Itoa(l.UseTime),
		isStream,
		strconv.Itoa(l.TokenId),
		l.Group,
		l.Ip,
		l.RequestId,
	}
	row = append(row, logCacheColumns(l.Other)...)
	// content goes last because it can be long enough to make the trailing
	// columns hard to read in a spreadsheet.
	return append(row, l.Content)
}

// ExportLogStatement streams the customer reconciliation statement as CSV. It is
// separate from ExportAllLogs, which exports the log table as the console shows it.
func ExportLogStatement(c *gin.Context) {
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

	filename := fmt.Sprintf("logs_%s.csv", time.Now().In(logExportTimeZone).Format("20060102_150405"))
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	c.Header("Transfer-Encoding", "chunked")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)

	w := csv.NewWriter(c.Writer)
	_ = w.Write(logCSVHeader)

	err = model.StreamAllLogsForExport(
		logType, startTimestamp, endTimestamp,
		modelName, username, tokenName,
		channel, group, requestId, upstreamRequestId,
		1000,
		func(logs []*model.Log) error {
			for _, l := range logs {
				if err := w.Write(logToCSVRow(l)); err != nil {
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
