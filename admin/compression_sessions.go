package admin

// NOTE: All SELECTs on tenant-scoped tables (request_logs etc.) in this file
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

	// Main query with LATERAL to get latest request's orig msg count.
	// LATERAL 口径：会话族 request_body 即消息数组（request_delta 投影），
	// 数组长度就是原始消息数；v1 时代的 request_body->'messages' 取键不再
	// 适用。ts 等值配对已按 §5.9 实测口径删除（bodies.ts 是正文写入时间，
	// 99.85% 不等于轮次时间）。
	mainSQL := `
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
		FROM (
			SELECT
				rl.gw_session_id,
				MAX(rl.compression_strategy) AS compression_strategy,
				COUNT(*) AS request_count,
				MIN(rl.ts) AS first_ts,
				MAX(rl.ts) AS last_ts,
				MAX(rl.outbound_msg_count) AS outbound_msg_count,
				MAX(rl.outbound_token_est) AS outbound_token_est,
				MAX(rl.request_id) AS sample_request_id
			FROM ` + dbpkg.SessionFamilyTurnsSourceSQL() + ` rl
			LEFT JOIN ` + dbpkg.SessionFamilyBodiesSourceSQL() + ` rb ON rb.request_id = rl.request_id
			WHERE ` + whereClause + `
			GROUP BY rl.gw_session_id
		) s
		LEFT JOIN LATERAL (
			SELECT jsonb_array_length(COALESCE(rb2.request_body::jsonb, '[]'::jsonb)) AS orig_msg_count
			FROM ` + dbpkg.SessionFamilyTurnsSourceSQL() + ` rl2
			LEFT JOIN ` + dbpkg.SessionFamilyBodiesSourceSQL() + ` rb2
			  ON rb2.request_id = rl2.request_id
			WHERE rl2.gw_session_id = s.gw_session_id
			  AND rb2.outbound_body IS NOT NULL
			ORDER BY rl2.ts DESC
			LIMIT 1
		) latest ON true
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
