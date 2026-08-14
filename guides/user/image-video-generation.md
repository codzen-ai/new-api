# 图片与视频生成模型调用指南

面向终端用户。本文说明如何调用三个生成模型：文生图、图生图、图生视频。

三个模型都是**异步任务**：提交请求拿到任务 ID，再轮询任务状态取结果。没有同步返回图片或视频的接口——生成需要几十秒到几分钟，不适合让请求一直挂着。

## 目录

- [模型一览](#模型一览)
- [开始之前](#开始之前)
- [调用流程](#调用流程)
- [模型参数](#模型参数)
  - [z-image-spicy 文生图](#z-image-spicy文生图)
  - [qwen-image-edit-spicy 图生图](#qwen-image-edit-spicy图生图)
  - [wan2.2-i2v-spicy 图生视频](#wan22-i2v-spicy图生视频)
- [查询任务](#查询任务)
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

所有请求都要带这两个头：

```
Authorization: Bearer sk-xxxxxxxx
Content-Type: application/json
```

## 调用流程

三个模型用同一套接口，只是 `model` 和参数不同。

```
提交    POST  $BASE_URL/v1/video/generations
查询    GET   $BASE_URL/v1/video/generations/{任务ID}
```

> 接口路径里的 `video` 是历史命名，它是本站统一的**异步任务**接口，图片模型同样走这里。

### 第一步：提交任务

把 `model` 和该模型的参数一起放在请求体顶层：

```bash
curl -X POST "$BASE_URL/v1/video/generations" \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "z-image-spicy",
    "prompt": "一只三花猫趴在洒满阳光的窗台上",
    "width": 1024,
    "height": 1024
  }'
```

返回：

```json
{
  "id": "task_uokagkWjYst2Y1KW0B9MvVJdgPAov2jN",
  "task_id": "task_uokagkWjYst2Y1KW0B9MvVJdgPAov2jN",
  "object": "video",
  "model": "z-image-spicy",
  "status": "queued",
  "progress": 0,
  "created_at": 1786714838
}
```

记下 `id`（与 `task_id` 相同），后面查询要用。

### 第二步：轮询结果

```bash
curl "$BASE_URL/v1/video/generations/task_uokagkWjYst2Y1KW0B9MvVJdgPAov2jN" \
  -H "Authorization: Bearer $API_KEY"
```

任务完成时（`status` 为 `SUCCESS`），`result_url` 就是产物地址：

```json
{
  "code": "success",
  "message": "",
  "data": {
    "task_id": "task_uokagkWjYst2Y1KW0B9MvVJdgPAov2jN",
    "status": "SUCCESS",
    "progress": "100%",
    "result_url": "https://.../result_00.png",
    "fail_reason": "",
    "submit_time": 1786714838,
    "finish_time": 1786714902
  }
}
```

图片和视频都放在 `result_url` 里，用扩展名区分（`.png` / `.mp4`）。

## 模型参数

参数直接写在请求体顶层，和 `model` 平级。

### z-image-spicy（文生图）

由文字提示词生成图片。

| 参数 | 类型 | 必填 | 默认 | 取值范围 | 说明 |
|---|---|---|---|---|---|
| `model` | string | ✅ | — | | 固定填 `z-image-spicy` |
| `prompt` | string | ✅ | — | 非空 | 图片描述 |
| `width` | integer | | 1024 | 256 – 1536 | 宽度（像素） |
| `height` | integer | | 1536 | 256 – 1536 | 高度（像素） |
| `prompt_extend` | boolean | | `true` | | 是否让模型智能改写提示词。开启通常出图更好，但会额外收费 |
| `seed` | integer / null | | `null` | | 随机种子。同样的种子 + 同样的参数可复现同一张图；`null` 或 `0` 表示随机 |

```bash
curl -X POST "$BASE_URL/v1/video/generations" \
  -H "Authorization: Bearer $API_KEY" -H "Content-Type: application/json" \
  -d '{
    "model": "z-image-spicy",
    "prompt": "一只三花猫趴在洒满阳光的窗台上，胶片质感",
    "width": 1024,
    "height": 1024,
    "prompt_extend": true
  }'
```

### qwen-image-edit-spicy（图生图）

按提示词编辑一张已有图片。输出尺寸由输入图决定，没有尺寸参数。

| 参数 | 类型 | 必填 | 默认 | 说明 |
|---|---|---|---|---|
| `model` | string | ✅ | — | 固定填 `qwen-image-edit-spicy` |
| `image` | string | ✅ | — | 输入图片。公网可访问的 `http`/`https` 链接，或 base64 编码的图片数据 |
| `prompt` | string | ✅ | — | 描述你想做的修改，例如「把背景换成雪山」 |
| `seed` | integer / null | | `null` | 随机种子 |

```bash
curl -X POST "$BASE_URL/v1/video/generations" \
  -H "Authorization: Bearer $API_KEY" -H "Content-Type: application/json" \
  -d '{
    "model": "qwen-image-edit-spicy",
    "image": "https://example.com/photo.png",
    "prompt": "把背景换成雪山，保持人物不变"
  }'
```

### wan2.2-i2v-spicy（图生视频）

把一张静态图变成一段视频。

| 参数 | 类型 | 必填 | 默认 | 取值范围 | 说明 |
|---|---|---|---|---|---|
| `model` | string | ✅ | — | | 固定填 `wan2.2-i2v-spicy` |
| `image` | string | ✅ | — | | 首帧图片，链接或 base64 |
| `prompt` | string | ✅ | — | 非空 | 描述镜头运动或画面变化 |
| `last_image` | string / null | | `null` | | 尾帧图片。给了的话模型会在首尾帧之间插值 |
| `duration` | integer | | `5` | **只能是 5 或 8** | 视频时长（秒） |
| `resolution` | string | | `"480p"` | **只能是 `480p` 或 `720p`** | 输出分辨率 |
| `prompt_extend` | boolean | | `true` | | 是否智能改写提示词 |
| `seed` | integer / null | | `null` | | 随机种子 |

```bash
curl -X POST "$BASE_URL/v1/video/generations" \
  -H "Authorization: Bearer $API_KEY" -H "Content-Type: application/json" \
  -d '{
    "model": "wan2.2-i2v-spicy",
    "image": "https://example.com/first-frame.png",
    "prompt": "镜头缓缓推进，人物微笑",
    "duration": 8,
    "resolution": "720p"
  }'
```

> `duration` 和 `resolution` 直接决定价格，取值范围是**严格校验**的。传 `duration: 9` 或 `resolution: "1080p"` 会被拒绝并返回 400，不会被悄悄改成合法值——避免你以为在生成 9 秒视频、实际付了别的钱。

## 查询任务

```bash
curl "$BASE_URL/v1/video/generations/{任务ID}" -H "Authorization: Bearer $API_KEY"
```

`data.status` 有六种取值：

| `status` | 含义 | 该做什么 |
|---|---|---|
| `NOT_START` | 刚提交，还没开始同步状态 | 继续等 |
| `SUBMITTED` | 已提交到生成服务 | 继续等 |
| `QUEUED` | 排队中 | 继续等 |
| `IN_PROGRESS` | 正在生成 | 继续等 |
| `SUCCESS` | 完成 | 从 `result_url` 取结果 |
| `FAILURE` | 失败 | 看 `fail_reason`，已扣费用自动退还 |

**轮询建议**：本站每 15 秒同步一次任务状态，查得比这更频繁不会更快拿到结果。

- 图片模型：每 5 秒查一次，通常 15–60 秒完成
- 视频模型：每 10 秒查一次，通常 1–3 分钟完成，8 秒 720p 更久

任务**不需要你持续查询也会继续推进**。可以提交后关掉程序，过几分钟再来取结果。

### 视频的 OpenAI 兼容格式

视频模型另有一个 OpenAI Video API 风格的查询接口，方便直接对接 OpenAI 客户端：

```bash
curl "$BASE_URL/v1/videos/{任务ID}" -H "Authorization: Bearer $API_KEY"
```

```json
{
  "id": "task_xxx",
  "object": "video",
  "model": "wan2.2-i2v-spicy",
  "status": "completed",
  "progress": 100,
  "created_at": 1786370025,
  "completed_at": 1786370124,
  "metadata": { "url": "https://.../result_00.mp4" }
}
```

这个接口**只支持视频模型**，图片模型请用上面的通用查询接口。

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
{ "code": "invalid_request", "message": "invalid value 9 for duration, allowed: 5, 8", "data": null }
```

| HTTP | `code` | 常见原因 | 怎么办 |
|---|---|---|---|
| 400 | `invalid_request` | 参数缺失或越界，`message` 写明了是哪个参数 | 按提示改参数 |
| 400 | `invalid_request` | 提示某参数「affect the price of this model and are not enabled」 | 你传了一个会影响价格但本站未开放的参数，去掉它 |
| 400 | `task_not_exist` | 查询用的任务 ID 不对 | 确认用的是提交时返回的 `id` |

常见的具体报错：

| `message` | 含义 |
|---|---|
| `prompt is required` | 缺 `prompt`，或 `prompt` 是空字符串 |
| `invalid value 9 for duration, allowed: 5, 8` | `duration` 只能是 5 或 8 |
| `invalid value "1080p" for resolution, allowed: 480p, 720p` | 分辨率只能是 480p 或 720p |
| `width must be <= 1536` | 尺寸超出 256 – 1536 |

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
| 503 | 该模型当前无可用服务 | 确认 `model` 拼写正确，或联系管理员 |

余额不足会在提交时明确报错，不会先生成再欠费。

## 完整示例

### Bash

```bash
#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:?}"
API_KEY="${API_KEY:?}"

# 1. 提交
resp=$(curl -sS -X POST "$BASE_URL/v1/video/generations" \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"z-image-spicy","prompt":"一只三花猫趴在窗台上","width":1024,"height":1024}')

task_id=$(echo "$resp" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
echo "任务已提交：$task_id"

# 2. 轮询
while true; do
  sleep 5
  data=$(curl -sS "$BASE_URL/v1/video/generations/$task_id" \
    -H "Authorization: Bearer $API_KEY" \
    | python3 -c 'import json,sys; d=json.load(sys.stdin)["data"]; print(d["status"], d.get("result_url",""), d.get("fail_reason",""), sep="\t")')
  status=$(echo "$data" | cut -f1)
  echo "状态：$status"
  case "$status" in
    SUCCESS) echo "结果：$(echo "$data" | cut -f2)"; break ;;
    FAILURE) echo "失败：$(echo "$data" | cut -f3)" >&2; exit 1 ;;
  esac
done
```

### Python

```python
import os
import time

import requests

BASE_URL = os.environ["BASE_URL"].rstrip("/")
API_KEY = os.environ["API_KEY"]
HEADERS = {"Authorization": f"Bearer {API_KEY}", "Content-Type": "application/json"}


def generate(payload: dict, poll_interval: int = 5, timeout: int = 900) -> str:
    """提交任务并等待完成，返回产物链接。"""
    resp = requests.post(f"{BASE_URL}/v1/video/generations",
                         headers=HEADERS, json=payload, timeout=60)
    resp.raise_for_status()
    task_id = resp.json()["id"]
    print(f"任务已提交：{task_id}")

    deadline = time.time() + timeout
    while time.time() < deadline:
        time.sleep(poll_interval)
        data = requests.get(f"{BASE_URL}/v1/video/generations/{task_id}",
                            headers=HEADERS, timeout=60).json()["data"]
        print(f"状态：{data['status']}")

        if data["status"] == "SUCCESS":
            return data["result_url"]
        if data["status"] == "FAILURE":
            raise RuntimeError(f"任务失败：{data.get('fail_reason')}")

    raise TimeoutError(f"任务 {task_id} 超过 {timeout} 秒仍未完成")


# 文生图
print(generate({
    "model": "z-image-spicy",
    "prompt": "一只三花猫趴在洒满阳光的窗台上",
    "width": 1024,
    "height": 1024,
}))

# 图生图
print(generate({
    "model": "qwen-image-edit-spicy",
    "image": "https://example.com/photo.png",
    "prompt": "把背景换成雪山",
}))

# 图生视频
print(generate({
    "model": "wan2.2-i2v-spicy",
    "image": "https://example.com/first-frame.png",
    "prompt": "镜头缓缓推进",
    "duration": 8,
    "resolution": "720p",
}, poll_interval=10))
```

## 注意事项

**产物链接有有效期**，本站不做长期保存。**拿到结果后请尽快下载到你自己的存储**，不要把链接直接存进数据库当长期地址用。

**超时**：任务超过 24 小时未完成会被自动判定为失败并退款。正常情况下图片一分钟内、视频几分钟内完成，如果长时间停在排队状态，通常是生成服务繁忙。

**并发**：没有单独的并发限制，但受令牌整体限流约束。批量任务建议控制并发，并对 429 做退避重试。

**内容审核**：提示词和产物都会经过审核，被拒绝的任务返回 `FAILURE` 并附带原因，费用自动退还。

**图片输入**：`image` 和 `last_image` 支持公网链接或 base64。用链接时确保生成服务能访问到——内网地址、需要登录才能打开的链接都会导致任务失败。base64 建议不超过 10 MB。

**参数位置**：所有参数写在请求体**顶层**，和 `model` 平级。也可以放在 `metadata` 对象里（两处同名时以 `metadata` 为准），但没有必要。
