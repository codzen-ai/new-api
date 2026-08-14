# MuleRouter 异步任务网关接入方案

> 目标：让 new-api 转发 MuleRouter `/vendors/{vendor}/v1/...` 下的异步生成类 API，
> 且新增 vendor / 新增模型时**只需改配置，不需改代码**。
> 本期范围：仅 `carrothub` 的 `qwen-image-edit-spicy`、`z-image-spicy`、`wan2.2-i2v-spicy` 三个模型；
> 其余 vendor 只作为「抽象是否够用」的检验对象，不实现、不配置。
> 状态：已实现（分支 `feat/mulerouter-async-task-gateway`），待评审与联调。
> 调研基线：仓库 `staging` 分支（2026-08-10）。
>
> 本文档已按实际实现回填，与代码一致；第 8 节列出落地文件，第 11 节记录相对初版设计的取舍变更。

## 1. 结论先行

按 vendor 硬编码是错的。MuleRouter 的 `/vendors/` 下已有 8+ 个 vendor、50+ 个模型，
且都在持续增加：

| Vendor | 模型数 | 动作段 |
|---|---|---|
| `alibaba` | 15（wan2.1 ~ wan2.6、qwen-image-*） | `generation` |
| `carrothub` | 8（*-spicy、face-swap、head-swap、wan-animate） | `generation` |
| `google` | 3（nano-banana-2 / -pro、veo3） | `generation`、`edit` |
| `klingai` | 2（kling-v3、kling-v3-omni） | `text-to-video`、`image-to-video`、`video-to-video-edit` 等 |
| `minimax` | 4（music-2.0/2.5、speech-2.8-hd/turbo） | `text-to-music`、`text-to-speech` |
| `mulerouter` | 5（*-spark、flashvsr） | `generation` |
| `midjourney` | 1 | `diffusion`、`video-diffusion` |
| `openai` | gpt-image-2 等 | `generation`、`edit` |

它们的**协议形状完全一致**，只有请求体字段不同：

```
POST   /vendors/{vendor}/v1/{model}/{action}              → 202 {"task_info": {...}}
GET    /vendors/{vendor}/v1/{model}/{action}/{task_id}    → 200 {"task_info": {...}, "images"|"videos": [...]}
DELETE /vendors/{vendor}/v1/{model}/{action}/{task_id}    → 取消任务
```

所以正确的抽象是：**一个渠道类型 + 一个协议适配器 + 一张管理员可配置的路由/计费表**，
而不是"每个 vendor 一个渠道"。以下方案按此设计。

> 边界：本方案只覆盖 `task_info` 形状的**异步任务**端点。MuleRouter 下的同步端点
> （`/vendors/openai/chat`、`/vendors/anthropic/*`）不在范围内——那些应该配成普通
> OpenAI / Claude 渠道或走 `advanced_custom`。

## 2. 现状：当前完全不支持

- **路由层无此路径。** 入口路由是硬编码枚举（[relay-router.go](../router/relay-router.go)、[video-router.go](../router/video-router.go)），只有 `/v1/*`、`/mj`、`/suno`、`/kling/v1`、`/jimeng`，没有 `/vendors/` 前缀。
- **异步任务适配器没有该平台。** `GetTaskAdaptor` 按渠道类型分发（[relay_adaptor.go:144-174](../relay/relay_adaptor.go#L144-L174)），仅覆盖 suno / ali / kling / jimeng / vertex / vidu / doubao / sora / gemini / minimax。
- **`advanced_custom` 救不了。** 它的 `incoming_path → upstream_path` 只覆盖 chat / responses / messages / rerank 这类同步端点，不处理"提交-轮询"生命周期、任务落库与按任务结算。
- 全仓库搜不到 `mulerouter` 任何引用。

骨架是对得上的：`TaskAdaptor` 接口（[adapter.go:35-83](../relay/channel/adapter.go#L35-L83)）就是为"提交 + 轮询"设计的，轮询调度器 `DispatchPlatformUpdate` 的 `default` 分支已经接管所有数字平台（[task_polling.go:178-194](../service/task_polling.go#L178-L194)），本方案不需要改动框架层逻辑。

## 3. 架构

```
POST /vendors/carrothub/v1/wan2.2-i2v-spicy/generation
GET  /vendors/carrothub/v1/wan2.2-i2v-spicy/generation/{task_id}
        │
        ├─ MuleRouterRequestConvert 中间件
        │    · 解析 vendor / model / action / task_id
        │    · 查渠道路由表 → 得到内部模型名 carrothub/wan2.2-i2v-spicy/generation
        │    · 按 billing_vars 校验计费变量边界（越界 400）
        │    · 改写 body 为统一 TaskSubmitReq、改写 path 供 Distribute 识别
        │
        ├─ TokenAuth → Distribute（按内部模型名选渠道）
        │
        └─ controller.RelayTask / RelayTaskFetch
                 └─ relay/channel/task/mulerouter.TaskAdaptor
                        └─ https://api.mulerouter.ai/vendors/{vendor}/v1/{model}/{action}
```

同时获得 **统一任务路由** `/v1/video/generations`（尽管名字带 video，它是通用异步任务入口，
三类模型都能走），视频模型还额外支持 OpenAI 风格的 `/v1/videos/:task_id`。

**这条路径才是面向终端用户的。** 配上渠道模型重定向（短名 → 三段式内部名）之后，
客户端只看到 `z-image-spicy` 这样的短名，路径和参数里都不出现上游厂商；原生 `/vendors/...`
路径因为渠道模型列表里没有三段式全名而自然不可达。终端用户文档见
[guides/user/image-video-generation.md](../guides/user/image-video-generation.md)。

### 3.1 内部模型名

内部模型名 = **`{vendor}/{model}/{action}`**，例如：

- `carrothub/z-image-spicy/generation`
- `carrothub/wan2.2-i2v-spicy/generation`
- `klingai/kling-v3/image-to-video`（未来）

必须带 `action`：`kling-v3` 的 `text-to-video` 与 `image-to-video` 价格不同，而 new-api
的按次计价是按模型名索引的，动作不进模型名就无法分别定价。本期三个模型的 action
都是 `generation`，但名字里仍要带上，避免后续接入多动作 vendor 时改名迁移。
名字长的代价由渠道的模型重定向（`ModelMappedHelper`，提交流程第 2.5 步）吸收——
管理员可以把 `z-image-spicy` 映射到全名，客户端在统一路由上仍可用短名。

## 4. 渠道配置：路由与计费表

新增渠道类型 **MuleRouter**，其 `ChannelOtherSettings` 增加一段配置（结构上对齐既有的
`AdvancedCustomConfig`，见 [channel_settings.go:118](../relaykit/dto/channel_settings.go#L118)）：

```jsonc
{
  "mulerouter": {
    "routes": [
      {
        "vendor": "carrothub",
        "model": "wan2.2-i2v-spicy",
        "action": "generation",              // 默认 generation
        "billing_vars": [
          { "name": "seconds",    "source": "duration",   "kind": "int",
            "enum": [5, 8], "default": "5", "divisor": 1 },
          { "name": "resolution", "source": "resolution", "kind": "enum",
            "values": { "480p": 1.0, "720p": 2.0 }, "default": "480p" }
        ]
      },
      {
        "vendor": "carrothub",
        "model": "z-image-spicy",
        "billing_vars": [
          { "name": "prompt_extend", "source": "prompt_extend", "kind": "enum",
            "values": { "true": 1.076923, "false": 1.0 }, "default": "true" },
          { "name": "width",  "source": "width",  "kind": "int",
            "min": 256, "max": 1536, "default": "1024" },   // 无 divisor = 只校验不计费
          { "name": "height", "source": "height", "kind": "int",
            "min": 256, "max": 1536, "default": "1536" }
        ]
      }
    ]
  }
}
```

`default` 一律写成字符串（`"5"` / `"480p"` / `"true"`）：enum 查表要把请求值字符串化，
统一成一种写法比按 kind 分两种类型更少踩坑。`default` 是**必填**的——省略字段时若没有
确定的取值，计费就不再是确定的。保存时会用它自己的边界校验 `default`，并拒绝
「产生倍率却没有 `max`/`enum` 上界」的声明。

`billing_vars` 是这个方案的核心，它一身兼两职：

1. **入参边界校验** —— `kind`/`min`/`max`/`enum`/`values` 定义合法域，越界一律 **400 拒绝，不静默 clamp**；
2. **倍率生成** —— 校验通过后按 `value / divisor`（int）或查 `values` 表（enum）产出倍率，
   以 `name` 为键交给 `PriceData.AddOtherRatio`。

这正是 AGENTS.md "计费安全不变量" 要求的：*每个变成计费乘数的用户可控量都必须在到达
额度计算之前被限界*。把限界写进配置而不是散在各 adaptor 里，是本方案敢做透传的前提。

### 4.1 三条安全护栏

透传的灵活性不能拿计费安全去换，所以：

1. **白名单制**：请求的 `{vendor}/{model}/{action}` 不在 `routes` 里 → 404 拒绝。
   未登记的模型既没有价格也没有乘数约束，放行等于开无底洞。
2. **危险键名黑名单**：未在 `billing_vars` 声明、但命中
   `n / count / num_images / duration / seconds / resolution / size / quality / batch_size`
   等已知成本键的字段 → 400 拒绝，提示管理员显式声明。
   防的是"上游加了个新的计费字段，管理员没跟进配置"导致的漏收费与乘数逃逸。
3. **其余字段原样透传**，但不参与计费。`prompt`、`negative_prompt`、`seed`、
   `image`、`last_image`、`safety_filter`、`prompt_extend` 等都属此类。

### 4.2 `prompt_extend`：加法项与乘法框架的不匹配

MuleRouter 对 `prompt_extend` 统一收 **$0.001/次**，是个**加法**项；而 new-api 的任务计费是
`按次基础价 × ∏倍率`，只能表达乘法。一个固定倍率无法在不同档位上同时精确——
`0.001` 占 5s/480p 视频（$0.10）的 1%，占 8s/720p（$0.32）的 0.3%。

按占比分类处理，不为此新增框架机制：

- **图片模型必须建模。** `z-image-spicy` 基础价 $0.013，$0.001 占 7.7%，不可忽略。
  它只有一个价位，倍率 `14/13 ≈ 1.076923` 是**精确**的，配成 enum 型 billing_var 即可。
- **视频模型直接忽略。** 占比 ≤1%，为 0.3% 的偏差引入一个在各档位上都不准的倍率不划算，
  这部分成本由渠道毛利吸收。

`qwen-image-edit-spicy` 无 `prompt_extend` 参数，不涉及。

## 5. 详细设计

### 5.1 渠道类型注册

[constant/channel.go](../constant/channel.go)：

- `ChannelTypeMuleRouter = 61`，插在 `ChannelTypeNewAPI = 60` 与 `ChannelTypeDummy` 之间。
- `ChannelBaseURLs` 是按下标索引的 slice，**必须同步追加** `"https://api.mulerouter.ai"` 到下标 61，否则 `ChannelBaseURLs[61]` 直接 panic。
  该 const 块没有 `iota`，`ChannelTypeDummy` 会复用上一行字面值，插入新常量后需确认它仍符合"计数哨兵"用途。
- `ChannelTypeNames[ChannelTypeMuleRouter] = "MuleRouter"`。

前端三处注册点（照 Vidu=52 抄）：
[constants.ts:75](../web/src/features/channels/constants.ts#L75)、
[channel-utils.ts:102](../web/src/features/channels/lib/channel-utils.ts#L102)、
[model-categories.ts:142](../web/src/features/channels/lib/model-categories.ts#L142)。
渠道编辑页需要一个路由表编辑器（可先做 JSON 文本框，参照 advanced_custom 的现有形态）。

### 5.2 路由

新建 `router/mulerouter-router.go`。动作段不定长（`generation` / `text-to-video` / `video-diffusion`），
用通配符接住再自行解析：

```go
g := router.Group("/vendors")
g.Use(middleware.RouteTag("relay"))
g.Use(middleware.MuleRouterRequestConvert(), middleware.TokenAuth(), middleware.Distribute())
{
    g.POST("/:vendor/v1/*rest", controller.RelayTask)
    g.GET("/:vendor/v1/*rest", controller.RelayTaskFetch)
    // DELETE 本期不注册，见 5.6
}
```

`*rest` 的分段数区分语义：2 段 = `{model}/{action}` 提交；3 段 = `{model}/{action}/{task_id}` 查询/取消。
模型名含 `.` 但不含 `/`，切分无歧义。

### 5.3 请求转换中间件

`middleware/mulerouter_adapter.go`，职责对照现成的 [kling_adapter.go](../middleware/kling_adapter.go)：

1. 解析 `vendor` / `model` / `action` / `task_id`，打上 `mulerouter_native_route` 标记
   （响应形态由它决定：原生路由回 MuleRouter 形状，统一路由回 OpenAI video 形状）；
2. 提交请求：改写 body 为统一形状
   `{"model": "<内部模型名>", "prompt": ..., "image": ..., "metadata": <原始 body>}`；
3. 查询请求：`c.Set("task_id", ...)` 即可，其余交给 distributor 分支。

**渠道未定时如何查路由表？** 中间件跑在 `Distribute` 之前，拿不到渠道配置。
所以中间件只做无状态的路径解析与 body 改写；路由表查找与 `billing_vars` 校验下沉到
`TaskAdaptor.ValidateRequestAndSetAction`——它在 `RelayTaskSubmit` 里跑在
`InitChannelMeta` 之后、`EstimateBilling`/预扣费之前（[relay_task.go:145-205](../relay/relay_task.go#L145-L205)），
既拿得到渠道配置，又还来得及在扣费前拒绝请求。中间件保持薄，安全校验全部在 adaptor 内完成。

**模型识别不靠改写路径。** `getModelRequest` 是按路径前缀分派的 if/else 链
（[distributor.go:253](../middleware/distributor.go#L253)），因此在其中新增一个 `/vendors/` 分支，
与既有的 `/suno/`、`/mj/` 分支同构：POST 从 body 读 model，GET 置
`shouldSelectChannel = false` 并设 `RelayModeMuleRouterFetchByID`。这比在中间件里把
`c.Request.URL.Path` 改写成 `/v1/video/generations` 更直白，也不会让后续读路径的代码看到假路径。

### 5.4 适配器：`relay/channel/task/mulerouter/`

以 [vidu/adaptor.go](../relay/channel/task/vidu/adaptor.go) 为模板：

| 文件 | 内容 |
|---|---|
| `route.go` | 路径解析（`ParsePath` / `IsVendorPath`）与原生路由上下文键，纯函数 |
| `dto.go` | 上游响应结构（提交/查询共用）、错误对象、状态常量 |
| `adaptor.go` | `TaskAdaptor` 接口实现 + 原生查询响应构造 |

路由表与 `billing_vars` 的求值放在 `relaykit/dto/mulerouter_settings.go`，与配置结构同处一地，
且能被 relaykit 独立测试。

要点：

- **`ValidateRequestAndSetAction`**：先按 `info.OriginModelName` 查路由表（未登记 → 404），
  再 `relaycommon.ValidateBasicTaskRequest`（含 prompt 非空与 `MaxTaskDurationSeconds` 边界），
  最后 `EvaluateBilling`。**校验对象是合并 metadata 之后的最终上游 body**，
  metadata 天然无法绕过边界检查。
- **路由按映射后的 `UpstreamModelName` 解析**：适配器自己先调一次 `ModelMappedHelper`，
  这样管理员可以用渠道的「模型重定向」把三段式内部名藏起来，只给客户端暴露短名
  （`z-image-spicy` → `carrothub/z-image-spicy/generation`）。模型重定向是管理员配置，
  与「用户可控量必须限界」无关；限界靠的是下面的 `billing_vars`。
- **`BuildRequestURL`**：`{baseURL}/vendors/{vendor}/v1/{model}/{action}`，
  取自路由表条目（已白名单化的值，不是用户原始输入）。
- **`BuildRequestBody`**：合并后的 body 原样序列化。不定义强类型请求结构——各 vendor 字段
  差异太大，透传才是这套抽象的意义；`seed: 0` / `prompt_extend: false` 因为走的是
  `map[string]any` 而非带 `omitempty` 的结构体，天然不会被丢弃（有回归测试守着）。
- **厂商字段顶层与 `metadata` 都接受**：`mergeUpstreamParams` 读的是**原始请求体**而不是
  解析后的 `TaskSubmitReq`——后者只认 new-api 的统一词汇表，`width` / `resolution` /
  `prompt_extend` 这些它没有的字段会在适配器看到之前就被丢掉，调用方会拿到一个用默认参数
  生成的结果却毫无提示。同名时以 `metadata` 为准。
- **`EstimateBilling`**：直接返回校验阶段算好的倍率表，不重新读原始 body——**倍率只能来自已校验值**。
- **`DoResponse`**：取 `task_info.id` 作为上游任务 ID，响应体原样作为 `taskData` 落库；
  按 `mulerouter_native_route` 决定回 MuleRouter 形状还是 OpenAI video 形状。
- **`FetchTask`**：`GET .../{model}/{action}/{task_id}`。`FetchTask(baseUrl, key, body, proxy)`
  签名里原本拿不到模型名，因此给轮询侧的 body map 增加了一个 `model` 键
  （取自 `task.Properties.UpstreamModelName`，`InitTask` 已经写好）；查询 URL 由模型名
  反解得到，无需路由表。
- **`ParseTaskResult`**：`pending → TaskStatusSubmitted`、`processing → TaskStatusInProgress`、
  `completed → TaskStatusSuccess`、`failed → TaskStatusFailure`（`Reason` = `error.title` + `error.detail`）；
  产物 URL 按 `images` → `videos` → `audios` 取第一个非空数组的首元素。未知状态返回 error。
- **`AdjustBillingOnSubmit` / `AdjustBillingOnComplete`**：上游不回传实际用量，直接嵌
  `taskcommon.BaseBilling` 用 no-op，预扣即终值（理由见第 6 节）。
- **`ConvertToOpenAIVideo`**：产物是 `videos` 时可用，让 `/v1/videos/:task_id` 生效；否则返回明确错误。

`GetTaskAdaptor` 增加 `case constant.ChannelTypeMuleRouter`。轮询调度侧零改动。

### 5.5 查询响应

新增 `RelayModeMuleRouterFetchByID` 与 `fetchRespBuilders` 条目（[relay_task.go:290](../relay/relay_task.go#L290)），
输出上游原生形状：

- `task_info.id` **必须替换为 new-api 的公开 task ID**，不能泄露上游 UUID；
- `status` 由 `model.TaskStatus` 反向映射回 `pending/processing/completed/failed`；
- `created_at` / `updated_at` 由任务时间戳格式化为 ISO8601；
- 产物数组从 `task.Data`（存的上游原始响应）取，**键名原样保留**——因为响应是在存下来的
  上游 body 上就地改写的，vendor 用 `images` 就还是 `images`，用 `videos` 就还是 `videos`，
  不需要配置声明产物字段；
- 任务未到 `completed` 时清空产物数组，避免上一轮轮询的陈旧结果被当成当前结果。

### 5.6 `delete-task`

alibaba 等 vendor 提供 `DELETE .../{task_id}`，但**本期三个 carrothub spicy 模型没有这个端点**，
因此不实现，路由里也不注册 `DELETE`。

留作后续：实现时应为**纯转发 + 本地置终态**且**不退款**（任务已提交、上游可能已产生成本），
并在文档与前端提示里写明，否则会被当成"取消即退款"。若要退款需额外设计条件（仅 pending 可退）。

## 6. 任务生命周期与计费时序（既有框架行为，非本方案新增）

这一节解释**为什么 5.4 里的计费方法可以是 no-op**：异步任务的轮询与结算全部由框架承担，
适配器只需实现"怎么问上游"和"上游的回答是什么意思"。

### 6.1 轮询由服务端定时发起，与客户端无关

调度在 [system_task_handlers.go:138-153](../controller/system_task_handlers.go#L138-L153)：
`async_task_poll` 系统任务，**间隔 15 秒**。`Enabled()` 同时要求 `constant.UpdateTask` 开启
且 `model.HasUnfinishedSyncTasks()` 为真——**没有未完成任务就不排期**，空闲时零开销，
不存在"无脑不停轮询"。

每轮 [`RunTaskPollingOnce`](../service/task_polling.go#L108)：

1. `sweepTimedOutTasks` 先清超时任务（`TASK_TIMEOUT_MINUTES`，默认 1440 = 24 小时），
   置 FAILURE 并退款，每轮最多 100 条，剩余下轮继续；
2. 取未完成任务（`TASK_QUERY_LIMIT`，默认 1000），按 platform → channel 分组；
3. **每个渠道一个 goroutine 并发，渠道内串行且任务间 sleep 1 秒**
   （[task_polling.go:418-435](../service/task_polling.go#L418-L435)），避免打爆单个上游；
   渠道设置 `DisableTaskPollingSleep` 可关闭该间隔。

分布式安全性已由框架保证：系统任务持 lease + runnerID，任务终态更新走 CAS
（`UpdateWithStatus`），CAS 竞争失败即跳过计费，防重复退款/重复结算。

### 6.2 计费时序：提交时预扣，终态时结算

| 时点 | 动作 | 位置 |
|---|---|---|
| 提交 | 算基础价 → `EstimateBilling` 倍率 → `PreConsumeBilling` **预扣** → 发上游 → `AdjustBillingOnSubmit` 调差额 | [relay_task.go:180-250](../relay/relay_task.go#L180-L250) |
| 成功 | `PerCallBilling` 直接跳过；否则 `AdjustBillingOnComplete` 补/退，再回退到 token 重算，都没有则保持预扣额 | [`settleTaskBillingOnComplete`](../service/task_polling.go#L643) |
| 失败 | `RefundTaskQuota` 全额退，退完 `task.Quota = 0` 落库防重复退 | [task_billing.go:166-204](../service/task_billing.go#L166-L204) |
| 超时 | 同失败路径退款（`TaskRefundLegacyCutoff` 之前的旧任务明确不退） | `sweepTimedOutTasks` |

### 6.3 客户端 fetch 只读本地任务表

`RelayTaskFetch` 走 `model.GetByTaskId` 读库，**不打上游、不参与计费**。
唯一例外是 Gemini/Vertex 的 `tryRealtimeFetch`（[relay_task.go:430](../relay/relay_task.go#L430)），
用户查询时实时拉上游状态——但那也只是读，计费仍归轮询。

**结论：用户不来取结果，任务照样会被轮到终态、照样结算或退款。**

### 6.4 本方案因此获得的白拿行为

三个模型都是按次计费、预扣即终值，所以 `AdjustBillingOnSubmit` / `AdjustBillingOnComplete`
直接嵌 `taskcommon.BaseBilling` 用 no-op 即可，随之自动获得：

- 成功不重算（预扣就是终值）；
- **失败自动全额退款**；
- **24 小时超时自动退款**（上游卡死不会永久占用用户额度）；
- 轮询频率、并发、限流间隔、CAS 防重全部继承，适配器里一行计费循环代码都不用写。

需要注意的两处：**上游 429 会被识别为限流而非失败**（[task_polling.go:500-520](../service/task_polling.go#L500-L520)），
保持原状态等下一轮，不会误退款；而 `ParseTaskResult` 返回未知状态会让该任务**每轮报错但不推进**，
所以 5.4 里对未知 status 返回 error 的做法要配合监控，避免任务悄悄卡到 24 小时超时才失败。

## 7. 影响面评估：会不会影响其他模型的计费

结论：**不影响任何现有模型的计费金额**，但有一条真实的间接路径需要在评审时确认容量水位，
另有两条非计费副作用。

### 7.1 计费金额不受影响

方案不修改任何共享计费代码。`EstimateBilling` / `PriceData.AddOtherRatio` /
`common.QuotaFromFloatChecked` / `PreConsumeBilling` 全部按现状调用，新增的改动都是 additive 的：
一个 `GetTaskAdaptor` case、一个 `fetchRespBuilders` 条目、一个渠道配置字段。
`middleware/distributor.go` 的 `getModelRequest` 也不需要改（靠中间件重写 path 复用
`/v1/video/generations` 分支），所以渠道分流逻辑零改动。

价格查找同样安全：`GetModelRatio` / `GetModelPrice` 是**精确查表**
（[model_ratio.go:396-410](../setting/ratio_setting/model_ratio.go#L396-L410)），
`FormatMatchingModelName` 的特判只针对 `gemini-2.5-*` / `gpt-4*-gizmo` 前缀，
唯一通配是 `:compact` 后缀。`carrothub/z-image-spicy/generation` 这种带 `/` 的名字
（openrouter 风格）撞不上任何既有规则，价格表只增不改。

### 7.2 唯一的真实风险：轮询资源全局共享 ⚠️

这是唯一能间接影响其他模型计费的路径：

- `GetAllUnFinishSyncTasks(TASK_QUERY_LIMIT)` 默认只取 **1000 条**，按 `id` 升序，
  **所有平台共用这一个池**（[task.go:311-320](../model/task.go#L311-L320)）；
- 外层 `for platform, tasks := range platformTask` 是**串行**的，只有渠道内并发；
  渠道内每个任务之间还 sleep 1 秒。

MuleRouter 任务量一大会同时造成：(a) 占满 1000 的取数配额，把其他平台的任务挤出本轮；
(b) 拉长单轮耗时。极端情况下其他平台的任务长时间轮不到 → 撞上 `TASK_TIMEOUT_MINUTES`
（默认 24h）→ **被判失败并退款，即使上游其实已经成功**。用户白拿结果，平台白退钱。

这是**既有隐患**，本方案不引入它，但会让它更容易被触发。缓解措施：

- 主要耗时来自渠道内的 1 秒间隔（N 个任务 ≈ N 秒），量上来后开渠道设置 `DisableTaskPollingSleep`；
- 监控未完成任务总数是否逼近 1000，必要时调大 `TASK_QUERY_LIMIT`；
- 上线前确认预期任务并发量与现有其他平台任务量之和的水位。

### 7.3 两条非计费副作用

1. **`ChannelTypeDummy` 会从 60 变成 61**——那个 const 块没有 `iota`，`ChannelTypeDummy`
   复用上一行字面值。它被 [controller/model.go:97](../controller/model.go#L97) 用作循环上界
   构建 `channelId2Models`。只影响 `/v1/models` 的展示，但需确认
   `ChannelType2APIType(61)` 返回 `false` 从而被跳过，否则会用默认 adaptor 的模型列表污染列表。
2. **`/vendors` 路由组不与现有路由冲突**——根级已有 `/:mode/mj` 参数路由与 `/v1`、`/mj`、
   `/suno` 静态段共存，说明 gin 容忍静态段与参数段同级，新增 `/vendors` 安全。

## 8. 实施清单（已完成）

| # | 改动 | 文件 |
|---|---|---|
| 1 | 渠道类型常量 + base URL + 名称 | `constant/channel.go` |
| 2 | 渠道配置结构 + 计费变量求值 | `relaykit/dto/mulerouter_settings.go`、`channel_settings.go` |
| 3 | 保存时校验接入 | `model/channel.go` |
| 4 | 适配器实现 | `relay/channel/task/mulerouter/{route,dto,adaptor}.go` |
| 5 | 适配器注册 | `relay/relay_adaptor.go` |
| 6 | 请求转换中间件 | `middleware/mulerouter_adapter.go` |
| 7 | 模型识别分支 | `middleware/distributor.go` |
| 8 | 路由 | `router/mulerouter-router.go` + `router/main.go` 挂载 |
| 9 | 查询响应构造器 + relayMode | `relay/relay_task.go`、`relay/constant/relay_mode.go` |
| 10 | 轮询侧传递模型名 | `service/task_polling.go` |
| 11 | 前端渠道注册 + 路由表编辑器 | `web/src/features/channels/` |
| 12 | 前端文案 i18n（en 为源，7 语言均已翻译） | `web/src/i18n/locales/*.json` |
| 13 | 单测 | `relaykit/dto/mulerouter_settings_test.go`、`relay/channel/task/mulerouter/adaptor_test.go`、`router/mulerouter_router_test.go` |

`relaykit/` 有改动，已用 `cd relaykit && GOWORK=off go build ./... && go test ./...` 单独验证（AGENTS.md 硬性要求）。

测试覆盖（按 AGENTS.md "后端测试质量"，只测真实契约）：

- **`billing_vars` 表驱动测试**：合法组合的倍率精确值、越界/非枚举/非整数/超大数（含
  `18446744073686646784` 这类包装过的负数）一律拒绝、缺省值生效；
- **白名单与危险键名黑名单**：未登记模型不解析、未声明的 `n` 被拒、非成本字段照常透传；
- **保存时校验**：重复模型名、模型名含 `/`、产生倍率却无上界、`default` 越界、enum 倍率非正；
- `ParseTaskResult` 对四种 status + `error` 对象 + `images`/`videos` 两种产物的映射，未知状态报错；
- 路径解析：2 段 / 3 段 / 多词 action / 含 `.` 的模型名 / 畸形路径；
- 查询响应：上游 task id 被替换、状态反向映射、未完成时不暴露陈旧产物、失败时合成 error；
- 请求体合并：`seed=0`、`prompt_extend=false` 不丢失，控制字段不外泄，vendor 字段优先；
- 路由注册：`/vendors/:vendor/v1/*rest` 与既有 `/:mode/mj` 参数路由共存不 panic。

不写覆盖率型测试、不写随机压力测试。

## 9. 首批落地路由与定价（配置数据，非代码）

上游价目（取自各端点文档页，2026-08-10）：

| 模型 | 上游价格 |
|---|---|
| `qwen-image-edit-spicy` | $0.04 / image |
| `z-image-spicy` | $0.013 / image；prompt rewriting $0.001 / request |
| `wan2.2-i2v-spicy` | 480p $0.02 / second；720p $0.04 / second；prompt rewriting $0.001 / request |

`model_price` 的单位就是美元（`quota = model_price × QuotaPerUnit × groupRatio`，
[price.go:216-221](../relay/helper/price.go#L216-L221)），下表数字可直接录入，实际售价再叠加加价倍率：

| 内部模型名 | 基准档 | `model_price` | `billing_vars` |
|---|---|---|---|
| `carrothub/qwen-image-edit-spicy/generation` | 单张 | `0.04` | 无 |
| `carrothub/z-image-spicy/generation` | 单张，不含改写 | `0.013` | `prompt_extend`: enum `{true: 1.076923, false: 1.0}`（默认 true）；`width`/`height`: int 256–1536，不设 `divisor`（只校验不计费） |
| `carrothub/wan2.2-i2v-spicy/generation` | **1 秒** / 480p | `0.02` | `seconds`: int enum `[5,8]`，`divisor: 1`；`resolution`: enum `{480p: 1.0, 720p: 2.0}` |

视频模型的基准档是**一秒**而不是一段 5 秒的片子，因为仓库里所有按时长计费的 task adaptor
（ali / gemini / sora / vertex）都把 `seconds` 倍率取成实际秒数，基础价按秒。用「每 5 秒一档」
的写法（`divisor: 5`、基础价 $0.10）算出来的钱一模一样，但管理端日志会显示
`计算参数：seconds: 1.00`，读起来像是 1 秒的视频。倍率的名字会出现在账单上，得说人话。

**本期范围就是这三个模型**，其余 vendor（alibaba / klingai / minimax / google 等）不实现、不配置。
它们的存在只用于论证第 3–5 节的抽象必须是配置驱动的——接它们时应当只加配置行，
若届时发现仍需改代码，说明本方案的抽象没做对。

验算 `wan2.2-i2v-spicy`：8s/720p = `0.02 × 8 × 2.0 = $0.32`，与上游 `$0.04/s × 8s` 一致；
5s/480p = `0.02 × 5 × 1.0 = $0.10` = `$0.02 × 5`。倍率模型对这个模型是**精确**的，
因为上游本身就是「单价 × 秒数」的线性结构。

`z-image-spicy` 的 `width`/`height` 不影响价格（各分辨率统一 $0.013），
但仍要在 `billing_vars` 里声明边界——否则会被 4.1 的危险键名黑名单（含 `size`/`width`/`height`）拦截，
且缺少上界意味着未来上游改成按面积计价时会留下一个无界乘数入口。

## 10. 待确认事项

1. ~~定价数据缺口~~ **已解决**：三个模型的价目已从文档页取到并填入第 9 节，本期无缺口。后续接新模型时须先确认其文档页是否列价。
2. **上游任务保留期与建议轮询间隔**：文档未说明，影响产物是否需要转存/代理。
3. **轮询容量水位**（评审重点，见 7.2）：需要确认预期任务并发量 + 现有其他平台任务量是否会逼近 `TASK_QUERY_LIMIT`（默认 1000）。逼近则其他平台的任务可能长时间轮不到、撞 24h 超时被误判失败并退款。上线前应给出水位估算与是否开启 `DisableTaskPollingSleep` 的结论。
4. ~~联调验证~~ **已完成**（2026-08-10，本地实例）：三个模型端到端跑通，扣费精确命中预期
   （6999 / 20000 / 50000），额度对账一致，上游 202 与查询响应形状与文档一致。
   回归工具见 `.claude/skills/mulerouter-smoke-test/`。仍未覆盖：失败态的 `error` 对象字段名
   （没有构造出上游失败的场景）、`wan-720p-8s` 档位、24 小时超时退款路径。
5. **是否泛化到非 MuleRouter 上游**：本方案把渠道类型命名为 MuleRouter、协议按 MuleRouter 的 `task_info` 形状固化。若将来其他聚合商采用相同形状，再考虑改名为通用的"异步任务自定义渠道"，路由表结构可直接复用。

## 11. 实现相对初版设计的取舍变更

初版设计里有几处经不起实现推敲，落地时改掉了，理由记录在此：

1. **删掉 `incoming_prefix`。** 入站路由是启动时静态注册进 gin 的，渠道级配置改不了它；
   而上游路径前缀由 MuleRouter 自己的布局固定为 `/vendors`。一个既不能生效、又容易被误解为
   "能改上游路径" 的配置项，不如没有。
2. **删掉 `result_keys`。** 查询响应是在存下来的上游 body 上就地改写的，vendor 自己的产物字段名
   天然被保留；`ParseTaskResult` 取 URL 时按 `images` → `videos` → `audios` 顺序找即可。
   配置里留着它就是死配置。
3. **不改写请求路径。** 初版打算把 `/vendors/...` 改写成 `/v1/video/generations` 来骗过
   `getModelRequest`。改成在 `getModelRequest` 里加一个 `/vendors/` 分支（与 `/suno/`、`/mj/` 同构），
   避免后续任何读取 `c.Request.URL.Path` 的代码看到一个假路径。
4. **路由按映射后的名字解析（一度改错又改回）。** 中途曾按 `OriginModelName` 查路由并拒绝
   一切模型映射，理由是"按 A 计价、跑 B 模型"。这个理由站不住：模型重定向是**管理员配置**，
   计费安全不变量管的是用户可控输入；而且 new-api 每个渠道都是按客户端名字计价的，
   MuleRouter 没道理特殊。放开之后管理员才能把三段式内部名藏起来只暴露短名——这既是易用性
   需求，也是不让终端用户看到上游厂商的前提。
5. **模型名不再塞进 `task.Properties`。** `InitTask` 本来就把 `UpstreamModelName` 写进了
   `Properties`；缺的只是轮询侧没把它传给 `FetchTask`。于是给轮询的 body map 加了一个 `model` 键
   （`service/task_polling.go`），这是本方案对共享代码的唯一改动，且对其他适配器无副作用。
6. **`billing_vars.default` 统一为字符串。** enum 查表需要把请求值字符串化，int 也用字符串写
   默认值可以只用一种类型表达；并强制 `default` 必填——省略字段时若没有确定取值，计费就不确定。
7. **保存时拒绝"有倍率但无上界"的声明。** 初版只在请求时校验边界；现在配置阶段就挡住
   `divisor > 0` 却既没有 `max` 也没有 `enum` 的变量，把无界乘数扼杀在配置里。
