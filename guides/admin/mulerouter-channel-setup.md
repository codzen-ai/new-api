# MuleRouter 渠道配置说明

面向管理员。这份文档说明如何配置 MuleRouter 渠道，让终端用户能用简短的模型名调用图片与视频生成模型，并且账单可核对、参数越界会被拒。

终端用户看的文档是 [image-video-generation.md](../user/image-video-generation.md)，那份刻意不提上游厂商。这份是内部文档，会直说。

## 这个渠道是什么

MuleRouter 是一家聚合平台，把多家厂商的生成模型收在 `/vendors/{厂商}/v1/{模型}/{动作}` 下，协议形状统一：提交返回任务 ID，轮询取结果。

本站通过内置的任务插件 `mulerouter`（`plugins/tasks/mulerouter/plugin.js`）对接这套协议。MuleRouter 渠道就是一个绑定了 `mulerouter` 插件的**任务插件渠道**（渠道类型 61）。客户端在 `POST /v1/tasks/mulerouter` 提交任务（视频也可以走 `POST /v1/videos`），用 `GET /v1/tasks/{id}` 和它的 `/artifacts` 取结果。

关键设计：**哪些 MuleRouter endpoint 可用、每个 endpoint 按哪些参数计费，写在插件代码里，不是渠道配置**。接一个新模型要改 `plugin.js` 并重新部署，见[新增一个模型](#新增一个模型)。为什么这样做见 [ADR-0001](../../docs/adr/0001-mulerouter-endpoints-live-in-a-task-plugin.md)。

本文以已接入的三个 CarrotHub 模型为例：

| 上游模型 | 能力 | 上游价格 |
|---|---|---|
| `z-image-spicy` | 文生图 | $0.013 / 张（提示词改写另收 $0.001/次） |
| `qwen-image-edit-spicy` | 图生图 | $0.04 / 张 |
| `wan2.2-i2v-spicy` | 图生视频 | 480p $0.02/秒，720p $0.04/秒 |

任务日志和统计按产物分类：`z-image-spicy` 的任务记为 `text_to_image`，`qwen-image-edit-spicy` 记为 `image_to_image`，`wan2.2-i2v-spicy` 记为 `image_to_video`，从哪个接口提交都一样。

## 两个名字，别搞混

这是配置里最容易出错的地方。

| 名字 | 样子 | 出现在哪 |
|---|---|---|
| **MuleRouter model name** | `carrothub/z-image-spicy/generation` | 插件 `ENDPOINTS` 表的键，`{厂商}/{模型}/{动作}` 三段，插件用它拼上游 URL |
| **Public model name** | `z-image-spicy` | 客户端传的名字，也是**计价用的名字** |

MuleRouter model name 必须带动作段，因为有些厂商同一个模型的不同动作价格不同（例如 `kling-v3` 的 `text-to-video` 和 `image-to-video`），要分别成为不同的 endpoint。

Public model name 通过**渠道的「模型重定向」**（`model_mapping`）映射到 MuleRouter model name。这一步不只是为了好写，更是为了**不让终端用户看到上游是谁**——不配重定向的话客户就得在请求里写 `carrothub/...`。

Public model name 可以取任意名字。本文示例取和上游同名的短名（`z-image-spicy`），如果连模型名本身都不想暴露，取 `image-gen-pro` 之类也完全可以，只要渠道模型、模型重定向、价格三处保持一致。

## 配置步骤

### 1. 新建渠道

渠道 → 新建，填：

| 字段 | 值 |
|---|---|
| 类型 | `Task Plugin`（任务插件，类型 61） |
| 任务插件 | `mulerouter` |
| 名称 | 自定，例如 `MuleRouter CarrotHub` |
| Base URL | 选插件后表单会自动填入插件默认的 `https://api.mulerouter.ai`，**保持这个值**。插件请求只发往渠道上保存的这个地址 |
| 密钥 | MuleRouter 控制台的 API Key |
| 分组 | 按你的分组策略填 |
| 测试模型 | **留空**——渠道测试发的是 chat completion，异步任务渠道必然失败 |

**模型**填 Public model name（逗号分隔）：

```
z-image-spicy,qwen-image-edit-spicy,wan2.2-i2v-spicy
```

> ⚠️ 这里**只填 Public model name，不要填三段式的 MuleRouter model name**。填了的话，`/v1/models` 会把 `carrothub/...` 列给客户看。

**模型重定向**填 Public model name → MuleRouter model name：

```json
{
  "z-image-spicy": "carrothub/z-image-spicy/generation",
  "qwen-image-edit-spicy": "carrothub/qwen-image-edit-spicy/generation",
  "wan2.2-i2v-spicy": "carrothub/wan2.2-i2v-spicy/generation"
}
```

重定向的值必须是插件 `ENDPOINTS` 表里已有的键，一字不差。计费参数（边界和倍率）已经写在插件里，渠道上没有别的要填。

### 2. 配价格

系统设置 → 模型 → **模型固定价格**，加三条：

```json
{
  "z-image-spicy": 0.013,
  "qwen-image-edit-spicy": 0.04,
  "wan2.2-i2v-spicy": 0.02
}
```

三件事必须注意：

- **键是 Public model name**，不是三段式的 MuleRouter model name。计价用的是客户端传的名字。
- **必须配「模型固定价格」，不能配「模型倍率」**。异步任务按次计费，配成倍率会走 token 结算路径，而这些模型上游不回传 token 用量，结果是扣一个和实际无关的数。
- **视频的价格是「每秒」不是「每次」**。`0.02` 是 480p 一秒的价，乘上 `seconds` 和 `resolution` 倍率才是整段视频的价。

单位是美元，实际售价再叠加分组倍率。上面填的是上游成本价，要加价就往上调。

## 计费是怎么算的

一次调用的扣费 = `模型固定价格 × 各计费参数的倍率 × 分组倍率`。哪些请求参数参与计费、取值边界和倍率，由插件 `ENDPOINTS` 表里每个 endpoint 的 `fields` 声明，管理员不需要也不能在后台改。要点：

- **边界与倍率是一件事。** 一个参数只有先被限界，才允许影响价格。越界的请求在扣费之前直接 400 拒绝，否则客户传 `duration: 99999` 就能让扣费溢出。
- **按时长计费的模型，`seconds` 倍率取实际秒数，基础价按秒。** 所以视频模型的固定价格填每秒价。倍率名会出现在账单日志里（`计算参数：seconds: 8.00`）。
- **客户没传某个计费参数时按插件里的默认值计费**，例如 `wan2.2-i2v-spicy` 省略 `duration` 按 5 秒计。
- **成本字段黑名单。** 以下字段一旦出现在 `/v1/tasks/mulerouter` 的请求里（顶层或 `metadata` 里），而该 endpoint 没声明它，请求会被 **400 拒绝**：

  ```
  n            count       num_images   num_outputs   batch_size
  duration     seconds     resolution   size          quality
  width        height      fps          frames        num_frames
  steps        sample_count
  ```

  这是防呆：上游哪天加了个新的计费维度而插件没跟进，客户传了它就会**按旧价格生成更贵的东西**——少收的钱谁也发现不了。宁可拒绝请求，也不静默漏收。所以 `z-image-spicy` 的 `width`/`height` 即使不计费，插件里也声明了 256–1536 的边界。
- **`/v1/videos` 更严格。** 它只接受 endpoint 在 `fields` 和 `params` 里列出的字段（外加 OpenAI 写法 `seconds`、`size`、`input_reference`），其余字段一律 400 拒绝，不管在不在黑名单里。`seconds` 换算成 `duration`，`size` 按短边换算成 `resolution`（`1280x720` → `720p`），按换算后的值计费。

## 验证配置

改完配置跑一次冒烟测试，它会先做**不花钱**的检查（配置项、护栏、余额未变动），确认无误后再花约 $0.15 实跑三个模型：

```bash
export NEW_API_KEY=sk-中转令牌
export NEW_API_ACCESS_TOKEN=令牌主人的访问令牌

# 只做免费检查
python3 .claude/skills/mulerouter-smoke-test/scripts/mulerouter_smoke_test.py \
  --dry-run --base-url http://localhost:3000

# 确认预算后实跑
python3 .claude/skills/mulerouter-smoke-test/scripts/mulerouter_smoke_test.py \
  --yes --base-url http://localhost:3000
```

它会核对扣费是否精确等于 `固定价 × 倍率 × 分组倍率`、产物类型是否正确、MuleRouter model name 有没有泄漏给客户端。

手工验一下也行：

```bash
# 应当返回 400，且报错里提到 duration
curl -X POST "$BASE_URL/v1/tasks/mulerouter" \
  -H "Authorization: Bearer $API_KEY" -H "Content-Type: application/json" \
  -d '{"model":"wan2.2-i2v-spicy","prompt":"x","image":"https://e.com/a.png","duration":9}'

# 应当只列出 Public model name，不含 carrothub
curl "$BASE_URL/v1/models" -H "Authorization: Bearer $API_KEY"
```

注意用 `/v1/tasks/mulerouter` 或 `/v1/videos` 测：`/v1/video/generations` 到不了任务插件渠道。

## 新增一个模型

MuleRouter endpoint 写在插件代码里，新增要改代码、发版：

1. **查上游价格和参数**。到 MuleRouter 文档的对应端点页，记下价格、参数的取值范围、以及哪些参数影响价格。
2. **改插件**。在 `plugins/tasks/mulerouter/plugin.js` 的 `ENDPOINTS` 表加一个 endpoint，键是 MuleRouter model name；写明它的产物（`produces`：`image` 或 `video`）和任务 action（如 `text_to_image`），把影响价格的参数写进它的 `fields`，命中成本字段黑名单的参数即使不计费也要声明边界；视频 endpoint 还要在 `params` 里列出上游接受的其他字段，`/v1/videos` 只放行这些。在 `plugins/mulerouter_test.go` 补上对应的测试，然后重新部署。
3. **加 Public model name**。渠道的「模型」字段加 Public model name，「模型重定向」加一条映射到新的 MuleRouter model name。
4. **配价格**。模型固定价格加一条，键是 Public model name。按时长计费的模型基础价填**每秒**价。

最后跑一次冒烟测试。

## 排障

**提交返回 503「无可用渠道」**
渠道的「模型」字段里没有客户传的那个名字。检查 Public model name 是否拼对、渠道是否启用、分组是否匹配。也确认客户用的是 `/v1/tasks/mulerouter` 或 `/v1/videos`，`/v1/video/generations` 到不了这个渠道。

**提交返回 `this model is not available on this channel`**
名字在渠道模型列表里，但重定向后的名字不是插件 `ENDPOINTS` 表里的键。检查「模型重定向」的值是否和 `ENDPOINTS` 的键一字不差；这个模型确实还没接入的话，按[新增一个模型](#新增一个模型)改插件。

**提交返回 400，提示某参数「affect the price of this model and are not enabled」**
客户传了黑名单里的字段而该 endpoint 没声明它。要么在 `plugin.js` 里给这个 endpoint 的 `fields` 加上（带边界）并发版，要么让客户别传。

**`/v1/videos` 返回 400，提示某参数「are not supported by this model」**
这个字段不在该 endpoint 的 `fields` 和 `params` 里，`/v1/videos` 不会把它透传给上游（例如 `mode`）。让客户去掉它；如果上游确实接受这个字段，在 `plugin.js` 里把它加进 `params` 并发版。

**`/v1/videos` 返回 400，提示「give different values」**
客户用两种写法给了同一个参数的不同值，例如 `seconds: "8"` 和 `duration: 5`，或顶层 `resolution` 和 `metadata.resolution` 不一致。让客户只留一种写法，或让两处的值一致。

**提交返回 400 `model_price_error` 或「模型倍率未配置」**
没配模型固定价格。注意键要用 Public model name。

**扣费金额是预期的 5 倍或 1/5**
视频模型的固定价格填成了每 5 秒价（或别的档位价）。插件里 `seconds` 倍率取实际秒数，固定价格应当填**每秒价**。

**任务一直停在排队状态，或几分钟后失败为「Failed to query the upstream task status」**
插件认 MuleRouter 文档里的两套状态词：`pending`/`queued`（排队中）、`processing`/`running`/`retrying`（进行中）、`completed`/`succeeded`（成功）、`failed`（失败）。上游返回别的值时，这次轮询记为失败，后端日志有一条带 `unknown task status` 和原始响应的 WARN；连续失败达到 `TASK_POLL_MAX_FAILURES` 次（默认 20 次，约 5 分钟）后，网关把任务判失败并退款。看到这条日志就把原始响应交给开发，在 `plugin.js` 的 `TASK_STATUSES` 里补映射。没有这条日志就是上游在排队。任务超过 24 小时会自动判失败并退款。

**任务失败，`fail_reason` 是什么意思**
插件依次读上游响应里 `task_info.error` 和顶层的 `error`，把标题、详情和错误码拼成失败原因，形如 `External service execution failed: Kling task failed: video generation failed due to prompt violation (error code 3005)`。两处都没有错误对象时才是通用的 `task failed`。提交时上游就同步返回失败的，按同样规则给出原因。

**任务失败，原因是「the task completed without producing any image, video or audio」**
上游报告完成，却没有返回任何产物。插件把这种任务判为失败，网关自动退款给用户。偶发的话是上游问题；频繁出现就去 MuleRouter 后台查对应任务。

**客户说参数传了没生效**
Vendor parameter 写在顶层或 `metadata` 里都生效。`/v1/tasks/mulerouter` 同名时以 `metadata` 为准；`/v1/videos` 两处的值必须一致，不一致直接 400。参数没传时按插件里的默认值计费和生成。如果客户在 `/v1/tasks/mulerouter` 上写了 OpenAI 写法的 `seconds` 或 `size`，会被成本字段黑名单拒绝——这两个写法只在 `/v1/videos` 上有效，`/v1/tasks/mulerouter` 要写 `duration` 和 `resolution`。

## 已知限制

**部分响应仍暴露上游。** 图片和视频都可以通过网关下载：`/v1/tasks/{id}/artifacts` 返回的 `content_url` 指向本站，视频另有 `/v1/videos/{id}/content`，客户拿不到上游链接也能取到产物。但 `GET /v1/videos/{id}` 的 `metadata.url` 仍是上游结果 URL（形如 `https://mule-router-assets.muleusercontent.com/.../carrothub/...`），任务查询响应里的 `properties.upstream_model_name` 和 `data` 字段同样带内部信息。

**轮询容量共享。** 所有异步任务平台共用一个轮询池（默认单轮最多取 1000 条未完成任务），任务量大时可能挤占其他平台的轮询。量上来后可以在渠道设置里开启「跳过任务轮询间隔」，并关注未完成任务总数。

**上游失败态未实测。** 状态词和失败响应里 `error` 对象的两种位置、字段名都来自 MuleRouter 文档（carrothub 端点 YAML 和通用异步任务文档），还没在真实失败场景下验证过。如果遇到任务失败但 `fail_reason` 只有 `task failed` 或乱码，把原始响应记下来反馈给开发。
