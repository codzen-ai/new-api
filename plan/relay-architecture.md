# New API — LLM 请求/响应转发架构总结

## 一、整体架构概览

New API 采用**适配器模式 (Adaptor Pattern)** 实现 40+ 上游 AI 提供商的统一代理。客户端使用 OpenAI 兼容 API 发送请求，系统自动选择合适的上游渠道，将请求转换为目标提供商格式，转发并将响应转换回客户端期望的格式。

```
客户端请求 (OpenAI/Claude/Gemini 格式)
    ↓
┌──────────────────────────────────────────┐
│           Router (路由层)                 │
│  relay-router.go — 定义所有 relay 路由    │
└──────────────┬───────────────────────────┘
               ↓
┌──────────────────────────────────────────┐
│         Middleware (中间件层)              │
│  TokenAuth → Distribute → RateLimit      │
│  认证 → 渠道选择 → 限流                    │
└──────────────┬───────────────────────────┘
               ↓
┌──────────────────────────────────────────┐
│        Controller (控制器层)               │
│  relay.go — 解析请求、生成 RelayInfo、     │
│  预扣费、重试调度                          │
└──────────────┬───────────────────────────┘
               ↓
┌──────────────────────────────────────────┐
│          Relay Handler (转发处理层)        │
│  TextHelper / ImageHelper / AudioHelper  │
│  EmbeddingHelper / RerankHelper 等        │
└──────────────┬───────────────────────────┘
               ↓
┌──────────────────────────────────────────┐
│        Adaptor (适配器层)                  │
│  每个提供商实现统一的 Adaptor 接口          │
│  openai/ claude/ gemini/ aws/ ...        │
│  负责: 请求转换 → HTTP 调用 → 响应处理      │
└──────────────┬───────────────────────────┘
               ↓
          上游提供商 API
```

## 二、请求生命周期详解

### 2.1 路由入口 (`router/relay-router.go`)

所有 relay 请求通过统一路由注册，主要端点：

| 端点 | 用途 |
|------|------|
| `/v1/chat/completions` | Chat 对话补全 (最核心) |
| `/v1/completions` | 文本补全 |
| `/v1/messages` | Claude 原生格式 |
| `/v1/responses` | OpenAI Responses API |
| `/v1/embeddings` | 向量嵌入 |
| `/v1/images/*` | 图片生成/编辑 |
| `/v1/audio/*` | TTS/STT/转录 |
| `/v1/rerank` | 重排序 |
| `/v1/realtime` | WebSocket 实时 API |
| `/v1beta/models/*` | Gemini 原生格式 |

每个路由附带中间件链：`RouteTag → TokenAuth → Distribute → SystemPerformanceCheck → ModelRequestRateLimit`

### 2.2 中间件 — 渠道分发 (`middleware/distributor.go`)

**Distribute 中间件**负责选择上游渠道：
1. 检查 Token 是否绑定了特定渠道
2. 根据请求的模型名查找支持该模型的所有渠道
3. 从可用渠道中负载均衡选择一个
4. 将渠道信息（channel_id, channel_type, api_key, base_url 等）注入到 Gin Context 中

### 2.3 控制器调度 (`controller/relay.go`)

`Controller.Relay(c, relayFormat)` 是核心入口函数，流程：

```
1. GetAndValidateRequest() — 根据 relayFormat 解析请求体
   → dto.GeneralOpenAIRequest / dto.ClaudeRequest / dto.GeminiChatRequest

2. GenRelayInfo() — 生成 RelayInfo 上下文结构体
   → 包含认证信息、模型映射、渠道元数据、计费信息等

3. PreConsumeBilling() — 根据模型价格和预估 Token 数预扣费

4. 重试循环 (最多 RetryTimes 次，失败时切换渠道):
   → 根据 RelayFormat 分发到不同 Handler
   → 根据 RelayMode 进一步分发到具体处理函数

5. PostConsumeQuota() — 根据实际用量结算费用
```

**按格式分发：**
- `RelayFormatClaude` → `relay.ClaudeHelper()`
- `RelayFormatGemini` → `geminiRelayHandler()`
- `RelayFormatOpenAIRealtime` → `relay.WssHelper()`
- 其他 → `relayHandler()`

**按模式分发 (relayHandler 内部)：**
- `RelayModeImagesGenerations` → `relay.ImageHelper()`
- `RelayModeAudioSpeech` → `relay.AudioHelper()`
- `RelayModeEmbeddings` → `relay.EmbeddingHelper()`
- `RelayModeRerank` → `relay.RerankHelper()`
- `RelayModeResponses` → `relay.ResponsesHelper()`
- 默认 → `relay.TextHelper()` (Chat/Completion)

### 2.4 RelayInfo — 核心上下文 (`relay/common/relay_info.go`)

`RelayInfo` 是贯穿整个请求生命周期的中心数据结构：

```go
RelayInfo {
    // 认证
    TokenId, TokenKey, TokenGroup
    UserId, UsingGroup, UserGroup

    // 请求类型
    RelayFormat     // 客户端请求格式 (OpenAI/Claude/Gemini)
    RelayMode       // 请求模式 (Chat/Image/Audio/Embedding...)

    // 模型信息
    OriginModelName     // 用户请求的模型名
    UpstreamModelName   // 发往上游的实际模型名

    // 状态
    IsStream            // 是否流式
    Request             // 解析后的请求对象 (多态)

    // 渠道
    ChannelMeta         // 选中的渠道信息

    // 计费
    Billing             // 配额结算器

    // 格式转换追踪
    RequestConversionChain []RelayFormat  // 记录格式转换链路
}
```

### 2.5 适配器接口 (`relay/channel/adapter.go`)

所有提供商适配器实现统一的 `Adaptor` 接口：

```go
type Adaptor interface {
    // 初始化
    Init(info *RelayInfo)

    // 构建请求
    GetRequestURL(info *RelayInfo) (string, error)
    SetupRequestHeader(c *gin.Context, req *http.Header, info *RelayInfo) error

    // 请求格式转换 — 从不同客户端格式转为提供商格式
    ConvertOpenAIRequest(...)   (any, error)
    ConvertClaudeRequest(...)   (any, error)
    ConvertGeminiRequest(...)   (any, error)
    ConvertEmbeddingRequest(...)
    ConvertAudioRequest(...)
    ConvertImageRequest(...)
    ConvertRerankRequest(...)
    ConvertOpenAIResponsesRequest(...)

    // 执行请求
    DoRequest(c, info, requestBody) (any, error)

    // 处理响应
    DoResponse(c, resp, info) (usage, err)

    // 元数据
    GetModelList() []string
    GetChannelName() string
}
```

### 2.6 适配器选择 (`relay/relay_adaptor.go`)

根据渠道的 `APIType` 常量选择对应适配器：

```go
func GetAdaptor(apiType int) channel.Adaptor {
    switch apiType {
    case APITypeOpenAI:     return &openai.Adaptor{}
    case APITypeAnthropic:  return &claude.Adaptor{}
    case APITypeGemini:     return &gemini.Adaptor{}
    case APITypeAws:        return &aws.Adaptor{}
    // ... 30+ 提供商
    }
}
```

### 2.7 请求转发流程 (以 TextHelper 为例)

```
adaptor.Init(info)
    ↓
ModelMappedHelper() — 模型名映射 (用户模型 → 上游模型)
    ↓
adaptor.ConvertOpenAIRequest() — 构建提供商特定的请求体
    ↓
ApplyParamOverride() — 应用渠道级参数覆盖
    ↓
adaptor.DoRequest() — 执行 HTTP 请求
    ├── GetRequestURL() — 构建完整 URL
    ├── SetupRequestHeader() — 设置认证头 (Bearer Token / X-API-Key 等)
    └── HTTP POST → 上游 API
    ↓
adaptor.DoResponse() — 处理上游响应
    ├── [流式] SSE 事件逐条解析并转发给客户端
    └── [非流式] 读取完整响应、解析 JSON、返回给客户端
```

## 三、跨格式转换

系统支持**跨格式透明转换**，例如：

| 客户端格式 | 上游渠道 | 转换链 |
|-----------|---------|--------|
| OpenAI → | Claude 渠道 | `["openai", "claude"]` |
| Claude → | OpenAI 渠道 | `["claude", "openai"]` |
| Gemini → | OpenAI 渠道 | `["gemini", "openai"]` |
| OpenAI → | Gemini 渠道 | `["openai", "gemini"]` |

核心转换函数：
- `service.ClaudeToOpenAIRequest()` — Claude → OpenAI
- `RequestOpenAI2ClaudeMessage()` — OpenAI → Claude
- `service.GeminiToOpenAIRequest()` — Gemini → OpenAI

`RelayInfo.RequestConversionChain` 记录整个转换链路，用于日志和计费追踪。

## 四、流式 vs 非流式响应

### 流式 (Streaming)
```
客户端 "stream": true
    ↓
SetEventStreamHeaders() — 设置 Content-Type: text/event-stream
    ↓
StreamScannerHandler() — 逐行解析 SSE 事件
    ↓
每个 chunk: 解析 JSON → 提取 delta → 立即发送给客户端 (flush)
    ↓
最终 chunk: 提取 usage → 发送 [DONE] → 结算费用
```

### 非流式 (Non-Streaming)
```
客户端 "stream": false 或省略
    ↓
读取完整响应 body → Unmarshal JSON
    ↓
提取 usage (tokens) → 单次返回完整 JSON 响应
    ↓
结算费用
```

## 五、计费流程

1. **预扣费** (PreConsumeBilling): 根据模型价格 × 预估 Token 数，提前扣除用户配额
2. **实际结算** (PostConsumeQuota): 请求完成后，根据上游返回的实际 usage 调整配额
3. 支持免费模型、按比例计费、输入/输出分别定价

## 六、重试与容错

- 请求失败时自动重试，最多 `RetryTimes` 次
- 每次重试可以切换到不同的渠道 (failover)
- 记录每次尝试使用的渠道 ID，用于故障排查

## 七、关键文件索引

| 文件 | 职责 |
|------|------|
| `router/relay-router.go` | 路由定义 |
| `middleware/distributor.go` | 渠道选择/负载均衡 |
| `controller/relay.go` | 请求调度、预扣费、重试 |
| `relay/relay_adaptor.go` | 适配器工厂 |
| `relay/channel/adapter.go` | Adaptor 接口定义 |
| `relay/common/relay_info.go` | RelayInfo 核心上下文 |
| `relay/text_handler.go` | Chat/Completion 处理 |
| `relay/claude_handler.go` | Claude 格式处理 |
| `relay/gemini_handler.go` | Gemini 格式处理 |
| `relay/channel/openai/adaptor.go` | OpenAI 适配器 (覆盖 30+ 渠道类型) |
| `relay/channel/claude/adaptor.go` | Claude 适配器 |
| `relay/channel/gemini/adaptor.go` | Gemini 适配器 |
| `relay/channel/api_request.go` | HTTP/WebSocket 请求执行 |
| `relay/channel/openai/relay-openai.go` | OpenAI 流式/非流式响应处理 |
| `relay/channel/claude/relay-claude.go` | Claude SSE 响应处理 |
