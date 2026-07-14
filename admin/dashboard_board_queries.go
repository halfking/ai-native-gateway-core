package admin

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

func (h *Handler) shouldUseBoardLogsFallback(ctx context.Context, tenantID string, tr boardTimeRange) bool {
	if tr.Days <= 1 {
		return false
	}
	where, args := boardMinuteWhere(tr, tenantID, 0)
	var minBucket sql.NullTime
	err := h.db.QueryRow(ctx, `
		SELECT MIN(bucket) FROM request_stats_minute WHERE `+where, args...).Scan(&minBucket)
	if err != nil || !minBucket.Valid {
		return true
	}
	return minBucket.Time.After(tr.Start.Add(boardStatsCoverageSlack(tr)))
}

func trendPointsCoverRange(tr boardTimeRange, points []boardTrendPoint) bool {
	if len(points) == 0 {
		return false
	}
	earliest, err := time.Parse(time.RFC3339, points[0].Bucket)
	if err != nil {
		return false
	}
	return !earliest.After(tr.Start.Add(boardStatsCoverageSlack(tr)))
}

func (h *Handler) queryBoardSummary(ctx context.Context, tenantID string, tr boardTimeRange) (map[string]any, bool) {
	where, args := boardMinuteWhere(tr, tenantID, 0)

	var totalReq, successCnt, failCnt, promptTok, compTok, totalTok, credits, latencySum int64
	var costUSD float64
	err := h.db.QueryRow(ctx, `
		SELECT
			COALESCE(SUM(requests), 0),
			COALESCE(SUM(success_count), 0),
			COALESCE(SUM(failure_count), 0),
			COALESCE(SUM(prompt_tokens), 0),
			COALESCE(SUM(completion_tokens), 0),
			COALESCE(SUM(total_tokens), 0),
			COALESCE(SUM(credits_charged), 0),
			COALESCE(SUM(cost_usd), 0),
			COALESCE(SUM(latency_ms_sum), 0)
		FROM request_stats_minute
		WHERE `+where+`
	`, args...).Scan(
		&totalReq, &successCnt, &failCnt, &promptTok, &compTok, &totalTok, &credits, &costUSD, &latencySum,
	)
	if err != nil || totalReq == 0 {
		return nil, false
	}
	if h.shouldUseBoardLogsFallback(ctx, tenantID, tr) {
		return nil, false
	}

	var activeKeys, activeModels, providers int
	_ = h.queryOverviewCounts(ctx, tenantID, tr, &activeKeys, &activeModels, &providers)

	successRate := 0.0
	if totalReq > 0 {
		successRate = float64(successCnt) / float64(totalReq)
	}
	avgLatency := 0.0
	if totalReq > 0 {
		avgLatency = float64(latencySum) / float64(totalReq)
	}

	return map[string]any{
		"total_requests":            totalReq,
		"total_prompt_tokens":       promptTok,
		"total_completion_tokens":   compTok,
		"total_tokens":              totalTok,
		"total_cost_usd":            costUSD,
		"total_credits_charged":     credits,
		"success_rate":              successRate,
		"avg_latency_ms":            avgLatency,
		"active_api_keys":           activeKeys,
		"active_models":             activeModels,
		"providers":                 providers,
	}, true
}

func (h *Handler) fallbackBoardSummary(ctx context.Context, tenantID string, tr boardTimeRange) map[string]any {
	logsTable, alias := boardRequestLogsFromClause()
	where, args := boardLogsWhere(tr, alias, tenantID)

	var totalReq int64
	var promptTok, compTok sql.NullInt64
	var costUSD sql.NullFloat64
	var avgLatency sql.NullFloat64
	var successRate sql.NullFloat64
	_ = h.db.QueryRow(ctx, `
		SELECT COUNT(*),
			COALESCE(SUM(`+alias+`.prompt_tokens), 0),
			COALESCE(SUM(`+alias+`.completion_tokens), 0),
			COALESCE(SUM(`+alias+`.cost_usd), 0),
			COALESCE(AVG(`+alias+`.latency_ms) FILTER (WHERE `+alias+`.latency_ms IS NOT NULL), 0),
			COALESCE(AVG(CASE WHEN `+alias+`.success THEN 1.0 ELSE 0.0 END), 0)
		FROM `+logsTable+`
		WHERE `+where+` AND `+alias+`.request_status IN ('success', 'failure')
	`, args...).Scan(&totalReq, &promptTok, &compTok, &costUSD, &avgLatency, &successRate)

	credits := h.queryTotalCreditsCharged(ctx, tenantID, tr.Days)

	var activeKeys, activeModels, providers int
	_ = h.queryOverviewCounts(ctx, tenantID, tr, &activeKeys, &activeModels, &providers)

	return map[string]any{
		"total_requests":            totalReq,
		"total_prompt_tokens":       promptTok.Int64,
		"total_completion_tokens":   compTok.Int64,
		"total_tokens":              promptTok.Int64 + compTok.Int64,
		"total_cost_usd":            costUSD.Float64,
		"total_credits_charged":     credits,
		"success_rate":              successRate.Float64,
		"avg_latency_ms":            avgLatency.Float64,
		"active_api_keys":           activeKeys,
		"active_models":             activeModels,
		"providers":                 providers,
	}
}

func (h *Handler) queryOverviewCounts(ctx context.Context, tenantID string, tr boardTimeRange, keys, models, providers *int) error {
	if err := h.queryOverviewCountsMinute(ctx, tenantID, tr, keys, models, providers); err != nil {
		if !IsMissingRelationError(err) {
			return err
		}
	}
	return h.fillOverviewCountsFromLogs(ctx, tenantID, tr, keys, models, providers)
}

func (h *Handler) queryOverviewCountsMinute(ctx context.Context, tenantID string, tr boardTimeRange, keys, models, providers *int) error {
	if tenantID != "" {
		return h.db.QueryRow(ctx, `
			SELECT
				(SELECT COUNT(*) FROM api_keys WHERE tenant_id = $1 AND enabled = TRUE),
				(SELECT COUNT(DISTINCT dim_key) FROM request_stats_dim_minute
				 WHERE dim_type = 'model' AND tenant_id = $1 AND bucket >= $2 AND bucket < $3),
				(SELECT COUNT(DISTINCT dim_key) FROM request_stats_dim_minute
				 WHERE dim_type = 'provider' AND tenant_id = $1 AND bucket >= $2 AND bucket < $3)
		`, tenantID, tr.Start, tr.End).Scan(keys, models, providers)
	}
	return h.db.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM api_keys WHERE enabled = TRUE),
			(SELECT COUNT(DISTINCT dim_key) FROM request_stats_dim_minute
			 WHERE dim_type = 'model' AND bucket >= $1 AND bucket < $2),
			(SELECT COUNT(DISTINCT dim_key) FROM request_stats_dim_minute
			 WHERE dim_type = 'provider' AND bucket >= $1 AND bucket < $2)
	`, tr.Start, tr.End).Scan(keys, models, providers)
}

func (h *Handler) resolveBoardPies(ctx context.Context, tenantID string, tr boardTimeRange) (map[string]any, error) {
	pies, err := h.queryBoardPies(ctx, tenantID, tr)
	if err != nil {
		if IsMissingRelationError(err) {
			return h.fallbackBoardPies(ctx, tenantID, tr)
		}
		return nil, err
	}
	if h.shouldUseBoardLogsFallback(ctx, tenantID, tr) {
		fb, fbErr := h.fallbackBoardPies(ctx, tenantID, tr)
		if fbErr == nil {
			return fb, nil
		}
	}
	return pies, nil
}

func (h *Handler) queryBoardPies(ctx context.Context, tenantID string, tr boardTimeRange) (map[string]any, error) {
	types := map[string]string{
		"clients":         "client_profile",
		"virtual_ips":     "virtual_ip",
		"identity_hashes": "identity_hash",
		"models":          "model",
		"errors":          "error_kind",
		"tenants":         "tenant",
		"providers":       "provider",
	}
	out := make(map[string]any, len(types))
	for key, dimType := range types {
		items, err := h.queryDimPie(ctx, tenantID, tr, dimType)
		if err != nil {
			return nil, err
		}
		if dimType == "provider" {
			items = h.resolveProviderPieLabels(ctx, items)
		}
		out[key] = items
	}
	return out, nil
}

func (h *Handler) queryDimPie(ctx context.Context, tenantID string, tr boardTimeRange, dimType string) ([]boardPieItem, error) {
	args := []any{dimType, tr.Start, tr.End}
	where := "dim_type = $1 AND bucket >= $2 AND bucket < $3"
	if tenantID != "" {
		where += " AND tenant_id = $4"
		args = append(args, tenantID)
	}

	rows, err := h.db.Query(ctx, `
		SELECT dim_key,
			COALESCE(SUM(requests), 0),
			COALESCE(SUM(total_tokens), 0),
			COALESCE(SUM(credits_charged), 0),
			COALESCE(SUM(cost_usd), 0)
		FROM request_stats_dim_minute
		WHERE `+where+`
		GROUP BY dim_key
		ORDER BY SUM(requests) DESC
		LIMIT 25
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []boardPieItem
	for rows.Next() {
		var item boardPieItem
		if err := rows.Scan(&item.Key, &item.Requests, &item.Tokens, &item.Credits, &item.CostUSD); err != nil {
			continue
		}
		items = append(items, item)
	}
	return items, nil
}

func (h *Handler) resolveBoardTrends(ctx context.Context, tenantID string, tr boardTimeRange, providerID int64) ([]boardTrendPoint, error) {
	points, err := h.queryBoardTrends(ctx, tenantID, tr, providerID)
	if err != nil {
		if IsMissingRelationError(err) {
			return h.fallbackBoardTrends(ctx, tenantID, tr, providerID)
		}
		return nil, err
	}
	if trendPointsCoverRange(tr, points) {
		return points, nil
	}
	fallback, fbErr := h.fallbackBoardTrends(ctx, tenantID, tr, providerID)
	if fbErr != nil {
		if len(points) > 0 {
			return points, nil
		}
		return nil, fbErr
	}
	return fallback, nil
}

func (h *Handler) queryBoardTrends(ctx context.Context, tenantID string, tr boardTimeRange, providerID int64) ([]boardTrendPoint, error) {
	where, args := boardMinuteWhere(tr, tenantID, 0)
	if providerID > 0 {
		where += fmt.Sprintf(" AND provider_id = $%d", len(args)+1)
		args = append(args, providerID)
	}
	bucketExpr := sqlTrendBucket("bucket", tr.trendBucketMinutes())

	rows, err := h.db.Query(ctx, fmt.Sprintf(`
		SELECT %s,
			COALESCE(SUM(requests), 0),
			COALESCE(SUM(total_tokens), 0),
			COALESCE(SUM(credits_charged), 0),
			COALESCE(SUM(cost_usd), 0)
		FROM request_stats_minute
		WHERE %s
		GROUP BY 1
		ORDER BY 1 ASC
	`, bucketExpr, where), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var points []boardTrendPoint
	for rows.Next() {
		var p boardTrendPoint
		var bucket time.Time
		if err := rows.Scan(&bucket, &p.Requests, &p.Tokens, &p.Credits, &p.CostUSD); err != nil {
			continue
		}
		p.Bucket = bucket.UTC().Format(time.RFC3339)
		points = append(points, p)
	}
	return points, nil
}
