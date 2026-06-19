package admin

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// handleCompressionStats returns aggregate compression strategy distribution
// and token savings for the last N hours. Added 2026-06-20 (P2 observability).
//
// GET /api/admin/compression/stats?hours=24
func (h *Handler) handleCompressionStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	hours := queryInt(r, "hours", 24)
	if hours < 1 {
		hours = 1
	}
	if hours > 168 {
		hours = 168
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var result = struct {
		TotalRequests    int            `json:"total_requests"`
		CompressedTotal  int            `json:"compressed_total"`
		StrategyDist     map[string]int `json:"strategy_distribution"`
		TotalBytesBefore *int64         `json:"total_bytes_before,omitempty"`
		TotalBytesAfter  *int64         `json:"total_bytes_after,omitempty"`
		TotalTokensBefore *int64        `json:"total_tokens_before,omitempty"`
		TotalTokensAfter  *int64        `json:"total_tokens_after,omitempty"`
	}{
		StrategyDist: make(map[string]int),
	}

	rows, err := h.db.Query(ctx, `
		SELECT
			COALESCE(NULLIF(rl.compression_strategy,''), 'none') AS strategy,
			COUNT(*) AS cnt,
			COUNT(rl.outbound_body)::bigint AS with_outbound,
			SUM(rl.outbound_token_est)::bigint AS total_tok_after,
			SUM(CASE WHEN rl.outbound_body IS NOT NULL THEN rl.outbound_token_est ELSE 0 END)::bigint AS compressed_tok
		FROM request_logs rl
		WHERE rl.ts > now() - ($1 || ' hours')::interval
		  AND ($2 OR rl.success)
		GROUP BY strategy
		ORDER BY cnt DESC
	`, fmt.Sprintf("%d", hours), !IsTenantAdmin(r))
	if err != nil {
		slog.Warn("compression_stats query failed", "error", err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	var totalToksAfter int64
	for rows.Next() {
		var strategy string
		var cnt, withOutbound, tokAfter, compressedTok int
		if err := rows.Scan(&strategy, &cnt, &withOutbound, &tokAfter, &compressedTok); err != nil {
			slog.Warn("compression_stats scan failed", "error", err)
			continue
		}
		result.TotalRequests += cnt
		if withOutbound > 0 {
			result.CompressedTotal += cnt
		}
		result.StrategyDist[strategy] = cnt
		totalToksAfter += int64(tokAfter)
	}
	result.TotalTokensAfter = &totalToksAfter

	writeJSON(w, http.StatusOK, result)
}
