-- Migration 1007 down: restore credential_model_capabilities CHECK to
-- the two-value Responses-only constraint (migration 613 state).

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

ALTER TABLE public.credential_model_capabilities
    DROP CONSTRAINT IF EXISTS credential_model_capabilities_capability_check;

-- Restore migration 613 state: only Responses family
ALTER TABLE public.credential_model_capabilities
    ADD CONSTRAINT credential_model_capabilities_capability_check
    CHECK (capability IN ('native_responses_nonstream', 'native_responses_stream'));

COMMENT ON TABLE public.credential_model_capabilities IS
    'Verified capabilities scoped to one credential-model binding; native Responses non-stream and stream are independent opt-ins.';

COMMIT;
