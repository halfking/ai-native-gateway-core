package reconciliation

var coverageColumns = []string{
	"local_day", "received_requests", "receipts_without_retained_terminal_usage",
	"orchestration_child_receipts", "orchestration_dispatches_registered", "orchestration_dispatches_unlinked",
	"terminal_usage_requests", "terminal_usage_without_retained_receipt", "terminal_usage_receipt_outside_window",
	"legacy_usage_request_keys", "legacy_usage_rows", "legacy_usage_without_retained_receipt",
	"legacy_usage_receipt_other_day", "legacy_usage_without_retained_terminal_usage",
	"posted_entries", "postings_without_retained_receipt", "postings_receipt_outside_window",
	"recorded_sends", "recorded_sends_without_estimate", "recorded_sends_without_reported_amount",
	"recorded_sends_without_contract_estimate",
	"recorded_media_sends", "recorded_media_sends_without_quantity",
	"recorded_sends_reported_amount_without_currency", "recorded_sends_without_provider_request_id",
}

// Seed keys with bounded scans of each clock. The lookups then check retained
// history by tenant and request, so a receipt in the preceding month is kept
// distinct from a receipt missing altogether. Each daily count has its own
// cohort. Legacy usage rows establish retained request keys, so the count of
// recorded sends still has no historical total.
const coverageFacts = `WITH postings AS (` + requestPostings + `),
legacy_usage_keys AS (
	SELECT timezone($4, ts)::date AS local_day, tenant_id, request_id, count(*) AS ledger_rows
	FROM public.usage_ledger_with_current_month
	WHERE ts >= $1 AND ts < $2 AND ($3 = '' OR tenant_id = $3)
	  AND NULLIF(request_id, '') IS NOT NULL
	GROUP BY 1, tenant_id, request_id
),
keys AS (
	SELECT tenant_id, request_id FROM public.request_wal_with_current_month
	WHERE created_at >= $1 AND created_at < $2 AND ($3 = '' OR tenant_id = $3)
	  AND ($3 = '' OR starts_with(error, 'gateway_admission:unassigned:') IS NOT TRUE)
	UNION
	SELECT tenant_id, request_id FROM public.usage_facts
	WHERE occurred_at >= $1 AND occurred_at < $2 AND ($3 = '' OR tenant_id = $3)
	  AND status IN ('success','failure','rate_limited','cancelled')
	UNION
	SELECT tenant_id, request_id FROM postings
	UNION
	SELECT tenant_id, request_id FROM legacy_usage_keys
), receipt_by_key AS (
	SELECT k.tenant_id, k.request_id, min(w.created_at) AS received_at, (l.child_request_id IS NOT NULL) AS orchestration_child
	FROM keys k JOIN public.request_wal_with_current_month w USING (tenant_id, request_id)
	LEFT JOIN public.request_reconciliation_links l ON l.tenant_id = k.tenant_id AND l.child_request_id = k.request_id
	WHERE $3 = '' OR starts_with(w.error, 'gateway_admission:unassigned:') IS NOT TRUE
	GROUP BY k.tenant_id, k.request_id, l.child_request_id
), usage_by_key AS (
	SELECT k.tenant_id, k.request_id, min(u.occurred_at) AS occurred_at
	FROM keys k JOIN public.usage_facts u USING (tenant_id, request_id)
	WHERE u.status IN ('success','failure','rate_limited','cancelled')
	GROUP BY k.tenant_id, k.request_id
), evidence AS (
	SELECT k.tenant_id, k.request_id, w.received_at, u.occurred_at, w.orchestration_child,
	       (w.received_at >= $1 AND w.received_at < $2) AS receipt_in_window
	FROM keys k
	LEFT JOIN receipt_by_key w USING (tenant_id, request_id)
	LEFT JOIN usage_by_key u USING (tenant_id, request_id)
), by_receipt AS (
	SELECT timezone($4, received_at)::date AS local_day,
	       count(*) FILTER (WHERE NOT orchestration_child) AS received_requests,
	       count(*) FILTER (WHERE NOT orchestration_child AND occurred_at IS NULL) AS receipts_without_retained_terminal_usage,
	       count(*) FILTER (WHERE orchestration_child) AS orchestration_child_receipts
	FROM evidence WHERE receipt_in_window GROUP BY 1
), by_dispatch AS (
	SELECT timezone($4, registered_at)::date AS local_day,
	       count(*) AS orchestration_dispatches_registered,
	       count(*) FILTER (WHERE child_request_id IS NULL) AS orchestration_dispatches_unlinked
	FROM public.request_reconciliation_links
	WHERE registered_at >= $1 AND registered_at < $2 AND ($3 = '' OR tenant_id = $3)
	GROUP BY 1
), by_terminal_usage AS (
	SELECT timezone($4, occurred_at)::date AS local_day,
	       count(*) AS terminal_usage_requests,
	       count(*) FILTER (WHERE received_at IS NULL) AS terminal_usage_without_retained_receipt,
	       count(*) FILTER (WHERE NOT receipt_in_window) AS terminal_usage_receipt_outside_window
	FROM evidence WHERE occurred_at >= $1 AND occurred_at < $2 GROUP BY 1
), by_legacy_usage AS (
	SELECT l.local_day, count(*) AS legacy_usage_request_keys, sum(l.ledger_rows) AS legacy_usage_rows,
	       count(*) FILTER (WHERE e.received_at IS NULL) AS legacy_usage_without_retained_receipt,
	       count(*) FILTER (WHERE timezone($4, e.received_at)::date <> l.local_day) AS legacy_usage_receipt_other_day,
	       count(*) FILTER (WHERE e.occurred_at IS NULL) AS legacy_usage_without_retained_terminal_usage
	FROM legacy_usage_keys l JOIN evidence e USING (tenant_id, request_id)
	GROUP BY l.local_day
), by_posting AS (
	SELECT timezone($4, p.posted_at)::date AS local_day,
	       count(*) AS posted_entries,
	       count(*) FILTER (WHERE e.received_at IS NULL) AS postings_without_retained_receipt,
	       count(*) FILTER (WHERE NOT e.receipt_in_window) AS postings_receipt_outside_window
	FROM postings p JOIN evidence e USING (tenant_id, request_id) GROUP BY 1
), by_send AS (
	SELECT timezone($4, sent_at)::date AS local_day,
	       count(*) AS recorded_sends,
	       count(*) FILTER (WHERE estimated_amount IS NULL) AS recorded_sends_without_estimate,
	       count(*) FILTER (WHERE estimated_amount IS NULL OR rate_snapshot #>> '{supplier_contract,rate_id}' IS NULL) AS recorded_sends_without_contract_estimate,
	       count(*) FILTER (WHERE reported_amount IS NULL) AS recorded_sends_without_reported_amount,
	       count(*) FILTER (WHERE reported_amount IS NOT NULL AND reported_currency IS NULL) AS recorded_sends_reported_amount_without_currency,
	       count(*) FILTER (WHERE provider_request_id IS NULL) AS recorded_sends_without_provider_request_id,
	       count(*) FILTER (WHERE is_media) AS recorded_media_sends,
	       count(*) FILTER (WHERE is_media AND media_usage_source IS NULL) AS recorded_media_sends_without_quantity
	FROM (
		SELECT sent_at, estimated_amount, rate_snapshot, reported_amount, reported_currency, provider_request_id, media_usage_source,
		       source ~ '(^|:)(audio_transcription_|audio_speech_|images_generations$|videos_generations$)' AS is_media
		FROM public.provider_call_attempts
		WHERE delivery = 'sent' AND sent_at >= $1 AND sent_at < $2 AND ($3 = '' OR tenant_id = $3)
	) calls
	GROUP BY 1
)
SELECT local_day,
	COALESCE(received_requests, 0) AS received_requests,
	COALESCE(receipts_without_retained_terminal_usage, 0) AS receipts_without_retained_terminal_usage,
	COALESCE(orchestration_child_receipts, 0) AS orchestration_child_receipts,
	COALESCE(orchestration_dispatches_registered, 0) AS orchestration_dispatches_registered,
	COALESCE(orchestration_dispatches_unlinked, 0) AS orchestration_dispatches_unlinked,
	COALESCE(terminal_usage_requests, 0) AS terminal_usage_requests,
	COALESCE(terminal_usage_without_retained_receipt, 0) AS terminal_usage_without_retained_receipt,
	COALESCE(terminal_usage_receipt_outside_window, 0) AS terminal_usage_receipt_outside_window,
	COALESCE(legacy_usage_request_keys, 0) AS legacy_usage_request_keys,
	COALESCE(legacy_usage_rows, 0) AS legacy_usage_rows,
	COALESCE(legacy_usage_without_retained_receipt, 0) AS legacy_usage_without_retained_receipt,
	COALESCE(legacy_usage_receipt_other_day, 0) AS legacy_usage_receipt_other_day,
	COALESCE(legacy_usage_without_retained_terminal_usage, 0) AS legacy_usage_without_retained_terminal_usage,
	COALESCE(posted_entries, 0) AS posted_entries,
	COALESCE(postings_without_retained_receipt, 0) AS postings_without_retained_receipt,
	COALESCE(postings_receipt_outside_window, 0) AS postings_receipt_outside_window,
	COALESCE(recorded_sends, 0) AS recorded_sends,
	COALESCE(recorded_sends_without_estimate, 0) AS recorded_sends_without_estimate,
	COALESCE(recorded_sends_without_contract_estimate, 0) AS recorded_sends_without_contract_estimate,
	COALESCE(recorded_media_sends, 0) AS recorded_media_sends,
	COALESCE(recorded_media_sends_without_quantity, 0) AS recorded_media_sends_without_quantity,
	COALESCE(recorded_sends_without_reported_amount, 0) AS recorded_sends_without_reported_amount,
	COALESCE(recorded_sends_reported_amount_without_currency, 0) AS recorded_sends_reported_amount_without_currency,
	COALESCE(recorded_sends_without_provider_request_id, 0) AS recorded_sends_without_provider_request_id
FROM by_receipt FULL JOIN by_dispatch USING (local_day) FULL JOIN by_terminal_usage USING (local_day)
FULL JOIN by_posting USING (local_day) FULL JOIN by_send USING (local_day) FULL JOIN by_legacy_usage USING (local_day)`

const coverageTotals = `SELECT
	COALESCE(sum(received_requests), 0) AS received_requests,
	COALESCE(sum(receipts_without_retained_terminal_usage), 0) AS receipts_without_retained_terminal_usage,
	COALESCE(sum(orchestration_child_receipts), 0) AS orchestration_child_receipts,
	COALESCE(sum(orchestration_dispatches_registered), 0) AS orchestration_dispatches_registered,
	COALESCE(sum(orchestration_dispatches_unlinked), 0) AS orchestration_dispatches_unlinked,
	COALESCE(sum(terminal_usage_requests), 0) AS terminal_usage_requests,
	COALESCE(sum(terminal_usage_without_retained_receipt), 0) AS terminal_usage_without_retained_receipt,
	COALESCE(sum(terminal_usage_receipt_outside_window), 0) AS terminal_usage_receipt_outside_window,
	COALESCE(sum(legacy_usage_request_keys), 0) AS legacy_usage_request_keys,
	COALESCE(sum(legacy_usage_rows), 0) AS legacy_usage_rows,
	COALESCE(sum(legacy_usage_without_retained_receipt), 0) AS legacy_usage_without_retained_receipt,
	COALESCE(sum(legacy_usage_receipt_other_day), 0) AS legacy_usage_receipt_other_day,
	COALESCE(sum(legacy_usage_without_retained_terminal_usage), 0) AS legacy_usage_without_retained_terminal_usage,
	COALESCE(sum(posted_entries), 0) AS posted_entries,
	COALESCE(sum(postings_without_retained_receipt), 0) AS postings_without_retained_receipt,
	COALESCE(sum(postings_receipt_outside_window), 0) AS postings_receipt_outside_window,
	COALESCE(sum(recorded_sends), 0) AS recorded_sends,
	COALESCE(sum(recorded_sends_without_estimate), 0) AS recorded_sends_without_estimate,
	COALESCE(sum(recorded_sends_without_contract_estimate), 0) AS recorded_sends_without_contract_estimate,
	COALESCE(sum(recorded_media_sends), 0) AS recorded_media_sends,
	COALESCE(sum(recorded_media_sends_without_quantity), 0) AS recorded_media_sends_without_quantity,
	COALESCE(sum(recorded_sends_without_reported_amount), 0) AS recorded_sends_without_reported_amount,
	COALESCE(sum(recorded_sends_reported_amount_without_currency), 0) AS recorded_sends_reported_amount_without_currency,
	COALESCE(sum(recorded_sends_without_provider_request_id), 0) AS recorded_sends_without_provider_request_id,
	min(local_day) FILTER (WHERE received_requests > 0) AS first_observed_receipt_day,
	max(local_day) FILTER (WHERE received_requests > 0) AS last_observed_receipt_day,
	min(local_day) FILTER (WHERE legacy_usage_request_keys > 0) AS first_observed_legacy_usage_day,
	max(local_day) FILTER (WHERE legacy_usage_request_keys > 0) AS last_observed_legacy_usage_day,
	min(local_day) FILTER (WHERE recorded_sends > 0) AS first_observed_send_day,
	max(local_day) FILTER (WHERE recorded_sends > 0) AS last_observed_send_day,
	round(sum(terminal_usage_requests - terminal_usage_without_retained_receipt) / NULLIF(sum(terminal_usage_requests), 0), 6)
		AS terminal_usage_with_retained_receipt_fraction,
	round(sum(recorded_sends - recorded_sends_without_estimate) / NULLIF(sum(recorded_sends), 0), 6)
		AS recorded_sends_with_estimate_fraction,
	round(sum(recorded_sends - recorded_sends_without_reported_amount - recorded_sends_reported_amount_without_currency)
		/ NULLIF(sum(recorded_sends), 0), 6) AS recorded_sends_with_reported_amount_and_currency_fraction
FROM facts`
