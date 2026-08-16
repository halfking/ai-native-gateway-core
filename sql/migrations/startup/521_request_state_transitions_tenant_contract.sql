-- Migration 521: reconcile request_state_transitions schema contract
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
