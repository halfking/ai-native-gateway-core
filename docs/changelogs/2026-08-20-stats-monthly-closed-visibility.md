# stats: surface closed-month silent drop in monthly rollup (P2-3)

## Background
`monthlyInsertSQL` uses `ON CONFLICT (...) DO UPDATE ... WHERE
stats_usage_monthly.status <> 'closed'`. PostgreSQL semantics: when the
WHERE clause is false on a conflicting row, the upsert silently degrades
to `DO NOTHING` — no error, no log, no metric. For a row already at
`status='closed'`, late-arriving facts (operator corrections, replayed
events) into that month are silently lost. The intent ("closed months
are immutable") is correct, but the failure mode was invisible.

## Changes
- `domains/stats/daily_monthly_rollup.go`: `monthlyInsertSQL` is now a
  CTE that also counts the number of pre-existing `status='closed'`
  rows in the target month range. The result exposes
  `skipped_closed_count`.
- `domains/stats/daily_monthly_rollup.go`: the single call site in
  `Refresh` reads the CTE result via `QueryRow` and, when
  `skipped_closed_count > 0`, logs a `slog.Warn` and increments
  `metrics.RecordStatsMonthlyClosedSkipped(n)`.
- `metrics/stats_reconciliation_metrics.go`: new counter
  `llm_gateway_stats_monthly_closed_skipped_total`.

## Operational impact
- Operators can now alert on
  `llm_gateway_stats_monthly_closed_skipped_total > 0` to detect
  late facts that hit immutable months. The intended escape hatch is
  to record those as `stats_adjustments` rows instead of via rollup.
- No schema change, no migration.
- Late corrections still do not modify closed monthly rows — only their
  suppression is now visible.

## Migration
None.