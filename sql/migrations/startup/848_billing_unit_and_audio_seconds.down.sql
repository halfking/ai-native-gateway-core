BEGIN;

ALTER TABLE public.request_logs_hot DROP COLUMN IF EXISTS audio_seconds;
ALTER TABLE public.request_logs DROP COLUMN IF EXISTS audio_seconds;

ALTER TABLE public.credential_model_bindings
    DROP CONSTRAINT IF EXISTS cmb_billing_unit_check;
ALTER TABLE public.credential_model_bindings
    DROP COLUMN IF EXISTS billing_unit;

DELETE FROM public.schema_migrations WHERE version = '848';

COMMIT;
