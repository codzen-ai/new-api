# 模型广场参数计价可见性方案（BillingDimensions）

> 状态：待执行（设计已定，未开始编码）
> 起因：模型广场对 seedance 2.0 这类「同一模型、不同参数不同价」的模型只显示基准价，用户在下单前无法知道实际会被扣多少。

## 1. 问题

以 `doubao-seedance-2-0-260128` 为例，实际单价由「输出分辨率档 × 输入是否含视频」两个维度决定（[relay/channel/task/doubao/constants.go:26-39](../relay/channel/task/doubao/constants.go#L26-L39)，单位：元/百万 token）：

| 输出档 | 无视频输入 | 含视频输入 |
|---|---|---|
| 480p / 720p（基准） | 46 | 28 |
| 1080p | 51 | 31 |
| 4K | 26 | 16 |

`doubao-seedance-2-0-fast-260128` 同理：37（无视频）/ 22（含视频），且未配置 1080p / 4K 档。

计费时 [adaptor.go:139-151](../relay/channel/task/doubao/adaptor.go#L139-L151) 取 `实际单价 / 基准价` 作为 `OtherRatio{"video_input": ratio}` 参与扣费。而广场展示的是管理员配置的基准档价格，也就是「480p/720p + 无视频输入」那一格。用户看不到 1080p 贵 11%、4K 便宜 43%、含视频输入便宜约 39% 这些差异。

三个环节都没有承载这个信息：

- **定价接口**：`Pricing` 结构体（[model/pricing.go:18-39](../model/pricing.go#L18-L39)）只有 `model_ratio` / `completion_ratio` / cache / image / audio 几个固定倍率字段，没有任何 OtherRatios 的位置。
- **前端广场**：详情页只在 `billing_mode === 'tiered_expr' && billing_expr` 时才渲染分级价格表（[model-details.tsx:1144-1183](../web/src/features/pricing/components/model-details.tsx#L1144-L1183)），seedance 走的是 task 按量计费 + 代码内价目表，这条路径不触发。全仓 `grep` 也没有任何 `video_input` / `other_ratios` 的前端处理。
- **表达式分级**：即使管理员用表达式配，tier 条件解析只认 `p` / `c` / `len` 三个变量（[billing-expr.ts:268-301](../web/src/features/pricing/lib/billing-expr.ts#L268-L301)），`param("resolution") == "1080p"` 这类条件会被丢弃，只剩下 tier 标签和价格、没有触发条件说明。

## 2. 这不是 seedance 一家的问题

5 个 task adaptor 都实现了 `EstimateBilling`，都有参数决定价格、广场看不到的情况：

| Adaptor | 返回的 OtherRatios | 价目表位置 |
|---|---|---|
| doubao | `video_input` | [doubao/constants.go:26](../relay/channel/task/doubao/constants.go#L26) `videoPriceTable` |
| gemini（Veo） | `seconds`、`resolution` | [gemini/billing.go:127-142](../relay/channel/task/gemini/billing.go#L127-L142) `VeoResolutionRatio`，4K 为 1.5 / 2.333 |
| vertex（Veo） | `seconds`、`resolution` | 复用 gemini 的 `VeoResolutionRatio`（[vertex/adaptor.go:126-141](../relay/channel/task/vertex/adaptor.go#L126-L141)） |
| sora | `seconds`、`size` | [sora/adaptor.go:98-128](../relay/channel/task/sora/adaptor.go#L98-L128)，1792x1024 / 1024x1792 为 1.666667 |
| ali（wan 系列） | `seconds`、`resolution-{480P\|720P\|1080P}` | [ali/adaptor.go:202-240](../relay/channel/task/ali/adaptor.go#L202-L240) `aliRatios`，8 个模型各一张表 |

同类问题还有图片生成的张数倍率 `n`（[openai/relay_image.go:29](../relay/channel/openai/relay_image.go#L29)、[ali/image.go:64](../relay/channel/ali/image.go#L64)、[ali/image_wan.go:37](../relay/channel/ali/image_wan.go#L37)）和阿里的 `prompt_extend`（[ali/image.go:53](../relay/channel/ali/image.go#L53)）。

**顺带发现的、比 seedance 更严重的一处**：sora / veo / ali 把 `seconds` 当作乘数直接进 quota。一条 10 秒的 sora 视频实际扣费是广场显示价格的 10 倍，若同时选 1792x1024 则约 16.7 倍。这个数量级的偏差比 seedance 的分辨率差价更容易让用户误判，建议同一批修掉。

## 3. 目标方案：声明式计费维度

**给 task adaptor 增加一个声明式的 `BillingDimensions()`，把维度和倍率从代码内的价目表提升为定价接口的一等数据，前端统一渲染成「参数计价」表。**

```
adaptor 价目表（唯一真源）
   ├─► EstimateBilling()      → 运行时按实际请求算 OtherRatios（现有行为，不改）
   └─► BillingDimensions()    → 静态声明全部维度与倍率
                                    │
                            billingdim 注册表（叶子包，按模型名索引）
                                    │
                            model.Pricing.billing_dimensions
                                    │
                            广场详情页「参数计价」表 + 卡片 badge
```

同一张表两个出口，是这个方案的关键：声明出来的倍率和实际计费用的倍率来自同一处，不会漂移。

### 3.1 为什么不选另外两条路

**不选「doubao 专属特例」**：5 个 adaptor 是同一个病，一次性做成通用机制的边际成本很低，而特例会让后面 4 个各写一遍前端。

**不选「先扩展 `param()` 的 tier 条件解析」**：task 提交路径走 `ModelPriceHelperPerCall`（[relay/relay_task.go:182](../relay/relay_task.go#L182)），而 `tiered_expr` 只在同步 relay 的 `ModelPriceHelper` 分支里判断（[relay/helper/price.go:79](../relay/helper/price.go#L79)）。**视频模型根本进不了表达式计费**，所以扩 `param()` 条件解析对本问题零收益。这件事只有在把 task 计费也接入 billingexpr 之后才值得做，属于独立议题。

**这个概念后端其实已经存在**：[service/task_billing.go:27-38](../service/task_billing.go#L27-L38) 已经把 OtherRatios 写进消费日志内容（「操作 generate, 计算参数：video_input: 0.61」）。现状是事后能看、事前看不到，本方案只是把同一组维度前移到售前页面，不需要新造数据模型。

## 4. 数据模型

新增叶子包 `pkg/billingdim`（不依赖 `model` / `relay`，避免循环）：

```go
type Kind string

const (
    KindOption Kind = "option"   // 离散取值 → 倍率，如 分辨率 / 是否含视频输入
    KindPerUnit Kind = "per_unit" // 线性乘数，如 seconds、n
)

type Option struct {
    Label string  `json:"label"`  // 展示用，如 "1080p"、"含视频输入"
    Ratio float64 `json:"ratio"`  // 相对基准价的倍率
}

type Dimension struct {
    Key     string   `json:"key"`               // 与 OtherRatios 的 key 一致，日志可对照
    Kind    Kind     `json:"kind"`
    Unit    string   `json:"unit,omitempty"`    // per_unit 用，如 "秒"、"张"
    Default float64  `json:"default,omitempty"` // per_unit 的默认值，用于估算示例
    Options []Option `json:"options,omitempty"` // option 用
}
```

注册与读取（按模型名索引，adaptor 在 `init()` 中注册自己 `GetModelList()` 覆盖的模型）：

```go
func Register(model string, dims []Dimension)
func Get(model string) []Dimension
```

**为什么用注册表而不是直接调 adaptor**：`GetTaskAdaptor` 按渠道类型分发且住在 `relay` 包（[relay/relay_adaptor.go:144](../relay/relay_adaptor.go#L144)），而 `relay` 已经 import `model`，`model.updatePricing` 反向 import `relay` 会形成循环。注册表放在叶子包，写入方是 adaptor、读取方是 `model`，两侧都不产生循环。

`channel.TaskAdaptor` 接口（[relay/channel/adapter.go:35](../relay/channel/adapter.go#L35)）增加：

```go
// BillingDimensions declares the parameter dimensions that affect this model's
// price, so the pricing page can show the full grid before a request is sent.
// Must stay consistent with EstimateBilling.
BillingDimensions(model string) []billingdim.Dimension
```

`taskcommon.BaseBilling` 给 nil 默认实现（[taskcommon/helpers.go:84](../relay/channel/task/taskcommon/helpers.go#L84) 旁边），与 `EstimateBilling` 同一套路，其余 adaptor 零改动即可编译。

对外 JSON：`model.Pricing` 增加 `billing_dimensions []billingdim.Dimension \`json:"billing_dimensions,omitempty"\``，在 `updatePricing` 里按模型名查注册表填充。

## 5. 实施步骤

### 阶段 1 · 打通链路（doubao + 前端）

1. ⬜ 新建 `pkg/billingdim`：类型定义 + 注册表 + 单元测试。
2. ⬜ `channel.TaskAdaptor` 加 `BillingDimensions`，`taskcommon.BaseBilling` 给 nil 默认实现。
3. ⬜ doubao 实现 `BillingDimensions`，**直接从现有 `videoPriceTable` 生成**，不要另抄一份常量；同时在 `init()` 里为 `ModelList` 中有价目表的模型注册。
4. ⬜ `model.Pricing` 加 `billing_dimensions`，`updatePricing` 填充；确认 [controller/pricing.go:36](../controller/pricing.go#L36) 原样透出。
5. ⬜ 前端：`PricingModel` 类型加字段；在详情页 `PriceSection`（[model-details.tsx:569](../web/src/features/pricing/components/model-details.tsx#L569)）之后新增「参数计价」小节，`option` 维度渲染成「取值 → 倍率 → 折算后单价」表，`per_unit` 渲染成「× N 秒」说明；卡片（[model-card.tsx:67](../web/src/features/pricing/components/model-card.tsx#L67)）加 badge。样式复用 [dynamic-pricing-breakdown.tsx](../web/src/features/pricing/components/dynamic-pricing-breakdown.tsx) 的表格与 Badge。
6. ⬜ i18n：新增 key 走 `web/src/i18n/locales/{lang}.json`，`bun run i18n:sync`。
7. ⬜ 在 staging 上人工确认 seedance 2.0 / 2.0-fast 的展示与 `videoPriceTable` 一致。

### 阶段 2 · 铺开其余 adaptor

8. ⬜ sora：`size`（1.666667）+ `seconds`（per_unit）。
9. ⬜ gemini / vertex：`resolution`（4K = 1.5 或 2.333，按模型）+ `seconds`（per_unit）。
10. ⬜ ali：`aliRatios` 8 个模型逐个生成 + `seconds`（per_unit，上限 `relaycommon.MaxTaskDurationSeconds`）。
11. ⬜ 图片侧 `n` / `prompt_extend`：这两个不在 task adaptor 接口上，需要另一条注册路径（可在 `relay/channel/<vendor>` 的 `init()` 直接调 `billingdim.Register`），确认后再做。

### 阶段 3 · 一致性护栏

12. ⬜ 每个 adaptor 一个表驱动测试：遍历 `BillingDimensions` 声明出的取值组合，构造对应请求，断言 `EstimateBilling` 返回同样的倍率。这是防止「价目表和实际计费漂移」的关键，也正好落在 `AGENTS.md` 允许的「保护计费不变量」类测试范围内。
13. ⬜ 一个跨 adaptor 测试：断言凡是实现了 `EstimateBilling`（非 `BaseBilling` 默认实现）的 adaptor 都声明了 `BillingDimensions`，防止以后新增 adaptor 时漏掉展示。

## 6. 约束与注意事项

- **`per_unit` 的展示必须说清乘法关系**，否则会从「少显示一个倍率」变成「显示了但仍然误导」。建议直接给一个算式示例：`基准价 × 时长(秒) × 分辨率倍率`。
- **倍率是相对基准价的相对值**，基准价是管理员配置的 `ModelRatio` / `ModelPrice`。展示折算后单价时必须用当前分组倍率与货币设置换算，与 `PriceSection` 现有逻辑保持一致（`currency.quotaDisplayType` / `usdExchangeRate`）。
- **不要在注册表里放 nil 或 0 倍率**。`types.PriceData.AddOtherRatio` 会拒绝非正数、NaN、+Inf（[types/price_data.go:35](../types/price_data.go#L35)），声明侧应当同样校验，避免声明出一个实际会被拒的倍率。
- **未配置的参数组合按基准价展示**。doubao 的 `GetVideoInputRatio` 对未配置组合返回 1.0（如 fast 无 1080p/4K，由上游报错），展示时不要凭空补格子。
- **`TaskPricePatches`（按次计费白名单，env 配置）下 OtherRatios 不参与扣费**（[relay/relay_task.go:198](../relay/relay_task.go#L198)），这类模型不应展示参数计价表。
- **`relaykit/` 不受影响**；若最终触及该模块，必须用 `cd relaykit && GOWORK=off go build ./...` 单独验证。

## 7. 未决

- 图片侧的 `n` / `prompt_extend` 是否纳入首版（阶段 2 第 11 步）。倾向纳入：`n` 是最直观的线性乘数，用户误判成本低但发生频率高。
- 是否同时在广场提供一个「参数 → 预估价格」的小计算器。倾向不做首版：先把静态价目表暴露出来，观察是否还有人问。
