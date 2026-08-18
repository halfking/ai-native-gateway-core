# Statistics Reconciliation Workflow

## Overview

The reconciliation worker periodically compares `usage_facts` (source of truth) with `stats_usage_daily`/`stats_usage_monthly` projections to detect and repair data discrepancies.

## Architecture

- **ReconciliationWorker**: Runs every 6 hours, compares last 7 days of data
- **Auto-repair threshold**: 2% relative difference OR <1000 absolute value
- **Tenant isolation**: All diffs include `tenant_id` for tenant-admin scoped access
- **Audit trail**: Approved diffs create `stats_adjustments` records

## API Endpoints

### GET /api/admin/stats/reconciliation

Query reconciliation runs and diffs.

**Query parameters:**
- `run_id` (optional): Filter by specific run
- `limit` (optional): Max results (default 50, max 200)

**Response:**
```json
{
  "runs": [
    {
      "run_id": "recon_20260818_abc123",
      "period_start": "2026-08-11T00:00:00Z",
      "period_end": "2026-08-18T00:00:00Z",
      "scope": "auto",
      "status": "completed",
      "events_seen": 15234,
      "rows_compared": 487,
      "rows_repaired": 12,
      "diff_count": 3,
      "started_at": "2026-08-18T03:00:00Z",
      "finished_at": "2026-08-18T03:02:15Z"
    }
  ],
  "diffs": [
    {
      "run_id": "recon_20260818_abc123",
      "tenant_id": "tenant1",
      "dimension_type": "daily_rollup",
      "dimension_key": "provider:1:cred:5:model:10:gpt-4",
      "metric": "total_tokens",
      "source_value": 150500,
      "projected_value": 148200,
      "difference": 2300,
      "resolution": "open",
      "created_at": "2026-08-18T03:01:32Z"
    }
  ]
}
```

**Resolution values:**
- `open`: Requires manual review (exceeds auto-repair threshold)
- `auto_repaired`: Automatically fixed by rebuild
- `approved`: Manually approved, adjustment created
- `rejected`: Manually rejected, no action taken

### POST /api/admin/stats/reconciliation/approve

Approve or reject reconciliation diffs. Only `super_admin` or `admin_key` can approve.

**Request body:**
```json
{
  "diff_ids": [123, 124, 125],
  "action": "approve",
  "reason": "Late provider invoice correction for 2026-08",
  "operator": "admin@example.com"
}
```

**Fields:**
- `diff_ids`: Array of diff IDs to process
- `action`: `"approve"` or `"reject"`
- `reason`: Human-readable explanation (required for approve)
- `operator` (optional): Defaults to authenticated user

**Response:**
```json
{
  "approved": 3,
  "rejected": 0,
  "operator": "admin@example.com"
}
```

**Effects of approval:**
1. Diff `resolution` updated to `"approved"`
2. `stats_adjustments` record created with:
   - `tenant_id`, `dimension_type`, `dimension_key`, `metric_name`
   - `delta` = difference value
   - `reason` = provided reason
   - `source_event_id` = reconciliation `run_id`
   - `approved_by` = operator
   - `approved_at` = current timestamp

## Workflow

### Automatic Reconciliation

1. Worker runs every 6 hours
2. Compares `usage_facts` aggregation vs `stats_usage_daily` for last 7 days
3. Small diffs (≤2% or <1000 absolute) → `auto_repaired`, trigger rebuild
4. Large diffs → `open`, requires manual approval

### Manual Approval

1. Query `/api/admin/stats/reconciliation` to find `open` diffs
2. Review `source_value` vs `projected_value` and `difference`
3. POST to `/api/admin/stats/reconciliation/approve` with:
   - `action: "approve"` to create adjustment
   - `action: "reject"` to dismiss
4. Approved diffs create audit trail in `stats_adjustments`

## On-Demand Reconciliation

For immediate reconciliation (e.g., after historical backfill):

```go
import "github.com/kaixuan/llm-gateway-go/domains/stats"

worker := stats.NewReconciliationWorker(dbPool, 6*time.Hour)
err := worker.ReconcilePeriod(ctx, startTime, endTime, "manual_backfill")
```

## Database Schema

### stats_reconciliation_runs

Tracks each reconciliation execution.

```sql
CREATE TABLE stats_reconciliation_runs (
    id               bigserial PRIMARY KEY,
    run_id           text NOT NULL UNIQUE,
    period_start     timestamptz NOT NULL,
    period_end       timestamptz NOT NULL,
    scope            text NOT NULL DEFAULT 'all',
    status           text NOT NULL DEFAULT 'running',
    source_watermark timestamptz,
    events_seen      bigint NOT NULL DEFAULT 0,
    rows_compared    bigint NOT NULL DEFAULT 0,
    rows_repaired    bigint NOT NULL DEFAULT 0,
    diff_count       bigint NOT NULL DEFAULT 0,
    error            text,
    started_at       timestamptz NOT NULL DEFAULT now(),
    finished_at      timestamptz
);
```

### stats_reconciliation_diffs

Records individual discrepancies.

```sql
CREATE TABLE stats_reconciliation_diffs (
    id              bigserial PRIMARY KEY,
    run_id          text NOT NULL REFERENCES stats_reconciliation_runs(run_id) ON DELETE CASCADE,
    tenant_id       text NOT NULL DEFAULT 'default',
    dimension_type  text NOT NULL,
    dimension_key   text NOT NULL,
    metric          text NOT NULL,
    source_value    numeric(30,8) NOT NULL DEFAULT 0,
    projected_value numeric(30,8) NOT NULL DEFAULT 0,
    difference      numeric(30,8) NOT NULL DEFAULT 0,
    resolution      text NOT NULL DEFAULT 'open',
    created_at      timestamptz NOT NULL DEFAULT now()
);
```

### stats_adjustments

Approved adjustments for closed periods (created by migration 536).

```sql
CREATE TABLE stats_adjustments (
    id              bigserial PRIMARY KEY,
    tenant_id       text NOT NULL DEFAULT 'default',
    month_start     date NOT NULL,
    adjustment_type text NOT NULL,
    dimension_type  text NOT NULL DEFAULT 'provider_model',
    dimension_key   text NOT NULL,
    metric_name     text NOT NULL,
    delta           numeric(30,8) NOT NULL,
    currency        text,
    reason          text NOT NULL,
    source_event_id text,
    approved_by     text,
    approved_at     timestamptz,
    created_by      text NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now()
);
```

## Tenant Isolation

- Tenant admins can only see diffs for their `tenant_id`
- `super_admin` and `admin_key` can see/approve all diffs
- Approval endpoint enforces tenant access control

## Migration Dependencies

- **536**: `stats_reconciliation_runs`, `stats_reconciliation_diffs`, `stats_adjustments` tables
- **537**: `usage_facts` table (source of truth)
- **539**: `tenant_id` column and index on `stats_reconciliation_diffs`

## Testing

Unit tests:
```bash
go test ./domains/stats -v -run TestReconciliationWorker
```

Integration tests (requires Docker):
```bash
go test -mod=mod -tags=integration ./domains/stats -run TestReconciliation
```

## Configuration

**Environment variables:**
- None required (worker starts automatically if DB connected)

**Code configuration:**
```go
// In cmd/gateway/main.go
statsReconciliationWorker = stats.NewReconciliationWorker(
    dbConn.Pool(), 
    6*time.Hour, // reconciliation interval
)
```

**Thresholds** (in `domains/stats/reconciliation.go`):
```go
const (
    autoRepairThreshold = 0.02  // 2% relative difference
    autoRepairMaxValue  = 1000  // absolute value threshold
)
```

## Future Enhancements

1. **Prometheus metrics**: `stats_reconciliation_diffs_total{resolution}`, `stats_adjustments_total`
2. **Webhook notifications**: Alert on large unresolved diffs
3. **Monthly reconciliation**: Compare with `stats_usage_monthly` for closed periods
4. **Auto-trigger rebuild**: Integrate with `DailyMonthlyRollup.Refresh()` after auto-repair

## See Also

- [Statistics Architecture](stats-architecture.md)
- [Usage Facts Design](usage-facts.md)
- [Monthly Close Process](monthly-close.md)
