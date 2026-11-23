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
}

var reportSections = map[string]reportSection{
	"requests": {
		base: requestFacts + ` SELECT tenant_id, request_id, received_at, local_day, status,
			session_id, client_id, application_id, input_tokens, output_tokens, usage_source FROM request_facts`,
		totals: `SELECT count(*) AS requests,
			count(*) FILTER (WHERE status IN ('success','failure','rate_limited','cancelled')) AS terminal,
			count(*) FILTER (WHERE status = 'success') AS success,
			count(*) FILTER (WHERE status = 'failure') AS failure,
			count(*) FILTER (WHERE status = 'rate_limited') AS rate_limited,
			count(*) FILTER (WHERE status = 'cancelled') AS cancelled,
			count(*) FILTER (WHERE status = 'pending') AS pending,
			count(*) FILTER (WHERE status = 'unknown') AS unknown_status,
			count(*) FILTER (WHERE session_id IS NULL) AS unassigned_session,
			count(input_tokens) AS measured_input_requests, count(output_tokens) AS measured_output_requests,
			sum(input_tokens) AS input_tokens, sum(output_tokens) AS output_tokens,
			sum(input_tokens + output_tokens) AS total_tokens FROM facts`,
		order: `received_at, tenant_id, request_id`, coverage: "recorded_wal_and_linked_terminal_usage",
		columns: []string{"tenant_id", "request_id", "received_at", "local_day", "status", "session_id", "client_id", "application_id", "input_tokens", "output_tokens", "usage_source"},
	},
	"attempts": {
		base: `SELECT attempt_id, tenant_id, request_id, job_id, traffic_class, client_id, source, attempt_no,
			provider_id, model, prepared_at, sent_at, finished_at, timezone($4, COALESCE(sent_at, prepared_at))::date AS local_day,
			delivery, outcome, http_status, provider_request_id, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
			usage_source, estimated_amount::text, estimated_currency, rate_snapshot,
			reported_amount::text, reported_currency, reported_semantics
			FROM public.provider_call_attempts
			WHERE (($3 = '') OR tenant_id = $3)
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
			sum(cache_read_tokens) FILTER (WHERE delivery = 'sent' AND cache_read_tokens <= input_tokens) AS comparable_cache_read_tokens,
			sum(input_tokens) FILTER (WHERE delivery = 'sent' AND cache_read_tokens <= input_tokens) AS comparable_cache_input_tokens,
			count(estimated_amount) FILTER (WHERE delivery = 'sent') AS attempts_with_estimate,
			count(reported_amount) FILTER (WHERE delivery = 'sent') AS attempts_with_reported_amount,
			count(reported_amount) FILTER (WHERE delivery = 'sent' AND reported_currency IS NULL) AS reported_unknown_currency,
			COALESCE((SELECT jsonb_object_agg(estimated_currency, amount)
			  FROM (SELECT estimated_currency, sum(estimated_amount::numeric)::text AS amount
			    FROM facts WHERE delivery = 'sent' AND estimated_amount IS NOT NULL GROUP BY estimated_currency) v), '{}'::jsonb) AS estimates_by_currency,
			COALESCE((SELECT jsonb_agg(to_jsonb(v) ORDER BY currency, semantics)
			  FROM (SELECT reported_currency AS currency, reported_semantics AS semantics, sum(reported_amount::numeric)::text AS amount
			    FROM facts WHERE delivery = 'sent' AND reported_amount IS NOT NULL AND reported_currency IS NOT NULL
			    GROUP BY reported_currency, reported_semantics) v), '[]'::jsonb) AS reported_by_currency_and_semantics
			FROM facts`,
		order: `COALESCE(sent_at, prepared_at), attempt_id`, coverage: "captured_attempts_only_historical_coverage_unverified",
		columns: []string{"attempt_id", "tenant_id", "request_id", "job_id", "traffic_class", "client_id", "source", "attempt_no", "provider_id", "model", "prepared_at", "sent_at", "finished_at", "local_day", "delivery", "outcome", "http_status", "provider_request_id", "input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens", "usage_source", "estimated_amount", "estimated_currency", "rate_snapshot", "reported_amount", "reported_currency", "reported_semantics"},
	},
	"postings": {
		base: `SELECT DISTINCT ON (tenant_id, id) tenant_id, id AS posting_id, ref_id AS request_id,
			created_at AS posted_at, timezone($4, created_at)::date AS local_day, entry_type, pool,
			owner_user, -amount::numeric AS net_credits,
			NULL::text AS internal_amount, NULL::text AS internal_currency
			FROM public.credit_ledger_with_current_month
			WHERE ref_type = 'request' AND ref_id IS NOT NULL
			  AND entry_type IN ('consume', 'refund', 'reversal', 'adjustment')
			  AND created_at >= $1 AND created_at < $2 AND ($3 = '' OR tenant_id = $3)
			ORDER BY tenant_id, id, created_at DESC`,
		totals: `SELECT count(*) AS postings, count(DISTINCT (tenant_id, request_id)) AS linked_requests,
			sum(net_credits) AS net_credits, NULL::text AS internal_amount, NULL::text AS internal_currency FROM facts`,
		order: `posted_at, tenant_id, posting_id`, coverage: "request_referenced_credit_postings_currency_unavailable",
		columns: []string{"tenant_id", "posting_id", "request_id", "posted_at", "local_day", "entry_type", "pool", "owner_user", "net_credits", "internal_amount", "internal_currency"},
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
			s.first_request_at, s.closed_at
		FROM public.session_dim s LEFT JOIN active a ON a.tenant_id = s.tenant_id AND a.session_id = s.gw_session_id
		WHERE ($3 = '' OR s.tenant_id = $3) AND $4::text IS NOT NULL
		  AND (a.window_requests IS NOT NULL OR (s.first_request_at >= $1 AND s.first_request_at < $2)
		    OR (s.closed_at >= $1 AND s.closed_at < $2))`,
		totals: `SELECT count(*) AS sessions, count(*) FILTER (WHERE active) AS active,
			count(*) FILTER (WHERE new) AS new, count(*) FILTER (WHERE closed) AS closed FROM facts`,
		order: `tenant_id, session_id`, coverage: "canonical_session_dimension_and_recorded_activity",
		columns: []string{"tenant_id", "session_id", "window_requests", "active", "new", "closed", "first_request_at", "closed_at"},
	},
}

// Both the details and totals read from facts inside one database statement.
// Terminal-only evidence is joined by tenant and server request ID. A WAL
// session ID is provisional until it matches a canonical dimension row.
const requestFacts = `WITH wal AS (
	SELECT DISTINCT ON (tenant_id, request_id) tenant_id, request_id, gw_session_id, status,
		min(created_at) OVER (PARTITION BY tenant_id, request_id) AS received_at
	FROM public.request_wal_with_current_month
	WHERE created_at >= $1 AND created_at < $2 AND ($3 = '' OR tenant_id = $3)
	ORDER BY tenant_id, request_id, stage DESC, completed_at DESC NULLS LAST, created_at DESC
), request_facts AS (
	SELECT w.tenant_id, w.request_id, w.received_at, timezone($4, w.received_at)::date AS local_day,
		CASE WHEN f.status IN ('rate_limited', 'cancelled') THEN f.status
		     WHEN w.status IN ('success','failure') THEN w.status
		     WHEN f.status IN ('success','failure') THEN f.status
		     WHEN w.status = 'pending' THEN 'pending' ELSE 'unknown' END AS status,
		s.gw_session_id AS session_id, COALESCE(NULLIF(c.virtual_client_id, ''), s.client_id) AS client_id,
		c.application_id,
			CASE WHEN f.usage_source = 'llm' THEN f.prompt_tokens END AS input_tokens,
			CASE WHEN f.usage_source = 'llm' THEN f.completion_tokens END AS output_tokens,
		f.usage_source
	FROM wal w
	LEFT JOIN LATERAL (
		SELECT gw_session_id, virtual_client_id, application_id FROM public.request_context_attrs
		WHERE request_id = w.request_id AND tenant_id = w.tenant_id ORDER BY ts DESC LIMIT 1
	) c ON true
	LEFT JOIN public.session_dim s ON s.tenant_id = w.tenant_id AND s.gw_session_id = COALESCE(NULLIF(c.gw_session_id,''), w.gw_session_id)
	LEFT JOIN LATERAL (
		SELECT status, prompt_tokens, completion_tokens, usage_source FROM public.usage_facts
		WHERE request_id = w.request_id AND tenant_id = w.tenant_id
		ORDER BY revision DESC, finalized_at DESC, fact_id DESC LIMIT 1
	) f ON true
)`

// ReportColumns is the common JSON/CSV detail column contract.
func ReportColumns(section string) []string {
	return append([]string(nil), reportSections[section].columns...)
}

func ReadReport(ctx context.Context, pool *pgxpool.Pool, scope ReportScope, query ReportQuery) (Report, error) {
	section, ok := reportSections[query.Section]
	if !ok || (!scope.Platform && (scope.TenantID == "" || query.Section == "attempts")) {
		return Report{}, fmt.Errorf("report section unavailable in caller scope")
	}
	result := Report{
		Version: ReportVersion, Section: query.Section, Start: query.Start.UTC(), End: query.End.UTC(),
		Timezone: query.Timezone, TenantID: scope.TenantID, PlatformScope: scope.Platform,
		Coverage: section.coverage, Limit: query.Limit, Offset: query.Offset,
	}
	stmt := `WITH facts AS (` + section.base + `), totals AS (` + section.totals + `), page AS (
		SELECT facts.*, row_number() OVER (ORDER BY ` + section.order + `) AS row_no
		FROM facts ORDER BY ` + section.order + ` LIMIT $5 OFFSET $6
	)
	SELECT statement_timestamp(), to_jsonb(totals),
		COALESCE((SELECT jsonb_agg(to_jsonb(page) - 'row_no' ORDER BY row_no) FROM page), '[]'::jsonb)
	FROM totals`
	// Keep RLS settings transaction-local. The SQL tenant predicate remains in
	// each source even on connections whose role owns the underlying tables.
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return Report{}, err
	}
	defer tx.Rollback(ctx)
	role, bypass := "tenant_admin", "false"
	if scope.Platform {
		role, bypass = "super_admin", "true"
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('app.current_tenant',$1,true), set_config('app.current_role',$2,true), set_config('app.bypass_rls',$3,true)`, scope.TenantID, role, bypass); err != nil {
		return Report{}, err
	}
	if err := tx.QueryRow(ctx, stmt, result.Start, result.End, scope.TenantID, query.Timezone, query.Limit, query.Offset).
		Scan(&result.ReadAt, &result.Summary, &result.Rows); err != nil {
		return Report{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Report{}, err
	}
	return result, nil
}
