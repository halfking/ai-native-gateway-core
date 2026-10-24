package admin

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/kaixuan/llm-gateway-go/bg"
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

// fillOverviewCountsFromLogs 补齐看板的「活跃模型数 / 活跃供应商数」。
//
// 2026-10-21（§143.36 / P12）：**从「两个标量子查询」改成「单遍一次扫描」**。
//
// ── 改的是什么 ────────────────────────────────────────────────────────
// 改前（本函数的旧形态，仓内逐字同构于生产 pg_stat_statements 的那条语句）：
//
//	SELECT
//	  (SELECT COUNT(DISTINCT modelExpr)  FROM <115列投影> WHERE <where> AND modelExpr IS NOT NULL),
//	  (SELECT COUNT(DISTINCT r.provider_id) FROM <115列投影> WHERE <where> AND r.provider_id IS NOT NULL)
//
// 两个标量子查询 ⇒ **同一份派生表被独立扫描两遍**（每遍 = hot 腿 + parent 腿
// 的 UNION ALL + 118 列投影 + details LEFT JOIN）。
//
// 改后：
//
//	SELECT COUNT(DISTINCT modelExpr), COUNT(DISTINCT r.provider_id)
//	  FROM <115列投影> WHERE <where>
//
// ── 为什么语义【逐值】不变（不是「大概等价」）────────────────────────────
// ★ 关键：`COUNT(DISTINCT x)` **按定义忽略 NULL**（SQL 标准：COUNT 只计非 NULL）。
//
//	⇒ 改前那两条 `... AND modelExpr IS NOT NULL` / `... AND provider_id IS NOT NULL`
//	  都是 **no-op**：它们只是把本来就不会被计入的行提前滤掉，不改变去重集合。
//	⇒ 改后（无这两臂）与改前（有此两臂）**逐值相等**，不是「近似相等」。
//	⇒ 实测印证（生产只读，两组窗口）：
//	   30 天窗·无租户：改前 (731, 15) == 改后 (731, 15)
//	   1 天窗·default：改前 (60, 6)   == 改后 (60, 6)
//
// ── 为什么值得改（生产只读实测，§143.36.5）────────────────────────────
// 同窗口同租户，EXPLAIN (ANALYZE, BUFFERS) 的 buffers：
//
//	形态                         buffers        相对现状    执行时间
//	改前（双子查询）              2,827,731        100.0%    37,328 ms
//	改后（单遍）                1,426,254         50.4%     4,587 ms
//
// ⇒ **恰好减半**（两次独立扫描 → 一次），执行时间 −87.7%。
// ⇒ 改前那条在 30 天窗上 **37.3 s 撞满 30 s statement_timeout**（= §143.26.3
//
//	记的「2 天 69 次被 statement timeout 杀」的物理原因）；改后 4.6 s 不会被杀。
//
// ⚠ 反直觉的一条（别再走错路）：★ **把 115 列投影裁成 9 列【零收益】**
//
//	（buffers 100.0% / 100.9%，实测）⇒ PostgreSQL 的 planner 已经把未引用的
//	投影列裁掉了（plan 里 width 从 700 降到 40）⇒ **根因是「扫两遍」，
//	不是「列太宽」**。⇒ 裁剪投影这条路已由实测排除，不要再做。
//
// ⚠ 边界：单遍形态的 WHERE **不含**那两个 IS NOT NULL 臂。若将来要把
//
//	聚合换成 SUM(…)/AVG(…)/COUNT(*)（这些对 NULL 敏感），必须把 IS NOT NULL
//	臂按需加回 —— COUNT(DISTINCT) 的免检许可**不可外推**。
func (h *Handler) fillOverviewCountsFromLogs(ctx context.Context, tenantID string, tr boardTimeRange, keys, models, providers *int) error {
	logsTable, alias := boardRequestLogsFromClause()
	where, args := boardLogsWhere(tr, alias, tenantID)
	where += ` AND ` + alias + `.request_status IN ('success', 'failure', 'rate_limited')`
	modelExpr := fmt.Sprintf(`COALESCE(NULLIF(%s.client_model, ''), NULLIF(%s.outbound_model, ''))`, alias, alias)
	var logModels, logProviders int
	err := h.db.QueryRow(ctx, fmt.Sprintf(`
		SELECT COUNT(DISTINCT %s), COUNT(DISTINCT %s.provider_id)
		FROM %s
		WHERE %s
	`, modelExpr, alias, logsTable, where), args...).Scan(&logModels, &logProviders)
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
	// 探针排除：错误钻取是看板的读面，与饼图/趋势/汇总必须同口径。
	// 不加这条，分钟路径（已按探针排除写入 request_stats_error_drill_minute）
	// 会报 0 条而 fallback 仍返回探针错误，运维点进错误饼图看到的是探针噪声。
	// requestLogsFromClause 可能是物理表也可能是视图，但两条分支都有
	// quality_flags / task_type / origin_actor 三列，故视图版谓词两边都成立。
	where += " AND " + fmt.Sprintf(bg.ProbeTrafficExclusionPredicateView, alias, alias, alias)
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
