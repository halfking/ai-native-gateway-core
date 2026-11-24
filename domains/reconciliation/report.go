// Package reconciliation reads gateway requests, supplier attempts and credit
// postings on their respective clocks.
package reconciliation

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const ReportVersion = "request-reconciliation-v1"

// ReportScope is assigned by the server from the authenticated caller.
// A tenant scope can read requests, sessions and internal postings.
type ReportScope struct {
	TenantID string
	Platform bool
}

type ReportQuery struct {
	Start, End time.Time
	Day        string
	Month      string
	Timezone   string
	Section    string
	Limit      int
	Offset     int
}

type Report struct {
	Version       string            `json:"version"`
	Section       string            `json:"section"`
	Start         time.Time         `json:"start"`
	End           time.Time         `json:"end"`
	Timezone      string            `json:"timezone"`
	Day           string            `json:"day,omitempty"`
	Month         string            `json:"month,omitempty"`
	TenantID      string            `json:"tenant_id,omitempty"`
	PlatformScope bool              `json:"platform_scope"`
	ReadAt        time.Time         `json:"read_at"`
	Coverage      string            `json:"coverage"`
	Limit         int               `json:"limit"`
	Offset        int               `json:"offset"`
	Summary       json.RawMessage   `json:"summary"`
	Rows          []json.RawMessage `json:"rows"`
}

type reportSection struct {
	base, totals, order, coverage string
	columns                       []string
	platformOnly                  bool
	calendarPeriodOnly            bool
}

var reportSections = map[string]reportSection{
	"requests": {
		base: requestFacts + ` SELECT CASE WHEN unassigned_admission IS NOT TRUE THEN tenant_id END AS tenant_id,
			request_id, received_at, local_day, status, traffic_class,
			session_id, client_id, application_id, input_tokens, output_tokens, usage_source, admission_rejection,
			CASE WHEN current_setting('app.bypass_rls', true) = 'true' THEN a.sent END AS recorded_supplier_sends
		FROM request_facts r LEFT JOIN LATERAL (
			SELECT count(*) AS sent FROM (
				SELECT request_id FROM public.provider_call_attempts
				WHERE delivery = 'sent' AND tenant_id = r.tenant_id AND request_id = r.request_id
				UNION ALL
				SELECT a.request_id FROM public.request_reconciliation_links l
				JOIN public.provider_call_attempts a ON a.tenant_id = l.tenant_id AND a.request_id = l.child_request_id
				WHERE l.tenant_id = r.tenant_id AND l.parent_request_id = r.request_id AND a.delivery = 'sent'
			) sent_attempts WHERE current_setting('app.bypass_rls', true) = 'true'
		) a ON true`,
		totals: `SELECT count(*) AS requests,
			count(*) FILTER (WHERE status IN ('success','failure','rate_limited','cancelled')) AS terminal,
			count(*) FILTER (WHERE status = 'success') AS success,
			count(*) FILTER (WHERE status = 'failure') AS failure,
			count(*) FILTER (WHERE status = 'rate_limited') AS rate_limited,
			count(*) FILTER (WHERE status = 'cancelled') AS cancelled,
			count(*) FILTER (WHERE status = 'pending') AS pending,
			count(*) FILTER (WHERE status = 'unknown') AS unknown_status,
			count(admission_rejection) AS admission_rejected_requests,
			count(*) FILTER (WHERE traffic_class = 'business') AS business_requests,
			count(*) FILTER (WHERE traffic_class = 'platform') AS platform_requests,
			count(*) FILTER (WHERE traffic_class = 'unknown') AS unassigned_requests,
			count(*) FILTER (WHERE session_id IS NULL) AS unassigned_session,
			CASE WHEN current_setting('app.bypass_rls', true) = 'true' THEN
				count(*) FILTER (WHERE recorded_supplier_sends = 0) END AS requests_with_no_recorded_sends,
			CASE WHEN current_setting('app.bypass_rls', true) = 'true' THEN
				count(*) FILTER (WHERE recorded_supplier_sends > 1) END AS requests_with_multiple_recorded_sends,
			count(input_tokens) AS measured_input_requests, count(output_tokens) AS measured_output_requests,
			count(*) FILTER (WHERE usage_source = 'estimated') AS estimated_usage_requests,
			count(*) FILTER (WHERE usage_source IS NULL OR usage_source = '') AS requests_without_usage_evidence,
			count(*) FILTER (WHERE input_tokens IS NOT NULL AND output_tokens IS NOT NULL) AS complete_measured_requests,
			count(*) FILTER (WHERE (input_tokens IS NULL) <> (output_tokens IS NULL)) AS partial_measured_requests,
			count(*) FILTER (WHERE input_tokens IS NULL AND output_tokens IS NULL) AS requests_without_measured_tokens,
			sum(input_tokens) AS input_tokens, sum(output_tokens) AS output_tokens,
			sum(input_tokens + output_tokens) AS total_tokens FROM facts`,
		order: `received_at, tenant_id, request_id`, coverage: "recorded_wal_and_linked_terminal_usage_legacy_zeros_unverified",
		columns: []string{"tenant_id", "request_id", "received_at", "local_day", "status", "traffic_class", "session_id", "client_id", "application_id", "input_tokens", "output_tokens", "usage_source", "recorded_supplier_sends", "admission_rejection"},
	},
	"attempts": {
		platformOnly: true,
		base: `SELECT attempt_id, a.tenant_id, request_id, l.parent_request_id, job_id, traffic_class, client_id, source, attempt_no,
			provider_id, credential_id, model, prepared_at, sent_at, finished_at, timezone($4, COALESCE(sent_at, prepared_at))::date AS local_day,
			delivery, outcome, http_status, provider_request_id, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
			audio_input_duration_us, audio_billed_duration_us, audio_output_duration_us, generated_images, video_output_duration_us, media_usage_source,
			usage_source, estimated_amount::text, estimated_currency, rate_snapshot,
			CASE WHEN rate_snapshot #>> '{supplier_contract,rate_id}' IS NOT NULL
			     THEN CASE WHEN estimated_amount IS NULL THEN 'contract_missing_usage' ELSE 'supplier_contract' END
			     WHEN estimated_amount IS NOT NULL THEN 'legacy_unverified' END AS estimate_basis,
			rate_snapshot #>> '{supplier_contract,reference}' AS supplier_contract_reference,
			reported_amount::text, reported_currency, reported_semantics
			FROM public.provider_call_attempts a
			LEFT JOIN public.request_reconciliation_links l ON l.tenant_id = a.tenant_id AND l.child_request_id = a.request_id
			WHERE ($3 = '' OR a.tenant_id = $3)
			  AND ((sent_at >= $1 AND sent_at < $2)
			    OR (sent_at IS NULL AND prepared_at >= $1 AND prepared_at < $2))`,
		totals: `SELECT count(*) AS recorded_attempts,
			count(*) FILTER (WHERE delivery = 'sent') AS sent,
			count(*) FILTER (WHERE delivery = 'sent' AND traffic_class = 'business') AS business_sent,
			count(*) FILTER (WHERE delivery = 'sent' AND traffic_class = 'platform') AS platform_sent,
			count(*) FILTER (WHERE delivery = 'sent' AND traffic_class = 'unknown') AS unassigned_sent,
			count(*) FILTER (WHERE delivery = 'sent' AND outcome IN ('success','failure','cancelled')) AS terminal_sent,
			count(*) FILTER (WHERE delivery = 'sent' AND outcome = 'success') AS successful_sent,
			count(*) FILTER (WHERE delivery = 'prepared') AS prepared,
			count(*) FILTER (WHERE delivery = 'unknown') AS unknown_delivery,
			count(*) FILTER (WHERE delivery = 'not_sent') AS not_sent,
			count(input_tokens) FILTER (WHERE delivery = 'sent') AS measured_input_attempts,
			sum(input_tokens) FILTER (WHERE delivery = 'sent') AS input_tokens,
			sum(output_tokens) FILTER (WHERE delivery = 'sent') AS output_tokens,
			sum(audio_input_duration_us) FILTER (WHERE delivery = 'sent') AS measured_audio_input_duration_us,
			sum(audio_billed_duration_us) FILTER (WHERE delivery = 'sent') AS measured_audio_billed_duration_us,
			sum(audio_output_duration_us) FILTER (WHERE delivery = 'sent') AS measured_audio_output_duration_us,
			sum(generated_images) FILTER (WHERE delivery = 'sent') AS measured_generated_images,
			sum(video_output_duration_us) FILTER (WHERE delivery = 'sent') AS measured_video_output_duration_us,
			sum(cache_read_tokens) FILTER (WHERE delivery = 'sent' AND cache_read_tokens <= input_tokens) AS comparable_cache_read_tokens,
			sum(input_tokens) FILTER (WHERE delivery = 'sent' AND cache_read_tokens <= input_tokens) AS comparable_cache_input_tokens,
			count(estimated_amount) FILTER (WHERE delivery = 'sent') AS attempts_with_estimate,
			count(*) FILTER (WHERE delivery = 'sent' AND estimate_basis = 'supplier_contract') AS attempts_with_contract_estimate,
			count(reported_amount) FILTER (WHERE delivery = 'sent') AS attempts_with_reported_amount,
			count(reported_amount) FILTER (WHERE delivery = 'sent' AND reported_currency IS NULL) AS reported_unknown_currency,
			COALESCE((SELECT jsonb_agg(to_jsonb(v) ORDER BY traffic_class, currency, estimate_basis)
			  FROM (SELECT traffic_class, estimated_currency AS currency, estimate_basis, count(*) AS attempts, sum(estimated_amount::numeric)::text AS amount
			    FROM facts WHERE delivery = 'sent' AND estimated_amount IS NOT NULL GROUP BY traffic_class, estimated_currency, estimate_basis) v), '[]'::jsonb) AS estimates_by_traffic_and_currency,
			COALESCE((SELECT jsonb_agg(to_jsonb(v) ORDER BY traffic_class, currency, semantics)
			  FROM (SELECT traffic_class, reported_currency AS currency, reported_semantics AS semantics, count(*) AS attempts, sum(reported_amount::numeric)::text AS amount
			    FROM facts WHERE delivery = 'sent' AND reported_amount IS NOT NULL AND reported_currency IS NOT NULL
			    GROUP BY traffic_class, reported_currency, reported_semantics) v), '[]'::jsonb) AS reported_by_traffic_currency_and_semantics
			FROM facts`,
		order: `COALESCE(sent_at, prepared_at), attempt_id`, coverage: "captured_attempts_only_historical_coverage_unverified",
		columns: []string{"attempt_id", "tenant_id", "request_id", "parent_request_id", "job_id", "traffic_class", "client_id", "source", "attempt_no", "provider_id", "credential_id", "model", "prepared_at", "sent_at", "finished_at", "local_day", "delivery", "outcome", "http_status", "provider_request_id", "input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens", "usage_source", "estimated_amount", "estimated_currency", "rate_snapshot", "reported_amount", "reported_currency", "reported_semantics", "estimate_basis", "supplier_contract_reference",
			"audio_input_duration_us", "audio_billed_duration_us", "audio_output_duration_us", "generated_images", "video_output_duration_us", "media_usage_source"},
	},
	"postings": {
		base: `SELECT p.tenant_id, posting_id, request_id, l.parent_request_id, posted_at, timezone($4, posted_at)::date AS local_day,
			entry_type, pool, owner_user, net_credits,
			NULL::text AS internal_amount, NULL::text AS internal_currency
			FROM (` + requestPostings + `) p
			LEFT JOIN public.request_reconciliation_links l ON l.tenant_id = p.tenant_id AND l.child_request_id = p.request_id`,
		totals: `SELECT count(*) AS postings, count(DISTINCT (tenant_id, request_id)) AS linked_requests,
			count(DISTINCT (tenant_id, COALESCE(parent_request_id, request_id))) AS linked_client_requests,
			sum(net_credits) AS net_credits, NULL::text AS internal_amount, NULL::text AS internal_currency FROM facts`,
		order: `posted_at, tenant_id, posting_id`, coverage: "request_referenced_credit_postings_currency_unavailable",
		columns: []string{"tenant_id", "posting_id", "request_id", "parent_request_id", "posted_at", "local_day", "entry_type", "pool", "owner_user", "net_credits", "internal_amount", "internal_currency"},
	},
	"sessions": {
		base: requestFacts + `, active AS (
			SELECT tenant_id, session_id, count(*) AS window_requests
			FROM request_facts WHERE session_id IS NOT NULL GROUP BY tenant_id, session_id
		)
		SELECT s.tenant_id, s.gw_session_id AS session_id,
			COALESCE(a.window_requests, 0) AS window_requests,
			(a.window_requests IS NOT NULL) AS active,
			COALESCE(s.first_request_at >= $1 AND s.first_request_at < $2, false) AS new,
			COALESCE(s.closed_at >= $1 AND s.closed_at < $2, false) AS closed,
			s.first_request_at, timezone($4, s.first_request_at)::date AS first_request_day,
			s.closed_at, timezone($4, s.closed_at)::date AS closed_day
		FROM public.session_dim s LEFT JOIN active a ON a.tenant_id = s.tenant_id AND a.session_id = s.gw_session_id
		WHERE ($3 = '' OR s.tenant_id = $3)
		  AND (a.window_requests IS NOT NULL OR (
		    ((s.first_request_at >= $1 AND s.first_request_at < $2) OR (s.closed_at >= $1 AND s.closed_at < $2))
		    AND NOT COALESCE((
		      SELECT bool_and(l.child_request_id IS NOT NULL)
		      FROM public.request_wal_with_current_month w
		      LEFT JOIN public.request_reconciliation_links l ON l.tenant_id = w.tenant_id AND l.child_request_id = w.request_id
		      WHERE w.tenant_id = s.tenant_id AND w.gw_session_id = s.gw_session_id
		    ), false)))`,
		totals: `SELECT count(*) AS sessions, count(*) FILTER (WHERE active) AS active,
			count(*) FILTER (WHERE new) AS new, count(*) FILTER (WHERE closed) AS closed FROM facts`,
		order: `tenant_id, session_id`, coverage: "canonical_session_dimension_and_recorded_activity",
		columns: []string{"tenant_id", "session_id", "window_requests", "active", "new", "closed", "first_request_at", "first_request_day", "closed_at", "closed_day"},
	},
	"coverage": {
		base: coverageFacts, totals: coverageTotals, order: "local_day", platformOnly: true, calendarPeriodOnly: true,
		coverage: "retained_evidence_only_historical_supplier_population_unknown",
		columns:  coverageColumns,
	},
	"internal": {
		base: internalFacts, totals: internalTotals, order: "tenant_id", calendarPeriodOnly: true,
		coverage: "retained_request_postings_hourly_buckets_and_stored_monthly_bills",
		columns:  internalColumns,
	},
}

const requestPostings = `SELECT DISTINCT ON (tenant_id, id) tenant_id, id AS posting_id, ref_id AS request_id,
	created_at AS posted_at, entry_type, pool, owner_user, -amount::numeric AS net_credits
	FROM public.credit_ledger_with_current_month
	WHERE ref_type = 'request' AND ref_id IS NOT NULL
	  AND entry_type IN ('consume', 'refund', 'reversal', 'adjust', 'adjustment')
	  AND created_at >= $1 AND created_at < $2 AND ($3 = '' OR tenant_id = $3)
	ORDER BY tenant_id, id, created_at DESC`

// Both the details and totals read from facts inside one database statement.
// Terminal-only evidence is joined by tenant and server request ID. A WAL
// session ID is provisional until it matches a canonical dimension row.
const requestFacts = `WITH candidates AS (
	SELECT DISTINCT tenant_id, request_id
	FROM public.request_wal_with_current_month
	WHERE created_at >= $1 AND created_at < $2 AND ($3 = '' OR tenant_id = $3)
), wal AS (
	SELECT DISTINCT ON (w.tenant_id, w.request_id) w.tenant_id, w.request_id, w.gw_session_id, w.status, w.error,
		min(w.created_at) OVER (PARTITION BY w.tenant_id, w.request_id) AS received_at
	FROM candidates c JOIN public.request_wal_with_current_month w ON w.tenant_id = c.tenant_id AND w.request_id = c.request_id
	ORDER BY w.tenant_id, w.request_id, w.stage DESC, w.completed_at DESC NULLS LAST, w.created_at DESC
), classified_requests AS (
	SELECT w.tenant_id, w.request_id, w.received_at, timezone($4, w.received_at)::date AS local_day,
		CASE WHEN w.status IN ('rate_limited', 'cancelled') THEN w.status
		     WHEN f.status IN ('rate_limited', 'cancelled') THEN f.status
		     WHEN w.error = 'client_disconnect: context canceled' THEN 'cancelled'
		     WHEN w.status IN ('success','failure','unknown') THEN w.status
		     WHEN f.status IN ('success','failure') THEN f.status
		     WHEN w.status = 'pending' THEN 'pending' ELSE 'unknown' END AS status,
		s.gw_session_id AS session_id,
		CASE WHEN starts_with(w.error, 'gateway_admission:platform:') THEN 'platform'
		     WHEN starts_with(w.error, 'gateway_admission:tenant:') THEN 'business'
		     WHEN k.is_system AND (f.traffic_class IN ('probe','self_check','system_health','admin_test')
		       OR (c.source_channel = 'reconciliation_modality' AND c.origin_stage IN
		           ('node_probe','probe_direct','probe_v2','model_probe','passive_probe','self_check','system_health','manual'))) THEN 'platform'
		     WHEN k.is_system = false THEN 'business' ELSE 'unknown' END AS traffic_class,
		COALESCE(NULLIF(c.virtual_client_id, ''), s.client_id) AS observed_client_id,
		c.application_id,
		CASE WHEN f.usage_source = 'llm' THEN NULLIF(f.prompt_tokens, 0) END AS input_tokens,
		CASE WHEN f.usage_source = 'llm' THEN NULLIF(f.completion_tokens, 0) END AS output_tokens,
		CASE WHEN starts_with(w.error, 'gateway_admission:') THEN split_part(w.error, ':', 3) END AS admission_rejection,
		starts_with(w.error, 'gateway_admission:unassigned:') AS unassigned_admission,
		f.usage_source
	FROM wal w
	LEFT JOIN public.request_reconciliation_links l ON l.tenant_id = w.tenant_id AND l.child_request_id = w.request_id
	LEFT JOIN public.request_context_attrs c ON c.request_id = w.request_id AND c.tenant_id = w.tenant_id
	LEFT JOIN public.session_dim s ON s.tenant_id = w.tenant_id AND s.gw_session_id = COALESCE(NULLIF(c.gw_session_id,''), w.gw_session_id)
	LEFT JOIN LATERAL (
		SELECT status, prompt_tokens, completion_tokens, usage_source, api_key_id, traffic_class FROM public.usage_facts
		WHERE request_id = w.request_id AND tenant_id = w.tenant_id
		ORDER BY revision DESC, finalized_at DESC, fact_id DESC LIMIT 1
	) f ON true
	LEFT JOIN public.api_keys k ON k.id = COALESCE(c.api_key_id, f.api_key_id) AND k.tenant_id = w.tenant_id
	WHERE w.received_at >= $1 AND w.received_at < $2 AND l.child_request_id IS NULL
	  AND (starts_with(w.error, 'gateway_admission:unassigned:') IS NOT TRUE
	       OR ($3 = '' AND current_setting('app.bypass_rls', true) = 'true'))
), request_facts AS (
	SELECT *, CASE WHEN traffic_class = 'platform' THEN 'platform-automation' ELSE observed_client_id END AS client_id
	FROM classified_requests WHERE traffic_class <> 'platform' OR current_setting('app.bypass_rls', true) = 'true'
)`

// ReportColumns is the common JSON/CSV detail column contract.
func ReportColumns(section string) []string {
	return append([]string(nil), reportSections[section].columns...)
}

// ReportRequiresPlatformAccess identifies report sections reserved for operators.
func ReportRequiresPlatformAccess(section string) bool {
	return reportSections[section].platformOnly
}

// ReportRequiresCalendarPeriod identifies calendar-bound reconciliation sections.
func ReportRequiresCalendarPeriod(section string) bool {
	return reportSections[section].calendarPeriodOnly
}

func ReadReport(ctx context.Context, pool *pgxpool.Pool, scope ReportScope, query ReportQuery) (Report, error) {
	section, ok := reportSections[query.Section]
	if !ok || (!scope.Platform && (scope.TenantID == "" || section.platformOnly)) {
		return Report{}, fmt.Errorf("report section unavailable in caller scope")
	}
	if query.Section == "internal" && scope.TenantID == "" {
		return Report{}, fmt.Errorf("internal reconciliation requires a tenant")
	}
	if query.Limit < 0 || query.Offset < 0 {
		return Report{}, fmt.Errorf("report limit and offset must be non-negative")
	}
	if query.Day != "" && query.Month != "" {
		return Report{}, fmt.Errorf("day and month are mutually exclusive")
	}
	if section.calendarPeriodOnly || query.Day != "" || query.Month != "" {
		period := query.Month
		bounds := NaturalMonth
		if query.Day != "" {
			period, bounds = query.Day, NaturalDay
		}
		start, end, err := bounds(period, query.Timezone)
		if err != nil || !start.Equal(query.Start) || !end.Equal(query.End) {
			return Report{}, fmt.Errorf("%s requires matching calendar period boundaries", query.Section)
		}
	}
	// Keep RLS settings transaction-local. The SQL tenant predicate remains in
	// each source even on connections whose role owns the underlying tables.
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return Report{}, err
	}
	defer tx.Rollback(ctx)
	if err := setReportScope(ctx, tx, scope); err != nil {
		return Report{}, err
	}
	result, err := readReportSection(ctx, tx, scope, query, section)
	if err != nil {
		return Report{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Report{}, err
	}
	return result, nil
}

func setReportScope(ctx context.Context, tx pgx.Tx, scope ReportScope) error {
	role, bypass := "tenant_admin", "false"
	if scope.Platform {
		role, bypass = "super_admin", "true"
	}
	_, err := tx.Exec(ctx, `SELECT set_config('app.current_tenant',$1,true), set_config('app.current_role',$2,true),
		set_config('app.bypass_rls',$3,true), set_config('TimeZone','UTC',true)`, scope.TenantID, role, bypass)
	return err
}

func readReportSection(ctx context.Context, tx pgx.Tx, scope ReportScope, query ReportQuery, section reportSection) (Report, error) {
	result := Report{
		Version: ReportVersion, Section: query.Section, Start: query.Start.UTC(), End: query.End.UTC(),
		Timezone: query.Timezone, Day: query.Day, Month: query.Month, TenantID: scope.TenantID, PlatformScope: scope.Platform,
		Coverage: section.coverage, Limit: query.Limit, Offset: query.Offset,
	}
	stmt := `WITH facts AS (` + section.base + `), totals AS (` + section.totals + `), page AS (
		SELECT facts.*, row_number() OVER (ORDER BY ` + section.order + `) AS row_no
		FROM facts ORDER BY ` + section.order + ` LIMIT NULLIF($5::bigint, -1) OFFSET $6
	)
	SELECT statement_timestamp(), to_jsonb(totals),
		COALESCE((SELECT jsonb_agg(to_jsonb(page) - 'row_no' ORDER BY row_no) FROM page), '[]'::jsonb)
	FROM totals`
	args := []any{result.Start, result.End, scope.TenantID, query.Timezone, query.Limit, query.Offset}
	if query.Section == "internal" {
		args = append(args, query.Month != "")
	}
	if err := tx.QueryRow(ctx, stmt, args...).
		Scan(&result.ReadAt, &result.Summary, &result.Rows); err != nil {
		return Report{}, err
	}
	result.ReadAt = result.ReadAt.UTC()
	return result, nil
}
