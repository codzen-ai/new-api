---
name: mulerouter-smoke-test
description: Smoke-test a new-api instance's MuleRouter channel end to end — submit, poll, verify the response shape, the artifacts, the billing multipliers and the charged quota for the spicy image and video models (z-image-spicy, qwen-image-edit-spicy, wan2.2-i2v-spicy). Use this whenever the user wants to test, verify, validate or debug a MuleRouter / carrothub / spicy channel on a running instance — including phrasings like "测一下 MuleRouter 渠道", "验证 carrothub 配置对不对", "跑一下冒烟测试", "调用本地接口测试刚创建的模型", "check the image/video generation models work", or when they have just created a MuleRouter channel and want to confirm it relays correctly. Also use it when a MuleRouter request fails, returns an unexpected status, or charges an amount that looks wrong, and you need evidence from a live run.
---

# MuleRouter 渠道联调

这个 skill 验证一个跑着的 new-api 实例的 MuleRouter 渠道是否真的能用。它回答三个问题，重要性递减：

1. **护栏还在吗** —— 未声明的成本字段、越界的计费乘数、未登记的模型，是否都在扣费之前被拒；
   厂商原生路径是否确实对客户端不可达；
2. **链路通不通** —— 提交能不能拿到本站任务 ID，轮询能不能推到 `completed`，产物 URL 有没有；
3. **钱扣得对不对** —— 实际扣费是否等于 `固定价 × 倍率 × 分组倍率`。

第 1 项是免费的（那些请求本来就应该在预扣费之前被拒），所以流程刻意把它排在花钱之前 —— 护栏破了就没必要往下跑了，而且这一步顺带证明了"被拒的请求不扣钱"。

## 为什么要真跑

MuleRouter 渠道的正确性有一半不在 new-api 里：上游响应体的字段名、失败态 `error` 对象的形状、产物数组的键名，都是文档里读来的假设。假设错了的表现很隐蔽 —— 提交会成功，然后任务卡在未知状态，每轮轮询报错，直到 24 小时超时才被判失败并退款。单测抓不到这个，只有真发一次请求才知道。

代价很低：默认三个用例合计约 $0.15（z-image $0.013、qwen-edit $0.04、wan-480p $0.10）。

## 准备

| 环境变量 | 命令行参数 | 用途 | 从哪来 |
|---|---|---|---|
| `NEW_API_BASE_URL` | `--base-url` | 实例地址，默认 `http://localhost:3000` | 本地开发通常不用改 |
| `NEW_API_KEY` | `--api-key` | 中转令牌（`sk-` 开头） | 后台「令牌」页面新建 |
| `NEW_API_ADMIN_TOKEN` | `--admin-token` | 管理员访问令牌，用来读 `/api/pricing`、`/api/log/`、`/api/user/self` | 后台「个人设置 → 访问令牌」 |
| `MULEROUTER_TEST_IMAGE_URL` | `--image-url` | 图生图/图生视频的输入图 | 可选，不给就用 z-image 用例的产出 |

如果这些还没准备好，先问清楚再动手 —— 把管理员令牌当中转令牌用会得到一个含义不明的 401，比直接问更浪费时间。

前置条件是渠道已经建好并且模型配了**固定价格**（不是倍率）。异步任务按次计费，配成倍率会走 token 结算路径，本 skill 的金额断言就没有意义了 —— 预检会直接拦住这种情况。

## 流程

### 第 1 步：免费检查

```bash
python3 .claude/skills/mulerouter-smoke-test/scripts/mulerouter_smoke_test.py --dry-run
```

这一步跑预检和护栏检查，一分钱不花：

- **预检**读 `/api/pricing` 确认每个待测模型都配了固定价格、价格不低于上游成本价，读分组倍率和当前余额，列出本轮预算；
- **护栏检查**发六个应当被拒的请求（未声明的 `n`、枚举外的 `duration`、包装过的巨大整数、越界的 `width`、未登记的模型、无令牌），然后**比对护栏前后的余额**——没变才说明这些请求确实死在扣费之前。

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

需要输入图的用例（`qwen-edit`、`wan-*`）会自动排到后面，用前面 z-image 的产出当输入。想固定输入图就传 `--image-url`。

单个用例的链路是：提交 → 断言 202 和本站任务 ID → 轮询到终态 → 读消费日志 → 复算扣费。视频用例慢，`--poll-timeout` 默认 600 秒。

### 第 3 步：读结果

每个用例六项断言，完整数据写进 `--out`（默认 `/tmp/mulerouter-smoke-report.json`）：

| 断言 | 检查什么 | 失败意味着 |
|---|---|---|
| `submit_accepted` | 提交返回 200 | 路由、渠道选择或上游鉴权有问题 |
| `model_name_is_public` | 响应里的 `model` 是客户端传的短名 | 内部三段式名字泄漏给了客户端 |
| `task_id_masked` | 返回的 `id` 是 `task_` 开头的本站 ID | 上游 UUID 泄漏给了客户端 |
| `billing_ratios` | 提交响应头 `X-New-Api-Other-Ratios` 等于该用例期望的倍率 | 计费变量没生效或配错了 |
| `task_completed` | 轮询到 `SUCCESS` | 上游失败，或响应体形状和假设不符 |
| `artifact_url` / `artifact_type` | `result_url` 是可用 URL 且扩展名符合模型的产物类型 | 产物解析和假设不符 |
| `quota_charged` | 扣费 == `固定价 × 倍率 × 分组倍率 × QuotaPerUnit` | 计费链路和预期不符 |

`quota_charged` 的复算刻意分两步取整（先把按次价折成基础额度，再乘倍率），因为 `relay_task.go` 就是这么算的，合成一次乘法会差 1 个额度。断言留了 ±1 容差吸收浮点误差 —— 数量级错误照样会被抓到。

最后还有一项跨用例对账：用户额度的实际减少量是否等于各用例扣费之和。对不上说明有额度泄漏或重复扣费。

## 汇报结论时

按这个顺序说，别只贴断言表：

1. **护栏结论**先说 —— 这是安全性结论，比功能可用性更重要。全过就一句话带过。
2. **每个用例一行**：用例、任务 ID、终态、实际扣费 vs 预期扣费、通过/失败。
3. **失败项的定性**：是链路不通、形状不符，还是金额不对。这三类的修复位置完全不同：链路问题看渠道配置和路由表，形状问题改 `relay/channel/task/mulerouter/dto.go`，金额问题查渠道的 `billing_vars` 和模型固定价格。
4. **本轮花费**和余额对账结果。
5. **哪些用例没跑** —— 明确说出来。没跑的模型不算验证通过，尤其默认用例只覆盖每个模型一档。

全过就直接说通过，不要为了显得严谨而制造疑虑。

## 排障

**提交 400 `model_price_error` / 「模型倍率未配置」** —— 模型没配固定价格。预检本该拦住，说明跑的时候跳过了预检。

**提交 503「无可用渠道」** —— 渠道的模型列表里没有这个名字。客户端用的是短名（`z-image-spicy`），渠道需要同时配好模型列表和「模型重定向」把短名映射到三段式内部名 `{vendor}/{model}/{action}`。

**提交 404 `model_not_found`** —— 名字在渠道模型列表里，但重定向后的三段式名字不在路由表里。检查两者是否对齐。

**提交 400 且报某个字段"影响成本但未声明"** —— 请求里带了成本字段黑名单里的键（`n`/`duration`/`resolution`/`size`/`width`/`height` 等）而路由的 `billing_vars` 没声明它。这是设计行为不是 bug：给它加一条带上界的声明，或者别传这个字段。

**提交成功但轮询一直 `pending`** —— 先看后端日志有没有 `unknown task status`。有的话说明上游状态值超出了 `pending/processing/completed/failed` 四种，需要在 `ParseTaskResult` 里补映射。没有的话就是上游确实在排队。

**提交返回 `invalid_upstream_response`（没有 task id）** —— 上游 202 响应体的形状和文档不符。把原始响应贴出来，对着改 `relay/channel/task/mulerouter/dto.go` 的 `taskResponse`。

**`billing_ratios` 拿到空表** —— 该用例期望的倍率本来就是空（`qwen-edit` 没有计费变量），这是正常的；如果 z-image 或 wan 拿到空表，说明 `billing_vars` 没配。

**额度对不上但每个用例都 PASS** —— 测试期间有其他请求在消费同一个账号。换一个专用账号重跑。

## 上游价目

脚本内置的 `UPSTREAM_BASE_PRICE` 是上游成本价（$0.04 / $0.013 / $0.10 基准档），只用于预检时警告"配置价低于成本价"，不参与断言 —— 加价多少是运营决策。上游调价后同步更新那个表，方案文档 `specifications/mulerouter-async-task-gateway.md` 第 9 节有价目来源。
