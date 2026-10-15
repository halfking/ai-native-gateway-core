package admin

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	dbpkg "github.com/kaixuan/llm-gateway-go/db"
)

// dataLifecycleCompressedExists 是「这一轮带出站正文」的跨族判据，供带租户
// 过滤的存储增长趋势用（S4 停写轮新增）。
//
// # 为什么这里是 EXISTS 而不是 JOIN
//
// 原形态是 `LEFT JOIN request_logs_bodies_with_current_month rb` +
// `COUNT(DISTINCT rb.request_id) FILTER (WHERE rb.outbound_body IS NOT NULL)`。
// 它统计的是「有多少轮带出站正文」——一个**存在性**问题，所以 EXISTS 就够，
// 而且不受镜像双生影响（本地实测 11,711 个 request_id 同时存在于两个家族：
// JOIN 形态必须额外去重，EXISTS 天然免疫）。
//
// ⚠ 与 `dataLifecycleBodiesBytesSource`（blobs 那侧）**不能互换**：那边要
// `pg_column_size` 的列值，只能 JOIN + `DISTINCT ON`；这边只要「有没有」。
// 两处形态不同不是不一致，是各自的最省形态。
var dataLifecycleCompressedExists = `(EXISTS (SELECT 1 FROM request_logs_bodies_with_current_month v
		      WHERE v.request_id = rl.request_id AND v.outbound_body IS NOT NULL)
		 OR EXISTS (SELECT 1 FROM ` + dbpkg.SessionFamilyBodiesSourceSQL() + ` sb
		      WHERE sb.request_id = rl.request_id AND ` + dbpkg.SessionBodiesHasOutboundPredicate("sb") + `))`

// Data lifecycle stats for UI display
type dataLifecycleStats struct {
	TotalRows      int64             `json:"total_rows"`
	TotalSizeBytes int64             `json:"total_size_bytes"`
	TotalSizeHuman string            `json:"total_size_human"`
	HotData        *dataSegment      `json:"hot_data"`     // 0-7 days
	WarmData       *dataSegment      `json:"warm_data"`    // 7-30 days
	ColdData       *dataSegment      `json:"cold_data"`    // 30-90 days
	ExpiredData    *dataSegment      `json:"expired_data"` // >90 days
	ByTenant       []tenantDataStats `json:"by_tenant"`
	GrowthTrend    []dailyGrowth     `json:"growth_trend"`
}

type dataSegment struct {
	Rows           int64   `json:"rows"`
	SizeBytes      int64   `json:"size_bytes"`
	SizeHuman      string  `json:"size_human"`
	Days           int     `json:"days"`
	PercentOfTotal float64 `json:"percent_of_total"`
}

type tenantDataStats struct {
	TenantID  string `json:"tenant_id"`
	Rows      int64  `json:"rows"`
	SizeBytes int64  `json:"size_bytes"`
	SizeHuman string `json:"size_human"`
}

type dailyGrowth struct {
	Date            string  `json:"date"`
	Requests        int64   `json:"requests"`
	Compressed      int64   `json:"compressed"`
	CompressionRate float64 `json:"compression_rate"`
}

// GET /api/admin/data-lifecycle/stats
// dataLifecycleTrendSQL 提为包级函数，理由与 compressionSessionsMainSQL 相同：
// 判据必须与被测逻辑同层——这段字符串就是发给 PostgreSQL 的文本，而 pgx
// 不解析中间形态。留在 handler 里就只能靠打端点来断言，那是另一种量具。
//
// 无租户过滤那一支把 bodies 的 7 天 `compressed` 计数改成**跨族 + 冻结点切分**
// （S4 停写轮）：真库实测低报 **68%**（7,076 → 21,431）——面板上「压缩率」
// 一栏在停写后几乎空掉，而压缩恰恰在停写后仍在发生。
// 切分用 `db.V1FreezeCutoffSQL` 而不是只靠 `COUNT(DISTINCT request_id)`
// 去重：后者能消掉同一 request_id 的双计，但**消不掉跨午夜的日桶分裂**
// （镜像两侧 ts 差 ~15s，跨午夜时同一轮会落进两个 day，各计一次）。
func dataLifecycleTrendSQL(tenantFilter string) string {
	if tenantFilter == "" {
		return `
		WITH trend AS (
			SELECT DATE(ts) AS day, COUNT(*) AS requests
			FROM request_logs_with_current_month
			WHERE ts > NOW() - INTERVAL '7 days'
			GROUP BY 1
		),
		bodies AS (
			SELECT DATE(ts) AS day, COUNT(DISTINCT request_id) AS compressed
			FROM (
				-- v1 腿：冻结点及之前（停写前的历史）。v1 用 SQL NULL 表示
				-- 「没有出站正文」（实测 jsonb-null 0 行），故裸 IS NOT NULL 即可。
				SELECT request_id, ts, (outbound_body IS NOT NULL) AS has_outbound
				  FROM request_logs_bodies_with_current_month
				 WHERE ts <= ` + dbpkg.V1FreezeCutoffSQL + `
				UNION ALL
				-- 会话腿：冻结点之后（停写后的新流量）。★ 这里**必须**走
				-- SessionBodiesHasOutboundPredicate：会话族用 jsonb null 表示
				-- 「没有正文」（实测 11,420 行），裸 IS NOT NULL 恒真 ⇒ 会把
				-- 当天**每一个** turn 都算成压缩过（实测 993 vs 真实 250）。
				SELECT request_id, ts, (` + dbpkg.SessionBodiesHasOutboundPredicate("s") + `) AS has_outbound
				  FROM public.session_bodies_hot s
				 WHERE s.ts > ` + dbpkg.V1FreezeCutoffSQL + `
				UNION ALL
				SELECT request_id, ts, (` + dbpkg.SessionBodiesHasOutboundPredicate("s") + `) AS has_outbound
				  FROM public.session_bodies s
				 WHERE s.ts > ` + dbpkg.V1FreezeCutoffSQL + `
			) u
			WHERE (has_outbound)
			  AND ts > NOW() - INTERVAL '7 days'
			GROUP BY 1
		)
		SELECT t.day, t.requests, COALESCE(b.compressed, 0) AS compressed
		FROM trend t
		LEFT JOIN bodies b ON b.day = t.day
		ORDER BY t.day DESC
		LIMIT 7
	`
	}
	return `
		SELECT
			DATE(rl.ts) AS day,
			COUNT(*) AS requests,
			COUNT(DISTINCT rl.request_id) FILTER (WHERE ` + dataLifecycleCompressedExists + `) AS compressed
		FROM request_logs_with_current_month rl
		WHERE rl.ts > NOW() - INTERVAL '7 days'` + tenantFilter + `
		GROUP BY DATE(rl.ts)
		ORDER BY day DESC
		LIMIT 7
	`
}

func (h *Handler) handleDataLifecycleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	isTenantAdmin := IsTenantAdmin(r)
	var tenantID string
	var tenantFilter string
	if isTenantAdmin {
		tenantID = GetTenantID(r)
		tenantFilter = " AND tenant_id = '" + tenantID + "'"
	}

	stats := dataLifecycleStats{
		ByTenant:    make([]tenantDataStats, 0),
		GrowthTrend: make([]dailyGrowth, 0),
	}

	// 1. Overall stats
	var totalSize int64
	var totalSizeStr string
	query := `
		SELECT 
			COUNT(*) AS total_rows,
			pg_total_relation_size('_request_logs') AS total_size,
			pg_size_pretty(pg_total_relation_size('_request_logs')) AS total_size_human
		FROM request_logs_with_current_month
		WHERE 1=1` + tenantFilter
	err := h.db.QueryRow(ctx, query).Scan(&stats.TotalRows, &totalSize, &totalSizeStr)
	if err != nil {
		slog.Warn("data_lifecycle_stats total failed", "error", err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	stats.TotalSizeBytes = totalSize
	stats.TotalSizeHuman = totalSizeStr

	// 2. Segment stats (hot/warm/cold/expired)
	type segmentRow struct {
		Segment   string
		Rows      int64
		SizeBytes int64
		SizeHuman string
	}

	segmentQuery := `
		WITH total AS (
			SELECT COUNT(*) AS total_count FROM request_logs_with_current_month WHERE 1=1` + tenantFilter + `
		),
		segments AS (
			SELECT 
				'hot' AS segment,
				COUNT(*) AS rows,
				(pg_total_relation_size('_request_logs') * COUNT(*)::numeric / NULLIF((SELECT total_count FROM total), 0))::bigint AS size_bytes,
				pg_size_pretty((pg_total_relation_size('_request_logs') * COUNT(*)::numeric / NULLIF((SELECT total_count FROM total), 0))::bigint) AS size_human
			FROM request_logs_with_current_month
			WHERE ts > NOW() - INTERVAL '7 days'` + tenantFilter + `
			UNION ALL
			SELECT 
				'warm',
				COUNT(*),
				(pg_total_relation_size('_request_logs') * COUNT(*)::numeric / NULLIF((SELECT total_count FROM total), 0))::bigint,
				pg_size_pretty((pg_total_relation_size('_request_logs') * COUNT(*)::numeric / NULLIF((SELECT total_count FROM total), 0))::bigint)
			FROM request_logs_with_current_month
			WHERE ts BETWEEN NOW() - INTERVAL '30 days' AND NOW() - INTERVAL '7 days'` + tenantFilter + `
			UNION ALL
			SELECT 
				'cold',
				COUNT(*),
				(pg_total_relation_size('_request_logs') * COUNT(*)::numeric / NULLIF((SELECT total_count FROM total), 0))::bigint,
				pg_size_pretty((pg_total_relation_size('_request_logs') * COUNT(*)::numeric / NULLIF((SELECT total_count FROM total), 0))::bigint)
			FROM request_logs_with_current_month
			WHERE ts BETWEEN NOW() - INTERVAL '90 days' AND NOW() - INTERVAL '30 days'` + tenantFilter + `
			UNION ALL
			SELECT 
				'expired',
				COUNT(*),
				(pg_total_relation_size('_request_logs') * COUNT(*)::numeric / NULLIF((SELECT total_count FROM total), 0))::bigint,
				pg_size_pretty((pg_total_relation_size('_request_logs') * COUNT(*)::numeric / NULLIF((SELECT total_count FROM total), 0))::bigint)
			FROM request_logs_with_current_month
			WHERE ts < NOW() - INTERVAL '90 days'` + tenantFilter + `
		)
		SELECT segment, rows, size_bytes, size_human FROM segments
	`
	segmentRows, err := h.db.Query(ctx, segmentQuery)
	if err != nil {
		slog.Warn("data_lifecycle_stats segments failed", "error", err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer segmentRows.Close()

	for segmentRows.Next() {
		var sr segmentRow
		if err := segmentRows.Scan(&sr.Segment, &sr.Rows, &sr.SizeBytes, &sr.SizeHuman); err != nil {
			warnRowSkip("dataLifecycle.stats.segments", err)
			continue
		}

		pct := 0.0
		if stats.TotalRows > 0 {
			pct = float64(sr.Rows) / float64(stats.TotalRows) * 100
		}

		seg := &dataSegment{
			Rows:           sr.Rows,
			SizeBytes:      sr.SizeBytes,
			SizeHuman:      sr.SizeHuman,
			PercentOfTotal: pct,
		}

		switch sr.Segment {
		case "hot":
			seg.Days = 7
			stats.HotData = seg
		case "warm":
			seg.Days = 23
			stats.WarmData = seg
		case "cold":
			seg.Days = 60
			stats.ColdData = seg
		case "expired":
			seg.Days = 999
			stats.ExpiredData = seg
		}
	}
	// 段位缺失（hot/warm/cold/expired 任一为空）会让 stats 报出错误的
	// 总量结构，迭代中断必须按查询侧同语义收口，不能静默 200。
	if writeAggRowsErr(w, "dataLifecycle.stats.segments", segmentRows.Err()) {
		return
	}

	// 3. By tenant (top 10)
	tenantQuery := `
		SELECT 
			COALESCE(tenant_id, 'default') AS tenant,
			COUNT(*) AS rows,
			(pg_total_relation_size('_request_logs') * COUNT(*)::numeric / NULLIF((SELECT COUNT(*) FROM request_logs_with_current_month WHERE 1=1` + tenantFilter + `), 0))::bigint AS size_bytes,
			pg_size_pretty((pg_total_relation_size('_request_logs') * COUNT(*)::numeric / NULLIF((SELECT COUNT(*) FROM request_logs_with_current_month WHERE 1=1` + tenantFilter + `), 0))::bigint) AS size_human
		FROM request_logs_with_current_month
		WHERE 1=1` + tenantFilter + `
		GROUP BY tenant_id
		ORDER BY rows DESC
		LIMIT 10
	`
	tenantRows, err := h.db.Query(ctx, tenantQuery)
	if err != nil {
		slog.Warn("data_lifecycle_stats tenant failed", "error", err)
		// non-fatal, continue
	} else {
		defer tenantRows.Close()
		for tenantRows.Next() {
			var ts tenantDataStats
			if err := tenantRows.Scan(&ts.TenantID, &ts.Rows, &ts.SizeBytes, &ts.SizeHuman); err != nil {
				warnRowSkip("dataLifecycle.stats.byTenant", err)
				continue
			}
			stats.ByTenant = append(stats.ByTenant, ts)
		}
		// 与上面的查询失败同语义：非致命，但必须留痕——否则 ByTenant 少
		// 租户无人知晓，租户计费/容量归属看板会给出偏小的排名。
		if err := tenantRows.Err(); err != nil {
			slog.Warn("data_lifecycle_stats byTenant rows iteration aborted; report truncated", "error", err)
		}
	}

	// 4. Growth trend (last 7 days)
	// 2026-09-14: outbound_body left the main table/view in the Phase-1
	// body-storage split (it lives in request_logs_bodies_* now). The
	// bodies view has no tenant_id, so: tenant-scoped callers take the
	// row-by-row LEFT JOIN (correct but heavy, guarded by the request
	// timeout); the unscoped path pre-aggregates both sides by day first —
	// a per-row join over a week of _request_logs × bodies blows the admin
	// time budget (measured 30s timeout on the local 100k-row dataset).
	trendQuery := dataLifecycleTrendSQL(tenantFilter)
	trendRows, err := h.db.Query(ctx, trendQuery)
	if err != nil {
		slog.Warn("data_lifecycle_stats trend failed", "error", err)
		// non-fatal
	} else {
		defer trendRows.Close()
		for trendRows.Next() {
			var dg dailyGrowth
			var day time.Time
			if err := trendRows.Scan(&day, &dg.Requests, &dg.Compressed); err != nil {
				warnRowSkip("dataLifecycle.stats.growthTrend", err)
				continue
			}
			dg.Date = day.Format("2006-01-02")
			if dg.Requests > 0 {
				// bodies 的 ts 是落库时间，Phase-1 回填行与主表 ts 可能不同日，
				// 单日 compressed 可能超过 requests —— 展示层夹到 100%。
				dg.CompressionRate = float64(dg.Compressed) / float64(dg.Requests) * 100
				if dg.CompressionRate > 100 {
					dg.CompressionRate = 100
				}
			}
			stats.GrowthTrend = append(stats.GrowthTrend, dg)
		}
		// 与查询失败同语义：非致命，但必须留痕——趋势图少一天就是压缩率
		// 看板给出错误的容量回收结论。
		if err := trendRows.Err(); err != nil {
			slog.Warn("data_lifecycle_stats growthTrend rows iteration aborted; report truncated", "error", err)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	//nolint:errcheck // HTTP write error non-recoverable
	json.NewEncoder(w).Encode(stats)
}

// POST /api/admin/data-lifecycle/cleanup/preview
type cleanupPreviewRequest struct {
	Action string `json:"action"` // "trim" | "archive" | "delete"
	From   string `json:"from"`   // YYYY-MM-DD
	To     string `json:"to"`     // YYYY-MM-DD
}

type cleanupPreviewResponse struct {
	AffectedRows        int64  `json:"affected_rows"`
	EstimatedFreedBytes int64  `json:"estimated_freed_bytes"`
	EstimatedFreedHuman string `json:"estimated_freed_human"`
	WarningMessage      string `json:"warning_message,omitempty"`
}

func (h *Handler) handleDataLifecycleCleanupPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req cleanupPreviewRequest
	if err := readJSONRequired(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}

	if req.Action != "trim" && req.Action != "archive" && req.Action != "delete" {
		writeError(w, http.StatusBadRequest, "invalid action")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	isTenantAdmin := IsTenantAdmin(r)

	// Build where clause with parameterized queries
	whereClause := "WHERE 1=1"
	args := []interface{}{}
	argIdx := 1

	if isTenantAdmin {
		whereClause += " AND tenant_id = $" + strconv.Itoa(argIdx)
		args = append(args, GetTenantID(r))
		argIdx++
	}

	if req.From != "" {
		whereClause += " AND ts >= $" + strconv.Itoa(argIdx) + "::date"
		args = append(args, req.From)
		argIdx++
	}
	if req.To != "" {
		whereClause += " AND ts < $" + strconv.Itoa(argIdx) + "::date + INTERVAL '1 day'"
		args = append(args, req.To)
	}

	var resp cleanupPreviewResponse
	err := h.db.QueryRow(ctx, `
		SELECT 
			COUNT(*) AS affected_rows,
			(pg_total_relation_size('_request_logs') * COUNT(*)::numeric / NULLIF((SELECT COUNT(*) FROM _request_logs), 0))::bigint AS freed_bytes,
			pg_size_pretty((pg_total_relation_size('_request_logs') * COUNT(*)::numeric / NULLIF((SELECT COUNT(*) FROM _request_logs), 0))::bigint) AS freed_human
		FROM request_logs_with_current_month
		`+whereClause, args...).Scan(&resp.AffectedRows, &resp.EstimatedFreedBytes, &resp.EstimatedFreedHuman)

	if err != nil {
		slog.Warn("cleanup_preview failed", "error", err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}

	if resp.AffectedRows == 0 {
		resp.WarningMessage = "没有数据需要清理"
	} else if resp.AffectedRows > 1000000 {
		resp.WarningMessage = "影响行数超过 100 万，建议分批执行"
	}

	w.Header().Set("Content-Type", "application/json")
	//nolint:errcheck // HTTP write error non-recoverable
	json.NewEncoder(w).Encode(resp)
}
