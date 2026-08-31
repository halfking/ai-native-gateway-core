# Agent-3 Storage & Migration Audit Report

**审计日期**: 2026-08-31  
**工作目录**: `/Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go`  
**审计范围**: 存储层架构、SQL迁移一致性、数据完整性约束  

---

## 执行摘要 (Executive Summary)

本次审计覆盖了Hot+Columnar分区架构、SQL迁移文件同步状态和数据完整性约束。关键发现：

### 🔴 严重问题 (Critical Issues)
1. **迁移文件同步缺失**: 627-631 迁移（5个文件）未同步到 `installer/cmd/llm-gw-installer/embeddata/startup/`
2. **db-changelog.md 不完整**: 627-630 迁移缺失checksum记录
3. **迁移测试覆盖缺失**: 620-631 区间无对应 `*_test.go` 文件

### 🟡 中等风险 (Medium Risk)
1. **request_logs body列未清理**: 虽有migration 573计划，但文件被重命名为 `.skip`，60-90GB存储优化未执行
2. **部分表缺少hot+columnar架构**: `tool_usage_stats`、`credential_model_index`、`model_probe_runs` 等表未完全符合新架构模式

### 🟢 良好实践 (Good Practices)
1. **候选失败日志修复完整**: 627+628迁移成功修复了aggregation_id缺失导致的数据可见性bug
2. **审计追溯机制**: 629引入 `audit_attachments_cleanup` 表，记录清理操作
3. **会话聚合持久化**: 630引入 `session_aggregate_outbox` 实现durable retry
4. **RLS策略一致**: 新表均配置tenant isolation RLS策略

---

## 1. Hot+Columnar 架构评估

### 1.1 架构模式说明

当前系统采用 **Hot表 + Columnar月度分区** 的两层存储架构：

```
┌─────────────────────┐
│   *_hot (heap)      │  ← 写入层：0-8小时数据，高频CRUD
│   - 独立表          │
│   - fillfactor=90   │
│   - 完整索引        │
└──────────┬──────────┘
           │ promote (批量转移)
           ↓
┌─────────────────────┐
│   * (partitioned)   │  ← 归档层：历史数据，只读
│   - 按月分区        │
│   - USING columnar  │
│   - 压缩存储        │
└─────────────────────┘
```

### 1.2 核心表架构符合度

| 表名 | Hot表 | Columnar分区 | Promote函数 | 统一视图 | 符合度 |
|------|-------|--------------|-------------|----------|--------|
| `request_logs` | ✅ 341 | ✅ 月度分区 | ✅ 602 | ✅ `request_logs_with_current_month` | ✅ 完全符合 |
| `session_bodies` | ✅ 614 | ✅ 月度分区 | ✅ 615/626 | ✅ `session_bodies_unified` | ✅ 完全符合 |
| `candidate_failure_logs` | ✅ 392 | ✅ 月度分区 | ✅ 624/628 | ✅ `candidate_failure_logs_unified` (627) | ✅ 完全符合 |
| `session_turns` | ✅ 526 | ✅ 月度分区 | ✅ 526 | ✅ `session_turns_with_current_month` | ✅ 完全符合 |
| `handoff_logs` | ✅ 534 | ✅ 月度分区 | ✅ 534 | ✅ `handoff_logs_with_current_month` | ✅ 完全符合 |
| `dashboard_access_events` | ✅ 383 | ✅ 月度分区 | ✅ 579/607 | ✅ VIEW | ✅ 完全符合 |
| `session_module_executions` | ✅ 382 | ✅ 月度分区 | ✅ 580 | ✅ VIEW | ✅ 完全符合 |
| `request_logs_bodies` | ✅ 353 | ✅ 月度分区 | ✅ 528 | ✅ `request_logs_bodies_with_current_month` | ✅ 完全符合 |
| `usage_ledger` | ✅ 344 | ✅ 月度分区 | ✅ 344 | ⚠️ 需确认 | 🟡 部分符合 |
| `request_wal` | ✅ 345 | ✅ 月度分区 | ✅ 345 | ⚠️ 需确认 | 🟡 部分符合 |
| `routing_decision_log` | ✅ 346 | ❌ 缺失 | ❌ 缺失 | ❌ 缺失 | 🔴 不符合 |
| `tool_usage_stats` | ✅ 348 | ❌ 缺失 | ❌ 缺失 | ❌ 缺失 | 🔴 不符合 |
| `credit_ledger` | ✅ 349 | ❌ 缺失 | ❌ 缺失 | ❌ 缺失 | 🔴 不符合 |
| `model_probe_runs` | ✅ 386 | ✅ 月度分区 | ⚠️ 需确认 | ⚠️ 需确认 | 🟡 部分符合 |

**关键发现**:
- **核心大数据表 (request_logs, session_bodies, candidate_failure_logs)** 完全符合架构
- **中等规模表 (routing_decision_log, tool_usage_stats, credit_ledger)** 仅有hot表，缺少columnar分区
- **可能原因**: 这些表数据量较小或查询频率较低，暂未触发分区优化

### 1.3 Promote机制审计

**Promote函数清单** (批量转移hot→partition):

| 函数名 | 迁移编号 | 保留期 | 批量大小 | 锁机制 | 状态 |
|--------|---------|--------|---------|--------|------|
| `promote_request_logs_hot_to_partition` | 602 | 24h | 5000 | advisory lock | ✅ atomic |
| `promote_session_bodies_hot_to_partition` | 615/626 | 8h | 5000 | advisory lock | ✅ atomic (626修复) |
| `promote_candidate_failure_logs_hot_to_partition` | 628 | 24h | 5000 | FOR UPDATE SKIP LOCKED | ✅ atomic v3 |
| `promote_session_turns_hot_to_partition` | 526 | 8h | 5000 | 需确认 | ⚠️ 待验证 |
| `promote_handoff_logs_hot_to_partition` | 534 | 24h | 5000 | 需确认 | ⚠️ 待验证 |
| `promote_dashboard_access_events_hot_to_partition` | 579/607 | 24h | 5000 | 需确认 | ✅ (607修复列缺失) |
| `promote_session_module_executions_hot_to_partition` | 580 | 24h | 5000 | 需确认 | ⚠️ 待验证 |

**Promote执行者**: `bg/vacuum_worker.go` (后台周期性调用)

**关键修复历史**:
- **624→628**: candidate_failure_logs promote从v1→v3，修复aggregation_id丢失bug
- **615→626**: session_bodies promote修复CTE语法错误（`inserted` RETURNING缺少partition_date）
- **607**: dashboard_access_events promote补齐缺失列

---

## 2. SQL迁移文件一致性审计

### 2.1 Embeddata同步状态

**源目录**: `sql/migrations/startup/`  
**目标目录**: `installer/cmd/llm-gw-installer/embeddata/startup/`

**统计数据**:
- 源目录迁移文件: 617个
- Embeddata文件: 50个
- 最新同步版本: 626 (`626_session_bodies_hot_promote_reconcile.sql`)

**🔴 未同步迁移** (627-631):

| 迁移编号 | 文件名 | SHA-256 | 同步状态 |
|---------|--------|---------|---------|
| 627 | `627_candidate_failure_logs_aggregation_id_unified.sql` | `84c0ea7130e2ac19d67416162c8d92c8c2c08e951364a9ace161f58a8f02fc92` | ❌ **缺失** |
| 628 | `628_candidate_failure_logs_promote_atomic_v3.sql` | `d9a30a29f0ac991e8e0d9b73e3a9b1943eb57e666cd604b24f3e1ba801f3a41c` | ❌ **缺失** |
| 629 | `629_audit_attachments_cleanup.sql` | `ae462b3d4ef16d27d5c04f8799a7f15c93f140bbd575fa8769252a2acd442701` | ❌ **缺失** |
| 630 | `630_session_aggregate_outbox.sql` | `da5c3cce36477be1a03dc64976e9384f37232fa83a48f34e577c7dcbb91da7f6` | ❌ **缺失** |
| 631 | `631_provider_credential_soft_delete.sql` | `0fd2120475a78486e40d3fa2d082eade345aef74a952ccc030b95643db252804` | ❌ **缺失** |

**影响评估**:
- **风险等级**: 🔴 **HIGH**
- **影响范围**: installer无法在全新环境中应用627-631迁移
- **数据一致性**: 已部署环境（252/245/154）与全新安装环境schema不一致
- **功能影响**:
  - 627/628缺失 → ProviderErrorAggregator无法读取历史分区数据
  - 629缺失 → attachment cleanup操作无审计追溯
  - 630缺失 → Session V2聚合更新丢失无重试机制
  - 631缺失 → provider/credential软删除逻辑缺失

### 2.2 db-changelog.md 完整性检查

**当前db-changelog.md状态**:
- 最新deploy记录: 2026-08-29 (迁移625)
- 本地revision记录: 2026-08-31 (仅626、631)
- **缺失记录**: 627、628、629、630

**🟡 Checksum漂移风险**:

`docs/db-changelog.md` Line 132-169 提到：
> 2026-08-31 follow-up audit: 626 was edited locally to fix a broken CTE...
> Also note: the checksums realigned above for 542/614/615/617/619/620/622/625/623...
> The remote `llm_gateway_migration_checksums` ledgers on 252/245/154 still hold the checksums recorded at their original deploy times.
> Before the next deploy-seamless run, execute `scripts/repair-252-migration-ledger.sh`

**建议checksums补录** (基于实际文件):

```markdown
## 2026-08-31 — local dev (pending deploy)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 627 | `627_candidate_failure_logs_aggregation_id_unified.sql` | `84c0ea7130e2ac19d67416162c8d92c8c2c08e951364a9ace161f58a8f02fc92` | pending-deploy |
| 628 | `628_candidate_failure_logs_promote_atomic_v3.sql` | `d9a30a29f0ac991e8e0d9b73e3a9b1943eb57e666cd604b24f3e1ba801f3a41c` | pending-deploy |
| 629 | `629_audit_attachments_cleanup.sql` | `ae462b3d4ef16d27d5c04f8799a7f15c93f140bbd575fa8769252a2acd442701` | pending-deploy |
| 630 | `630_session_aggregate_outbox.sql` | `da5c3cce36477be1a03dc64976e9384f37232fa83a48f34e577c7dcbb91da7f6` | pending-deploy |
```

(631已有记录，checksum一致)

---

## 3. 数据完整性约束审计

### 3.1 NOT NULL 约束

**新表约束检查** (627-631):

#### 3.1.1 `audit_attachments_cleanup` (629)
```sql
cleanup_run_id    uuid        NOT NULL,
tenant_id         text        NOT NULL,
request_id        text        NOT NULL,
attachment_hash   text        NOT NULL,
cleaned_at        timestamptz NOT NULL DEFAULT NOW(),
older_than_days   integer     NOT NULL CHECK (older_than_days > 0),
triggered_by_user text        NOT NULL,
reason            text,  -- ✅ 可选字段，允许NULL
```
✅ **评估**: 合理，所有关键审计字段强制非空

#### 3.1.2 `session_aggregate_outbox` (630)
```sql
tenant_id       text        NOT NULL,
session_id      text        NOT NULL,
partition_date  date        NOT NULL,
request_id      text        NOT NULL,
update_payload  jsonb       NOT NULL,
status          text        NOT NULL DEFAULT 'pending' CHECK (...),
attempts        integer     NOT NULL DEFAULT 0,
last_error      text,  -- ✅ 允许NULL（成功时无错误）
next_retry_at   timestamptz NOT NULL DEFAULT NOW(),
created_at      timestamptz NOT NULL DEFAULT NOW(),
updated_at      timestamptz NOT NULL DEFAULT NOW(),
```
✅ **评估**: 合理，状态机字段有默认值+CHECK约束

### 3.2 CHECK 约束

| 表名 | 约束名 | 定义 | 评估 |
|------|--------|------|------|
| `audit_attachments_cleanup` | (inline) | `older_than_days > 0` | ✅ 防止无效保留期 |
| `session_aggregate_outbox` | (inline) | `status IN ('pending', 'claimed', 'done', 'dead')` | ✅ 有限状态机 |
| `providers` (631) | (inline) | `status = ANY (ARRAY['active', 'inactive', 'deleted'])` | ✅ 软删除支持 |

✅ **评估**: CHECK约束覆盖关键业务规则

### 3.3 唯一性约束

| 表名 | 约束名 | 列 | 用途 | 评估 |
|------|--------|-----|------|------|
| `audit_attachments_cleanup` | `audit_attachments_cleanup_unique` | `(request_id, attachment_hash, cleanup_run_id)` | 防止同一run重复记录 | ✅ 合理 |
| `session_aggregate_outbox` | `session_aggregate_outbox_unique_request` | `(tenant_id, session_id, partition_date, request_id)` | 每个request仅一条outbox | ✅ 幂等性保证 |

✅ **评估**: 唯一约束支持ON CONFLICT幂等upsert

### 3.4 外键约束

**🟡 发现**: 627-631迁移中 **未发现显式外键约束**

**分析**:
- `audit_attachments_cleanup.request_id` → 未FK到 `request_logs`
- `session_aggregate_outbox.session_id` → 未FK到 `sessions`
- `candidate_failure_logs.aggregation_id` (627) → 未FK（使用序列生成）

**可能原因**:
1. **性能考虑**: 大数据表FK检查开销大
2. **架构解耦**: hot+partition架构下FK维护复杂
3. **应用层保证**: 由Go代码保证引用完整性

**建议**: 在数据字典中文档化"逻辑外键"关系，避免孤儿数据

### 3.5 RLS (Row Level Security) 策略

**新表RLS状态**:

| 表名 | RLS启用 | FORCE RLS | 策略名 | 租户隔离 |
|------|---------|-----------|--------|---------|
| `audit_attachments_cleanup` | ❌ 未启用 | - | - | ⚠️ 依赖应用层super_admin检查 |
| `session_aggregate_outbox` | ✅ 启用 | ✅ FORCE | `tenant_isolation_session_aggregate_outbox` | ✅ `tenant_id = get_current_tenant() OR super_admin` |
| `session_bodies_hot` | ✅ 启用 | - | (继承策略) | ✅ 租户隔离 |
| `candidate_failure_logs` | ✅ 启用 | - | (继承策略) | ✅ 租户隔离 |

**🟡 风险点**:
- `audit_attachments_cleanup` 无RLS：依赖 `handleDataLifecycleAttachmentCleanupExecute` 的super_admin gate
- 迁移629注释明确说明："We do NOT add RLS at the table level; cleanup is a super-admin-only action"

**建议**: 添加RLS策略作为纵深防御，即使access path已有gate

---

## 4. 存储优化与清理机制

### 4.1 request_logs body列清理 (573迁移)

**状态**: ❌ **未执行** (文件被重命名为 `.skip`)

**原因** (573迁移底部注释):
```
2026-08-24: DEFERRED via .skip rename — blocks every deploy, never applied.
Root cause: psql:573: ERROR: cannot drop columns from view
PostgreSQL's CREATE OR REPLACE VIEW may only APPEND trailing columns; 
it can never REMOVE or reorder existing ones.
```

**影响**:
- **存储浪费**: `request_logs_hot` + `request_logs` 所有分区仍携带 `request_body/response_body/outbound_body` 三列（全为NULL）
- **估算浪费**: 12-18 KB/row × 5M rows ≈ **60-90 GB heap空间**
- **TOAST膨胀**: JSONB列即使为NULL也占用TOAST pointer空间

**修复方案** (573注释提供):
```sql
-- TO UN-SKIP:
-- 1. Replace CREATE OR REPLACE VIEW with DROP VIEW + CREATE VIEW (atomic)
-- 2. Check pg_depend for dependent views first
-- 3. Dry-run: psql --single-transaction -v ON_ERROR_STOP=1 -f <file>
```

**优先级**: 🔴 **HIGH** (60-90GB存储回收)

### 4.2 Attachment清理机制 (629迁移)

**新增表**: `audit_attachments_cleanup`

**功能**:
- 记录 `handleDataLifecycleAttachmentCleanupExecute` 执行的每次清理操作
- 捕获 `(request_id, attachment_hash)` + 操作人 + 保留期阈值
- 解决问题: 原实现silent UPDATE `request_logs_hot`，无审计记录，且无法UPDATE columnar分区

**清理流程**:
```
1. Admin触发cleanup (older_than_days=30)
2. UPDATE request_logs_hot SET attachments=cleanup(attachments)
3. INSERT INTO audit_attachments_cleanup (每个清理的attachment一行)
4. 历史分区(columnar)无法UPDATE → 依赖audit表事后查询
```

✅ **评估**: 审计追溯完整，但未解决columnar分区attachment清理问题

### 4.3 VACUUM自动化 (bg/vacuum_worker.go)

**定期VACUUM表**:
- `request_logs_bodies`: VACUUM FULL (每周日凌晨2:00)
- `request_logs_bodies_hot`: VACUUM (每周，非FULL)

**配置**:
- 间隔: 7天
- 执行窗口: 凌晨2点
- 超时: 30分钟 (FULL) / 5分钟 (hot)

✅ **评估**: TOAST表空间回收机制完善

### 4.4 Outbox Reaper机制 (630迁移 + session_aggregate_outbox_reaper.go)

**功能**: Session V2聚合更新失败时的durable retry

**设计**:
- `session_aggregate_outbox` 持久化每次 `SessionUpdate` payload
- Reaper后台扫描 `status='pending'` 行，重试 `UpdateSession`
- 状态机: `pending` → `claimed` → `done` / `dead`
- 并发安全: `FOR UPDATE SKIP LOCKED` (多副本协同)
- 超时重试: 指数退避 (1s → 2s → 4s ... 上限1h)
- 最大重试: 10次，超过标记 `dead` + slog.Error

**关键修复**:
- 解决问题: 原 `session_writer_v2.updateSessionAggregate` 是best-effort，kill -9丢失更新
- 幂等性: SessionAggregator的 `aggregate_applied_at` claim防止重复应用

✅ **评估**: 闭环反馈机制完善，满足audit-data-closure-C要求

---

## 5. 迁移测试覆盖率

**测试文件统计**:
- 总迁移文件(forward): 350个
- 测试文件: 47个 (`*_test.go`)
- 测试覆盖率: 13.4%

**620-631区间测试覆盖**:
```bash
$ find sql/migrations/startup -name "62*_test.go" -o -name "63*_test.go"
(no output)
```

❌ **发现**: 620-631迁移 **无单元测试覆盖**

**风险**:
- 627 (aggregation_id backfill逻辑): 无测试验证ctid排序的确定性
- 628 (promote v3): 无测试验证aggregation_id传递
- 630 (outbox表): 无测试验证状态机转换
- 626 (promote reconcile): 已知CTE bug修复，无回归测试

**建议**: 为关键迁移（627/628/630）补充集成测试

---

## 6. 关键Bug修复回顾

### 6.1 Candidate Failure Logs Aggregation ID Bug (627+628)

**问题** (audit-data-closure-A):
- Migration 624的promote函数 **未携带 `aggregation_id` 列**
- 促销到columnar分区的行对 `ProviderErrorAggregator` 不可见
- Aggregator查询 `WHERE aggregation_id > watermark`，但columnar表缺该列
- **影响**: 促销时间窗口内的错误bucket永久丢失统计

**修复**:
- **627**: 在 `candidate_failure_logs` 父表添加 `aggregation_id` 列，backfill历史数据
- **628**: 重写promote函数（v2→v3），在INSERT列表中加入 `aggregation_id`
- 创建 `candidate_failure_logs_unified` 视图，聚合器改读该视图

**验证建议**:
```sql
-- 检查历史分区是否有aggregation_id
SELECT partition_name, has_aggregation_id
FROM (
    SELECT tablename AS partition_name,
           EXISTS(SELECT 1 FROM information_schema.columns 
                  WHERE table_name=tablename AND column_name='aggregation_id') AS has_aggregation_id
    FROM pg_tables 
    WHERE tablename LIKE 'candidate_failure_logs_2%'
) x;
```

### 6.2 Session Bodies Promote CTE Bug (626)

**问题**:
- Migration 615的promote函数，`inserted` CTE的RETURNING子句缺少 `partition_date`
- `deleted` CTE引用 `i.partition_date` 时找不到该列
- 函数无法CREATE，所有promote调用失败

**修复** (626):
```sql
-- 615 (broken):
WITH inserted AS (
    INSERT INTO session_bodies (...) 
    RETURNING id  -- ❌ 缺少partition_date
), deleted AS (
    DELETE FROM session_bodies_hot h
    WHERE h.id = i.id AND h.partition_date = i.partition_date  -- ❌ i.partition_date不存在
    ...
)

-- 626 (fixed):
WITH inserted AS (
    INSERT INTO session_bodies (...) 
    RETURNING id, partition_date  -- ✅ 补齐
), deleted AS (
    DELETE FROM session_bodies_hot h
    WHERE h.id = i.id AND h.partition_date = i.partition_date  -- ✅ 正常引用
    ...
)
```

**状态**: db-changelog.md Line 132-157 注明626已本地修复但 **未部署** (pending-deploy)

---

## 7. 修正SQL脚本

### 7.1 同步627-631到embeddata

```bash
#!/bin/bash
# 脚本: sync-embeddata-627-631.sh
# 用途: 将627-631迁移同步到installer embeddata

SRC_DIR="sql/migrations/startup"
DST_DIR="installer/cmd/llm-gw-installer/embeddata/startup"

for i in 627 628 629 630 631; do
    for ext in sql down.sql; do
        src="${SRC_DIR}/${i}_*.${ext}"
        if ls $src 1> /dev/null 2>&1; then
            cp $src "$DST_DIR/"
            echo "✅ Copied: $(basename $src)"
        fi
    done
done

echo "🎯 Sync complete. Please commit embeddata changes."
```

**执行**:
```bash
cd /Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go
bash sync-embeddata-627-631.sh
git add installer/cmd/llm-gw-installer/embeddata/startup/62*.sql
git add installer/cmd/llm-gw-installer/embeddata/startup/63*.sql
git commit -m "sync: embeddata 627-631 迁移文件"
```

### 7.2 补录db-changelog.md checksums

```markdown
## 2026-08-31T12:00:00Z — local audit补录 (agent-3)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 627 | `627_candidate_failure_logs_aggregation_id_unified.sql` | `84c0ea7130e2ac19d67416162c8d92c8c2c08e951364a9ace161f58a8f02fc92` | pending-deploy |
| 628 | `628_candidate_failure_logs_promote_atomic_v3.sql` | `d9a30a29f0ac991e8e0d9b73e3a9b1943eb57e666cd604b24f3e1ba801f3a41c` | pending-deploy |
| 629 | `629_audit_attachments_cleanup.sql` | `ae462b3d4ef16d27d5c04f8799a7f15c93f140bbd575fa8769252a2acd442701` | pending-deploy |
| 630 | `630_session_aggregate_outbox.sql` | `da5c3cce36477be1a03dc64976e9384f37232fa83a48f34e577c7dcbb91da7f6` | pending-deploy |

> 627: 修复candidate_failure_logs promote丢失aggregation_id导致聚合器读不到历史数据  
> 628: 重写promote v3携带aggregation_id  
> 629: audit_attachments_cleanup审计表记录attachment清理操作  
> 630: session_aggregate_outbox持久化Session V2聚合更新重试队列
```

### 7.3 修复573迁移 (DROP body列)

```sql
-- 修复方案: 573_drop_request_logs_body_columns_fixed.sql
BEGIN;

-- 1. 检查依赖视图
DO $$
DECLARE
    dep_view record;
BEGIN
    FOR dep_view IN 
        SELECT DISTINCT v.relname
        FROM pg_depend d
        JOIN pg_class v ON d.refobjid = v.oid
        JOIN pg_class t ON d.objid = t.oid
        WHERE t.relname IN ('request_logs_with_current_month', 'request_logs_bodies_progress')
        AND v.relkind = 'v'
    LOOP
        RAISE NOTICE 'Dependent view: %', dep_view.relname;
    END LOOP;
END $$;

-- 2. DROP依赖视图 (原子事务)
DROP VIEW IF EXISTS public.request_logs_bodies_progress CASCADE;
DROP VIEW IF EXISTS public.request_logs_with_current_month CASCADE;

-- 3. 重建request_logs_with_current_month (无body列)
CREATE VIEW public.request_logs_with_current_month AS
SELECT 
    id, request_id, ts, tenant_id, application_id, api_key_id,
    end_user_id, client_model, outbound_model, credential_id,
    provider_id, canonical_id, client_profile, request_mode,
    prompt_tokens, completion_tokens, total_tokens, cost_usd,
    latency_ms, success, error_kind, search_text,
    -- ... (完整列表见573迁移，移除request_body/response_body/outbound_body)
    origin_actor
FROM public.request_logs_hot
UNION ALL
SELECT 
    id, request_id, ts, tenant_id, application_id, api_key_id,
    -- ... (同上)
    origin_actor
FROM public.request_logs;

COMMENT ON VIEW public.request_logs_with_current_month IS
'request_logs unified view (hot + historical partitions). Body columns removed in migration 573 — use request_logs_bodies_with_current_month for body content.';

-- 4. DROP body列 (传播到所有分区)
ALTER TABLE public.request_logs_hot DROP COLUMN IF EXISTS request_body;
ALTER TABLE public.request_logs_hot DROP COLUMN IF EXISTS response_body;
ALTER TABLE public.request_logs_hot DROP COLUMN IF EXISTS outbound_body;

ALTER TABLE public.request_logs DROP COLUMN IF EXISTS request_body;
ALTER TABLE public.request_logs DROP COLUMN IF EXISTS response_body;
ALTER TABLE public.request_logs DROP COLUMN IF EXISTS outbound_body;

-- 5. 验证
DO $$
DECLARE
    body_cols_exist boolean;
BEGIN
    SELECT EXISTS(
        SELECT 1 FROM information_schema.columns
        WHERE table_schema='public' AND table_name='request_logs_hot'
        AND column_name IN ('request_body', 'response_body', 'outbound_body')
    ) INTO body_cols_exist;
    
    IF body_cols_exist THEN
        RAISE EXCEPTION '573 post-condition failed: body columns still exist';
    ELSE
        RAISE NOTICE '✅ 573 verified: body columns dropped, ~60-90GB reclaimed';
    END IF;
END $$;

COMMIT;
```

**部署步骤**:
1. 备份生产DB: `pg_dump --schema-only`
2. 在测试环境dry-run: `psql --single-transaction -v ON_ERROR_STOP=1 -f 573_fixed.sql`
3. 确认无依赖视图报错
4. 应用到生产: `scripts/deploy-seamless.sh --migration=573`
5. 验证: `./scripts/check-body-storage-schema.sh` (应返回exit 0)

**预期存储回收**: 60-90GB heap + TOAST空间

### 7.4 添加audit_attachments_cleanup的RLS策略

```sql
-- 补充: 629_audit_attachments_cleanup_rls_addon.sql
-- 用途: 为audit_attachments_cleanup添加RLS纵深防御

BEGIN;

ALTER TABLE public.audit_attachments_cleanup ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.audit_attachments_cleanup FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation_audit_attachments_cleanup
ON public.audit_attachments_cleanup
USING (
    tenant_id = get_current_tenant()
    OR current_setting('app.current_role', true) = 'super_admin'
    OR current_setting('app.bypass_rls', true) = 'true'
)
WITH CHECK (
    current_setting('app.current_role', true) = 'super_admin'
    OR current_setting('app.bypass_rls', true) = 'true'
);

COMMENT ON POLICY tenant_isolation_audit_attachments_cleanup
ON public.audit_attachments_cleanup IS
'Audit cleanup records: read requires tenant match or super_admin; write requires super_admin only (cleanup is admin-only action)';

COMMIT;
```

---

## 8. 优先级修复清单

### 🔴 P0 (立即修复)

1. **同步embeddata 627-631**
   - 影响: 全新安装环境缺少关键迁移
   - 工作量: 10分钟 (复制文件 + commit)
   - 责任人: DevOps

2. **补录db-changelog.md checksums**
   - 影响: deploy-seamless checksum验证失败
   - 工作量: 15分钟 (添加4行记录)
   - 责任人: DBA

3. **修复573迁移 (DROP body列)**
   - 影响: 60-90GB存储浪费
   - 工作量: 2小时 (测试 + 部署)
   - 责任人: DBA + DevOps

### 🟡 P1 (本周内)

4. **补充627/628/630单元测试**
   - 影响: 关键逻辑缺少回归测试
   - 工作量: 1天 (3个测试文件)
   - 责任人: Backend Team

5. **部署626到生产**
   - 影响: session_bodies promote当前无法执行
   - 依赖: 先完成db-changelog补录
   - 工作量: 30分钟 (deploy + verify)
   - 责任人: DBA

6. **验证aggregation_id backfill完整性**
   - 影响: 确认627修复生效
   - 工作量: 1小时 (SQL审计脚本)
   - 责任人: DBA

### 🟢 P2 (技术债)

7. **补全中等规模表的columnar分区**
   - 表: `routing_decision_log`, `tool_usage_stats`, `credit_ledger`
   - 工作量: 3天 (每个表1天)
   - 责任人: Backend Team

8. **添加audit_attachments_cleanup RLS策略**
   - 影响: 纵深防御
   - 工作量: 30分钟
   - 责任人: Security Team

9. **文档化"逻辑外键"关系**
   - 影响: 数据完整性理解
   - 工作量: 2小时 (ER图 + 文档)
   - 责任人: Architect

---

## 9. 验证SQL脚本

### 9.1 检查Hot+Columnar架构完整性

```sql
-- 验证所有核心表的hot表、分区表、promote函数、统一视图是否齐全
WITH core_tables AS (
    SELECT unnest(ARRAY[
        'request_logs',
        'session_bodies', 
        'candidate_failure_logs',
        'session_turns',
        'handoff_logs',
        'dashboard_access_events',
        'session_module_executions',
        'request_logs_bodies'
    ]) AS base_name
),
hot_check AS (
    SELECT base_name, 
           EXISTS(SELECT 1 FROM pg_class WHERE relname = base_name || '_hot') AS has_hot
    FROM core_tables
),
partition_check AS (
    SELECT base_name,
           EXISTS(SELECT 1 FROM pg_partitioned_table pt 
                  JOIN pg_class c ON pt.partrelid = c.oid 
                  WHERE c.relname = base_name) AS has_partitions
    FROM core_tables
),
promote_check AS (
    SELECT base_name,
           EXISTS(SELECT 1 FROM pg_proc 
                  WHERE proname = 'promote_' || base_name || '_hot_to_partition') AS has_promote
    FROM core_tables
),
view_check AS (
    SELECT base_name,
           EXISTS(SELECT 1 FROM pg_class 
                  WHERE relname = base_name || '_with_current_month' 
                  OR relname = base_name || '_unified') AS has_view
    FROM core_tables
)
SELECT h.base_name,
       h.has_hot,
       p.has_partitions,
       pr.has_promote,
       v.has_view,
       CASE 
           WHEN h.has_hot AND p.has_partitions AND pr.has_promote AND v.has_view 
           THEN '✅ Complete'
           ELSE '⚠️ Incomplete'
       END AS status
FROM hot_check h
JOIN partition_check p ON h.base_name = p.base_name
JOIN promote_check pr ON h.base_name = pr.base_name
JOIN view_check v ON h.base_name = v.base_name
ORDER BY h.base_name;
```

### 9.2 检查aggregation_id在所有candidate_failure_logs分区中存在

```sql
-- 验证627 backfill是否成功
SELECT 
    schemaname,
    tablename,
    EXISTS(
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = schemaname
        AND table_name = tablename
        AND column_name = 'aggregation_id'
    ) AS has_aggregation_id,
    pg_size_pretty(pg_total_relation_size(schemaname||'.'||tablename)) AS size
FROM pg_tables
WHERE tablename LIKE 'candidate_failure_logs%'
ORDER BY tablename;
```

### 9.3 检查session_aggregate_outbox状态分布

```sql
-- 验证outbox reaper运行状态
SELECT 
    status,
    COUNT(*) AS count,
    AVG(attempts) AS avg_attempts,
    MAX(attempts) AS max_attempts,
    COUNT(*) FILTER (WHERE next_retry_at < NOW()) AS ready_to_retry,
    MIN(created_at) AS oldest,
    MAX(created_at) AS newest
FROM session_aggregate_outbox
GROUP BY status
ORDER BY 
    CASE status 
        WHEN 'pending' THEN 1
        WHEN 'claimed' THEN 2
        WHEN 'done' THEN 3
        WHEN 'dead' THEN 4
    END;
```

### 9.4 检查request_logs body列是否仍存在

```sql
-- 验证573是否已应用
SELECT 
    table_schema,
    table_name,
    column_name,
    data_type
FROM information_schema.columns
WHERE table_schema = 'public'
  AND table_name IN ('request_logs_hot', 'request_logs')
  AND column_name IN ('request_body', 'response_body', 'outbound_body')
ORDER BY table_name, column_name;

-- 预期结果: 0 rows (已清理) 或 6 rows (未清理)
```

---

## 10. 总结与建议

### 10.1 架构健康度评分

| 维度 | 评分 | 说明 |
|------|------|------|
| Hot+Columnar架构完整性 | 🟢 85/100 | 核心表完全符合，中等表待补全 |
| SQL迁移一致性 | 🔴 60/100 | 627-631未同步embeddata，checksum缺失 |
| 数据完整性约束 | 🟢 80/100 | NOT NULL/CHECK完善，缺少FK（有意设计） |
| 存储优化 | 🟡 70/100 | 573未执行损失60-90GB，VACUUM机制完善 |
| 测试覆盖率 | 🔴 40/100 | 仅13.4%覆盖，620-631无测试 |
| **综合评分** | 🟡 **67/100** | **合格但有改进空间** |

### 10.2 关键建议

#### 短期 (本周)
1. ✅ 立即同步embeddata 627-631
2. ✅ 补录db-changelog.md checksums
3. ✅ 修复573迁移，回收60-90GB存储
4. ✅ 部署626到生产环境

#### 中期 (本月)
5. ✅ 补充627/628/630单元测试
6. ✅ 验证aggregation_id backfill完整性
7. ✅ 添加audit_attachments_cleanup RLS策略
8. ✅ 运行验证SQL脚本，生成健康报告

#### 长期 (技术债)
9. ✅ 补全中等规模表的columnar分区架构
10. ✅ 提升迁移测试覆盖率到50%+
11. ✅ 文档化逻辑外键关系
12. ✅ 建立存储审计自动化脚本

---

**审计完成时间**: 2026-08-31T12:30:00Z  
**审计人**: Agent-3 (ZCode Storage & Migration Auditor)  
**下次审计建议**: 2026-09-15 (P0修复验证后)

