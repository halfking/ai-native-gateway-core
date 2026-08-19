# stats: explicit phantom-row guard in reconciliation (P1-4)

## Background
In `reconcileDailyOnce`, when iterating `stats_usage_daily` projections,
a "phantom" row (projection exists but no matching `usage_facts` source
row) is currently recorded as a regular diff with `resolution='open'`.
The `canAutoRepair` check happened to exclude it because the implicit
relative-diff `abs(0 - projected) / projected = 1.0` exceeds
`autoRepairThreshold` (0.02), but that exclusion was implicit and would
break silently if the threshold were ever loosened. Phantom rows are
also indistinguishable in dashboards from regular mismatches, and they
should never enter `Refresh()`'s pending-repair path.

## Changes
- `domains/stats/reconciliation.go`: `canAutoRepair` now explicitly
  requires both `d.source != 0` and `d.projected != 0`. The previous
  implicit relDiff=1.0 exclusion is preserved but no longer load-bearing.
- `domains/stats/reconciliation.go`: phantom rows are now recorded
  with `resolution='phantom_open'` instead of `'open'`. They never
  enter `pendingRepairs` and are never passed to `Refresh()`.
- `sql/migrations/startup/545_stats_reconciliation_phantom_resolution.sql`
  (and `*.down.sql`): add a new CHECK constraint on
  `stats_reconciliation_diffs.resolution` allowing
  `('open','auto_repair_pending','auto_repaired','phantom_open',
  'rejected','approved','adjusted')`. The `adjusted` value is included
  because `admin/stats.go:405` already reads it as a terminal state
  even though no current writer produces it; omitting it would cause
  the new constraint to reject any pre-existing `adjusted` rows and
  abort the migration. The constraint is added `NOT VALID` first and
  then `VALIDATE CONSTRAINT` is run separately so the migration does
  not take an `ACCESS EXCLUSIVE` lock on the (potentially large)
  `stats_reconciliation_diffs` table.
- The down migration now **fails loudly** if any `phantom_open` or
  `adjusted` rows exist; it does not silently reclassify them to
  `open`, because doing so would convert them into approvable rows
  and any subsequent bulk approval would create `stats_adjustments`
  crediting or debiting tenants for differences that have no
  source-of-truth behind them. Operators must reclassify
  `phantom_open` rows to `'rejected'` (or delete them) before
  rolling back 545.
- `installer/cmd/llm-gw-installer/embeddata/startup/545_*`: mirror the
  migration into the installer's embedded migration set.
- `admin/stats.go`: `handleStatsReconciliationApprove` now treats
  `phantom_open` like an already-resolved diff (skips it) so the
  approval flow cannot accidentally approve or reject a phantom row.
- `domains/stats/reconciliation_test.go`: new unit tests covering
  phantom row → `phantom_open`, real small diff → `auto_repair_pending`,
  real large diff → `open`, and defence-in-depth against threshold
  loosening.

## Operational impact
- `phantom_open` rows surface in operator dashboards and admin queries
  as a distinct category from regular `open` mismatches.
- No change in auto-repair behavior for legitimate small diffs.
- The CHECK constraint migration is non-destructive: existing `open`
  rows continue to pass the new constraint; only the new value is
  permitted, not retroactively populated.
- The migration does **not** block readers or writers of
  `stats_reconciliation_diffs` while it runs (NOT VALID + separate
  VALIDATE).

## Migration
Apply migration 545 **before** the gateway code that writes
`phantom_open` rolls out. The new gateway code is forward-compatible
with the old schema (it just writes the new value), and applying the
constraint first means the deployment is monotonic in the safe
direction. The constraint is `NOT VALID` at ADD time, so adding it
does not take a write-blocking lock; `VALIDATE` runs separately and
takes only `SHARE UPDATE EXCLUSIVE`.

Down-migration: roll back is only safe when zero `phantom_open` and
zero `adjusted` rows exist; the down migration enforces this and
returns an error otherwise.