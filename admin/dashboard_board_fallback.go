package admin

import (
	"context"
	"fmt"
	"time"

	"github.com/kaixuan/llm-gateway-go/maas"
)

func emptyBoardPies() map[string]any {
	return map[string]any{
		"clients":         []boardPieItem{},
		"virtual_ips":     []boardPieItem{},
		"identity_hashes": []boardPieItem{},
		"models":          []boardPieItem{},
		"errors":          []boardPieItem{},
		"tenants":         []boardPieItem{},
		"providers":       []boardPieItem{},
	}
}

func boardPiesAllEmpty(pies map[string]any) bool {
	if len(pies) == 0 {
		return true
	}
	for _, v := range pies {
		items, ok := v.([]boardPieItem)
		if ok && len(items) > 0 {
			return false
		}
	}
	return true
}

func (h *Handler) fallbackBoardPies(ctx context.Context, tenantID string, days int) (map[string]any, error) {
	types := map[string]string{
		"clients":         "client_profile",
		"virtual_ips":     "virtual_ip",
		"identity_hashes": "identity_hash",
		"models":          "model",
		"errors":          "error_kind",
		"tenants":         "tenant",
		"providers":       "provider",
	}
	out := emptyBoardPies()
	for key, dimType := range types {
		items, err := h.fallbackDimPie(ctx, tenantID, days, dimType)
		if err != nil {
			out[key] = []boardPieItem{}
			continue
		}
		out[key] = items
	}
	return out, nil
}

func (h *Handler) fallbackDimPie(ctx context.Context, tenantID string, days int, dimType string) ([]boardPieItem, error) {
	logsTable, alias := requestLogsFromClause(days)
	where := alias + `.ts >= now() - ($1 * INTERVAL '1 day')`
	where += ` AND ` + alias + `.request_status IN ('success', 'failure')`
	args := []any{days}
	if tenantID != "" {
		where += fmt.Sprintf(" AND %s.tenant_id = $%d", alias, len(args)+1)
		args = append(args, tenantID)
	}

	groupExpr, onlyFailures := fallbackDimGroupExpr(alias, dimType)
	if onlyFailures {
		where += ` AND (` + alias + `.success = FALSE OR COALESCE(` + alias + `.error_kind, '') <> '')`
	}

	creditsExpr := maas.RequestLogCreditsSQL(alias, tenantID == "" || tenantID == "default")
	rows, err := h.db.Query(ctx, fmt.Sprintf(`
		SELECT %s,
			COUNT(*)::bigint,
			COALESCE(SUM(COALESCE(%s.prompt_tokens, 0) + COALESCE(%s.completion_tokens, 0)
				+ COALESCE(%s.cache_read_tokens, 0) + COALESCE(%s.cache_write_tokens, 0)), 0)::bigint,
			COALESCE(SUM(%s), 0)::bigint,
			COALESCE(SUM(%s.cost_usd), 0)::float8
		FROM %s
		WHERE %s
		GROUP BY 1
		ORDER BY 2 DESC
		LIMIT 25
	`, groupExpr, alias, alias, alias, alias, creditsExpr, alias, logsTable, where), args...)
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

func fallbackDimGroupExpr(alias, dimType string) (expr string, onlyFailures bool) {
	unknown := "'__unknown__'"
	switch dimType {
	case "client_profile":
		return fmt.Sprintf("COALESCE(NULLIF(%s.client_profile, ''), %s)", alias, unknown), false
	case "virtual_ip":
		return fmt.Sprintf("COALESCE(NULLIF(%s.virtual_ip, ''), %s)", alias, unknown), false
	case "identity_hash":
		return fmt.Sprintf("COALESCE(NULLIF(%s.identity_hash, ''), %s)", alias, unknown), false
	case "model":
		return fmt.Sprintf("COALESCE(NULLIF(%s.client_model, ''), NULLIF(%s.outbound_model, ''), %s)", alias, alias, unknown), false
	case "error_kind":
		return fmt.Sprintf("COALESCE(NULLIF(%s.error_kind, ''), %s)", alias, unknown), true
	case "tenant":
		return fmt.Sprintf("COALESCE(NULLIF(%s.tenant_id, ''), %s)", alias, unknown), false
	case "provider":
		return fmt.Sprintf("COALESCE(%s.provider_id::text, %s)", alias, unknown), false
	default:
		return unknown, false
	}
}

func (h *Handler) fallbackBoardTrends(ctx context.Context, tenantID string, days int, providerID int64) ([]boardTrendPoint, error) {
	logsTable, alias := requestLogsFromClause(days)
	where := alias + `.ts >= now() - ($1 * INTERVAL '1 day')`
	where += ` AND ` + alias + `.request_status IN ('success', 'failure')`
	args := []any{days}
	if tenantID != "" {
		where += fmt.Sprintf(" AND %s.tenant_id = $%d", alias, len(args)+1)
		args = append(args, tenantID)
	}
	if providerID > 0 {
		where += fmt.Sprintf(" AND %s.provider_id = $%d", alias, len(args)+1)
		args = append(args, providerID)
	}
	creditsExpr := maas.RequestLogCreditsSQL(alias, tenantID == "" || tenantID == "default")

	rows, err := h.db.Query(ctx, fmt.Sprintf(`
		SELECT date_trunc('minute', %s.ts),
			COUNT(*)::bigint,
			COALESCE(SUM(COALESCE(%s.prompt_tokens, 0) + COALESCE(%s.completion_tokens, 0)
				+ COALESCE(%s.cache_read_tokens, 0) + COALESCE(%s.cache_write_tokens, 0)), 0)::bigint,
			COALESCE(SUM(%s), 0)::bigint,
			COALESCE(SUM(%s.cost_usd), 0)::float8
		FROM %s
		WHERE %s
		GROUP BY 1
		ORDER BY 1 ASC
	`, alias, alias, alias, alias, alias, creditsExpr, alias, logsTable, where), args...)
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
