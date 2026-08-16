-- Down migration for 521_request_state_transitions_tenant_contract.sql
--
-- 521 only adds tenant_id + idx_state_transitions_tenant_request to
-- request_state_transitions (purely additive). Symmetric reverse:
--   - drop the tenant-scoped index (cheap, no data impact)
--   - drop tenant_id (NULLable first, in case the env still has legacy rows)
--
-- Environments whose request_state_transitions predates 511 itself should
-- stay in tenant_id-less form after rollback; envs that already had 511
-- are unaffected because IF EXISTS keeps the script idempotent.

BEGIN;

DROP INDEX IF EXISTS idx_state_transitions_tenant_request;

ALTER TABLE request_state_transitions
    DROP COLUMN IF EXISTS tenant_id;

COMMIT;