\set ON_ERROR_STOP on
\set close_start '2026-11-22T16:00:00Z'
\set close_end '2026-11-23T16:00:00Z'

BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY;
SET LOCAL TIME ZONE 'UTC';
SET LOCAL row_security = off;
SET LOCAL statement_timeout = '10min';
SET LOCAL lock_timeout = '5s';
SELECT to_regclass('public.request_metrics_projection') IS NOT NULL AS has_projection
\gset

COPY (
  SELECT a.attempt_id, a.tenant_id, a.request_id, l.parent_request_id, a.job_id,
         a.traffic_class, a.source, a.attempt_no, a.provider_id, a.credential_id, a.model,
         a.prepared_at, a.sent_at, a.finished_at, a.delivery, a.outcome, a.http_status,
         a.provider_request_id, a.input_tokens, a.output_tokens, a.cache_read_tokens, a.cache_write_tokens,
         a.audio_input_duration_us, a.audio_billed_duration_us, a.audio_output_duration_us,
         a.generated_images, a.video_output_duration_us, a.media_usage_source, a.usage_source,
         a.estimated_amount, a.estimated_currency, a.rate_snapshot,
         a.reported_amount, a.reported_currency, a.reported_semantics
  FROM public.provider_call_attempts a
  LEFT JOIN public.request_reconciliation_links l
    ON (l.tenant_id, l.child_request_id) = (a.tenant_id, a.request_id)
  WHERE (a.sent_at >= :'close_start'::timestamptz AND a.sent_at < :'close_end'::timestamptz)
     OR (a.sent_at IS NULL AND a.prepared_at >= :'close_start'::timestamptz
                         AND a.prepared_at < :'close_end'::timestamptz)
  ORDER BY coalesce(a.sent_at, a.prepared_at), a.attempt_id
) TO STDOUT WITH (FORMAT csv, HEADER, NULL '\N')
\g 01-attempts.csv

COPY (
  SELECT invoice_id, provider_id, account_ref, currency, invoice_ref, month_start,
         timezone, window_start, window_end, credential_ids, stated_total, content_sha256, imported_at,
         (window_start = :'close_start'::timestamptz AND window_end = :'close_end'::timestamptz) AS exact_close_day
  FROM public.provider_invoices
  WHERE window_start < :'close_end'::timestamptz AND window_end > :'close_start'::timestamptz
  ORDER BY provider_id, window_start, invoice_id
) TO STDOUT WITH (FORMAT csv, HEADER, NULL '\N')
\g 02-invoices.csv

COPY (
  SELECT l.invoice_id, l.line_ref, l.kind, l.provider_request_id, l.amount, l.model,
         l.bucket_sent_attempts, l.bucket_input_tokens, l.bucket_output_tokens,
         l.bucket_audio_input_duration_us, l.bucket_audio_billed_duration_us, l.bucket_audio_output_duration_us,
         l.bucket_generated_images, l.bucket_video_output_duration_us
  FROM public.provider_invoice_lines l JOIN public.provider_invoices i USING (invoice_id)
  WHERE i.window_start < :'close_end'::timestamptz AND i.window_end > :'close_start'::timestamptz
  ORDER BY l.invoice_id, l.line_ref
) TO STDOUT WITH (FORMAT csv, HEADER, NULL '\N')
\g 03-invoice-lines.csv

COPY (
  SELECT rate_id, provider_id, credential_id, model, reference, currency,
         effective_from, effective_to, terms, content_sha256, imported_at
  FROM public.supplier_contract_rates
  WHERE effective_from < :'close_end'::timestamptz
    AND (effective_to IS NULL OR effective_to > :'close_start'::timestamptz)
  ORDER BY provider_id, credential_id, model, effective_from, rate_id
) TO STDOUT WITH (FORMAT csv, HEADER, NULL '\N')
\g 04-contract-rates.csv

COPY (
  SELECT DISTINCT ON (p.tenant_id, p.id)
         p.tenant_id, p.id AS posting_id, p.ref_type, p.ref_id AS request_id,
         l.parent_request_id, p.entry_type, p.pool, p.amount AS signed_credits, p.created_at AS posted_at
  FROM public.credit_ledger_with_current_month p
  LEFT JOIN public.request_reconciliation_links l
    ON (l.tenant_id, l.child_request_id) = (p.tenant_id, p.ref_id)
  WHERE p.created_at >= :'close_start'::timestamptz AND p.created_at < :'close_end'::timestamptz
    AND p.ref_type = 'request' AND p.ref_id IS NOT NULL
    AND p.entry_type IN ('consume', 'refund', 'reversal', 'adjust', 'adjustment')
  ORDER BY p.tenant_id, p.id, p.created_at DESC
) TO STDOUT WITH (FORMAT csv, HEADER, NULL '\N')
\g 05-request-postings.csv

COPY (
  WITH day_debits AS (
    SELECT DISTINCT p.tenant_id, coalesce(l.parent_request_id, p.ref_id) AS client_request_id
    FROM public.credit_ledger_with_current_month p
    LEFT JOIN public.request_reconciliation_links l
      ON (l.tenant_id, l.child_request_id) = (p.tenant_id, p.ref_id)
    WHERE p.created_at >= :'close_start'::timestamptz AND p.created_at < :'close_end'::timestamptz
      AND p.ref_type = 'request' AND p.ref_id IS NOT NULL AND p.entry_type = 'consume' AND p.amount < 0
  )
  SELECT DISTINCT ON (p.tenant_id, p.id)
         p.tenant_id, p.id AS posting_id, p.ref_id AS request_id, l.parent_request_id,
         p.entry_type, p.pool, p.amount AS signed_credits, p.created_at AS posted_at
  FROM public.credit_ledger_with_current_month p
  LEFT JOIN public.request_reconciliation_links l
    ON (l.tenant_id, l.child_request_id) = (p.tenant_id, p.ref_id)
  JOIN day_debits d ON (d.tenant_id, d.client_request_id) = (p.tenant_id, coalesce(l.parent_request_id, p.ref_id))
  WHERE p.created_at >= :'close_end'::timestamptz AND p.created_at <= transaction_timestamp()
    AND p.ref_type = 'request' AND p.ref_id IS NOT NULL AND p.entry_type IN ('refund', 'reversal')
  ORDER BY p.tenant_id, p.id, p.created_at DESC
) TO STDOUT WITH (FORMAT csv, HEADER, NULL '\N')
\g 05b-post-close-returns.csv

COPY (
  SELECT w.tenant_id, w.request_id, l.parent_request_id, w.status, w.stage, w.created_at, w.completed_at,
         w.upstream_request_at, w.upstream_response_at, w.upstream_provider_id, w.upstream_credential_id
  FROM public.request_wal_with_current_month w
  LEFT JOIN public.request_reconciliation_links l
    ON (l.tenant_id, l.child_request_id) = (w.tenant_id, w.request_id)
  WHERE w.created_at >= :'close_start'::timestamptz AND w.created_at < :'close_end'::timestamptz
  ORDER BY w.tenant_id, w.request_id, w.created_at, w.stage
) TO STDOUT WITH (FORMAT csv, HEADER, NULL '\N')
\g 06-wal-receipts.csv

COPY (
  SELECT f.tenant_id, f.request_id, l.parent_request_id, f.fact_id, f.revision, f.occurred_at, f.finalized_at,
         f.status, f.traffic_class, f.provider_id, f.credential_id, f.canonical_id, f.raw_model_name,
         f.prompt_tokens, f.completion_tokens, f.cache_read_tokens, f.cache_write_tokens, f.total_tokens,
         f.usage_source, f.cost_amount, f.cost_currency, f.credits_charged, f.source
  FROM public.usage_facts f
  LEFT JOIN public.request_reconciliation_links l
    ON (l.tenant_id, l.child_request_id) = (f.tenant_id, f.request_id)
  WHERE f.occurred_at >= :'close_start'::timestamptz AND f.occurred_at < :'close_end'::timestamptz
  ORDER BY f.tenant_id, f.request_id, f.occurred_at, f.revision, f.finalized_at, f.fact_id
) TO STDOUT WITH (FORMAT csv, HEADER, NULL '\N')
\g 07-usage-facts.csv

\if :has_projection
COPY (
  SELECT tenant_id, request_id, occurred_at, source_fact_id, source_revision, source_finalized_at,
         status, traffic_class, provider_id, credential_id, prompt_tokens, completion_tokens, total_tokens,
         usage_source, cost_amount, cost_currency, credits_charged
  FROM public.request_metrics_projection
  WHERE occurred_at >= :'close_start'::timestamptz AND occurred_at < :'close_end'::timestamptz
    AND source_fact_id IS NOT NULL
  ORDER BY tenant_id, request_id, occurred_at, source_fact_id
) TO STDOUT WITH (FORMAT csv, HEADER, NULL '\N')
\g 08-v2-projection.csv
\endif

COPY (
  SELECT :'capture_ref' AS capture_ref, current_database() AS source_database,
         'Asia/Shanghai' AS close_timezone, :'close_start'::timestamptz AS utc_start,
         :'close_end'::timestamptz AS utc_end_exclusive,
         transaction_timestamp() AS transaction_started_at, pg_current_snapshot()::text AS transaction_snapshot,
         current_setting('transaction_isolation') AS isolation, current_setting('transaction_read_only') AS read_only,
         :'has_projection'::boolean AS has_v2_projection
) TO STDOUT WITH (FORMAT csv, HEADER, NULL '\N')
\g 00-snapshot.csv

COMMIT;
