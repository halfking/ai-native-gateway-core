-- Optional whole-statement model usage cohorts on normalized supplier invoices.
\set ON_ERROR_STOP on
BEGIN;
SET lock_timeout = '5s';
SET statement_timeout = '10min';

ALTER TABLE public.provider_invoice_lines
    ADD COLUMN IF NOT EXISTS bucket_sent_attempts bigint,
    ADD COLUMN IF NOT EXISTS bucket_input_tokens bigint,
    ADD COLUMN IF NOT EXISTS bucket_output_tokens bigint,
    ADD COLUMN IF NOT EXISTS bucket_audio_input_duration_us bigint,
    ADD COLUMN IF NOT EXISTS bucket_audio_billed_duration_us bigint,
    ADD COLUMN IF NOT EXISTS bucket_audio_output_duration_us bigint,
    ADD COLUMN IF NOT EXISTS bucket_generated_images bigint,
    ADD COLUMN IF NOT EXISTS bucket_video_output_duration_us bigint;
ALTER TABLE public.provider_invoice_lines DROP CONSTRAINT IF EXISTS provider_invoice_lines_bucket_cohort;
ALTER TABLE public.provider_invoice_lines ADD CONSTRAINT provider_invoice_lines_bucket_cohort CHECK (
    (ROW(bucket_sent_attempts, bucket_input_tokens, bucket_output_tokens, bucket_audio_input_duration_us,
         bucket_audio_billed_duration_us, bucket_audio_output_duration_us, bucket_generated_images,
         bucket_video_output_duration_us) IS NULL)
    OR (
        kind = 'usage_bucket' AND NULLIF(btrim(model), '') IS NOT NULL
        AND bucket_sent_attempts IS NOT NULL AND bucket_sent_attempts > 0
        AND (bucket_input_tokens IS NULL OR bucket_input_tokens >= 0)
        AND (bucket_output_tokens IS NULL OR bucket_output_tokens >= 0)
        AND (bucket_audio_input_duration_us IS NULL OR bucket_audio_input_duration_us >= 0)
        AND (bucket_audio_billed_duration_us IS NULL OR bucket_audio_billed_duration_us >= 0)
        AND (bucket_audio_output_duration_us IS NULL OR bucket_audio_output_duration_us >= 0)
        AND (bucket_generated_images IS NULL OR bucket_generated_images >= 0)
        AND (bucket_video_output_duration_us IS NULL OR bucket_video_output_duration_us >= 0)
    )
);

CREATE INDEX IF NOT EXISTS idx_provider_invoices_window
    ON public.provider_invoices (window_start, window_end, invoice_id);

INSERT INTO public.schema_migrations (version, description)
VALUES ('987', 'structured supplier invoice usage buckets')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;
COMMIT;
