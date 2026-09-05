-- Migration 521 rollback: remove repaired tenant isolation contract
-- Status: active
-- Idempotent: YES
-- Changelog:
--   2026-08-16 v1.0 Initial rollback
-- WARNING: destructive; run only after confirming no tenant-aware consumers.

BEGIN;

DROP POLICY IF EXISTS state_transitions_super_admin_bypass
    ON request_state_transitions;
DROP POLICY IF EXISTS state_transitions_tenant_isolation
    ON request_state_transitions;
DROP INDEX IF EXISTS idx_state_transitions_tenant_request;
ALTER TABLE request_state_transitions DROP COLUMN IF EXISTS tenant_id;

COMMIT;
