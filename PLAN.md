# 日志导出：缓存用量与缓存花费

## 背景

CSV 日志导出（`controller/log.go` 的 `logCSVHeader` / `logToCSVRow`）此前只导出 `prompt_tokens`、`completion_tokens`、`quota`、`usd`、`cny`，无法体现缓存的用量和花费。对使用 prompt caching 的模型（Claude、OpenAI 等），缓存读取与缓存写入的单价与普通输入不同，缺少这些列就无法做成本分析，也看不出缓存到底省了多少钱。

本文档描述导出缓存用量与缓存花费的实现方案，并明确其准确性边界。

## `Log.Other` 中的实际字段

缓存相关数据不在 `logs` 表的独立列上，而是序列化在 `Log.Other`（`model/log.go:80`，`string` 类型的 JSON）里，由 `service/log_info_generate.go` 在计费完成时写入。

**用量字段**（`GenerateTextOtherInfo` / `GenerateClaudeOtherInfo`）：

| 字段 | 含义 | 写入条件 |
| --- | --- | --- |
| `cache_tokens` | 缓存读取 tokens | 文本请求恒有 |
| `cache_creation_tokens` | 缓存写入 tokens | Claude 语义 |
| `cache_creation_tokens_5m` | 5 分钟缓存写入 tokens | 仅当非 0 |
| `cache_creation_tokens_1h` | 1 小时缓存写入 tokens | 仅当非 0 |

**倍率字段**：

| 字段 | 含义 | 写入条件 |
| --- | --- | --- |
| `cache_ratio` | 缓存读取倍率 | 文本请求恒有 |
| `cache_creation_ratio` | 缓存写入倍率 | Claude 语义 |
| `cache_creation_ratio_5m` | 5 分钟缓存写入倍率 | 仅当对应 tokens 非 0 |
| `cache_creation_ratio_1h` | 1 小时缓存写入倍率 | 仅当对应 tokens 非 0 |

**计费基准字段**：`model_ratio`、`group_ratio`、`completion_ratio`、`model_price`、`user_group_ratio`，以及 Claude 语义标记 `claude`。

关键性质：这些倍率是**计费时刻的历史快照**。后台修改模型倍率或汇率不会影响已落库的日志，因此用它们重算缓存花费是准确的，不会被后来的调价污染。

## 缓存花费的推导

计费公式见 `service/text_quota.go:285-357`。按倍率计费（非 `UsePrice`）时：

```
ratio = model_ratio × group_ratio

cache_read_quota  = cache_tokens × cache_ratio × ratio

cache_write_quota = cache_creation_tokens × cache_creation_ratio × ratio          （OpenAI 语义）
                  = [(cache_creation_tokens − 5m − 1h) × cache_creation_ratio
                     + 5m × cache_creation_ratio_5m
                     + 1h × cache_creation_ratio_1h] × ratio                      （Claude 语义）
```

`group_ratio` 就是计费实际使用的倍率：`log_info_generate.go:477` 写入的是 `summary.GroupRatio`，而 `user_group_ratio` 存的是 `GroupRatioInfo.GroupSpecialRatio`，仅用于界面展示，不参与计算。因此重算时用 `group_ratio`，不要用 `user_group_ratio`。

quota 转 USD 沿用既有换算：`usd = quota / common.QuotaPerUnit`（`QuotaPerUnit = 500000`，即 `model_ratio = 1` 对应 $2/M tokens）。

**缓存节省**表示这些缓存读取的 tokens 如果按普通输入价计费需要多花的钱：

```
cache_saving_quota = cache_tokens × (1 − cache_ratio) × ratio
```

`cache_ratio < 1` 时为正（省钱），等于 1 时为 0。

## 导出列设计

在 `content` 列之前插入 12 列，保持 `content`（可能很长）仍在最后：

**语义标记**（1 列）：`usage_semantic` —— 取值 `anthropic` / `openai`，由 `Other.claude` 推导。

**用量**（4 列）：`cache_tokens`、`cache_creation_tokens`、`cache_creation_tokens_5m`、`cache_creation_tokens_1h`

**倍率**（4 列）：`cache_ratio`、`cache_creation_ratio`、`cache_creation_ratio_5m`、`cache_creation_ratio_1h`

**花费**（3 列）：`cache_read_usd`、`cache_write_usd`、`cache_saving_usd`

花费只给 USD，不给 CNY：缓存单行金额比总额小一到两个数量级，CNY 需要 `6 + 汇率小数位` 位小数才能精确表示，6 位小数的相对舍入误差在小数值上更明显。需要人民币口径时，用 USD 列求和后再乘汇率，结果与总额换算一致。

字段缺失时输出空字符串而非 `0`，`0` 会被误读成“确实发生了但为零”。

## 准确性边界

以下三点必须在实现中体现，否则导出数据会被误用。

**1. 缓存花费各项之和 ≠ `quota` 列。** 文本日志的 `Other` 没有落库 `other_ratios`（只有任务日志有，见 `model/task.go:120`），此外音频单价项（`audioInputQuota`）、工具调用附加费（`ToolCallSurchargeQuota`）、以及最终的“最小值 1”兜底和 `QuotaFromDecimalChecked` 的饱和截断都不在拆分范围内。因此这些列可以回答“缓存花了多少钱”，但**不能用于反推或对账总额**。若将来需要严格对账，前置条件是把 `other_ratios` 也写入日志 `Other`。

**2. `prompt_tokens` 的含义随协议变化，直接与缓存列相加会重复计算。** OpenAI 语义下 `prompt_tokens` **包含** cached tokens，计费时要减去（`text_quota.go:308`）；Claude 语义下 `prompt_tokens` **不含**缓存部分。这是导出 `usage_semantic` 列的原因——让每一行自解释该不该相加。

**3. 按次计费的请求没有缓存计费。** `model_price > 0` 表示走固定价格（判据与前端 `isPerCallBilling` 一致），此时缓存倍率不参与计算，倍率列与花费列必须留空。用量列照实导出，因为缓存调用确实发生了。

## 实现要点

1. `controller/log.go`：扩展 `logCSVHeader`，在 `content` 前插入上述 12 列。
2. 新增缓存列的解析与重算逻辑，用 `common.StrToMap` 解析 `Other`（标准 `json.Unmarshal`，数字为 `float64`）。金额用 `shopspring/decimal` 计算避免浮点误差累积，输出格式与既有 `usd` 列一致（6 位小数）。
3. 不新增数据库列、不做迁移，全部数据已在 `Other` 中。

## 测试覆盖

`controller/log_export_test.go` 需覆盖：

- Claude 语义下 5m / 1h 拆分的缓存写入花费
- OpenAI 语义下缓存写入花费（不拆分）
- `cache_ratio < 1` 时的节省金额，以及 `= 1` 时节省为 0
- `model_price > 0`（按次计费）时倍率列与花费列留空
- `Other` 为空、非法 JSON、字段缺失时输出空字符串且行宽不变
