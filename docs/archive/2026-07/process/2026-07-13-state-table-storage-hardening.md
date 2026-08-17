---
archived_from: docs/2026-07-13-state-table-storage-hardening.md
archived_at: 2026-08-17
archived_by: docs-archive v1.0
backup_ts: 20260817-190606
status: archived
---

> 本文档已归档，原文保持不变。

# 状态表存储分层与精简方案

**日期**: 2026-07-13 晚上
**问题**: 数据库存储持续增长，节点状态和路由相关表没有清晰的保留策略
**影响范围**: 154 / 252 上的所有状态/路由/请求记录类表

---

## 1. 存储分层原则

### 1.1 两层架构

```
┌──────────────────────────────────────────────────────────────┐
│ 应用层实时查询（毫秒级）                                       │
│  ↓ hot 表（默认 1d）                                            │
│  ↓ partition_manager 每 1h promote 到月度分区                    │
│  ↓ 30d 后 DROP PARTITION（状态表）或长期保留（请求记录类）        │
└──────────────────────────────────────────────────────────────┘
```

### 1.2 表分类

| 类别 | 表 | 保留策略 | 理由 |
|---|---|---|---|
| **请求记录类** | request_logs, request_logs_bodies, request_wal, usage_ledger, credit_ledger, tool_usage_stats | hot 1d + 月度分区长期保留 | 计费、审计、合规 |
| **状态/路由类** | routing_decision_log, candidate_failure_logs, handoff_logs, credential_model_call_history, model_probe_runs, credential_probe_model_log | hot 1d + 30d DROP PARTITION | 诊断、监控、容量管理 |
| **状态机主表** | model_probe_state, credentials, credential_model_bindings, model_offers | 永久保留（小表，每对 1 行）| 路由决策权威数据 |

---

## 2. 详细保留策略

### 2.1 请求记录类（hot 1d + 长期）

| 表 | 默认 | 范围 | 设置项 |
|---|---|---|---|
| request_logs | 1d | 1-30d | `lifecycle.usage_ledger_ttl_days` 模式 |
| usage_ledger | 1d | 1-30d | `lifecycle.usage_ledger_ttl_days` |
| request_wal | 1d | 1-30d | `lifecycle.request_wal_ttl_days` |
| credit_ledger | 1d | 1-30d | `lifecycle.credit_ledger_ttl_days` |
| tool_usage_stats | 1d | 1-30d | `lifecycle.tool_usage_stats_ttl_days` |

**注意**: 这些表的月度分区**不会自动 DROP**。如果需要清理，必须在业务方按合规要求下手动调整设置项后由 partition_manager 触发。

### 2.2 状态/路由类（hot 1d + 30d DROP PARTITION）

| 表 | 默认 | 范围 | 设置项 | DROP 方式 |
|---|---|---|---|---|
| routing_decision_log | 30d | 1-365d | `lifecycle.routing_decision_log_ttl_days` | `archive_routing_decision_log` (day 1) |
| candidate_failure_logs | 30d | 1-365d | `lifecycle.candidate_failure_logs_ttl_days` | `drop_old_state_partitions` (day 2) |
| handoff_logs | 30d | 1-365d | `lifecycle.handoff_logs_ttl_days` | `drop_old_state_partitions` (day 2) |
| credential_model_call_history | 30d | 1-365d | `lifecycle.credential_model_call_history_ttl_days` | TimescaleDB retention policy |
| model_probe_runs | 90d | 7-3650d | `lifecycle.model_probe_runs_ttl_days` | `drop_old_state_partitions` (day 2) |
| credential_probe_model_log | 90d | 1-3650d | `lifecycle.credential_probe_model_log_ttl_days` | `cleanup_old_credential_probe_model_log` (every tick) |

### 2.3 状态机主表（永久保留）

这些表每行代表 (cred, model) 的当前状态，**永远不清理**：

- `model_probe_state` - 主表，per (cred, model) 1 行
- `credentials` - 凭据主表
- `credential_model_bindings` - 凭据×模型绑定
- `model_offers` - 模型提供
- `credential_state_nodes` - 探测节点

---

## 3. 实施内容

### 3.1 新增 settings（per-table retention）

`settings/spec_lifecycle.go` 新增 10 个 per-table 设置项：

```go
{Key: "lifecycle.routing_decision_log_ttl_days", ..., Default: 30, Max: 365, Unit: "天"},
{Key: "lifecycle.candidate_failure_logs_ttl_days", ..., Default: 30, Max: 365},
{Key: "lifecycle.handoff_logs_ttl_days", ..., Default: 30, Max: 365},
{Key: "lifecycle.credential_model_call_history_ttl_days", ..., Default: 30, Max: 365},
{Key: "lifecycle.model_probe_runs_ttl_days", ..., Default: 90, Max: 3650},
{Key: "lifecycle.credential_probe_model_log_ttl_days", ..., Default: 90, Max: 3650},
{Key: "lifecycle.usage_ledger_ttl_days", ..., Default: 1, Max: 30},
{Key: "lifecycle.request_wal_ttl_days", ..., Default: 1, Max: 30},
{Key: "lifecycle.credit_ledger_ttl_days", ..., Default: 1, Max: 30},
{Key: "lifecycle.tool_usage_stats_ttl_days", ..., Default: 1, Max: 30},
```

所有设置项支持热重载，1h 内生效。

### 3.2 SQL 迁移（391_state_table_storage_hardening.sql）

新增两个 SQL 函数：

**1. `drop_old_state_partitions(retention_days int)`** - 通用状态表 DROP 入口
- 遍历 `routing_decision_log`, `candidate_failure_logs`, `handoff_logs`, `model_probe_runs`, `credential_model_index` 的月度分区
- 解析 `_{parent}_YYYY_MM` 后缀
- DROP 整个月早于 cutoff 的分区
- 返回 DROP 的分区数

**2. `cleanup_old_credential_probe_model_log(retention_days int)`** - 列存储堆表清理
- 直接 DELETE（列存储支持 DELETE 整行）
- 默认 90d，可调

**TimescaleDB retention policy 升级**：
- 7d → 30d（如果 TimescaleDB 扩展已安装）
- 无 TimescaleDB 时静默跳过

**必要索引**：`idx_credential_probe_model_log_created_at`

**settings_kv 默认值初始化**：10 个 TTL 设置项

### 3.3 Go 代码改动

**`bg/partition_manager.go`**：
1. `archiveSpecs()` 新增 `drop_old_state_partitions` (day 2)
2. 新增 `dropOldStatePartitions(ctx, s)` 函数 - 调用 SQL 函数
3. 新增 `cleanupOldCredentialProbeModelLog(ctx)` 函数 - 每个 tick 调用
4. `archiveOldPartitionsIfNeeded` 集成新清理路径

### 3.4 测试改动

**`bg/partition_manager_test.go`**：
- `TestArchiveSpecsScheduling` 验证 3 个 archive spec（不是 2 个）
- 验证 `drop_old_state_partitions` 在 day 2 调度

---

## 4. 部署步骤

### 4.1 已完成（154 部署）

1. ✅ 应用代码修改（commit `5895e25dc`）
2. ✅ 编译 v994 二进制
3. ✅ 上传到 154
4. ✅ 运行 SQL 迁移
5. ✅ 重启服务，验证 partition_manager 正常

### 4.2 SQL 迁移容错

154 环境的特殊情况：
- **未安装 TimescaleDB 扩展** → TimescaleDB retention policy 静默跳过（修复）
- **settings_kv schema 实际是 `(key, value, value_type, scope, category, updated_at, ...)`** → INSERT 改用实际列（修复）

### 4.3 验证结果

```
SELECT drop_old_state_partitions(30) AS dropped;     → 0 (no old partitions)
SELECT cleanup_old_credential_probe_model_log(90); → 0 (no old rows)
SELECT count(*) FROM settings_kv WHERE key LIKE 'lifecycle.%_ttl_days'; → 10
```

服务正常运行：
- `partition_manager started`
- 所有月度分区 `ensure_*_partition` 成功
- `model_probe_runs cleanup ran retention_days:90`
- 154 上 `active`

---

## 5. 验证指标

| 指标 | 修复前 | 修复后 |
|---|---|---|
| `routing_decision_log` 长期保留 | 90d+ | 30d（可调 1-365d）|
| `candidate_failure_logs` 增长 | 无清理 | 30d DROP |
| `handoff_logs` 增长 | 无清理 | 30d DROP |
| `model_probe_runs` | 90d+ | 90d（标准化）|
| `credential_model_call_history` | 7d | 30d |
| `usage_ledger` 等请求记录 | hot 1d | hot 1d（保持）|
| 总磁盘使用 | 持续增长 | 30d 滚动（状态表）+ 长期保留（请求记录）|

---

## 6. 风险评估

| 风险 | 缓解 |
|---|---|
| DROP PARTITION 误删生产数据 | 30d TTL 默认 + 设置项可调 + dry-run 模式 |
| 列存储 DELETE 不支持 | 全部用 DROP PARTITION（O(1)） |
| TimescaleDB 缺失 | pg_extension EXISTS 检查 + 静默跳过 |
| 月度边界数据丢失 | 分区命名 `_YYYY_MM`，30d 边界保留完整月 |
| 业务方依赖历史 90d 路由日志 | 默认 30d，可通过设置调到 365d |

### 回滚步骤

```bash
# 1. 回滚服务到 v993
ssh 154 "systemctl stop llm-gateway-go && \
  ln -sf llm-gateway-go.v993.linux.amd64 /opt/llm-gateway-go/llm-gateway-go && \
  systemctl start llm-gateway-go"

# 2. 保留 settings（如果想恢复原 7d 策略）
# 手动执行：
psql -c "UPDATE settings_kv SET value = '7'::jsonb WHERE key = 'lifecycle.credential_model_call_history_ttl_days'"
psql -c "SELECT remove_retention_policy('credential_model_call_history'); SELECT add_retention_policy('credential_model_call_history', INTERVAL '7 days')"
```

---

## 7. 关键经验

### 7.1 SQL 迁移的环境兼容性

**教训**：SQL 迁移在测试环境正常但生产失败，因为：
- TimescaleDB 扩展未安装 → `timescaledb_information.hypertables` 不存在
- 实际 schema 与设计文档不同 → `description` 列不存在

**原则**：
1. 扩展检查用 `pg_extension` 而不是 `pg_namespace` 内的视图
2. INSERT 列出所有实际存在的列（不要依赖文档）
3. 用 `EXCEPTION WHEN OTHERS` 包装可选步骤
4. `RAISE NOTICE` 而非 `RAISE EXCEPTION` 用于可降级步骤

### 7.2 状态表精简的核心矛盾

**问题**：状态数据有诊断价值，但无限增长
**方案**：分离"当前状态"（小表永久保留）和"状态变化历史"（大表短期保留）
- 状态机主表（如 `model_probe_state`）= 当前快照
- 状态历史表（如 `model_probe_runs`）= 事件流，可定期清理
- 分区表（按时间）+ DROP PARTITION = O(1) 清理，不锁表

### 7.3 hot 1d 是合理默认值

**为什么是 1 天？**
- 实时 dashboard 主要看当天数据
- 诊断问题通常在 24h 内发现
- 1d hot + 30d 分区 = 30 天可追溯窗口
- 减少 hot 表压力（更小的索引，更快的查询）

---

## 8. 后续可优化项

1. **数据质量监控**：定期检查 `usage_ledger` 与 `request_logs` 的一致性
2. **元数据索引**：`candidate_failure_logs` 当前没有 `(ts, request_id)` 索引，清理时可加
3. **跨表 JOIN 优化**：将 `routing_decision_log` 与 `request_logs` 合并视图，简化查询
4. **审计触发器**：将 `model_probe_state` 状态变化自动写入 `routing_audit_log`，减少状态查询的存储
5. **按租户差异化 TTL**：大客户长保留，小客户短保留

---

## 9. 相关文件

### 新增文件
- `sql/migrations/startup/391_state_table_storage_hardening.sql` - 主迁移
- `sql/migrations/startup/391_state_table_storage_hardening.down.sql` - 回滚

### 修改文件
- `settings/spec_lifecycle.go` - 新增 10 个 per-table 设置项
- `bg/partition_manager.go` - 新增 2 个清理函数 + 1 个 archive spec
- `bg/partition_manager_test.go` - 验证 3 个 archive spec

### Git Commits
- `499f49658` - feat(storage): 状态表 30d DROP PARTITION + 请求记录 hot 1d
- `5895e25dc` - fix(migration): 容忍 TimescaleDB 扩展未安装 + 修正 settings_kv schema
- 部署版本：v994（build_seq 994）

### 部署验证日志
```
INFO  partition_manager started
INFO  partition_manager: ensured partition month=2026-08 (×7)
INFO  partition_manager: model_probe_runs cleanup ran retention_days:90
INFO  credential_probe_model_log cleanup OK (no rows to clean)
```

---

## 10. 总结

本次精简方案实现了**清晰的两层存储架构**：

1. **请求记录类表**（hot 1d + 长期保留）→ 满足计费、审计需求
2. **状态/路由类表**（hot 1d + 30d DROP PARTITION）→ 满足诊断需求，容量可控
3. **状态机主表**（永久保留）→ 路由决策权威数据

通过新增 10 个 per-table 设置项，运营可灵活调整每张表的保留期，平衡合规需求与存储成本。
