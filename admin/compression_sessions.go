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

	whereClause := `rl.ts >= $1 AND rl.ts <= $2 AND rb.outbound_body IS NOT NULL AND rl.gw_session_id IS NOT NULL AND ($3 OR rl.success)`
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

	// Count distinct sessions
	//
	// 2026-10-13 S4 停写后读源会话族化：turns 腿换 canonical 会话族源
	// （v1 列名投影，hot∪父表+details），bodies 腿换会话族 bodies 源。
	// 原两腿读 v1 视图族（request_logs_with_current_month ×
	// request_logs_bodies_with_current_month），停写后对新会话恒空。
	// 压缩列（compression_strategy/outbound_msg_count/outbound_token_est）
	// 在 canonical 投影内由 turns/details 层供值（真库实测会话侧已有数据）。
	countSQL := `SELECT COUNT(DISTINCT rl.gw_session_id) FROM ` + dbpkg.SessionFamilyTurnsSourceSQL() + ` rl LEFT JOIN ` + dbpkg.SessionFamilyBodiesSourceSQL() + ` rb ON rb.request_id = rl.request_id WHERE ` + whereClause
	var totalCount int
	if err := h.db.QueryRow(ctx, countSQL, args...).Scan(&totalCount); err != nil {
		slog.Warn("compression_sessions count query failed", "error", err)
		writeJSON(w, http.StatusOK, compressionSessionsResponse{Items: make([]compressionSessionItem, 0), Count: 0})
		return
	}

	// Main query: 单趟窗口聚合 + DISTINCT ON 取每会话最新带 outbound 的轮次。
	//
	// 2026-10-13 S4 停写后读源会话族化：turns 腿换 canonical 会话族源
	// （v1 列名投影，hot∪父表+details），bodies 腿换会话族 bodies 源。
	// 口径：会话族 request_body 即消息数组（request_delta 投影），数组长度
	// 就是原始消息数；v1 时代的 request_body->'messages' 取键不再适用。
	// ts 等值配对已按 §5.9 实测口径删除（bodies.ts 是正文写入时间，
	// 99.85% 不等于轮次时间）。
	// ⚠ 形状守卫（2605 部署实测抓的缺陷）：request_delta 有四种形态——
	// array / SQL NULL / **JSON 字面量 null / 其它标量**。JSON null 的 SQL
	// 值非 NULL，COALESCE 救不了，jsonb_array_length 直接 22023
	// 「cannot get array length of a scalar」。必须 jsonb_typeof 判形，
	// 非 array 一律记 0。
	//
	// ⚠ 2612 部署实测抓的性能缺陷（生产规模）：第一版把「每会话最新轮」写
	// 成 LEFT JOIN LATERAL——LATERAL 体里**重跑一遍 canonical 全源**，对
	// 每个 GROUP 结果行执行一次；本地 12k 行测不出，生产 925k turns ×
	// 窗口内 ~1k 会话直接把 10s handler 预算打满。正确形态是单趟 CTE：
	// window_turns 物化窗口行（countSQL 同谓词），stats 组内聚合、latest 用
	// DISTINCT ON (gw_session_id) ORDER BY ts DESC 各扫一遍窗口——代价与
	// 会话数**无关**。判据钉住「不得再出现 LEFT JOIN LATERAL」。
	mainSQL := `
		WITH window_turns AS (
			SELECT rl.gw_session_id, rl.ts, rl.request_id,
			       rl.compression_strategy, rl.outbound_msg_count, rl.outbound_token_est,
			       rb.outbound_body, rb.request_body
			FROM ` + dbpkg.SessionFamilyTurnsSourceSQL() + ` rl
			LEFT JOIN ` + dbpkg.SessionFamilyBodiesSourceSQL() + ` rb ON rb.request_id = rl.request_id
			WHERE ` + whereClause + `
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
				CASE WHEN jsonb_typeof(request_body) = 'array'
				     THEN jsonb_array_length(request_body)
				     ELSE 0 END AS orig_msg_count
			FROM window_turns
			WHERE outbound_body IS NOT NULL
			ORDER BY gw_session_id, ts DESC
		)
		SELECT
			s.gw_session_id,
			s.compression_strategy,
			s.request_count,
			s.first_ts,
			s.last_ts,
			s.outbound_msg_count,
			s.outbound_token_est,
			s.sample_request_id,
			COALESCE(latest.orig_msg_count, 0) AS estimated_original_msgs
		FROM s
		LEFT JOIN latest ON latest.gw_session_id = s.gw_session_id
		ORDER BY s.last_ts DESC
		LIMIT $` + strconv.Itoa(argIdx) + ` OFFSET $` + strconv.Itoa(argIdx+1)
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
