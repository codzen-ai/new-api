# 日志 CSV 导出功能：与系统真实计费的差异与已知问题

> 状态：自维护功能（非上游）。本文档记录该导出功能在「金额/账单」语义上与系统真实计费之间的已知差异，供使用与后续改进参考。
>
> 涉及代码：
> - 导出实现：[`controller/log.go`](../../controller/log.go)（`logCSVHeader`、`logToCSVRow`、`ExportAllLogs`/`StreamAllLogsForExport`）
> - 查询构造：[`model/log.go`](../../model/log.go)（`buildLogExportQuery`、`CountAllLogsForExport`、`StreamAllLogsForExport`）
> - 真实计费：[`service/text_quota.go`](../../service/text_quota.go)（`calculateTextQuotaSummary`）、[`service/quota.go`](../../service/quota.go)、[`relay/helper/price.go`](../../relay/helper/price.go)
> - 金额展示口径：[`logger/logger.go`](../../logger/logger.go)（`LogQuota`、`FormatQuota`）

## 1. 背景：系统以 `quota` 为唯一记账单位

系统对用户的真实扣费始终以整数 `quota` 进行，记录在 `logs.quota` 列。货币只是**展示层换算**：

```
USD = quota / QuotaPerUnit          // QuotaPerUnit = 500000，见 common/constants.go
CNY = USD × USDExchangeRate         // USDExchangeRate 为后台可配置全局值
```

`quota` 本身与汇率无关、与时间无关，是当时实际扣费的权威值。**因此导出中的 `quota` 列和 `usd` 列（仅由 quota 线性换算）是可信的；问题主要集中在 `cny` 列、token 明细列、以及「逐笔对账」的可重算性上。**

## 2. 导出功能现状

`logToCSVRow` 当前导出列：

```
id, created_at, type, username, token_name, model_name, channel, channel_name,
prompt_tokens, completion_tokens, quota, usd, cny, use_time, is_stream,
token_id, group, ip, request_id, content
```

关键实现点：

- `usd = quota / QuotaPerUnit`，`cny = usd × operation_setting.USDExchangeRate`，均保留 6 位小数（与后端 `LogQuota`/`FormatQuota` 的 `%.6f` 口径一致）。
- `created_at` 与导出文件名均渲染为 **UTC+8**（固定偏移 `time.FixedZone("UTC+8", 8*3600)`）。数据库存储的是 Unix 时间戳（与时区无关）。
- **不导出 `Other` 字段**——而 `Other` 里恰恰存着复算费用所需的全部明细（见下）。

## 3. 系统真实计费比导出展示的复杂得多

`calculateTextQuotaSummary` 显示，单笔文本请求的 `quota` 由多类 token × 各自倍率组成，并整体乘以 `模型倍率 × 分组倍率`：

```
quota ≈ ( baseTokens
        + cacheTokens          × cacheRatio
        + cacheCreationTokens  × cacheCreationRatio (含 5m / 1h 拆分)
        + imageTokens          × imageRatio
        + completionTokens     × completionRatio )
      × (modelRatio × groupRatio)
      + toolCallSurchargeQuota
      + audioInputQuota          // 音频输入按「每百万 token 单价」单独计价
```

其中 `baseTokens = promptTokens − cacheTokens − cacheCreationTokens − imageTokens − audioTokens`（不同 usage 语义/渠道下扣减规则不同）。

这些计费要素——`model_ratio`、`group_ratio`、`completion_ratio`、`cache_tokens`、`cache_ratio`、`model_price`、`user_group_ratio` 等——全部写在日志的 **`Other` JSON 字段**里（见 `GenerateTextOtherInfo`），而导出并未包含该字段。

---

## 4. 已知问题清单

### 问题 1：汇率在「导出时」换算，且未随日志固化 → CNY 账单会随汇率变动而漂移 ⚠️ 高

- **现象**：`cny` 列用的是**导出那一刻**的全局 `USDExchangeRate`。该值是一个可在后台随时修改的全局变量（[`setting/operation_setting/payment_setting_old.go`](../../setting/operation_setting/payment_setting_old.go)，由 [`model/option.go`](../../model/option.go) 的 `USDExchangeRate` 选项写入），**不会随每条日志保存历史快照**。
- **后果**：
  - 若管理员在某次消费之后调整了汇率，再导出历史日志，这些历史行的 `cny` 会按**新汇率**重算，与用户消费当时「应付人民币」不一致。
  - 同一批日志，在不同时间导出，`cny` 合计可能不同。
  - 自定义币种（`QuotaDisplayTypeCustom`）同理。
- **不受影响**：`usd` 列（仅 `quota/QuotaPerUnit`，与汇率无关）始终稳定。
- **说明**：系统自身的 `FormatQuota`/`LogQuota` 也是「展示时换算」，本身并不承诺 CNY 是历史值；但导出物以「账单」形态呈现，容易让人误以为 `cny` 是消费当时的固定金额。
- **可选缓解**：① 账单类用途以 `usd`（或 `quota`）为准；② 若必须出 CNY 账单，应在消费时把当时汇率写入 `Other` 并在导出时优先使用该历史汇率；③ 在表头/文档注明「CNY 按导出时汇率换算，仅供参考」。

### 问题 2：缓存 / 图像 / 音频 token 未导出 → 仅凭可见列无法解释费用 ✅ 已解决（部分）

> **已解决**：导出现已解析 `Log.Other`，新增 `cache_tokens`、`cache_creation_tokens`、`model_ratio`、`group_ratio`、`completion_ratio`、`cache_ratio`、`model_price`、`billing_mode` 列（见 `logToCSVRow`）。大多数模型的单行 `quota` 现可由 token × 倍率复算核对；分层/固定价模型通过 `billing_mode`/`model_price` 列显式标记为「不可用 token 复算」。无计费信息的行（系统/充值/报错日志、非法 JSON）这些列留空。
>
> 仍未拆列的明细：图像 token、音频 token 及其单独定价、缓存创建的 5m/1h 拆分。如需完全复算这些场景，可进一步增列或附带原始 `other` JSON。

- **现象（历史）**：导出只有 `prompt_tokens` 与 `completion_tokens` 两个 token 列。但 `prompt_tokens` 是**包含缓存命中 token 在内的总和**，而计费时缓存命中部分按 `cacheRatio`（通常更便宜）折算、缓存创建部分按 `cacheCreationRatio` 计价、图像/音频 token 又另有倍率。
- **后果**：
  - 两行 `prompt_tokens`/`completion_tokens` 完全相同的记录，`quota` 可能差异很大（一行命中了缓存、另一行没有）。
  - 读者**无法**用「prompt × 单价 + completion × 单价」从可见列推回 `quota`，token 列对费用是「欠定」的。
- **重要边界**：这并不意味着 `quota` 不准——`quota` 是实际扣费的权威值，`usd = quota/500000` 也正确。问题是**展示出来的 token 列与 quota 之间无法自洽**，不利于人工核对。
- **可选缓解**：增加 `cache_tokens`、`cache_ratio`、`model_ratio`、`group_ratio`、`completion_ratio` 等列（来源：日志的 `Other` 字段），或直接附带原始 `other` JSON。

### 问题 3：按「固定价格」计费的模型，token 列与费用完全脱钩 🟡 中

- **现象**：对走 `UsePrice` 的模型，`quota = modelPrice × QuotaPerUnit × groupRatio`（[`relay/helper/price.go`](../../relay/helper/price.go)），**与 token 数无关**。
- **后果**：这类记录的 `prompt_tokens`/`completion_tokens` 与 `usd`/`cny` 之间没有任何换算关系，按 token 推算费用会得到错误结论。
- **可选缓解**：导出 `model_price`/`use_price` 标记（来自 `Other`），或在文档中说明此类模型的费用以 `quota` 为准。

### 问题 4：每笔 quota 存在取整、最低 1、分层计费、退款等修正 → 逐笔重算无法对账 🟡 中

真实扣费链路中存在多处使其**无法用单一公式复现**的修正：

- **整数化**：每笔 `quota = int(decimal.Round(0))`（[`service/quota.go`](../../service/quota.go)），逐笔四舍五入到整数。
- **最低 1 quota**：`ratio != 0 且 quota <= 0` 时强制置为 1。
- **分层/表达式计费**：`TryTieredSettle` 命中时直接覆盖上述公式（见 `pkg/billingexpr/`）。
- **工具调用附加费** `toolCallSurchargeQuota`、**音频输入单独计价** `audioInputQuota`。
- **预扣费 / 实际扣费 / 退款**：视频等异步任务存在补扣与退还（`controller/task_video.go`）。

- **后果**：`quota` 对「实际扣了多少」是准确的，但若试图用 token × 倍率自行复算来校验 `usd`/`cny`，会因上述修正而对不上。
- **可选缓解**：明确「以 `quota` 列为对账依据，不要用 token 重算」。

### 问题 5：逐行 6 位四舍五入后求和，存在累积误差 🟢 低

- **现象**：每行 `usd`/`cny` 先被四舍五入到 6 位再写入。对大量行求和时，逐行舍入的累积误差会与「先汇总 `quota` 再换算」的结果产生微小偏差。
- **后果**：导出表自带的金额合计（若按行相加）可能与「`Σquota / 500000`」存在末位级差异。
- **可选缓解**：账单合计以 `Σquota` 为基准换算，而非对 `usd`/`cny` 列逐行相加。

### 问题 6：`prompt_tokens` 的语义随渠道/usage 口径变化 🟢 低

- **现象**：如 OpenRouter 的 Claude 计费路径会从 `prompt_tokens` 中扣除缓存与缓存创建 token（`calculateTextQuotaSummary` 中 `summary.PromptTokens -= ...`），不同 usage 语义（`anthropic` 等）下扣减规则不同。
- **后果**：跨渠道横向比较 `prompt_tokens` 时口径未必一致。
- **可选缓解**：在文档中说明 token 口径依赖上游 usage 语义。

### 问题 7：时区口径需对齐 🟢 低（说明项）

- 导出的 `created_at` 与文件名为 **UTC+8**；数据库存储为 Unix 时间戳。与其他以 UTC 呈现的系统数据交叉核对时需注意换算。

---

## 5. 结论与建议

| 列 / 用途 | 可信度 | 说明 |
|---|---|---|
| `quota` | ✅ 权威 | 实际扣费值，对账以此为准 |
| `usd` | ✅ 稳定 | `quota/500000`，与汇率/时间无关 |
| `cny` | ⚠️ 参考 | 按**导出时**全局汇率换算，会随汇率变动漂移 |
| `prompt_tokens` / `completion_tokens` | ⚠️ 不可独立解释费用 | 不含缓存/图像/音频拆分，无法推回 quota |

**改进优先级建议**：

1. （高）账单/对账场景统一以 `quota`/`usd` 为准；CNY 仅作参考，并在表头或随附说明中标注「按导出时汇率换算」。
2. （高）如需可解释、可复核的导出，增列 `Other` 中的计费要素（`cache_tokens`、各类 `ratio`、`model_price` 等）或直接附带原始 `other` JSON。
3. （中）如确需历史 CNY 账单，须在消费时将当时汇率写入 `Other`，导出时优先用历史汇率而非全局当前值。
4. （低）金额合计以 `Σquota` 换算，避免逐行舍入累积误差。
