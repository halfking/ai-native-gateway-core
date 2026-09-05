-- Migration 521: repair request_state_transitions tenant isolation
--
-- Purpose: Migration 511 used CREATE TABLE IF NOT EXISTS, so installations
-- with the pre-existing table never received tenant_id, its index, or RLS.
-- Status: active
-- Idempotent: YES
-- Changelog:
--   2026-08-16 v1.0 Add the missing tenant contract to legacy tables
-- Rollback: 521_repair_state_transitions_tenant.down.sql

BEGIN;

ALTER TABLE request_state_transitions
    ADD COLUMN IF NOT EXISTS tenant_id TEXT NOT NULL DEFAULT 'default';

COMMENT ON COLUMN request_state_transitions.tenant_id IS
    'Tenant ID matching request_logs.tenant_id for RLS isolation';

CREATE INDEX IF NOT EXISTS idx_state_transitions_tenant_request
    ON request_state_transitions (tenant_id, request_id, created_at DESC);

ALTER TABLE request_state_transitions ENABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS state_transitions_tenant_isolation
    ON request_state_transitions;
CREATE POLICY state_transitions_tenant_isolation
    ON request_state_transitions
    USING (tenant_id = current_setting('app.current_tenant', true)::TEXT)
    WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::TEXT);

DROP POLICY IF EXISTS state_transitions_super_admin_bypass
    ON request_state_transitions;
CREATE POLICY state_transitions_super_admin_bypass
    ON request_state_transitions
    USING (
        current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true'
    )
    WITH CHECK (
        current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true'
    );

COMMIT;

-- Verification:
-- SELECT column_name, is_nullable, column_default
-- FROM information_schema.columns
-- WHERE table_name = 'request_state_transitions' AND column_name = 'tenant_id';
