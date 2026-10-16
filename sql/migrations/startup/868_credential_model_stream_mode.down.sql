BEGIN;

ALTER TABLE public.credential_model_bindings
    DROP CONSTRAINT IF EXISTS credential_model_bindings_stream_mode_check;

ALTER TABLE public.credential_model_bindings
    DROP COLUMN IF EXISTS stream_mode;

COMMIT;
