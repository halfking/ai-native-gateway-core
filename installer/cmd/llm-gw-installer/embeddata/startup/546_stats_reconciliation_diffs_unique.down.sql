-- 546_stats_reconciliation_diffs_unique.down.sql
-- Rollback: drop the unique index. After this, a binary-shard that crosses
-- a UTC day boundary will re-introduce duplicate diff rows for that date.
-- Only roll back if you have also reverted the binary-shard logic in
-- reconcileDaily.

BEGIN;

DROP INDEX IF EXISTS idx_stats_reconciliation_diffs_run_dim_metric;

COMMIT;
