package admin

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// CacheMetricsHandler handles cache metrics queries (docs/omni-ref3 D2).
type CacheMetricsHandler struct {
	db *pgxpool.Pool
}

// NewCacheMetricsHandler creates a new handler.
func NewCacheMetricsHandler(db *pgxpool.Pool) *CacheMetricsHandler {
	return &CacheMetricsHandler{db: db}
}

// RegisterRoutes registers cache metrics routes.
func (h *CacheMetricsHandler) RegisterRoutes(mux *http.ServeMux, adminWrap func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("/api/admin/cache-metrics/summary", adminWrap(h.handleSummary))
	mux.HandleFunc("/api/admin/cache-metrics/timeline", adminWrap(h.handleTimeline))
}

// handleSummary returns overall cache hit rate and tokens saved by layer.
//
// Query params:
//   - tenant_id (optional): filter by tenant
//   - hours (optional, default 24): time window in hours
//
// Response:
//
//	{
//	  "overall": {
//	    "hits": 12000,
//	    "misses": 3000,
//	    "hit_rate": 0.80,
//	    "tokens_saved": 5600000
//	  },
//	  "by_layer": {
//	    "semantic": {"hits": 5000, "misses": 1000, "hit_rate": 0.83, "tokens_saved": 2400000},
//	    "prefix": {"hits": 4000, "misses": 1200, "hit_rate": 0.77, "tokens_saved": 1800000},
//	    ...
//	  }
//	}
func (h *CacheMetricsHandler) handleSummary(w http.ResponseWriter, r *http.Request) {
	tenantID := r.URL.Query().Get("tenant_id")
	hoursStr := r.URL.Query().Get("hours")
	hours := 24
	if hoursStr != "" {
		if h, err := strconv.Atoi(hoursStr); err == nil && h > 0 {
			hours = h
		}
	}

	cutoff := time.Now().Add(-time.Duration(hours) * time.Hour)

	// Overall summary
	var overallHits, overallMisses int64
	var overallTokensSaved int64
	query := `
		SELECT
			COALESCE(SUM(CASE WHEN event_type = 'hit' THEN 1 ELSE 0 END), 0) AS hits,
			COALESCE(SUM(CASE WHEN event_type = 'miss' THEN 1 ELSE 0 END), 0) AS misses,
			COALESCE(SUM(tokens_saved), 0) AS tokens_saved
		FROM cache_metrics
		WHERE recorded_at >= $1
	`
	args := []interface{}{cutoff}
	if tenantID != "" {
		query += " AND tenant_id = $2"
		args = append(args, tenantID)
	}

	err := h.db.QueryRow(r.Context(), query, args...).Scan(&overallHits, &overallMisses, &overallTokensSaved)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "query failed"})
		return
	}

	overallTotal := overallHits + overallMisses
	overallHitRate := 0.0
	if overallTotal > 0 {
		overallHitRate = float64(overallHits) / float64(overallTotal)
	}

	// By layer summary
	query = `
		SELECT
			cache_layer,
			COALESCE(SUM(CASE WHEN event_type = 'hit' THEN 1 ELSE 0 END), 0) AS hits,
			COALESCE(SUM(CASE WHEN event_type = 'miss' THEN 1 ELSE 0 END), 0) AS misses,
			COALESCE(SUM(tokens_saved), 0) AS tokens_saved
		FROM cache_metrics
		WHERE recorded_at >= $1
	`
	args = []interface{}{cutoff}
	if tenantID != "" {
		query += " AND tenant_id = $2"
		args = append(args, tenantID)
	}
	query += " GROUP BY cache_layer"

	rows, err := h.db.Query(r.Context(), query, args...)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "query failed"})
		return
	}
	defer rows.Close()

	byLayer := make(map[string]map[string]any)
	for rows.Next() {
		var layer string
		var hits, misses, tokensSaved int64
		if err := rows.Scan(&layer, &hits, &misses, &tokensSaved); err != nil {
			continue
		}
		total := hits + misses
		hitRate := 0.0
		if total > 0 {
			hitRate = float64(hits) / float64(total)
		}
		byLayer[layer] = map[string]any{
			"hits":         hits,
			"misses":       misses,
			"hit_rate":     hitRate,
			"tokens_saved": tokensSaved,
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"overall": map[string]any{
			"hits":         overallHits,
			"misses":       overallMisses,
			"hit_rate":     overallHitRate,
			"tokens_saved": overallTokensSaved,
		},
		"by_layer": byLayer,
		"hours":    hours,
	})
}

// handleTimeline returns cache hit/miss counts over time (for charting).
//
// Query params:
//   - tenant_id (optional): filter by tenant
//   - cache_layer (optional): filter by layer
//   - hours (optional, default 24): time window
//   - bucket_minutes (optional, default 60): aggregation bucket size
//
// Response:
//
//	{
//	  "buckets": [
//	    {"timestamp": "2026-08-07T10:00:00Z", "hits": 450, "misses": 50, "tokens_saved": 120000},
//	    {"timestamp": "2026-08-07T11:00:00Z", "hits": 480, "misses": 60, "tokens_saved": 135000},
//	    ...
//	  ]
//	}
func (h *CacheMetricsHandler) handleTimeline(w http.ResponseWriter, r *http.Request) {
	tenantID := r.URL.Query().Get("tenant_id")
	cacheLayer := r.URL.Query().Get("cache_layer")
	hoursStr := r.URL.Query().Get("hours")
	bucketMinutesStr := r.URL.Query().Get("bucket_minutes")

	hours := 24
	if hoursStr != "" {
		if h, err := strconv.Atoi(hoursStr); err == nil && h > 0 {
			hours = h
		}
	}

	bucketMinutes := 60
	if bucketMinutesStr != "" {
		if b, err := strconv.Atoi(bucketMinutesStr); err == nil && b > 0 {
			bucketMinutes = b
		}
	}

	cutoff := time.Now().Add(-time.Duration(hours) * time.Hour)

	query := `
		SELECT
			date_trunc('hour', recorded_at) + 
			(EXTRACT(minute FROM recorded_at)::int / $1) * ($1 || ' minutes')::interval AS bucket,
			COALESCE(SUM(CASE WHEN event_type = 'hit' THEN 1 ELSE 0 END), 0) AS hits,
			COALESCE(SUM(CASE WHEN event_type = 'miss' THEN 1 ELSE 0 END), 0) AS misses,
			COALESCE(SUM(tokens_saved), 0) AS tokens_saved
		FROM cache_metrics
		WHERE recorded_at >= $2
	`
	args := []interface{}{bucketMinutes, cutoff}
	argIndex := 3
	if tenantID != "" {
		query += fmt.Sprintf(" AND tenant_id = $%d", argIndex)
		args = append(args, tenantID)
		argIndex++
	}
	if cacheLayer != "" {
		query += fmt.Sprintf(" AND cache_layer = $%d", argIndex)
		args = append(args, cacheLayer)
		argIndex++
	}
	query += " GROUP BY bucket ORDER BY bucket"

	rows, err := h.db.Query(r.Context(), query, args...)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "query failed"})
		return
	}
	defer rows.Close()

	type bucket struct {
		Timestamp   string `json:"timestamp"`
		Hits        int64  `json:"hits"`
		Misses      int64  `json:"misses"`
		TokensSaved int64  `json:"tokens_saved"`
	}
	var buckets []bucket
	for rows.Next() {
		var ts time.Time
		var hits, misses, tokensSaved int64
		if err := rows.Scan(&ts, &hits, &misses, &tokensSaved); err != nil {
			continue
		}
		buckets = append(buckets, bucket{
			Timestamp:   ts.Format(time.RFC3339),
			Hits:        hits,
			Misses:      misses,
			TokensSaved: tokensSaved,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{"buckets": buckets})
}
