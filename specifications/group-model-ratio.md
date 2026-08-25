# 分组模型倍率（GroupModelRatio）设计文档

> 目标：让管理员按「分组 × 模型」设定倍率，实现分组级差异化定价，
> 而不必为同一个上游模型克隆出多个改名模型。
> 语义：**该值就是这个模型在这个分组的分组倍率** —— 取代 `GroupRatio` / `GroupGroupRatio`，
> 不与之叠乘；模型自身的定价方式（全局模型倍率、固定价、计费表达式）照常生效。
> 范围：三种计费方式全部生效（见 §5），配置层不需要区分模型的计费方式。
> 状态：已实现。语义在 2026-08 从「覆盖即最终价」改为系数并带数据迁移（见 §2.2、§6）。
> 基线：`staging`（2026-08-25）。

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
| **`GroupModelRatio`** | **分组 × 模型** | **该模型在该分组的分组倍率（取代上面两者）** |

关键的语义选择是**取代分组倍率、而不是与之叠乘**，且它是系数而不是绝对价。理由见 §2.2。

## 2. 语义

### 2.1 计价公式

命中与未命中的差别只有一处：分组倍率取哪个值。

```
分组倍率 = GroupModelRatio[计费组][模型]                    // 命中
         或 GroupGroupRatio[用户组][计费组]                  // 未命中且配了分组间覆盖
         或 GroupRatio[计费组]                              // 未命中

最终价 = 模型自身定价 × 分组倍率
        // 按量倍率模型：模型自身定价 = ModelRatio[模型] × tokens
        // 阶梯模型：    模型自身定价 = 表达式求值结果
```

命中时 `GroupGroupRatio` 同时失效——它也是一种分组倍率，两个都生效就成了叠乘。
这一步由 `ratio_setting.ResolveGroupModelGroupRatio` 统一完成，禁止在调用点自行拼装。
签名里只有分组倍率，正是为了让「本配置不给模型定价」在类型上就无法违反：

```go
// 命中 → (override, true)；未命中 → (baseGroupRatio, false)
func ResolveGroupModelGroupRatio(usingGroup, model string, baseGroupRatio float64) (
    groupRatio float64, overridden bool)
```

### 2.2 为什么是「取代分组倍率的系数」而不是「覆盖即最终价」

第一版实现的是绝对价语义（命中时该值就是最终模型倍率，分组倍率归一为 1.0）。
理由是管理员想表达的是「这个模型在这个分组卖多少钱」，绝对价便于和成本对账。
2026-08 改成系数，原因：

- **心智模型统一**。阶梯计费模型的价格是一张随上下文长度变化的价目表，标量替换不了整张表，
  绝对价语义在那条路径上无从落地。要让两类模型共用一个配置字段，只能选系数。
  两套语义两个字段的方案（`GroupModelRatio` + `GroupModelExprRatio`）意味着同一个数字
  在不同字段里差出数量级，是更糟的抽象。
- **跟随上游同步**。`ModelRatio` 会被上游倍率同步刷新，绝对价不会跟着动，
  会在上游降价后变成高于市场的僵尸价；系数自动跟随。
- **表达折扣是主要诉求**。「vip 这个模型八折」用系数是一步，用绝对价要先心算 `10 × 0.8`，
  且每次全局倍率变动都要重算。

保留下来的不变量是「命中后分组倍率对该模型不再生效」——这条在两种语义下都成立，
只是从「归一为 1.0」变成了「被系数取代」。

代价是**绝对价不再可直接表达**，以及必须做一次性数据迁移（§6）。
另一个代价是它不再能给模型定价：旧语义下「只配分组值、不配全局倍率」是受支持的用法，
新语义下必须先有全局倍率或表达式（§3.4）。

### 2.3 派生倍率

系数作���在整体价位上，其余倍率照常相乘：

```
输入价     = ModelRatio × 系数
输出价     = ModelRatio × 系数 × CompletionRatio
缓存读取价 = ModelRatio × 系数 × CacheRatio
音频、图片等���理
```

也就是说它只调整该模型在该分组的整体价位，输入输出比价关系不变。
若需要输入输出解耦，属于 Phase 2 的 `GroupModelCompletionRatio`，本期不做。

### 2.4 边界取值

| 情况 | 行为 |
|---|---|
| 值 = 0 | 命中，分组倍率 0，走既有「倍率为 0 即免费模型」逻辑 |
| 值 < 0 | 保存时被 `CheckGroupModelRatio` 拒绝 |
| 值 > 1 | 合法，表示该分组加价 |
| 分组倍率 = 0 且未命中 | 保留 0（免费），不得被 `|| 1` 之类的写法吃掉 |
| 模型无任何定价（无全局倍率、无固定价、无表达式） | 该模型不可计费，配了本项也不会让它可用（§3.4） |
| 模型是固定价 | 生效，乘在固定价上（见 §5） |
| 模型是 `tiered_expr` | 生效，乘在表达式结果上（见 §5） |

## 3. 计费路径注入点

分组模型倍率必须在**每一条**会重新推导倍率的路径上应用。任何一条漏掉，都会因为预扣与结算不一致
而把它抹掉——结算普遍是全量替换语义（`task.Quota = actualQuota`，差额补扣或退还），
漏掉的一侧会直接决定最终收费。

| # | 路径 | 位置 | 说明 |
|---|---|---|---|
| 1 | 同步请求预扣 | `relay/helper/price.go` `ModelPriceHelper` | 在计费方式分支**之前**应用，一次覆盖按量 / 固定价 / 阶梯三条路径 |
| 2 | 按次 / 任务预扣 | `relay/helper/price.go` `ModelPriceHelperPerCall` | MJ、异步任务走这里，同样在分支之前应用 |
| 3 | 实时（WSS）计费 | `service/quota.go` `PreWssConsumeQuota` | 该路径独立重取倍率，必须单独注入 |
| 4 | 异步任务差额结算 | `service/task_billing.go` `RecalculateTaskQuotaByTokens` | 见 §3.3 |
| 5 | 选定渠道后的分组刷新 | `relay/helper/price.go` `RefreshGroupPricingForSelectedGroup` | auto 分组重试会换计费分组，见 §5.2 |

在计费方式分支之前应用是有意的：`groupRatioInfo` 是三条路径的公共输入，
在分支前写一次就不可能漏掉某一条，也不需要在每条分支里重复判断。

同步文本请求的结算不重新取倍率，直接复用 `ModelPriceHelper` 产出的 `PriceData`，
因此天然覆盖——前提是注入点 5 让 `PriceData` 跟着最终分组走。
阶梯计费的结算复用 `BillingSnapshot`（其 `GroupRatio` 已含系数），同样无需额外注入。

注入点 1、2、5 共用 `applyGroupModelRatio`，它在写入倍率的同时把 `GroupSpecialRatio` /
`HasSpecialRatio` 清掉。禁止在调用点自行拼装，漏掉这步清理会让分组间覆盖继续叠乘。

### 3.3 异步任务结算的判定顺序

`RecalculateTaskQuotaByTokens` 自行读取倍率重算，其判定顺序必须与 `ModelPriceHelperPerCall` 严格一致：

```
固定价（GetModelPrice / DefaultModelPriceMap）
  → 命中则不按 token 重算（预扣的按次额度就是最终额度）
全局模型倍率
  → 按 token 重算
分组模型倍率
  → 与计费方式无关，始终取代分组倍率
```

分组模型倍率不再参与「是否按 token 重算」的判定（旧语义下它会把模型变成按量计费，
所以必须排在固定价之后）。现在它只决定分组倍率，因此无条件应用即可，
与 `ModelPriceHelperPerCall` 的顺序天然一致。

### 3.4 本配置不给模型定价

`GroupModelRatio` 只替换分组倍率，模型自己的价格始终取全局配置。因此**没有任何定价
（全局倍率 / 固定价 / 计费表达式都没有）的模型不会因为配了本项就变成可计费**——
`ratioSuccess` 保持为假，未开启「接受未设定价格模型」的用户会拿到「价格未配置」错误。

这是与第一版语义的一处行为差异：旧语义允许「只配分组值、不配全局倍率」，
新语义要求先配全局价。文档在管理员指南里明确写出了这个前提。

对应地，`HasModelBillingConfig`（用于 `ListModels` 过滤和 Gemini 非思考变体探测）
不再需要分组视角，签名回到：

```go
func HasModelBillingConfig(modelName string) bool
```

判定顺序上 `tiered_expr` 必须最先短路：这类模型只由表达式定价，
一个 `billing_mode = tiered_expr` 但表达式为空的模型不能因为全局倍率被判为可计费，
否则它会进入模型列表，而实际调用在 `modelPriceHelperTiered` 直接报错。
`GroupModelRatio` 同理不参与这个判定——它是系数，不构成定价。

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

`GET /api/pricing` 返回 `group_model_ratio` 字段，形状与配置一致。

**只返回用户可用分组的条目**（`GetGroupModelRatioForUsableGroups`）。
这一点是必需的：若原样返回全表，专属分组、私有分组的价格会泄露给所有能打开定价页的人。

### 4.3 前端定价计算

后端只下发原始数据，最终价由前端计算（与既有分组倍率的处理方式一致）。
「有效分组倍率」的解析集中在 `web/src/features/pricing/lib/model-helpers.ts`，
两类计费模型共用，避免两处实现漂移：

- `getGroupModelRatio(model, group)` —— 读取该模型在该分组的分组模型倍率，无则 `undefined`。
- `getEffectiveGroupRatio(model, group, groupRatio)` —— 命中取它，否则取分组倍率。
- `getDisplayEffectiveGroupRatio(model, selectedGroup)` —— 模型广场汇总用：
  指定分组时取该分组，未指定时取跨可用分组的**最低**值；分组模型倍率参与这个最低价比较。

在此之上：`price.ts` 的按 token 定价乘 `model_ratio`（`effectiveRatioProduct` /
`getDisplayRatioProduct`），按次定价乘 `model_price`（`formatFixedPrice` / `formatRequestPrice`），
`dynamic-price.ts` 把它作为表达式结果的系数（`getDynamicGroupRatio` /
`getDynamicDisplayGroupRatio`）。模型详情页的阶梯分组对照表也必须走 `getDynamicGroupRatio`，
否则展示价会与实际计费不一致。

读取分组倍��一律走 `getConfiguredGroupRatio`，它保留合法的 0 值；
写成 `groupRatio[group] || 1` 会把免费分组显示成原价。

## 5. 三种计费方式

| 计费方式 | 效果 |
|---|---|
| 按量倍率 | `tokens × ModelRatio × 该值` |
| 固定价（`ModelPrice`） | `ModelPrice × QuotaPerUnit × 该值`，`OtherRatios`（张数、时长、分辨率）照常相乘 |
| 表达式（`tiered_expr`） | `expr(...) / 1e6 × QuotaPerUnit × 该值`，阶梯结构不变 |

三种都能共用同一个字段，是系数语义的直接收益（§2.2）：三条路径的最后一步都是
`× 分组倍率`，系数只是替换了这个乘数。因此配置层不必区分计费方式，
「静默失效」这一整类问题（以及为它准备的后台提示 UI）随之消失。

阶梯模型的价目表按比例平移，长上下文加价关系不受影响。
曾考虑过的替代方案：命中时让阶梯模型退回按量倍率计费。否决理由是它会**静默丢掉长上下文加价**
——管理员配了一个「特价」，结果超长请求按单一倍率计费，站点少收钱且从配置上看不出来。

### 5.1 阶梯计费的注入细节

阶梯计费只有一条定价入口：`relay/helper/price.go` 的 `modelPriceHelperTiered`，
系数由调用方 `ModelPriceHelper` 在分支前应用，因此它同时进入 `BillingSnapshot.GroupRatio`，
结算（`service/tiered_settle.go` `TryTieredSettle`）复用快照，无需第二处注入。

`refreshTieredBillingGroup` 用 `EstimatedQuotaBeforeGroup × 当前分组倍率` 重算预留额度，
而 `EstimatedQuotaBeforeGroup` 与分组无关（表达式全局唯一），
所以 auto 分组重试只要刷新 `PriceData.GroupRatioInfo.GroupRatio` 即可，见 §5.2。

异步任务（`ModelPriceHelperPerCall`）不走表达式计费。
实时（WSS）预��（`PreWssConsumeQuota`）本身不支持表达式计费，且固定价直接 early return，
只需处理按量倍率一条。

### 5.2 auto 分组重试必须重解析

`ModelPriceHelper` 在整个请求里只跑一次（`controller/relay.go`，重试循环之前），
而 auto 分组会在重试时换计费分组。原实现只在 `getChannel` 里用 `HandleGroupRatio`
刷新分组倍率，分组模型倍率不参与刷新，于是换组后出现两个错误：

- 旧分组的分组模型倍率被新分组的分组倍率顶掉（第一版语义下更糟：
  `PriceData.ModelRatio` 留着旧分组的绝对覆盖值，`GroupRatio` 变成新分组的分组倍率，
  实际按「覆盖 × 分组倍率」结算，正是本方案明确排除的叠乘）
- 新分组自己的分组模型倍率不生效

因此 `helper.RefreshGroupPricingForSelectedGroup` 取代了原来的 `HandleGroupRatio` 调用：
它按新分组重新解析分组倍率与分组模型倍率，同样不区分计费方式。

## 6. 语义迁移（绝对值 → 系数）

> 运维操作手册：[guides/migration/group-model-ratio-coefficient.md](../guides/migration/group-model-ratio-coefficient.md)。

第一版的 `GroupModelRatio` 是绝对值语义。同一个数字在两种语义下都合法且无法判别，
所以迁移必须是一次性的、带标记位的数据改写，而不是运行时兼容。

`model/group_model_ratio_migration.go` 的 `MigrateGroupModelRatioToCoefficient`：

```
新值 = 旧值 ÷ ModelRatio[模型]
```

分组倍率不参与换算——旧语义下命中的模型本来就不受分组倍率影响，
所以按此换算前后价格完全一致。

| 情况 | 处理 |
|---|---|
| 有全局倍率且 > 0 | 换算，保留完整精度 |
| 旧值 = 0 | 保留 0（免费在两种语义下同义），不做除法 |
| 无全局倍率、或全局倍率 ≤ 0 | **丢弃该条目**并逐条写入 `SysError` |
| 换算结果 NaN / Inf | 同上 |
| **模型按固定价计费** | **丢弃该条目**，单独一条 `SysError` |
| 某分组全部条目被丢弃 | 该分组键一并移除 |

前两类丢弃是因为新语义无法表达这类条目的原价格：留着会把绝对倍率当成系数，
价格差出数量级。

固定价那一类是另一个理由：这些条目在旧版本里**本来就不生效**，而新版本对固定价模型生效。
换算后保留等于让一批从未生效的配置突然开始影响价格，所以一并丢弃，日志里单独说明。
判定与 `ModelPriceHelperPerCall` 一致（`GetModelPrice` + `GetDefaultModelPriceMap`），
包含「同时配了固定价和全局倍率」这种能换算但不该换算的情况。

两类日志都给出 `分组/模型=原倍率` 便于管理员重新配置。

调用时机与幂等：

- 必须在 `model.InitOptionMap()` **之后**——换算依赖 `ratio_setting.GetModelRatio`
  的实际配置与模型名归一（`FormatMatchingModelName`）。
- 只在 `common.IsMasterNode` 执行，从节点通过选项同步（默认 60s）拿到结果。
  因此多节点升级必须主节点先完成迁移：从节点按新语义解释库里的值，
  主节点还没迁移时它会把旧的绝对倍率当成系数，超收一个数量级。
- 通过 `GroupModelRatioSemantics = "coefficient"` 选项标记；空配置也写标记，
  否则新装站点第一次配好后重启会被当成旧数据再除一遍。

### 6.1 失败必须终止启动

迁移失败有两种后果，都会错误计费：

| 失败点 | 后果 |
|---|---|
| 换算没写进库 | 进程用新语义解释旧的绝对倍率——旧值 5 被当成 5 倍，**超收一个数量级** |
| 换算写成功、标记没写成功 | 下次启动再除一次全局倍率，**少收一个数量级** |

所以存量配置非空时，任何一步失败都用 `ErrGroupModelRatioMigrationUnsafe` 包装，
`main.go` 识别到就 `FatalLog` 退出——宁可起不来，也不能带着错误的价格对外服务。
存量配置为空时上述两种后果都不存在（没有条目可解释错，标记缺失只是下次重试），
返回普通错误、只记日志，避免一次瞬时数据库故障挡住启动。

回滚同样不是免费的：迁移后库里存的是系数，用旧语义的二进制回滚会把 0.5 读成
「最终倍率 0.5」，少收到 1/10。回滚必须同时把 `GroupModelRatio` 恢复成迁移前的值
并删掉 `GroupModelRatioSemantics` 这行，所以升级前应当先把该选项的值备份出来。

## 7. 后台配置界面

分组倍率设置页有可视化与 JSON 两种模式。`GroupModelRatio` **只在 JSON 模式**下可编辑。

可视化编辑器整体以分组为单位组织（一行一个分组），而这张表的内层键是模型——
基数差两个量级。直接套用 `GroupGroupRatio` 的「分组折叠 + 内嵌表格」模式，
会退化成「展开一个分组后是几百行表格、加一条要手打模型名」，比 JSON 更难用。

若后续要做，建议按模型维度组织（外层模型、内层分组），并在每行展示
`全局倍率 × 系数` 的换算结果。该项未纳入本期。

数据是安全的：`GroupModelRatio` 是独立注册的表单字段，可视化编辑器只回写
`GroupRatio` / `UserUsableGroups` / `TopupGroupRatio`，不会清空它。

## 8. 落地文件

| 文件 | 改动 |
|---|---|
| `setting/ratio_setting/group_ratio.go` | 存储、读取、校验、`ResolveGroupModelGroupRatio` |
| `relay/helper/price.go` | 注入点 1、2、5；`applyGroupModelRatio` 在计费方式分支前应用；`HasModelBillingConfig` 去掉分组视角、`tiered_expr` 先短路 |
| `controller/relay.go` | `getChannel` 改调 `RefreshGroupPricingForSelectedGroup`（§5.2） |
| `service/quota.go` | 注入点 3 |
| `service/task_billing.go` | 注入点 4 |
| `model/group_model_ratio_migration.go`、`main.go` | 语义迁移（§6）及其调用时机 |
| `controller/option.go`、`model/option.go` | 选项校验与持久化 |
| `controller/pricing.go` | 定价接口按可用分组返回配置 |
| `controller/model.go`、`relay/gemini_handler.go` | `HasModelBillingConfig` 调用点 |
| `web/src/features/pricing/lib/model-helpers.ts` | 有效分组倍率解析（三种计费方式共用） |
| `web/src/features/pricing/lib/price.ts` | 按量倍率模型的前端定价计算 |
| `web/src/features/pricing/lib/dynamic-price.ts` | 阶梯模型的前端定价计算 |
| `web/src/features/pricing/components/model-details.tsx` | 阶梯模型的分组对照表用有效系数 |
| `web/src/features/system-settings/models/group-ratio-form.tsx` | JSON 编辑入口（静默失效提示已随例外消失而删除） |

## 9. 测试

| 层次 | 位置 | 覆盖 |
|---|---|---|
| 解析器 | `setting/ratio_setting/group_model_ratio_test.go` | 命中 / 未命中 / 值为 0 / 分组隔离 / 模型倍率不被替换 |
| 计价端到端 | `relay/helper/price_test.go` | 按量 / 固定价 / 阶梯三条路径的注入（含按次预扣）、分组间覆盖失效、auto 分组换组后重解析、`HasModelBillingConfig` 不认分组模型倍率与 `tiered_expr` 短路 |
| 任务结算 | `service/task_billing_test.go` | 倍率生效、单独存在时不构成定价、固定价不重算 |
| 语义迁移 | `model/group_model_ratio_migration_test.go` | 换算公式、无法换算与固定价条目被丢弃、幂等、空配置也写标记 |
| 前端定价 | `web/src/features/pricing/lib/__tests__/group-model-ratio-price.test.ts`（按量 + 固定价）、`group-model-ratio-dynamic-price.test.ts`（阶梯） | 取代分组倍率、最低价比较、0 倍率保留 |

前端断言一律用「等价配置产出同一价格」的形式，不断言货币字符串字面量——
`formatCurrencyFromUSD` 依赖全局货币显示配置，硬编码会在管理员改显示币种后失败。

## 10. 已知限制

- **可视化编辑器无入口**（§7）。只能在 JSON 模式下编辑。
- **账单里没有「命中了分组模型倍率」的显式标记**。日志表现为 `model_ratio` = 全局倍率、
  `group_ratio` = 配置的系数，可以推断但要对着配置看。第一版留下的
  `GroupRatioInfo.ModelRatioOverridden` 标记位始终只写不读，已随语义迁移一并删除；
  若要在账单上显式标注，应在写日志时按需重新解析，而不是恢复这个字段。
- **阶梯模型只能整体平移，不能换价目表**。分组要一张结构不同的阶梯表做不到（见 §11）。
- **绝对价不可直接表达**（§2.2）。想把某分组某模型定成固定倍率，得自己算 `目标 ÷ 全局倍率`，
  且全局倍率变动后该分组的绝对价会跟着变。
- **迁移会丢弃无法换算的条目**（§6）。升级后需要看一次启动日志。

## 11. 后续（Phase 2，未排期）

- `GroupBillingExpr`：分组 × 模型的完整表达式覆盖，用于分组需要独立阶梯表的场景。
  结算天然跟随 `BillingSnapshot.ExprString`，但 `refreshTieredBillingGroup` 必须在换组时
  重跑表达式刷新 `EstimatedQuotaBeforeGroup` / `ExprHash` / `ExprVersion`，
  保存时还需逐条 `SmokeTestExpr`。
- `GroupModelCompletionRatio`：输入输出解耦。
- 缓存 / 音频 / 图片倍率的分组覆盖。
