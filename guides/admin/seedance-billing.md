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

复现方式见下方[「怎么自己跑一遍」](#怎么自己跑一遍)，四个用例的原始请求与原始响应都在那一节。

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

## 怎么自己跑一遍

这一节是上面那张结论表的完整复现步骤。**每跑一个用例都会真实花钱**，480p 约 ¥2.3、720p 约 ¥5、1080p 约 ¥12.4、4K 约 ¥25。想只花 ¥5 验证核心逻辑，跑 480p 和「480p 含视频输入」两个用例即可。

下面的响应都是 2026-08-02 那轮实测抓到的原样内容，只把视频链接的签名参数省略了。

### 准备

两个凭据，别混用（混用会得到一个含义不明的 401）：

| 凭据 | 从哪来 | 用途 |
|---|---|---|
| 中转 API key（`sk-` 开头） | 后台「令牌」页新建 | 提交任务、查任务 |
| 管理员访问令牌 | 后台「个人设置 → 访问令牌」 | 查实际扣费、查日志 |

```bash
export BASE=https://你的实例地址          # 不要带尾斜杠
export KEY=sk-xxxx                        # 中转 API key
export ADMIN=xxxx                         # 管理员访问令牌
export MODEL=doubao-seedance-2-0-260128
```

开跑前先在后台确认三件事，任何一条不满足，测出来的数都没有意义：

1. **有一个启用状态的渠道**能提供该模型。停用的渠道不会报「渠道停用」，而是报模型不存在，容易误判。
2. **该模型配了「模型倍率」**，且**没有**配「模型固定价格」。配了固定价格会退化成按次收费，token 结算被整体跳过。
3. 记下当前的**模型倍率**、**分组倍率**、**汇率**，复算要用。

### 第 1 步：提交任务

无视频输入（480p / 720p / 1080p / 4k 只改 `resolution`）：

```bash
curl -s -X POST "$BASE/v1/video/generations" \
  -H "Authorization: Bearer $KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "doubao-seedance-2-0-260128",
    "prompt": "a calm ocean wave rolling onto an empty beach at sunrise, slow camera pan",
    "seconds": "5",
    "metadata": { "resolution": "480p" }
  }'
```

含视频输入，在 `metadata.content` 里挂一个参考视频。**`role` 必须是 `reference_video`**：

```bash
curl -s -X POST "$BASE/v1/video/generations" \
  -H "Authorization: Bearer $KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "doubao-seedance-2-0-260128",
    "prompt": "a calm ocean wave rolling onto an empty beach at sunrise, slow camera pan",
    "seconds": "5",
    "metadata": {
      "resolution": "480p",
      "content": [
        {
          "type": "video_url",
          "video_url": { "url": "https://公网可访问的.mp4" },
          "role": "reference_video"
        }
      ]
    }
  }'
```

参考视频可以直接用上一个用例产出的视频 URL（火山返回的链接 24 小时内有效）。漏掉 `role` 会被上游拒绝，实测拿到的原始报错：

```json
{
  "code": "fail_to_fetch_task",
  "message": "{\"error\":{\"code\":\"InvalidParameter\",\"message\":\"The parameter `content` specified in the request is not valid: reference media mode requires video role to be reference_video.\",\"param\":\"content\",\"type\":\"BadRequest\"}}",
  "data": null
}
```

提交成功的响应**顶层直接带 `task_id`**，记下它，后面每一步都要用。

### 第 2 步：轮询到终态

```bash
curl -s -H "Authorization: Bearer $KEY" "$BASE/v1/video/generations/$TASK_ID"
```

**终态是大写的 `SUCCESS` / `FAILURE`**，不是 OpenAI 风格的 `completed` / `failed`；任务体裹在 `data` 信封里，不在顶层。写轮询脚本时这两点最容易踩坑——实测第一轮就是因为读顶层小写状态，四个用例全部空转到 900 秒超时，钱花了但结论没拿到。

5 秒视频通常 3-4 分钟出结果。以「480p 含视频输入」用例为例，实测原始响应：

```json
{
  "code": "success",
  "message": "",
  "data": {
    "id": 47,
    "task_id": "task_To0oAtJorNK2Zm8Wg9hYvMTMMHUx6FlX",
    "platform": "54",
    "group": "default",
    "channel_id": 1,
    "quota": 184175,
    "action": "generate",
    "status": "SUCCESS",
    "fail_reason": "",
    "result_url": "https://ark-acg-cn-beijing.tos-cn-beijing.volces.com/doubao-seedance-2-0/0217856800062...mp4?<签名参数已省略>",
    "submit_time": 1785680006,
    "start_time": 1785680019,
    "finish_time": 1785680214,
    "progress": "100%",
    "properties": {
      "upstream_model_name": "doubao-seedance-2-0-260128",
      "origin_model_name": "doubao-seedance-2-0-260128"
    },
    "data": {
      "id": "cgt-20260802221320-zjbjt",
      "model": "doubao-seedance-2-0-260128",
      "status": "succeeded",
      "resolution": "480p",
      "ratio": "16:9",
      "duration": 5,
      "framespersecond": 24,
      "generate_audio": true,
      "draft": false,
      "seed": 75103,
      "content": { "video_url": "https://ark-acg-cn-beijing.tos-cn-beijing.volces.com/...mp4?<签名参数已省略>" },
      "usage": {
        "completion_tokens": 100858,
        "total_tokens": 100858
      }
    }
  }
}
```

这个响应里有对账需要的全部东西：

- `data.data.usage.completion_tokens` = **上游认定的 token 用量**，官方计费以它为准
- `data.quota` = **实例实际扣的额度**（差额结算后的最终值）
- `data.data.resolution` / `duration` / `framespersecond` = 复算公式要用的参数

如果 `data.data.usage` 是空的，说明上游没返回用量，扣费会停留在预扣的小额——这本身是个严重问题，不要当成「测试通过」。

### 第 3 步：复算并比对

```
应扣额度 = floor(completion_tokens × 模型倍率 × 分组倍率 × video_input 倍率)
```

`video_input` 倍率查[上面那张表](#价格表与相对倍率)：无视频输入的 480p/720p 是 1.0，1080p 是 1.1087，含视频输入的 480p 是 0.6087。

四个用例的实测算式（模型倍率 3.0、分组倍率 1.0）：

| 用例 | 算式 | 复算 | `data.quota` 实际 |
|---|---|---|---|
| 480p | `floor(50638 × 3 × 1 × 1)` | 151,914 | 151,914 |
| 720p | `floor(108900 × 3 × 1 × 1)` | 326,700 | 326,700 |
| 1080p | `floor(245025 × 3 × 1 × 1.1087)` | 814,974 | 814,974 |
| 480p 含视频输入 | `floor(100858 × 3 × 1 × 0.6087)` | 184,175 | 184,175 |

两边相等即计费正确。不相等时按这个顺序查：`video_input` 倍率是否生效（对比 1080p 和 480p 的单价比）→ 模型倍率是否配在基准档 → 差额结算是否触发（见下一步）。

想再核一遍官方人民币金额：`completion_tokens ÷ 1,000,000 × 官方单价`。480p 含视频输入即 `100858 ÷ 1e6 × 28 = ¥2.82`。实例扣的额度换算人民币是 `额度 ÷ 500000 × 汇率`，即 `184175 ÷ 500000 × 7 = ¥2.58`，比官方低 8.7%——这是模型倍率配 3.0 而非 3.2857 带来的折扣，全档位一致。

### 第 4 步：核对日志（这一步容易看错）

在**用量日志**页面按时间筛出这几笔，会看到**每个任务有两条记录**：

| 类型 | 记的是什么 | 480p 那笔 |
|---|---|---|
| 消费 | **提交时的预扣额度**，不是最终扣费 | 750,000 |
| 退款 | 差额结算退还的部分 | 598,086 |

净额 `750000 − 598086 = 151914`，才等于实际扣费。实测四笔：

| 用例 | 消费（预扣） | 退款（差额） | 净额 = 实际扣费 |
|---|---|---|---|
| 480p | 750,000 | 598,086 | 151,914 |
| 720p | 750,000 | 423,300 | 326,700 |
| 1080p | 831,521 | 16,547 | 814,974 |
| 480p 含视频输入 | 456,521 | 272,346 | 184,175 |

**只看「消费」类型会严重高估**，480p 那笔会看成 750,000（是实际的 4.9 倍）。页面上的「消费额度」统计也只累加消费、不减退款，同样会高。

两条记录共用同一个 `request_id`，`other` 里带同一个 `task_id`，所以按 request_id 一搜就能看到完整的一笔。这个关联是后来才加上的，**该改动上线之前产生的历史日志**里结算行的 request_id 是独立生成的，只能按时间和金额人工对上。

导出[用量对账单](log-export-statement.md)时不需要手工处理：类型选「消费」导出，退款行会一并带出并以**负数**参与求和，`entry_type` 列标明哪行是预扣（`prepaid_hold`）哪行是结算（`refund` / `settlement`），`task_id` 列用来归组。480p 那笔在账单里是这样两行（CNY，汇率 7）：

| entry_type | output_tokens | output_price_per_1m | output_amount | other_amount | total_amount |
|---|---|---|---|---|---|
| `prepaid_hold` | | | | 10.500000 | 10.500000 |
| `refund` | 100858 | 21.087033 | 2.126796 | −10.500000 | −8.373204 |

净额 2.126796 元即 151,914 额度。结算行的 `output_tokens` 就是上游返回的计费用量，`other_amount` 是冲回的预扣。

预扣额度本身的算法是 `模型倍率 ÷ 2 × 500000 × 分组倍率 × video_input 倍率`，所以 480p/720p 都是 750,000（`3 ÷ 2 × 500000`），1080p 是 `750000 × 1.1087 = 831521`。

退款日志的 `content` 字段会把结算参数写全，可以直接看到实例用的 token 数和各项倍率：

```
token重算：tokens=100858, modelRatio=3.00, groupRatio=1.00, otherMultiplier=0.6087
```

**这条是判断计费是否正确最直接的证据**，比任何推断都可靠。

### 第 5 步：确认总额对得上

跑之前和跑之后各记一次用户余额，减少量应当精确等于各用例实际扣费之和。实测那轮三个用例合计 `151914 + 326700 + 814974 = 1293588`，与余额减少量完全一致，说明没有额度泄漏或重复扣费。

### 用脚本自动跑

上面五步已经封装成 skill `seedance-billing-check`，会自动完成提交、轮询、等结算落库、复算、比对，并输出逐用例的 PASS/FAIL：

```bash
# 预检，不花钱：检查模型是否按量计费、倍率是否配在基准档、余额是否够、本轮预计花多少
python3 .claude/skills/seedance-billing-check/scripts/seedance_billing_check.py --dry-run

# 实跑，默认 480p,720p,1080p,video-480p 四档，约 ¥20
python3 .claude/skills/seedance-billing-check/scripts/seedance_billing_check.py --yes

# 只跑最小集，约 ¥5
python3 .claude/skills/seedance-billing-check/scripts/seedance_billing_check.py --yes --cases 480p,video-480p
```

需要三个环境变量：`NEW_API_BASE_URL`、`NEW_API_KEY`、`NEW_API_ADMIN_TOKEN`。详见 [.claude/skills/seedance-billing-check/SKILL.md](../.claude/skills/seedance-billing-check/SKILL.md)。

**注意**：脚本的 `token_field` 断言在上游返回的两个 token 字段相等时不具备区分能力，通过不等于代码读对了字段（原因见[上一节](#一个已知的潜伏风险)）。

## 参考文档

- 火山方舟《模型价格》— 视频生成模型章节：https://docs.volcengine.com/docs/82379/1544106
  页面为客户端渲染，抓取工具只能拿到导航壳，核对时需在浏览器打开或导出 PDF。价格核对日期 2026-07-31。
- 官方价格表摘录与计费规则整理：[.claude/skills/seedance-billing-check/references/official-prices.md](../.claude/skills/seedance-billing-check/references/official-prices.md)
- 计费表达式系统设计文档：[pkg/billingexpr/expr.md](../pkg/billingexpr/expr.md)

官方调价后需同步更新三处：`relay/channel/task/doubao/constants.go` 的 `videoPriceTable`、skill 脚本里的 `OFFICIAL_PRICES`、以及 `references/official-prices.md`。如果实测出现全档位等比例偏差，先怀疑价格表过期，而不是代码有 bug。
