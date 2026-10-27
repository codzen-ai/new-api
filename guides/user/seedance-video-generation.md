# Seedance 视频生成调用指南

面向终端用户。本文说明如何用 OpenAI Video API 风格的接口调用四个 Seedance 视频模型：文生视频、首帧、首尾帧、多图参考和参考视频。

视频生成是**异步任务**：提交请求拿到任务 ID，再轮询任务状态，完成后下载视频。生成一般要几十秒到几分钟。

## 目录

- [模型一览](#模型一览)
- [开始之前](#开始之前)
- [调用流程](#调用流程)
- [请求参数](#请求参数)
  - [参数放在哪里](#参数放在哪里)
  - [时长](#时长)
  - [分辨率与宽高比](#分辨率与宽高比)
  - [图片与视频输入](#图片与视频输入)
  - [其他参数](#其他参数)
  - [不支持的参数](#不支持的参数)
- [查询任务](#查询任务)
- [下载视频](#下载视频)
- [计费](#计费)
- [错误处理](#错误处理)
- [完整示例](#完整示例)
- [注意事项](#注意事项)

## 模型一览

| 模型 | 时长 | 分辨率 | 参考图上限 | 参考视频上限 |
|---|---|---|---|---|
| `seedance-2.5` | 4–30 秒，或 `-1`（由模型决定） | 480p / 720p / 1080p | 30 | 10 |
| `seedance-2.0-pro` | 4–15 秒 | 480p / 720p / 1080p / 4k | 9 | 3 |
| `seedance-2.0-fast` | 4–15 秒 | 480p / 720p | 9 | 3 |
| `seedance-2.0-mini` | 4–15 秒 | 480p / 720p | 9 | 3 |

四个模型都支持文生视频、首帧、首尾帧、多图参考、参考视频五种输入方式，素材只接受链接。

价格按视频 token 计算，各模型、各分辨率的单价见本站「模型广场」或价格页，计算方式见[计费](#计费)。

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

所有请求都要带 `Authorization` 头，提交请求还要带 `Content-Type`：

```
Authorization: Bearer sk-xxxxxxxx
Content-Type: application/json
```

## 调用流程

```
提交      POST  $BASE_URL/v1/videos
查询      GET   $BASE_URL/v1/videos/{任务ID}
下载视频  GET   $BASE_URL/v1/videos/{任务ID}/content
```

> 这四个模型只能用 `POST /v1/videos` 提交，`/v1/video/generations` 到不了它们。

### 第一步：提交任务

```bash
curl -X POST "$BASE_URL/v1/videos" \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "seedance-2.0-mini",
    "prompt": "一只三花猫在洒满阳光的窗台上伸懒腰，镜头缓缓推进",
    "seconds": "5",
    "resolution": "720p",
    "aspect_ratio": "16:9"
  }'
```

返回 OpenAI 视频对象，`id` 就是任务 ID：

```json
{
  "id": "task_8LLoYb0V6aHt49LzmVynGryY59d152xD",
  "object": "video",
  "model": "seedance-2.0-mini",
  "status": "queued",
  "progress": 0,
  "created_at": 1793060759
}
```

### 第二步：轮询状态

```bash
curl "$BASE_URL/v1/videos/task_8LLoYb0V6aHt49LzmVynGryY59d152xD" \
  -H "Authorization: Bearer $API_KEY"
```

`status` 变成 `completed` 后去下载视频，变成 `failed` 时看 `error.message`。返回格式见[查询任务](#查询任务)。

### 第三步：下载视频

```bash
curl -L "$BASE_URL/v1/videos/task_8LLoYb0V6aHt49LzmVynGryY59d152xD/content" \
  -H "Authorization: Bearer $API_KEY" -o result.mp4
```

视频在任务完成后 **24 小时内**可以下载，见[下载视频](#下载视频)。

## 请求参数

请求体必须是 JSON 对象，不支持 multipart 上传文件。

| 参数 | 类型 | 必填 | 默认 | 说明 |
|---|---|---|---|---|
| `model` | string | ✅ | — | 四个模型名之一 |
| `prompt` | string | ✅ | — | 视频描述，不能为空 |
| `seconds` / `duration` | integer 或数字字符串 | | 见[时长](#时长) | 视频时长（秒），两种写法任选一种 |
| `resolution` | string | | `720p` | `480p`、`720p`、`1080p`、`4k`，按模型而定 |
| `aspect_ratio` | string | | 由模型决定 | `16:9`、`4:3`、`1:1`、`3:4`、`9:16`、`21:9`；`seedance-2.5` 另有 `adaptive` |
| `size` | string | | — | OpenAI 写法，形如 `1280x720`，同时表示分辨率和宽高比 |
| `input_reference` / `image_url` | string | | — | 首帧图片链接，两种写法任选一种 |
| `media` | array | | — | 首尾帧、参考图、参考视频，见[图片与视频输入](#图片与视频输入) |
| `with_audio` | boolean | | 由模型决定 | 是否生成音频。不传时模型会生成带音轨的视频 |
| `seed` | integer | | — | 随机种子，用于复现结果 |
| `output_format` | string | | — | 只有 `seedance-2.5` 支持，取值 `mp4` 或 `mov` |

### 参数放在哪里

除 `model` 外，上表的参数写在请求体顶层或 `metadata` 对象里都生效：

```json
{
  "model": "seedance-2.5",
  "prompt": "海浪拍打礁石，慢动作",
  "metadata": {
    "resolution": "1080p",
    "seed": 42
  }
}
```

同一个参数在顶层和 `metadata` 里都写时，两处的值必须一致，否则返回 400。`metadata` 必须是 JSON 对象，不要写成字符串。

### 时长

`seconds`（OpenAI 写法）和 `duration` 含义相同，可以写成整数 `5` 或字符串 `"5"`，只接受整数秒。两个都传时值必须一致。

| 模型 | 允许范围 | 不传时 |
|---|---|---|
| `seedance-2.5` | 4–30 秒，或 `-1` | `-1`：由模型自己决定长度 |
| `seedance-2.0-pro` / `-fast` / `-mini` | 4–15 秒 | 5 秒 |

超出范围返回 400，报错里写明允许的范围。

### 分辨率与宽高比

用 `resolution` 和 `aspect_ratio` 分别指定，或者用 OpenAI 的 `size` 一次指定两者。不传分辨率时按 `720p` 生成。

| 模型 | `resolution` 可选值 | `aspect_ratio` 可选值 |
|---|---|---|
| `seedance-2.5` | `480p`、`720p`、`1080p` | `16:9`、`4:3`、`1:1`、`3:4`、`9:16`、`21:9`、`adaptive` |
| `seedance-2.0-pro` | `480p`、`720p`、`1080p`、`4k` | `16:9`、`4:3`、`1:1`、`3:4`、`9:16`、`21:9` |
| `seedance-2.0-fast` | `480p`、`720p` | 同上 |
| `seedance-2.0-mini` | `480p`、`720p` | 同上 |

`size` 写成 `宽x高`。短边决定分辨率：480 → `480p`，720 → `720p`，1080 → `1080p`，2160 → `4k`。宽高之比决定宽高比，允许 1% 以内的误差，所以 `854x480` 算作 16:9。

| `size` | 等同于 |
|---|---|
| `1280x720` | `720p` + `16:9` |
| `720x1280` | `720p` + `9:16` |
| `854x480` | `480p` + `16:9` |
| `1440x1080` | `1080p` + `4:3` |
| `3840x2160` | `4k` + `16:9`（只有 `seedance-2.0-pro` 支持） |

对不上上面任何一档，或者该模型不支持换算出的档位，都返回 400。实际输出尺寸由模型决定，例如 480p 9:16 的视频是 496×864。

### 图片与视频输入

素材只接受 `http`/`https` 链接，不支持 base64，也不支持上传文件。链接必须能从公网直接打开，详见[注意事项](#注意事项)。

**首帧**：用 `input_reference` 或 `image_url` 传一张图片链接。两个都传时值必须一致。

```json
{
  "model": "seedance-2.0-fast",
  "prompt": "人物转身微笑",
  "input_reference": "https://example.com/first-frame.png"
}
```

**其他输入方式**都写在 `media` 数组里，每一项只能有 `type` 和 `url` 两个字段：

| `type` | 含义 |
|---|---|
| `first_frame` | 首帧图片 |
| `last_frame` | 尾帧图片 |
| `reference_image` | 参考图（人物、风格等） |
| `reference_video` | 参考视频（风格、动作等） |

**首尾帧**：

```json
{
  "model": "seedance-2.0-pro",
  "prompt": "花朵从含苞到盛开",
  "media": [
    { "type": "first_frame", "url": "https://example.com/bud.png" },
    { "type": "last_frame", "url": "https://example.com/bloom.png" }
  ]
}
```

**多图参考**：传多张 `reference_image` 即可，不需要额外设置参考模式。

```json
{
  "model": "seedance-2.5",
  "prompt": "图1的女孩穿着图2的外套走在街上",
  "media": [
    { "type": "reference_image", "url": "https://example.com/girl.png" },
    { "type": "reference_image", "url": "https://example.com/coat.png" }
  ]
}
```

**参考视频**：可以单独传，也可以和参考图一起传。带参考视频的请求按「有参考视频」那一档单价计费，见[计费](#计费)。

```json
{
  "model": "seedance-2.0-mini",
  "prompt": "参考视频的运镜，拍摄一片向日葵田",
  "seconds": 4,
  "resolution": "480p",
  "media": [
    { "type": "reference_video", "url": "https://example.com/camera-move.mp4" }
  ]
}
```

数量上限见[模型一览](#模型一览)。超过上限时立即返回 400，报错里写明上限，不会被悄悄截断。

不同输入方式能否混用（例如首帧能否和参考图一起传）由生成服务判断。组合不合法时，提交请求会直接返回 400，不扣费。

### 其他参数

- `with_audio`：`true` 或 `false`。不传时模型会生成带音轨的视频，不需要声音时传 `false`。
- `seed`：整数。同样的种子和参数可以复现相近的结果。
- `output_format`：只有 `seedance-2.5` 支持，取值 `mp4` 或 `mov`。其他模型传了会返回 400。

### 不支持的参数

以下参数会被拒绝（返回 400），请不要传：

- `asset_id`，以及 `media[]` 里的 `asset_id`：不支持引用素材库，请直接传链接。
- 音频素材（`media[].type` 为 `input_audio`、`reference_audio`、`mouth_audio`）：暂未开放。
- `web_search`：暂未开放。
- `thumbnail_format`、`fps` 等上表以外的字段：报错里会列出这些字段名，去掉即可。

## 查询任务

```bash
curl "$BASE_URL/v1/videos/{任务ID}" \
  -H "Authorization: Bearer $API_KEY"
```

完成时返回：

```json
{
  "id": "task_8LLoYb0V6aHt49LzmVynGryY59d152xD",
  "object": "video",
  "model": "seedance-2.0-mini",
  "status": "completed",
  "progress": 100,
  "created_at": 1793060759,
  "completed_at": 1793060823
}
```

`status` 取值为 `queued`、`in_progress`、`completed`、`failed`。查询结果里没有视频链接，完成后从 `/content` 下载，见[下载视频](#下载视频)。

失败时带 `error`：

```json
{
  "id": "task_xl7oWqF1pRwlfPKCnzQEqvR0bBiV05mU",
  "object": "video",
  "model": "seedance-2.0-mini",
  "status": "failed",
  "progress": 100,
  "created_at": 1793060497,
  "error": { "code": "task_failed", "message": "上游服务处理失败，请稍后重试。" }
}
```

`error.message` 是生成服务给出的原因，可以据此判断该重试还是修改请求。失败的任务全额退款。

用 `GET $BASE_URL/v1/tasks/{任务ID}` 也能查询同一个任务，返回里的 `platform` 是本站内部标识，忽略即可。

## 下载视频

```bash
curl -L "$BASE_URL/v1/videos/{任务ID}/content" \
  -H "Authorization: Bearer $API_KEY" -o result.mp4
```

- 请求要带 `Authorization` 头，返回视频文件，默认是 mp4。
- 视频在任务完成后 **24 小时内**可以下载，过期后下载失败，也无法重新生成链接。**拿到结果后请尽快保存到你自己的存储。**

## 计费

视频按 **token** 计费，单价由模型、分辨率和是否带参考视频决定，在本站价格页可以查看。

视频 token 数由输出视频的尺寸和帧数决定：

```
token ≈ 宽 × 高 × (24 × 秒数 + 1) / 1024
```

例如一段 4 秒 480p 9:16 的视频是 496×864，约 `496 × 864 × 97 / 1024 ≈ 40594` token。带参考视频时，参考视频本身也会计入 token。

扣费分两步：

1. **提交时预扣**：按请求的时长和分辨率预估 token 并预扣，预估按该分辨率下最大的输出尺寸计算，一般不低于实际消耗。`seedance-2.5` 不传时长（`-1`）时按 30 秒预扣。预估不包括参考视频本身的 token。
2. **完成后结算**：按实际消耗的 token 计算最终费用，多退少补。

关于退费：

- **任务失败自动全额退款**，不需要申请。
- 请求被拒绝（参数错误、余额不足、素材组合不合法等）**不会扣费**。

后台「日志」页面可以看到每次调用的预扣和结算明细。

## 错误处理

### 参数错误

HTTP 400，响应体形如：

```json
{ "error": { "code": "", "message": "seconds must be between 4 and 15 seconds (request id: ...)", "type": "new_api_error" } }
```

常见原因：

| `message` 中包含 | 含义 |
|---|---|
| `prompt is required` | 缺 `prompt`，或 `prompt` 是空字符串 |
| `must be between 4 and 15 seconds`、`must be -1 or between 4 and 30 seconds` | 时长超出该模型的范围 |
| `must be a whole number` | 时长或 `seed` 不是整数 |
| `give different values` | 同一个参数给了两个不同的值，例如 `seconds` 和 `duration`、`input_reference` 和 `image_url`、顶层和 `metadata` |
| `resolution must be one of` | 该模型不支持这个分辨率，例如 `seedance-2.0-mini` 传 `1080p` |
| `aspect_ratio must be one of` | 该模型不支持这个宽高比，例如 `seedance-2.0-*` 传 `adaptive` |
| `size must be WIDTHxHEIGHT`、`matches no resolution and aspect ratio` | `size` 格式不对，或换算不出支持的分辨率和宽高比 |
| `must be an http(s) URL` | 图片或视频不是 `http`/`https` 链接 |
| `media[0].type must be one of` | `media` 里有不支持的类型，例如音频素材 |
| `takes only type and url` | `media` 的某一项带了 `type`、`url` 以外的字段，例如 `asset_id` |
| `media takes at most 9 reference images`、`media takes at most 3 reference videos` | 参考图或参考视频超过该模型的上限 |
| `output_format must be one of` | `output_format` 不是 `mp4` 或 `mov` |
| `are not supported by this model` | 传了不支持的字段，例如 `asset_id`、`web_search`、`thumbnail_format`，或在 `seedance-2.0-*` 上传 `output_format` |
| `metadata must be a JSON object` | `metadata` 写成了字符串或数组 |
| `a JSON request body is required` | 请求体不是 JSON，例如用了 multipart |

素材组合不合法、素材链接无法访问等由生成服务判断的问题，也会在提交时返回 400，响应体形状不同，`message` 里是生成服务给出的原因：

```json
{ "code": "fail_to_fetch_task", "message": "{\"error\":{\"message\":\"...\",\"type\":\"invalid_request_error\",\"code\":\"...\"}}", "data": null }
```

这些请求都不扣费。

查询或下载时任务 ID 不对，返回 404。

### 鉴权与调度类错误

| HTTP | 原因 | 怎么办 |
|---|---|---|
| 401 | 密钥无效、已删除或已过期 | 检查 `Authorization` 头，重新生成令牌 |
| 403 | 余额不足（`code` 是 `insufficient_user_quota`），或令牌无权访问该模型 | 充值，或检查令牌的「可用模型」设置 |
| 429 | 触发本站的请求限流 | 退避后重试 |
| 503 | 该模型当前无可用服务 | 确认 `model` 拼写正确、提交地址是 `/v1/videos`，或联系管理员 |

余额不足会在提交时明确报错，不会先生成再欠费。

## 完整示例

一段 5 秒 720p 竖屏、不带声音的首帧视频：

```bash
# 1. 提交
curl -X POST "$BASE_URL/v1/videos" \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "seedance-2.0-fast",
    "prompt": "女孩回头微笑，头发被风吹起",
    "input_reference": "https://example.com/girl.png",
    "seconds": "5",
    "size": "720x1280",
    "with_audio": false
  }'
# 记下返回的 id，例如 task_AbCdEfGhIjKlMnOpQrStUvWxYz012345

# 2. 每隔 15 秒左右查询一次，直到 status 变成 completed 或 failed
curl "$BASE_URL/v1/videos/task_AbCdEfGhIjKlMnOpQrStUvWxYz012345" \
  -H "Authorization: Bearer $API_KEY"

# 3. 下载
curl -L "$BASE_URL/v1/videos/task_AbCdEfGhIjKlMnOpQrStUvWxYz012345/content" \
  -H "Authorization: Bearer $API_KEY" -o result.mp4
```

用 OpenAI Python SDK 调用：

```python
import time
from openai import OpenAI

client = OpenAI(base_url="https://your-site.com/v1", api_key="sk-你的令牌")

video = client.videos.create(
    model="seedance-2.0-mini",
    prompt="一只三花猫在洒满阳光的窗台上伸懒腰",
    seconds="5",
    size="1280x720",
)
while video.status in ("queued", "in_progress"):
    time.sleep(15)
    video = client.videos.retrieve(video.id)

if video.status == "completed":
    client.videos.download_content(video.id).write_to_file("result.mp4")
else:
    print("失败：", video.error)
```

`seconds`、`size`、`input_reference` 以外的参数（如 `resolution`、`aspect_ratio`、`media`、`with_audio`）可以通过 SDK 的 `extra_body` 传入。

## 注意事项

**视频只保留 24 小时**。完成后请尽快下载并保存到你自己的存储，过期后无法再下载。

**素材链接必须公网可直接访问**。生成服务会自己去下载图片和视频，内网地址、需要登录或带鉴权头才能打开的链接（包括本站的 `/content` 下载地址）都会导致提交失败。签名链接请确保在任务完成前不过期。

**时长不传的区别**：`seedance-2.5` 不传时长时由模型决定长度，按 30 秒预扣、完成后按实际结算；`seedance-2.0-*` 不传时默认生成 5 秒。

**超时**：任务长时间未完成会被判定为失败并全额退款，具体时长以本站设置为准。

**并发**：`/v1/videos` 受本站的请求限流约束。批量任务建议控制并发，并对 429 做退避重试。
