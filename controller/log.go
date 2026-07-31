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
