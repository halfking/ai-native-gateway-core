# ⚠️ 本文件已废弃 / This File Is Deprecated

> **归档日期**: 2026-07-05
> **替代文档**: [`docs/partition/IMPLEMENTATION_NOTES.md`](../../docs/partition/IMPLEMENTATION_NOTES.md) 第 §13 节
> **原因**: 内容已合并到主实施记录（与参考标准的差距分析 5 大 GAP）
> **保留原因**: 提供历史归档追溯

---

# Partition Architecture Gap Analysis Report
**Date**: 2026-07-04
**Environments**: 71 (llm.kxpms.cn) & 184 (both working)
**Reference Standard**: `/Users/xutaohuang/workspace/llm-gateway-go-2`
**Current Project**: `/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go`

---

## Executive Summary

Both 71 and 184 environments are now **WRITING SUCCESSFULLY** after migrations 336-339. The core partition architecture is **CORRECTLY IMPLEMENTED** and aligns with the reference standard documented in `lessons-learned-2026-07-03-partitioned-tables.md`.

However, there are **5 CRITICAL GAPS** compared to the reference standard that need attention for production readiness.

---

## ✅ What's Working (Matches Reference Standard)

### 1. Partition Attachment Strategy ✅
**Reference Standard**: 
- Current month (2026-07): DETACHED
- Future months (2026-08+): DETACHED
- Historical months (2026-06-): ATTACHED

**Current Implementation**: Migration 337 **CORRECTLY** implements this:
- Lines 36-104: Detaches 2026-07 through 2026-12 for all tables
- Allows `*_default` partitions to receive all new data
- Historical partitions remain attached for SELECT aggregation

**Status**: ✅ **ALIGNED**

### 2. Write Operations Target `*_default` ✅
**Reference Standard**: All INSERT/UPDATE/DELETE must go through `*_default` partitions only.

**Current Implementation**: Code review confirms:
- `domains/hooks/observability/telemetry/client.go`: 
  - Lines show `UPDATE request_logs_default`
  - Lines show `UPDATE usage_ledger_default`
- `admin/data_lifecycle_blobs.go`: `UPDATE request_logs_default`
- `admin/data_lifecycle_attachments.go`: `UPDATE request_logs_default`

**Status**: ✅ **ALIGNED**

### 3. Promote Functions Exist ✅
**Reference Standard**: Each partitioned table needs a `promote_*_default_batch()` function.

**Current Implementation**: Migration 336 creates all 8 functions:
- `promote_request_logs_default_batch`
- `promote_request_wal_default_batch`
- `promote_usage_ledger_default_batch`
- `promote_routing_decision_log_default_batch`
- `promote_credential_model_index_default_batch`
- `promote_request_logs_bodies_default_batch`
- `promote_credit_ledger_default_batch`
- `promote_tool_usage_stats_default_batch`

**Status**: ✅ **ALIGNED**

### 4. Background Partition Manager ✅
**Reference Standard**: Automated background process to move data from `*_default` to monthly partitions.

**Current Implementation**: `bg/partition_manager.go` (325 lines):
- `promoteDefaultToPartitions()` runs every 1 hour (line 16)
- 7-day retention window (line 27)
- Batch size 5000 (line 31)
- Loops until all cold rows moved (lines 294-325)

**Status**: ✅ **ALIGNED**

### 5. Monthly Partition Creation ✅
**Reference Standard**: Auto-create next month's partitions.

**Current Implementation**: 
- `ensureNextMonthPartitions()` in partition_manager.go (lines 136-157)
- Runs every 24 hours
- Creates current + next month partitions

**Status**: ✅ **ALIGNED**

---

## ❌ CRITICAL GAPS (Current vs Reference)

### GAP #1: Missing `*_with_current_month` VIEWs ❌

**Reference Standard Expects**:
According to the lessons-learned document architecture, when querying data that spans the current month AND historical months, the reference implies using VIEWs that UNION the `*_default` partition with monthly partitions.

**What Current Project Has**:
```bash
$ grep -r "CREATE VIEW" deploy/sql/01-schema.sql | grep -v "cost\|model_offers\|tenant_model"
# Returns: NO *_with_current_month VIEWs found
```

**What's Missing**:
- `request_logs_with_current_month` VIEW
- `usage_ledger_with_current_month` VIEW  
- `request_wal_with_current_month` VIEW
- `routing_decision_log_with_current_month` VIEW

**Expected VIEW Pattern**:
```sql
CREATE VIEW request_logs_with_current_month AS
SELECT * FROM request_logs_default
UNION ALL
SELECT * FROM request_logs_2026_06
UNION ALL
SELECT * FROM request_logs_2026_05
-- ... (historical months that are ATTACHED)
;
```

**Impact**: 
- ⚠️ **MEDIUM RISK**: Application code querying parent tables (e.g., `SELECT FROM request_logs`) will NOT see data in `*_default` if future month partitions are DETACHED and no matching monthly partition exists.
- Current workaround: Parent table queries still work because historical partitions are ATTACHED, but querying across current+historical requires careful handling.

**Evidence of Potential Issue**:
Found in `admin/logs.go`, `admin/data_lifecycle.go`:
```go
SELECT COUNT(*) FROM request_logs rl WHERE ...
```
These queries hit the parent table, which currently works but may miss `*_default` data in certain edge cases.

---

### GAP #2: READ Queries Still Hit Parent Tables ❌

**Reference Standard Recommendation** (from lessons-learned):
> **读操作（SELECT 聚合查询）** → 查询主表（自动包含 default + 所有分区）

The reference standard says SELECT queries SHOULD use the parent table for aggregation. However, the document also warns that UPDATE on parent tables scans all partitions including columnar.

**Current Implementation Issues**:
Found **3 locations** where SELECT queries on parent tables could be optimized:

1. **`domains/authentication/verifier.go:130`**:
```go
SELECT COALESCE(SUM(cost_usd), 0)::float8 FROM usage_ledger WHERE api_key_id = $1
```

2. **`admin/keys.go`**:
```go
SELECT COALESCE(SUM(cost_usd), 0) FROM usage_ledger WHERE api_key_id = $1
```

3. **Multiple admin handlers** (`admin/logs.go`, `admin/data_lifecycle.go`, etc.):
```go
SELECT COUNT(*) FROM request_logs rl WHERE ...
```

**Impact**:
- ⚠️ **LOW-MEDIUM RISK**: These queries work correctly (parent table aggregates all partitions), but performance could degrade as historical columnar partitions accumulate.
- For queries needing ONLY recent data (< 7 days), directly querying `*_default` would be faster.

**Recommendation**:
The reference standard's lesson is: **distinguish between**:
- Recent data queries (< 7 days) → Query `*_default` directly
- Historical queries → Query parent table
- Cross-month queries → Use `*_with_current_month` VIEW (if implemented)

---

### GAP #3: No Daily Maintenance Scripts ❌

**Reference Standard Has** (`llm-gateway-go-2/scripts/`):
- `archive-request-logs.sh` (7077 bytes)
- `delete-old-request-logs.sh` (5721 bytes)
- `backfill_request_logs_provider_model.sh` (5055 bytes)
- `analyze-request-logs-size.sh` (5284 bytes)

**What Current Project Has**:
```bash
$ ls scripts/ | grep -iE "(migrate|partition|promote|rotate)"
deploy-verify-partition-archive.sh
local-r112-migrate.sh
migrate-to-domains.sh
pg-columnar-rotate-local.sh
pg-columnar-rotate.sh  # ← Only this one is partition-related
```

**What's Missing**:
1. **Daily default → monthly migration script** (manual trigger)
   - Current: Only runs via `bg/partition_manager.go` (automatic)
   - Missing: CLI script for manual/emergency migration
   
2. **Monthly VIEW update script** (regenerate `*_with_current_month` VIEWs)
   - Completely missing (because VIEWs don't exist yet)
   
3. **Partition monitoring/diagnostic scripts**
   - No script to check `*_default` table sizes
   - No script to verify partition attachment status
   - No script to audit data distribution across partitions

**Impact**:
- ⚠️ **MEDIUM RISK**: Operators lack manual tools to:
  - Drain `*_default` tables in emergencies
  - Verify partition health
  - Debug data migration issues

**Available Script Analysis**:
- `pg-columnar-rotate.sh` (199 lines): Converts heap → columnar, runs monthly
  - This is GOOD but insufficient without daily maintenance tools

---

### GAP #4: No Partition-Specific Monitoring & Alerts ❌

**Reference Standard Expects** (from lessons-learned section "监控告警"):
- Monitor `*_default` table size (alert when > threshold)
- Monitor migration task execution status
- Monitor columnar-related errors

**Current Implementation**:
Found `observability/alerts/availability.rules.yml` (292 lines) but contains:
- ✅ Redis availability cache alerts
- ✅ Suspicious exit alerts
- ❌ **ZERO alerts for partitions/default tables**

**What's Missing**:
1. **Default Table Size Alert**:
```yaml
# Missing alert example:
- alert: LLMGatewayDefaultPartitionSizeHigh
  expr: pg_table_size{table=~".*_default"} > 10GB
  for: 1h
  annotations:
    summary: "Default partition exceeding 10GB"
```

2. **Partition Migration Lag Alert**:
```yaml
# Missing alert example:
- alert: LLMGatewayPartitionMigrationLag
  expr: |
    (now() - min(ts) FROM request_logs_default) > 7 days + 6 hours
  annotations:
    summary: "Default partition contains data older than retention window"
```

3. **Columnar Conversion Errors**:
```yaml
# Missing alert example:
- alert: LLMGatewayColumnarConversionFailed
  expr: llmgw_partition_rotate_errors_total > 0
```

**Impact**:
- ⚠️ **HIGH RISK**: No visibility into partition health
- Silent failures in background migration could accumulate
- `*_default` tables could grow unbounded without alerting

---

### GAP #5: Missing Documentation Updates ❌

**Reference Standard Has** (`llm-gateway-go-2/docs/`):
- `lessons-learned-2026-07-03-partitioned-tables.md` (373 lines)
- Clear architecture diagrams
- Operational runbooks
- Troubleshooting guides

**Current Project Docs**:
```bash
$ find docs/ -name "*partition*" -o -name "*columnar*"
# Returns: NO partition-specific documentation
```

**What's Missing**:
1. **Architecture Document**: Explaining the partition design
2. **Operational Runbook**: How to manually trigger migrations
3. **Troubleshooting Guide**: Common issues and fixes
4. **Query Patterns Guide**: When to use `*_default` vs parent table vs VIEWs

**Impact**:
- ⚠️ **MEDIUM RISK**: Knowledge is locked in code
- New team members lack context
- Emergency procedures undocumented

---

## 📊 Detailed Comparison Matrix

| Dimension | Reference Standard | Current Project | Status |
|-----------|-------------------|----------------|--------|
| **1. Partition Attachment** | DETACHED 2026-07+ | ✅ DETACHED 2026-07+ | ✅ ALIGNED |
| **2. Write Targets** | `*_default` only | ✅ `*_default` only | ✅ ALIGNED |
| **3. Query VIEWs** | `*_with_current_month` | ❌ Missing | ❌ GAP |
| **4. Promote Functions** | 8 functions | ✅ 8 functions | ✅ ALIGNED |
| **5. Background Manager** | Yes (Go service) | ✅ Yes (bg/partition_manager.go) | ✅ ALIGNED |
| **6. Monthly Rotation** | Yes (CronJob) | ✅ Yes (pg-columnar-rotate.sh) | ✅ ALIGNED |
| **7. Query Patterns** | Documented | ❌ Undocumented | ❌ GAP |
| **8. Maintenance Scripts** | 4+ scripts | ⚠️ Only 1 script | ❌ GAP |
| **9. Monitoring** | Partition-specific | ❌ None | ❌ GAP |
| **10. Documentation** | Comprehensive | ❌ Missing | ❌ GAP |

---

## 🎯 Recommendations (Priority Order)

### Priority 1: HIGH (Production Blockers)
1. **Add Partition Monitoring Alerts** (Est: 2-4 hours)
   - Default table size alert
   - Migration lag alert
   - Add metrics to partition_manager.go

2. **Create Emergency Maintenance Scripts** (Est: 4-6 hours)
   - Manual promote script: `scripts/manual-promote-default.sh`
   - Partition status checker: `scripts/check-partition-health.sh`
   - Default table size reporter: `scripts/report-default-sizes.sh`

### Priority 2: MEDIUM (Optimization)
3. **Implement `*_with_current_month` VIEWs** (Est: 2-3 hours)
   - Create VIEWs in new migration
   - Update admin queries to use VIEWs where appropriate
   - Add VIEW refresh to monthly rotation

4. **Document Partition Architecture** (Est: 3-4 hours)
   - Port lessons-learned doc to current project
   - Add operational runbook
   - Create query pattern guide

### Priority 3: LOW (Polish)
5. **Optimize Query Patterns** (Est: 4-6 hours)
   - Audit all parent table queries
   - Convert recent-data queries to `*_default`
   - Add query pattern comments

---

## 📝 Action Items

### Immediate (Next 24 Hours)
- [ ] Add `pg_table_size` monitoring for `*_default` tables
- [ ] Create `scripts/check-partition-health.sh` diagnostic script
- [ ] Set up alert for default partition size > 5GB

### Short Term (Next Week)
- [ ] Implement `*_with_current_month` VIEWs (new migration)
- [ ] Document partition architecture in `docs/partition-architecture.md`
- [ ] Create manual promotion script for emergencies
- [ ] Add partition metrics to Prometheus

### Medium Term (Next Month)
- [ ] Audit and optimize query patterns
- [ ] Add comprehensive partition monitoring dashboard
- [ ] Create operational runbook for partition maintenance

---

## ✅ Conclusion

**Current Status**: The partition architecture is **FUNDAMENTALLY SOUND** and matches the reference standard's core design. Both 71 and 184 environments are writing successfully.

**Key Strengths**:
- ✅ Correct partition attachment strategy
- ✅ All writes target `*_default` partitions
- ✅ Automated background migration working
- ✅ Monthly columnar rotation script exists

**Critical Gaps**:
- ❌ No partition-specific monitoring/alerts
- ❌ Missing maintenance/diagnostic scripts
- ❌ Missing `*_with_current_month` VIEWs
- ❌ Undocumented architecture and operations

**Risk Assessment**:
- **Current Risk**: LOW (system is working)
- **Production Risk**: MEDIUM (lack of observability could hide issues)
- **Operational Risk**: MEDIUM (no emergency procedures documented)

**Recommendation**: Address Priority 1 items (monitoring + emergency scripts) before declaring "production ready". Priority 2 and 3 items can be handled iteratively.

---

**Report Generated**: 2026-07-04
**Analyst**: ZCode AI
**Review Status**: Ready for Team Review
