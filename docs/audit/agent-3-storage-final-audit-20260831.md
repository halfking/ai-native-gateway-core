# Agent 3: 存储层/迁移审计报告

**审计时间**: 2026-08-31  
**审计范围**: SQL迁移627-631、Hot+Columnar架构、数据完整性约束  
**审计方法**: 文件完整性检查、结构对比、迁移幂等性验证

---

## 审计范围

### 审计文件清单
- **迁移文件**: `sql/migrations/startup/627-631` (5个up + 5个down)
- **embeddata同步**: `installer/cmd/llm-gw-installer/embeddata/startup/`
- **promote函数**: `626_session_bodies_hot_promote_reconcile.sql`, `628_candidate_failure_logs_promote_atomic_v3.sql`
- **background worker**: `bg/vacuum_worker.go`
- **变更日志**: `docs/db-changelog.md`
- **一致性验证**: `scripts/local-dev/verify-db-consistency.sh`
- **252同步脚本**: `scripts/pg-table-copy.sh`

### 审计维度
1. 迁移文件完整性与幂等性
2. embeddata同步状态
3. Hot+Columnar架构一致性
4. promote函数原子性与保留策略
5. 数据完整性约束
6. 存储优化验证

---

## 审计发现

### P0级问题（立即修复）

**无P0级问题**

所有627-631迁移文件已通过幂等性、原子性、回滚安全性验证。

---

### P1级问题（下一迭代）

#### P1-1: embeddata未同步627-631迁移文件

**位置**: `installer/cmd/llm-gw-installer/embeddata/startup/`

**现状**:
- embeddata最新文件止于626 (2026-08-31 02:31)
- 627-631迁移文件(2026-08-31创建)未同步到embeddata
- db-changelog.md中仅记录631，缺少627-630条目

**影响**:
- installer二进制无法自动应用627-630迁移
- 新环境部署时需手动执行SQL文件
- 破坏了"installer自包含"的设计契约

**建议**:
```bash
# 同步627-631到embeddata
cp sql/migrations/startup/627_*.sql installer/cmd/llm-gw-installer/embeddata/startup/
cp sql/migrations/startup/628_*.sql installer/cmd/llm-gw-installer/embeddata/startup/
cp sql/migrations/startup/629_*.sql installer/cmd/llm-gw-installer/embeddata/startup/
cp sql/migrations/startup/630_*.sql installer/cmd/llm-gw-installer/embeddata/startup/
cp sql/migrations/startup/631_*.sql installer/cmd/llm-gw-installer/embeddata/startup/

# 重新构建installer
cd installer && go build -o llm-gw-installer ./cmd/llm-gw-installer
```

---

#### P1-2: db-changelog.md缺少627-630迁移记录

**位置**: `docs/db-changelog.md:160-169`

**现状**:
- 仅记录631迁移(pending deploy)
- 627-630无checksum记录
- 缺少部署状态跟踪

**计算checksum**:
```
627: 84c0ea7130e2ac19d67416162c8d92c8c2c08e951364a9ace161f58a8f02fc92
628: d9a30a29f0ac991e8e0d9b73e3a9b1943eb57e666cd604b24f3e1ba801f3a41c
629: ae462b3d4ef16d27d5c04f8799a7f15c93f140bbd575fa8769252a2acd442701
630: da5c3cce36477be1a03dc64976e9384f37232fa83a48f34e577c7dcbb91da7f6
631: 0fd2120475a78486e40d3fa2d082eade345aef74a952ccc030b95643db252804 (已记录)
```

**建议**: 在db-changelog.md追加：
```markdown
## 2026-08-31 — audit-data-closure hotfixes (pending deploy)

| Migration | File | SHA-256 | Status |
|-----------|------|---------|--------|
| 627 | `627_candidate_failure_logs_aggregation_id_unified.sql` | `84c0ea7130e2ac19d67416162c8d92c8c2c08e951364a9ace161f58a8f02fc92` | pending deploy |
| 628 | `628_candidate_failure_logs_promote_atomic_v3.sql` | `d9a30a29f0ac991e8e0d9b73e3a9b1943eb57e666cd604b24f3e1ba801f3a41c` | pending deploy |
| 629 | `629_audit_attachments_cleanup.sql` | `ae462b3d4ef16d27d5c04f8799a7f15c93f140bbd575fa8769252a2acd442701` | pending deploy |
| 630 | `630_session_aggregate_outbox.sql` | `da5c3cce36477be1a03dc64976e9384f37232fa83a48f34e577c7dcbb91da7f6` | pending deploy |

> 627-628: 闭合ProviderErrorAggregator的watermark可见性缺口(audit-data-closure-A)
> 629: attachment清理操作的审计tombstone表(audit-data-closure-B)
> 630: Session V2聚合更新的持久化重试队列(audit-data-closure-C)
```

---

#### P1-3: 缺少627-630的测试覆盖

**位置**: `sql/migrations/startup/migration_*_test.go`

**现状**:
- 仅631有测试文件(`migration_631_test.go`)
- 627-630无contract test验证

**影响**:
- 无法通过CI验证迁移SQL的正确性
- 回归风险：未来修改可能破坏迁移契约

**建议**: 为627-630补充测试文件，参考631的测试模式：
```go
// migration_627_test.go
func TestMigration627AddsAggregationIdColumn(t *testing.T) {
    // 验证: aggregation_id列添加、backfill、unified view创建
}

// migration_628_test.go
func TestMigration628UpdatesPromoteFunction(t *testing.T) {
    // 验证: promote函数包含aggregation_id列
}

// migration_629_test.go
func TestMigration629CreatesAuditAttachmentsCleanup(t *testing.T) {
    // 验证: audit_attachments_cleanup表结构、索引
}

// migration_630_test.go
func TestMigration630CreatesSessionAggregateOutbox(t *testing.T) {
    // 验证: outbox表结构、状态CHECK约束、RLS策略
}
```

---

### P2级问题（技术债）

#### P2-1: session_bodies父表未声明columnar

**位置**: `sql/migrations/startup/430_sessions_v2_schema.sql`

**现状**:
- session_bodies父表是PARTITION BY RANGE (partition_date)
- 子分区通过ensure函数动态创建为USING columnar
- 父表本身未显式声明存储引擎

**技术背景**:
- PostgreSQL分区表的父表是虚表(meta-table)，不存储数据
- 实际数据存储在子分区中
- 父表的USING子句不会被继承到子分区

**当前行为**:
- 子分区正确创建为columnar (通过ensure函数)
- 数据写入正确路由到columnar子分区
- 父表的heap/columnar属性不影响数据存储

**建议**:
- 当前设计正确，无需修改
- 文档化说明：父表是虚表，子分区才是实际存储层

---

#### P2-2: vacuum_worker.go仅覆盖request_logs_bodies

**位置**: `bg/vacuum_worker.go:178-203`

**现状**:
- VacuumWorker仅对request_logs_bodies执行VACUUM FULL
- 其他大数据表(candidate_failure_logs_hot, session_bodies_hot)未覆盖

**建议**:
- Hot表设计为8小时TTL，数据量有界，普通VACUUM即可
- 可在下一迭代将vacuum_worker泛化为多表配置：
```go
type VacuumWorker struct {
    targets []VacuumTarget // {table, mode: FULL|STANDARD, interval}
}
```

---

#### P2-3: promote函数未记录执行统计

**位置**: `626_session_bodies_hot_promote_reconcile.sql:60`, `628_candidate_failure_logs_promote_atomic_v3.sql:90`

**现状**:
- promote函数返回moved_count
- 无持久化日志(仅pg_stat_user_functions可查)
- 无法回溯历史promote执行记录

**建议**:
- 当前设计足够(bg worker的slog已记录moved_count)
- 如需审计，可引入promote_execution_log表：
```sql
CREATE TABLE promote_execution_log (
    id bigserial PRIMARY KEY,
    table_name text NOT NULL,
    moved_count bigint NOT NULL,
    cutoff_ts timestamptz NOT NULL,
    executed_at timestamptz DEFAULT NOW()
);
```

---

## 闭环验证

### 数据闭环: **通过**

✓ 627迁移backfill逻辑：
- 使用CTE + ORDER BY (ts, ctid)保证deterministic assignment
- LOCK TABLE防止并发aggregator tick
- 幂等性：WHERE aggregation_id IS NULL

✓ 628 promote函数原子性：
- 使用WITH CTE: moved_rows → inserted_rows → count
- DELETE FROM hot USING inserted确保仅删除成功插入的行
- ON CONFLICT DO NOTHING未覆盖(candidate_failure_logs无UNIQUE约束，合理)

✓ 626 promote函数冲突处理：
- ON CONFLICT (id, partition_date) DO NOTHING
- 仅删除成功inserted的行(USING joined)
- advisory lock防止并发promote

✓ 630 outbox唯一约束：
- UNIQUE (tenant_id, session_id, partition_date, request_id)
- 防止重复入队

✓ 629 audit tombstone唯一约束：
- UNIQUE (request_id, attachment_hash, cleanup_run_id)
- 幂等cleanup记录

---

### 流程闭环: **通过**

✓ 迁移up/down配对：
- 所有627-631均有对应.down.sql
- down脚本包含rollback警告(数据丢失风险)
- 627/628 down明确标注"re-introduces missing-bucket bug"

✓ embeddata同步流程：
- 已建立verify-db-consistency.sh作为结构一致性gate
- pg-table-copy.sh支持原子per-table导入
- 缺陷：627-631未执行同步(P1-1)

✓ 变更日志流程：
- db-changelog.md记录checksum + deploy状态
- 缺陷：627-630未记录(P1-2)

---

### 反馈闭环: **部分通过**

✓ 627-631均有详细注释说明audit背景(audit-data-closure-A/B/C/D)

✓ 628标注为"v3"版本，与624(v2)形成演进链

✗ 缺少自动化验证：
- 627的backfill正确性无SQL测试
- 628的promote列对齐无contract test
- 依赖人工code review

**建议**: 补充P1-3测试覆盖

---

## Hot+Columnar架构一致性验证

### 分区表清单

| 父表 | Hot表 | 分区模式 | Columnar | 保留策略 | Promote函数 |
|------|-------|----------|----------|----------|-------------|
| session_bodies | session_bodies_hot | RANGE(partition_date) | ✓ | 8h | promote_session_bodies_hot_to_partition |
| candidate_failure_logs | candidate_failure_logs_hot | RANGE(ts) | ✓ | 24h | promote_candidate_failure_logs_hot_to_partition |
| request_logs_bodies | request_logs_bodies_hot | RANGE(partition_date) | heap (562修正) | 24h | ensure_request_logs_bodies_partition |

### 存储策略一致性

✓ **session_bodies**: 
- 父表分区：PARTITION BY RANGE (partition_date)
- 子分区通过ensure函数创建为columnar
- Hot表：heap (8h TTL)
- 626迁移修复promote函数的ON CONFLICT语义

✓ **candidate_failure_logs**:
- 父表分区：PARTITION BY RANGE (ts)
- 子分区通过392迁移的ensure函数创建为columnar
- Hot表：heap + aggregation_id序列 (24h TTL)
- 627-628迁移闭合aggregation_id promote gap

✓ **request_logs_bodies**:
- 562迁移修正为heap (columnar对TOAST不友好)
- 不在本次审计范围，但架构一致

---

## 数据完整性约束审计

### 主键/唯一约束

| 表 | 约束类型 | 列 | 迁移号 | 验证 |
|----|---------|----|--------|------|
| session_aggregate_outbox | PRIMARY KEY | id | 630 | ✓ |
| session_aggregate_outbox | UNIQUE | (tenant_id, session_id, partition_date, request_id) | 630 | ✓ |
| audit_attachments_cleanup | UNIQUE | (request_id, attachment_hash, cleanup_run_id) | 629 | ✓ |
| candidate_failure_logs | INDEX | aggregation_id | 627 | ✓ |

### CHECK约束

| 表 | 约束 | 迁移号 | 验证 |
|----|------|--------|------|
| session_aggregate_outbox | status IN ('pending','claimed','done','dead') | 630 | ✓ |
| audit_attachments_cleanup | older_than_days > 0 | 629 | ✓ |
| credentials | status包含'deleted' | 631 | ✓ |

### 外键约束

627-631无新增外键(符合预期：audit表和outbox表为append-only，不需要FK级联)

### RLS策略

✓ session_aggregate_outbox: tenant_isolation策略 + bypass_rls支持(630)  
✓ audit_attachments_cleanup: 无RLS(super_admin-only访问路径)

---

## 存储优化验证

### 573迁移执行状态

**查找结果**:
- 573迁移: DROP request_logs.body / response_body / outbound_body
- 603迁移修复573在252上的半成功状态(hot表成功，parent因view回滚)
- 当前状态：body列已外置到request_logs_bodies表

**验证**: ✓ 通过(603已修复573遗留问题)

---

### 大字段外置完整性

| 原表 | 外置表 | 列 | 迁移号 | 状态 |
|------|--------|----|----|------|
| request_logs | request_logs_bodies | body/response_body/outbound_body | 328a/573 | ✓ 已外置 |
| sessions | session_bodies | request_delta/response_delta/outbound_body | 430 | ✓ V2架构 |

---

### 索引合理性

**627新增索引**:
- `idx_candidate_failure_logs_aggregation_id`: 支持aggregator的watermark查询 ✓

**630新增索引**:
- `idx_session_aggregate_outbox_pending (next_retry_at) WHERE status='pending'`: partial index优化reaper查询 ✓
- `idx_session_aggregate_outbox_dead (created_at DESC) WHERE status='dead'`: 支持失败诊断 ✓
- `idx_session_aggregate_outbox_session (tenant_id, session_id, partition_date)`: 支持per-session查询 ✓

**629新增索引**:
- `idx_audit_attachments_cleanup_run (cleanup_run_id)`: 支持per-run查询 ✓
- `idx_audit_attachments_cleanup_request (request_id, cleaned_at DESC)`: 支持per-request审计 ✓
- `idx_audit_attachments_cleanup_tenant_ts (tenant_id, cleaned_at DESC)`: 支持per-tenant审计 ✓

**631新增索引**:
- `idx_providers_live (id) WHERE deleted_at IS NULL`: partial index优化live-row查询 ✓

**无冗余索引发现**

---

## 迁移幂等性验证

### 627: candidate_failure_logs_aggregation_id_unified

✓ ALTER TABLE ADD COLUMN IF NOT EXISTS  
✓ CREATE INDEX IF NOT EXISTS  
✓ CREATE OR REPLACE VIEW  
✓ Backfill使用WHERE aggregation_id IS NULL(幂等)  
✓ Post-condition检查(RAISE EXCEPTION on failure)  

### 628: candidate_failure_logs_promote_atomic_v3

✓ CREATE OR REPLACE FUNCTION(幂等)  
✓ Post-condition检查  

### 629: audit_attachments_cleanup

✓ CREATE TABLE IF NOT EXISTS  
✓ CREATE INDEX IF NOT EXISTS  
✓ UNIQUE约束防止重复插入  

### 630: session_aggregate_outbox

✓ CREATE TABLE IF NOT EXISTS  
✓ CREATE INDEX IF NOT EXISTS  
✓ RLS策略使用DROP POLICY IF EXISTS + CREATE POLICY  

### 631: provider_credential_soft_delete

✓ DROP CONSTRAINT IF EXISTS  
✓ ADD COLUMN IF NOT EXISTS  
✓ CREATE INDEX IF NOT EXISTS  
✓ Down脚本包含data guard(检查是否有deleted状态行)  

**所有迁移均通过幂等性验证**

---

## 建议的修复方案

### 立即执行(P1级)

1. **同步embeddata** (P1-1):
```bash
cd /Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go
cp sql/migrations/startup/62{7,8,9}_*.sql installer/cmd/llm-gw-installer/embeddata/startup/
cp sql/migrations/startup/63{0,1}_*.sql installer/cmd/llm-gw-installer/embeddata/startup/
git add installer/cmd/llm-gw-installer/embeddata/startup/
git commit -m "sync: embeddata 627-631 migrations"
```

2. **更新db-changelog.md** (P1-2):
- 追加627-630条目
- 记录checksum
- 标注audit-data-closure背景

3. **补充测试覆盖** (P1-3):
- 创建migration_627_test.go (验证aggregation_id列+view)
- 创建migration_628_test.go (验证promote函数签名)
- 创建migration_629_test.go (验证audit表结构)
- 创建migration_630_test.go (验证outbox表+RLS)

### 技术债跟踪(P2级)

- P2-1: 文档化父表虚表特性(无需代码修改)
- P2-2: 考虑泛化vacuum_worker(下一迭代)
- P2-3: 考虑promote_execution_log表(下一迭代)

---

## 审计结论

### 总体评价: **PASS (有条件通过)**

✓ **迁移质量**: 627-631迁移文件设计良好，幂等性、原子性、回滚安全性均符合标准  
✓ **数据闭环**: promote函数原子性、唯一约束、RLS策略均正确  
✓ **架构一致性**: Hot+Columnar分层存储策略一致，保留策略明确  
✓ **存储优化**: 573外置正确，索引合理，无冗余  

✗ **流程缺陷**: embeddata未同步(P1-1)、changelog未更新(P1-2)、测试覆盖不足(P1-3)

### 部署Gate建议

**在部署627-631到252之前**:
1. 执行P1-1: 同步embeddata
2. 执行P1-2: 更新db-changelog.md
3. 执行P1-3: 补充测试并验证通过
4. 执行verify-db-consistency.sh --verify确认本地-252一致性
5. 使用deploy-seamless.sh应用迁移(内置checksum验证)

**部署后验证**:
```sql
-- 验证627: aggregation_id列存在且有索引
SELECT count(*) FROM information_schema.columns 
WHERE table_name='candidate_failure_logs' AND column_name='aggregation_id';

-- 验证628: promote函数包含aggregation_id
SELECT prosrc FROM pg_proc WHERE proname='promote_candidate_failure_logs_hot_to_partition';

-- 验证629: audit表存在
SELECT count(*) FROM pg_tables WHERE tablename='audit_attachments_cleanup';

-- 验证630: outbox表+RLS
SELECT count(*) FROM pg_tables WHERE tablename='session_aggregate_outbox';
SELECT count(*) FROM pg_policies WHERE tablename='session_aggregate_outbox';

-- 验证631: credentials.status包含deleted
SELECT conname FROM pg_constraint WHERE conrelid='credentials'::regclass AND conname='credentials_status_check';
SELECT count(*) FROM information_schema.columns WHERE table_name='providers' AND column_name='deleted_at';
```

---

**审计人**: Agent 3 (存储层审计专员)  
**审计完成时间**: 2026-08-31  
**下一审计节点**: 252部署后一致性验证
