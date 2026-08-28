-- Rollback for migration 612: restore the previous non-stream-only key set.
BEGIN;

DELETE FROM public.credential_model_capabilities
 WHERE capability = 'native_responses_stream';

ALTER TABLE public.credential_model_capabilities
    DROP CONSTRAINT IF EXISTS credential_model_capabilities_capability_check;

ALTER TABLE public.credential_model_capabilities
    ADD CONSTRAINT credential_model_capabilities_capability_check
    CHECK (capability = 'native_responses_nonstream');

COMMIT;
