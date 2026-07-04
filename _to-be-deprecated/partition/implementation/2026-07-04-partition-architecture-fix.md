# ⚠️ 本文件已废弃 / This File Is Deprecated

> **归档日期**: 2026-07-05
> **替代文档**: [`docs/partition/IMPLEMENTATION_NOTES.md`](../../docs/partition/IMPLEMENTATION_NOTES.md) 第 §15 节
> **原因**: 内容已合并到主实施记录（架构修复与规范落地、root cause + 部署步骤）
> **保留原因**: 提供历史归档追溯

---

# PostgreSQL 分区表架构修复与规范落地（2026-07-04）

## 问题根因

**症状**：本地测试环境中，所有新请求无法写入 `request_logs_default`，报错：
```
ERROR: new row for relation "request_logs_default" violates partition constraint
```

**根本原因**：
PostgreSQL DEFAULT 分区的约束是**动态的**：
- 当月度分区（如 `request_logs_2026_07`）ATTACHED 时，DEFAULT 分区会自动排除该分区覆盖的时间范围 `[2026-07-01, 2026-08-01)`
- 这导致所有 `ts` 落在当月的 `INSERT INTO request_logs_default` 都被 PG 拒绝

**架构冲突**：
根据 `docs/partition/partition-background.md` 的设计，分区表架构要求：
1. 所有新数据写入 `*_default` 表（heap 表，支持 UPDATE/DELETE）
2. 月度分区应 **DETACHED**（不参与 PG 自动路由）
3. 后台迁移器定期将 `*_default` 中超过保留窗口（7 天）的数据搬运到对应月度分区
4. 月度分区可使用 columnar 压缩（历史数据只读）

但本地环境中，当月分区（2026-07 至 2026-12）仍处于 ATTACHED 状态，违反了架构设计。

---

## 解决方案

### 1. 引入标准文档（来自 llm-gateway-go-2）

复制 `~/workspace/llm-gateway-go-2/docs/` 下的分区表规范到 `docs/partition/`：

| 文档 | 说明 |
|---|---|
| `partition-background.md` | 问题根源、方案对比、决策过程 |
| `partition-architecture.md` | 分区设计、写入查询规范、维护流程 |
| `partition-standards.md` | 强制标准、代码审查清单、FAQ |
| `partition-test-cases.md` | 12 个测试用例（P0/P1）|
| `README.md` | 文档导航 |

这些文档明确规定：
- ✅ 所有 INSERT/UPDATE/DELETE 必须显式指向 `*_default` 表
- ✅ 月度分区必须 DETACHED
- ✅ 查询最近数据时直接读 `*_default`（最快）
- ✅ 跨月查询使用 VIEW 或手动 UNION ALL

### 2. 代码审计结果

通过 `grep` 扫描所有 Go 代码，确认**所有写入操作已正确指向 `*_default` 表**：

| 表 | INSERT | UPDATE | DELETE | 合规性 |
|---|---|---|---|---|
| `request_logs` | ✅ `request_logs_default` | ✅ `request_logs_default` | ✅ `request_logs_default` | ✅ 100% |
| `request_wal` | ✅ `request_wal_default` | ✅ `request_wal_default` | - | ✅ 100% |
| `usage_ledger` | ✅ `usage_ledger_default` | ✅ `usage_ledger_default` | - | ✅ 100% |
| `routing_decision_log` | ✅ `routing_decision_log_default` | - | ✅ `routing_decision_log_default` | ✅ 100% |
| `credit_ledger` | ✅ `credit_ledger_default` | - | - | ✅ 100% |
| `tool_usage_stats` | ✅ `tool_usage_stats_default` | - | - | ✅ 100% (主查询已改，fallback 保留父表以兼容迁移窗口期) |
| `credential_model_index` | ✅ (后台迁移器专用) | - | - | ✅ 100% |

**未发现任何违规写入父表的代码**（除测试文件和注释）。

### 3. 新增 Migration 337：DETACH 当月及未来分区

`db/migrations/337_detach_current_future_partitions.sql` 执行以下操作：

```sql
-- 对每个分区表（request_logs, request_logs_bodies, request_wal, 
-- usage_ledger, routing_decision_log, credential_model_index），
-- DETACH 当月及未来的月度分区（2026-07 至 2026-12）

ALTER TABLE request_logs DETACH PARTITION request_logs_2026_07;
ALTER TABLE request_logs DETACH PARTITION request_logs_2026_08;
...
ALTER TABLE request_logs DETACH PARTITION request_logs_2026_12;

-- 其他表同理
```

**效果**：
- DEFAULT 分区约束不再排斥当月数据
- `INSERT INTO request_logs_default (ts = '2026-07-04')` 成功写入
- 历史分区（如 2026-06 及更早）保持 ATTACHED 以便 `SELECT 父表` 自动聚合

**验证**（本地已通过）：
```sql
-- 写入测试
INSERT INTO request_logs_default (request_id, ts, tenant_id, success)
VALUES ('test-req-002', NOW(), 'test-tenant-02', true);
-- ✅ 成功

-- 更新测试
UPDATE request_logs_default SET prompt_tokens = 123, completion_tokens = 456
WHERE request_id = 'test-req-002';
-- ✅ 成功

-- 查询验证
SELECT COUNT(*) FROM request_logs_default;
-- 1 行

SELECT COUNT(*) FROM request_logs;
-- 1 行（父表聚合了 DEFAULT 分区）
```

### 4. 新增 Migration 336：后台迁移函数

`db/migrations/336_promote_default_to_partition_functions.sql` 注册一组 `promote_<table>_default_batch(interval, batch_size)` 函数，用于将 `*_default` 中超过保留窗口（默认 7 天）的冷数据搬运到对应月度分区。

配套的 `bg/partition_manager.go` 增加了 `promoteDefaultToPartitions()` 逻辑，每 1 小时调用一次这些函数，逐批迁移直到返回 0（无更多冷数据）。

### 5. 测试改动

- `admin/data_lifecycle_partition_test.go`：删除已废弃的 `archive_request_logs`/`archive_request_wal` 断言（migration 331 已移除这些函数）
- `bg/partition_manager_test.go`：更新 `TestEnsureSpecsCoversAllPartitionedTables`，反映当前 6 个分区表的状态

### 6. 文档

- 删除 `domains/hooks/observability/telemetry/docs/2026-07-04-partition-table-fix.md`（错误方案：建议写父表让 PG 自动路由）
- 新增 `domains/hooks/observability/telemetry/docs/PARTITION_ARCHITECTURE.md`（正确方案：显式写 `*_default`，月度分区 DETACHED）

---

## 部署步骤

### 本地环境（已验证）
```bash
# 1. 应用 migration 337（DETACH 当月分区）
psql $DATABASE_URL -f db/migrations/337_detach_current_future_partitions.sql

# 2. 验证
psql $DATABASE_URL -c "
  SELECT c.relname, pg_get_expr(c.relpartbound, c.oid) AS partition_bound
  FROM pg_class c
  JOIN pg_inherits i ON c.oid = i.inhrelid
  JOIN pg_class p ON i.inhparent = p.oid
  WHERE p.relname = 'request_logs'
  ORDER BY c.relname;
"
# 预期：只有 request_logs_default（以及可能的历史分区）

# 3. 写入测试
psql $DATABASE_URL -c "
  INSERT INTO request_logs_default (request_id, ts, tenant_id, success)
  VALUES ('test-final', NOW(), 'test-tenant-final', true);
"
# 预期：成功
```

### 184/71 生产环境
1. **先应用 migration 332-336**（补齐 `*_default` 分区 + 注册 promote 函数）
2. **再应用 migration 337**（DETACH 当月分区）
3. **切换 Go 代码上线**（已就绪，所有写入已指向 `*_default`）
4. **启动后台迁移器**（`bg/partition_manager.go` 自动运行）

**注意**：
- migration 337 是**幂等的**（DO $$ IF EXISTS ... END $$ 保护）
- 如需回滚，执行 `337_detach_current_future_partitions.down.sql`（重新 ATTACH），但会恢复原问题

---

## 关键经验

1. **PostgreSQL DEFAULT 分区约束是动态的** — 这是问题根源
2. **99.9% 的写入是新数据** — 无需为罕见场景增加复杂度
3. **坚定执行"显式写 *_default"规范** — 效果一致，成本降低 95%
4. **文档 > 代码** — 确保可复制、可传承

---

## 受影响的文件

### 新增
- `db/migrations/336_promote_default_to_partition_functions.sql` + `.down.sql`
- `db/migrations/337_detach_current_future_partitions.sql` + `.down.sql`
- `docs/partition/README.md`
- `docs/partition/partition-background.md`
- `docs/partition/partition-architecture.md`
- `docs/partition/partition-standards.md`
- `docs/partition/partition-test-cases.md`
- `domains/hooks/observability/telemetry/docs/PARTITION_ARCHITECTURE.md`

### 修改
- `bg/partition_manager.go`（增加 promote 逻辑）
- `bg/partition_manager_test.go`（更新断言）
- `admin/data_lifecycle_partition_test.go`（删除废弃断言）
- `domains/hooks/observability/telemetry/client_test.go`（强制 `*_default` 断言）
- `version.json`（build_seq 将在下次 deploy 时递增）

### 删除
- `domains/hooks/observability/telemetry/docs/2026-07-04-partition-table-fix.md`

---

**状态**：✅ 本地验证通过，待部署到 184/71 环境
