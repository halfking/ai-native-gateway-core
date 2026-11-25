package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type BoardQuery struct {
	MetricsFilter
	Owner         string
	Model         string
	Agent         string
	ProviderCode  string
	BucketMinutes int
}

type BoardItem struct {
	Key      string  `json:"key"`
	Requests int64   `json:"requests"`
	Tokens   int64   `json:"tokens"`
	Credits  int64   `json:"credits"`
	CostUSD  float64 `json:"cost_usd"`
}

type BoardPoint struct {
	Bucket   time.Time `json:"bucket"`
	Requests int64     `json:"requests"`
	Tokens   int64     `json:"tokens"`
	Credits  int64     `json:"credits"`
	CostUSD  float64   `json:"cost_usd"`
}

type BoardTrendPoint struct {
	BoardPoint
	Success      int64   `json:"success"`
	AvgLatencyMS float64 `json:"avg_latency_ms"`
}

type BoardModelPoint struct {
	Model string `json:"model"`
	BoardPoint
}

type RequestBoard struct {
	Ready        bool
	UpdatedAt    *time.Time
	PendingSince *time.Time
	Summary      json.RawMessage
	Pies         map[string][]BoardItem
	Trends       []BoardTrendPoint
	ModelPoints  []BoardModelPoint
	ProviderRows []json.RawMessage
}

const boardProjectionState = `
	SELECT backfill_complete, updated_at,
		(SELECT enqueued_at FROM public.request_metrics_pending_events ORDER BY enqueued_at LIMIT 1) AS pending_since
	FROM public.request_metrics_repair_state WHERE singleton`

const boardMeasures = `
	count(*) AS requests,
	coalesce(sum(total_tokens),0) AS tokens,
	coalesce(sum(credits_charged),0) AS credits,
	coalesce(sum(cost_amount) FILTER (WHERE cost_currency='USD'),0) AS cost_usd`

const boardError = "coalesce(nullif(error_kind,''), status)"
const boardModel = requestModelKey

const boardPieKeys = `tenant_id AS tenant_key, nullif(agent_name,'') AS agent_key, host(client_ip) AS ip_key,
		nullif(identity_hash,'') AS hash_key,
		CASE WHEN status <> 'success' THEN ` + boardError + ` END AS error_key
`

const boardPieGrouping = `CASE
		WHEN GROUPING(agent_key)=0 THEN 'clients'
		WHEN GROUPING(ip_key)=0 THEN 'client_ips'
		WHEN GROUPING(hash_key)=0 THEN 'identity_hashes'
		WHEN GROUPING(model)=0 THEN 'models'
		WHEN GROUPING(error_key)=0 THEN 'errors'
		WHEN GROUPING(status)=0 THEN 'statuses'
		ELSE 'tenants'
	END AS dimension,
	CASE
		WHEN GROUPING(agent_key)=0 THEN agent_key
		WHEN GROUPING(ip_key)=0 THEN ip_key
		WHEN GROUPING(hash_key)=0 THEN hash_key
		WHEN GROUPING(model)=0 THEN model
		WHEN GROUPING(error_key)=0 THEN error_key
		WHEN GROUPING(status)=0 THEN status
		ELSE tenant_key
	END AS key`

const boardPieSets = "(agent_key), (ip_key), (hash_key), (model), (error_key), (status), (tenant_key)"

func boardBucket(timestamp, size string) string {
	return "date_bin(make_interval(mins => " + size + "), " + timestamp + ", '2000-01-01 00:00:00+00')"
}

const boardRankedPies = `ranked_pies AS (
	SELECT *, row_number() OVER (PARTITION BY dimension ORDER BY requests DESC, key) AS rank
	FROM (
		SELECT * FROM pie_groups WHERE key IS NOT NULL
		UNION ALL
		SELECT 'providers', provider_code, request_count, total_tokens, credits, total_cost_usd FROM providers
	) groups
),
pies AS (
	SELECT dimension, jsonb_agg(jsonb_build_object(
		'key', key, 'requests', requests, 'tokens', tokens, 'credits', credits, 'cost_usd', cost_usd
	) ORDER BY rank) AS items
	FROM ranked_pies WHERE rank <= 25 GROUP BY dimension
)`

const boardResultSQL = `SELECT projection.backfill_complete, projection.updated_at, projection.pending_since, to_jsonb(summary),
	coalesce((SELECT jsonb_object_agg(dimension, items) FROM pies), '{}'::jsonb),
	` + boardTimeSeriesSQL + `,
	coalesce((SELECT jsonb_agg(to_jsonb(p) ORDER BY p.total_cost_usd DESC, p.provider_id)
		FROM (SELECT * FROM providers ORDER BY total_cost_usd DESC, provider_id LIMIT 50) p), '[]'::jsonb)
FROM projection CROSS JOIN summary`

func boardWhere(query BoardQuery) ([]string, []any) {
	clauses, args := buildFilterClauses(query.MetricsFilter, "", "occurred_at", kindTimestamp)
	clauses = append(clauses, "source_fact_id IS NOT NULL")
	for _, filter := range []struct{ key, value string }{
		{"owner_user", query.Owner}, {boardModel, query.Model}, {"agent_name", query.Agent}, {"provider_code", query.ProviderCode},
	} {
		if filter.value != "" {
			args = append(args, filter.value)
			clauses = append(clauses, fmt.Sprintf("%s = $%d", filter.key, len(args)))
		}
	}
	return clauses, args
}

// ReadRequestBoard calculates all request widgets in one database snapshot.
func ReadRequestBoard(ctx context.Context, db requestStatsDB, query BoardQuery) (RequestBoard, error) {
	var board RequestBoard
	switch query.BucketMinutes {
	case 5, 15, 60, 360:
	default:
		return board, fmt.Errorf("unsupported board bucket size")
	}
	start, end := query.MetricsFilter.effectiveWindow()
	if end.Sub(start) >= 24*time.Hour && !start.IsZero() && canUseRequestDailyScope(query.MetricsFilter) &&
		query.Owner == "" && query.Model == "" && query.Agent == "" && query.ProviderCode == "" {
		return readRequestBoardDaily(ctx, db, query)
	}
	return readRequestBoardDetail(ctx, db, query)
}

func readRequestBoardDetail(ctx context.Context, db requestStatsDB, query BoardQuery) (RequestBoard, error) {
	clauses, args := boardWhere(query)
	args = append(args, query.BucketMinutes)
	bucket := boardBucket("occurred_at", fmt.Sprintf("$%d::int", len(args)))
	sql := `
WITH projection AS (` + boardProjectionState + `),
bounded AS MATERIALIZED (
	SELECT occurred_at, tenant_id, api_key_id, session_id, provider_id, provider_code, provider_name,
		outbound_model, client_model, agent_name, client_ip, identity_hash, status, error_kind,
		prompt_tokens, completion_tokens, total_tokens, cached_tokens, cache_write_tokens, credits_charged,
		image_tokens, audio_tokens, video_tokens, cost_amount, cost_currency, usage_source,
		cache_input_tokens, latency_ms, ttft_ms,
		` + bucket + ` AS bucket, ` + boardModel + ` AS model,
		` + boardPieKeys + `
	FROM public.request_metrics_projection
	WHERE ` + strings.Join(clauses, " AND ") + `
	  AND (SELECT backfill_complete FROM projection)
),
summary AS (
	SELECT measured.*, unique_models AS active_models
	FROM (
		SELECT ` + statsWithGroupedMembership("bounded") + `,
			count(DISTINCT api_key_id) AS active_api_keys,
			count(DISTINCT provider_id) AS providers
		FROM bounded
	) measured
),
pie_groups AS (
	SELECT ` + boardPieGrouping + `,
	` + boardMeasures + `
	FROM bounded
	GROUP BY GROUPING SETS (` + boardPieSets + `)
),
providers AS (
	SELECT provider_id,
		coalesce(max(nullif(provider_code,'')), provider_id::text) AS provider_code,
		coalesce(max(nullif(provider_name,'')), max(nullif(provider_code,'')), provider_id::text) AS provider_name,
		count(*) AS request_count, coalesce(sum(prompt_tokens),0) AS prompt_tokens,
		coalesce(sum(completion_tokens),0) AS completion_tokens,
		coalesce(sum(total_tokens),0) AS total_tokens, coalesce(sum(credits_charged),0) AS credits,
		coalesce(sum(cost_amount) FILTER (WHERE cost_currency='USD'),0) AS total_cost_usd,
		count(*) FILTER (WHERE status='success')::float8 / count(*) AS success_rate
	FROM bounded
	WHERE provider_id IS NOT NULL
	GROUP BY provider_id
),
` + boardRankedPies + `,
top_models AS (
	SELECT key FROM pie_groups WHERE dimension='models'
	ORDER BY requests DESC, key LIMIT 6
),
chart_points AS (
	SELECT bucket,
		CASE WHEN model IN (SELECT key FROM top_models) THEN model ELSE '__others__' END AS series,
		` + boardMeasures + `,
		count(*) FILTER (WHERE status='success') AS success,
		sum(latency_ms) AS latency_sum, count(latency_ms) AS latency_count
	FROM bounded GROUP BY bucket, series
),
trends AS (
	SELECT bucket, sum(requests) AS requests, sum(tokens) AS tokens, sum(credits) AS credits, sum(cost_usd) AS cost_usd,
		sum(success) AS success, coalesce(sum(latency_sum)::float8/nullif(sum(latency_count),0),0) AS avg_latency_ms
	FROM chart_points GROUP BY bucket
)
` + boardResultSQL
	return scanRequestBoard(ctx, db, sql, args, false)
}

func scanRequestBoard(ctx context.Context, db requestStatsDB, sql string, args []any, withoutJIT bool) (RequestBoard, error) {
	var board RequestBoard
	var trends, models []byte
	err := readRequestStatsRow(ctx, db, sql, args, withoutJIT, &board.Ready, &board.UpdatedAt, &board.PendingSince,
		&board.Summary, &board.Pies, &trends, &models, &board.ProviderRows)
	if err != nil {
		return board, err
	}
	board.Trends, board.ModelPoints, err = decodeBoardTimeSeries(trends, models)
	return board, err
}

func ReadBoardErrorDrill(ctx context.Context, db requestStatsDB, query BoardQuery, kind, dimension string) ([]BoardItem, bool, error) {
	var key string
	switch dimension {
	case "model":
		key = boardModel
	case "provider":
		key = "coalesce(nullif(provider_code,''), provider_id::text, '__unknown__')"
	case "client":
		key = "coalesce(nullif(agent_name,''), '__unknown__')"
	default:
		return nil, false, fmt.Errorf("unsupported board drill dimension")
	}
	clauses, args := boardWhere(query)
	args = append(args, kind)
	clauses = append(clauses, "status <> 'success'", fmt.Sprintf("%s = $%d", boardError, len(args)))
	sql := `
WITH projection AS (` + boardProjectionState + `)
SELECT backfill_complete, CASE WHEN backfill_complete THEN (
	SELECT coalesce(jsonb_agg(to_jsonb(items) ORDER BY requests DESC, key), '[]'::jsonb)
	FROM (
		SELECT ` + key + ` AS key,` + boardMeasures + `
		FROM public.request_metrics_projection WHERE ` + strings.Join(clauses, " AND ") + `
		GROUP BY 1 ORDER BY requests DESC, key LIMIT 20
	) items
) ELSE '[]'::jsonb END
FROM projection`
	var items []BoardItem
	var ready bool
	err := db.QueryRow(ctx, sql, args...).Scan(&ready, &items)
	return items, ready, err
}
