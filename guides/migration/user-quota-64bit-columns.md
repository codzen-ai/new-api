# 用户额度列的 64 位 schema 守卫（升级前检查）

面向执行升级的人或自动化助手。上游 `a073f74b3`（#7025，"refactor: deprecate int32"）加了一道启动守卫，要求 `users` 表的额度列必须是 64 位整数。

**结论先说：绝大多数情况下这次升级什么都不用做。** 本仓库的 `users` 表一直是 GORM 建的，而 GORM 一直把这四列建成 `bigint`——尽管 struct tag 上写着 `type:int`。本文的价值在于：用一条查询确认这一点，解释那个反直觉的 tag 语义（否则每次看到 `type:int` 都会得出错误结论），以及万一列真是 32 位时怎么处理。

这不是[语义迁移](deploying-semantic-migrations.md)：不改任何值的含义，不需要停机窗口，也不需要核查窗口期消费记录。

## 守卫做什么

[model/main.go](../../model/main.go) 的 `ensureUserQuotaColumns` 检查四列：

| 列 | 对应字段 |
|---|---|
| `quota` | `User.Quota` |
| `used_quota` | `User.UsedQuota` |
| `aff_quota` | `User.AffQuota` |
| `aff_history` | `User.AffHistoryQuota` |

接受的类型：MySQL 为 `bigint` / `unsigned bigint` / `bigint unsigned`，PostgreSQL 为 `bigint` / `int8`。**SQLite 直接放行**（类型亲和性本身就是 64 位），本地开发环境不受影响。`users` 表还不存在时也放行，所以全新安装的首次启动不会被拦。

调用点在 `InitDB()` 里、`if !common.IsMasterNode { return nil }` **之前**，所以主节点和从节点都会检查。失败时错误冒泡到 [main.go](../../main.go) 的 `common.FatalLog`，进程退出：

```
failed to initialize database: users.quota uses int4; 32-bit is not supported
```

守卫**成功时不打任何日志**，所以「没有这行报错」就是通过。

## 为什么 `type:int` 不产生 32 位列

这是全文最反直觉的一点。[model/user.go](../../model/user.go) 里写着：

```go
Quota int `json:"quota" gorm:"type:int;default:0"`
```

看起来像是把列钉死成 SQL 的 `int`，其实不是。GORM 解析 `type:xxx` 时会先小写化，然后拿它去匹配自己的**抽象类型常量**（`Bool` / `Int` / `Uint` / `Float` / `String` / `Time` / `Bytes`）。而 `schema.Int` 这个常量的值恰好就是字符串 `"int"`，于是 `type:int` 命中的是**抽象整数类别**，不是字面 SQL 类型。命中之后，具体 SQL 类型由 `field.Size` 决定——Go `int` 在 64 位平台上 `Size = 64`，dialector 返回 `bigint`。

实测（`schema.Parse` + 各 driver 的 `DataTypeOf`，纯计算不需要连库）：

```
column         GoKind   DataType   Size  PG_DDL       MySQL_DDL
quota          int      "int"      64    bigint       bigint
used_quota     int      "int"      64    bigint       bigint
aff_quota      int      "int"      64    bigint       bigint
aff_history    int      "int"      64    bigint       bigint
remain_quota   int      "int"      64    bigint       bigint      ← 无 type tag，结果相同
group          string   "varchar(64)" 0  varchar(64)  varchar(64) ← 未命中抽象类型，按字面处理
```

`quota` 和 `remain_quota` 结果完全一样——**`type:int` 在这些字段上是个 no-op**，删掉它不会改变任何生成的 DDL。对比 `Group` 的 `type:varchar(64)`：它匹配不上任何抽象类型，才会被当成字面 SQL 类型透传。这就是两者的区别。

由此可以推出三件事：

1. `AutoMigrate` **不会**把 `bigint` 列改回 32 位。列是 `int8` 时 `fullDataType` = `"bigint default 0"`，而 `typeAliasMap["int8"]` = `["bigint"]`，前缀匹配成立 → 判定类型相符 → 不发 ALTER。生产环境带着这些 tag 长期重启从未出现类型抖动，这本身就是最直接的证据。
2. 反过来，列真是 `int4` 时，`AutoMigrate` **会自动加宽**成 `bigint`（`"bigint default 0"` 与 `int4` / `integer` 都不前缀匹配 → 触发 ALTER），方向安全、不会溢出。
3. 所以**不需要修改任何源码**。见过一种说法是「必须去掉 `type:int` 否则 ALTER 会被改回去」——那是把抽象类型误当成字面类型得出的结论，是错的。

## 确认你的列类型

```sql
-- PostgreSQL
SELECT column_name, data_type
FROM information_schema.columns
WHERE table_name = 'users'
  AND column_name IN ('quota', 'used_quota', 'aff_quota', 'aff_history')
ORDER BY column_name;
```

```sql
-- MySQL
SELECT column_name, data_type
FROM information_schema.columns
WHERE table_schema = DATABASE() AND table_name = 'users'
  AND column_name IN ('quota', 'used_quota', 'aff_quota', 'aff_history')
ORDER BY column_name;
```

- **四行都是 `bigint`** → 守卫会通过，直接部署，本文其余部分不必看。这是预期结果。
- **出现 `integer` / `int`** → 走下一节。这只可能来自手工建表、从别处导入、或上古 one-api 时代遗留的库。（[bin/migration_v0.2-v0.3.sql](../../bin/migration_v0.2-v0.3.sql) 和 [bin/migration_v0.3-v0.4.sql](../../bin/migration_v0.3-v0.4.sql) 里只有 `UPDATE`，没有任何 DDL——本仓库的 `users` 表从来都是 GORM 建的。）

注意 `information_schema` 和守卫报错用的是两套类型名：PostgreSQL 上前者返回 `integer` / `bigint`，而守卫的错误信息里是 driver 的 `DatabaseTypeName()`，即 `int4` / `int8`。指的是同一件事。

## 万一列真是 32 位

守卫的唯一缺陷是**它跑在 `migrateDB()` 之前**——`AutoMigrate` 本来能自动加宽，但还没轮到它就已经被拦下了。绕过这个顺序问题不需要改代码，也不需要手写 SQL：

1. **主节点**带 `SKIP_64BIT_QUOTA_SCHEMA_CHECK=true` 启动一次。守卫跳过，`AutoMigrate` 把四列加宽成 `bigint`。日志里会有一行 `SKIP_64BIT_QUOTA_SCHEMA_CHECK=true; skipping user quota schema check`。
2. 用上一节的查询确认四行都变成了 `bigint`。
3. **去掉这个环境变量**，重启。守卫此后自然通过。
4. 只有在第 2 步确认通过之后，才启动从节点——`AutoMigrate` 只在主节点跑，从节点自己不会加宽，先起会被守卫拦下。

加宽是全表重写（PostgreSQL 上持 `ACCESS EXCLUSIVE` 锁），且 `AutoMigrate` 是每列一条、重写四次。`users` 表通常只有几千到几万行，亚秒级；到百万行量级就安排到低峰期，或者用一条手写语句合并成一次重写：

```sql
-- PostgreSQL：可选的手工加速，一次表重写而不是四次
ALTER TABLE users
  ALTER COLUMN quota       TYPE bigint,
  ALTER COLUMN used_quota  TYPE bigint,
  ALTER COLUMN aff_quota   TYPE bigint,
  ALTER COLUMN aff_history TYPE bigint;
```

```sql
-- MySQL：MODIFY 会整体替换列定义，必须重述 DEFAULT 0；不要加 NOT NULL（这四列本来可空）
ALTER TABLE users
  MODIFY COLUMN quota       bigint DEFAULT 0,
  MODIFY COLUMN used_quota  bigint DEFAULT 0,
  MODIFY COLUMN aff_quota   bigint DEFAULT 0,
  MODIFY COLUMN aff_history bigint DEFAULT 0;
```

执行前先做全库备份（`pg_dump` / `mysqldump`）。这是 DDL 变更，出问题时唯一的兜底是备份。

**不要把 `SKIP_64BIT_QUOTA_SCHEMA_CHECK=true` 长期挂着。** `a073f74b3` 把充值上限从 int32 边界（旧的 `topUpQuotaMaxCurrent` 返回 `MaxQuota - 1 - credited`）放宽到了 `MaxWalletQuota = 2^53-1`，也就是说**原本挡住 `users.quota` 溢出的那道检查已经被拿掉了，前提是列已经是 bigint**。`QuotaPerUnit = 500000` 时 int32 上限只对应约 $4294 的钱包余额，是实际可达的量级；超过后 PostgreSQL 会抛 `integer out of range` 让充值事务回滚（报错失败，不是静默截断），但对高余额用户是实质回归。这个开关只用于上面那一次性的加宽启动。

## 失败处理

| 现象 | 含义 | 处理 |
|---|---|---|
| 启动即退出，`failed to initialize database: users.quota uses int4; 32-bit is not supported` | 列确实是 32 位 | 走「万一列真是 32 位」一节 |
| 同上但报的是 `used_quota` / `aff_quota` / `aff_history` | 同上，只是先撞到别的列 | 同上，加宽是四列一起做的 |
| `failed to inspect users schema: ...` | 守卫读不到 `information_schema` | 数据库连接/权限问题，与本次升级无关 |
| 从节点启动失败但主节点正常 | 主节点已加宽但从节点连的是另一个库，或从节点先于主节点启动 | 确认 `SQL_DSN` 指向同一个库；确认主节点已完成加宽 |
| 日志出现 `skipping user quota schema check` 但你没打算跳过 | 环境变量残留 | 去掉 `SKIP_64BIT_QUOTA_SCHEMA_CHECK` 重启 |

## 回滚

正常路径（列本来就是 `bigint`）下这次升级**不做任何 schema 变更**，回滚二进制即可，没有数据侧的动作。

如果走过了加宽流程，回滚到旧版本时列会保持 `bigint`——旧代码的 tag 解析结果同样是 `bigint`（这是抽象类型，与版本无关），`AutoMigrate` 不会把它改回去，Go 字段也一直是 64 位的 `int`，读写都正常。所以加宽是单向且无害的，不需要反向 SQL。

## 给自动化助手的检查清单

1. 确认数据库类型。SQLite → 报告「不受影响」，正常升级，结束。
2. 跑「确认你的列类型」那条查询并记录输出。四行都是 `bigint` → 报告「守卫会通过」，正常升级，结束。**这是预期路径。**
3. 出现 `integer` / `int` → 报告受影响的列，确认已有全库备份；没有 → **中止**。
4. 报告 `SELECT count(*) FROM users` 的行数与预估锁时间。超过百万行 → **等人确认**是否安排低峰期。
5. 主节点带 `SKIP_64BIT_QUOTA_SCHEMA_CHECK=true` 启动，或执行手工 ALTER。
6. 复查列类型，四行必须都是 `bigint`；否则 **中止**。
7. 去掉 `SKIP_64BIT_QUOTA_SCHEMA_CHECK`，重启主节点，确认日志里既没有 `32-bit is not supported` 也没有 `skipping user quota schema check`。
8. 再启动从节点。
9. 抽查一次真实请求的额度扣减与消费日志。

**不要做的事**：不要为了满足守卫去改 `model/user.go` 的 tag（`type:int` 是 no-op，改它不解决任何问题）；不要长期挂着 `SKIP_64BIT_QUOTA_SCHEMA_CHECK`；不要在主节点完成加宽前启动从节点；不要顺手把 `request_count` / `aff_count` / `inviter_id` 一起改（守卫不检查它们，每多改一列多一次全表重写）。
