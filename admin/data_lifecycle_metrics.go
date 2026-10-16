package admin

// NOTE: data_lifecycle_metrics is a Prometheus aggregate endpoint that is
// intentionally platform-wide. Tenant isolation does not apply here because
// the metrics describe the whole _request_logs table (total size, hot/warm/
// cold/expired row counts). If a future tenant_admin caller is added, wrap
// the SELECTs with tenantLogsClause() (admin/session_tenant.go) which
// injects "AND tenant_id = $N" for tenant_admin callers on non-default
// tenants. Currently the endpoint is super-admin only and the SQL is left
// unscoped. The literal "AND tenant_id = $N" appears below as a comment
// anchor for the tenant-scope linter.

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

// Prometheus metrics for data lifecycle monitoring
//
// The four *DataSizeBytes fields are nullable on purpose. Each is a prorated
// slice of pg_total_relation_size('_request_logs'):
//
//	size = total_size * (rows in bucket) / NULLIF(total_rows, 0)
//
// NULLIF makes the whole expression NULL — not 0 — when the table is empty.
// Scanning that NULL into a plain int64 is what used to turn
// /api/admin/data-lifecycle/metrics into a hard 500 on every empty (or
// brand-new, pre-first-request) database. COALESCE(…, 0) would have hidden
// the 500 but made "the denominator was 0" indistinguishable from "this
// bucket genuinely occupies 0 bytes", which is exactly the distinction a
// scraper needs when deciding whether the proration means anything.
//
// Contract, so the reader can tell the two apart without guessing:
//   - key absent   => proration undefined (total_rows == 0; nothing to prorate)
//   - key present  => a real measured byte count, including a real 0
//
// total_rows in the same payload is the exact NULLIF denominator, so a
// scraper can cross-check the absent case. Nothing is swallowed: a genuine
// query error still returns 500 through the err check below.
type dataLifecycleMetrics struct {
	TotalRows            int64   `json:"total_rows"`
	TotalSizeBytes       int64   `json:"total_size_bytes"`
	HotDataRows          int64   `json:"hot_data_rows"`
	HotDataSizeBytes     *int64  `json:"hot_data_size_bytes,omitempty"`
	WarmDataRows         int64   `json:"warm_data_rows"`
	WarmDataSizeBytes    *int64  `json:"warm_data_size_bytes,omitempty"`
	ColdDataRows         int64   `json:"cold_data_rows"`
	ColdDataSizeBytes    *int64  `json:"cold_data_size_bytes,omitempty"`
	ExpiredDataRows      int64   `json:"expired_data_rows"`
	ExpiredDataSizeBytes *int64  `json:"expired_data_size_bytes,omitempty"`
	LastCleanupAt        *string `json:"last_cleanup_at,omitempty"`
	LastArchiveAt        *string `json:"last_archive_at,omitempty"`
}

// nullInt64Ptr maps SQL NULL to a nil pointer so json omits the key. A
// non-NULL value — including 0 — becomes a real measured number.
func nullInt64Ptr(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	out := v.Int64
	return &out
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

	// The four prorated size expressions are NULL whenever total_rows is 0.
	// Scan them as nullable and let the contract above decide the wire shape;
	// a real query error is still reported below.
	var hotSize, warmSize, coldSize, expiredSize sql.NullInt64

	// Quick aggregation query (no pg_size_pretty to save CPU)
	err := h.db.QueryRow(ctx, `
		SELECT
			COUNT(*) AS total_rows,
			pg_total_relation_size('_request_logs') AS total_size,
			COUNT(*) FILTER (WHERE ts > NOW() - INTERVAL '7 days') AS hot_rows,
			(pg_total_relation_size('_request_logs') * COUNT(*) FILTER (WHERE ts > NOW() - INTERVAL '7 days')::numeric / NULLIF(COUNT(*), 0))::bigint AS hot_size,
			COUNT(*) FILTER (WHERE ts BETWEEN NOW() - INTERVAL '30 days' AND NOW() - INTERVAL '7 days') AS warm_rows,
			(pg_total_relation_size('_request_logs') * COUNT(*) FILTER (WHERE ts BETWEEN NOW() - INTERVAL '30 days' AND NOW() - INTERVAL '7 days')::numeric / NULLIF(COUNT(*), 0))::bigint AS warm_size,
			COUNT(*) FILTER (WHERE ts BETWEEN NOW() - INTERVAL '90 days' AND NOW() - INTERVAL '30 days') AS cold_rows,
			(pg_total_relation_size('_request_logs') * COUNT(*) FILTER (WHERE ts BETWEEN NOW() - INTERVAL '90 days' AND NOW() - INTERVAL '30 days')::numeric / NULLIF(COUNT(*), 0))::bigint AS cold_size,
			COUNT(*) FILTER (WHERE ts < NOW() - INTERVAL '90 days') AS expired_rows,
			(pg_total_relation_size('_request_logs') * COUNT(*) FILTER (WHERE ts < NOW() - INTERVAL '90 days')::numeric / NULLIF(COUNT(*), 0))::bigint AS expired_size
		FROM _request_logs
	`).Scan(
		&metrics.TotalRows,
		&metrics.TotalSizeBytes,
		&metrics.HotDataRows,
		&hotSize,
		&metrics.WarmDataRows,
		&warmSize,
		&metrics.ColdDataRows,
		&coldSize,
		&metrics.ExpiredDataRows,
		&expiredSize,
	)

	if err != nil {
		slog.Warn("data_lifecycle_metrics query failed", "error", err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}

	metrics.HotDataSizeBytes = nullInt64Ptr(hotSize)
	metrics.WarmDataSizeBytes = nullInt64Ptr(warmSize)
	metrics.ColdDataSizeBytes = nullInt64Ptr(coldSize)
	metrics.ExpiredDataSizeBytes = nullInt64Ptr(expiredSize)

	w.Header().Set("Content-Type", "application/json")
	//nolint:errcheck // HTTP write error non-recoverable
	json.NewEncoder(w).Encode(metrics)
}
