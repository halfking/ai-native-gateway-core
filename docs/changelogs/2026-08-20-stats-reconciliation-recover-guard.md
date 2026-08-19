# stats: defer recover + auto-shard reconciliation (P1-1)

## Background
Audit of `domains/stats/reconciliation.go` identified two failure modes
that could leave a `stats_reconciliation_runs` row stuck in
`status='running'`:

1. A panic inside `reconcileDaily` (nil deref, type mismatch in
   `Scan`, etc.) propagates out of `ReconcilePeriod` without ever
   calling `finishRun`, leaving the run row in `running` forever.
2. The 100k hard row cap in `reconcileDaily` was originally a safety
   belt, but in production the 7-day lookback window can exceed it
   under multi-tenant high-cardinality load. The cap produced
   recurring "row limit exceeded" errors that lost reconciliation
   coverage for the tail of the period.

## Changes
- `domains/stats/reconciliation.go`: `ReconcilePeriod` now installs a
  `defer recover()` immediately after the run row INSERT. On panic, the
  run is marked `failed` via `finishRun(context.Background(), ...)` and
  the panic is re-raised so the original stack trace is preserved at
  the caller. `context.Background()` is intentional: a cancelled `ctx`
  must not prevent the status UPDATE from reaching the database.
- `domains/stats/reconciliation.go`: `reconcileDaily` is split into a
  thin `reconcileDaily` wrapper and a `reconcileDailyOnce` worker.
  When the worker hits `maxReconciliationRows`, the wrapper splits the
  window in half and recurses, accumulating totals. The 100k cap is
  retained as a final safety net when the window cannot be split
  below 1 second.
- `domains/stats/reconciliation.go`: `ReconciliationWorker.db` is now
  typed as a `DBQuerier` interface (`Exec`/`Query`/`QueryRow`) so unit
  tests can drive reconciliation with `pgxmock.PgxPoolIface` without
  standing up PostgreSQL. `NewReconciliationWorker(*pgxpool.Pool, ...)`
  keeps the production constructor unchanged. The one internal call
  site that requires the concrete `*pgxpool.Pool` (for
  `NewDailyMonthlyRollup`) uses a type assertion.
- `domains/stats/reconciliation.go`: package-private test seams
  `reconcileDailyOverride` and `finishRunOverride` allow tests to
  inject a controlled implementation without standing up a DB.
- `domains/stats/reconciliation_test.go`: new pgxmock unit tests for
  panic recovery, binary sharding, and `finishRun` UPDATE-failure
  tolerance.

## Operational impact
- Operators no longer need to manually mark a reconciliation run
  `failed` after a panic in the reconciler.
- The 7-day window can now reconcile multi-tenant production loads
  without hitting the safety cap in normal operation.
- The 100k cap is unchanged as a hard ceiling; an alert on
  "row limit still exceeded after min-granularity split" remains a
  clear signal that something is structurally wrong (e.g. dimension
  explosion).

## Migration
None. No schema change.
