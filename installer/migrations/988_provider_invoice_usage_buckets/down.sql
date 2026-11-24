\set ON_ERROR_STOP on
BEGIN;
SET lock_timeout = '5s';
SET statement_timeout = '10min';

ALTER TABLE public.provider_invoice_lines
    DROP CONSTRAINT IF EXISTS provider_invoice_lines_bucket_cohort,
    DROP COLUMN IF EXISTS bucket_sent_attempts,
    DROP COLUMN IF EXISTS bucket_input_tokens,
    DROP COLUMN IF EXISTS bucket_output_tokens,
    DROP COLUMN IF EXISTS bucket_audio_input_duration_us,
    DROP COLUMN IF EXISTS bucket_audio_billed_duration_us,
    DROP COLUMN IF EXISTS bucket_audio_output_duration_us,
    DROP COLUMN IF EXISTS bucket_generated_images,
    DROP COLUMN IF EXISTS bucket_video_output_duration_us;
DROP INDEX IF EXISTS public.idx_provider_invoices_window;
DELETE FROM public.schema_migrations WHERE version = '988';
COMMIT;
