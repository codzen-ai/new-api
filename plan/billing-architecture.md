# New API — 计费方案架构总结

## 一、计费模型概览

系统使用统一的 **Quota（配额）积分制**，不直接使用货币。核心常量：

- **QuotaPerUnit = 500,000** — 1 个显示单位 = 500,000 quota 积分
- **PreConsumedQuota = 500** — 默认预扣额度
- **TrustQuota = 5,000,000** — 高余额用户可跳过预扣的信任阈值

用户模型中的关键字段：
- `quota` — 剩余配额
- `used_quota` — 累计消耗
- `request_count` — API 调用次数

## 二、两种定价模式

### A. 比率定价 (Ratio-based) — 按 Token 计费

用于 Chat、Embedding、Rerank 等按 Token 计量的请求：

```
actualQuota = (promptQuota + completionQuota) × modelRatio × groupRatio
```

**核心比率参数：**

| 参数 | 说明 | 示例 |
|------|------|------|
| `ModelRatio` | 模型基础倍率 | gpt-4o: 1.25, claude-3-opus: 7.5 |
| `CompletionRatio` | 输出 Token 倍率 | 通常 1.0-5.0 |
| `CacheRatio` | 缓存命中 Token 倍率 | 0.1-0.5（即打 1-5 折）|
| `CacheCreationRatio` | 缓存创建 Token 倍率 | 1.25（Claude）|
| `ImageRatio` | 图片 Token 倍率 | 按模型不同 |
| `AudioRatio` | 音频 Token 倍率 | 按模型不同 |

### B. 固定定价 (Price-based) — 按次计费

用于图片生成、音乐生成等按次计费的请求：

```
actualQuota = modelPrice × QuotaPerUnit × groupRatio
```

示例：`dall-e-3: 0.04`, `mj_imagine: 0.1`, `suno_music: 0.1`

## 三、计费生命周期

```
┌─────────────────────────────────────────────────┐
│ 1. 定价计算 (ModelPriceHelper)                    │
│    确定 modelRatio/modelPrice、groupRatio 等参数    │
└───────────────────┬─────────────────────────────┘
                    ↓
┌─────────────────────────────────────────────────┐
│ 2. 预扣费 (PreConsumeBilling)                     │
│    估算 Token → 预扣 quota → 创建 BillingSession  │
└───────────────────┬─────────────────────────────┘
                    ↓
              [执行上游请求]
                    ↓
┌─────────────────────────────────────────────────┐
│ 3. 实际结算 (SettleBilling)                       │
│    根据实际 usage 计算 → 多退少补                   │
└───────────────────┬─────────────────────────────┘
                    ↓
┌─────────────────────────────────────────────────┐
│ 4. 日志记录 (RecordConsumeLog)                    │
│    记录模型、Token 数、quota 消耗、渠道等           │
└─────────────────────────────────────────────────┘
```

### 3.1 预扣费

```go
// Ratio 模式
preConsumedTokens = max(promptTokens, 500) + maxTokens
preConsumedQuota  = preConsumedTokens × modelRatio × groupRatio

// Price 模式
preConsumedQuota  = modelPrice × QuotaPerUnit × groupRatio
```

**信任机制 (Trust Quota)**：余额 > 5M 的钱包用户可跳过预扣，减少锁竞争。订阅用户和异步任务不适用。

### 3.2 实际结算

```go
delta = actualQuota - preConsumedQuota

if delta > 0 → 追扣（预扣不足）
if delta < 0 → 退还（预扣过多）
if delta == 0 → 无需调整
```

### 3.3 退款

- 请求失败未到达结算阶段 → 全额退还预扣
- 上游返回 0 Token（超时/错误）→ `actualQuota = 0`，全额退还
- 订阅模式下通过 `requestId` 保证幂等性，防止重复扣费

## 四、Token 费用明细计算

### 文本请求 (Chat/Completion)

```
baseTokens    = promptTokens - cachedTokens
cachedQuota   = cachedTokens × cacheRatio
promptQuota   = (baseTokens + cachedQuota) × modelRatio
completionQ   = completionTokens × completionRatio × modelRatio

总 quota = (promptQuota + completionQ) × groupRatio
         + webSearchQuota + fileSearchQuota + audioInputQuota（附加费用）
```

### Token 详情结构

```go
Usage {
  PromptTokens      int   // 输入 Token 总数
  CompletionTokens  int   // 输出 Token 总数
  PromptTokensDetails {
    TextTokens           int
    ImageTokens          int   // 图片 Token（按 ImageRatio）
    AudioTokens          int   // 音频输入 Token（按 AudioRatio）
    CachedTokens         int   // 缓存命中（按 CacheRatio，打折）
    CachedCreationTokens int   // 缓存创建（按 CacheCreationRatio，加价）
  }
  CompletionTokenDetails {
    TextTokens  int
    AudioTokens int   // 音频输出 Token
  }
}
```

### 音频/实时请求

```
quota = (inputTextTokens + inputAudioTokens × audioRatio
       + outputTextTokens × completionRatio
       + outputAudioTokens × audioRatio × audioCompletionRatio)
      × modelRatio × groupRatio
```

## 五、不同请求类型的计费差异

| 请求类型 | 计费方式 | 特殊处理 |
|---------|---------|---------|
| Chat/Completion | Ratio × Token | 支持缓存折扣、输出倍率 |
| Embedding | Ratio × Token | 通常只有输入 Token |
| Rerank | Ratio × Token | Query + Document Token |
| Image 生成 | 固定 Price 或 ImageRatio | `dall-e-3: 0.04/次` |
| Audio TTS/STT | AudioRatio + AudioCompletionRatio | 输入输出音频分别计价 |
| Realtime (WS) | Audio + Text 混合 | 混合音频/文本 Token |
| MJ/Suno 任务 | 固定 Price/次 | `mj_imagine: 0.1/次` |
| Web Search 附加 | 额外按次加价 | `webSearchPrice / 1000 × 调用次数` |

## 六、分组定价 (Group Ratio)

支持两级分组倍率：

```go
// 一级：使用组倍率
groupRatio = GetGroupRatio(usingGroup)    // 默认 1.0

// 二级：用户组 × 使用组 特殊倍率（优先级更高）
specialRatio = GetGroupGroupRatio(userGroup, usingGroup)  // 如 VIP 享 0.9 折
```

实际应用：`finalQuota = tokenQuota × actualGroupRatio`

## 七、钱包 vs 订阅

### 钱包 (Wallet) — 按量付费

- 用户有单一 `quota` 余额
- 每次请求扣减/退还 quota
- 高余额用户可享受 Trust Quota 优化

### 订阅 (Subscription) — 周期配额

- `amount_total` — 周期内最大配额
- `amount_used` — 当前已用
- `quota_reset_period` — 重置周期（日/周/月/永不）
- `next_reset_time` — 下次重置时间
- 通过 `requestId` + 预扣记录表保证幂等

### 计费优先级

| 配置 | 行为 |
|------|------|
| `subscription_first`（默认）| 先扣订阅，无可用订阅则扣钱包 |
| `wallet_first` | 先扣钱包，余额不足则扣订阅 |
| `subscription_only` | 仅扣订阅 |
| `wallet_only` | 仅扣钱包 |

## 八、免费模型

当以下任一条件成立时，模型视为免费：
- `ModelRatio == 0` 或 `ModelPrice == 0`
- `GroupRatio == 0`（分组全免）

免费模型：`preConsumedQuota = 0`，不创建 BillingSession，不扣费。

## 九、关键文件索引

| 文件 | 职责 |
|------|------|
| `setting/ratio_setting/model_ratio.go` | 模型比率/价格定义 |
| `setting/ratio_setting/cache_ratio.go` | 缓存倍率 |
| `setting/ratio_setting/group_ratio.go` | 分组倍率 |
| `relay/helper/price.go` | 预扣费计算 (ModelPriceHelper) |
| `service/billing.go` | 计费入口 |
| `service/billing_session.go` | BillingSession 生命周期 |
| `service/funding_source.go` | 钱包/订阅资金源实现 |
| `service/text_quota.go` | 文本请求 Token 费用计算 |
| `service/quota.go` | 音频请求 Token 费用计算 |
| `types/price_data.go` | PriceData 数据结构 |
| `model/user.go` | 用户 Quota 操作 |
| `model/subscription.go` | 订阅生命周期 |
