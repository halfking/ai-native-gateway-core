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
	return buildGrainReport(ctx, q, start, end, view, filter, names, daily, &coverage, "")
}

type ProjectedSummaryScope string

const (
	ProjectedSummaryTotals       ProjectedSummaryScope = "totals"
	ProjectedSummaryDistribution ProjectedSummaryScope = "distribution"
)

// BuildProjectedSummary computes only totals or the distributions shown by the
// requested view. Both scopes include exact daily totals and coverage.
func BuildProjectedSummary(ctx context.Context, q Querier, start, end time.Time, view View, filter GrainFilter, names Names, coverage ProjectionCoverage, scope ProjectedSummaryScope) (*GrainReport, error) {
	if scope != ProjectedSummaryTotals && scope != ProjectedSummaryDistribution {
		return nil, fmt.Errorf("unknown request metrics summary scope %q", scope)
	}
	return buildGrainReport(ctx, q, start, end, view, filter, names, false, &coverage, scope)
}

const projectedModelKey = `coalesce(nullif(raw_model_name,''),nullif(outbound_model,''),nullif(client_model,''),'__unknown__')`
const projectedDimensions = projectedModelKey + ` AS raw_model_name,
	provider_id, credential_id, api_key_id, tenant_id, coalesce(nullif(person_key,''),'unknown') AS person`

const projectedDateWhere = `occurred_at >= $1 AND occurred_at < $2 AND source_fact_id IS NOT NULL`
const projectedErrorKind = `coalesce(nullif(error_kind,''),nullif(status,''),'unknown')`
const projectedLatencySample = `status='success' AND latency_ms>0`

const projectedAdditiveMeasures = `count(*) AS requests, count(*) FILTER (WHERE status='success') AS successes,
	coalesce(sum(prompt_tokens),0)::bigint AS input_tokens,
	coalesce(sum(completion_tokens),0)::bigint AS output_tokens,
	coalesce(sum(cached_tokens),0)::bigint AS cache_read,
	coalesce(sum(cache_write_tokens),0)::bigint AS cache_write,
	coalesce(sum(cost_amount) FILTER (WHERE cost_currency='USD'),0)*100 AS usd_cents,
	coalesce(sum(credits_charged),0)::bigint AS credits,
	coalesce(sum(total_tokens),0)::bigint AS tokens,
	coalesce(sum(cached_tokens) FILTER (WHERE cache_input_tokens IS NOT NULL),0)::bigint AS cache_covered_read,
	coalesce(sum(cache_input_tokens),0)::bigint AS cache_input,
	count(cache_input_tokens) AS cache_coverage,
	count(cost_amount) FILTER (WHERE cost_currency='USD') AS usd_coverage`

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

const projectedGrainColumns = `date_group, provider_group, credential_group, model_group, tenant_group, person_group, key_group,
	report_date, provider_id, credential_id, raw_model_name, tenant_id, person, api_key_id`

var projectedOwnerKeys = strings.NewReplacer("provider_id, credential_id", "credential_id", "tenant_id, api_key_id", "api_key_id")

func projectedProviderDistribution(view View) bool {
	return view == ViewProvider || view == ViewCredential
}

var projectedTenantPersonGroups = []string{"(tenant_id)", "(tenant_id, person)"}

func projectedGrainGroups(daily bool, view View, scope ProjectedSummaryScope) []string {
	if scope == "" {
		groups := grainGroupSets(daily)
		for i := range groups {
			groups[i] = projectedOwnerKeys.Replace(groups[i])
		}
		return groups
	}
	groups := []string{"()", "(report_date)"}
	if scope == ProjectedSummaryDistribution {
		groups = append(groups, "(raw_model_name)")
		if projectedProviderDistribution(view) {
			groups = append(groups, "(provider_id)")
		} else {
			groups = append(groups, projectedTenantPersonGroups...)
		}
	}
	return groups
}

func projectedGrainKeys(sets string, owners bool) (flags, measureKeys, errorKeys string) {
	for _, dimension := range []struct{ column, flag, pgType string }{
		{"report_date", "date_group", "date"}, {"provider_id", "provider_group", "bigint"},
		{"credential_id", "credential_group", "bigint"}, {"raw_model_name", "model_group", "text"},
		{"tenant_id", "tenant_group", "text"}, {"person", "person_group", "text"}, {"api_key_id", "key_group", "bigint"},
	} {
		key, flag := "NULL::"+dimension.pgType, "1"
		if strings.Contains(sets, dimension.column) {
			key, flag = dimension.column, "GROUPING("+dimension.column+")"
		}
		flags += flag + " AS " + dimension.flag + ", "
		errorKeys += key + ", "
		measureKey := key
		if owners && dimension.column == "provider_id" {
			measureKey = `CASE WHEN GROUPING(credential_id)=1 THEN provider_id
			     WHEN count(provider_id)=count(*) AND min(provider_id)=max(provider_id) THEN min(provider_id) END`
		}
		if owners && dimension.column == "tenant_id" {
			measureKey = `CASE WHEN GROUPING(api_key_id)=1 THEN tenant_id
			     WHEN count(tenant_id)=count(*) AND min(tenant_id)=max(tenant_id) THEN min(tenant_id) END`
		}
		measureKeys += measureKey + " AS " + dimension.column + ", "
	}
	return flags, measureKeys, errorKeys
}

func projectedGrainSQL(where string, groups []string, fullReport bool) string {
	// The displayed credential and key rows each have one ID, independent of
	// source attribution. Aggregate their samples together before computing ranks.
	sets := strings.Join(groups, ", ")
	flags, measureKeys, errorKeys := projectedGrainKeys(sets, fullReport)
	errorSets := strings.Join(grainErrorGroups(groups), ", ")
	materialization := "MATERIALIZED"
	if !fullReport {
		// The smaller grouping sets leave fewer columns live. Inline each scan
		// so PostgreSQL can prune columns and push down the error filter.
		materialization = "NOT MATERIALIZED"
	}
	return `WITH bounded AS (
		SELECT (occurred_at AT TIME ZONE $3)::date AS report_date, ` + projectedDimensions + `,
			outbound_model, status, ` + projectedErrorKind + ` AS kind,
			prompt_tokens, completion_tokens, cached_tokens, cache_write_tokens, cache_input_tokens,
			total_tokens, credits_charged, latency_ms, cost_amount, cost_currency
		FROM public.request_metrics_projection WHERE ` + projectedDateWhere + ` AND (NOT $4::boolean OR traffic_class='business')
	), facts AS ` + materialization + ` (SELECT * FROM bounded WHERE true` + where + `),
	measures AS (
		SELECT ` + flags + measureKeys + projectedAdditiveMeasures + `,
			percentile_cont(ARRAY[0.5,0.95]) WITHIN GROUP (ORDER BY latency_ms)
				FILTER (WHERE ` + projectedLatencySample + `) AS latency_ranks,
			count(*) FILTER (WHERE ` + projectedLatencySample + `) AS latency_coverage
		FROM facts GROUP BY GROUPING SETS (` + sets + `)
	)
	SELECT ` + projectedGrainColumns + `, NULL::text,
		requests, successes, input_tokens, output_tokens, cache_read, cache_write, usd_cents, 'USD',
		credits, 0::float8, coalesce(latency_ranks[1],0), coalesce(latency_ranks[2],0),
		(latency_coverage>0)::int::float8, 0::bigint,
		tokens, cache_covered_read, cache_input, cache_coverage, usd_coverage, latency_coverage
	FROM measures
	UNION ALL
	SELECT ` + flags + errorKeys + `kind,
		0,0,0,0,0,0,0,'USD',0,0,0,0,0,count(*),0,0,0,0,0,0
	FROM facts WHERE status<>'success' GROUP BY GROUPING SETS (` + errorSets + `)`
}

func runProjectedGrain(ctx context.Context, q Querier, view View, filter GrainFilter, daily bool, coverage ProjectionCoverage, scope ProjectedSummaryScope, sink func(groupKind, grainScanRow)) error {
	groups := projectedGrainGroups(daily, view, scope)
	if eligibleDailyHistory(filter, coverage, scope) {
		used, err := runDailyHistory(ctx, q, view, filter, coverage, scope, sink)
		if err != nil {
			return err
		}
		if used {
			if scope == ProjectedSummaryTotals || projectedProviderDistribution(view) {
				return nil
			}
			// Published history covers totals and models. Query tenant and
			// person groups in the same snapshot without repeating those ranks.
			groups = projectedTenantPersonGroups
		}
	}
	args := []any{coverage.From, coverage.Through, coverage.Timezone, projectedBusinessOnly(view, filter)}
	where, args := projectedGrainWhere(filter, args)
	rows, err := q.Query(ctx, projectedGrainSQL(where, groups, scope == ""), args...)
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
		FROM public.request_metrics_projection WHERE ` + projectedDateWhere + ` AND (NOT $3::boolean OR traffic_class='business')
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
