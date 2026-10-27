---
name: mulerouter-smoke-test
description: Smoke-test a new-api instance's MuleRouter channel (a Task Plugin channel bound to the `mulerouter` plugin) end to end — submit, poll, verify the response shape, the artifacts, the billing multipliers, the charged quota, and that callers never see MuleRouter for the spicy image and video models (z-image-spicy, qwen-image-edit-spicy, wan2.2-i2v-spicy). Use this whenever the user wants to test, verify, validate or debug a MuleRouter / carrothub / spicy channel on a running instance — including phrasings like "测一下 MuleRouter 渠道", "验证 carrothub 配置对不对", "跑一下冒烟测试", "调用本地接口测试刚创建的模型", "check the image/video generation models work", or when they have just created a MuleRouter channel and want to confirm it relays correctly. Also use it when a MuleRouter request fails, returns an unexpected status, or charges an amount that looks wrong, and you need evidence from a live run.
---

# MuleRouter 渠道联调

这个 skill 验证一个跑着的 new-api 实例的 MuleRouter 渠道（绑定 `mulerouter` 插件的任务插件渠道）是否真的能用。它回答三个问题，重要性递减：

1. **护栏还在吗** —— 未声明的成本字段、越界的计费乘数、未登记的模型，是否都在扣费之前被拒；
2. **链路通不通** —— 提交能不能拿到本站任务 ID，轮询能不能推到 `SUCCESS`，产物能不能下载；
3. **钱扣得对不对** —— 实际扣费是否等于 `固定价 × 倍率 × 分组倍率`。

第 1 项是免费的（那些请求本来就应该在预扣费之前被拒），所以流程刻意把它排在花钱之前 —— 护栏破了就没必要往下跑了，而且这一步顺带证明了"被拒的请求不扣钱"。

## 为什么要真跑

MuleRouter 渠道的正确性有一半不在 new-api 里：上游响应体的字段名、失败态 `error` 对象的形状、产物数组的键名，都是文档里读来的假设。假设错了的表现很隐蔽 —— 提交会成功，然后任务卡在未知状态，每轮轮询报错，连续失败到 `TASK_POLL_MAX_FAILURES` 次（默认 20 次，约 5 分钟）后被判失败并退款，而上游可能照样生成、照样收我们的钱。单测抓不到这个，只有真发一次请求才知道。

代价很低：默认三个用例合计约 $0.15（z-image $0.013、qwen-edit $0.04、wan-480p $0.10）。

## 准备

| 环境变量 | 命令行参数 | 用途 | 从哪来 |
|---|---|---|---|
| `NEW_API_BASE_URL` | `--base-url` | 实例地址，默认 `http://localhost:3000` | 本地开发通常不用改 |
| `NEW_API_KEY` | `--api-key` | 中转令牌（`sk-` 开头），决定请求落到哪个分组、哪个渠道 | 后台「令牌」页面新建 |
| `NEW_API_ACCESS_TOKEN` | `--access-token` | 令牌主人的访问令牌，用来读这个账号的 `/api/pricing`、`/api/log/self`、`/api/user/self` | 后台「个人设置 → 访问令牌」 |
| `NEW_API_USER_ID` | `--user-id` | 令牌主人的用户 id，随访问令牌放进 `New-API-User` 头 | 登录方式下自动取 |
| `NEW_API_USERNAME` / `NEW_API_PASSWORD` | `--username` / `--password` | 没有访问令牌时登录取 JWT 代替 | 测试账号的用户名和密码 |
| `MULEROUTER_TEST_GROUP` | `--group` | 请求应落到的分组 | 默认取账号的用户分组；令牌自己设了分组时必须给 |
| `MULEROUTER_TEST_IMAGE_URL` | `--image-url` | 图生图/图生视频的输入图 | 可选，不给就用 z-image 用例的产出 |

**选中要测的渠道靠令牌和分组**：渠道按分组划给用户，所以测哪个渠道，就用那个分组里账号的中转令牌，访问令牌（或用户名密码）也必须是**同一个账号**的 —— 价格和分组倍率按账号算，消费日志按令牌名筛，余额对账看的也是这个账号。测插件渠道时，用测试分组里的测试账号；staging 的测试账号见主仓库 `.local-tests/staging-test-accounts.md`（只在本地，密钥在仓库根目录 `.env`）。

如果这些还没准备好，先问清楚再动手 —— 把访问令牌当中转令牌用会得到一个含义不明的 401，比直接问更浪费时间。

前置条件是渠道已经建好并且模型配了**固定价格**（不是倍率）。异步任务按次计费，配成倍率会走 token 结算路径，本 skill 的金额断言就没有意义了 —— 预检会直接拦住这种情况。

## 流程

脚本走用户指南里的入口：提交 `POST /v1/tasks/mulerouter`，查询 `GET /v1/tasks/{id}`，产物 `GET /v1/tasks/{id}/artifacts` 和其中的 `content_url`。`POST /v1/video/generations` 到不了插件渠道，别用它测。

### 第 1 步：免费检查

```bash
python3 .claude/skills/mulerouter-smoke-test/scripts/mulerouter_smoke_test.py --dry-run
```

这一步跑预检和护栏检查，一分钱不花：

- **预检**读该账号的 `/api/pricing`，确认每个待测模型在目标分组里有渠道、配了固定价格、价格不低于上游成本价，读分组倍率和当前余额，列出本轮预算；
- **护栏检查**向 `/v1/tasks/mulerouter` 发六个应当被拒的请求（`metadata` 里未声明的 `n`、枚举外的 `duration`、包装过的巨大整数、`metadata` 里越界的 `width`、未登记的模型、无令牌），然后**比对护栏前后的余额**——没变才说明这些请求确实死在扣费之前。

把预算念给用户听，再问要不要继续。任何护栏失败都先停下来查，别急着花钱。

### 第 2 步：实跑

```bash
python3 .claude/skills/mulerouter-smoke-test/scripts/mulerouter_smoke_test.py --yes
```

默认用例是 `z-image,qwen-edit,wan-480p`，覆盖三个模型各一次。换用例：

```bash
--cases z-image                        # 最便宜的单发，$0.013，够验证链路和护栏
--cases z-image,z-image-noextend       # 验证 prompt_extend 倍率真的在起作用
--cases z-image,qwen-edit,wan-480p,wan-720p-8s   # 加上最贵档，$0.47
```

用例照用户指南示例的写法发：Vendor parameter（`width`、`height`、`prompt_extend`、`resolution`）放在 `metadata` 下，`prompt`、`image`、`duration` 留在顶层。两个入口都认顶层和 `metadata` 两种写法，新加用例照示例写。

需要输入图的用例（`qwen-edit`、`wan-*`）会自动排到后面，用前面 z-image 产物的 `content_url` 当输入。这个地址在本站上，生成服务必须访问得到：测 localhost 这类外网到不了的实例时，传 `--image-url` 给一张公网图。

单个用例的链路是：提交 → 断言 200 和本站任务 ID → 轮询到终态 → 取产物并用 GET 探一次 `content_url`（带 `Range: bytes=0-0`，只取 1 个字节） → 读消费日志 → 复算扣费。视频用例慢，`--poll-timeout` 默认 600 秒。

**`POST /v1/videos` 脚本不覆盖**，验证 OpenAI 视频入口时手工发一次标准写法，输入图用 z-image 用例的产物：

```bash
curl -sS -D - -X POST "$NEW_API_BASE_URL/v1/videos" -H "Authorization: Bearer $NEW_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"wan2.2-i2v-spicy","prompt":"镜头缓缓推进，人物微笑","input_reference":"<图片 URL>","seconds":"5","size":"1280x720"}'
```

期望：200，响应头 `X-New-Api-Other-Ratios: {"resolution":2,"seconds":5}`（`size` 按短边映射成 `720p`）；`GET /v1/videos/{id}` 轮询到 `completed`；`GET /v1/videos/{id}/content` 返回 `video/mp4`；消费日志扣 `固定价 × 5 × 2`，action 为 `image_to_video`。约 $0.20。同一请求里 `seconds` 和 `duration` 取值不同、或带 `mode` 这类上游不认的字段，会在扣费前返回 400 —— 这两个请求免费，适合用来确认实例跑的是哪个版本的插件。

### 第 3 步：读结果

每个用例十项断言，完整数据写进 `--out`（默认 `/tmp/mulerouter-smoke-report.json`）：

| 断言 | 检查什么 | 失败意味着 |
|---|---|---|
| `submit_accepted` | 提交返回 200 | 路由、渠道选择或上游鉴权有问题 |
| `model_name_is_public` | 响应里的 `model` 是客户端传的短名 | 内部三段式名字泄漏给了客户端 |
| `task_id_masked` | 返回的 `id` 是 `task_` 开头的本站 ID | 上游 UUID 泄漏给了客户端 |
| `billing_ratios` | 提交响应头 `X-New-Api-Other-Ratios` 等于该用例期望的倍率 | 计费变量没生效或配错了 |
| `task_completed` | 轮询到 `SUCCESS` | 上游失败，或响应体形状和假设不符 |
| `artifact_listed` | `/artifacts` 列出一个产物：图片模型是 `image-1`（`image`），视频模型是 `video`（`video`） | 产物解析和假设不符（插件的 `listArtifacts`） |
| `artifact_content` | 不带令牌 GET `content_url`（`Range: bytes=0-0`）返回 200 或 206，`Content-Type` 是 `image/*` 或 `video/*`。不用 HEAD：调用方都用 GET 下载，上游签名链接只对 GET 有效 | 产物下载链路不通（插件的 `buildContentRequest`，或上游产物链接失效） |
| `quota_charged` | 扣费 == `固定价 × 倍率 × 分组倍率 × QuotaPerUnit` | 计费链路和预期不符 |
| `group_routed` | 消费日志的分组 == 目标分组 | 请求没落到目标分组的渠道上，测的不是你想测的渠道 |
| `vendor_hidden` | 调用方能看到的五个响应里没有 `carrothub` 和 `muleusercontent`，见下文 | 调用方能看出背后是 MuleRouter |

#### `vendor_hidden`：调用方看不到 MuleRouter

GLOSSARY 规定调用方只见 Public model name，见不到 MuleRouter model name 和 MuleRouter 的结果 URL。这两样都带着 `carrothub`（MuleRouter model name 和结果 URL 的路径）或 `muleusercontent`（结果 URL 的域名），所以查这两个词就够了。

**范围**：用例拿到任务 ID 后，不论终态，都读这五个响应：

| 响应 | 怎么读 |
|---|---|
| `GET /v1/videos/{id}` | 中转令牌；图片任务这里返回报错，报错体照样查 |
| `GET /v1/tasks/{id}` | 中转令牌 |
| `GET /v1/tasks/{id}/artifacts` | 中转令牌 |
| 控制台任务列表 `GET /api/task/self?task_id=` | 访问令牌 |
| 使用日志 `GET /api/log/self` | 沿用 `quota_charged` 找到的那条消费日志 |

`GET /v1/video/generations/{id}`、`platform: "mulerouter"` 和提交地址里的插件 key 不在范围内，见 codzen-ai/new-api#40。插件 key 是 `mulerouter`，不含这两个词，所以 `platform` 字段不会触发失败。

**判定**：任何字段的字符串值里出现两个词之一（不分大小写）就失败。值本身是 JSON 字符串的（使用日志的 `other`）先拆开再查。

**输出**：失败时 detail 逐条列出 `<响应> <字段路径> 含 <词>`，比如 `控制台任务列表 GET /api/task/self data.items[0].data.videos[0] 含 muleusercontent`。五个响应的完整内容写在报告每个用例的 `caller_views` 里。

**花费**：只发 GET，不提交任务，不额外花钱；`--dry-run` 不跑这项。

`quota_charged` 的复算刻意分两步取整（先把按次价折成基础额度，再乘倍率），因为 `relay_task.go` 就是这么算的，合成一次乘法会差 1 个额度。断言留了 ±1 容差吸收浮点误差 —— 数量级错误照样会被抓到。

最后还有一项跨用例对账：用户额度的实际减少量是否等于各用例扣费之和。对不上说明有额度泄漏或重复扣费。

## 汇报结论时

按这个顺序说，别只贴断言表：

1. **护栏结论**先说 —— 这是安全性结论，比功能可用性更重要。全过就一句话带过。
2. **每个用例一行**：用例、任务 ID、终态、实际扣费 vs 预期扣费、通过/失败。
3. **失败项的定性**：是链路不通、形状不符，还是金额不对。这三类的修复位置完全不同：链路问题看渠道配置和端点表，形状问题看响应解析，金额问题看计费声明和模型固定价格。位置见下面「修复位置」。
4. **本轮花费**和余额对账结果。
5. **哪些用例没跑** —— 明确说出来。没跑的模型不算验证通过，尤其默认用例只覆盖每个模型一档。

全过就直接说通过，不要为了显得严谨而制造疑虑。

## 修复位置

排障里的「端点表」「计费声明」「响应解析」都在 `plugins/tasks/mulerouter/plugin.js` 里：

| 概念 | 位置 |
|---|---|
| 端点表 | `ENDPOINTS` 能力表（键即 Public model name；每个端点的 `mulerouterModel` 是它对应的 MuleRouter model name `{vendor}/{model}/{action}`，另有 `produces`、`action`） |
| 计费声明 | 同一张表里每个端点的 `fields`：`values` 是取值到倍率的映射，`ratio` 是宿主收到的倍率名，`default` 是缺省时的计费值 |
| 成本字段黑名单 | `COST_FIELDS` |
| `/v1/videos` 解码 | `OPENAI_VIDEO_FIELDS`（`seconds`/`size`/`input_reference` 的映射），视频端点的 `params`（允许透传的字段） |
| 响应解析 | `parseSubmitResponse` / `parseTaskResult` / `TASK_STATUSES` / `failureReason` |
| 产物 | `listArtifacts` / `buildContentRequest` |

这些是代码，改完要重新部署（见 [ADR-0001](../../../docs/adr/0001-mulerouter-endpoints-live-in-a-task-plugin.md)）。渠道上能改的只有模型列表、Base URL 和密钥，「模型重定向」留空（见 [ADR-0002](../../../docs/adr/0002-mulerouter-plugin-declares-public-model-names.md)）；价格在模型固定价格里。

## 排障

**提交 400 `model_price_error` / 「模型倍率未配置」** —— 模型没配固定价格。预检本该拦住，说明跑的时候跳过了预检。

**提交 503「无可用渠道」** —— 先确认提交地址是 `/v1/tasks/mulerouter`：`/v1/video/generations` 到不了插件渠道。地址对的话，是目标分组里没有绑定 `mulerouter` 插件的渠道提供这个名字（报错里带着分组名，先确认分组对不对），或渠道的模型列表里没有这个名字。客户端用的是 Public model name（`z-image-spicy`），渠道的模型列表只填这个名字，不配「模型重定向」。

**提交报 `this model is not available on this channel`** —— 名字在渠道模型列表里，但插件不认它：重定向后的名字不是 `ENDPOINTS` 的键。最常见的原因是渠道还配着到三段式 `carrothub/...` 的「模型重定向」，删掉即可；配了别名重定向的，检查目标是否和 `ENDPOINTS` 的键一字不差。

**提交 400 且报某个字段"影响成本但未声明"** —— 请求里（顶层或 `metadata` 里）带了成本字段黑名单里的键（`n`/`duration`/`resolution`/`size`/`width`/`height` 等）而该 endpoint 没声明它。这是设计行为不是 bug：在该端点的 `fields` 里给它加一条带取值范围的声明，或者别传这个字段。

**`/v1/videos` 提交 400「not supported by this model」或「give different values」** —— 前者是请求带了端点 `fields` 和 `params` 之外的字段（如 `mode`），后者是同一参数两种写法取值不同（`seconds` 和 `duration`、`size` 和 `resolution`、顶层和 `metadata`）。都是设计行为。

**提交成功但轮询一直不结束** —— 先看后端日志有没有 `unknown task status`。有的话说明上游状态值超出了插件认的两套状态词（见管理员指南 `guides/admin/mulerouter-channel-setup.md` 的排障），需要在 `TASK_STATUSES` 里补映射；连续失败到 `TASK_POLL_MAX_FAILURES` 次后任务会被判失败并退款。没有这条日志就是上游确实在排队。

**`task_completed` 失败，原因是 `the task completed without producing any image, video or audio`** —— 上游报告完成但没有给产物，插件按设计判失败、宿主退款。把轮询的原始响应贴出来：多半是产物数组的键名和文档不符，对着改 `resultURL` / `resultImageURLs`。

**`task_completed` 失败，原因只有 `task failed`** —— 上游失败响应里 `task_info.error` 和顶层 `error` 都没有可读的错误对象。把原始响应贴出来，对着改 `failureReason`。

**提交返回 `invalid_upstream_response`（没有 task id）** —— 上游 202 响应体的形状和文档不符。把原始响应贴出来，对着改 `plugin.js` 的 `parseSubmitResponse`。

**`billing_ratios` 拿到空表** —— 该用例期望的倍率本来就是空（`qwen-edit` 没有计费变量），这是正常的；如果 z-image 或 wan 拿到空表，说明这个端点的 `fields` 里没有带 `ratio` 的字段。

**额度对不上但每个用例都 PASS** —— 测试期间有其他请求在消费同一个账号。换一个专用账号重跑。

**`group_routed` 失败** —— 令牌自己设了分组（或是 `auto` 分组），请求没走 `--group` 指的分组。换一个分组就是目标分组的令牌重跑；扣费结论对那个分组不成立。

**`vendor_hidden` 失败** —— 按 detail 里的字段找来源：
- `GET /v1/videos/{id}` 的 `metadata.*`：插件 `openai_video.render` 又带出了结果 URL。
- `properties.upstream_model_name`（任务列表）或 `other.upstream_model_name`（使用日志）含 `carrothub`：渠道配了到 MuleRouter model name 的 `model_mapping`。删掉它，原因见 [ADR-0002](../../../docs/adr/0002-mulerouter-plugin-declares-public-model-names.md)。
- 控制台任务列表的 `data.*`：任务的 `data` 是宿主存下的上游原始响应。控制台列表查询时不读这一列（`model/task.go` 里 `TaskGetAllUserTask` 的 `Omit("data")`），正常应为 `null`；出现了说明宿主改了这条查询，插件管不到，把证据交给用户决定怎么处理。`GET /v1/video/generations/{id}` 会返回完整的 `data`，它不在本检查范围内，见 codzen-ai/new-api#40。

## 上游价目

脚本内置的 `UPSTREAM_BASE_PRICE` 是上游基准单位价（图片按张 $0.04 / $0.013，视频按秒 $0.02），只用于预检时警告"配置价低于成本价"，不参与断言 —— 加价多少是运营决策。上游调价后同步更新那个表，原方案文档 `specifications/implemented/mulerouter-async-task-gateway.md` 第 9 节有价目来源（该文档的设计已被 ADR-0001 取代，价目仍可参考）。
