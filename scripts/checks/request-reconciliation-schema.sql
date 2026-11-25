SELECT EXISTS (SELECT 1 FROM public.schema_migrations WHERE version = '982')
   AND (SELECT count(*) FROM public.schema_migrations WHERE (version, description) IN (
       ('925', 'effective supplier contract rate evidence'),
       ('985', 'client and internal request reconciliation links'),
       ('986', 'measured supplier attempt media usage'),
       ('987', 'normalized supplier invoice evidence'),
       ('988', 'structured supplier invoice usage buckets')
   )) = 5
   AND to_regclass('public.request_reconciliation_links') IS NOT NULL
   AND to_regclass('public.provider_invoices') IS NOT NULL
   AND to_regclass('public.supplier_contract_rates') IS NOT NULL
   AND (SELECT count(*) FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'provider_call_attempts'
          AND column_name IN ('audio_input_duration_us','audio_billed_duration_us','audio_output_duration_us',
                              'generated_images','video_output_duration_us','media_usage_source')) = 6
   AND (SELECT count(*) FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'provider_invoice_lines'
          AND column_name IN ('bucket_sent_attempts','bucket_input_tokens','bucket_output_tokens',
                              'bucket_audio_input_duration_us','bucket_audio_billed_duration_us','bucket_audio_output_duration_us',
                              'bucket_generated_images','bucket_video_output_duration_us')) = 8
   AND (SELECT count(*) FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'usage_ledger_with_current_month'
          AND column_name IN ('request_id','tenant_id','ts')) = 3;
