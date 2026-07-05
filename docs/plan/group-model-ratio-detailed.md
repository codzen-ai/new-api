# 分组模型倍率（GroupModelRatio）— 详细实现方案

> 目标读者：实现者
> 配套文档：`group-model-ratio-high-level.md`（背景、目标、投入评估）
> 本文件给出逐文件、逐函数的改动，含代码片段、边界处理、测试与上线步骤。

---

## 需求对齐（三条，全部 Phase 1）

| # | 需求 | 落点章节 |
|---|------|---------|
| 1 | 管理员可在前端为不同用户组设置模型倍率 | §2 后台接入 + §5 前端编辑器 |
| 2 | 有分组模型倍率的优先使用分组模型倍率 | §3 计价注入（命中即替换，未配置回退） |
| 3 | 用户能看到当前分组的模型价格 | §4 定价接口 + 前端定价页（**本轮已坐实进 Phase 1，不再延后**） |

---

## 0. 术语与数据模型

**新增配置项 `GroupModelRatio`**

```
GroupModelRatio: map[group]map[model]float64
```

- 外层 key：分组名（对应计费时的 `relayInfo.UsingGroup`，即「使用分组」）。
- 内层 key：模型名（对应 `info.OriginModelName`，即客户请求里的原始模型名）。
- value：该 (分组, 模型) 的**最终模型倍率**。
- 语义（**覆盖即最终价，已确认**）：命中即作为该分组下该模型的**最终倍率**，且**分组倍率（`GroupRatio` / `GroupGroupRatio`）对该模型失效**；未命中回退全局 `GetModelRatio × 分组倍率`。
- 补全倍率等其余机制不变（补全倍率仍按覆盖后的模型倍率比例作用于输出 token）。

**最终计费（Phase 1 后）**

```
若 GroupModelRatio[UsingGroup][model] 存在：
    effectiveModelRatio = 覆盖值
    effectiveGroupRatio = 1.0            # 分组倍率对该模型失效（覆盖即最终价）
否则：
    effectiveModelRatio = GetModelRatio(model)
    effectiveGroupRatio = 正常/特殊分组倍率（HandleGroupRatio 结果）
额度 = effectiveModelRatio × effectiveGroupRatio × (输入 token + 输出 token × 补全倍率) …
```

> **关键**：覆盖命中时必须把 `groupRatioInfo.GroupRatio` 归一为 `1.0`（并清除特殊分组倍率），否则会叠乘。由于一次请求只有一个模型，归一 groupRatio 不影响其它计费。

---

## 1. 配置存储层：`setting/ratio_setting/group_ratio.go`

完全仿照同文件里已有的 `GroupGroupRatio`（二维 map）实现。

### 1.1 新增默认值与 map（文件顶部，`defaultGroupGroupRatio` 附近）

```go
// 分组 × 模型 倍率覆盖：GroupModelRatio[group][model] = ratio
var defaultGroupModelRatio = map[string]map[string]float64{}

var groupModelRatioMap = types.NewRWMap[string, map[string]float64]()
```

### 1.2 加入 `GroupRatioSetting` 结构体

```go
type GroupRatioSetting struct {
	GroupRatio              *types.RWMap[string, float64]            `json:"group_ratio"`
	GroupGroupRatio         *types.RWMap[string, map[string]float64] `json:"group_group_ratio"`
	GroupModelRatio         *types.RWMap[string, map[string]float64] `json:"group_model_ratio"` // 新增
	GroupSpecialUsableGroup *types.RWMap[string, map[string]string]  `json:"group_special_usable_group"`
}
```

### 1.3 `init()` 里注册

```go
func init() {
	// … 现有 groupSpecialUsableGroup / groupRatioMap / groupGroupRatioMap …
	groupModelRatioMap.AddAll(defaultGroupModelRatio)

	groupRatioSetting = GroupRatioSetting{
		GroupSpecialUsableGroup: groupSpecialUsableGroup,
		GroupRatio:              groupRatioMap,
		GroupGroupRatio:         groupGroupRatioMap,
		GroupModelRatio:         groupModelRatioMap, // 新增
	}

	config.GlobalConfig.Register("group_ratio_setting", &groupRatioSetting)
}
```

### 1.4 读写 / 校验函数（仿 `GetGroupGroupRatio` 等）

```go
// GetGroupModelRatio 返回某分组下某模型的模型倍率覆盖；不存在返回 (-1, false)
func GetGroupModelRatio(group, model string) (float64, bool) {
	gm, ok := groupModelRatioMap.Get(group)
	if !ok {
		return -1, false
	}
	ratio, ok := gm[model]
	if !ok {
		return -1, false
	}
	return ratio, true
}

func GroupModelRatio2JSONString() string {
	return groupModelRatioMap.MarshalJSONString()
}

func UpdateGroupModelRatioByJSONString(jsonStr string) error {
	return types.LoadFromJsonString(groupModelRatioMap, jsonStr)
}

// CheckGroupModelRatio 校验 JSON 合法且倍率非负
func CheckGroupModelRatio(jsonStr string) error {
	check := make(map[string]map[string]float64)
	if err := json.Unmarshal([]byte(jsonStr), &check); err != nil {
		return err
	}
	for group, models := range check {
		for modelName, ratio := range models {
			if ratio < 0 {
				return errors.New("group model ratio must be not less than 0: " + group + "/" + modelName)
			}
		}
	}
	return nil
}
```

> 注意：本仓库业务代码要求 JSON 走 `common.*` 包装（见 AGENTS.md）。但此文件里现有 `CheckGroupRatio` 已直接用 `encoding/json`，为保持一致可沿用；如需合规则改用 `common.Unmarshal`。实现时与同文件既有风格保持一致即可。

---

## 2. 后台接入层：`model/option.go`

### 2.1 展示（约第 147 行，`GroupGroupRatio` 之后）

```go
common.OptionMap["GroupRatio"] = ratio_setting.GroupRatio2JSONString()
common.OptionMap["GroupGroupRatio"] = ratio_setting.GroupGroupRatio2JSONString()
common.OptionMap["GroupModelRatio"] = ratio_setting.GroupModelRatio2JSONString() // 新增
```

### 2.2 更新路由（约第 531 行的 switch，`case "GroupGroupRatio"` 之后）

```go
case "GroupGroupRatio":
	err = ratio_setting.UpdateGroupGroupRatioByJSONString(value)
case "GroupModelRatio": // 新增
	err = ratio_setting.UpdateGroupModelRatioByJSONString(value)
```

> 若存在保存前校验的入口（如管理端更新 option 时调用 `Check*`），在对应位置加 `CheckGroupModelRatio(value)`，与 `CheckGroupRatio` 一致。

---

## 3. 计价注入层（核心）

### 3.1 主注入点：`relay/helper/price.go` → `ModelPriceHelper`

当前代码（第 95 行附近）：

```go
modelRatio, success, matchName = ratio_setting.GetModelRatio(info.OriginModelName)
if !success {
	// … acceptUnsetRatio 处理 …
}
```

改为：先取全局倍率，再套用分组覆盖（**覆盖即最终价：同时归一 groupRatio**）：

```go
modelRatio, success, matchName = ratio_setting.GetModelRatio(info.OriginModelName)
// 分组模型价覆盖：命中即最终倍率，分组倍率对该模型失效
// （info.UsingGroup 此时已由 HandleGroupRatio 解析为最终分组）
if override, ok := ratio_setting.GetGroupModelRatio(info.UsingGroup, info.OriginModelName); ok {
	modelRatio = override
	success = true // 有显式覆盖即视为已配置，跳过 unset 处理
	// 覆盖即最终价：分组倍率（含特殊分组倍率）对该模型失效
	groupRatioInfo.GroupRatio = 1.0
	groupRatioInfo.GroupSpecialRatio = -1
	groupRatioInfo.HasSpecialRatio = false
	groupRatioInfo.ModelRatioOverridden = true // 新增标记，供日志/展示区分
}
if !success {
	// … 原 acceptUnsetRatio 处理不变 …
}
```

需在 `types.GroupRatioInfo` 增加 `ModelRatioOverridden bool` 字段（用于日志/结算展示区分"该笔按分组模型价计费、分组倍率未参与"）。

**为什么这样就一致**：
- `modelRatio` 写入 `PriceData.ModelRatio`（第 145 行），归一后的 `groupRatioInfo` 写入 `PriceData.GroupRatioInfo`（第 147 行）。
- 预扣：第 114 行 `ratio := modelRatio * groupRatioInfo.GroupRatio = override × 1.0 = override`。✓
- 结算：`service/quota.go` 第 182-183 行 `modelRatio := PriceData.ModelRatio` / `groupRatio := PriceData.GroupRatioInfo.GroupRatio` 复用同值，**自动一致**，最终仍是 `override × 1.0`。✓
- 免费模型：第 135 行 `if modelRatio == 0`（覆盖为 0 即免费）自动生效。

> `HandleGroupRatio(c, info)` 在第 70 行已先执行，auto 分组已把 `info.UsingGroup` 更新为最终分组，且已算好 `groupRatioInfo`；此处在其结果上归一，顺序正确。
>
> **归一 groupRatio 安全性**：一次请求只有一个模型，`groupRatioInfo.GroupRatio` 是该请求的唯一分组倍率，归一为 1.0 只影响这一个（被覆盖的）模型的计费，不波及其它。

### 3.2 按次/按量计费：`ModelPriceHelperPerCall`（同文件第 182 行附近）

该函数在 fallback 到按量计费时也调用 `GetModelRatio`。若要让**按量计费**的任务类模型也支持分组覆盖，同样注入（覆盖即最终价 → 归一 groupRatio）：

```go
modelRatio, ratioSuccess, matchName = ratio_setting.GetModelRatio(info.OriginModelName)
if override, ok := ratio_setting.GetGroupModelRatio(info.UsingGroup, info.OriginModelName); ok {
	modelRatio = override
	ratioSuccess = true
	groupRatioInfo.GroupRatio = 1.0
	groupRatioInfo.GroupSpecialRatio = -1
	groupRatioInfo.HasSpecialRatio = false
	groupRatioInfo.ModelRatioOverridden = true
}
```

> 固定价（`usePrice` 分支，走 `GetModelPrice`）不在 Phase 1 范围；见第 6 节 Phase 2。

### 3.3 表达式计费（tiered_expr）说明

`modelPriceHelperTiered` 用表达式产出 `rawCost`，再乘 `groupRatio`（第 268 行），**不经过 `modelRatio`**。因此本方案的模型倍率覆盖对表达式计费模型**不生效**（这类模型的差异化应通过表达式本身或分组倍率实现）。此为预期行为，需在文档/后台 UI 上向管理员说明：GroupModelRatio 仅作用于「按量倍率」模型。

### 3.4 实时(WSS)旁路：`service/quota.go` → `PreWssConsumeQuota`

当前（第 108-109 行）**独立重取**倍率，而非复用 PriceData：

```go
groupRatio := ratio_setting.GetGroupRatio(relayInfo.UsingGroup)
modelRatio, _, _ := ratio_setting.GetModelRatio(modelName)
```

需补注入，保持与主链路一致（覆盖即最终价 → 同时归一本地 groupRatio）：

```go
groupRatio := ratio_setting.GetGroupRatio(relayInfo.UsingGroup)
modelRatio, _, _ := ratio_setting.GetModelRatio(modelName)
// 覆盖即最终价：命中则 modelRatio=override 且 groupRatio=1.0
modelRatio, groupRatio, _ = ratio_setting.ResolveGroupModelPrice(relayInfo.UsingGroup, modelName, modelRatio, groupRatio)
```

（`ResolveGroupModelPrice` 见 §3.5）此路径用本地 `modelRatio`/`groupRatio` 变量而非 `groupRatioInfo` 结构，故用返回 (modelRatio, groupRatio) 的解析器最贴合。

> 复核：确认 realtime 结算侧 `PostWssConsumeQuota`（第 157 行起）读取的是 `relayInfo.PriceData.ModelRatio`（第 303 行）还是重取；若重取则同样注入。计费的「预扣」与「结算」两侧必须用同一 `modelRatio`，否则会出现预扣/实扣不一致。实现时对每个重取 `GetModelRatio` 的位置逐一核对补齐。

### 3.5 排查清单（务必逐一核对）

以 `grep -rn "GetModelRatio(" service/ relay/` 全量列出调用点，判断每处是否属于「计费」用途（而非展示/统计）。**凡计费用途**都要在取值后补一层 `GetGroupModelRatio` 覆盖。已知计费相关：
- `relay/helper/price.go` `ModelPriceHelper` ✅（3.1）
- `relay/helper/price.go` `ModelPriceHelperPerCall` ✅（3.2）
- `service/quota.go` `PreWssConsumeQuota` ✅（3.4）
- 其余音频/任务/视频路径（如 `service/task_video.go`）如独立取倍率也需覆盖——实现时核对。

> **单一真源封装（推荐）**：由于「覆盖即最终价」把 modelRatio 覆盖与 groupRatio 归一**耦合**在一起，各注入点必须成对处理，散写极易漏掉归一而叠乘。封装成一个解析器收敛为单一真源：
>
> ```go
> // ResolveGroupModelPrice 覆盖即最终价语义。
> // 命中分组模型价 → (override, 1.0, true)；未命中 → (baseModelRatio, baseGroupRatio, false)。
> func ResolveGroupModelPrice(usingGroup, model string, baseModelRatio, baseGroupRatio float64) (modelRatio float64, groupRatio float64, overridden bool) {
> 	if override, ok := GetGroupModelRatio(usingGroup, model); ok {
> 		return override, 1.0, true
> 	}
> 	return baseModelRatio, baseGroupRatio, false
> }
> ```
>
> - **本地变量场景**（PreWssConsumeQuota，§3.4）：直接 `modelRatio, groupRatio, _ = ResolveGroupModelPrice(...)`。
> - **结构体场景**（ModelPriceHelper/PerCall，§3.1/§3.2）：`modelRatio, gr, overridden := ResolveGroupModelPrice(info.UsingGroup, model, modelRatio, groupRatioInfo.GroupRatio)`；再把 `groupRatioInfo.GroupRatio = gr`，`overridden` 时清 `GroupSpecialRatio=-1 / HasSpecialRatio=false / ModelRatioOverridden=true`（这几个是展示字段，数值已由解析器保证）。
>
> 这是一个稳定的业务概念（「某分组下某模型的实际生效计费倍率」），符合 AGENTS.md「稳定业务概念才独立成函数」的原则。所有计费点统一调它，杜绝漏归一。

---

## 4. 定价展示层（需求 3：用户看到当前分组的模型价格）

### 4.0 现状与难点

`GET /api/pricing` → `controller/pricing.go:GetPricing`（第 36 行）当前返回：

- `data`：`[]model.Pricing`，**每个模型一个 `model_ratio` / `model_price`**（`model/pricing.go` 的 `Pricing` 结构，第 18 行），是**全局单值**，不含分组维度。
- `group_ratio`：`map[group]float64`，分组标量（已按用户可用分组过滤，并对用户分组套用了 `GroupGroupRatio`，第 49-54 行）。
- `usable_group`：用户可用分组。

前端定价页计算展示价的逻辑在 `web/default/src/features/pricing/lib/price.ts`：

```ts
// formatPriceForGroup(model, group, …, groupRatio)  第 ~202 行
const ratio = groupRatio[group] || 1
// 展示价 ≈ model.model_ratio × … × ratio
```

即 **展示价 = 模型全局倍率 × 分组标量**。这与 `GroupModelRatio` 冲突：同一模型在不同分组下 `model_ratio` 已不同，单值无法表达。

**因此需求 3 必须改造「定价接口 + 前端价格计算」，把「模型全局倍率」升级为「按分组取生效倍率」。**

### 4.1 后端：定价接口补一份分组覆盖数据

在 `GetPricing`（`controller/pricing.go`）返回体中，新增一个**已按可用分组过滤**的覆盖表：

```go
// 组装 group_model_ratio：仅保留用户可用分组
groupModelRatio := map[string]map[string]float64{}
for g := range usableGroup {
	if gm, ok := ratio_setting.GetGroupModelRatioByGroup(g); ok { // 见下方新增取值函数
		// 仅保留该分组下确有覆盖的模型
		copyInner := make(map[string]float64, len(gm))
		for m, r := range gm {
			copyInner[m] = r
		}
		groupModelRatio[g] = copyInner
	}
}

c.JSON(200, gin.H{
	// … 现有字段不变 …
	"group_ratio":       groupRatio,
	"group_model_ratio": groupModelRatio, // 新增：{group:{model:ratio}}
	"usable_group":      usableGroup,
	// …
})
```

需在 `setting/ratio_setting/group_ratio.go` 增补一个按分组整取的函数（供上面组装用）：

```go
// GetGroupModelRatioByGroup 返回某分组下的模型倍率覆盖表副本
func GetGroupModelRatioByGroup(group string) (map[string]float64, bool) {
	gm, ok := groupModelRatioMap.Get(group)
	if !ok || len(gm) == 0 {
		return nil, false
	}
	out := make(map[string]float64, len(gm))
	for m, r := range gm {
		out[m] = r
	}
	return out, true
}
```

> 过滤到 `usableGroup` 是**安全要求**：不能把其它分组（尤其专属/隔离分组）的价格泄露给无关用户，与现有 `group_ratio` 只返回可用分组保持一致。

**⚠️ 定价页是公开页（关键前提）**：`/api/pricing` 路由用 `middleware.HeaderNavModuleAuth("pricing")`（`router/api-router.go:34`）。当管理员把 pricing 模块设为公开（默认）时走 `TryUserAuth()`——**登录用户会带上 `id`，匿名访客则 `id` 不存在**。因此：

| 访客 | `group` | `usableGroup` | 看到的 `group_model_ratio` |
|------|---------|--------------|--------------------------|
| 已登录 | `user.Group` | 其可用分组 | 仅自己可用分组的覆盖价 |
| 匿名 | `""` | `GetUserUsableGroups("")` = 仅公开分组 | **仅公开分组**，专属/隔离分组价格不外泄 |

实现时**务必**用 `usableGroup`（而非 `GetGroupRatioCopy()` 全量）作为 `group_model_ratio` 的分组来源，否则匿名访客能拿到专属客户价，造成价格泄露。此过滤是需求 3 在公开页场景下的安全底线。

### 4.2 前端：按分组取「生效模型倍率」

**类型**（`web/default/src/features/pricing/types.ts`）：新增 `group_model_ratio?: Record<string, Record<string, number>>`。

**取数**（`web/default/src/features/pricing/hooks/use-pricing-data.ts` 第 58/66 行附近）：把 `group_model_ratio` 一并透出：

```ts
group_ratio: data.group_ratio,
group_model_ratio: data.group_model_ratio ?? {},
// …
groupModelRatio: data?.group_model_ratio ?? {},
```

**价格计算**（`web/default/src/features/pricing/lib/price.ts`）：引入一个「按分组算最终倍率」的小工具。注意**覆盖即最终价**——命中覆盖时**直接返回覆盖值，不再乘 `group_ratio`**：

```ts
// 某分组下某模型的最终倍率（用于展示价）
// 覆盖即最终价：命中覆盖 → 直接返回覆盖值（不乘 groupRatio）；否则 model_ratio × groupRatio
function finalRatioForGroup(
  model: PricingModel,
  group: string,
  groupRatio: Record<string, number>,
  groupModelRatio: Record<string, Record<string, number>>,
): number {
  const override = groupModelRatio?.[group]?.[model.model_name]
  if (typeof override === 'number') return override
  return model.model_ratio * (groupRatio[group] ?? 1)
}
```

改造以下函数，把内部「`model.model_ratio × group_ratio[group]`」整体换成 `finalRatioForGroup(model, group, groupRatio, groupModelRatio)`，并给入参补 `groupModelRatio`：

- `formatPriceForGroup(...)`（第 ~202 行，按量计费、指定分组）
- `formatFixedPriceForGroup(...)`（第 ~237 行，按次计费、指定分组）—— 固定价的分组覆盖属于 Phase 2，这里仍按现状用 `model_price × group_ratio`；仅倍率类模型走 `finalRatioForGroup`
- 「最低价」相关：`getMinGroupRatio(...)`（第 ~56 行）及其调用者（第 ~175/280 行）。当前「最低价」只按分组标量取 min。引入覆盖后，某模型的最低展示价应按 **每个可用分组各自的 `finalRatioForGroup(model, g, …)`** 取 min（命中覆盖的分组用覆盖值、未命中的用 `model_ratio × groupRatio[g]`），而非只对 `group_ratio` 取 min。需把这几处改为「遍历可用分组，算每组最终倍率，取最小」。

> 影响面：`price.ts` 是纯展示计算，改动集中、无副作用；配合 `pricing-columns.tsx` / `pricing-table.tsx` 若显式传 `groupRatio` 也需一并传 `groupModelRatio`。

### 4.3 i18n

若定价页新增任何文案（如「分组专属价」标记），走 `useTranslation()` + 英文源 key，`bun run i18n:sync`。

### 4.4 验收（需求 3）

- 管理员配置 `GroupModelRatio[vip][gpt-4o]` 后，用 vip 用户登录定价页，选择 vip 分组，`gpt-4o` 显示的是覆盖价；`claude-*` 显示全局价。
- default 用户定价页看不到 vip 的覆盖价（接口已按可用分组过滤）。
- 定价页「最低价」列对存在分组覆盖的模型取到正确的跨分组最小值。

---

## 5. 前端配置页

### 5.1 参照文件

- `web/default/src/features/system-settings/models/ratio-settings-card.tsx`（卡片容器）
- `web/default/src/features/system-settings/models/group-ratio-form.tsx`
- `web/default/src/features/system-settings/models/group-ratio-visual-editor.tsx`（二维可视化编辑器，最可复用）
- 类型：`web/default/src/features/system-settings/types.ts`

### 5.2 改造点

1. 在 `types.ts` 增加 `GroupModelRatio` 字段（字符串 JSON，与 `GroupGroupRatio` 同型）。
2. 新增 `group-model-ratio-form.tsx`（或扩展 visual editor 支持「分组 → 模型 → 倍率」三级）。UI 形态：选分组 → 添加多行 (模型, 倍率)。
3. 在 `ratio-settings-card.tsx` 挂入新表单，保存时提交 option key `GroupModelRatio`。
4. i18n：新增文案走 `useTranslation()` + `t('English key')`，locale 文件 `web/default/src/i18n/locales/*.json`（英文 key 为源），并 `bun run i18n:sync`。

### 5.3 前端校验

保存前做 JSON 合法性与倍率非负校验，错误就地提示（与 `GroupGroupRatio` 表单一致）。

---

## 5b. 交叉场景与边界（多公开/私有分组 × 覆盖表）

多个公开/私有分组与 `GroupModelRatio` 交叉时，**计费侧无歧义**（一次请求只有一个 `UsingGroup`，单点查表），但以下边界必须显式处理，否则会出现"配了没反应""最低价算错""价格叠乘"等问题。

### 5b.1 语义：覆盖即最终价，分组倍率对该模型失效（已确认）

`GroupModelRatio` 命中即作为该分组该模型的**最终倍率**，`groupRatio` 对它**不生效**：

| 分组配置 | groupRatio | gpt-4o 覆盖 | 最终 gpt-4o 倍率 | claude（无覆盖） |
|---------|:---:|:---:|:---:|:---:|
| groupRatio=1.0 | 1.0 | 5 | **5** | 全局 × 1.0 |
| groupRatio=0.8 | 0.8 | 5 | **5**（分组倍率被忽略） | 全局 × 0.8 |

**设计决定（需求方已确认）**：覆盖即最终价——所见即所得，不叠乘。实现上命中覆盖时把 `groupRatio` 归一为 1.0（见 §0/§3.1/§3.5）。
**好处**：管理员配"vip 的 gpt-4o = 5"就是 5，不受该组全局折扣影响；未覆盖的模型（如 claude）仍照常享分组倍率。这样同一分组内可精确区分"哪些模型按专价、哪些模型走全局折扣"。

### 5b.2 覆盖对部分模型静默失效（必须在 UI 标注）

| 模型计费方式 | 取价路径 | 覆盖是否生效 |
|-------------|---------|:---:|
| 按量倍率 | `GetModelRatio`（§3.1 注入点） | ✅ 生效 |
| 固定价/按次 | `GetModelPrice`（`usePrice` 分支） | ❌ 不生效（Phase 2 `GroupModelPrice`） |
| 表达式（tiered_expr） | 表达式，绕过 modelRatio（§3.3） | ❌ 不生效 |

后台配置页需明确提示：**分组模型倍率仅对「按量倍率」模型生效**。理想情况下，编辑器只允许对按量模型配置覆盖，或对非按量模型给出灰化/警告。

### 5b.3 悬空覆盖：模型未对该分组启用

若 `GroupModelRatio[G][M]` 存在但 M 的渠道不服务 G（`Pricing.EnableGroup` 不含 G）：
- 计费：请求在选渠道阶段即失败，覆盖不触发 —— 无害死配置。
- 展示：定价页按 `EnableGroup` 过滤，M 在 G 下不显示 —— 覆盖成悬空数据。

**建议**：后台保存 `GroupModelRatio` 时做一次交叉校验，对"模型未对该分组启用"的条目给出警告（非阻断）。

### 5b.4 auto 分组

覆盖必须用**解析后的实际分组**查（`HandleGroupRatio` 已先解析 `info.UsingGroup`），不能用字面量 `"auto"`。管理员应把覆盖配置在具体分组上。

### 5b.5 展示「最低价」必须逐分组计算（高危漏点）

见 §4.2：`getMinGroupRatio` 及调用者当前只对 `group_ratio` 取 min 再乘单一 `model_ratio`。引入覆盖后，最低价 = `min over 可用分组 g of ( effectiveModelRatio(model, g) × group_ratio[g] )`。**必须改**，否则多分组下最低价错误。

### 5b.6 私有分组隔离（见 §4.1）

展示侧 `group_model_ratio` 按 `usableGroup` 过滤，匿名/无关用户拿不到私有分组价。此为安全底线。

### 5b.7 与 GroupGroupRatio 的关系（覆盖时特殊分组倍率也失效）

用户在 `UserGroup=X` 用 `UsingGroup=Y` 调 model M 时：

- **M 有覆盖**：`最终倍率 = GetGroupModelRatio(Y, M)`，`GroupRatio(Y)` 与 `GroupGroupRatio(X,Y)` **都不参与**（§3.1 已清 `GroupSpecialRatio`）。
- **M 无覆盖**：`最终倍率 = GetModelRatio(M) × (GroupGroupRatio(X,Y)?special:GetGroupRatio(Y))`，与现状一致。

即覆盖命中时，任何分组级倍率（普通/特殊）一律让位于"覆盖即最终价"，不存在双重应用。

> 注：`GroupModelRatio` 键为 `UsingGroup`（单维），`GroupGroupRatio` 键为 `(UserGroup, UsingGroup)`（二维）。这是刻意的：模型定价档位跟随请求实际使用的分组（令牌/用户分组），无需二维。
>
> **✅ 已与需求方确认（设计冻结）**：业务模型为「一个客户固定一个分组、按分组制定模型价」，因此 `GroupModelRatio` 采用**单维键 `UsingGroup`**。不实现 `(UserGroup, UsingGroup)` 二维模型价。若未来出现"同一客户借用共享分组时另一套模型价"的二维需求，再在此基础上扩展（结构与注入点不变，仅键升级为二维）。

## 6. 分期范围

**Phase 1（本方案主体，含全部三条需求）**
- 需求 2：配置存储 + 后台接入 + 计价注入（3.1 / 3.2 / 3.4 + 封装 `ResolveGroupModelPrice`）+ 单测。
- 需求 1：后台前端二维编辑器（§5）。
- 需求 3：定价接口按分组返回生效倍率 + 前端定价页按分组展示（§4，**已坐实，不再延后**）。

**Phase 2（可选）**
- `GroupModelCompletionRatio`：分组 × 模型的补全倍率覆盖（输入输出解耦）。
- `GroupModelPrice`：分组 × 模型的固定价覆盖（`usePrice` 分支，绘图/任务类按次模型）。
- 缓存/音频/图片倍率的分组覆盖。
- 数据结构与 Phase 1 同型，注入点相同，按需增量添加。

---

## 7. 测试计划

参照 `service/text_quota_test.go` 的表驱动风格，用 `testify/require` + `assert`（见 AGENTS.md 测试规范）。测试前在 fixture 里显式初始化 `GroupModelRatio` 配置。

必测用例（断言精确期望额度）：

| 用例 | 期望（覆盖即最终价语义） |
|------|------|
| 覆盖命中：`GroupModelRatio[vip][gpt-4o]=5`，vip groupRatio=1.0 | 最终倍率 = 5 |
| **覆盖即最终价（5b.1）**：`GroupModelRatio[vip][gpt-4o]=5`，vip groupRatio=0.8 | 最终倍率 = **5**（分组倍率被忽略，不是 4） |
| 回退：vip 请求未配置的 claude-3-5，groupRatio=0.8 | 全局 `GetModelRatio` × 0.8，照常享分组倍率 |
| 分组隔离：default 请求 gpt-4o | 不受 vip 覆盖影响，用全局倍率 × default 分组倍率 |
| 特殊分组倍率失效（5b.7）：vip 命中覆盖且存在 GroupGroupRatio | 最终 = 覆盖值，特殊分组倍率不参与 |
| 覆盖为 0：`GroupModelRatio[vip][x]=0` | 触发免费模型逻辑，预扣/实扣为 0 |
| **预扣=实扣一致性**：同一 (group, model) 走 ModelPriceHelper 与结算 | 两侧 modelRatio、groupRatio(=1.0) 相同 |
| 多分组独立（5b）：default 与 vip 对同一模型不同覆盖 | 各自按本组覆盖计费，互不影响 |
| 静默失效（5b.2）：对固定价/tiered_expr 模型配覆盖 | 覆盖不生效，按原路径计费（回归保护） |
| 展示最低价（5b.5）：模型在多可用分组下有不同覆盖 | 最低价 = 跨分组 `finalRatioForGroup` 的 min |
| 私有分组隔离（4.1/5b.6）：匿名/无关用户请求 GetPricing | 返回的 group_model_ratio 不含私有分组 |

> `ResolveGroupModelPrice`（§3.5）应有独立表测：命中 → `(override, 1.0, true)`；未命中 → `(base, base, false)`。这是覆盖即最终价的核心不变量。

`ResolveGroupModelPrice` 封装是核心不变量，务必单独写表测（命中/回退/多分组）。

---

## 8. 数据库与兼容性

- **无 schema 变更**：`GroupModelRatio` 以 JSON 字符串存入现有 options 表（沿用 `GroupGroupRatio` 完全相同的存储路径），SQLite / MySQL / PostgreSQL 天然兼容。
- **无迁移脚本**。
- **热更新**：走现有 `config.GlobalConfig` 注册与 option 更新机制，与 `GroupGroupRatio` 同路径，配置保存后即时生效，无额外缓存刷新。

---

## 9. 上线步骤

1. 后端：改 `group_ratio.go`（1，含 `GetGroupModelRatio` / `GetGroupModelRatioByGroup` / `ResolveGroupModelPrice`）→ `option.go`（2）→ 用 `ResolveGroupModelPrice` 替换各计费注入点（3）→ 定价接口补 `group_model_ratio`（4.1）。
2. 单测（7）：`go test ./service/... ./setting/...` 全绿。
3. 前端：
   - 需求 1：配置页二维编辑器（5）。
   - 需求 3：定价页 `types.ts` / `use-pricing-data.ts` / `price.ts` 改造（4.2），按分组取生效倍率。
   - i18n sync，`bun run build` 通过。
4. 手工验证（对照 `/verify` 思路）：
   - 建 vip 分组 + `GroupModelRatio[vip][某测试模型]`，用 vip key 实调该模型与一个未覆盖模型，核对日志中 `model_ratio` / 实际扣费与预期一致。
   - 用 default key 调同模型，确认不受影响。
5. 提交（用户要求时再 commit/PR）；如提 PR 上游，关联 Issue #4602，并按 `.github/PULL_REQUEST_TEMPLATE.md` 填写、注明 AI 辅助生成。

---

## 10. 关键代码位置速查

| 用途 | 文件:行 |
|------|--------|
| 分组倍率配置（照抄模板） | `setting/ratio_setting/group_ratio.go` |
| GroupRatioInfo（加 `ModelRatioOverridden` 字段） | `types/price_data.go:5` |
| 主计价入口 | `relay/helper/price.go:67` `ModelPriceHelper` |
| 模型倍率取值点 | `relay/helper/price.go:95` |
| 按次/按量计价 | `relay/helper/price.go:167` `ModelPriceHelperPerCall` |
| 结算复用 PriceData | `service/quota.go:182` |
| 实时旁路重取倍率 | `service/quota.go:108` `PreWssConsumeQuota` |
| 后台展示配置 | `model/option.go:146` |
| 后台更新路由 | `model/option.go:530` |
| 定价接口 GetPricing | `controller/pricing.go:36` |
| Pricing 结构（单值 model_ratio） | `model/pricing.go:18` |
| 前端展示价计算 | `web/default/src/features/pricing/lib/price.ts:202`（`formatPriceForGroup`） |
| 前端定价数据 hook | `web/default/src/features/pricing/hooks/use-pricing-data.ts:58` |
| 前端定价类型 | `web/default/src/features/pricing/types.ts` |
| 前端分组倍率编辑器（配置页，照抄） | `web/default/src/features/system-settings/models/group-ratio-visual-editor.tsx` |
