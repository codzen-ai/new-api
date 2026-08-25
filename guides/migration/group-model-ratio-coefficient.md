# 分组模型倍率语义迁移（绝对值 → 系数）操作手册

面向执行升级的人或自动化助手。本次迁移**自动运行**，本文的价值在于：判断你是否受影响、
升级前必须备份什么、启动后如何确认成功、失败时怎么处理、以及怎么回滚。

功能说明见 [guides/admin/group-model-ratio.md](../admin/group-model-ratio.md)，
设计与失败模式见 [specifications/group-model-ratio.md](../../specifications/group-model-ratio.md) §6。

## 这次迁移做什么

`分组模型倍率`（选项 `GroupModelRatio`）的语义从**绝对值**改成了**系数**：

| | 旧语义 | 新语义 |
|---|---|---|
| `{"vip":{"gpt-4o":5}}` 的含义 | vip 用 gpt-4o 的最终倍率就是 5，分组倍率不生效 | 最终倍率 = 全局倍率 × 5 |

主节点首次启动时按 `新值 = 旧值 ÷ 该模型的全局模型倍率` 自动换算，**换算后价格不变**。
换算只做一次，用选项 `GroupModelRatioSemantics = coefficient` 标记。

## 第一步：判断你是否受影响

```sql
-- PostgreSQL 用 "key"；MySQL / SQLite 用 `key`
SELECT value FROM options WHERE "key" = 'GroupModelRatio';
```

- **没有这一行，或值是 `{}` / 空** → 你没用过这个功能，本次迁移对你是空操作，
  直接升级即可，下面的备份和验证都不必做。启动日志里仍会出现一行
  `分组模型倍率已迁移为系数语义：0 个分组，0 个条目被移除`，那只是写标记，属正常。
- **有内容** → 继续往下走，全部步骤都要做。

## 三个必须先知道的风险

1. **回滚不是免费的。** 迁移后库里存的是系数，旧版本二进制会把 `0.5` 读成「最终倍率 0.5」，
   **少收到 1/10**。回滚必须连数据一起回滚，而那需要迁移前的原始值 → 所以必须先备份。
2. **多节点必须主节点先行。** 从节点按新语义解释库里的值，主节点还没迁移完就先起从节点，
   这段时间会把旧的绝对倍率当成系数，**超收一个数量级**。
3. **有存量配置时迁移失败会导致进程退出**（这是故意的）。日志里是
   `[FATAL] ... migration left an unsafe state`。修好数据库问题后重启，迁移会重新尝试。

## 操作步骤

### 0. 备份（必做，只需一次）

把两个选项的值存到文件里，用于回滚和事后核对：

```sql
SELECT value FROM options WHERE "key" = 'GroupModelRatio';   -- 存成 gmr-before.json
SELECT value FROM options WHERE "key" = 'ModelRatio';        -- 存成 model-ratio.json
```

### 1. 预演换算（推荐，供事后比对）

用备份的两个文件算出**期望结果**，事后拿它和实际结果比对，就能立刻判断迁移是否符合预期：

```bash
jq -n --slurpfile gmr gmr-before.json --slurpfile mr model-ratio.json '
  $gmr[0] | to_entries | map({
    group: .key,
    entries: (.value | to_entries | map({
      model: .key,
      old: .value,
      base: ($mr[0][.key] // null),
      expect: (if .value == 0 then "保留 0"
               elif ($mr[0][.key] // 0) > 0 then (.value / $mr[0][.key] | tostring)
               else "会被丢弃（没有全局模型倍率）" end)
    }))
  })'
```

注意这个预演**不识别固定价模型**。凡是在「模型固定价格」（选项 `ModelPrice`）里出现的模型，
无论能不能换算，都会被丢弃（旧版本里这类配置本来就不生效）。需要的话把 `ModelPrice`
也导出来对照一下。

### 2. 多节点：先停从节点

单节点部署跳过这步。多节点（有 `NODE_TYPE=slave` 的实例）请先把从节点全部停掉，
只保留主节点，避免从节点在主节点迁移完成前用新语义读旧数据。

### 3. 升级主节点并启动

按你平常的方式部署新版本（docker compose / 二进制 / k8s 均可），启动主节点。

### 4. 读日志确认结果（关键一步）

日志默认同时写 stdout 和 `<--log-dir>/oneapi-<时间戳>.log`（`--log-dir` 默认 `./logs`）。
Docker 部署直接看 `docker logs <容器>`。

按下面四个字符串检查，**必须出现第 1 条**：

```bash
# 1) 迁移成功（必须出现）
grep "分组模型倍率已迁移为系数语义" <日志>

# 2) 被丢弃：没有全局模型倍率
grep "没有对应的全局模型倍率" <日志>

# 3) 被丢弃：配在固定价模型上
grep "配在固定价模型上" <日志>

# 4) 迁移失败并退出（出现即中止流程，见下节）
grep "migration left an unsafe state" <日志>
```

第 1 条形如：

```
[SYS] 2026/08/25 - 15:40:12 | 分组模型倍率已迁移为系数语义：2 个分组，1 个条目被移除
```

第 2、3 条会把被丢弃的条目逐个列出，格式 `分组/模型=原倍率`，**请原样记录下来**，
步骤 6 要用。

再确认标记已落库：

```sql
SELECT value FROM options WHERE "key" = 'GroupModelRatioSemantics';  -- 期望 coefficient
```

以及换算结果与步骤 1 的预演一致：

```sql
SELECT value FROM options WHERE "key" = 'GroupModelRatio';
```

### 5. 验证计费

任选一个存活下来的 `(分组, 模型)` 组合：

- **定价页**：用属于该分组的账号打开模型广场，或直接调 `GET /api/pricing`（带该账号的登录态），
  看该模型价格是否**与升级前一致**（这是本次迁移的核心不变量：价格不变）。
- **真实账单**：用该分组的令牌发一条请求，在日志详情里看
  `model_ratio` = **全局**模型倍率、`group_ratio` = 换算后的系数。
  乘积应等于升级前的最终倍率。

固定价模型看「模型价格 + 分组倍率」，阶梯计费模型没有 `model_ratio`，都只看 `group_ratio`。

### 6. 重配被丢弃的条目

按步骤 4 记下的清单处理：

- **没有全局模型倍率的**：先在「模型倍率」里给该模型配好全局价，再按系数重配分组差异。
- **配在固定价模型上的**：这类配置在旧版本从未生效。新版本固定价也吃系数，
  如果确实想给这些分组打折，现在按系数（比如八折填 `0.8`）配上即可。

改配置立即生效，不需要重启。

### 7. 起从节点

确认步骤 4、5 都通过后，再把从节点升级并启动。

## 失败处理

| 现象 | 含义 | 处理 |
|---|---|---|
| `[FATAL] ... migration left an unsafe state` 且进程退出 | 有存量配置，但换算或标记没写成功 | 修数据库连接/权限/磁盘，然后重启。**不要**手工改 `GroupModelRatio`，重启会重新换算 |
| 启动正常，但没有「已迁移为系数语义」这一行 | 要么本来就没有存量配置（正常），要么标记早已存在（已迁移过） | 查 `GroupModelRatioSemantics`：是 `coefficient` 说明已迁移过，无需处理 |
| 有 `[SYS] ... failed to migrate group model ratio semantics`，但进程没退出 | 存量配置为空时的失败，无计费风险 | 记录即可；下次启动会重试 |
| 换算结果与预演不一致 | 可能涉及模型名归一（如思考预算变体），也可能预演漏算了固定价模型 | **暂停，交给人确认**，不要继续起从节点 |
| 定价页/账单价格与升级前不一致 | 不符合本次迁移的核心不变量 | **立即暂停并上报**，按下节回滚 |

## 回滚

回滚二进制**必须**同时回滚数据，否则会少收一个数量级：

```sql
UPDATE options SET value = '<gmr-before.json 的内容>' WHERE "key" = 'GroupModelRatio';
DELETE FROM options WHERE "key" = 'GroupModelRatioSemantics';
```

顺序：停所有节点 → 执行上面两条 SQL → 部署旧版本 → 起主节点 → 起从节点。
删掉标记行是为了将来再次升级时迁移能重新执行。

## 给自动化助手的检查清单

按顺序执行，任一「中止」条件命中就停下来把情况交给人：

1. 读 `GroupModelRatio`。为空 → 报告「无需迁移」，升级后只需确认进程正常启动，结束。
2. 备份 `GroupModelRatio` 与 `ModelRatio` 到文件，报告文件路径。
3. 生成预演结果并报告（哪些会换算成什么、哪些会被丢弃）。
4. 若存在从节点：先停从节点，报告已停实例。
5. 升级并启动主节点。
6. 等启动完成后 grep 四个关键字符串。
   - 命中 `migration left an unsafe state` → **中止**。
   - 未命中「已迁移为系数语义」→ 查标记；不是 `coefficient` → **中止**。
7. 比对实际 `GroupModelRatio` 与预演结果；不一致 → **中止**。
8. 报告被丢弃的条目清单（原样贴日志里的 `分组/模型=原倍率`）。
9. 抽查一个存活组合的定价页价格与升级前是否一致；不一致 → **中止并建议回滚**。
10. 全部通过后再起从节点，并在此后 10 分钟内复查一次新产生的消费日志，
    确认相关模型的 `group_ratio` 是系数而不是原来的分组倍率。

**不要做的事**：不要在主节点迁移完成前启动从节点；不要手工改 `GroupModelRatio` 来"帮助"
迁移（会被判成已是新语义）；不要在没有备份的情况下升级；不要在验证未通过时继续推进。
