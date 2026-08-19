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
  CTE that materialises the daily `GROUP BY` into a `daily_groups`
  CTE, then runs the upsert from that CTE, and finally counts how
  many target PK tuples (from `daily_groups`) map to a pre-existing
  `status='closed'` row in `stats_usage_monthly`. The result exposes
  `skipped_closed_count`.
- `domains/stats/daily_monthly_rollup.go`: the single call site in
  `Refresh` reads the CTE result via `QueryRow` and, when
  `skipped_closed_count > 0`, logs a `slog.Warn` and increments
  `metrics.RecordStatsMonthlyClosedSkipped(n)`.
- `metrics/stats_reconciliation_metrics.go`: new counter
  `llm_gateway_stats_monthly_closed_skipped_total`.

## Why count against `daily_groups` (not the entire `stats_usage_monthly`)
A previous version of the CTE simply counted `status='closed'` rows
in the month range. That counted rows whose PK was not actually
targeted by the upsert (e.g. a quiet closed month with no
`stats_usage_daily` input) — every hourly Refresh would inflate the
counter by the full closed-row cardinality even though no
suppression actually happened. The new CTE joins against
`daily_groups` (the intended target PK set), so the counter only
increments for *actual* closed-PK suppressions, at most once per
closed PK per Refresh.

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