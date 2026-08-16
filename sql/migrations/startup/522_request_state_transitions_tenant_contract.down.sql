-- Down migration for 522_request_state_transitions_tenant_contract.sql
--
-- Renumbered from 521 to clear a sequence-number collision with
-- 521_repair_state_transitions_tenant.sql. WARNING: on environments where
-- 521 also ran, running this down migration drops tenant_id and the tenant-
-- scoped index that 521's RLS policies depend on — roll back 521 too, or
-- skip this down migration. Down migrations are operator-gated.
--
-- 522 only adds tenant_id + idx_state_transitions_tenant_request to
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