package reportrollup

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

const ProjectedSource = "request_metrics"

var ErrProjectionBackfill = errors.New("request metrics backfill is running")

// ProjectionCoverage describes the half-open interval actually read by the
// historical request report. PendingSince is limited to these occurrence dates.
type ProjectionCoverage struct {
	From         time.Time  `json:"from"`
	Through      time.Time  `json:"through"`
	Timezone     string     `json:"timezone"`
	UpdatedAt    *time.Time `json:"updated_at"`
	PendingSince *time.Time `json:"pending_since"`
}

type projectedMeasures struct {
	tokens, cacheRead, cacheInput, cacheMeasured, usdMeasured, latencyMeasured int64
}

func (p *projectedMeasures) add(row *projectedMeasures) {
	p.tokens += row.tokens
	p.cacheRead += row.cacheRead
	p.cacheInput += row.cacheInput
	p.cacheMeasured += row.cacheMeasured
	p.usdMeasured += row.usdMeasured
	p.latencyMeasured += row.latencyMeasured
}

// ReadProjectionCoverage and the report queries run in the same read snapshot.
// The picker includes its end date. The active local date is excluded, even
// when it falls inside the selected interval.
func ReadProjectionCoverage(ctx context.Context, q Querier, start, end, now time.Time) (ProjectionCoverage, error) {
	loc := start.Location()
	localDay := func(t time.Time) time.Time {
		year, month, day := t.In(loc).Date()
		return time.Date(year, month, day, 0, 0, 0, 0, loc)
	}
	from, through := localDay(start), localDay(end).AddDate(0, 0, 1)
	if today := localDay(now); today.Before(through) {
		through = today
	}
	if through.Before(from) {
		through = from
	}
	p := ProjectionCoverage{From: from.UTC(), Through: through.UTC(), Timezone: loc.String()}
	var ready bool
	err := q.QueryRow(ctx, `SELECT backfill_complete, updated_at,
		CASE WHEN backfill_complete THEN (SELECT min(enqueued_at) FROM public.request_metrics_pending WHERE occurred_at >= $1 AND occurred_at < $2) END
		FROM public.request_metrics_projection_state WHERE singleton`, p.From, p.Through).
		Scan(&ready, &p.UpdatedAt, &p.PendingSince)
	if err != nil {
		return p, fmt.Errorf("read request metrics coverage: %w", err)
	}
	if !ready {
		return p, ErrProjectionBackfill
	}
	return p, nil
}

func BuildProjectedReport(ctx context.Context, q Querier, start, end time.Time, view View, filter GrainFilter, names Names, daily bool, coverage ProjectionCoverage) (*GrainReport, error) {
	return buildGrainReport(ctx, q, start, end, view, filter, names, daily, &coverage)
}

const projectedDimensions = `coalesce(nullif(raw_model_name,''),nullif(outbound_model,''),nullif(client_model,''),'__unknown__') AS raw_model_name,
	provider_id, credential_id, api_key_id, tenant_id, coalesce(nullif(person_key,''),'unknown') AS person`

const projectedDateWhere = `occurred_at >= $1 AND occurred_at < $2 AND source_fact_id IS NOT NULL`

func projectedBusinessOnly(view View, filter GrainFilter) bool {
	return filter.BusinessOnly || view == ViewInternal || view == ViewKey
}

func projectedGrainWhere(filter GrainFilter, args []any) (string, []any) {
	var where strings.Builder
	arg := func(value any) int {
		args = append(args, value)
		return len(args)
	}
	for _, f := range []struct {
		column string
		value  *int64
	}{{"provider_id", filter.ProviderID}, {"credential_id", filter.CredentialID}, {"api_key_id", filter.APIKeyID}} {
		if f.value != nil {
			if *f.value == UnassignedID {
				fmt.Fprintf(&where, " AND %s IS NULL", f.column)
			} else {
				fmt.Fprintf(&where, " AND %s=$%d", f.column, arg(*f.value))
			}
		}
	}
	if filter.TenantID != "" {
		fmt.Fprintf(&where, " AND tenant_id=$%d", arg(filter.TenantID))
	}
	if filter.Person != "" {
		fmt.Fprintf(&where, " AND person=$%d", arg(filter.Person))
	}
	if len(filter.ModelNames) > 0 {
		fmt.Fprintf(&where, " AND raw_model_name=ANY($%d)", arg(filter.ModelNames))
	} else if filter.Model != "" {
		fmt.Fprintf(&where, " AND (raw_model_name=$%[1]d OR outbound_model=$%[1]d)", arg(filter.Model))
	}
	return where.String(), args
}

const projectedGrainKeys = `GROUPING(report_date), GROUPING(provider_id), GROUPING(credential_id),
	GROUPING(raw_model_name), GROUPING(tenant_id), GROUPING(person), GROUPING(api_key_id),
	report_date, provider_id, credential_id, raw_model_name, tenant_id, person, api_key_id`

func projectedGrainSQL(where string, daily bool) string {
	return `WITH bounded AS (
		SELECT (occurred_at AT TIME ZONE $3)::date AS report_date, ` + projectedDimensions + `,
			outbound_model, status, coalesce(nullif(error_kind,''),nullif(status,''),'unknown') AS kind,
			prompt_tokens, completion_tokens, cached_tokens, cache_write_tokens, cache_input_tokens,
			total_tokens, credits_charged, latency_ms, cost_amount, cost_currency
		FROM public.request_metrics WHERE ` + projectedDateWhere + ` AND (NOT $4::boolean OR traffic_class='business')
	), facts AS MATERIALIZED (SELECT * FROM bounded WHERE true` + where + `)
	SELECT ` + projectedGrainKeys + `, NULL::text,
		count(*), count(*) FILTER (WHERE status='success'),
		coalesce(sum(prompt_tokens),0)::bigint, coalesce(sum(completion_tokens),0)::bigint,
		coalesce(sum(cached_tokens),0)::bigint, coalesce(sum(cache_write_tokens),0)::bigint,
		coalesce(sum(cost_amount) FILTER (WHERE cost_currency='USD'),0)*100, 'USD',
		coalesce(sum(credits_charged),0)::bigint, 0::float8,
		coalesce(percentile_cont(0.5) WITHIN GROUP (ORDER BY latency_ms) FILTER (WHERE status='success' AND latency_ms>0),0),
		coalesce(percentile_cont(0.95) WITHIN GROUP (ORDER BY latency_ms) FILTER (WHERE status='success' AND latency_ms>0),0),
		CASE WHEN count(*) FILTER (WHERE status='success' AND latency_ms>0)>0 THEN 1::float8 ELSE 0::float8 END, 0::bigint,
		coalesce(sum(total_tokens),0)::bigint, coalesce(sum(cached_tokens) FILTER (WHERE cache_input_tokens IS NOT NULL),0)::bigint,
		coalesce(sum(cache_input_tokens),0)::bigint, count(cache_input_tokens), count(cost_amount) FILTER (WHERE cost_currency='USD'),
		count(*) FILTER (WHERE status='success' AND latency_ms>0)
	FROM facts GROUP BY GROUPING SETS (` + strings.Join(grainGroupSets(daily), ", ") + `)
	UNION ALL
	SELECT ` + projectedGrainKeys + `, kind,
		0,0,0,0,0,0,0,'USD',0,0,0,0,0,count(*),0,0,0,0,0,0
	FROM facts WHERE status<>'success' GROUP BY GROUPING SETS (` + strings.Join(grainErrGroupSets(daily), ", ") + `)`
}

func runProjectedGrain(ctx context.Context, q Querier, view View, filter GrainFilter, daily bool, coverage ProjectionCoverage, sink func(groupKind, grainScanRow)) error {
	args := []any{coverage.From, coverage.Through, coverage.Timezone, projectedBusinessOnly(view, filter)}
	where, args := projectedGrainWhere(filter, args)
	rows, err := q.Query(ctx, projectedGrainSQL(where, daily), args...)
	if err != nil {
		return fmt.Errorf("aggregate request metrics report: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		row, err := scanGrainMeasures(rows, true)
		if err != nil {
			return err
		}
		sink(classify(row), row)
	}
	return rows.Err()
}

func LoadProjectedDimensionOptions(ctx context.Context, q Querier, view View, filter GrainFilter, coverage ProjectionCoverage) (*DimensionOptions, error) {
	if !view.Valid() {
		return nil, fmt.Errorf("unknown view %q", view)
	}
	cte := `WITH candidate_requests AS MATERIALIZED (
		SELECT ` + projectedDimensions + `, 1 AS request_count, false AS legacy_row
		FROM public.request_metrics WHERE ` + projectedDateWhere + ` AND (NOT $3::boolean OR traffic_class='business')
	)`
	args := []any{coverage.From, coverage.Through, projectedBusinessOnly(view, filter)}
	dims, err := loadDimensionOptions(ctx, q, cte, "SELECT * FROM candidate_requests", "", args)
	if err != nil {
		return nil, err
	}
	for _, list := range []*[]DimensionOption{&dims.Providers, &dims.Credentials, &dims.APIKeys, &dims.Models, &dims.Tenants, &dims.Persons} {
		if *list == nil {
			*list = []DimensionOption{}
		}
	}
	return dims, nil
}
