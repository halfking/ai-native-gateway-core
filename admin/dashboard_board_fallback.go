package admin

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/kaixuan/llm-gateway-go/maas"
)

func emptyBoardPies() map[string]any {
	return map[string]any{
		"clients":         []boardPieItem{},
		"client_ips":      []boardPieItem{},
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

func (h *Handler) fillOverviewCountsFromLogs(ctx context.Context, tenantID string, tr boardTimeRange, keys, models, providers *int) error {
	logsTable, alias := boardRequestLogsFromClause()
	where, args := boardLogsWhere(tr, alias, tenantID)
	where += ` AND ` + alias + `.request_status IN ('success', 'failure', 'rate_limited')`
	modelExpr := fmt.Sprintf(`COALESCE(NULLIF(%s.client_model, ''), NULLIF(%s.outbound_model, ''))`, alias, alias)
	var logModels, logProviders int
	err := h.db.QueryRow(ctx, fmt.Sprintf(`
		SELECT
			(SELECT COUNT(DISTINCT %s) FROM %s WHERE %s AND %s IS NOT NULL),
			(SELECT COUNT(DISTINCT %s.provider_id) FROM %s WHERE %s AND %s.provider_id IS NOT NULL)
	`, modelExpr, logsTable, where, modelExpr, alias, logsTable, where, alias), args...).Scan(&logModels, &logProviders)
	if err != nil {
		return err
	}
	if *models == 0 && logModels > 0 {
		*models = logModels
	}
	if *providers == 0 && logProviders > 0 {
		*providers = logProviders
	}
	return nil
}

func (h *Handler) fallbackBoardTrends(ctx context.Context, tenantID string, tr boardTimeRange, providerID int64) ([]boardTrendPoint, error) {
	logsTable, alias := boardRequestLogsFromClause()
	where, args := boardLogsWhere(tr, alias, tenantID)
	where += ` AND ` + alias + `.request_status IN ('success', 'failure', 'rate_limited')`
	if providerID > 0 {
		where += fmt.Sprintf(" AND %s.provider_id = $%d", alias, len(args)+1)
		args = append(args, providerID)
	}
	creditsExpr := maas.RequestLogCreditsSQL(alias, tenantID == "" || tenantID == "default")
	bucketExpr := sqlTrendBucket(alias+".ts", tr.trendBucketMinutes())

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
		ORDER BY 1 ASC
	`, bucketExpr, alias, alias, alias, alias, creditsExpr, alias, logsTable, where), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// R36：make(…,0)——空窗时序列化 [] 而非 null（R35 nil-slice 批同族，前端另有 ?? [] 兜底）。
	points := make([]boardTrendPoint, 0)
	for rows.Next() {
		var p boardTrendPoint
		var bucket time.Time
		if err := rows.Scan(&bucket, &p.Requests, &p.Tokens, &p.Credits, &p.CostUSD); err != nil {
			warnRowSkip("fallback board trends", err)
			continue
		}
		p.Bucket = bucket.UTC().Format(time.RFC3339)
		points = append(points, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return points, nil
}

// boardPieDegradation 记录哪些饼图维度没能算出来。
//
// 为什么需要它：2026-10-03 之前这里是一个 `err != nil → out[key] = []` 的静默吞错。
// 某个维度查询失败（最典型是 42P01：聚合视图未迁移）时，页面拿到的是一个
// **长度为零的数组**，与「这个维度真的没有任何客户端」在渲染上完全同形 ——
// 排行榜卡会照常画出一张空表，顶部分布条停在 0%。
//
// 与第 17 节 credits 那处同型：**降级本身站得住，但载荷不能假装它是真值。**
type boardPieDegradation struct {
	// Keys 是算不出来的饼图维度名（clients/errors/models/...）。
	Keys []string
	// MissingView 是缺失的视图名（42P01 时非空），供前端显示可操作的提示。
	MissingView string
}

func (d boardPieDegradation) Any() bool { return len(d.Keys) > 0 }

// fallbackBoardPies 在日志回退路径上算出各维度饼图。
//
// 第二个返回值是降级账本：以前每个维度失败都只写一个空数组，
// 整轮扫描下来「全部维度都缺」与「真的一个客户端都没有」产出完全相同的载荷。
func (h *Handler) fallbackBoardPies(ctx context.Context, tenantID string, tr boardTimeRange) (map[string]any, boardPieDegradation, error) {
	types := map[string]string{
		"clients":         "agent_name",
		"client_ips":      "client_ip",
		"identity_hashes": "identity_hash",
		"models":          "model",
		"errors":          "error_kind",
		"tenants":         "tenant",
		"providers":       "provider",
	}
	out := emptyBoardPies()
	var degraded boardPieDegradation
	for key, dimType := range types {
		items, err := h.fallbackDimPie(ctx, tenantID, tr, dimType)
		if err != nil {
			// 仍然发出空数组（前端那些 `?? []` 兜底与既有渲染路径不变），
			// 但把这一维度记进降级账本，让载荷能自报「这里不是 0，是没算出来」。
			out[key] = []boardPieItem{}
			degraded.Keys = append(degraded.Keys, key)
			if degraded.MissingView == "" {
				if IsMissingRelationError(err) {
					degraded.MissingView = ExtractMissingRelationName(err)
				}
			}
			continue
		}
		if dimType == "provider" {
			items = h.resolveProviderPieLabels(ctx, items)
		}
		if dimType == "client_ip" {
			items = classifyClientIPPie(items)
		}
		out[key] = items
	}
	// map 遍历顺序随机，报错与载荷的键序都必须稳定，否则「同一份降级」会有多种字节形态。
	sort.Strings(degraded.Keys)
	return out, degraded, nil
}

func (h *Handler) fallbackDimPie(ctx context.Context, tenantID string, tr boardTimeRange, dimType string) ([]boardPieItem, error) {
	logsTable, alias := boardRequestLogsFromClause()
	where, args := boardLogsWhere(tr, alias, tenantID)
	where += ` AND ` + alias + `.request_status IN ('success', 'failure', 'rate_limited')`

	groupExpr, onlyFailures := fallbackDimGroupExpr(alias, dimType)
	if onlyFailures {
		where += ` AND (` + alias + `.request_status = 'failure' OR (` + alias + `.request_status = '' AND (` + alias + `.success = FALSE OR COALESCE(` + alias + `.error_kind, '') <> '')))`
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

	items := make([]boardPieItem, 0)
	for rows.Next() {
		var item boardPieItem
		if err := rows.Scan(&item.Key, &item.Requests, &item.Tokens, &item.Credits, &item.CostUSD); err != nil {
			warnRowSkip("fallback dim pie", err)
			continue
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func fallbackDimGroupExpr(alias, dimType string) (expr string, onlyFailures bool) {
	unknown := "'__unknown__'"
	switch dimType {
	case "agent_name":
		// 2026-09-03 (audit closure): the dashboard 'clients' pie reads
		// from the agent_name column (canonical client-type identifier)
		// rather than client_profile (legacy device-fingerprint column).
		return fmt.Sprintf("COALESCE(NULLIF(%s.agent_name, ''), %s)", alias, unknown), false
	case "client_profile":
		// Legacy dim kept for backward-compat with operators comparing
		// old vs new tagging. Once enough history accumulates under
		// agent_name the live 'clients' path drops this case.
		return fmt.Sprintf("COALESCE(NULLIF(%s.client_profile, ''), %s)", alias, unknown), false
	case "client_ip":
		// R57 B7: real resolved client IP (740 view chain projection);
		// replaces the legacy virtual_ip pseudo dim on the board.
		return fmt.Sprintf("COALESCE(NULLIF(HOST(%s.client_ip), ''), %s)", alias, unknown), false
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

func (h *Handler) fallbackErrorDrill(
	ctx context.Context,
	tenantID string,
	days int,
	errorKind, dimension string,
) ([]boardPieItem, error) {
	logsTable, alias := requestLogsFromClause(days)
	where := alias + `.ts >= now() - ($1 * INTERVAL '1 day')`
	where += ` AND ` + alias + `.request_status IN ('success', 'failure', 'rate_limited')`
	where += ` AND COALESCE(` + alias + `.error_kind, '') = $2`
	args := []any{days, errorKind}
	if tenantID != "" {
		where += fmt.Sprintf(" AND %s.tenant_id = $%d", alias, len(args)+1)
		args = append(args, tenantID)
	}

	var groupExpr string
	switch dimension {
	case "provider":
		groupExpr = fmt.Sprintf("COALESCE(%s.provider_id::text, '__unknown__')", alias)
	// 2026-09-03 (audit closure): the error drill-down's "client" dimension
	// now prefers agent_name (canonical client-type identifier) and falls
	// back to client_profile for legacy rows that pre-date the rename.
	case "client", "client_profile", "agent_name":
		groupExpr = fmt.Sprintf("COALESCE(NULLIF(%s.agent_name, ''), COALESCE(NULLIF(%s.client_profile, ''), '__unknown__'))", alias, alias)
	default:
		groupExpr = fmt.Sprintf("COALESCE(NULLIF(%s.client_model, ''), NULLIF(%s.outbound_model, ''), '__unknown__')", alias, alias)
	}

	rows, err := h.db.Query(ctx, fmt.Sprintf(`
		SELECT %s, COUNT(*)::bigint, 0::bigint, 0::bigint, 0::float8
		FROM %s
		WHERE %s
		GROUP BY 1
		ORDER BY 2 DESC
		LIMIT 20
	`, groupExpr, logsTable, where), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]boardPieItem, 0)
	for rows.Next() {
		var item boardPieItem
		if err := rows.Scan(&item.Key, &item.Requests, &item.Tokens, &item.Credits, &item.CostUSD); err != nil {
			warnRowSkip("fallback error drill", err)
			continue
		}
		items = append(items, item)
	}
	if dimension == "provider" {
		items = h.resolveProviderPieLabels(ctx, items)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}
