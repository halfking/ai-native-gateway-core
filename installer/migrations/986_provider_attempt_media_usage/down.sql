\set ON_ERROR_STOP on
BEGIN;
SET lock_timeout = '5s';
SET statement_timeout = '10min';
ALTER TABLE public.provider_call_attempts
    DROP COLUMN IF EXISTS audio_input_duration_us,
    DROP COLUMN IF EXISTS audio_billed_duration_us,
    DROP COLUMN IF EXISTS audio_output_duration_us,
    DROP COLUMN IF EXISTS generated_images,
    DROP COLUMN IF EXISTS video_output_duration_us,
    DROP COLUMN IF EXISTS media_usage_source;
DELETE FROM public.schema_migrations WHERE version = '985';
COMMIT;
