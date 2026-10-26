package admin

// NOTE: All SELECTs on tenant-scoped tables (_request_logs etc.) in this file
// rely on tenantLogsClause() (admin/session_tenant.go) to inject
// "AND tenant_id = $N" for tenant_admin callers on non-default tenants.
// Super-admin / legacy admin_key / default-tenant callers see all rows.

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	dbpkg "github.com/kaixuan/llm-gateway-go/db"
)

type compressionSessionItem struct {
	GwSessionID         string    `json:"gw_session_id"`
	CompressionStrategy string    `json:"compression_strategy"`
	RequestCount        int       `json:"request_count"`
	FirstTs             time.Time `json:"first_ts"`
	LastTs              time.Time `json:"last_ts"`
	OutboundMsgCount    *int      `json:"outbound_msg_count"`
	OutboundTokenEst    *int      `json:"outbound_token_est"`
	EstimatedOrigMsgs   *int      `json:"estimated_original_msgs"`
	MsgReduction        *int      `json:"msg_reduction"`
	SampleRequestID     string    `json:"sample_request_id"`
}

type compressionSessionsResponse struct {
	Items []compressionSessionItem `json:"items"`
	Count int                      `json:"count"`
}

func (h *Handler) handleCompressionSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	hours := queryInt(r, "hours", 24)
	if hours < 1 {
		hours = 1
	}
	if hours > 720 {
		hours = 720
	}

	fromStr := r.URL.Query().Get("from")
	toStr := r.URL.Query().Get("to")
	strategyFilter := r.URL.Query().Get("strategy")
	page := queryInt(r, "page", 1)
	pageSize := queryInt(r, "page_size", 50)
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 200 {
		pageSize = 50
	}
	offset := (page - 1) * pageSize

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	var from, to time.Time
	if fromStr != "" {
		var err error
		from, err = time.Parse(time.RFC3339, fromStr)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid from")
			return
		}
	} else {
		to = time.Now().UTC()
		from = to.Add(-time.Duration(hours) * time.Hour)
	}
	if toStr != "" {
		var err error
		to, err = time.Parse(time.RFC3339, toStr)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid to")
			return
		}
	} else if fromStr != "" {
		to = time.Now().UTC()
	}

	tenantFilter := IsTenantAdmin(r)

	// ★ R76 轮（2026-10-24，生产 252 共享库实测）：whereClause **不再**带
	//   `rb.outbound_body IS NOT NULL` —— 那个谓词要求把 bodies 的 jsonb 载荷
	//   搬过整个 24h 窗口 ⇒ 生产实测主查询 **105,934 ms**（EXPLAIN ANALYZE 口径）
	//   ⇒ 必然撞 10s 的 ctx ⇒ ⇒ `/api/admin/compression/sessions` **恒 500**。
	//   ⇒ bodies 侧改成只用 EXISTS 半连接判「有没有 outbound body」（不搬载荷），
	//     载荷只在**最终页**（LIMIT 之后）回表取 —— 详见 compressionSessionsSQL。
	whereClause := `rl.ts >= $1 AND rl.ts <= $2 AND rl.gw_session_id IS NOT NULL AND ($3 OR rl.success)`
	args := []any{from, to, !tenantFilter}
	argIdx := 4
	tenantFrag, tenantArgs, nextArg := tenantLogsClause(r, argIdx)
	if tenantFrag != "" {
		whereClause += tenantFrag
		args = append(args, tenantArgs...)
		argIdx = nextArg
	}

	if strategyFilter != "" {
		whereClause += ` AND rl.compression_strategy = $` + strconv.Itoa(argIdx)
		args = append(args, strategyFilter)
		argIdx++
	}

	// Count distinct sessions + 单页明细（两段 SQL 由同一个构造函数产出，
	// 便于门对**真实产物**断言形状 —— admin/compression_sessions_shape_test.go）。
	countSQL, mainSQL := compressionSessionsSQL(whereClause, argIdx, argIdx+1)
	var totalCount int
	if err := h.db.QueryRow(ctx, countSQL, args...).Scan(&totalCount); err != nil {
		slog.Warn("compression_sessions count query failed", "error", err)
		writeJSON(w, http.StatusOK, compressionSessionsResponse{Items: make([]compressionSessionItem, 0), Count: 0})
		return
	}

	// Main query 的形状说明见 compressionSessionsSQL 的文档注释。
	//
	// ⚠⚠ R76 轮（2026-10-24，生产 252 共享库实测）：旧形状把 `rb.outbound_body,
	// rb.request_body` 两列**带过整个窗口**（CTE window_turns）⇒ 生产实测
	// **105,934 ms**（EXPLAIN ANALYZE 口径，Buffers hit=307,841 ≈ 2.4GB）
	// ⇒ 必然撞 10s ctx ⇒ 端点恒 500。
	//   ⚠ 大头是「每会话最新轮」的载荷解冻：生产 session_bodies_hot 9,499 行占
	//   **1,058 MB**（TOAST 1,027 MB）⇒ 单行载荷 ~110KB；旧形状的 `latest` CTE
	//   对**每个会话**都算 jsonb_array_length ⇒ 解冻量级是「会话数」而非「页行数」。
	// ⇒⇒ 新形状（载荷只在 LIMIT 之后回表）生产实测 **359 ms**（EXPLAIN ANALYZE，
	//   同形状另一次实测 322 ms ⇒ 区间 322~359 ms，随共享库负载波动）⇒ **295~329×**。
	//   逐行等价（冻结窗口，差异 0 行）见
	//   docs/audit/2026-10-24-R76-compression-sessions生产恒500根因与修复.md。
	args = append(args, pageSize, offset)

	rows, err := h.db.Query(ctx, mainSQL, args...)
	if err != nil {
		slog.Warn("compression_sessions query failed", "error", err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	items := make([]compressionSessionItem, 0)
	for rows.Next() {
		var item compressionSessionItem
		var firstTs, lastTs time.Time
		if err := rows.Scan(
			&item.GwSessionID,
			&item.CompressionStrategy,
			&item.RequestCount,
			&firstTs,
			&lastTs,
			&item.OutboundMsgCount,
			&item.OutboundTokenEst,
			&item.SampleRequestID,
			&item.EstimatedOrigMsgs,
		); err != nil {
			slog.Warn("compression_sessions scan failed", "error", err)
			continue
		}
		item.FirstTs = firstTs
		item.LastTs = lastTs
		if item.EstimatedOrigMsgs != nil && item.OutboundMsgCount != nil {
			red := *item.EstimatedOrigMsgs - *item.OutboundMsgCount
			if red < 0 {
				red = 0
			}
			item.MsgReduction = &red
		}
		if item.GwSessionID != "" {
			items = append(items, item)
		}
	}
	// 压缩会话页少一截 → count 与 items 对不上，分页到底判断失真。
	if writeAggRowsErr(w, "compression.sessions", rows.Err()) {
		return
	}

	if items == nil {
		items = make([]compressionSessionItem, 0)
	}

	writeJSON(w, http.StatusOK, compressionSessionsResponse{
		Items: items,
		Count: totalCount,
	})
}

// compressionSessionsSQL 产出 count 与主查询两段 SQL。
//
// ★ 为什么单独抽成函数：形状门必须断言**真实产物**（而不是源码里的字面量片段），
//
//	否则「换个位置写同样的 SQL」就会让门失去牙。
//
// # R76 轮改造（2026-10-24，生产 252 共享库实测）
//
// 旧形状（生产恒 500）：
//
//	CTE window_turns = turns 源 LEFT JOIN bodies 源，
//	                     **并把 rb.outbound_body, rb.request_body 带过整个 24h 窗口**
//	latest CTE = 对 window_turns 每个会话算 jsonb_array_length(request_body)
//
//	⇒ 生产 EXPLAIN ANALYZE 实测 **105,934 ms**（Buffers hit=307,841 ≈ 2.4GB）
//	⇒ handler 的 ctx 只有 10s ⇒ ⇒ 端点**必然**超时 500。
//
// 新形状（三条独立改动，每条都有生产读数支撑）：
//
//	① bodies 侧只用 EXISTS 半连接判「有没有 outbound body」——**不搬载荷**。
//	   语义等价性：旧形状的 LEFT JOIN + `rb.outbound_body IS NOT NULL`
//	   与 EXISTS 等价（生产实测 window 内 turn×body 是 1:1，无重复放大）。
//	② CTE window_turns **只留 6 个标量列**，不带任何 jsonb 载荷列。
//	③ 载荷只在**最终页**（LIMIT 之后）用 LATERAL 回表取 jsonb_array_length。
//	   ★ 关键：LATERAL 必须在 LIMIT **之外**——放在 LIMIT 之前的话它会对
//	   「每个会话」执行一次（生产 14,434 会话 ⇒ 14,434 次载荷解冻），
//	   实测该形态 6,090 ms；挪到 LIMIT 之后只剩 50 次 ⇒ 359 ms。
//
// 生产实测（冻结窗口 '2026-10-24 06:00:00+08'，逐行对拍 old/new）：
//
//	差异行 = **0**（old 独有 0 / new 独有 0），count **14,487 = 14,487**
//	EXPLAIN ANALYZE 口径：105,934ms → **359ms**（**295×**）
//	（同形状另一次实测 322 ms ⇒ 区间 322~359 ms，随共享库负载波动）
//
// 形状门：admin/compression_sessions_shape_test.go
func compressionSessionsSQL(whereClause string, pageSizeArg, offsetArg int) (string, string) {
	// ★ 双源会话族化：turns 源与 bodies 源各出现 2 次
	//   （count 侧一次、主查询侧一次）—— 与既有门
	//   admin/s3_wave4_bodies_sources_test.go 的字面计数口径一致。
	//   ⇒ 这里**刻意内联**而不抽变量：抽变量会让既有的「出现 2 次」判据失效。
	outboundExists := `EXISTS (SELECT 1 FROM ` + dbpkg.SessionFamilyBodiesSourceSQL() +
		` rb WHERE rb.request_id = rl.request_id AND rb.outbound_body IS NOT NULL)`

	countSQL := `SELECT COUNT(DISTINCT rl.gw_session_id) FROM ` + dbpkg.SessionFamilyTurnsSourceSQL() +
		` rl WHERE ` + whereClause + ` AND ` + outboundExists

	// ⚠ 形状守卫（2605 部署实测抓的缺陷，保留）：request_delta 有四种形态——
	//   array / SQL NULL / **JSON 字面量 null / 其它标量**。JSON null 的 SQL 值
	//   非 NULL，COALESCE 救不了，jsonb_array_length 直接 22023
	//   「cannot get array length of a scalar」⇒ 必须 jsonb_typeof 判形。
	//
	// ⚠ 形状守卫（2612 部署实测抓的性能缺陷，保留）：**不得**用「LATERAL 体里
	//   重跑一遍 canonical 全源」那种写法——代价与**会话数**相关，生产 925k turns
	//   规模直接把 10s 预算打满。本查询里载荷用**标量子查询**取（带 LIMIT 1，
	//   不可被 pull-up 展开）⇒ 只在**最终页行**上求值（页行数级，不是会话数级）。
	mainSQL := `
		WITH window_turns AS (
			SELECT rl.gw_session_id, rl.ts, rl.request_id,
			       rl.compression_strategy, rl.outbound_msg_count, rl.outbound_token_est
			FROM ` + dbpkg.SessionFamilyTurnsSourceSQL() + ` rl
			WHERE ` + whereClause + ` AND ` + outboundExists + `
		),
		s AS (
			SELECT
				gw_session_id,
				MAX(compression_strategy) AS compression_strategy,
				COUNT(*) AS request_count,
				MIN(ts) AS first_ts,
				MAX(ts) AS last_ts,
				MAX(outbound_msg_count) AS outbound_msg_count,
				MAX(outbound_token_est) AS outbound_token_est,
				MAX(request_id) AS sample_request_id
			FROM window_turns
			GROUP BY gw_session_id
		),
		latest AS (
			SELECT DISTINCT ON (gw_session_id)
				gw_session_id,
				request_id
			FROM window_turns
			ORDER BY gw_session_id, ts DESC
		),
		page AS (
			SELECT
				s.gw_session_id,
				s.compression_strategy,
				s.request_count,
				s.first_ts,
				s.last_ts,
				s.outbound_msg_count,
				s.outbound_token_est,
				s.sample_request_id,
				latest.request_id AS latest_request_id
			FROM s
			LEFT JOIN latest ON latest.gw_session_id = s.gw_session_id
			ORDER BY s.last_ts DESC
			LIMIT $` + strconv.Itoa(pageSizeArg) + ` OFFSET $` + strconv.Itoa(offsetArg) + `
		)
		SELECT
			p.gw_session_id,
			p.compression_strategy,
			p.request_count,
			p.first_ts,
			p.last_ts,
			p.outbound_msg_count,
			p.outbound_token_est,
			p.sample_request_id,
			COALESCE((
				SELECT CASE WHEN jsonb_typeof(request_body) = 'array'
				            THEN jsonb_array_length(request_body)
				            ELSE 0 END
				FROM ` + dbpkg.SessionFamilyBodiesSourceSQL() + ` rb
				WHERE rb.request_id = p.latest_request_id
				LIMIT 1
			), 0) AS estimated_original_msgs
		FROM page p
		ORDER BY p.last_ts DESC`

	return countSQL, mainSQL
}
