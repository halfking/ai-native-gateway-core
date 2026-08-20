-- 548_stats_reconciliation_diffs_identity.sql
-- Correct the reconciliation diff conflict identity introduced by 546.
--
-- A reconciliation run spans multiple UTC days and can cover multiple
-- tenants. The original 546 index omitted tenant_id, and the original
-- dimension_key omitted the day, so legitimate diffs could collide and be
-- silently discarded by INSERT ... ON CONFLICT DO NOTHING.
--
-- Reconciliation now prefixes its dimension_key with tenant and day. The
-- explicit tenant_id column remains in the unique identity as defense in
-- depth, and makes the schema contract clear to future writers.

BEGIN;

DROP INDEX IF EXISTS idx_stats_reconciliation_diffs_run_dim_metric;

CREATE UNIQUE INDEX IF NOT EXISTS idx_stats_reconciliation_diffs_run_tenant_dim_metric
    ON stats_reconciliation_diffs (run_id, tenant_id, dimension_type, dimension_key, metric);

COMMIT;
