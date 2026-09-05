-- 548_stats_reconciliation_diffs_identity.down.sql
-- Restore the 546 index only after rolling back gateway code that writes
-- tenant/day-prefixed reconciliation dimension keys.

BEGIN;

DROP INDEX IF EXISTS idx_stats_reconciliation_diffs_run_tenant_dim_metric;

CREATE UNIQUE INDEX IF NOT EXISTS idx_stats_reconciliation_diffs_run_dim_metric
    ON stats_reconciliation_diffs (run_id, dimension_type, dimension_key, metric);

COMMIT;
