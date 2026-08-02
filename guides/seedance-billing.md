# Seedance 2.0 计费说明

面向运营与研发。这份文档说明火山方舟 Seedance 2.0 的官方计费口径、New API 的实现方式与两者的差异，以及一次真实付费实测的用例和结论。

## 官方计费方式

Seedance 2.0 是**按 token 后付费**的视频生成模型，不按次收费。

```
视频价格 = token 单价 × token 用量
token 用量 = (输入视频时长 + 输出视频时长) × 输出宽 × 输出高 × 输出帧率 / 1024
```

三条关键规则：

- **只对成功生成的视频计费**。因审核等原因失败不收费。
- **准确 token 用量以调用后返回的 `usage.completion_tokens` 为准**。官方在单价说明和最低用量说明中两次强调这一点。
- **含视频输入时有最低 token 用量下限**。估算低于下限按下限计价，下限与分辨率、宽高比、输出时长有关。因为最终以 `completion_tokens` 为准，按该字段计费即自动继承此规则。

单价按「输出分辨率档」和「输入是否含视频」两个维度区分（元/百万 token）：

| 模型 | 分辨率档 | 输入不含视频 | 输入包含视频 |
|---|---|---|---|
| `doubao-seedance-2.0` | 480p / 720p | 46.00 | 28.00 |
| `doubao-seedance-2.0` | 1080p | 51.00 | 31.00 |
| `doubao-seedance-2.0` | 4k | 26.00 | 16.00 |
| `doubao-seedance-2.0-fast` | 480p / 720p | 37.00 | 22.00 |
| `doubao-seedance-2.0-mini` | 480p / 720p | 23.00 | 14.00 |
| `doubao-seedance-2.5` | 480p / 720p | 70.00 | 42.00 |

2.0-fast 和 2.0-mini 不支持 1080p 和 4K，2.5 和 2.0-mini 只有 480p/720p 档。

注意官方公式里输入视频时长和输出视频时长是**相加**的，也就是输入视频的开销已经折进同一个 token 数。官方并不区分输入 token 和输出 token 的单价，「含视频输入更便宜」是通过降低整体单价实现的，不是通过分项计价。这一点决定了 New API 的实现方式。

## New API 的实现方式

New API 用「模型倍率 + 相对倍率」表达官方的绝对单价表。

### 价格表与相对倍率

绝对单价存在 [relay/channel/task/doubao/constants.go](../relay/channel/task/doubao/constants.go) 的 `videoPriceTable` 里，计费时取 `实际单价 ÷ 基准价` 作为一个名为 `video_input` 的 OtherRatio。基准档是 `{480p/720p, 不含视频输入}`。

以 2.0 为例：

| 档位 | 官方单价 | 相对基准价的倍率 |
|---|---|---|
| 480p / 720p，无视频输入 | 46.00 | 1.0000（基准） |
| 480p / 720p，含视频输入 | 28.00 | 0.6087 |
| 1080p，无视频输入 | 51.00 | 1.1087 |
| 1080p，含视频输入 | 31.00 | 0.6739 |
| 4k，无视频输入 | 26.00 | 0.5652 |
| 4k，含视频输入 | 16.00 | 0.3478 |

因此**后台配置的「模型倍率」必须对应基准档单价**，配错档会让所有分辨率整体偏移：

```
模型倍率 = 官方基准价(元/百万 token) ÷ usd_exchange_rate ÷ 2
```

除以 2 是因为倍率 1 对应 $0.002/1K tokens，即 $2/1M tokens（`QuotaPerUnit = 500000`）。汇率 7.0 时 2.0 应配 `46 ÷ 7 ÷ 2 = 3.2857`。

### 计费流程

视频生成是异步任务，扣费分两步：

1. **提交时预扣占位**。按 `模型倍率 ÷ 2 × QuotaPerUnit × 分组倍率` 扣一个小额（[relay/helper/price.go:231](../relay/helper/price.go#L231)），此时还不知道实际 token 数。
2. **任务成功后差额结算**。用上游返回的 usage 重算实际应扣，与预扣额度补扣或退还（[service/task_billing.go](../service/task_billing.go) 的 `RecalculateTaskQuotaByTokens`）：

```
实际扣费 = floor(tokens × 模型倍率 × 分组倍率 × OtherRatios 乘积)
```

任务失败时预扣额度全额退还，与官方「失败不收费」一致。

### 与官方的异同

**相同**：计费维度（分辨率档 × 是否含视频输入）、失败不收费、以上游返回的 usage 为准而非本地估算 token（因而自动继承官方的最低 token 用量下限）。

**不同**，三点需要注意：

1. **绝对价 vs 相对倍率**。官方是每档一个绝对单价，New API 是基准价 × 相对倍率。结果等价，但前提是模型倍率配在基准档上。配错档会让所有分辨率同比例偏移，且不会报错。
2. **加价由倍率承担**。实例配的模型倍率与官方换算值的差就是加价（或折扣）率，全档位统一。这是运营决策，不是计费 bug。
3. **配了「模型固定价格」会退化成按次收费**。固定价格会让 `PerCallBilling=true`，token 差额结算被整体跳过，无论生成什么分辨率都收同一个价。Seedance 只能配模型倍率，不能配固定价格。

## 实测用例与结论

2026-08-02 在 staging 实例对 `doubao-seedance-2-0-260128` 做了真实付费调用，渠道为方舟-豆包（火山官方直连），模型倍率 3.0，分组倍率 1.0，汇率 7.0。全部为 16:9、5 秒、24fps。

| 用例 | 上游 `completion_tokens` | `video_input` 倍率 | 复算应扣 | 实际扣费 | 结果 |
|---|---|---|---|---|---|
| 480p 无视频输入 | 50,638 | — | 151,914 | 151,914 | 通过 |
| 720p 无视频输入 | 108,900 | — | 326,700 | 326,700 | 通过 |
| 1080p 无视频输入 | 245,025 | 1.1087 | 814,974 | 814,974 | 通过 |
| 480p 含视频输入 | 100,858 | 0.6087 | 184,175 | 184,175 | 通过 |

四档零误差。已验证的结论：

- **差额结算链路正常**。实际扣费远高于预扣占位，说明异步结算确实触发。
- **分辨率倍率生效**。1080p 的 1.1087 正是官方 51/46。
- **视频输入折扣生效**。0.6087 正是官方 28/46。
- **额度对账无泄漏**。用户余额减少量精确等于各档扣费之和，无重复扣费。
- **本轮相对官方口径便宜 8.7%**，四档偏差完全一致。来自模型倍率配 3.0 而非换算值 3.2857，属定价决策而非计费问题。

### 一个已知的潜伏风险

**结算读的是 `total_tokens` 而非官方要求的 `completion_tokens`**。

[relay/channel/task/doubao/adaptor.go](../relay/channel/task/doubao/adaptor.go) 两个字段都解析了，但结算路径 [service/task_polling.go:656](../service/task_polling.go#L656) 传给 `RecalculateTaskQuotaByTokens` 的是 `TotalTokens`。

实测四档都通过，是因为火山对这些请求返回的两个值恰好完全相等（`50638/50638`、`108900/108900`、`245025/245025`、`100858/100858`），**实测无法区分实例读的是哪个字段**，该结论来自代码静态确认而非实测。

目前没有造成超收。但 usage 结构里还有 `ToolUsage` 字段，一旦某类请求的 `total_tokens` 把工具或输入开销算进去，会立刻变成系统性超收且无任何告警。

修复方式：在 adaptor 的 `ParseTaskResult` 里把 `taskResult.TotalTokens` 改为取 `resTask.Usage.CompletionTokens`。**不要改 `service/task_billing.go` 的通用结算路径**，那是所有 task adaptor 共用的。

### 未覆盖的范围

以下均未实测，不能视为已验证：

- **4K 档**（单次约 ¥25）
- **2.0-fast / 2.0-mini / 2.5 / 1.5-pro** 型号。其中 1.5-pro 按输出视频有声无声区分定价，另有 draft 模式的 token 折算系数（无声 0.7、有声 0.6），计费逻辑与 2.0 不同
- 非 16:9 宽高比、非 5 秒时长、非 24fps

## 复测方式

项目内置了 skill `seedance-billing-check`，对已部署实例发真实付费调用并自动对账：

```bash
# 预检，不花钱
python3 .claude/skills/seedance-billing-check/scripts/seedance_billing_check.py --dry-run

# 实跑
python3 .claude/skills/seedance-billing-check/scripts/seedance_billing_check.py --yes
```

需要三个环境变量：`NEW_API_BASE_URL`、`NEW_API_KEY`（中转令牌）、`NEW_API_ADMIN_TOKEN`（管理员访问令牌）。默认档位 `480p,720p,1080p,video-480p` 一轮约 ¥20。详见 [.claude/skills/seedance-billing-check/SKILL.md](../.claude/skills/seedance-billing-check/SKILL.md)。

**注意**：该脚本的 `token_field` 断言在上游返回两个 token 字段相等时不具备区分能力，通过不等于代码读对了字段。

## 参考文档

- 火山方舟《模型价格》— 视频生成模型章节：https://docs.volcengine.com/docs/82379/1544106
  页面为客户端渲染，抓取工具只能拿到导航壳，核对时需在浏览器打开或导出 PDF。价格核对日期 2026-07-31。
- 官方价格表摘录与计费规则整理：[.claude/skills/seedance-billing-check/references/official-prices.md](../.claude/skills/seedance-billing-check/references/official-prices.md)
- 计费表达式系统设计文档：[pkg/billingexpr/expr.md](../pkg/billingexpr/expr.md)

官方调价后需同步更新三处：`relay/channel/task/doubao/constants.go` 的 `videoPriceTable`、skill 脚本里的 `OFFICIAL_PRICES`、以及 `references/official-prices.md`。如果实测出现全档位等比例偏差，先怀疑价格表过期，而不是代码有 bug。
