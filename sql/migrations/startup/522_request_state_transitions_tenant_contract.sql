-- Migration 522: reconcile request_state_transitions schema contract
--
-- Renumbered from 521 to clear a sequence-number collision with
-- 521_repair_state_transitions_tenant.sql (the deployed, changelog-verified
-- superset that also installs RLS policies). On any environment that already
-- ran 521 this migration is a no-op: ADD COLUMN IF NOT EXISTS, UPDATE backfill
-- is vacuous (tenant_id is already NOT NULL DEFAULT 'default'), and CREATE
-- INDEX IF NOT EXISTS skips. Retained for audit lineage.
--
-- Purpose
-- ───────
-- Repair environments whose migration ledger records 511/515 but whose
-- request_state_transitions table predates the tenant-scoped contract.
-- The repair is forward-only and safe for existing rows: legacy transitions
-- are assigned to the default tenant before tenant_id becomes mandatory.

BEGIN;

ALTER TABLE request_state_transitions
    ADD COLUMN IF NOT EXISTS tenant_id TEXT;

UPDATE request_state_transitions
SET tenant_id = 'default'
WHERE tenant_id IS NULL;

ALTER TABLE request_state_transitions
    ALTER COLUMN tenant_id SET NOT NULL;

CREATE INDEX IF NOT EXISTS idx_state_transitions_tenant_request
    ON request_state_transitions (tenant_id, request_id, created_at DESC);

COMMIT;

-- POST_CONDITION: SELECT 1 FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'request_state_transitions' AND column_name = 'tenant_id' AND is_nullable = 'NO';
-- POST_CONDITION: SELECT 1 FROM pg_indexes WHERE schemaname = current_schema() AND tablename = 'request_state_transitions' AND indexname = 'idx_state_transitions_tenant_request';
