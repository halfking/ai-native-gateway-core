-- 538_stats_reconciliation_tenant.sql
-- Backfill the tenant scope needed to enforce tenant-admin isolation on diffs.

BEGIN;

ALTER TABLE IF EXISTS stats_reconciliation_diffs
    ADD COLUMN IF NOT EXISTS tenant_id text NOT NULL DEFAULT 'default';

CREATE INDEX IF NOT EXISTS idx_stats_reconciliation_diffs_tenant
    ON stats_reconciliation_diffs (tenant_id, created_at DESC);

COMMIT;
