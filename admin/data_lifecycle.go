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

// dataLifecycleCompressedExists 是「这一轮带出站正文」的判据，供带租户
// 过滤的存储增长趋势用（S4 停写轮新增）。
//
// # 为什么这里是 EXISTS 而不是 JOIN
//
// 原形态是把 v1 正文视图（request_logs_bodies_with_current_month）
// LEFT JOIN 进来 + `COUNT(DISTINCT rb.request_id) FILTER (WHERE rb.outbound_body IS NOT NULL)`。
// 它统计的是「有多少轮带出站正文」——一个**存在性**问题，所以 EXISTS 就够，
// 而且不受镜像双生影响（本地实测 11,711 个 request_id 同时存在于两个家族：
// JOIN 形态必须额外去重，EXISTS 天然免疫）。
//
// ⚠ 与 `dataLifecycleBodiesBytesSource`（blobs 那侧）**不能互换**：那边要
// `pg_column_size` 的列值，只能 JOIN + `DISTINCT ON`；这边只要「有没有」。
// 两处形态不同不是不一致，是各自的最省形态。
//
// 2026-10-20（迁移 890）：v1 族整体退役 ⇒ 跨族的 v1 腿删除，只剩会话族
// 一条腿。会话族的「无正文」是 jsonb null 而不是 SQL NULL，所以判据用
// db.SessionBodiesHasOutboundPredicate（IS NOT NULL **且** <> 'null'::jsonb），
// 裸 IS NOT NULL 在会话族上是恒真（会把当天每一个 turn 都算成压缩过）。
var dataLifecycleCompressedExists = `(EXISTS (SELECT 1 FROM ` + dbpkg.SessionFamilyBodiesSourceSQL() + ` sb
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

// dataLifecycleCanonicalSourceSQL 是 request_logs_with_current_month 视图
// 去掉列膨胀之后的等价源。⚠ 这不是「换个便宜表」，是**逐条核对过视图语义**的结果，
// 第一版我按「视图 = v1 减去与 session_turns 重复的部分」去写，数字错了近 4 倍。
//
// 视图在 890 之前的真实构成（pg_get_viewdef 实读，560 行）:
//
//	leg 1  FROM session_turns_hot  t LEFT JOIN session_turn_details_hot d
//	leg 2  FROM session_turns      t LEFT JOIN session_turn_details     d
//	leg 3  FROM v1 包装视图（…_without_request_class_due_at）
//	         LEFT JOIN LATERAL (v1 hot ∪ v1 母表，取 7 个附加列) source
//	WHERE NOT EXISTS(session_turns_hot) AND NOT EXISTS(session_turns)   ← 只滤 leg 3
//
// ⇒ 末尾那两条 NOT EXISTS 只对 leg 3 生效（leg 1/2 的行按定义就存在于
//
//	  session_turns，若真被滤掉整个视图会是空的），于是视图的净内容是：
//
//		会话族（hot ∪ 父表分区）∪（v1 里没被会话族覆盖的那些行）
//
// 2026-10-20（迁移 890）：v1 族整体退役 ⇒ leg 3 随之消失，canonical 视图
// 变成 session-only 的两臂体（leg 1/2），本函数去掉自己的 v1 臂之后与
// 视图**逐条等价**（两臂的投影文本与 710/734/815/816/817/824 装出来的
// 逐字相同，118 列同名同序）。
//
// 验证（逐日对比视图，真库 7 天窗）：
//
//	视图     37270 28298 13044 19495 18194 18917 23516     32
//	本源     37210 28298 13044 19495 18194 18917 23516     68
//	└─ 6 天逐条相等；两端日的差来自 session_turns_hot promote 的移动边界，
//	   两次查询不是同一时刻拍的。
//
// 为什么仍然值得换源：视图贵在**它定义的东西**——每行补 canonical 列、
// 反连 session_dim、给 7 个本端点根本不用的列做逐行 LATERAL 查。
// 实测 `SELECT COUNT(*) FROM request_logs_with_current_month` >60s 跑不完，
// 而本源同口径 7.26s。端点原本为拿总量/四段/按租户发了约 9 次这样的全扫。
func dataLifecycleCanonicalSourceSQL() string {
	return `
		SELECT s.tenant_id, s.ts FROM (
			SELECT tenant_id::text AS tenant_id, ts, request_id FROM session_turns_hot
			UNION ALL
			SELECT tenant_id::text, ts, request_id FROM session_turns
		) s
	`
}

// dataLifecycleCanonicalSegmentsSQL 在上面那个源上，**一次扫描**同时产出
// 「全局总量 + hot/warm/cold/expired 四段」与「按租户行数」。
//
// 口径与原实现逐项一致：同样的四个时间边界、同样的
// 「size_bytes = 表总大小 × 行数 / 总行数」按比例摊派。
// 原实现是 3 条查询、约 9 次视图全扫 ⇒ 稳定 500@30s；
// 现在是 1 条，实测 7.26s（30s 预算内留 2 倍余量）。
func dataLifecycleCanonicalSegmentsSQL(tenantFilter string) string {
	return `
		WITH merged AS MATERIALIZED (
			` + dataLifecycleCanonicalSourceSQL() + tenantFilter + `
		),
		agg AS (
			SELECT tenant_id,
			       -- ★ GROUPING() 只能出现在 SELECT 列表，不能用在 FILTER 里
			       --   （PG 42803: grouping operations are not allowed in FILTER）。
			       --   所以在这里把「这一行是不是总计行」标记出来，grand 再按它取。
			       GROUPING(tenant_id) AS is_total,
			       COUNT(*) AS total,
			       COUNT(*) FILTER (WHERE ts >  NOW() - INTERVAL '7 days') AS hot,
			       COUNT(*) FILTER (WHERE ts BETWEEN NOW() - INTERVAL '30 days' AND NOW() - INTERVAL '7 days') AS warm,
			       COUNT(*) FILTER (WHERE ts BETWEEN NOW() - INTERVAL '90 days' AND NOW() - INTERVAL '30 days') AS cold,
			       COUNT(*) FILTER (WHERE ts <  NOW() - INTERVAL '90 days') AS expired
			  FROM merged
			 GROUP BY GROUPING SETS ((tenant_id), ())
		),
		grand AS (
			SELECT COALESCE(MAX(total) FILTER (WHERE is_total = 1), 0) AS rows_all
			  FROM agg
		)
		SELECT CASE WHEN a.is_total = 1 THEN '__ALL__'
		            ELSE COALESCE(a.tenant_id, 'default') END AS scope,
		       a.total, a.hot, a.warm, a.cold, a.expired,
		       -- ★ 2026-10-21（R67 / C9）：这两列**曾经**会产出 NULL。
		       --   rows_all = 0（会话族两腿都空）⇒ NULLIF(g.rows_all, 0) = NULL
		       --   ⇒ 除以 NULL ⇒ size_bytes = NULL、size_human = NULL。
		       --   而 handler 把它们扫进**非指针** int64 / string
		       --   （data_lifecycle.go 的 dedupRow）⇒ pgx scan 报错 ⇒ 该行被跳过
		       --   ⇒ __ALL__ 行缺失 ⇒ 端点 500。
		       --   实测可达态：新装库、TTL 清空后、日志族整体退役后。
		       --   修法是**在 SQL 侧**收口（不改成可空扫描）：
		       --   0 行 ⇒ 摊派结果本就该是 0 字节，COALESCE 把「无行」翻译成 0，
		       --   而不是让 NULL 穿透到 scan 变成 500。
		       COALESCE((sz.total_size * a.total / NULLIF(g.rows_all, 0))::bigint, 0) AS size_bytes,
		       pg_size_pretty(COALESCE((sz.total_size * a.total / NULLIF(g.rows_all, 0))::bigint, 0)) AS size_human,
		       sz.total_size AS table_size
		  FROM agg a
		  CROSS JOIN grand g
		  -- 2026-10-20（迁移 890）：v1 族退役 ⇒ 尺寸源换成会话族母表。
		  -- ⚠ pg_total_relation_size 对**分区父表**返回 0（父表自身没有存储），
		  --   v1 母表当年也是分区父表、同样是 0 ⇒ 这个数字换源前后**逐字相同**，
		  --   没有引入新的口径。若要报真实占用得改成按 pg_partition_tree 求和，
		  --   那是一个会改变可观测数字的独立决定，不夹带在这次退役里。
		  CROSS JOIN LATERAL (SELECT pg_total_relation_size('session_turns') AS total_size) sz
		 ORDER BY (a.total) DESC
	`
}

// dataLifecycleTrendBudget 是趋势腿的子预算（端点总预算 30s）。
//
// 分段那条实测 12.8s，趋势那条要跑 17s+，两条串起来必然打穿 30s。
// 取 12s：最坏 12.8 + 12 = 24.8s，留 5s 余量；超时则 growth_trend 返回空数组，
// 端点仍返 200 —— 看板上的总量/四段/按租户是主载荷，不该被趋势拖成 500。
const dataLifecycleTrendBudget = 12 * time.Second

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
			-- ★ 2026-10-19：与分段那条同源，改读 dataLifecycleCanonicalSourceSQL()
			--   而不是 request_logs_with_current_month 视图。视图的 >60s 全量展开
			--   让整个端点打不进 30s 预算；这里换成等价源后 7 天窗实测 ~7s。
			--   bodies 那条腿**没动**：它是 S4 停写轮的口径修复（跨族 + 冻结点切分），
			--   不在本轮性能修复范围内。
			--   ★ 注释必须用 SQL 的 --：本段在 Go 的 raw string 里，写成 //
			--     会被原样发到 PostgreSQL，报 syntax error at or near "//"。
			--     本轮真踩过一次：端点 200 但 trend 腿整条失败，日志里只有一行 WARN。
			SELECT DATE(ts) AS day, COUNT(*) AS requests
			FROM (` + dataLifecycleCanonicalSourceSQL() + `) c
			WHERE ts > NOW() - INTERVAL '7 days'
			GROUP BY 1
		),
		bodies AS (
			SELECT DATE(ts) AS day, COUNT(DISTINCT request_id) AS compressed
			FROM (
				-- 会话腿：走 session_bodies_unified 合并视图（hot ∪ 月度分区父表，
				-- 迁移 625；promote 排空 hot ⇒ 两侧 request_id 交集 0，UNION ALL
				-- 不重复）。★ 这里**必须**走
				-- SessionBodiesHasOutboundPredicate：会话族用 jsonb null 表示
				-- 「没有正文」（实测 11,420 行），裸 IS NOT NULL 恒真 ⇒ 会把
				-- 当天**每一个** turn 都算成压缩过（实测 993 vs 真实 250）。
				--
				-- 2026-10-20（迁移 890）：v1 腿（冻结点及之前的停写前历史）随 v1
				-- 族退役而删除。冻结点本身已是常数 '-infinity'（db/v1_freeze_cutoff.go
				-- 的 890 注释），所以剩下的会话腿覆盖全部时间，与删掉 v1 腿之前的
				-- 实际取值**逐字相同**（v1 腿在冻结点之下恒空）。
				SELECT request_id, ts, (` + dbpkg.SessionBodiesHasOutboundPredicate("s") + `) AS has_outbound
				  FROM public.session_bodies_unified s
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

	// ★ 2026-10-19：原来的 1)~3)（总量 / 四段分段 / 按租户）三条查询，
	//   合计约 9 次 request_logs_with_current_month 视图全扫，真库每次 >60s，
	//   端点因此稳定 500@30s（29 次部署里的既定故障）。
	//   现在折叠成 **1 条**：单次去重扫描 + GROUPING SETS 同时产出
	//   「全局四段」与「按租户」。实测 6,080 ms（口径逐项一致，理由见
	//   dataLifecycleCanonicalSegmentsSQL 的注释）。
	//
	//   一次拿到两类行：
	//     scope='__ALL__'  → 总量 + hot/warm/cold/expired 四段
	//     scope=租户名     → 按租户行数（供 ByTenant，top 10）
	type dedupRow struct {
		Scope     string
		Total     int64
		Hot       int64
		Warm      int64
		Cold      int64
		Expired   int64
		SizeBytes int64
		SizeHuman string
		TableSize int64
	}
	dedupRows, err := h.db.Query(ctx, dataLifecycleCanonicalSegmentsSQL(tenantFilter))
	if err != nil {
		slog.Warn("data_lifecycle_stats dedup segments failed", "error", err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer dedupRows.Close()

	const allScope = "__ALL__"
	segOf := map[string]int64{"hot": 0, "warm": 0, "cold": 0, "expired": 0}
	daysOf := map[string]int{"hot": 7, "warm": 23, "cold": 60, "expired": 999}
	gotAll := false
	for dedupRows.Next() {
		var dr dedupRow
		if err := dedupRows.Scan(&dr.Scope, &dr.Total, &dr.Hot, &dr.Warm, &dr.Cold, &dr.Expired,
			&dr.SizeBytes, &dr.SizeHuman, &dr.TableSize); err != nil {
			warnRowSkip("dataLifecycle.stats.dedupSegments", err)
			continue
		}
		if dr.Scope == allScope {
			gotAll = true
			stats.TotalRows = dr.Total
			stats.TotalSizeBytes = dr.TableSize
			stats.TotalSizeHuman = dr.SizeHuman
			segOf["hot"], segOf["warm"], segOf["cold"], segOf["expired"] =
				dr.Hot, dr.Warm, dr.Cold, dr.Expired
			continue
		}
		stats.ByTenant = append(stats.ByTenant, tenantDataStats{
			TenantID: dr.Scope, Rows: dr.Total,
			SizeBytes: dr.SizeBytes, SizeHuman: dr.SizeHuman,
		})
	}
	// 迭代中断必须按查询侧同语义收口，不能静默 200：缺了 __ALL__ 行时
	// TotalRows=0 会让四段的 PercentOfTotal 全部变成 0，
	// 看板上表现为「总量 0 但各段有值」，比报错更难查。
	if err := dedupRows.Err(); err != nil {
		slog.Warn("data_lifecycle_stats dedup rows aborted; report truncated", "error", err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	if !gotAll {
		slog.Warn("data_lifecycle_stats dedup rows missing grand-total scope")
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}

	// 四段由同一次聚合产出，逐段挂到响应字段上。
	for name, seg := range map[string]**dataSegment{
		"hot": &stats.HotData, "warm": &stats.WarmData,
		"cold": &stats.ColdData, "expired": &stats.ExpiredData,
	} {
		rows := segOf[name]
		pct := 0.0
		if stats.TotalRows > 0 {
			pct = float64(rows) / float64(stats.TotalRows) * 100
		}
		sizeBytes := int64(0)
		if stats.TotalRows > 0 {
			sizeBytes = stats.TotalSizeBytes * rows / stats.TotalRows
		}
		*seg = &dataSegment{
			Rows: rows, SizeBytes: sizeBytes, SizeHuman: humanBytes(sizeBytes),
			Days: daysOf[name], PercentOfTotal: pct,
		}
	}

	// ByTenant 取 top 10（按行数降序，SQL 已排序，这里只截断）。
	if len(stats.ByTenant) > 10 {
		stats.ByTenant = stats.ByTenant[:10]
	}

	// 4. Growth trend (last 7 days)
	// 2026-09-14: outbound_body left the main table/view in the Phase-1
	// body-storage split (it lives in the bodies family now). The
	// bodies view has no tenant_id, so: tenant-scoped callers take the
	// row-by-row LEFT JOIN (correct but heavy, guarded by the request
	// timeout); the unscoped path pre-aggregates both sides by day first —
	// a per-row join over a week of request rows × bodies blows the admin
	// time budget (measured 30s timeout on the local 100k-row dataset).
	// ★ 2026-10-19：趋势腿自带子预算。
	//   分段那条（单次规范源扫描）实测 12.8s，趋势那条在同一 30s 总预算里
	//   只能分到 ~17s，而它自己就要跑更久 ⇒ 整端点 500@30s。
	//   与 filter-options 的两条臂同一套降级语义：**趋势失败只让 growth_trend 空着，
	//   端点仍返 200**，看板上的总量/分段/按租户照常显示。
	//   实测最坏 12.8 + 12 = 24.8s，仍在 30s 内。
	trendQuery := dataLifecycleTrendSQL(tenantFilter)
	trendCtx, trendCancel := context.WithTimeout(ctx, dataLifecycleTrendBudget)
	trendRows, err := h.db.Query(trendCtx, trendQuery)
	trendCancel()
	if err != nil {
		slog.Warn("data_lifecycle_stats trend failed; growth_trend 已退化为空",
			"budget", dataLifecycleTrendBudget, "error", err)
		stats.GrowthTrend = []dailyGrowth{}
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
		-- 2026-10-20（迁移 890）：v1 族退役 ⇒ 尺寸源与分母都换成会话族母表。
		-- pg_total_relation_size 对分区父表返回 0（父表自身无存储），v1 母表
		-- 当年同理 ⇒ 换源前后这个估算值**逐字相同**。
		SELECT 
			COUNT(*) AS affected_rows,
			(pg_total_relation_size('session_turns') * COUNT(*)::numeric / NULLIF((SELECT COUNT(*) FROM session_turns), 0))::bigint AS freed_bytes,
			pg_size_pretty((pg_total_relation_size('session_turns') * COUNT(*)::numeric / NULLIF((SELECT COUNT(*) FROM session_turns), 0))::bigint) AS freed_human
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
