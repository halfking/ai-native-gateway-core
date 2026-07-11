# Migration 382/383/384 — Session Module Executions, Dashboard Events, Hot Table Fix

**Date**: 2026-07-11  
**Commit**: b78425992  
**Type**: Schema addition + idempotent fix

## Overview

Three migrations renumbered from conflicting 341/360/361:

- **382_session_module_executions.sql**: Session module execution records (hot + monthly partitions)
- **383_dashboard_access_events.sql**: Dashboard API埋点 (hot + monthly partitions)  
- **384_hot_table_independence_fix.sql**: Backfill missing columns in `request_logs_hot`

## Production Safety

### Idempotence

All three migrations use `IF NOT EXISTS` / `IF EXISTS` guards:
- Tables: `CREATE TABLE IF NOT EXISTS`
- Indexes: `CREATE INDEX IF NOT EXISTS`
- Functions: `CREATE OR REPLACE FUNCTION`
- Columns (384): `ADD COLUMN IF NOT EXISTS`

**Safe to re-run** on existing schemas.

### Deployment Order

1. Apply migrations in numerical order: 382 → 383 → 384
2. Both 382 and 383 create partitioned tables with automatic partition creation functions
3. Migration 384 is a repair migration; can be skipped if `request_logs_hot` already has all 7 columns

### Rollback

Each migration includes a `.down.sql` file:
- **382_session_module_executions.down.sql**: Drops views, functions, and tables
- **383_dashboard_access_events.down.sql**: Drops views, functions, and tables  
- **384_hot_table_independence_fix.down.sql**: Drops added columns and indexes

**Warning**: Rolling back 382/383 will **permanently delete** all session module execution and dashboard access event data.

## Archive Strategy

### Difference from Standard Hot Tables

Migrations 382 and 383 use `archive_*` functions instead of `promote_*_hot_to_partition`:

| Migration | Function | Invocation |
|-----------|----------|------------|
| 382 | `archive_session_module_executions(retention_days INT)` | Manual or cron |
| 383 | `archive_dashboard_events(retention_days INT)` | Manual or cron |

These functions are **NOT** called by `bg/partition_manager.go` background loop.

### Manual Archival

To archive old records:

```sql
-- Archive session module executions older than 7 days
SELECT * FROM archive_session_module_executions(7);

-- Archive dashboard events older than 30 days
SELECT * FROM archive_dashboard_events(30);
```

### Automatic Archival (Optional)

Add to cron or systemd timer:

```sql
-- Run daily at 02:00
SELECT cron.schedule('session-module-archive', '0 2 * * *', 
  'SELECT * FROM archive_session_module_executions(7)');

SELECT cron.schedule('dashboard-events-archive', '0 2 * * *', 
  'SELECT * FROM archive_dashboard_events(30)');
```

## Application Integration

### Session Module Executions

**Go package**: `domains/moduleexec`

**Admin API**:
- `GET /api/admin/module-executions/stats` — execution statistics
- `POST /api/admin/module-executions/archive` — trigger manual archival

**Code reference**:
```go
// domains/moduleexec/admin.go:227
func (s *AdminService) ArchiveOldRecords(ctx context.Context, retentionDays int) (int64, error) {
    err := s.db.QueryRow(ctx, "SELECT * FROM archive_session_module_executions($1)", retentionDays).Scan(&archived)
    // ...
}
```

### Dashboard Access Events

**No Go package yet** —埋点表供 BI/审计查询，暂无应用层 API。

## Verification

Run after deployment:

```bash
cd /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go
bash scripts/verify-migrations.sh
```

Expected output:
```
✅ session_module_executions_hot exists
✅ session_module_executions partition table exists
✅ dashboard_access_events_hot exists
✅ dashboard_access_events partition table exists
✅ archive_session_module_executions function exists
✅ archive_dashboard_events function exists
✅ request_logs_hot has 7 required columns
```

## References

- [382 SQL](../../sql/migrations/startup/382_session_module_executions.sql)
- [383 SQL](../../sql/migrations/startup/383_dashboard_access_events.sql)
- [384 SQL](../../sql/migrations/startup/384_hot_table_independence_fix.sql)
- [Module Exec Design](../../domains/moduleexec/DESIGN.md)
- [Dashboard API Spec](../DASHBOARD_API.md)
