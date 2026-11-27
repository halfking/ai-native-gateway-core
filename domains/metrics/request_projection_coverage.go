package metrics

import (
	"context"
	"time"
)

type ProjectionCoverage struct {
	From               time.Time `json:"from"`
	Through            time.Time `json:"through"`
	Expected           int64     `json:"expected_requests"`
	Current            int64     `json:"current_requests"`
	WithSession        int64     `json:"with_session"`
	WithProvider       int64     `json:"with_provider"`
	WithMeasuredTokens int64     `json:"with_measured_tokens"`
	WithCurrency       int64     `json:"with_currency"`
	Fraction           *float64  `json:"fraction"`
}

// ReadProjectionCoverage checks the latest retained fact version at each occurrence.
// The reader can be an operations connection or a report's existing transaction.
func ReadProjectionCoverage[Row interface{ Scan(...any) error }](ctx context.Context, queryRow func(context.Context, string, ...any) Row, start, end time.Time) (ProjectionCoverage, error) {
	result := ProjectionCoverage{From: start, Through: end}
	err := queryRow(ctx, projectionCoverageSQL, start, end).Scan(&result.Expected, &result.Current, &result.WithSession, &result.WithProvider,
		&result.WithMeasuredTokens, &result.WithCurrency)
	if err == nil && result.Expected > 0 {
		fraction := float64(result.Current) / float64(result.Expected)
		result.Fraction = &fraction
	}
	return result, err
}

const projectionCoverageSQL = `
WITH latest AS (
	SELECT DISTINCT ON (tenant_id,request_id,occurred_at)
		tenant_id,request_id,occurred_at,fact_id,revision,finalized_at
	FROM public.usage_facts
	WHERE occurred_at >= $1 AND occurred_at < $2
	ORDER BY tenant_id,request_id,occurred_at,revision DESC,finalized_at DESC,fact_id DESC
)
SELECT count(*), count(p.source_fact_id), count(NULLIF(p.session_id,'')), count(p.provider_id),
       count(*) FILTER (WHERE p.usage_source IN ('llm','corrected')),
       count(NULLIF(p.cost_currency,''))
FROM latest l
LEFT JOIN public.request_metrics_projection p
	ON (p.tenant_id,p.request_id,p.occurred_at)=(l.tenant_id,l.request_id,l.occurred_at)
	AND (p.source_fact_id,p.source_revision,p.source_finalized_at)=(l.fact_id,l.revision,l.finalized_at)
`
