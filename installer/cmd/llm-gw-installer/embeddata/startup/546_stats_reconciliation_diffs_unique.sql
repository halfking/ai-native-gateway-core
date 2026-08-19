-- 546_stats_reconciliation_diffs_unique.sql
-- Add a uniqueness constraint on (run_id, dimension_type, dimension_key, metric)
-- so that when reconcileDaily's binary-shard recurses across a UTC day
-- boundary (e.g. a 7-day window split at 3.5 days), the same projection row
-- cannot be inserted into stats_reconciliation_diffs twice.
--
-- The reconciler now uses INSERT ... ON CONFLICT DO NOTHING to absorb the
-- duplicate, so the diff counter semantics ("one diff per run x dimension x
-- metric") are preserved regardless of sharding path.
--
-- This is a forward-only safety net. It is added as a non-deferrable unique
-- index because the underlying INSERTs are single-row and high-frequency; a
-- partial unique index would not help.

BEGIN;

CREATE UNIQUE INDEX IF NOT EXISTS idx_stats_reconciliation_diffs_run_dim_metric
    ON stats_reconciliation_diffs (run_id, dimension_type, dimension_key, metric);

COMMIT;
