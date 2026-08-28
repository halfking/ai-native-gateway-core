-- Migration 612: enable an independent native Responses SSE capability key.
-- Default-off: existing non-stream capability does not imply stream support.

BEGIN;

ALTER TABLE public.credential_model_capabilities
    DROP CONSTRAINT IF EXISTS credential_model_capabilities_capability_check;

ALTER TABLE public.credential_model_capabilities
    ADD CONSTRAINT credential_model_capabilities_capability_check
    CHECK (capability IN ('native_responses_nonstream', 'native_responses_stream'));

COMMENT ON TABLE public.credential_model_capabilities IS
    'Verified capabilities scoped to one credential-model binding; native Responses non-stream and stream are independent opt-ins.';

COMMIT;
