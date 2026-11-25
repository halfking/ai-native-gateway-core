package reconciliation

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrTerminalProjectionBackfill = errors.New("request metrics backfill is running")

// ReportProjection describes terminal usage evidence. Pending includes the
// caller's retained backlog on either side of midnight, since a terminal fact
// may be linked to a receipt on a different day.
type ReportProjection struct {
	Source       string     `json:"source"`
	UpdatedAt    *time.Time `json:"updated_at"`
	PendingSince *time.Time `json:"pending_since"`
}

func ReadProjectedReport(ctx context.Context, pool *pgxpool.Pool, scope ReportScope, query ReportQuery) (Report, error) {
	if query.Day == "" || query.Month != "" {
		return Report{}, fmt.Errorf("projected reconciliation requires one calendar day")
	}
	return readReport(ctx, pool, scope, query, true)
}

func needsRequestProjection(section string) bool {
	return section == "requests" || section == "sessions" || section == "coverage"
}

func readTerminalProjection(ctx context.Context, tx pgx.Tx, tenant string) (*ReportProjection, error) {
	p := &ReportProjection{Source: "request_metrics"}
	var ready bool
	err := tx.QueryRow(ctx, `SELECT backfill_complete, updated_at,
		CASE WHEN backfill_complete THEN (SELECT min(enqueued_at) FROM public.request_metrics_pending_events
			WHERE $1='' OR tenant_id=$1) END
		FROM public.request_metrics_repair_state WHERE singleton`, tenant).Scan(&ready, &p.UpdatedAt, &p.PendingSince)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !ready) {
		return nil, ErrTerminalProjectionBackfill
	}
	if err != nil {
		return nil, fmt.Errorf("read reconciliation projection state: %w", err)
	}
	return p, nil
}

const projectedUsageColumns = `CASE WHEN f.usage_source IN ('llm','corrected') THEN NULLIF(f.prompt_tokens, 0) END AS input_tokens,
		CASE WHEN f.usage_source IN ('llm','corrected') THEN NULLIF(f.completion_tokens, 0) END AS output_tokens,
		CASE WHEN f.usage_source IN ('llm','corrected') THEN NULLIF(f.total_tokens, 0) END AS total_tokens,
		` + requestAdmissionColumns

const projectedUsageSource = `LEFT JOIN LATERAL (
		SELECT CASE WHEN status IN ('failed','timeout') THEN 'failure' ELSE status END AS status,
		       prompt_tokens, completion_tokens, total_tokens, usage_source, api_key_id, traffic_class
		FROM public.request_metrics_projection
		WHERE request_id = w.request_id AND tenant_id = w.tenant_id AND source_fact_id IS NOT NULL
		ORDER BY source_revision DESC NULLS LAST, source_finalized_at DESC NULLS LAST, source_fact_id DESC LIMIT 1
	) f ON true
	`

const projectedReceiptFacts = requestClassificationStart + `coalesce(f.traffic_class, ` + receiptTrafficClass + `)` +
	requestClassificationEnd + projectedUsageColumns + requestEvidenceDimensions + projectedUsageSource + requestScopeFilter

const projectedTerminalStatuses = `('success','failure','failed','timeout','rate_limited','cancelled')`

const projectedUsageWindowKeys = `SELECT tenant_id, request_id FROM public.request_metrics_projection
	WHERE occurred_at >= $1 AND occurred_at < $2 AND ($3 = '' OR tenant_id = $3)
	  AND source_fact_id IS NOT NULL AND status IN ` + projectedTerminalStatuses + `
	`

const projectedUsageReceiptJoin = `SELECT k.tenant_id, k.request_id, u.occurred_at
	FROM keys k JOIN LATERAL (
		SELECT occurred_at FROM public.request_metrics_projection
		WHERE tenant_id=k.tenant_id AND request_id=k.request_id AND source_fact_id IS NOT NULL
		  AND status IN ` + projectedTerminalStatuses + `
		ORDER BY occurred_at LIMIT 1
	) u ON true
`

func projectedReportSection(name string, section reportSection) reportSection {
	switch name {
	case "requests":
		section.base = projectedReceiptFacts + requestDetailColumns + " total_tokens, " + requestDetailSends
		section.totals = requestTotals + `count(total_tokens) AS measured_total_requests, sum(total_tokens) AS total_tokens,
			count(*) FILTER (WHERE traffic_class NOT IN ('business','platform','unknown')) AS other_traffic_requests
			FROM facts`
		section.coverage = "recorded_wal_and_projected_terminal_usage_legacy_zeros_unverified"
		section.columns = append(append([]string(nil), section.columns...), "total_tokens")
	case "sessions":
		section.base = projectedReceiptFacts + sessionReportRows
		section.coverage = "canonical_session_dimension_and_recorded_activity_with_projected_terminal_usage"
	case "coverage":
		section.base = coverageEvidenceKeys + projectedUsageWindowKeys + coverageReceiptKeys + projectedUsageReceiptJoin + coverageComparison
		section.coverage = "projected_terminal_usage_and_retained_evidence_historical_supplier_population_unknown"
	case "postings":
		section.totals = postingTotals + `,
			count(*) FILTER (WHERE entry_type='consume' AND net_credits > 0) AS recorded_debit_entries,
			sum(net_credits) FILTER (WHERE entry_type='consume' AND net_credits > 0) AS recorded_gross_debit_credits,
			count(*) FILTER (WHERE entry_type IN ('refund','reversal')) AS recorded_return_entries,
			sum(-net_credits) FILTER (WHERE entry_type IN ('refund','reversal')) AS recorded_returned_credits
			FROM facts`
	}
	return section
}
