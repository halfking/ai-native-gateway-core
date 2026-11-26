package reconciliation

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type InvoiceReport struct {
	Version string            `json:"version"`
	ReadAt  time.Time         `json:"read_at"`
	Invoice json.RawMessage   `json:"invoice"`
	Summary json.RawMessage   `json:"summary"`
	Lines   []json.RawMessage `json:"lines"`
	Limit   int               `json:"limit"`
	Offset  int               `json:"offset"`
}

const invoiceReportVersion = "supplier-invoice-v1"

const invoiceFields = `invoice_id, provider_id, account_ref, currency, invoice_ref, to_char(month_start, 'YYYY-MM') AS month,
	timezone, window_start, window_end, timezone(timezone, window_start)::date AS period_start_day,
	timezone(timezone, window_end)::date AS period_end_day, credential_ids, stated_total::text, content_sha256, imported_by, imported_at`

func ListInvoices(ctx context.Context, pool *pgxpool.Pool, providerID int64, month string, limit, offset int) ([]json.RawMessage, error) {
	monthStart := month + "-01"
	return listInvoices(ctx, pool, providerID, &monthStart, nil, nil, limit, offset)
}

// ListDailyInvoices selects the exact requested day window. The invoice's
// local calendar can place the same window in another month.
func ListDailyInvoices(ctx context.Context, pool *pgxpool.Pool, providerID int64, day, zone string, limit, offset int) ([]json.RawMessage, error) {
	start, end, err := NaturalDay(day, zone)
	if err != nil {
		return nil, err
	}
	return listInvoices(ctx, pool, providerID, nil, &start, &end, limit, offset)
}

func listInvoices(ctx context.Context, pool *pgxpool.Pool, providerID int64, monthStart *string, start, end *time.Time, limit, offset int) ([]json.RawMessage, error) {
	tx, err := beginPlatformEvidenceRead(ctx, pool)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var items []json.RawMessage
	err = tx.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(p) ORDER BY invoice_id), '[]'::jsonb) FROM (
		SELECT `+invoiceFields+` FROM public.provider_invoices WHERE ($1::bigint=0 OR provider_id=$1)
		  AND ($2::text IS NULL OR month_start=$2::text::date)
		  AND ($5::timestamptz IS NULL OR (window_start=$5 AND window_end=$6))
		ORDER BY invoice_id LIMIT $3 OFFSET $4) p`, providerID, monthStart, limit, offset, start, end).Scan(&items)
	if err != nil {
		return nil, err
	}
	return items, tx.Commit(ctx)
}

// ReadInvoiceReport reconciles a frozen invoice against the currently available
// per-call evidence in a single snapshot. Only explicit, disjoint usage cohorts
// with matching measured quantities can contribute comparable bucket amounts.
// Coverage and duplicate checks use the supplier, credentials and period.
// Monetary comparisons additionally require matching currencies.
func ReadInvoiceReport(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID, limit, offset int) (InvoiceReport, error) {
	tx, err := beginPlatformEvidenceRead(ctx, pool)
	if err != nil {
		return InvoiceReport{}, err
	}
	defer tx.Rollback(ctx)
	report, err := readInvoiceReport(ctx, tx, id, limit, offset)
	if err != nil {
		return InvoiceReport{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return InvoiceReport{}, err
	}
	return report, nil
}

func readInvoiceReport(ctx context.Context, tx pgx.Tx, id uuid.UUID, limit, offset int) (InvoiceReport, error) {
	report := InvoiceReport{Version: invoiceReportVersion, Limit: limit, Offset: offset}
	err := tx.QueryRow(ctx, invoiceReportSQL, id, limit, offset).Scan(&report.ReadAt, &report.Invoice, &report.Summary, &report.Lines)
	if err != nil {
		return InvoiceReport{}, err
	}
	report.ReadAt = report.ReadAt.UTC()
	return report, nil
}

func beginPlatformEvidenceRead(ctx context.Context, pool *pgxpool.Pool) (pgx.Tx, error) {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('app.bypass_rls','true',true), set_config('TimeZone','UTC',true)`); err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}

const invoiceReportSQL = `WITH bill AS (
	SELECT ` + invoiceFields + ` FROM public.provider_invoices WHERE invoice_id=$1
), scoped_attempts AS MATERIALIZED (
	SELECT a.credential_id, a.model, a.delivery, a.sent_at, a.provider_request_id,
	       a.input_tokens, a.output_tokens, a.audio_input_duration_us, a.audio_billed_duration_us,
	       a.audio_output_duration_us, a.generated_images, a.video_output_duration_us,
	       a.estimated_amount, a.estimated_currency,
	       (a.rate_snapshot #>> '{supplier_contract,rate_id}' IS NOT NULL) AS has_contract_rate,
	       a.reported_amount, a.reported_currency, a.reported_semantics
	FROM bill b JOIN public.provider_call_attempts a ON a.provider_id=b.provider_id AND a.credential_id=ANY(b.credential_ids)
	WHERE a.delivery IN ('sent','prepared','unknown')
	  AND COALESCE(a.sent_at,a.prepared_at)>=b.window_start AND COALESCE(a.sent_at,a.prepared_at)<b.window_end
), bucket_measurements AS (
	SELECT a.model,
	       count(*) FILTER (WHERE a.delivery='sent') AS observed_sends,
	       count(*) FILTER (WHERE a.delivery<>'sent') AS unconfirmed_attempts,
	       CASE WHEN count(*) FILTER (WHERE a.delivery='sent' AND a.input_tokens IS NULL)=0
	         THEN sum(a.input_tokens) FILTER (WHERE a.delivery='sent') END AS observed_input,
	       CASE WHEN count(*) FILTER (WHERE a.delivery='sent' AND a.output_tokens IS NULL)=0
	         THEN sum(a.output_tokens) FILTER (WHERE a.delivery='sent') END AS observed_output,
	       CASE WHEN count(*) FILTER (WHERE a.delivery='sent' AND a.audio_input_duration_us IS NULL)=0
	         THEN sum(a.audio_input_duration_us) FILTER (WHERE a.delivery='sent') END AS observed_audio_input_duration_us,
	       CASE WHEN count(*) FILTER (WHERE a.delivery='sent' AND a.audio_billed_duration_us IS NULL)=0
	         THEN sum(a.audio_billed_duration_us) FILTER (WHERE a.delivery='sent') END AS observed_audio_billed_duration_us,
	       CASE WHEN count(*) FILTER (WHERE a.delivery='sent' AND a.audio_output_duration_us IS NULL)=0
	         THEN sum(a.audio_output_duration_us) FILTER (WHERE a.delivery='sent') END AS observed_audio_output_duration_us,
	       CASE WHEN count(*) FILTER (WHERE a.delivery='sent' AND a.generated_images IS NULL)=0
	         THEN sum(a.generated_images) FILTER (WHERE a.delivery='sent') END AS observed_generated_images,
	       CASE WHEN count(*) FILTER (WHERE a.delivery='sent' AND a.video_output_duration_us IS NULL)=0
	         THEN sum(a.video_output_duration_us) FILTER (WHERE a.delivery='sent') END AS observed_video_output_duration_us,
	       count(a.estimated_amount) FILTER (WHERE a.delivery='sent' AND a.estimated_currency=b.currency AND a.has_contract_rate) AS comparable_estimates,
	       sum(a.estimated_amount) FILTER (WHERE a.delivery='sent' AND a.estimated_currency=b.currency AND a.has_contract_rate) AS estimate,
	       count(a.reported_amount) FILTER (WHERE a.delivery='sent' AND a.reported_currency=b.currency
	         AND a.reported_semantics='openrouter_usage_cost_usd') AS comparable_reports,
	       sum(a.reported_amount) FILTER (WHERE a.delivery='sent' AND a.reported_currency=b.currency
	         AND a.reported_semantics='openrouter_usage_cost_usd') AS reported
	FROM bill b CROSS JOIN scoped_attempts a GROUP BY a.model
), bucket_checks AS (
	SELECT l.line_ref, COALESCE(m.observed_sends,0) AS bucket_observed_sends,
	       COALESCE(m.unconfirmed_attempts,0) AS bucket_unconfirmed_attempts,
	       m.observed_input AS bucket_observed_input, m.observed_output AS bucket_observed_output,
	       m.observed_audio_input_duration_us AS bucket_observed_audio_input_duration_us,
	       m.observed_audio_billed_duration_us AS bucket_observed_audio_billed_duration_us,
	       m.observed_audio_output_duration_us AS bucket_observed_audio_output_duration_us,
	       m.observed_generated_images AS bucket_observed_generated_images,
	       m.observed_video_output_duration_us AS bucket_observed_video_output_duration_us,
	       (
	         SELECT CASE
	           WHEN bool_or(observed IS NULL) THEN 'bucket_usage_missing'
	           WHEN bool_or(declared<>observed) THEN 'bucket_usage_mismatch'
	         END
	         FROM (VALUES
	           (l.bucket_input_tokens, m.observed_input),
	           (l.bucket_output_tokens, m.observed_output),
	           (l.bucket_audio_input_duration_us, m.observed_audio_input_duration_us),
	           (l.bucket_audio_billed_duration_us, m.observed_audio_billed_duration_us),
	           (l.bucket_audio_output_duration_us, m.observed_audio_output_duration_us),
	           (l.bucket_generated_images, m.observed_generated_images),
	           (l.bucket_video_output_duration_us, m.observed_video_output_duration_us)
	         ) units(declared, observed)
	         WHERE declared IS NOT NULL
	       ) AS bucket_usage_status,
	       COALESCE(m.observed_sends-m.comparable_estimates,0) AS bucket_sends_without_comparable_estimate,
	       COALESCE(m.observed_sends-m.comparable_reports,0) AS bucket_sends_without_comparable_report,
	       CASE WHEN m.comparable_estimates=m.observed_sends THEN m.estimate END AS bucket_estimate,
	       CASE WHEN m.comparable_reports=m.observed_sends THEN m.reported END AS bucket_reported,
	       (SELECT count(*) FROM scoped_attempts a WHERE NULLIF(a.model,'') IS NULL) AS bucket_attempts_without_model,
	       EXISTS (
	         SELECT 1 FROM public.provider_invoices ob
	         JOIN public.provider_invoice_lines ol ON ol.invoice_id=ob.invoice_id
	         WHERE ob.provider_id=b.provider_id AND ob.credential_ids && b.credential_ids
	           AND ob.window_start<b.window_end AND ob.window_end>b.window_start
	           AND (ol.invoice_id,ol.line_ref)<>(l.invoice_id,l.line_ref)
	           AND ol.kind IN ('request_total','usage_bucket')
	           AND (
	             NULLIF(ol.model,'') IS NULL OR ol.model=l.model
	             OR (ol.kind='request_total' AND EXISTS (
	               SELECT 1 FROM scoped_attempts a
	               WHERE a.model=l.model AND a.provider_request_id=ol.provider_request_id
	                 AND a.delivery='sent' AND a.credential_id=ANY(ob.credential_ids)
	                 AND a.sent_at>=ob.window_start AND a.sent_at<ob.window_end
	             ))
	           )
	       ) AS bucket_overlap
	FROM bill b JOIN public.provider_invoice_lines l USING (invoice_id)
	LEFT JOIN bucket_measurements m ON m.model=l.model
	WHERE l.kind='usage_bucket' AND l.bucket_sent_attempts IS NOT NULL
), candidates AS (
	SELECT l.*, b.currency, b.provider_id, m.candidate_count,
	       a.attempt_id, a.sent_at, a.credential_id, a.model AS attempt_model,
	       (a.rate_snapshot #>> '{supplier_contract,rate_id}' IS NOT NULL) AS has_contract_rate,
	       a.estimated_amount, a.estimated_currency, a.reported_amount, a.reported_currency, a.reported_semantics,
	       EXISTS (
	         SELECT 1 FROM public.provider_call_attempts u
	         WHERE l.kind='request_total'
	           AND u.provider_id=b.provider_id AND u.provider_request_id=l.provider_request_id
	           AND u.credential_id=ANY(b.credential_ids) AND u.delivery IN ('prepared','unknown')
	           AND u.prepared_at>=b.window_start AND u.prepared_at<b.window_end
	       ) AS delivery_unconfirmed,
	       EXISTS (
	         SELECT 1 FROM public.provider_invoice_lines ol
	         JOIN public.provider_invoices ob ON ob.invoice_id=ol.invoice_id
	         WHERE ol.kind='request_total' AND ol.provider_request_id=l.provider_request_id
	           AND (ol.invoice_id,ol.line_ref)<>(l.invoice_id,l.line_ref)
	           AND ob.provider_id=b.provider_id
	           AND a.sent_at>=ob.window_start AND a.sent_at<ob.window_end AND a.credential_id=ANY(ob.credential_ids)
	       ) AS duplicate_line
	FROM bill b JOIN public.provider_invoice_lines l USING (invoice_id)
	LEFT JOIN LATERAL (
	  SELECT count(*) AS candidate_count, min(attempt_id) AS attempt_id FROM (
	    SELECT a.attempt_id FROM public.provider_call_attempts a
	    WHERE l.kind='request_total' AND a.provider_id=b.provider_id AND a.provider_request_id=l.provider_request_id
	      AND a.credential_id=ANY(b.credential_ids) AND a.delivery='sent'
	      AND a.sent_at>=b.window_start AND a.sent_at<b.window_end
	    LIMIT 2
	  ) matches
	) m ON true
	LEFT JOIN public.provider_call_attempts a ON m.candidate_count=1 AND a.attempt_id=m.attempt_id
), classified AS (
	SELECT c.*, bc.bucket_observed_sends, bc.bucket_unconfirmed_attempts,
	       bc.bucket_observed_input, bc.bucket_observed_output, bc.bucket_attempts_without_model,
	       bc.bucket_observed_audio_input_duration_us, bc.bucket_observed_audio_billed_duration_us,
	       bc.bucket_observed_audio_output_duration_us, bc.bucket_observed_generated_images, bc.bucket_observed_video_output_duration_us,
	       bc.bucket_sends_without_comparable_estimate, bc.bucket_sends_without_comparable_report,
	       bc.bucket_estimate, bc.bucket_reported, CASE
	  WHEN kind='usage_bucket' AND bc.bucket_overlap THEN 'bucket_scope_overlap'
	  WHEN kind='usage_bucket' AND bc.bucket_attempts_without_model>0 THEN 'bucket_model_unconfirmed'
	  WHEN kind='usage_bucket' AND bc.bucket_unconfirmed_attempts>0 THEN 'bucket_delivery_unconfirmed'
	  WHEN kind='usage_bucket' AND bc.bucket_observed_sends<>bucket_sent_attempts THEN 'bucket_count_mismatch'
	  WHEN kind='usage_bucket' AND bc.bucket_usage_status IS NOT NULL THEN bc.bucket_usage_status
	  WHEN kind='usage_bucket' AND bucket_sent_attempts IS NOT NULL THEN 'bucket_matched'
	  WHEN kind='usage_bucket' THEN 'bucket_review'
	  WHEN kind<>'request_total' THEN 'bill_adjustment'
	  WHEN candidate_count>1 THEN 'attempt_ambiguous'
	  WHEN delivery_unconfirmed THEN 'delivery_unconfirmed'
	  WHEN candidate_count=0 THEN 'attempt_missing'
	  WHEN duplicate_line THEN 'invoice_reference_ambiguous'
	  WHEN NULLIF(model,'') IS NOT NULL AND attempt_model IS DISTINCT FROM model THEN 'model_mismatch'
	  ELSE 'matched'
	END AS match_status
	FROM candidates c LEFT JOIN bucket_checks bc USING (line_ref)
), lines AS (
	SELECT line_ref, kind, provider_request_id, amount AS invoice_amount, currency AS invoice_currency, model, description, match_status,
	       bucket_sent_attempts, bucket_input_tokens, bucket_output_tokens,
	       bucket_audio_input_duration_us, bucket_audio_billed_duration_us, bucket_audio_output_duration_us,
	       bucket_generated_images, bucket_video_output_duration_us,
	       bucket_observed_sends, bucket_observed_input, bucket_observed_output,
	       bucket_observed_audio_input_duration_us, bucket_observed_audio_billed_duration_us, bucket_observed_audio_output_duration_us,
	       bucket_observed_generated_images, bucket_observed_video_output_duration_us,
	       bucket_attempts_without_model, bucket_unconfirmed_attempts,
	       bucket_sends_without_comparable_estimate, bucket_sends_without_comparable_report,
	       CASE WHEN match_status='matched' THEN attempt_id END AS attempt_id,
	       CASE WHEN match_status='matched' THEN sent_at END AS sent_at,
	       CASE WHEN match_status='matched' THEN credential_id END AS credential_id,
	       CASE WHEN match_status='matched' THEN estimated_amount::text END AS captured_estimate,
	       CASE WHEN match_status='matched' THEN estimated_currency END AS estimated_currency,
	       CASE WHEN match_status='matched' AND estimated_amount IS NOT NULL
	            THEN CASE WHEN has_contract_rate THEN 'supplier_contract' ELSE 'legacy_unverified' END END AS estimate_basis,
	       CASE WHEN match_status='matched' THEN reported_amount::text END AS captured_reported,
	       CASE WHEN match_status='matched' THEN reported_currency END AS reported_currency,
	       CASE WHEN match_status='matched' THEN reported_semantics END AS reported_semantics,
	       CASE WHEN match_status='bucket_matched' THEN bucket_estimate
	            WHEN match_status='matched' AND estimated_currency=currency AND has_contract_rate THEN estimated_amount END AS comparable_estimate,
	       CASE WHEN match_status='bucket_matched' THEN bucket_reported
	            WHEN match_status='matched' AND reported_currency=currency
	          AND reported_semantics='openrouter_usage_cost_usd' THEN reported_amount END AS comparable_reported
	FROM classified
), totals AS (
	SELECT count(*) AS invoice_lines, sum(invoice_amount)::text AS invoice_total,
	       count(*) FILTER (WHERE match_status='matched') AS matched_lines,
	       sum(invoice_amount) FILTER (WHERE match_status='matched')::text AS matched_invoice_amount,
	       count(*) FILTER (WHERE match_status='bucket_matched') AS bucket_matched_lines,
	       sum(invoice_amount) FILTER (WHERE match_status='bucket_matched')::text AS bucket_matched_invoice_amount,
	       sum(bucket_observed_sends) FILTER (WHERE match_status='bucket_matched') AS recorded_sends_in_matched_buckets,
	       count(*) FILTER (WHERE match_status NOT IN ('matched','bucket_matched','bill_adjustment')) AS review_lines,
	       sum(invoice_amount) FILTER (WHERE match_status='bill_adjustment')::text AS adjustment_total,
	       count(comparable_estimate) AS estimate_comparable_lines,
	       sum(invoice_amount) FILTER (WHERE comparable_estimate IS NOT NULL)::text AS estimate_comparable_invoice_amount,
	       sum(comparable_estimate)::text AS comparable_estimated_amount,
	       sum(comparable_estimate-invoice_amount)::text AS estimated_minus_invoiced_amount,
	       count(comparable_reported) AS reported_comparable_lines,
	       sum(invoice_amount) FILTER (WHERE comparable_reported IS NOT NULL)::text AS reported_comparable_invoice_amount,
	       sum(comparable_reported)::text AS comparable_reported_amount,
	       sum(comparable_reported-invoice_amount)::text AS reported_minus_invoiced_amount,
	       (SELECT jsonb_object_agg(x.kind, x.amount) FROM (
	           SELECT kind, sum(invoice_amount)::text AS amount FROM lines GROUP BY kind
	       ) x) AS amounts_by_line_kind
	FROM lines
), send_coverage AS (
	SELECT count(*) FILTER (WHERE delivery='sent') AS recorded_sent_attempts,
	       count(*) FILTER (WHERE delivery='sent' AND provider_request_id IS NULL) AS recorded_sends_without_provider_request_id,
	       count(*) FILTER (WHERE delivery='sent' AND provider_request_id IS NOT NULL AND NOT has_request_total) AS recorded_sends_without_invoice_request_total,
	       count(*) FILTER (WHERE delivery IN ('prepared','unknown')) AS recorded_unconfirmed_attempts,
	       min(sent_at) AS earliest_recorded_send, max(sent_at) AS latest_recorded_send
	FROM (
	  SELECT a.delivery, a.sent_at, a.provider_request_id,
	         EXISTS (
	           SELECT 1 FROM public.provider_invoice_lines l
	           JOIN public.provider_invoices i ON i.invoice_id=l.invoice_id
	           WHERE l.kind='request_total' AND l.provider_request_id=a.provider_request_id
	             AND i.provider_id=b.provider_id
	             AND a.credential_id=ANY(i.credential_ids) AND a.sent_at>=i.window_start AND a.sent_at<i.window_end
	         ) AS has_request_total
	  FROM bill b CROSS JOIN scoped_attempts a
	) scoped
), page AS (
	SELECT line_ref, kind, provider_request_id, invoice_amount::text, invoice_currency, model, description, match_status, attempt_id, sent_at,
	       credential_id, captured_estimate, estimated_currency, estimate_basis, captured_reported, reported_currency, reported_semantics,
	       bucket_sent_attempts, bucket_input_tokens::text, bucket_output_tokens::text,
	       bucket_audio_input_duration_us::text, bucket_audio_billed_duration_us::text, bucket_audio_output_duration_us::text,
	       bucket_generated_images::text, bucket_video_output_duration_us::text,
	       bucket_observed_sends, bucket_observed_input::text, bucket_observed_output::text,
	       bucket_observed_audio_input_duration_us::text, bucket_observed_audio_billed_duration_us::text, bucket_observed_audio_output_duration_us::text,
	       bucket_observed_generated_images::text, bucket_observed_video_output_duration_us::text,
	       bucket_attempts_without_model, bucket_unconfirmed_attempts, bucket_sends_without_comparable_estimate, bucket_sends_without_comparable_report,
	       comparable_estimate::text, comparable_reported::text,
	       (comparable_estimate-invoice_amount)::text AS estimated_minus_invoiced_amount,
	       (comparable_reported-invoice_amount)::text AS reported_minus_invoiced_amount
	FROM lines ORDER BY line_ref LIMIT $2 OFFSET $3
)
SELECT transaction_timestamp(), to_jsonb(bill), to_jsonb(totals) || to_jsonb(send_coverage) || jsonb_build_object(
       'invoice_currency',bill.currency,'invoice_ref',bill.invoice_ref,'month',bill.month,
       'timezone',bill.timezone,'window_start',bill.window_start,'window_end',bill.window_end,
       'period_start_day',bill.period_start_day,'period_end_day',bill.period_end_day,
       'read_at',transaction_timestamp(),'version','` + invoiceReportVersion + `'),
       COALESCE((SELECT jsonb_agg(to_jsonb(page) ORDER BY line_ref) FROM page), '[]'::jsonb)
FROM bill CROSS JOIN totals CROSS JOIN send_coverage`
