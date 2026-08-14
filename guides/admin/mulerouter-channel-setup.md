# MuleRouter 渠道配置说明

面向管理员。这份文档说明如何配置 MuleRouter 渠道，让终端用户能用简短的模型名调用图片与视频生成模型，并且账单可核对、参数越界会被拒。

终端用户看的文档是 [image-video-generation.md](../user/image-video-generation.md)，那份刻意不提上游厂商。这份是内部文档，会直说。

## 这个渠道是什么

MuleRouter 是一家聚合平台，把多家厂商的生成模型收在 `/vendors/{厂商}/v1/{模型}/{动作}` 下，协议形状统一：提交返回任务 ID，轮询取结果。本渠道类型就是对接这套协议的。

关键设计：**哪些厂商、哪些模型可用，是渠道配置，不是代码**。接一个新模型只需要往路由表里加一行，不需要改代码、不需要发版。

本文以已接入的三个 CarrotHub 模型为例：

| 上游模型 | 能力 | 上游价格 |
|---|---|---|
| `z-image-spicy` | 文生图 | $0.013 / 张（提示词改写另收 $0.001/次） |
| `qwen-image-edit-spicy` | 图生图 | $0.04 / 张 |
| `wan2.2-i2v-spicy` | 图生视频 | 480p $0.02/秒，720p $0.04/秒 |

## 三个名字，别搞混

这是配置里最容易出错的地方。

| 名字 | 样子 | 出现在哪 |
|---|---|---|
| **上游模型名** | `z-image-spicy` | 路由表的 `model` 字段，用于拼上游 URL |
| **内部模型名** | `carrothub/z-image-spicy/generation` | 由路由表的 `vendor`/`model`/`action` 三段拼出，是渠道内部的路由标识 |
| **对外模型名** | `z-image-spicy` | 客户端传的名字，也是**计价用的名字** |

内部模型名必须带动作段，因为有些厂商同一个模型的不同动作价格不同（例如 `kling-v3` 的 `text-to-video` 和 `image-to-video`），而按次计价是按模型名索引的，动作不进名字就没法分别定价。

对外模型名通过**渠道的「模型重定向」**映射到内部模型名。这一步不只是为了好写，更是为了**不让终端用户看到上游是谁**——不配重定向的话客户就得在请求里写 `carrothub/...`。

对外名可以取任意名字。本文示例取和上游同名的短名（`z-image-spicy`），如果连模型名本身都不想暴露，取 `image-gen-pro` 之类也完全可以，只要三处保持一致。

## 配置步骤

### 1. 新建渠道

渠道 → 新建，填：

| 字段 | 值 |
|---|---|
| 类型 | `MuleRouter` |
| 名称 | 自定，例如 `MuleRouter CarrotHub` |
| Base URL | **留空**（默认 `https://api.mulerouter.ai`） |
| 密钥 | MuleRouter 控制台的 API Key |
| 分组 | 按你的分组策略填 |
| 测试模型 | **留空**——渠道测试发的是 chat completion，异步任务渠道必然失败 |

**模型**填对外名（逗号分隔）：

```
z-image-spicy,qwen-image-edit-spicy,wan2.2-i2v-spicy
```

> ⚠️ 这里**只填对外名，不要填内部三段式名**。填了的话，`/v1/models` 会把 `carrothub/...` 列给客户看，而且客户能直接用 `/vendors/carrothub/v1/...` 原生路径调用。只填对外名时那条路径会返回 503，客户无从发现。

**模型重定向**填对外名 → 内部名：

```json
{
  "z-image-spicy": "carrothub/z-image-spicy/generation",
  "qwen-image-edit-spicy": "carrothub/qwen-image-edit-spicy/generation",
  "wan2.2-i2v-spicy": "carrothub/wan2.2-i2v-spicy/generation"
}
```

### 2. 填路由表

选了 MuleRouter 类型后会出现 **MuleRouter 路由表** 输入框，填 JSON：

```json
{
  "routes": [
    { "vendor": "carrothub", "model": "qwen-image-edit-spicy" },
    {
      "vendor": "carrothub",
      "model": "z-image-spicy",
      "billing_vars": [
        { "name": "prompt_extend", "kind": "enum",
          "values": { "true": 1.076923, "false": 1.0 }, "default": "true" },
        { "name": "width",  "kind": "int", "min": 256, "max": 1536, "default": "1024" },
        { "name": "height", "kind": "int", "min": 256, "max": 1536, "default": "1536" }
      ]
    },
    {
      "vendor": "carrothub",
      "model": "wan2.2-i2v-spicy",
      "billing_vars": [
        { "name": "seconds", "source": "duration", "kind": "int",
          "enum": [5, 8], "default": "5", "divisor": 1 },
        { "name": "resolution", "kind": "enum",
          "values": { "480p": 1.0, "720p": 2.0 }, "default": "480p" }
      ]
    }
  ]
}
```

保存时会校验，不合法会直接报错并指出是第几条路由的第几个变量。

### 3. 配价格

系统设置 → 模型 → **模型固定价格**，加三条：

```json
{
  "z-image-spicy": 0.013,
  "qwen-image-edit-spicy": 0.04,
  "wan2.2-i2v-spicy": 0.02
}
```

三件事必须注意：

- **键是对外名**，不是内部三段式名。计价用的是客户端传的名字。
- **必须配「模型固定价格」，不能配「模型倍率」**。异步任务按次计费，配成倍率会走 token 结算路径，而这些模型上游不回传 token 用量，结果是扣一个和实际无关的数。
- **视频的价格是「每秒」不是「每次」**。`0.02` 是 480p 一秒的价，乘上 `seconds` 和 `resolution` 倍率才是整段视频的价。

单位是美元，实际售价再叠加分组倍率。上面填的是上游成本价，要加价就往上调。

## 路由表详解

### 路由字段

| 字段 | 必填 | 说明 |
|---|---|---|
| `vendor` | ✅ | 上游厂商段，例如 `carrothub`、`alibaba`、`klingai` |
| `model` | ✅ | 上游模型段 |
| `action` | | 上游动作段，默认 `generation`。多动作厂商要显式写（`text-to-video` 等） |
| `billing_vars` | | 计费变量声明，见下 |

`vendor` 和 `model` 里**不能有 `/` 或空格**——它们要拼成三段式名字，带斜杠会让解析错位。保存时会拒绝。

### billing_vars：边界与倍率是一件事

这是整套配置的核心。一个 `billing_vars` 条目同时干两件事：

1. **划定这个参数的合法取值范围**，越界的请求直接 400 拒绝；
2. **把参数值转换成计费倍率**，乘进这次调用的价格。

两件事绑在一起是刻意的：**一个参数只有先被限界，才允许影响价格**。否则客户传 `duration: 99999` 就能让扣费溢出。

| 字段 | 说明 |
|---|---|
| `name` | 倍率名。会出现在账单日志里（`计算参数：seconds: 8.00`），**取人看得懂的名字** |
| `source` | 请求里的字段名。省略时等于 `name` |
| `kind` | `int` 或 `enum` |
| `default` | **必填**，字符串写法（`"5"` / `"480p"` / `"true"`）。客户没传这个字段时按它计费 |
| `min` / `max` | `int` 专用，闭区间 |
| `enum` | `int` 专用，只允许列出的几个数 |
| `values` | `enum` 专用，取值 → 倍率的映射表 |
| `divisor` | `int` 专用。倍率 = 值 ÷ divisor。**不填或填 0 = 只校验不计费** |

`default` 统一写成字符串，是因为 enum 查表要把请求值字符串化，一种写法比按类型分两种少踩坑。它是必填的——客户省略某个字段时如果没有确定取值，计费就不确定了。

### 两个约定

**按时长计费的模型，`seconds` 倍率取实际秒数，基础价按秒。**（`divisor: 1`）

仓库里所有按时长计费的适配器都是这个约定。用「每 5 秒一档」的写法（`divisor: 5`、基础价 $0.10）算出来的钱一模一样，但账单会显示 `seconds: 1.00`，读起来像一秒的视频。倍率名会出现在账单上，得说人话。

**只校验不计费的字段也要声明。** `z-image-spicy` 的 `width`/`height` 不影响价格，仍然声明了 256–1536 的边界，原因见下一节。

### 成本字段黑名单

以下字段名一旦出现在请求里、而路由的 `billing_vars` 没有声明它，请求会被 **400 拒绝**：

```
n            count       num_images   num_outputs   batch_size
duration     seconds     resolution   size          quality
width        height      fps          frames        num_frames
steps        sample_count
```

这是防呆：上游哪天加了个新的计费维度、你没跟进配置，客户传了它就会**按旧价格生成更贵的东西**——少收的钱谁也发现不了。宁可拒绝请求，也不静默漏收。

所以 `width`/`height` 即使不计费也必须声明——否则带这两个参数的正常请求会被拒。

### 保存时的校验

以下配置会被直接拒绝，报错会指出具体位置：

| 情况 | 原因 |
|---|---|
| 没有任何路由 | 空表没有意义 |
| 两条路由拼出同一个内部模型名 | 无法确定用哪条 |
| `vendor` / `model` / `action` 含 `/` 或空格 | 会破坏三段式名字解析 |
| `divisor > 0` 但没有 `max` 也没有 `enum` | **无界乘数**，正是这套机制要防的东西 |
| `default` 不满足它自己声明的边界 | 省略字段时会算出非法倍率 |
| enum 的 `values` 里有非正数或非有限值 | 倍率必须为正 |
| `kind` 不是 `int` / `enum` | — |

## 验证配置

改完配置跑一次冒烟测试，它会先做**不花钱**的检查（配置项、护栏、余额未变动），确认无误后再花约 $0.15 实跑三个模型：

```bash
export NEW_API_KEY=sk-中转令牌
export NEW_API_ADMIN_TOKEN=管理员访问令牌

# 只做免费检查
python3 .claude/skills/mulerouter-smoke-test/scripts/mulerouter_smoke_test.py \
  --dry-run --base-url http://localhost:3000

# 确认预算后实跑
python3 .claude/skills/mulerouter-smoke-test/scripts/mulerouter_smoke_test.py \
  --yes --base-url http://localhost:3000
```

它会核对扣费是否精确等于 `固定价 × 倍率 × 分组倍率`、产物类型是否正确、内部模型名有没有泄漏给客户端、原生厂商路径是否确实不可达。

手工验一下也行：

```bash
# 应当返回 400，且报错里提到 duration
curl -X POST "$BASE_URL/v1/video/generations" \
  -H "Authorization: Bearer $API_KEY" -H "Content-Type: application/json" \
  -d '{"model":"wan2.2-i2v-spicy","prompt":"x","image":"https://e.com/a.png","duration":9}'

# 应当只列出对外名，不含 carrothub
curl "$BASE_URL/v1/models" -H "Authorization: Bearer $API_KEY"
```

## 新增一个模型

不需要改代码，四步：

1. **查上游价格和参数**。到 MuleRouter 文档的对应端点页，记下价格、参数的取值范围、以及哪些参数影响价格。
2. **加路由**。往路由表加一条，把影响价格的参数写成 `billing_vars`；只要命中黑名单的参数即使不计费也要声明边界。
3. **加对外名**。渠道的「模型」字段加对外名，「模型重定向」加一条映射。
4. **配价格**。模型固定价格加一条，键是对外名。按时长计费的模型基础价填**每秒**价。

最后跑一次冒烟测试。

## 排障

**提交返回 503「无可用渠道」**
渠道的「模型」字段里没有客户传的那个名字。检查对外名是否拼对、渠道是否启用、分组是否匹配。

**提交返回 404 `model_not_found`**
名字在渠道模型列表里，但重定向后的内部名不在路由表里。检查「模型重定向」的值和路由表拼出的三段式名字是否一字不差。

**提交返回 400，提示某参数「affect the price of this model and are not enabled」**
客户传了黑名单里的字段而路由没声明它。要么在 `billing_vars` 里加上（带边界），要么让客户别传。

**提交返回 400 `model_price_error` 或「模型倍率未配置」**
没配模型固定价格。注意键要用对外名。

**扣费金额是预期的 5 倍或 1/5**
基础价和 `divisor` 不匹配。视频模型应当是「每秒价 + `divisor: 1`」，不是「每 5 秒价 + `divisor: 5`」。两处必须一起改。

**任务一直停在 `NOT_START` / 排队状态**
先看后端日志有没有 `unknown task status` ——有的话说明上游返回了四种状态（`pending`/`processing`/`completed`/`failed`）之外的值，需要开发补映射。没有的话就是上游在排队。任务超过 24 小时会自动判失败并退款。

**客户说参数传了没生效**
参数写在请求体顶层和 `metadata` 里都支持，同名时以 `metadata` 为准。如果两处都没写对，会按 `default` 计费和生成。

## 已知限制

**产物链接暴露上游。** 结果 URL 形如 `https://mule-router-assets.muleusercontent.com/.../carrothub/...`，任务查询响应里的 `properties.upstream_model_name` 和 `data` 字段同样带内部信息。路径和参数里已经看不到厂商，但结果链接还没挡住。要彻底隐藏需要做产物代理（视频已有 `/v1/videos/{id}/content`，图片还没有）。

**轮询容量共享。** 所有异步任务平台共用一个轮询池（默认单轮最多取 1000 条未完成任务），任务量大时可能挤占其他平台的轮询。量上来后可以在渠道设置里开启「跳过任务轮询间隔」，并关注未完成任务总数。

**上游失败态未实测。** 失败响应里 `error` 对象的字段名来自文档，还没在真实失败场景下验证过。如果遇到任务失败但 `fail_reason` 为空或乱码，把原始响应记下来反馈给开发。
