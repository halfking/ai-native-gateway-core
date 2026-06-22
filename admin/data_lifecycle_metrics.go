package admin

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

// Prometheus metrics for data lifecycle monitoring
type dataLifecycleMetrics struct {
	TotalRows           int64   `json:"total_rows"`
	TotalSizeBytes      int64   `json:"total_size_bytes"`
	HotDataRows         int64   `json:"hot_data_rows"`
	HotDataSizeBytes    int64   `json:"hot_data_size_bytes"`
	WarmDataRows        int64   `json:"warm_data_rows"`
	WarmDataSizeBytes   int64   `json:"warm_data_size_bytes"`
	ColdDataRows        int64   `json:"cold_data_rows"`
	ColdDataSizeBytes   int64   `json:"cold_data_size_bytes"`
	ExpiredDataRows     int64   `json:"expired_data_rows"`
	ExpiredDataSizeBytes int64  `json:"expired_data_size_bytes"`
	LastCleanupAt       *string `json:"last_cleanup_at,omitempty"`
	LastArchiveAt       *string `json:"last_archive_at,omitempty"`
}

// GET /api/admin/data-lifecycle/metrics
// Lightweight metrics endpoint for Prometheus scraping (no tenant filtering)
func (h *Handler) handleDataLifecycleMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	var metrics dataLifecycleMetrics

	// Quick aggregation query (no pg_size_pretty to save CPU)
	err := h.db.QueryRow(ctx, `
		SELECT 
			COUNT(*) AS total_rows,
			pg_total_relation_size('request_logs') AS total_size,
			COUNT(*) FILTER (WHERE ts > NOW() - INTERVAL '7 days') AS hot_rows,
			(pg_total_relation_size('request_logs') * COUNT(*) FILTER (WHERE ts > NOW() - INTERVAL '7 days')::numeric / NULLIF(COUNT(*), 0))::bigint AS hot_size,
			COUNT(*) FILTER (WHERE ts BETWEEN NOW() - INTERVAL '30 days' AND NOW() - INTERVAL '7 days') AS warm_rows,
			(pg_total_relation_size('request_logs') * COUNT(*) FILTER (WHERE ts BETWEEN NOW() - INTERVAL '30 days' AND NOW() - INTERVAL '7 days')::numeric / NULLIF(COUNT(*), 0))::bigint AS warm_size,
			COUNT(*) FILTER (WHERE ts BETWEEN NOW() - INTERVAL '90 days' AND NOW() - INTERVAL '30 days') AS cold_rows,
			(pg_total_relation_size('request_logs') * COUNT(*) FILTER (WHERE ts BETWEEN NOW() - INTERVAL '90 days' AND NOW() - INTERVAL '30 days')::numeric / NULLIF(COUNT(*), 0))::bigint AS cold_size,
			COUNT(*) FILTER (WHERE ts < NOW() - INTERVAL '90 days') AS expired_rows,
			(pg_total_relation_size('request_logs') * COUNT(*) FILTER (WHERE ts < NOW() - INTERVAL '90 days')::numeric / NULLIF(COUNT(*), 0))::bigint AS expired_size
		FROM request_logs
	`).Scan(
		&metrics.TotalRows,
		&metrics.TotalSizeBytes,
		&metrics.HotDataRows,
		&metrics.HotDataSizeBytes,
		&metrics.WarmDataRows,
		&metrics.WarmDataSizeBytes,
		&metrics.ColdDataRows,
		&metrics.ColdDataSizeBytes,
		&metrics.ExpiredDataRows,
		&metrics.ExpiredDataSizeBytes,
	)
	
	if err != nil {
		slog.Warn("data_lifecycle_metrics query failed", "error", err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	//nolint:errcheck // HTTP write error non-recoverable
	json.NewEncoder(w).Encode(metrics)
}
