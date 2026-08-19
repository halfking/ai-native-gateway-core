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
  (and `*.down.sql`): add / extend the
  `stats_reconciliation_diffs_resolution_check` CHECK constraint to
  allow `'phantom_open'`. The down migration reclassifies any existing
  `phantom_open` rows back to `'open'` before tightening the constraint.
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

## Migration
Apply 545 alongside the next gateway rollout. Order: gateway code
deploy (which starts writing `phantom_open`) before the migration is
strictly required, but apply 545 in the same maintenance window to
avoid the CHECK constraint rejecting the new value. Down-migration is
safe if no `phantom_open` rows exist; if any exist, the down migration
reclassifies them to `'open'` first.