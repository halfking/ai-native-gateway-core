package admin

import (
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"time"
)

// Model status page — 24h traffic-based availability (not probe queues).
// Spec: plan 模型状态页面 (2026-08-24).

const modelStatusWindowHours = 24

type modelStatusSummary struct {
	TotalModels         int     `json:"total_models"`
	Healthy             int     `json:"healthy"`
	Interrupted         int     `json:"interrupted"`
	NoData              int     `json:"no_data"`
	AvgAvailabilityPct  float64 `json:"avg_availability_pct"`
}

type modelStatusBucket struct {
	Hour  string `json:"hour"`
	State string `json:"state"` // ok | degraded | down | empty
}

type modelStatusRow struct {
	Name            string              `json:"name"`
	Status          string              `json:"status"` // healthy | interrupted | no_data
	AvailabilityPct float64             `json:"availability_pct"`
	AvgLatencyMs    *float64            `json:"avg_latency_ms,omitempty"`
	RequestCount    int                 `json:"request_count"`
	Buckets         []modelStatusBucket `json:"buckets"`
}

type modelStatusResponse struct {
	WindowHours int                `json:"window_hours"`
	GeneratedAt time.Time          `json:"generated_at"`
	Summary     modelStatusSummary `json:"summary"`
	Models      []modelStatusRow   `json:"models"`
}

type modelAgg struct {
	Success      int
	Total        int
	LatencySumMs float64
	LatencyN     int
}

type hourAgg struct {
	Success int
	Total   int
}

// bucketStateFromCounts maps hourly success/total to strip color states.
// empty: no traffic; ok ≥95%; degraded 50–95%; down <50%.
func bucketStateFromCounts(success, total int) string {
	if total <= 0 {
		return "empty"
	}
	rate := float64(success) / float64(total) * 100
	switch {
	case rate >= 95:
		return "ok"
	case rate >= 50:
		return "degraded"
	default:
		return "down"
	}
}

// classifyModelStatus decides row badge from 24h request volume + availability.
// interrupted ≈ availability near zero (< 1%).
func classifyModelStatus(requestCount int, availabilityPct float64) string {
	if requestCount <= 0 {
		return "no_data"
	}
	if availabilityPct < 1.0 {
		return "interrupted"
	}
	return "healthy"
}

func buildModelStatusSummary(models []modelStatusRow) modelStatusSummary {
	sum := modelStatusSummary{TotalModels: len(models)}
	var availSum float64
	var withData int
	for _, m := range models {
		switch m.Status {
		case "healthy":
			sum.Healthy++
			availSum += m.AvailabilityPct
			withData++
		case "interrupted":
			sum.Interrupted++
			availSum += m.AvailabilityPct
			withData++
		case "no_data":
			sum.NoData++
		}
	}
	if withData > 0 {
		sum.AvgAvailabilityPct = round2(availSum / float64(withData))
	}
	return sum
}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}

// assembleHourBuckets returns exactly windowHours buckets, oldest first.
func assembleHourBuckets(now time.Time, windowHours int, points map[time.Time]hourAgg) []modelStatusBucket {
	end := now.UTC().Truncate(time.Hour)
	out := make([]modelStatusBucket, 0, windowHours)
	for i := windowHours - 1; i >= 0; i-- {
		h := end.Add(-time.Duration(i) * time.Hour)
		agg := points[h]
		out = append(out, modelStatusBucket{
			Hour:  h.Format(time.RFC3339),
			State: bucketStateFromCounts(agg.Success, agg.Total),
		})
	}
	return out
}

func mergeModelStatusRows(
	now time.Time,
	catalog []string,
	stats map[string]modelAgg,
	hourPoints map[string]map[time.Time]hourAgg,
) []modelStatusRow {
	seen := make(map[string]struct{}, len(catalog)+len(stats))
	names := make([]string, 0, len(catalog)+len(stats))
	for _, n := range catalog {
		if n == "" {
			continue
		}
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		names = append(names, n)
	}
	for n := range stats {
		if n == "" {
			continue
		}
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		names = append(names, n)
	}

	rows := make([]modelStatusRow, 0, len(names))
	for _, name := range names {
		agg := stats[name]
		var avail float64
		if agg.Total > 0 {
			avail = round2(float64(agg.Success) / float64(agg.Total) * 100)
		}
		row := modelStatusRow{
			Name:            name,
			Status:          classifyModelStatus(agg.Total, avail),
			AvailabilityPct: avail,
			RequestCount:    agg.Total,
			Buckets:         assembleHourBuckets(now, modelStatusWindowHours, hourPoints[name]),
		}
		if agg.LatencyN > 0 {
			lat := round2(agg.LatencySumMs / float64(agg.LatencyN))
			row.AvgLatencyMs = &lat
		}
		rows = append(rows, row)
	}

	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].RequestCount != rows[j].RequestCount {
			return rows[i].RequestCount > rows[j].RequestCount
		}
		return rows[i].Name < rows[j].Name
	})
	return rows
}

// GET /api/admin/model-status
func (h *Handler) handleModelStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	now := time.Now().UTC()
	since := now.Add(-time.Duration(modelStatusWindowHours) * time.Hour)

	catalog, err := h.queryModelStatusCatalog(ctx)
	if err != nil {
		slog.Error("model-status catalog query failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	stats, err := h.queryModelStatusAggs(ctx, since)
	if err != nil {
		slog.Error("model-status agg query failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	hourPoints, err := h.queryModelStatusHourBuckets(ctx, since)
	if err != nil {
		slog.Error("model-status hour query failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	models := mergeModelStatusRows(now, catalog, stats, hourPoints)
	resp := modelStatusResponse{
		WindowHours: modelStatusWindowHours,
		GeneratedAt: now,
		Summary:     buildModelStatusSummary(models),
		Models:      models,
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (h *Handler) queryModelStatusCatalog(ctx context.Context) ([]string, error) {
	rows, err := h.db.Query(ctx, `
		SELECT canonical_name
		  FROM models_canonical
		 WHERE status = 'active'
		 ORDER BY canonical_name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

func (h *Handler) queryModelStatusAggs(ctx context.Context, since time.Time) (map[string]modelAgg, error) {
	rows, err := h.db.Query(ctx, `
		SELECT COALESCE(NULLIF(TRIM(client_model), ''), '(unknown)') AS model_name,
		       COUNT(*)::int AS total,
		       COUNT(*) FILTER (WHERE success IS TRUE)::int AS success_n,
		       COALESCE(SUM(latency_ms) FILTER (WHERE success IS TRUE AND latency_ms IS NOT NULL), 0)::float8 AS latency_sum,
		       COUNT(*) FILTER (WHERE success IS TRUE AND latency_ms IS NOT NULL)::int AS latency_n
		  FROM request_logs_with_current_month
		 WHERE ts >= $1
		   AND client_model IS NOT NULL
		   AND TRIM(client_model) <> ''
		 GROUP BY 1
	`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]modelAgg)
	for rows.Next() {
		var name string
		var agg modelAgg
		if err := rows.Scan(&name, &agg.Total, &agg.Success, &agg.LatencySumMs, &agg.LatencyN); err != nil {
			return nil, err
		}
		out[name] = agg
	}
	return out, rows.Err()
}

func (h *Handler) queryModelStatusHourBuckets(ctx context.Context, since time.Time) (map[string]map[time.Time]hourAgg, error) {
	rows, err := h.db.Query(ctx, `
		SELECT COALESCE(NULLIF(TRIM(client_model), ''), '(unknown)') AS model_name,
		       date_trunc('hour', ts) AS hour_bucket,
		       COUNT(*)::int AS total,
		       COUNT(*) FILTER (WHERE success IS TRUE)::int AS success_n
		  FROM request_logs_with_current_month
		 WHERE ts >= $1
		   AND client_model IS NOT NULL
		   AND TRIM(client_model) <> ''
		 GROUP BY 1, 2
	`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]map[time.Time]hourAgg)
	for rows.Next() {
		var name string
		var hour time.Time
		var total, success int
		if err := rows.Scan(&name, &hour, &total, &success); err != nil {
			return nil, err
		}
		hour = hour.UTC().Truncate(time.Hour)
		if out[name] == nil {
			out[name] = make(map[time.Time]hourAgg)
		}
		out[name][hour] = hourAgg{Success: success, Total: total}
	}
	return out, rows.Err()
}

// RegisterModelStatusRoutes mounts GET /api/admin/model-status.
func (h *Handler) RegisterModelStatusRoutes(mux *http.ServeMux, adminWrap func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("/api/admin/model-status", adminWrap(h.handleModelStatus))
}
