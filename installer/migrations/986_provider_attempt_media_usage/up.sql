-- Keep media measurements separate from token usage and from client request parameters.
\set ON_ERROR_STOP on
BEGIN;
SET lock_timeout = '5s';
SET statement_timeout = '10min';
ALTER TABLE public.provider_call_attempts
    ADD COLUMN IF NOT EXISTS audio_input_duration_us bigint CHECK (audio_input_duration_us >= 0),
    ADD COLUMN IF NOT EXISTS audio_billed_duration_us bigint CHECK (audio_billed_duration_us >= 0),
    ADD COLUMN IF NOT EXISTS audio_output_duration_us bigint CHECK (audio_output_duration_us >= 0),
    ADD COLUMN IF NOT EXISTS generated_images bigint CHECK (generated_images >= 0),
    ADD COLUMN IF NOT EXISTS video_output_duration_us bigint CHECK (video_output_duration_us >= 0),
    ADD COLUMN IF NOT EXISTS media_usage_source text;

INSERT INTO public.schema_migrations (version, description)
VALUES ('986', 'measured supplier attempt media usage')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;
COMMIT;
