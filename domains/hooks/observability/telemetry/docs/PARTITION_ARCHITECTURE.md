# 分区表架构规范（2026-07 数据生命周期）

> 本规范取代早期（已废弃）的"PG 自动路由到月度分区"方案。**所有
> INSERT/UPDATE/DELETE 必须显式指向 `*_default` 表，绕过 PG 的 partition
> pruning 自动路由。** SELECT 仍走父表以聚合 default + 月度分区。

## 1. 规范要点

### 1.1 表的三层结构

```
*_default           ← 当前数据（heap, 支持 UPDATE/DELETE, 写入目标）
*_YYYY_MM           ← 历史月度分区（heap 或 columnar, 只读/可由后台迁移器填入）
*_archive_YYYY_MM   ← 已归档的历史分区（columnar, 只读）
```

### 1.2 写操作的唯一目标

| 操作 | 表名 | 备注 |
|---|---|---|
| INSERT | `<table>_default` | 强制；禁止 `<table>` 父表 |
| UPDATE | `<table>_default` | 强制；不允许在已迁移到月度分区的行上 UPDATE（迁移后的行只读） |
| DELETE | `<table>_default` | 强制 |
| SELECT | `<table>` 父表 | OK；PostgreSQL 聚合所有分区（含 default + 月度 + archive） |

### 1.3 为什么不用 PG 的自动路由

PG 的 `INSERT INTO <parent>` 会按分区键自动路由到匹配的月度分区。但是：

1. **UPDATE/DELETE 不能命中 columnar 月度分区**（如 `request_logs_2026_06`）；
2. 写时还不知道这条记录最终会落在哪个月度分区；
3. **每日增量数据应集中留在 `_default`**，由后台迁移器定期按月搬运到对应的
   月度分区。这样既压缩了历史存储，又保留了对最新数据的可写性。

### 1.4 月度分区的预创建

`bg/partition_manager.go::ensure_next_month_partitions()` 每 24h 调一次
`SELECT ensure_<table>_partition($1)`，确保本月 + 下个月的月度分区存在。
这是在 **写之前** 把骨架搭好，但**月度分区不从 _default 自动接收新行**。

### 1.5 数据迁移

后台工具（独立进程，与 gateway 解耦）按时间窗口把 `_default` 中的历史
数据搬到对应的月度分区。搬运完成后从 `_default` 删除原行。搬运动作
本身要先 ATTACH DETACH，再 INSERT...SELECT，最后 DELETE...WHERE，保持
总行数不变。

## 2. 受控表清单

下列大表全部遵循本规范。每个表都配套：
- `*_default` 分区（catch-all）
- `ensure_<table>_partition()` 函数（idempotent，由 `partition_manager.go` 调）
- 月度分区（预创建）
- 后台迁移器（搬运 + 删除）

| 表 | 父表目标 | `_default` 已存在 | ensure 函数 | 迁移器 |
|---|---|---|---|---|
| `request_logs` | `request_logs_default` | ✅ 01-schema.sql | ✅ | ✅ |
| `request_logs_bodies` | `request_logs_bodies_default` | ✅ migration 328a | ✅ | ✅ |
| `request_wal` | `request_wal_default` | ✅ migration 332 | ✅ | TBD |
| `usage_ledger` | `usage_ledger_default` | ✅ migration 330 | ✅ | TBD |
| `routing_decision_log` | `routing_decision_log_default` | ✅ migration 333 | ✅ | TBD |
| `credential_model_index` | `credential_model_index_default` | ✅ migration 317 | ✅ | ✅ |
| `credit_ledger` | `credit_ledger_default` | ✅ migration 334 | ✅ | TBD |
| `tool_usage_stats` | `tool_usage_stats_default` | ✅ migration 335 | ✅ | TBD |

下列小表**不分区**，按普通表直接走 INSERT/UPDATE/DELETE：
- `tuning_signals`（量级不大）
- `analysis_events`（量级不大）
- `api_keys`（量级小但更新频繁；表太大时再分）
- `request_wal_bodies`（独立兄弟表，按 request_id PK，不分区）

## 3. SQL 模板

### 3.1 INSERT（典型）

```sql
INSERT INTO <table>_default (
    request_id, ts, ...
) VALUES (
    $1, now(), ...
)
ON CONFLICT (id, ts) DO UPDATE SET ...
```

- 目标必须显式写 `<table>_default`；
- ON CONFLICT 子句中的 EXCLUDED 引用合法，COALESCE 中的"原值"必须
  用 `<table>_default.col` 限定，避免歧义（参照 PG 处理 INSERT INTO
  分区表的语义）。

### 3.2 UPDATE（典型）

```sql
UPDATE <table>_default
   SET field = COALESCE($2, field),
       ...
 WHERE id = (
     SELECT id FROM <table>_default
      WHERE request_id = $1
      ORDER BY ts DESC LIMIT 1
 )
```

- UPDATE 目标必须显式写 `<table>_default`；
- 内层 SELECT 也只读 `<table>_default`，避免扫描已迁移到月度分区的行
  （那些行已离开 default，无法 UPDATE）。

### 3.3 DELETE（典型）

```sql
DELETE FROM <table>_default
 WHERE <cond>
```

- DELETE 目标必须显式写 `<table>_default`；
- 删除条件如果按时间过滤，要确保月度分区已 detach，否则会被 PG
  解释成跨分区的大批 DELETE（性能糟糕）。

### 3.4 SELECT（聚合查询）

```sql
SELECT ... FROM <table>
WHERE ts >= '2026-07-01' AND ts < '2026-08-01'
```

- SELECT 走父表，自动聚合 default + 所有月度分区（包括 archive）；
- 不要写 `FROM <table>_default` 又 `UNION ALL <table>_2026_07` —
  父表的 partition pruning 已经做了这件事。

## 4. Go 代码 enforce

通过 `golangci-lint` `gocritic` + 静态检查 + 测试用例确保代码不滑回旧模式：

- `domains/hooks/observability/telemetry/client_test.go::TestRequestLogsUpdateSQL_SetClauseDoesNotReferenceTargetAlias`
  强制 `UPDATE request_logs_default`，禁止别名 `rl`、禁止父表 `request_logs`；
- `...TestInsertUpsertSQL_DoesNotReferenceUndefinedRLAlias`
  强制 ON CONFLICT 子句中的 EXCLUDED 引用 `<table>_default`。

## 5. 不要做的事

- ❌ `INSERT INTO <table>` （父表） → 改写为 `INSERT INTO <table>_default`
- ❌ `UPDATE <table>` → 改写为 `UPDATE <table>_default`
- ❌ `DELETE FROM <table>` → 改写为 `DELETE FROM <table>_default`
- ❌ `UPDATE <table> SET ... FROM latest WHERE <table>.id = latest.id`
      （父表自动路由会让 WHERE 选择错误的分区）→ 改写为
      `UPDATE <table>_default ... WHERE <table>_default.id = ...`
- ❌ `ON CONFLICT ... DO UPDATE SET col = ..., existing.col` 中漏掉
      `<table>_default.` 限定 → 加上
- ❌ 在 SQL 字符串常量里写 `<table>` 然后依赖 PG auto-route → 全部改
      为 `*_default`

## 6. 废弃的旧规范（不要参考）

`2026-07-04-partition-table-fix.md` 已被本文件取代。它错误地认为"应当
操作父表让 PG 自动路由"，这与数据生命周期架构冲突。已删除。
