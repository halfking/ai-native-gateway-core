package admin

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"
)

// Data lifecycle stats for UI display
type dataLifecycleStats struct {
	TotalRows        int64            `json:"total_rows"`
	TotalSizeBytes   int64            `json:"total_size_bytes"`
	TotalSizeHuman   string           `json:"total_size_human"`
	HotData          *dataSegment     `json:"hot_data"`   // 0-7 days
	WarmData         *dataSegment     `json:"warm_data"`  // 7-30 days
	ColdData         *dataSegment     `json:"cold_data"`  // 30-90 days
	ExpiredData      *dataSegment     `json:"expired_data"` // >90 days
	ByTenant         []tenantDataStats `json:"by_tenant"`
	GrowthTrend      []dailyGrowth    `json:"growth_trend"`
}

type dataSegment struct {
	Rows          int64   `json:"rows"`
	SizeBytes     int64   `json:"size_bytes"`
	SizeHuman     string  `json:"size_human"`
	Days          int     `json:"days"`
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
			pg_total_relation_size('request_logs') AS total_size,
			pg_size_pretty(pg_total_relation_size('request_logs')) AS total_size_human
		FROM request_logs
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
			SELECT COUNT(*) AS total_count FROM request_logs WHERE 1=1` + tenantFilter + `
		),
		segments AS (
			SELECT 
				'hot' AS segment,
				COUNT(*) AS rows,
				(pg_total_relation_size('request_logs') * COUNT(*)::numeric / NULLIF((SELECT total_count FROM total), 0))::bigint AS size_bytes,
				pg_size_pretty((pg_total_relation_size('request_logs') * COUNT(*)::numeric / NULLIF((SELECT total_count FROM total), 0))::bigint) AS size_human
			FROM request_logs
			WHERE ts > NOW() - INTERVAL '7 days'` + tenantFilter + `
			UNION ALL
			SELECT 
				'warm',
				COUNT(*),
				(pg_total_relation_size('request_logs') * COUNT(*)::numeric / NULLIF((SELECT total_count FROM total), 0))::bigint,
				pg_size_pretty((pg_total_relation_size('request_logs') * COUNT(*)::numeric / NULLIF((SELECT total_count FROM total), 0))::bigint)
			FROM request_logs
			WHERE ts BETWEEN NOW() - INTERVAL '30 days' AND NOW() - INTERVAL '7 days'` + tenantFilter + `
			UNION ALL
			SELECT 
				'cold',
				COUNT(*),
				(pg_total_relation_size('request_logs') * COUNT(*)::numeric / NULLIF((SELECT total_count FROM total), 0))::bigint,
				pg_size_pretty((pg_total_relation_size('request_logs') * COUNT(*)::numeric / NULLIF((SELECT total_count FROM total), 0))::bigint)
			FROM request_logs
			WHERE ts BETWEEN NOW() - INTERVAL '90 days' AND NOW() - INTERVAL '30 days'` + tenantFilter + `
			UNION ALL
			SELECT 
				'expired',
				COUNT(*),
				(pg_total_relation_size('request_logs') * COUNT(*)::numeric / NULLIF((SELECT total_count FROM total), 0))::bigint,
				pg_size_pretty((pg_total_relation_size('request_logs') * COUNT(*)::numeric / NULLIF((SELECT total_count FROM total), 0))::bigint)
			FROM request_logs
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

	// 3. By tenant (top 10)
	tenantQuery := `
		SELECT 
			COALESCE(tenant_id, 'default') AS tenant,
			COUNT(*) AS rows,
			(pg_total_relation_size('request_logs') * COUNT(*)::numeric / NULLIF((SELECT COUNT(*) FROM request_logs WHERE 1=1` + tenantFilter + `), 0))::bigint AS size_bytes,
			pg_size_pretty((pg_total_relation_size('request_logs') * COUNT(*)::numeric / NULLIF((SELECT COUNT(*) FROM request_logs WHERE 1=1` + tenantFilter + `), 0))::bigint) AS size_human
		FROM request_logs
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
				continue
			}
			stats.ByTenant = append(stats.ByTenant, ts)
		}
	}

	// 4. Growth trend (last 7 days)
	trendQuery := `
		SELECT 
			DATE(ts) AS day,
			COUNT(*) AS requests,
			COUNT(*) FILTER (WHERE outbound_body IS NOT NULL) AS compressed
		FROM request_logs
		WHERE ts > NOW() - INTERVAL '7 days'` + tenantFilter + `
		GROUP BY DATE(ts)
		ORDER BY day DESC
		LIMIT 7
	`
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
				continue
			}
			dg.Date = day.Format("2006-01-02")
			if dg.Requests > 0 {
				dg.CompressionRate = float64(dg.Compressed) / float64(dg.Requests) * 100
			}
			stats.GrowthTrend = append(stats.GrowthTrend, dg)
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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
		argIdx++
	}

	var resp cleanupPreviewResponse
	err := h.db.QueryRow(ctx, `
		SELECT 
			COUNT(*) AS affected_rows,
			(pg_total_relation_size('request_logs') * COUNT(*)::numeric / NULLIF((SELECT COUNT(*) FROM request_logs), 0))::bigint AS freed_bytes,
			pg_size_pretty((pg_total_relation_size('request_logs') * COUNT(*)::numeric / NULLIF((SELECT COUNT(*) FROM request_logs), 0))::bigint) AS freed_human
		FROM request_logs
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
