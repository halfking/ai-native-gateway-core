package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// RequestStatsQuery describes a read from the request projection.
type RequestStatsQuery struct {
	MetricsFilter
	Group         string
	Timezone      string
	Limit         int
	Offset        int
	OwnerSearch   string
	Credential    *int64
	Canonical     *int64
	APIKey        *int64
	Application   *int64
	Model         string
	Models        []string
	ClientModel   string
	EndUser       string
	Owner         string
	KeyPrefix     string
	AppCode       string
	TaskType      string
	BucketMinutes int
}

type RequestStats struct {
	Source       string            `json:"source"`
	Start        time.Time         `json:"start"`
	End          time.Time         `json:"end"`
	Timezone     string            `json:"timezone"`
	Group        string            `json:"group"`
	Ready        bool              `json:"ready"`
	UpdatedAt    *time.Time        `json:"updated_at"`
	PendingSince *time.Time        `json:"pending_since"`
	Items        []json.RawMessage `json:"items"`
}

type requestStatsDB interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

const requestModelKey = "coalesce(nullif(outbound_model,''), nullif(client_model,''), '__unknown__')"

const (
	uniqueModelAggregate   = `count(DISTINCT nullif(` + requestModelKey + `,'__unknown__'))`
	activeSessionAggregate = `count(DISTINCT (tenant_id, session_id)) FILTER (WHERE session_id IS NOT NULL)`
)

// Separate membership grouping lets PostgreSQL use hashing for exact counts
// and aggregate the request measures without an ordered aggregate.
func statsWithGroupedMembership(source string) string {
	models := "(SELECT count(*) FROM (SELECT " + requestModelKey + " AS model FROM " + source +
		" GROUP BY 1) AS models WHERE model <> '__unknown__')"
	sessions := "(SELECT count(*) FROM (SELECT tenant_id, session_id FROM " + source +
		" WHERE session_id IS NOT NULL GROUP BY tenant_id, session_id) AS sessions)"
	return statsAggregates(models, sessions)
}

var requestStatsCounters = []struct{ name, measure string }{
	{"total_requests", "count(*)"},
	{"success_count", "count(*) FILTER (WHERE status = 'success')"},
	{"failed_count", "count(*) FILTER (WHERE status <> 'success')"},
	{"timeout_count", "count(*) FILTER (WHERE status = 'timeout')"},
	{"rate_limited_count", "count(*) FILTER (WHERE status = 'rate_limited')"},
	{"cache_hit_requests", "count(*) FILTER (WHERE cached_tokens > 0)"},
	{"cache_input_covered_requests", "count(cache_input_tokens)"},
	{"cache_input_read_tokens", "coalesce(sum(cached_tokens) FILTER (WHERE cache_input_tokens IS NOT NULL),0)"},
	{"cache_input_total_tokens", "coalesce(sum(cache_input_tokens),0)"},
	{"measured_token_requests", "count(*) FILTER (WHERE usage_source IN ('llm','corrected'))"},
	{"currency_evidence_requests", "count(cost_currency)"},
	{"usd_evidence_requests", "count(*) FILTER (WHERE cost_currency='USD')"},
	{"total_prompt_tokens", "coalesce(sum(prompt_tokens),0)"},
	{"total_completion_tokens", "coalesce(sum(completion_tokens),0)"},
	{"total_tokens", "coalesce(sum(total_tokens),0)"},
	{"cache_read_tokens", "coalesce(sum(cached_tokens),0)"},
	{"cache_write_tokens", "coalesce(sum(cache_write_tokens),0)"},
	{"image_tokens", "coalesce(sum(image_tokens),0)"},
	{"audio_tokens", "coalesce(sum(audio_tokens),0)"},
	{"video_tokens", "coalesce(sum(video_tokens),0)"},
	{"total_credits_charged", "coalesce(sum(credits_charged),0)"},
	{"total_cost_usd", "coalesce(sum(cost_amount) FILTER (WHERE cost_currency='USD'),0)"},
	{"cost_rows_outside_usd", "count(*) FILTER (WHERE cost_amount <> 0 AND cost_currency IS DISTINCT FROM 'USD')"},
}

// Use the same definitions when grouping requests and when rebuilding a day.
func requestStatsCounterSQL(merge bool) string {
	columns := make([]string, 0, len(requestStatsCounters))
	for _, counter := range requestStatsCounters {
		measure := counter.measure
		if merge {
			measure = "coalesce(sum(" + counter.name + "),0)"
		}
		columns = append(columns, measure+" AS "+counter.name)
	}
	return strings.Join(columns, ", ")
}

var requestStatsRawCounters = requestStatsCounterSQL(false)

func statsAggregates(uniqueModels, activeSessions string) string {
	return requestStatsRawCounters + ", " + uniqueModels + " AS unique_models, " + activeSessions + ` AS active_sessions,
	coalesce(avg(latency_ms),0) AS avg_latency_ms,
	coalesce(avg(ttft_ms),0) AS avg_ttft_ms,
	coalesce(count(*) FILTER (WHERE status='success')::float8/nullif(count(*),0),0) AS success_rate,
	min(occurred_at) AS first_request_at,
	max(occurred_at) AS last_request_at
`
}

const requestCostAggregates = `
	count(*) AS total_requests,
	coalesce(sum(prompt_tokens),0) AS total_prompt_tokens,
	coalesce(sum(completion_tokens),0) AS total_completion_tokens,
	coalesce(sum(credits_charged),0) AS total_credits_charged,
	coalesce(sum(cost_amount) FILTER (WHERE cost_currency='USD'),0) AS total_cost_usd,
	count(*) FILTER (WHERE cost_amount <> 0 AND cost_currency IS DISTINCT FROM 'USD') AS cost_rows_outside_usd`

var requestCostDimensions = map[string]string{
	"cost-model":       requestModelKey,
	"cost-provider":    "coalesce(nullif(provider_code,''), provider_id::text, '__unknown__')",
	"cost-api_key":     "coalesce(nullif(api_key_prefix,'') || ' · ' || api_key_id::text, api_key_id::text, '__unknown__')",
	"cost-application": "coalesce(nullif(application_code,''), application_id::text, '__unknown__')",
	"cost-tenant":      "tenant_id",
	"cost-work_type":   "coalesce(work_type,'__unknown__')",
	"cost-intent":      "coalesce(session_intent,'__unknown__')",
}

func buildRequestStatsQuery(query RequestStatsQuery) (string, []any, error) {
	clauses, args := buildFilterClauses(query.MetricsFilter, "", "occurred_at", kindTimestamp)
	scope := clauses[2:]
	clauses = append(clauses, "source_fact_id IS NOT NULL")
	addFilter := func(column string, value any) {
		args = append(args, value)
		clauses = append(clauses, fmt.Sprintf("%s = $%d", column, len(args)))
	}
	for _, filter := range []struct {
		column string
		value  *int64
	}{
		{"credential_id", query.Credential},
		{"canonical_id", query.Canonical},
		{"api_key_id", query.APIKey},
		{"application_id", query.Application},
	} {
		if filter.value != nil {
			addFilter(filter.column, *filter.value)
		}
	}
	if query.Model != "" {
		addFilter("outbound_model", query.Model)
	}
	const trendModel = requestModelKey
	if len(query.Models) != 0 {
		args = append(args, query.Models)
		clauses = append(clauses, fmt.Sprintf("%s = ANY($%d::text[])", trendModel, len(args)))
	}
	for _, filter := range []struct {
		column, value string
	}{
		{"coalesce(client_model,'unknown')", query.ClientModel},
		{"coalesce(application_code,'unknown')", query.AppCode},
		{"coalesce(task_type,'unknown')", query.TaskType},
		{"owner_user", query.Owner},
	} {
		if filter.value != "" {
			addFilter(filter.column, filter.value)
		}
	}
	if query.EndUser != "" {
		args = append(args, query.EndUser)
		clauses = append(clauses, fmt.Sprintf("(user_id = $%d OR coalesce(person_key,'unknown') = $%d)", len(args), len(args)))
	}
	if query.KeyPrefix != "" {
		args = append(args, query.KeyPrefix)
		clauses = append(clauses, fmt.Sprintf("starts_with(api_key_prefix, $%d)", len(args)))
	}

	key, dimensions, groupBy, order := "", "", "", ""
	aggregates := statsAggregates(uniqueModelAggregate, activeSessionAggregate)
	switch query.Group {
	case "summary":
	case "currency":
		key = "cost_currency"
		aggregates = `count(*) AS total_requests, count(cost_currency) AS evidenced_requests,
			sum(cost_amount) FILTER (WHERE cost_currency IS NOT NULL) AS original_amount`
		order = " ORDER BY total_requests DESC, key NULLS LAST"
	case "cache-economics":
		aggregates = requestCostAggregates + `,
		coalesce(sum(cached_tokens),0) AS cache_read_tokens,
		coalesce(sum(cache_write_tokens),0) AS cache_write_tokens,
		coalesce(sum(cached_tokens) FILTER (WHERE cache_input_tokens IS NOT NULL),0) AS eligible_cache_read_tokens,
		coalesce(sum(cache_input_tokens),0) AS cache_input_tokens,
		count(cache_input_tokens) AS cache_scope_requests,
		count(compression_applied) AS compression_scope_requests,
		count(*) FILTER (WHERE compression_applied) AS compressed_requests`
	case "cost-model", "cost-provider", "cost-api_key", "cost-application", "cost-tenant", "cost-work_type", "cost-intent":
		key = requestCostDimensions[query.Group]
		aggregates = requestCostAggregates + `,
		coalesce(avg(latency_ms),0) AS avg_latency_ms,
		count(*) FILTER (WHERE status <> 'success') AS failed_count`
		order = " ORDER BY total_cost_usd DESC, total_requests DESC, key"
	case "key-summary":
		dimensions = "coalesce(max(api_key_prefix),'') AS key_prefix,"
	case "provider-summary":
		dimensions = "min(provider_id) AS provider_id, max(provider_code) AS provider_code, max(provider_name) AS provider_name,"
	case "minute", "hour", "day", "week", "month":
		args = append(args, query.Timezone)
		key = fmt.Sprintf(`to_char(date_trunc('%s', occurred_at, $%d) AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')`, query.Group, len(args))
		dimensions, order = "min(occurred_at) AS first_in_bucket,", " ORDER BY first_in_bucket"
	case "top-models", "top-clients", "top-users", "top-keys", "top-projects", "top-tasks":
		keys := map[string]string{
			"top-models":   trendModel,
			"top-clients":  "coalesce(client_model,'unknown')",
			"top-users":    "coalesce(person_key,'unknown')",
			"top-keys":     "coalesce(api_key_prefix || '...' || coalesce(owner_user,''),'unknown')",
			"top-projects": "coalesce(application_code,'unknown')",
			"top-tasks":    "coalesce(task_type,'unknown')",
		}
		key = keys[query.Group]
		if query.Group == "top-keys" {
			clauses = append(clauses, "api_key_id IS NOT NULL")
		}
		order = " ORDER BY total_cost_usd DESC, total_tokens DESC, total_requests DESC, key"
	case "trend-model":
		key = trendModel
		dimensions = trendModel + " AS model,"
	case "trend-bucket":
		switch query.BucketMinutes {
		case 5, 15, 60:
		default:
			return "", nil, fmt.Errorf("unsupported trend bucket size")
		}
		args = append(args, query.BucketMinutes)
		key = fmt.Sprintf(`to_char(date_bin(make_interval(mins => $%d::int), occurred_at, '2000-01-01 00:00:00+00') AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')`, len(args))
		dimensions = trendModel + " AS model,"
		groupBy, order = ", "+trendModel, " ORDER BY key, model"
	case "day-model":
		args = append(args, query.Timezone)
		key = fmt.Sprintf("to_char(occurred_at AT TIME ZONE $%d, 'YYYY-MM-DD')", len(args))
		dimensions = "coalesce(outbound_model,'') AS model,"
		groupBy, order = ", coalesce(outbound_model,'')", " ORDER BY key, model"
	case "model":
		key = "coalesce(outbound_model,'')"
		dimensions = "coalesce(outbound_model,'') AS model, coalesce(provider_code,'') AS provider_code,"
		groupBy = ", coalesce(provider_code,'')"
	case "provider":
		key = "coalesce(provider_id::text,'')"
		dimensions = "provider_id, coalesce(max(provider_code),'') AS provider_code, coalesce(max(provider_name),'') AS provider_name,"
		clauses = append(clauses, "provider_id IS NOT NULL")
		groupBy = ", provider_id"
	case "key":
		key = "api_key_id::text"
		dimensions = "api_key_id, max(api_key_prefix) AS key_prefix, max(application_code) AS application_code, max(owner_user) AS owner_user,"
		groupBy = ", api_key_id"
		clauses = append(clauses, "api_key_id IS NOT NULL")
	case "credential":
		key = "credential_id::text"
		clauses = append(clauses, "credential_id IS NOT NULL")
	case "canonical":
		key = "canonical_id::text"
		clauses = append(clauses, "canonical_id IS NOT NULL")
	case "application":
		key = "application_id::text"
		clauses = append(clauses, "application_id IS NOT NULL")
	case "tenant":
		key = "tenant_id"
	case "person":
		key = "person_key"
		clauses = append(clauses, "person_key IS NOT NULL")
	case "client":
		key = "client_id"
		clauses = append(clauses, "client_id IS NOT NULL")
	case "owner":
		key = "owner_user"
		clauses = append(clauses, "owner_user IS NOT NULL")
	case "owner-directory":
		key = "owner_user"
		clauses = append(clauses, "nullif(owner_user,'') IS NOT NULL")
		if query.OwnerSearch != "" {
			args = append(args, query.OwnerSearch)
			clauses = append(clauses, fmt.Sprintf("strpos(lower(owner_user),lower($%d)) > 0", len(args)))
		}
		dimensions = `tenant_id,count(DISTINCT (tenant_id, person_key)) FILTER (WHERE person_key IS NOT NULL) AS end_user_count,
			count(cost_amount) FILTER (WHERE cost_currency='USD') AS usd_measured_requests,
			sum(cost_amount) FILTER (WHERE cost_currency='USD' AND nullif(session_id,'') IS NOT NULL) /
				nullif(count(DISTINCT nullif(session_id,'')) FILTER (WHERE cost_currency='USD' AND cost_amount IS NOT NULL),0) AS avg_profile_usd,`
		groupBy = ", tenant_id"
		order = " ORDER BY total_cost_usd DESC, total_requests DESC, key, tenant_id"
	case "owner-models":
		key = trendModel
		dimensions = `tenant_id,owner_user,coalesce(provider_code,'') AS provider,
			count(cost_amount) FILTER (WHERE cost_currency='USD') AS usd_measured_requests,`
		groupBy = ", tenant_id, owner_user, coalesce(provider_code,'')"
		clauses = append(clauses, "nullif(owner_user,'') IS NOT NULL")
		order = " ORDER BY total_requests DESC, key, tenant_id, owner_user, provider"
	case "task":
		key = "task_id"
		clauses = append(clauses, "task_id IS NOT NULL")
	case "session":
		key = "session_id"
		dimensions = "tenant_id,"
		groupBy = ", tenant_id"
		clauses = append(clauses, "session_id IS NOT NULL")
	case "modality":
		key = "coalesce(modality,'')"
	case "protocol":
		key = "coalesce(protocol,'')"
	case "errors":
		key = "coalesce(error_kind,'')"
		clauses = append(clauses, "status <> 'success'")
	default:
		return "", nil, fmt.Errorf("unsupported request metrics group %q", query.Group)
	}
	if key != "" {
		dimensions = key + " AS key, " + dimensions
		groupBy = " GROUP BY 1" + groupBy
		if order == "" {
			order = " ORDER BY total_requests DESC, key"
		}
		if query.Limit > 0 && query.Group != "owner-directory" {
			args = append(args, query.Limit)
			order += fmt.Sprintf(" LIMIT $%d", len(args))
		}
	}
	if strings.HasPrefix(query.Group, "top-") || query.Group == "trend-model" || query.Group == "trend-bucket" {
		aggregates = `count(*) AS total_requests,
			coalesce(sum(total_tokens),0) AS total_tokens,
			coalesce(sum(credits_charged),0) AS total_credits_charged,
			coalesce(sum(cost_amount) FILTER (WHERE cost_currency='USD'),0) AS total_cost_usd`
	}
	itemSQL := "SELECT " + dimensions + aggregates + " FROM public.request_metrics_projection WHERE " + strings.Join(clauses, " AND ") + groupBy + order
	if query.Group == "summary" {
		itemSQL = `WITH summary_input AS NOT MATERIALIZED (
			SELECT * FROM public.request_metrics_projection WHERE ` + strings.Join(clauses, " AND ") + `
		) SELECT ` + statsWithGroupedMembership("summary_input") + ` FROM summary_input`
	}
	itemArraySQL := "SELECT coalesce(jsonb_agg(to_jsonb(items)), '[]'::jsonb) FROM (" + itemSQL + ") items"
	start, end := query.MetricsFilter.effectiveWindow()
	if query.Group == "summary" && end.Sub(start) > 24*time.Hour && canUseRequestDailySummary(query) {
		itemArraySQL = requestDailySummaryItemsSQL(scope, itemArraySQL)
	}
	if query.Group == "owner-directory" {
		args = append(args, query.Limit, query.Offset)
		itemArraySQL = `WITH directory AS MATERIALIZED (` + itemSQL + `)
		SELECT jsonb_build_array(jsonb_build_object(
			'total', (SELECT count(*) FROM directory),
			'users', (SELECT coalesce(jsonb_agg(to_jsonb(page)), '[]'::jsonb) FROM
			 (SELECT * FROM directory ORDER BY total_cost_usd DESC, total_requests DESC, key, tenant_id` +
			fmt.Sprintf(" LIMIT $%d OFFSET $%d", len(args)-1, len(args)) + `) page)))`
	}
	return requestStatsSQL(itemArraySQL), args, nil
}

func requestStatsSQL(itemArraySQL string) string {
	return `
WITH projection AS (
	SELECT backfill_complete, updated_at,
	       (SELECT enqueued_at FROM public.request_metrics_pending ORDER BY enqueued_at LIMIT 1) AS pending_since
	FROM public.request_metrics_projection_state WHERE singleton
)
SELECT backfill_complete, updated_at, pending_since,
	CASE WHEN backfill_complete THEN (` + itemArraySQL + `) ELSE '[]'::jsonb END
FROM projection`
}

// ReadRequestStats reads only the materialized projection and its progress.
// The window is applied to occurred_at before grouping by a display timezone.
func ReadRequestStats(ctx context.Context, db requestStatsDB, query RequestStatsQuery) (RequestStats, error) {
	result := RequestStats{
		Source: "request_metrics", Start: query.StartTime, End: query.EndTime, Timezone: query.Timezone, Group: query.Group,
	}
	start, end := query.MetricsFilter.effectiveWindow()
	usesDailySummary := query.Group == "summary" && canUseRequestDailySummary(query)
	if usesDailySummary && end.Sub(start) >= 24*time.Hour {
		clauses, args := buildFilterClauses(query.MetricsFilter, "", "occurred_at", kindTimestamp)
		sql := requestStatsSQL(requestCoveredSummaryItemsSQL(clauses[2:]))
		err := db.QueryRow(ctx, sql, args...).Scan(&result.Ready, &result.UpdatedAt, &result.PendingSince, &result.Items)
		if err != nil || result.Items != nil {
			return result, err
		}
	}
	sql, args, err := buildRequestStatsQuery(query)
	if err != nil {
		return result, err
	}
	err = readRequestStatsRow(ctx, db, sql, args, usesDailySummary && end.Sub(start) > 24*time.Hour,
		&result.Ready, &result.UpdatedAt, &result.PendingSince, &result.Items)
	return result, err
}

func readRequestStatsRow(ctx context.Context, db requestStatsDB, query string, args []any, withoutJIT bool, dest ...any) error {
	if withoutJIT {
		if beginner, ok := db.(interface {
			Begin(context.Context) (pgx.Tx, error)
		}); ok {
			tx, err := beginner.Begin(ctx)
			if err != nil {
				return err
			}
			// These mixed plans can spend more time compiling branches than
			// reading uncovered dates. Rollback restores the caller's setting.
			defer tx.Rollback(ctx)
			if _, err := tx.Exec(ctx, "SET LOCAL jit = off"); err != nil {
				return err
			}
			db = tx
		}
	}
	return db.QueryRow(ctx, query, args...).Scan(dest...)
}
