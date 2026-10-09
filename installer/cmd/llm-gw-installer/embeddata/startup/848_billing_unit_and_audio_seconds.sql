-- Migration 848: Phase B — billing_unit + audio_seconds.
--
-- 1) credential_model_bindings.billing_unit
--    token (default) | audio_second | request | image
--    Operators mark ASR bindings as audio_second so routing/ops UI can
--    distinguish duration-priced offers from per-token chat offers.
--
-- 2) request_logs(_hot).audio_seconds
--    Persist ASR duration for audit / future telemetry. MaaS charge already
--    uses TokenUsage.AudioSeconds independently (ledger by request_id).
--
-- Compatibility: ADD COLUMN IF NOT EXISTS; defaults keep legacy behaviour.

BEGIN;

ALTER TABLE public.credential_model_bindings
    ADD COLUMN IF NOT EXISTS billing_unit text NOT NULL DEFAULT 'token';

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'cmb_billing_unit_check'
          AND conrelid = 'public.credential_model_bindings'::regclass
    ) THEN
        ALTER TABLE public.credential_model_bindings
            ADD CONSTRAINT cmb_billing_unit_check
            CHECK (billing_unit = ANY (ARRAY[
                'token'::text, 'audio_second'::text, 'request'::text, 'image'::text
            ]));
    END IF;
END $$;

COMMENT ON COLUMN public.credential_model_bindings.billing_unit IS
    'Pricing unit: token | audio_second | request | image. Independent of billing_mode (routing tier).';

ALTER TABLE public.request_logs_hot
    ADD COLUMN IF NOT EXISTS audio_seconds double precision;

ALTER TABLE public.request_logs
    ADD COLUMN IF NOT EXISTS audio_seconds double precision;

COMMENT ON COLUMN public.request_logs_hot.audio_seconds IS
    'ASR/TTS wall-clock duration in seconds (2026-10-08 Phase B).';

INSERT INTO public.schema_migrations (version, description)
VALUES ('848', 'cmb.billing_unit + request_logs.audio_seconds for media duration billing')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
