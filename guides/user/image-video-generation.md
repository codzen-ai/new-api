# 图片与视频生成模型调用指南

面向终端用户。本文说明如何调用三个生成模型：文生图、图生图、图生视频。

三个模型都是**异步任务**：提交请求拿到任务 ID，再轮询任务状态，完成后取产物。没有同步返回图片或视频的接口——生成需要几十秒到几分钟，不适合让请求一直挂着。

## 目录

- [模型一览](#模型一览)
- [开始之前](#开始之前)
- [调用流程](#调用流程)
- [模型参数](#模型参数)
  - [参数放在哪里](#参数放在哪里)
  - [z-image-spicy 文生图](#z-image-spicy文生图)
  - [qwen-image-edit-spicy 图生图](#qwen-image-edit-spicy图生图)
  - [wan2.2-i2v-spicy 图生视频](#wan22-i2v-spicy图生视频)
- [查询任务](#查询任务)
- [获取产物](#获取产物)
- [视频的 OpenAI 兼容接口](#视频的-openai-兼容接口)
- [计费](#计费)
- [错误处理](#错误处理)
- [完整示例](#完整示例)
- [注意事项](#注意事项)

## 模型一览

| 模型 | 能力 | 必需输入 | 产物 | 价格 |
|---|---|---|---|---|
| `z-image-spicy` | 文字生成图片 | 提示词 | 1 张图 | $0.013 / 张 |
| `qwen-image-edit-spicy` | 按提示词编辑图片 | 图片 + 提示词 | 1 张图 | $0.04 / 张 |
| `wan2.2-i2v-spicy` | 由静态图生成视频 | 图片 + 提示词 | 1 段视频 | $0.02 / 秒（480p）<br>$0.04 / 秒（720p） |

以上为基准价，实际扣费按你所在分组的倍率浮动，详见[计费](#计费)。

## 开始之前

你需要两样东西：

| | 说明 |
|---|---|
| **接口地址** | 本站地址，例如 `https://your-site.com`。下文写作 `$BASE_URL` |
| **API 密钥** | 后台「令牌」页面创建，形如 `sk-xxxxxxxx`。下文写作 `$API_KEY` |

先设好环境变量，后面的示例可以直接复制运行：

```bash
export BASE_URL="https://your-site.com"
export API_KEY="sk-你的令牌"
```

提交和查询请求都要带这两个头：

```
Authorization: Bearer sk-xxxxxxxx
Content-Type: application/json
```

## 调用流程

三个模型用同一套接口，只是 `model` 和参数不同。

```
提交      POST  $BASE_URL/v1/tasks/mulerouter
查询      GET   $BASE_URL/v1/tasks/{任务ID}
取产物    GET   $BASE_URL/v1/tasks/{任务ID}/artifacts
下载产物  GET   上一步返回的 content_url
```

视频模型另有 OpenAI Video API 风格的一组接口，见[视频的 OpenAI 兼容接口](#视频的-openai-兼容接口)。

> 不要用 `/v1/video/generations` 提交这三个模型，这个接口到不了它们。

### 第一步：提交任务

`model`、`prompt`、`image`、`duration` 这几个通用参数写在请求体顶层；其余参数是某个模型专属的（如 `width`、`resolution`、`seed`），写在顶层或 `metadata` 对象里都可以，本文示例放在 `metadata` 里，详见[参数放在哪里](#参数放在哪里)：

```bash
curl -X POST "$BASE_URL/v1/tasks/mulerouter" \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "z-image-spicy",
    "prompt": "一只三花猫趴在洒满阳光的窗台上",
    "metadata": {
      "width": 1024,
      "height": 1024
    }
  }'
```

返回：

```json
{
  "id": "task_AbCdEfGhIjKlMnOpQrStUvWxYz012345",
  "task_id": "task_AbCdEfGhIjKlMnOpQrStUvWxYz012345",
  "model": "z-image-spicy",
  "status": "queued",
  "created_at": 1792504208
}
```

记下 `id`（与 `task_id` 相同），后面查询要用。提交返回只有这五个字段，不含 `object` 和 `progress`；任务进度请通过查询接口获取。

### 第二步：轮询状态

```bash
curl "$BASE_URL/v1/tasks/task_AbCdEfGhIjKlMnOpQrStUvWxYz012345" \
  -H "Authorization: Bearer $API_KEY"
```

```json
{
  "task_id": "task_AbCdEfGhIjKlMnOpQrStUvWxYz012345",
  "platform": "<平台标识>",
  "status": "SUCCESS",
  "progress": "100%",
  "fail_reason": "",
  "created_at": 1792504208,
  "finished_at": 1792504223
}
```

`status` 变成 `SUCCESS` 后去取产物。`platform` 是本站内部的标识，忽略即可。

### 第三步：取产物

```bash
curl "$BASE_URL/v1/tasks/task_AbCdEfGhIjKlMnOpQrStUvWxYz012345/artifacts" \
  -H "Authorization: Bearer $API_KEY"
```

```json
{
  "task_id": "task_AbCdEfGhIjKlMnOpQrStUvWxYz012345",
  "artifacts": [
    {
      "key": "image-1",
      "type": "image",
      "content_url": "https://your-site.com/v1/tasks/task_AbCdEfGhIjKlMnOpQrStUvWxYz012345/artifacts/image-1/content?access=..."
    }
  ]
}
```

`content_url` 就是产物的下载地址，直接 GET 即可，不用再带 `Authorization` 头：

```bash
curl -o result.png "<content_url>"
```

## 模型参数

### 参数放在哪里

请求体里的参数分两类：

| 类别 | 参数 | 位置 |
|---|---|---|
| 通用参数 | `model`、`prompt`、`image`、`duration` | 请求体**顶层** |
| 模型专属参数 | 其余参数，如 `width`、`height`、`prompt_extend`、`seed`、`last_image`、`resolution` | 顶层或 **`metadata` 对象**里，两处都生效 |

```json
{
  "model": "wan2.2-i2v-spicy",
  "prompt": "镜头缓缓推进",
  "image": "https://example.com/first-frame.png",
  "duration": 8,
  "metadata": {
    "resolution": "720p",
    "seed": 42
  }
}
```

模型专属参数写在顶层或 `metadata` 里，在 `POST /v1/tasks/mulerouter` 和 `POST /v1/videos` 上都生效，并按实际取值生成和计费。同一个参数顶层和 `metadata` 都写时：`/v1/tasks/mulerouter` 以 `metadata` 为准；`/v1/videos` 要求两处的值一致，不一致返回 400。

`metadata` 必须是 JSON 对象，不要写成字符串。

下面各表的「位置」一列是本文示例用的位置；写「`metadata`」的参数也可以写在顶层。

### z-image-spicy（文生图）

由文字提示词生成图片。

| 参数 | 位置 | 类型 | 必填 | 默认 | 取值范围 | 说明 |
|---|---|---|---|---|---|---|
| `model` | 顶层 | string | ✅ | — | | 固定填 `z-image-spicy` |
| `prompt` | 顶层 | string | ✅ | — | 非空 | 图片描述 |
| `width` | `metadata` | integer | | 1024 | 256 – 1536 | 宽度（像素） |
| `height` | `metadata` | integer | | 1536 | 256 – 1536 | 高度（像素） |
| `prompt_extend` | `metadata` | boolean | | `true` | | 是否让模型智能改写提示词。开启通常出图更好，但会额外收费 |
| `seed` | `metadata` | integer / null | | `null` | | 随机种子。同样的种子 + 同样的参数可复现同一张图；`null` 或 `0` 表示随机 |

```bash
curl -X POST "$BASE_URL/v1/tasks/mulerouter" \
  -H "Authorization: Bearer $API_KEY" -H "Content-Type: application/json" \
  -d '{
    "model": "z-image-spicy",
    "prompt": "一只三花猫趴在洒满阳光的窗台上，胶片质感",
    "metadata": {
      "width": 1024,
      "height": 1024,
      "prompt_extend": true
    }
  }'
```

### qwen-image-edit-spicy（图生图）

按提示词编辑一张已有图片。输出尺寸由输入图决定，没有尺寸参数。

| 参数 | 位置 | 类型 | 必填 | 默认 | 说明 |
|---|---|---|---|---|---|
| `model` | 顶层 | string | ✅ | — | 固定填 `qwen-image-edit-spicy` |
| `image` | 顶层 | string | ✅ | — | 输入图片。公网可访问的 `http`/`https` 链接，或 base64 编码的图片数据 |
| `prompt` | 顶层 | string | ✅ | — | 描述你想做的修改，例如「把背景换成雪山」 |
| `seed` | `metadata` | integer / null | | `null` | 随机种子 |

```bash
curl -X POST "$BASE_URL/v1/tasks/mulerouter" \
  -H "Authorization: Bearer $API_KEY" -H "Content-Type: application/json" \
  -d '{
    "model": "qwen-image-edit-spicy",
    "image": "https://example.com/photo.png",
    "prompt": "把背景换成雪山，保持人物不变",
    "metadata": {
      "seed": 7
    }
  }'
```

### wan2.2-i2v-spicy（图生视频）

把一张静态图变成一段视频。

| 参数 | 位置 | 类型 | 必填 | 默认 | 取值范围 | 说明 |
|---|---|---|---|---|---|---|
| `model` | 顶层 | string | ✅ | — | | 固定填 `wan2.2-i2v-spicy` |
| `image` | 顶层 | string | ✅ | — | | 首帧图片，链接或 base64 |
| `prompt` | 顶层 | string | ✅ | — | 非空 | 描述镜头运动或画面变化 |
| `duration` | 顶层 | integer | | `5` | **只能是 5 或 8** | 视频时长（秒） |
| `last_image` | `metadata` | string / null | | `null` | | 尾帧图片。给了的话模型会在首尾帧之间插值 |
| `resolution` | `metadata` | string | | `"480p"` | **只能是 `480p` 或 `720p`** | 输出分辨率 |
| `prompt_extend` | `metadata` | boolean | | `true` | | 是否智能改写提示词 |
| `seed` | `metadata` | integer / null | | `null` | | 随机种子 |

```bash
curl -X POST "$BASE_URL/v1/tasks/mulerouter" \
  -H "Authorization: Bearer $API_KEY" -H "Content-Type: application/json" \
  -d '{
    "model": "wan2.2-i2v-spicy",
    "image": "https://example.com/first-frame.png",
    "prompt": "镜头缓缓推进，人物微笑",
    "duration": 8,
    "metadata": {
      "resolution": "720p"
    }
  }'
```

> `duration` 和 `resolution` 直接决定价格，取值范围是**严格校验**的。传 `duration: 9` 或 `resolution: "1080p"` 会被拒绝并返回 400，不会被悄悄改成合法值——避免你以为在生成 9 秒视频、实际付了别的钱。

## 查询任务

```bash
curl "$BASE_URL/v1/tasks/{任务ID}" -H "Authorization: Bearer $API_KEY"
```

`status` 有六种取值：

| `status` | 含义 | 该做什么 |
|---|---|---|
| `NOT_START` | 刚提交，还没开始同步状态 | 继续等 |
| `SUBMITTED` | 已提交到生成服务 | 继续等 |
| `QUEUED` | 排队中 | 继续等 |
| `IN_PROGRESS` | 正在生成 | 继续等 |
| `SUCCESS` | 完成 | [获取产物](#获取产物) |
| `FAILURE` | 失败 | 看 `fail_reason`，已扣费用自动退还 |

任务失败时，`fail_reason` 尽量给出生成服务的具体原因，包括错误标题、详情和错误码，例如 `Invalid Request: Task execution failed (error code 3001)`，方便你判断是提示词违规、参数问题还是服务故障。生成服务报告完成却没有返回任何图片或视频时，任务同样判为 `FAILURE`，原因是 `the task completed without producing any image, video or audio`，费用自动退还。

`finished_at` 在任务结束前是 `0`。

**轮询建议**：本站每 15 秒同步一次任务状态，查得比这更频繁不会更快拿到结果。

- 图片模型：每 5 秒查一次，通常 15–60 秒完成
- 视频模型：每 10 秒查一次，通常 1–3 分钟完成，8 秒 720p 更久

任务**不需要你持续查询也会继续推进**。可以提交后关掉程序，过几分钟再来取结果。

## 获取产物

```bash
curl "$BASE_URL/v1/tasks/{任务ID}/artifacts" -H "Authorization: Bearer $API_KEY"
```

任务成功后，`artifacts` 列出每个产物：

| 模型 | `key` | `type` |
|---|---|---|
| 图片模型 | `image-1`（有多张时依次是 `image-2`、`image-3`……） | `image` |
| 视频模型 | `video` | `video` |

视频任务的返回：

```json
{
  "task_id": "task_ZyXwVuTsRqPoNmLkJiHgFeDcBa543210",
  "artifacts": [
    {
      "key": "video",
      "type": "video",
      "content_url": "https://your-site.com/v1/tasks/task_ZyXwVuTsRqPoNmLkJiHgFeDcBa543210/artifacts/video/content?access=..."
    }
  ]
}
```

任务还没成功时 `artifacts` 是空数组。

每个产物的 `content_url` 都可以直接 GET 下载（也支持 HEAD），链接里已经带了访问凭证，不用再带 `Authorization` 头，可以直接交给浏览器或下载工具。

## 视频的 OpenAI 兼容接口

视频模型另有一组 OpenAI Video API 风格的接口，方便直接对接 OpenAI 客户端：

```
提交      POST  $BASE_URL/v1/videos
查询      GET   $BASE_URL/v1/videos/{任务ID}
下载视频  GET   $BASE_URL/v1/videos/{任务ID}/content
```

提交的请求体是 JSON 对象，除了[模型参数](#wan22-i2v-spicy图生视频)里的写法，还接受 OpenAI Video API 的标准字段：

| OpenAI 字段 | 等同于 | 说明 |
|---|---|---|
| `seconds` | `duration` | 视频时长（秒），可以写成字符串 `"8"` 或数字。只能是 5 或 8 |
| `size` | `resolution` | 形如 `宽x高`，按短边换算：`1280x720`、`720x1280` 是 `720p`，`854x480`、`480x832` 是 `480p`。短边只能是 480 或 720 |
| `input_reference` | `image` | 首帧图片的链接 |

用 OpenAI 写法提交一段 8 秒 480p 的竖屏视频：

```bash
curl -X POST "$BASE_URL/v1/videos" \
  -H "Authorization: Bearer $API_KEY" -H "Content-Type: application/json" \
  -d '{
    "model": "wan2.2-i2v-spicy",
    "prompt": "镜头缓缓推进，人物微笑",
    "image": "https://example.com/first-frame.png",
    "seconds": "8",
    "size": "480x832"
  }'
```

模型专属参数写在顶层或 `metadata` 里都生效，下面这条按 5 秒 720p 生成和计费：

```bash
curl -X POST "$BASE_URL/v1/videos" \
  -H "Authorization: Bearer $API_KEY" -H "Content-Type: application/json" \
  -d '{
    "model": "wan2.2-i2v-spicy",
    "prompt": "镜头缓缓推进，人物微笑",
    "image": "https://example.com/first-frame.png",
    "resolution": "720p",
    "seed": 7,
    "prompt_extend": false,
    "last_image": "https://example.com/last-frame.png"
  }'
```

`/v1/videos` 的请求比 `/v1/tasks/mulerouter` 校验得更严：

- **同一个参数只能有一个值。** `seconds` 和 `duration`、`size` 和 `resolution`、`input_reference` 和 `image`、顶层和 `metadata` 里的同名参数，同时写时值必须一致，否则返回 400。
- **不认识的字段直接拒绝。** 模型不接受的字段（例如 `mode`）返回 400，不会被悄悄忽略。
- **图片只接受链接。** `image`、`input_reference` 必须是 `http`/`https` 链接，不支持 base64，也不支持 multipart 上传文件。

`seconds`、`size`、`input_reference` 只在 `/v1/videos` 上有效；在 `/v1/tasks/mulerouter` 上请写 `duration`、`resolution`、`image`，传 `seconds` 或 `size` 会被拒绝。

查询返回：

```json
{
  "id": "task_ZyXwVuTsRqPoNmLkJiHgFeDcBa543210",
  "object": "video",
  "model": "wan2.2-i2v-spicy",
  "status": "completed",
  "progress": 100,
  "created_at": 1792504230,
  "completed_at": 1792504283
}
```

`status` 取值为 `queued`、`in_progress`、`completed`、`failed`。`completed_at` 只在 `completed` 时出现，未完成的任务只有前六个字段。查询结果里没有视频链接，任务完成后从 `GET $BASE_URL/v1/videos/{任务ID}/content` 下载视频，请求要带 `Authorization` 头：

```bash
curl -L "$BASE_URL/v1/videos/{任务ID}/content" \
  -H "Authorization: Bearer $API_KEY" -o result.mp4
```

失败的任务带 `error`，`message` 与 `/v1/tasks/{任务ID}` 的 `fail_reason` 相同：

```json
{
  "id": "task_ZyXwVuTsRqPoNmLkJiHgFeDcBa543210",
  "object": "video",
  "model": "wan2.2-i2v-spicy",
  "status": "failed",
  "progress": 100,
  "created_at": 1792504230,
  "error": { "message": "...", "code": "task_failed" }
}
```

这组接口**只支持视频模型**，图片模型请用 `/v1/tasks` 系列接口。用 `/v1/tasks/mulerouter` 提交的视频任务也可以用 `/v1/videos/{任务ID}` 查询和下载，反过来也一样。

## 计费

三个模型都是**按次计费**，提交任务时即扣费。

| 模型 | 计费方式 |
|---|---|
| `z-image-spicy` | 每张固定价；`prompt_extend` 开启时略有加价 |
| `qwen-image-edit-spicy` | 每张固定价，无附加项 |
| `wan2.2-i2v-spicy` | **按秒 × 分辨率**：`单价 × 时长 × 分辨率倍率`，720p 是 480p 的 2 倍 |

视频价格示例（基准价，实际按你的分组倍率浮动）：

| 请求 | 算式 | 价格 |
|---|---|---|
| 5 秒 480p | `$0.02 × 5 × 1` | $0.10 |
| 8 秒 480p | `$0.02 × 8 × 1` | $0.16 |
| 5 秒 720p | `$0.02 × 5 × 2` | $0.20 |
| 8 秒 720p | `$0.02 × 8 × 2` | $0.32 |

关于退费：

- **任务失败自动全额退款**，不需要申请。
- **超过 24 小时未完成**判定为超时失败，同样全额退款。
- 请求被拒绝（参数错误、余额不足等）**不会扣费**——校验发生在扣费之前。

后台「日志」页面可以看到每次调用的扣费明细。

## 错误处理

### 参数与任务类错误

HTTP 状态码非 2xx，响应体形如：

```json
{ "code": "plugin_request_invalid", "message": "...invalid value 9 for duration, allowed: 5, 8...", "data": null }
```

`message` 里会包含具体原因。常见原因：

| `message` 中包含 | 含义 |
|---|---|
| `prompt is required` | 缺 `prompt`，或 `prompt` 是空字符串 |
| `invalid value 9 for duration, allowed: 5, 8` | `duration` 只能是 5 或 8 |
| `invalid value "1080p" for resolution, allowed: 480p, 720p` | 分辨率只能是 480p 或 720p |
| `width must be <= 1536` | 尺寸超出 256 – 1536 |
| `affect the price of this model and are not enabled on this endpoint` | 你传了一个会影响价格但这个模型不支持的参数（如给图片模型传 `duration`），去掉它 |
| `plugin usage value must be a number` | `duration` 等数值参数写成了非数字 |
| `are not supported by this model` | `/v1/videos` 上传了这个模型不接受的字段（如 `mode`），去掉它 |
| `give different values` | `/v1/videos` 上同一个参数用两种写法给了不同的值（如 `seconds: "8"` 和 `duration: 5`），只留一种 |
| `size must be WIDTHxHEIGHT` | `size` 要写成 `1280x720` 这样的 `宽x高` |
| `input_reference must be an image URL` | `/v1/videos` 的 `input_reference` 或 `image` 不是 `http`/`https` 链接 |

查询和取产物时任务 ID 不对，返回 404。

### 鉴权与调度类错误

响应体形如：

```json
{ "error": { "code": "", "message": "Invalid token (request id: ...)", "type": "new_api_error" } }
```

| HTTP | 原因 | 怎么办 |
|---|---|---|
| 401 | 密钥无效、已删除或已过期 | 检查 `Authorization` 头，重新生成令牌 |
| 403 | 令牌无权访问该模型 | 检查令牌的「可用模型」设置 |
| 429 | 触发限流 | 退避后重试 |
| 503 | 该模型当前无可用服务 | 确认 `model` 拼写正确、提交地址是 `/v1/tasks/mulerouter`，或联系管理员 |

余额不足会在提交时明确报错，不会先生成再欠费。

## 完整示例

三条 curl：一条提交拿任务 ID，一条查询状态，一条取产物地址。

### 第一条：提交

```bash
curl -X POST "$BASE_URL/v1/tasks/mulerouter" \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "z-image-spicy",
    "prompt": "一只三花猫趴在洒满阳光的窗台上",
    "metadata": {
      "width": 1024,
      "height": 1024
    }
  }'
```

```json
{
  "id": "task_AbCdEfGhIjKlMnOpQrStUvWxYz012345",
  "task_id": "task_AbCdEfGhIjKlMnOpQrStUvWxYz012345",
  "model": "z-image-spicy",
  "status": "queued",
  "created_at": 1792504208
}
```

### 第二条：查询

把上一步的 `id` 填进路径。图片等 15 秒左右再查，视频等 1 分钟左右。

```bash
curl "$BASE_URL/v1/tasks/task_AbCdEfGhIjKlMnOpQrStUvWxYz012345" \
  -H "Authorization: Bearer $API_KEY"
```

```json
{
  "task_id": "task_AbCdEfGhIjKlMnOpQrStUvWxYz012345",
  "platform": "<平台标识>",
  "status": "SUCCESS",
  "progress": "100%",
  "fail_reason": "",
  "created_at": 1792504208,
  "finished_at": 1792504223
}
```

`status` 还不是 `SUCCESS` 就过一会儿再执行同一条 curl，直到变成 `SUCCESS` 或 `FAILURE`（看 `fail_reason`）。

### 第三条：取产物

```bash
curl "$BASE_URL/v1/tasks/task_AbCdEfGhIjKlMnOpQrStUvWxYz012345/artifacts" \
  -H "Authorization: Bearer $API_KEY"
```

```json
{
  "task_id": "task_AbCdEfGhIjKlMnOpQrStUvWxYz012345",
  "artifacts": [
    {
      "key": "image-1",
      "type": "image",
      "content_url": "https://your-site.com/v1/tasks/task_AbCdEfGhIjKlMnOpQrStUvWxYz012345/artifacts/image-1/content?access=..."
    }
  ]
}
```

用 `content_url` 下载图片。

### 换成另外两个模型

提交那条 curl 换 `model` 和参数即可，查询和取产物完全一样。

```bash
# 图生图
-d '{
  "model": "qwen-image-edit-spicy",
  "image": "https://example.com/photo.png",
  "prompt": "把背景换成雪山"
}'

# 图生视频
-d '{
  "model": "wan2.2-i2v-spicy",
  "image": "https://example.com/first-frame.png",
  "prompt": "镜头缓缓推进",
  "duration": 8,
  "metadata": {
    "resolution": "720p"
  }
}'
```

## 注意事项

**产物链接有有效期**，本站不做长期保存。**拿到结果后请尽快下载到你自己的存储**，不要把 `content_url` 直接存进数据库当长期地址用。

**超时**：任务超过 24 小时未完成会被自动判定为失败并退款。正常情况下图片一分钟内、视频几分钟内完成，如果长时间停在排队状态，通常是生成服务繁忙。

**并发**：没有单独的并发限制，但受令牌整体限流约束。批量任务建议控制并发，并对 429 做退避重试。

**内容审核**：提示词和产物都会经过审核，被拒绝的任务返回 `FAILURE` 并附带原因，费用自动退还。

**图片输入**：在 `/v1/tasks/mulerouter` 上，`image` 和 `last_image` 支持公网链接或 base64；`/v1/videos` 的 `image` 和 `input_reference` 只支持链接。用链接时确保生成服务能访问到——内网地址、需要登录才能打开的链接都会导致任务失败。base64 建议不超过 10 MB。

**参数位置**：`model`、`prompt`、`image`、`duration` 写在请求体顶层，其余模型专属参数（`width`、`height`、`prompt_extend`、`seed`、`last_image`、`resolution`）写在顶层或 `metadata` 对象里都生效，见[参数放在哪里](#参数放在哪里)。
