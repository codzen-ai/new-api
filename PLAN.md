# 日志导出功能缺失缓存字段问题分析与实现方案

## 问题分析

在 commit `b8b0ba61` 中实现的日志导出功能的 CSV 导出中，缺失了与大模型计费相关的缓存字段。

### 问题详情

#### 1. **缺失的缓存相关字段**

当前 CSV 导出的字段（`controller/log.go` 第 179-184 行）：
```go
var logCSVHeader = []string{
    "id", "created_at", "type", "username", "token_name", "model_name",
    "channel", "channel_name", "prompt_tokens", "completion_tokens",
    "quota", "use_time", "is_stream", "token_id", "group", "ip",
    "request_id", "content",
}
```

**缺失的字段**（存储在 `Log.Other` JSON 中）：
- `cache_tokens` - 缓存读取的 tokens（Claude 等大模型使用）
- `cache_creation_tokens` - 缓存写入的 tokens
- `cache_creation_tokens_5m` - 5分钟缓存写入 tokens
- `cache_creation_tokens_1h` - 1小时缓存写入 tokens

#### 2. **缓存字段的来源**

这些字段存储在 `Log.Other` 字段中，是一个 JSON 字符串，包含了大模型 API 返回的缓存相关信息。

参考 commit `c01bbd00`（feat: logs cache field #2920），前端已经支持显示这些缓存字段，但 CSV 导出功能中没有包含。

#### 3. **计费影响**

根据 Anthropic 协议，Claude API 的计费规则：
- `prompt_tokens` - 仅统计非缓存输入
- `cache_tokens` - 缓存读取的 tokens（计费较低）
- `cache_creation_tokens` - 缓存写入的 tokens（计费较高）

这些字段对于准确的成本分析和计费至关重要。

## 实现方案

### 方案 1：直接在 CSV 导出中添加缓存字段（推荐）

修改 `controller/log.go` 中的 CSV 导出逻辑，从 `Log.Other` JSON 中提取缓存字段：

#### 步骤 1：更新 CSV 表头

```go
var logCSVHeader = []string{
    "id", "created_at", "type", "username", "token_name", "model_name",
    "channel", "channel_name", "prompt_tokens", "completion_tokens",
    "quota", "use_time", "is_stream", "token_id", "group", "ip",
    "request_id", "cache_tokens", "cache_creation_tokens",
    "cache_creation_tokens_5m", "cache_creation_tokens_1h", "content",
}
```

#### 步骤 2：创建辅助函数解析缓存字段

```go
// extractCacheTokens 从 Log.Other JSON 中提取缓存相关字段
func extractCacheTokens(otherStr string) (cacheTokens, cacheCreationTokens, cacheCreationTokens5m, cacheCreationTokens1h string) {
    if otherStr == "" {
        return "0", "0", "0", "0"
    }

    var otherMap map[string]interface{}
    if err := common.Unmarshal([]byte(otherStr), &otherMap); err != nil {
        return "0", "0", "0", "0"
    }

    // 提取缓存字段，如果不存在则返回 "0"
    cacheTokens = toString(otherMap["cache_tokens"], "0")
    cacheCreationTokens = toString(otherMap["cache_creation_tokens"], "0")
    cacheCreationTokens5m = toString(otherMap["cache_creation_tokens_5m"], "0")
    cacheCreationTokens1h = toString(otherMap["cache_creation_tokens_1h"], "0")

    return
}

// toString 将 interface{} 转换为字符串
func toString(v interface{}, defaultVal string) string {
    if v == nil {
        return defaultVal
    }
    switch val := v.(type) {
    case string:
        return val
    case float64:
        return strconv.FormatInt(int64(val), 10)
    case int:
        return strconv.Itoa(val)
    default:
        return defaultVal
    }
}
```

#### 步骤 3：更新 logToCSVRow 函数

```go
func logToCSVRow(l *model.Log) []string {
    isStream := "false"
    if l.IsStream {
        isStream = "true"
    }

    cacheTokens, cacheCreationTokens, cacheCreationTokens5m, cacheCreationTokens1h := extractCacheTokens(l.Other)

    return []string{
        strconv.Itoa(l.Id),
        time.Unix(l.CreatedAt, 0).UTC().Format("2006-01-02 15:04:05"),
        strconv.Itoa(l.Type),
        l.Username,
        l.TokenName,
        l.ModelName,
        strconv.Itoa(l.ChannelId),
        l.ChannelName,
        strconv.Itoa(l.PromptTokens),
        strconv.Itoa(l.CompletionTokens),
        strconv.Itoa(l.Quota),
        strconv.Itoa(l.UseTime),
        isStream,
        strconv.Itoa(l.TokenId),
        l.Group,
        l.Ip,
        l.RequestId,
        cacheTokens,
        cacheCreationTokens,
        cacheCreationTokens5m,
        cacheCreationTokens1h,
        l.Content,
    }
}
```

**优点**：
- 简单直接，只需修改 CSV 导出逻辑
- 不需要修改数据库或 Log 结构体
- 充分利用现有的 `Log.Other` 字段
- 改动最小，风险最低
- 无需数据库迁移
- 快速解决问题

## 实现步骤

1. 在 `controller/log.go` 中添加 `extractCacheTokens()` 和 `toString()` 函数
2. 更新 `logCSVHeader` 变量
3. 修改 `logToCSVRow()` 函数
4. 测试 CSV 导出是否包含缓存字段
