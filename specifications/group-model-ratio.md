# 分组模型倍率（GroupModelRatio）设计文档

> 目标：让管理员按「分组 × 模型」设定模型倍率，实现分组级差异化定价，
> 而不必为同一个上游模型克隆出多个改名模型。
> 语义：**覆盖即最终价** —— 命中覆盖时该值就是最终模型倍率，分组倍率不再叠乘。
> 本期范围：仅按量倍率（token ratio）计费的模型。固定价、表达式（`tiered_expr`）计费不在范围内。
> 状态：已实现（分支 `feat/group-model-ratio`）。
> 基线：`staging`（2026-08-20）。

面向管理员的操作说明见 [guides/admin/group-model-ratio.md](../guides/admin/group-model-ratio.md)，
面向终端用户的影响说明见 [guides/user/group-pricing.md](../guides/user/group-pricing.md)。

## 1. 结论先行

系统原有的分组定价能力只有一个维度：**分组倍率**（`GroupRatio`），它对该分组下的**所有模型**统一打折。
想给「vip 分组的 gpt-4o 单独定价、其他模型不变」，原本只能靠改名模型 + 渠道重定向绕过去，
代价是模型列表被污染、用户看到一堆语义不明的名字、后续维护成本随模型数线性增长。

本方案增加第二个维度 `GroupModelRatio`，形状 `{分组: {模型: 倍率}}`：

| 配置 | 维度 | 语义 |
|---|---|---|
| `ModelRatio` | 模型 | 全局模型倍率 |
| `GroupRatio` | 分组 | 分组统一折扣，乘在模型倍率上 |
| `GroupGroupRatio` | 分组 × 分组 | 用户属于 A 组、按 B 组计费时的折扣 |
| **`GroupModelRatio`** | **分组 × 模型** | **该分组下该模型的最终倍率** |

关键的语义选择是**覆盖即最终价**，而不是「再乘一个系数」。理由见 §2.2。

## 2. 语义

### 2.1 计价公式

未命中覆盖（原有行为）：

```
最终倍率 = ModelRatio[模型] × 分组倍率
```

其中「分组倍率」优先取 `GroupGroupRatio[用户组][计费组]`，没有则取 `GroupRatio[计费组]`。

命中覆盖：

```
最终倍率 = GroupModelRatio[计费组][模型]
```

分组倍率被归一为 `1.0`，特殊分组倍率（`GroupGroupRatio`）同时失效。
这一步由 `ratio_setting.ResolveGroupModelPrice` 统一完成，禁止在调用点自行拼装，
否则极易漏掉归一而变成叠乘。

```go
// 命中 → (override, 1.0, true)；未命中 → (base, base, false)
func ResolveGroupModelPrice(usingGroup, model string, baseModelRatio, baseGroupRatio float64) (
    modelRatio float64, groupRatio float64, overridden bool)
```

### 2.2 为什么是「覆盖即最终价」而不是「叠乘一个系数」

叠乘方案（`最终 = 模型倍率 × 分组倍率 × 覆盖系数`）看起来更"正交"，但对管理员是错误的抽象：

- 管理员真正想表达的是「这个分组的这个模型卖多少钱」，是一个**绝对价**，不是相对折扣。
  叠乘要求他先心算全局倍率和分组倍率的乘积，再反推系数，改任何一个上游值都要重算。
- 分组倍率通常是「vip 打八折」这种粗粒度策略。一旦对某个模型做了特殊定价，
  再让粗粒度折扣继续生效，几乎总是意料之外的结果（特价商品又打八折）。
- 绝对价可以直接和成本对账，叠乘不行。

代价是必须清晰地告知管理员「配了覆盖，分组倍率对该模型就不生效了」。
这条在后台字段说明、管理员文档、本文档中重复声明。

### 2.3 派生倍率

覆盖替换的是 **base**，其余倍率照常在 base 上相乘：

```
输入价     = base
输出价     = base × CompletionRatio
缓存读取价 = base × CacheRatio
音频、图片等同理
```

也就是说覆盖只调整该模型在该分组的整体价位，输入输出比价关系不变。
若需要输入输出解耦，属于 Phase 2 的 `GroupModelCompletionRatio`，本期不做。

### 2.4 边界取值

| 情况 | 行为 |
|---|---|
| 覆盖 = 0 | 命中，最终倍率 0，走既有「倍率为 0 即免费模型」逻辑 |
| 覆盖 < 0 | 保存时被 `CheckGroupModelRatio` 拒绝 |
| 分组倍率 = 0 且无覆盖 | 保留 0（免费），不得被 `|| 1` 之类的写法吃掉 |
| 模型无全局倍率、只有覆盖 | 视为按量计费，覆盖即其倍率（见 §3.4） |
| 模型是固定价或 `tiered_expr` | 覆盖不生效，后台给出提示（见 §5） |

## 3. 计费路径注入点

覆盖必须在**每一条**会重新推导倍率的路径上应用。任何一条漏掉，都会因为预扣与结算不一致
而把覆盖抹掉——结算普遍是全量替换语义（`task.Quota = actualQuota`，差额补扣或退还），
漏掉的一侧会直接决定最终收费。

| # | 路径 | 位置 | 说明 |
|---|---|---|---|
| 1 | 同步请求预扣 | `relay/helper/price.go` `ModelPriceHelper` | 命中时置 `success = true`，见 §3.4 |
| 2 | 按次 / 任务预扣 | `relay/helper/price.go` `ModelPriceHelperPerCall` | MJ、异步任务走这里 |
| 3 | 实时（WSS）计费 | `service/quota.go` `PreWssConsumeQuota` | 该路径独立重取倍率，必须单独注入 |
| 4 | 异步任务差额结算 | `service/task_billing.go` `RecalculateTaskQuotaByTokens` | 见 §3.3 |

同步文本请求的结算不重新取倍率，直接复用 `ModelPriceHelper` 产出的 `PriceData`，
因此天然覆盖，无需第五处注入。

### 3.3 异步任务结算的判定顺序

`RecalculateTaskQuotaByTokens` 自行读取倍率重算，其判定顺序必须与 `ModelPriceHelperPerCall` 严格一致：

```
固定价（GetModelPrice / DefaultModelPriceMap）
  → 命中则不适用覆盖，也不按 token 重算
分组模型倍率覆盖
  → 命中则为最终倍率，且该模型即按量计费
全局模型倍率
```

顺序错了会产生新的不一致：例如把覆盖判定放在固定价之前，
「固定价 + 覆盖」的模型会变成预扣按固定价、结算按 token 重算。

### 3.4 只有覆盖、没有全局倍率的模型

设计上允许管理员只配覆盖而不配全局倍率——这类模型对配了覆盖的分组是可计费的。
实现上表现为覆盖命中时把「是否已配置倍率」置真。

受此影响的还有 `HasModelBillingConfig`：该函数判断模型是否已配置计费方式，
用于 `ListModels` 过滤和 Gemini 非思考变体探测。它必须同样认覆盖，否则会出现
**能按覆盖价计费、却被排除在 `/v1/models` 之外**。由于覆盖按分组存储，
该函数需要分组视角，签名为：

```go
func HasModelBillingConfig(modelName string, groups []string) bool
```

调用点分别传入用户可用分组（`ListModels` 的 `ownerGroups`，auto 分组会展开为多个）
与当前请求分组。**不得**实现成「任意分组存在覆盖即为真」——那会让没有配置覆盖的分组
也看见该模型，属于越权暴露。

## 4. 存储、配置与接口

### 4.1 存储

与 `GroupGroupRatio` 完全同型，复用既有配置基础设施：

| 环节 | 实现 |
|---|---|
| 内存 | `groupModelRatioMap *types.RWMap[string, map[string]float64]` |
| 持久化 | 选项 `GroupModelRatio`，JSON 字符串 |
| 热更新 | `config.GlobalConfig` 既有通道，与 `GroupGroupRatio` 同路径 |
| 校验 | `CheckGroupModelRatio`：JSON 合法 + 所有倍率 ≥ 0 |

### 4.2 定价接口

`GET /api/pricing` 增加 `group_model_ratio` 字段，形状与配置一致。

**只返回用户可用分组的覆盖**（`GetGroupModelRatioForUsableGroups`）。
这一点是必需的：若原样返回全表，专属分组、私有分组的价格会泄露给所有能打开定价页的人。

### 4.3 前端定价计算

后端只下发原始数据，最终价由前端计算（与既有分组倍率的处理方式一致），
逻辑集中在 `web/src/features/pricing/lib/price.ts`：

- `effectiveRatioProduct(model, group, groupRatio)` —— 单个分组的最终倍率乘积，命中覆盖则直接返回覆盖值。
- `getDisplayRatioProduct(model, selectedGroup)` —— 模型广场汇总价。
  指定分组时取该分组，未指定时取跨可用分组的**最低**乘积；覆盖参与这个最低价比较。

注意 `getDisplayRatioProduct` 必须与 `model-helpers.ts` 的 `getDisplayGroupRatio` 行为对齐：
响应分组筛选、跳过既无覆盖又无分组倍率的分组。读取分组倍率一律走 `getConfiguredGroupRatio`，
它保留合法的 0 值；写成 `groupRatio[group] || 1` 会把免费分组显示成原价。

## 5. 不支持的计费方式

| 计费方式 | 是否生效 | 原因 |
|---|---|---|
| 按量倍率 | ✅ | 本方案目标 |
| 固定价（`ModelPrice`） | ❌ | 走 `usePrice` 分支，不经过 `modelRatio` |
| 表达式（`tiered_expr`） | ❌ | 表达式直接产出 `rawCost`，只乘分组倍率，没有 `modelRatio` 可覆盖 |

这两类是**静默失效**：保存成功、界面无异常、计费不变。这是配置类功能最伤人的失败模式，
因此后台在 `GroupModelRatio` 字段下方按已保存的 `ModelPrice` 与 `billing_setting.billing_mode`
比对覆盖表中的模型名，列出不会生效的模型。仅提示、不阻断保存——管理员可能先配倍率、后改计费方式。

表达式模型若需要分组差异化，应在表达式内部处理，或使用分组倍率，而不是外挂一层覆盖：
在表达式路径上「分组模型倍率」只可能解释为「覆盖分组倍率」，与本功能的「覆盖模型倍率」
是两套语义，混在同一张配置表里会让同一个数字在不同模型上差出数量级。

## 6. 后台配置界面

分组倍率设置页有可视化与 JSON 两种模式。`GroupModelRatio` **只在 JSON 模式**下可编辑。

可视化编辑器整体以分组为单位组织（一行一个分组），而 `GroupModelRatio` 的内层键是模型——
基数差两个量级。直接套用 `GroupGroupRatio` 的「分组折叠 + 内嵌表格」模式，
会退化成「展开一个分组后是几百行表格、加一条要手打模型名」，比 JSON 更难用。

若后续要做，建议按模型维度组织（外层模型、内层分组），并在每行展示
`全局倍率 × 分组倍率` 的对照值——「覆盖即最终价」最容易被误解的就是这一点。
该项未纳入本期。

数据是安全的：`GroupModelRatio` 是独立注册的表单字段，可视化编辑器只回写
`GroupRatio` / `UserUsableGroups` / `TopupGroupRatio`，不会清空它。

## 7. 落地文件

| 文件 | 改动 |
|---|---|
| `setting/ratio_setting/group_ratio.go` | 配置存储、读取、校验、`ResolveGroupModelPrice` |
| `relay/helper/price.go` | 注入点 1、2；`HasModelBillingConfig` 加分组视角 |
| `service/quota.go` | 注入点 3 |
| `service/task_billing.go` | 注入点 4 |
| `controller/option.go`、`model/option.go` | 选项校验与持久化 |
| `controller/pricing.go` | 定价接口按可用分组返回覆盖 |
| `controller/model.go`、`relay/gemini_handler.go` | `HasModelBillingConfig` 调用点 |
| `types/price_data.go` | `GroupRatioInfo.ModelRatioOverridden` 标记 |
| `web/src/features/pricing/lib/price.ts` | 前端定价计算 |
| `web/src/features/system-settings/models/group-ratio-form.tsx` | JSON 编辑入口 + 静默失效提示 |

## 8. 测试

| 层次 | 位置 | 覆盖 |
|---|---|---|
| 解析器 | `setting/ratio_setting/group_model_ratio_test.go` | 命中 / 未命中 / 覆盖为 0 / 分组隔离 |
| 计价端到端 | `relay/helper/price_test.go` | 覆盖注入、`HasModelBillingConfig` 的分组视角 |
| 任务结算 | `service/task_billing_test.go` | 覆盖生效、只有覆盖时按 token 重算、固定价不重算 |
| 前端定价 | `web/src/features/pricing/lib/__tests__/group-model-ratio-price.test.ts` | 覆盖即最终价、最低价比较、0 倍率保留 |
| 前端提示 | `web/src/features/system-settings/models/__tests__/group-model-ratio-warning.test.tsx` | 固定价 / 表达式识别、去重排序、非法 JSON |

前端断言一律用「等价配置产出同一价格」的形式，不断言货币字符串字面量——
`formatCurrencyFromUSD` 依赖全局货币显示配置，硬编码会在管理员改显示币种后失败。

## 9. 已知限制

- **可视化编辑器无入口**（§6）。可视化模式下既不能编辑，也看不到静默失效提示。
- **`ModelRatioOverridden` 是死字段**。`types/price_data.go` 定义、`price.go` 赋值，
  但日志与前端都没有消费方。命中覆盖的账单在日志里表现为 `model_ratio` = 覆盖值、
  `group_ratio` = 1.0，可以推断但没有显式标记。
- **固定价与表达式不支持**（§5）。

## 10. 后续（Phase 2，未排期）

- `GroupModelPrice`：分组 × 模型的固定价覆盖。结构与本方案同型，
  注入点是 `price.go` 的两处 `modelPrice × QuotaPerUnit × groupRatio`。
  需先明确覆盖与 `ImagePriceRatio`、`OtherRatios`（时长、分辨率）的关系——
  建议覆盖只替换基础单价，这些倍率继续相乘。
- `GroupModelCompletionRatio`：输入输出解耦。
- 缓存 / 音频 / 图片倍率的分组覆盖。
