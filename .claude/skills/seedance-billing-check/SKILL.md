---
name: seedance-billing-check
description: Run real paid API calls against a deployed new-api instance to verify that Doubao Seedance video billing matches Volcengine's official price table, then report per-case PASS/FAIL. Use this whenever the user wants to test, verify, validate, or sanity-check Seedance / doubao-video / 视频生成 billing, quota deduction, 计费, 扣费, 倍率, or token settlement on a live instance — including phrasings like "测一下 Seedance 2.0 收费对不对", "验证视频计费", "跑一下计费测试", "check if we're overcharging for video", or when they ask to confirm findings from a billing audit with actual requests. Also use it when someone reports a Seedance video charge that looks wrong and you need evidence from a live run.
---

# Seedance 计费实测

这个 skill 用真实付费调用验证部署实例的 Seedance 视频计费是否与火山方舟官方价格表一致。核心问题不是"能不能生成视频"，而是"扣的钱对不对"，所以整个流程围绕一件事展开：**拿到上游返回的真实 token 数，用官方口径独立复算一遍应扣额度，再和实例实际扣的钱对账。**

## 为什么需要真跑

Seedance 2.0 是按 token 后付费的：提交任务时只预扣一个小额占位（`模型倍率 ÷ 2 × QuotaPerUnit × 分组倍率`），任务成功后才用上游返回的 usage 做差额结算。这意味着计费逻辑里最容易出错的部分（用哪个 token 字段、分辨率倍率有没有生效、差额结算有没有触发）全都发生在异步轮询阶段，读代码只能推断，必须真跑一次才能确认。

代价是真花钱。官方单价下 5 秒视频约为：480p ¥2.3、720p ¥5.0、1080p ¥12.4、4K ¥25.3。所以脚本默认要求显式确认才会花钱，且优先跑最便宜的档位。

## 准备

凭据可以走环境变量，也可以用命令行参数覆盖（参数优先）。三项必需：

| 环境变量 | 命令行参数 | 用途 | 从哪来 |
|---|---|---|---|
| `NEW_API_BASE_URL` | `--base-url` | 实例地址，例如 `https://api.example.com` | 部署地址，不要带尾斜杠 |
| `NEW_API_KEY` | `--api-key` | 中转 API key（`sk-` 开头），用来调 `/v1/video/generations` | 后台「令牌」页面新建 |
| `NEW_API_ADMIN_TOKEN` | `--admin-token` | 管理员访问令牌（PAT），用来读 `/api/task/`、`/api/log/`、`/api/pricing` | 后台「个人设置 → 访问令牌」 |

可选：

| 环境变量 | 命令行参数 | 说明 |
|---|---|---|
| `SEEDANCE_MODEL` | `--model` | 被测模型，默认 `doubao-seedance-2-0-260128` |
| `SEEDANCE_TEST_VIDEO_URL` | `--video-url` | 含视频输入档位要用的公网 mp4；不给则用第一档产出的视频喂回去 |
| `NEW_API_USER_ID` | `--user-id` | 只有当实例的管理接口要求 `New-API-User` 头时才需要 |

日常跑用环境变量，命令行参数适合临时切换实例（比如先测预发再测生产）。但注意命令行里的密钥会出现在进程列表和 shell 历史里，共享机器上别这么传。

如果用户还没准备好这些，先问清楚再动手 — 用错 key（比如把管理员 PAT 当中转 key）会得到一个含义不明的 401，比直接问更浪费时间。

## 流程

### 第 1 步：先跑预检，不花钱

```bash
python3 .claude/skills/seedance-billing-check/scripts/seedance_billing_check.py --dry-run
```

预检做四件事，任何一条不过就停下来先修配置，别急着花钱：

1. **确认模型是按量计费**。读 `/api/pricing`，如果被测模型的 `quota_type` 是 `1`（配了固定价格），整个测试没有意义 — 固定价格会让 `PerCallBilling=true`，token 差额结算被整体跳过，退化成按次收费。这时应该先让用户把「模型固定价格」里的条目删掉，只保留模型倍率。
2. **反推配置的倍率是否合理**。用实例的 `usd_exchange_rate` 把官方基准价（人民币/百万 token）换算成应配的模型倍率，和实际配置比对。偏差过大说明倍率配错了档 — 倍率必须对应官方的「480p/720p + 无视频输入」基准档，因为代码里的分辨率倍率是 `实际单价 / 基准价` 的相对比值。
3. **列出每个待测档位的预期倍率和预期花费**，让用户知道这一轮要烧多少钱。
4. **确认余额够用**。

把预检输出念给用户听，特别是总花费，然后再问要不要继续。

### 第 2 步：实跑

```bash
python3 .claude/skills/seedance-billing-check/scripts/seedance_billing_check.py --yes
```

默认档位是标准集：`480p`、`720p`、`1080p`（均为无视频输入）加 `video-480p`（含视频输入）。想改：

```bash
--cases 480p,video-480p              # 最小集，约 ¥5，够验证 token 口径和折扣倍率
--cases 480p,720p,1080p,4k,video-480p,video-1080p   # 全量，¥60+
--model doubao-seedance-2-0-fast-260128 --cases 480p,video-480p   # 换 fast 型号
```

`video-*` 档位会自动排到无视频档位之后，因为它要用前一档产出的视频当输入（除非设了 `SEEDANCE_TEST_VIDEO_URL`）。

每个档位的执行链是：提交任务 → 轮询到 `completed` → 等异步差额结算落库 → 读管理端任务详情拿上游原始 usage → 读日志拿实际扣费和倍率 → 复算比对。单档位耗时通常 1-3 分钟，视频生成本身是慢的，`--poll-timeout` 默认 900 秒。

### 第 3 步：读结果

脚本输出一张逐档位的断言表，并把完整数据写进 `--out`（默认 `/tmp/seedance-billing-report.json`）。四个关键断言：

| 断言 | 检查什么 | 失败意味着 |
|---|---|---|
| `token_field` | 实际计费用的 token 数等于上游的 `completion_tokens` | 用了 `total_tokens` 就是超收，官方明确以 `completion_tokens` 为准 |
| `other_ratio` | 日志 `other` 里的 `video_input` 倍率等于官方价格表算出的比值 | 分辨率档或视频输入折扣没生效 |
| `quota_recompute` | `扣费额度 == floor(tokens × 模型倍率 × 分组倍率 × 倍率乘积)` | 计费公式实现和预期不符 |
| `quota_reconciled` | 用户额度实际减少量等于各档位扣费之和 | 有额度泄漏或重复扣费 |

`token_field` 失败时脚本会同时报出用 `completion_tokens` 和 `total_tokens` 分别复算的结果，直接指出实例用的是哪一个 — 这是判定超收的决定性证据。

另有两项参考信息（不作为断言，因为有合理误差）：

- **官方人民币对照**：用官方单价 × 实际 token 数算出官方应收人民币，和实例扣费按 `usd_exchange_rate` 折算的人民币比对。差异主要来自倍率配置里的加价，这是运营决策而非 bug，所以只报数字不判对错。
- **token 用量公式**：按官方公式 `(输入视频时长 + 输出视频时长) × 宽 × 高 × 帧率 / 1024` 估算，和上游实际返回值比对。含视频输入时官方有「最低 token 用量」下限，实际值高于估算是正常的。

## 汇报结论时

用户跑这个测试通常是为了给一份计费核对结论收尾，所以别只贴断言表。按这个顺序说：

1. **每档位一行结论**：档位、实际 token、实际扣费、预期扣费、通过/失败。
2. **失败项的定性**：是超收还是少收、幅度多少、根因在哪个环节（token 字段口径 / 倍率未生效 / 差额结算未触发）。这决定了修复的优先级。
3. **本轮花费**，以及余额变化是否对得上。
4. **哪些档位没测**，明确说出来。没跑的档位不能算验证通过 — 尤其 4K 和 fast 型号默认不在标准集里。

如果全部通过，直接说通过，不要为了显得严谨而制造疑虑。如果 `token_field` 失败，那就是实锤的系统性超收，值得单独强调并给出修复位置（`relay/channel/task/doubao/adaptor.go` 的 `ParseTaskResult` 里把 `TaskInfo.TotalTokens` 改成 `Usage.CompletionTokens`；不要动 `service/task_billing.go` 的 `RecalculateTaskQuotaByTokens`，它是所有 task adaptor 共用的通用结算路径）。

## 排障

**提交返回 400 `model_price_error` 或「模型倍率未配置」** — 模型没配倍率，且用户没开「接受未设置倍率的模型」。先去后台配倍率。

**提交返回 400 `invalid_api_platform`** — 该模型没路由到 doubao-video 任务渠道，通常是渠道没配这个模型名，或渠道被禁用。

**任务一直 `queued` 到超时** — 上游排队或渠道 key 无效。看管理端任务详情的 `fail_reason`，以及后端日志。这种情况不会扣钱（失败会退款），但要在报告里标为未验证而不是失败。

**任务成功但等不到差额结算** — 脚本会等到 `--settle-timeout`（默认 180 秒）。等不到有三种可能，按可能性排序：上游没返回 usage（则扣费停留在预扣的几分钱，这本身是个严重问题，要在报告里明确指出）；模型被配成了固定价格（预检应该已经拦住）；后台的任务轮询间隔太长。区分方法是看报告里的 `upstream_usage` 字段是否为空。

**含视频输入档位报上游拉不到视频** — 上一档产出的 URL 可能是本站代理地址而非上游 CDN 地址，或者已过期。这时改用 `SEEDANCE_TEST_VIDEO_URL` 提供一个稳定的公网 mp4。

## 官方价格表

脚本内置了官方单价用于复算，来源见 `references/official-prices.md`。官方调价后必须同步更新那份文件和脚本里的 `OFFICIAL_PRICES` — 如果实测出现全档位等比例偏差，先怀疑价格表过期，而不是代码有 bug。
