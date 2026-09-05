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
      "dimension_key": "tenant:tenant1:day:2026-08-18:provider:1:cred:5:model:10:gpt-4",
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
- `open`: Requires manual review (a source-backed mismatch exceeds the auto-repair threshold)
- `auto_repair_pending`: A source-backed missing projection or small mismatch waiting for rebuild
- `phantom_open`: A projection row has no matching source fact; investigate out of band and do not approve
- `auto_repaired`: Automatically fixed by rebuild
- `approved`: Manually approved, adjustment created
- `rejected`: Manually rejected, no action taken
- `adjusted`: Legacy terminal value retained for compatibility

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
   - `adjustment_id`, `tenant_id`, `month_start`, `dimension_type`, `dimension_key`, `metric`
   - `delta` = difference value
   - `reason` = provided reason prefixed with `[reconciliation]`
   - `source_event_id` = reconciliation `run_id`
   - `approved_by` / `created_by` = operator; `approved_at` / `created_at` = current timestamp
   - migration 544 also supplies `adjustment_type='reconciliation'` and `metric_name` for compatibility

## Workflow

### Automatic Reconciliation

1. Worker runs every 6 hours
2. Compares `usage_facts` aggregation vs `stats_usage_daily` for last 7 days
3. Small diffs (≤2% or <1000 absolute) → `auto_repaired`, trigger rebuild
	4. Large source-backed diffs → `open`, requires manual review; projection-only rows → `phantom_open` and are never approved

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
    adjustment_id   text NOT NULL UNIQUE,
    tenant_id       text NOT NULL,
    month_start     date NOT NULL,
    dimension_type  text NOT NULL,
    dimension_key   text NOT NULL,
    metric          text NOT NULL,
    delta           numeric(30,8) NOT NULL,
    currency        text,
    reason          text NOT NULL,
    source_event_id text,
    approved_by     text,
    approved_at     timestamptz,
    created_by      text NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    -- Added by migration 544, preserving baseline compatibility:
    adjustment_type text NOT NULL DEFAULT 'reconciliation',
    metric_name     text
);
```

## Tenant Isolation

- Tenant admins can only see diffs for their `tenant_id`
- `super_admin` and `admin_key` can see/approve all diffs
- Approval endpoint enforces tenant access control

## Migration Dependencies

Apply forward migrations in numeric order before enabling the matching gateway code:

- **536**: `stats_reconciliation_runs`, `stats_reconciliation_diffs`, `stats_adjustments` tables
- **537**: `usage_facts` table (source of truth)
- **539**: `tenant_id` column and index on `stats_reconciliation_diffs`
- **544**: additive `stats_adjustments.adjustment_type` and `metric_name`
- **545**: `phantom_open` / `adjusted` resolution constraint
- **546**: initial reconciliation diff uniqueness protection
- **547**: session project attribution tables and indexes
- **548**: tenant-aware reconciliation diff uniqueness; required by the current five-column `ON CONFLICT` writer

The reconciliation worker currently compares only `stats_usage_daily.dimension_type='provider_model'`, because its source aggregation is provider-model granularity. Other dimensions remain available for reporting but are not independently reconciled by this worker.

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

## Shadow Read Cutover

The first migration stage keeps legacy responses authoritative and asynchronously compares their summary values with `stats_usage_daily`. It covers only `GET /api/usage/summary` and `GET /api/admin/dashboard/board` summary payloads; reconciliation approval, exports, session overview, and error drills are excluded.

**Platform settings** (all hot reloadable):

- `stats.shadow_read.enabled`: master switch; defaults to `false`.
- `stats.shadow_read.sample_percent`: sampled requests from `0` to `100`; defaults to `0`.
- `stats.shadow_read.timeout_ms`: background query timeout; defaults to `750`, bounded to `100`–`5000` ms.

The shadow queue is bounded (32 tasks, 2 workers). A full queue, a timeout, a missing projection, or a comparison failure never changes or delays the primary response. Tenant scope is derived from authenticated context; only `super_admin` and `admin_key` may select another tenant through `tenant_id`.

**Prometheus metrics**:

- `llm_gateway_stats_shadow_comparisons_total{endpoint,result}` where `result` is `exact`, `tolerated_drift`, `material_drift`, `degraded`, `timeout`, or `query_error`.
- `llm_gateway_stats_shadow_duration_seconds{endpoint}`.
- `llm_gateway_stats_shadow_dropped_total{endpoint}`.

Do not use the current UTC day alone as a primary cutover gate: legacy usage uses a rolling window while canonical stats are daily projections and can lag the live source. Start at a low sample rate, investigate `material_drift` logs (which contain tenant scope and field names), and only expand endpoint coverage after sustained low drift and zero sustained queue drops/timeouts.

## Operational rollout

For production enablement, drain/rollback, alert thresholds and the
shadow-sampling ladder see
[Stats Reconciliation Production Rollout Runbook](runbooks/stats-reconciliation-rollout.md).

## Prometheus metrics

Reconciliation emits three low-cardinality counter families. Labels are
restricted to fixed enums; never add `run_id`, `diff_id`, `tenant_id`,
`provider_id`, `credential_id`, `model`, `dimension_key`, `operator`,
or raw error text.

- `llm_gateway_stats_reconciliation_runs_total{status}` —
  `completed` and `failed` describe `ReconcilePeriod` logical outcomes.
  `panicked` records a worker tick recovered by `runTick`; it does not
  necessarily map to a persisted run row. Pair all labels with
  `stats_reconciliation_runs.status` when investigating drift because
  `finishRun` intentionally swallows UPDATE errors.
- `llm_gateway_stats_reconciliation_diffs_total{resolution}` —
  `auto_repaired` is the persisted `RowsAffected()` after
  `DailyMonthlyRollup.Refresh` plus the resolution UPDATE succeeds;
  `open` is an unresolved source-backed mismatch; `phantom_open` is a
  projection-only row that requires investigation and is never approved.
- `llm_gateway_stats_adjustments_total{action,result}` —
  `action ∈ {approve, reject}`, `result ∈ {committed, failed}`.
  `committed` is incremented only after `tx.Commit` succeeds; any
  pre-commit failure (loop error, commit error, schema mismatch) emits
  `failed`. The handler uses the schema-aligned baseline columns
  `adjustment_id` and `metric`; migration 544's additive columns remain
  compatible with the same INSERT path.

## Future work

1. Webhook notifications: Alert on large unresolved diffs
2. Monthly reconciliation: Compare with `stats_usage_monthly` for closed periods
3. Auto-trigger rebuild: Already integrated with `DailyMonthlyRollup.Refresh()` after auto-repair

## See Also

- [Stats Reconciliation Production Rollout Runbook](runbooks/stats-reconciliation-rollout.md)
- [Statistics Architecture](stats-architecture.md)
- [Usage Facts Design](usage-facts.md)
- [Monthly Close Process](monthly-close.md)
