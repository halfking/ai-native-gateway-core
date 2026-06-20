package admin

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"time"
)

type compressionSessionItem struct {
	GwSessionID          string    `json:"gw_session_id"`
	CompressionStrategy  string    `json:"compression_strategy"`
	RequestCount         int       `json:"request_count"`
	FirstTs              time.Time `json:"first_ts"`
	LastTs               time.Time `json:"last_ts"`
	OutboundMsgCount     *int      `json:"outbound_msg_count"`
	OutboundTokenEst     *int      `json:"outbound_token_est"`
	EstimatedOrigMsgs    *int      `json:"estimated_original_msgs"`
	MsgReduction         *int      `json:"msg_reduction"`
	SampleRequestID      string    `json:"sample_request_id"`
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

	whereClause := `rl.ts >= $1 AND rl.ts <= $2 AND rl.outbound_body IS NOT NULL AND rl.gw_session_id IS NOT NULL AND ($3 OR rl.success)`
	args := []any{from, to, !tenantFilter}
	argIdx := 4

	if strategyFilter != "" {
		whereClause += ` AND rl.compression_strategy = $` + strconv.Itoa(argIdx)
		args = append(args, strategyFilter)
		argIdx++
	}

	// Count distinct sessions
	countSQL := `SELECT COUNT(DISTINCT rl.gw_session_id) FROM request_logs rl WHERE ` + whereClause
	var totalCount int
	if err := h.db.QueryRow(ctx, countSQL, args...).Scan(&totalCount); err != nil {
		slog.Warn("compression_sessions count query failed", "error", err)
		writeJSON(w, http.StatusOK, compressionSessionsResponse{Items: make([]compressionSessionItem, 0), Count: 0})
		return
	}

	// Main query with LATERAL to get latest request's orig msg count
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
			FROM request_logs rl
			WHERE ` + whereClause + `
			GROUP BY rl.gw_session_id
		) s
		LEFT JOIN LATERAL (
			SELECT jsonb_array_length(COALESCE(rl2.request_body::jsonb->'messages', '[]'::jsonb)) AS orig_msg_count
			FROM request_logs rl2
			WHERE rl2.gw_session_id = s.gw_session_id
			  AND rl2.outbound_body IS NOT NULL
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

	if items == nil {
		items = make([]compressionSessionItem, 0)
	}

	writeJSON(w, http.StatusOK, compressionSessionsResponse{
		Items: items,
		Count: totalCount,
	})
}
