// Package admin — auto-route analytics endpoints (Phase 2a).
//
//	GET /api/admin/auto-route/analytics/matrix
//	GET /api/admin/auto-route/analytics/flow
//	GET /api/admin/auto-route/analytics/model-task-index
package admin

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// AnalyticsHandlers serves matrix / flow / model-task-index endpoints.
type AnalyticsHandlers struct {
	db *pgxpool.Pool
}

// NewAnalyticsHandlers constructs the analytics handler set.
func NewAnalyticsHandlers(db *pgxpool.Pool) *AnalyticsHandlers {
	return &AnalyticsHandlers{db: db}
}

// RegisterAnalyticsRoutes mounts analytics endpoints onto the admin mux.
func (h *AnalyticsHandlers) RegisterAnalyticsRoutes(mux *http.ServeMux, adminWrap func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("/api/admin/auto-route/analytics/matrix", adminWrap(h.handleMatrix))
	mux.HandleFunc("/api/admin/auto-route/analytics/flow", adminWrap(h.handleFlow))
	mux.HandleFunc("/api/admin/auto-route/analytics/model-task-index", adminWrap(h.handleModelTaskIndex))
}

func parseAnalyticsWindow(raw string) (string, time.Duration, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "7d":
		return "7d", 7 * 24 * time.Hour, nil
	case "24h":
		return "24h", 24 * time.Hour, nil
	default:
		return "", 0, fmt.Errorf("window must be 24h or 7d")
	}
}

func parseAnalyticsMetric(raw string) (string, error) {
	m := strings.ToLower(strings.TrimSpace(raw))
	if m == "" {
		return "count", nil
	}
	switch m {
	case "count", "success_rate", "p95_ms", "cost_usd":
		return m, nil
	default:
		return "", fmt.Errorf("metric must be one of: count, success_rate, p95_ms, cost_usd")
	}
}

// handleMatrix returns a task_type × outbound_model heatmap.
//
// Query params: window=24h|7d, row=task_type, metric=count|success_rate|p95_ms|cost_usd
func (h *AnalyticsHandlers) handleMatrix(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	windowLabel, windowDur, err := parseAnalyticsWindow(r.URL.Query().Get("window"))
	if err != nil {
		writeJSONErr(w, http.StatusBadRequest, err.Error())
		return
	}
	rowDim := strings.TrimSpace(r.URL.Query().Get("row"))
	if rowDim == "" {
		rowDim = "task_type"
	}
	if rowDim != "task_type" {
		writeJSONErr(w, http.StatusBadRequest, "Phase 2a only supports row=task_type")
		return
	}
	metric, err := parseAnalyticsMetric(r.URL.Query().Get("metric"))
	if err != nil {
		writeJSONErr(w, http.StatusBadRequest, err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	var metricExpr string
	switch metric {
	case "count":
		metricExpr = "COUNT(*)::float8"
	case "success_rate":
		metricExpr = "AVG(CASE WHEN success THEN 1.0 ELSE 0.0 END)"
	case "p95_ms":
		metricExpr = "percentile_cont(0.95) WITHIN GROUP (ORDER BY latency_ms)"
	case "cost_usd":
		metricExpr = "COALESCE(SUM(cost_usd), 0)"
	}

	query := fmt.Sprintf(`
		SELECT COALESCE(task_type, 'unknown') AS row_key,
		       outbound_model AS col_key,
		       %s AS val
		FROM request_logs
		WHERE is_auto_request = TRUE
		  AND ts >= NOW() - $1::interval
		  AND task_type IS NOT NULL
		  AND outbound_model IS NOT NULL
		GROUP BY task_type, outbound_model
	`, metricExpr)

	intervalStr := fmt.Sprintf("%d seconds", int(windowDur.Seconds()))
	rows, err := h.db.Query(ctx, query, intervalStr)
	if err != nil {
		writeInternalErr(w, err)
		return
	}
	defer rows.Close()

	type cellKey struct{ row, col string }
	cellMap := map[cellKey]float64{}
	rowSet := map[string]struct{}{}
	colSet := map[string]struct{}{}

	for rows.Next() {
		var rowKey, colKey string
		var val float64
		if err := rows.Scan(&rowKey, &colKey, &val); err != nil {
			continue
		}
		cellMap[cellKey{rowKey, colKey}] = val
		rowSet[rowKey] = struct{}{}
		colSet[colKey] = struct{}{}
	}

	rowKeys := make([]string, 0, len(rowSet))
	for k := range rowSet {
		rowKeys = append(rowKeys, k)
	}
	sort.Strings(rowKeys)

	colKeys := make([]string, 0, len(colSet))
	for k := range colSet {
		colKeys = append(colKeys, k)
	}
	sort.Strings(colKeys)

	cells := make([][]float64, len(rowKeys))
	for i, rk := range rowKeys {
		cells[i] = make([]float64, len(colKeys))
		for j, ck := range colKeys {
			cells[i][j] = cellMap[cellKey{rk, ck}]
		}
	}

	writeJSONOk(w, map[string]interface{}{
		"rows":  rowKeys,
		"cols":  colKeys,
		"cells": cells,
		"meta": map[string]string{
			"window": windowLabel,
			"metric": metric,
			"row":    rowDim,
		},
	})
}

// handleFlow returns a 3-layer Sankey dataset:
// task_type → outbound_model → provider_name.
func (h *AnalyticsHandlers) handleFlow(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	windowLabel, windowDur, err := parseAnalyticsWindow(r.URL.Query().Get("window"))
	if err != nil {
		writeJSONErr(w, http.StatusBadRequest, err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	intervalStr := fmt.Sprintf("%d seconds", int(windowDur.Seconds()))

	// Layer 1→2: task_type → outbound_model
	l12Query := `
		SELECT COALESCE(task_type, 'unknown') AS src,
		       outbound_model AS dst,
		       COUNT(*)::float8 AS val
		FROM request_logs
		WHERE is_auto_request = TRUE
		  AND ts >= NOW() - $1::interval
		  AND task_type IS NOT NULL
		  AND outbound_model IS NOT NULL
		GROUP BY task_type, outbound_model
	`
	l12Rows, err := h.db.Query(ctx, l12Query, intervalStr)
	if err != nil {
		writeInternalErr(w, err)
		return
	}
	type link struct {
		Source string  `json:"source"`
		Target string  `json:"target"`
		Value  float64 `json:"value"`
	}
	type node struct {
		ID    string `json:"id"`
		Label string `json:"label"`
		Layer int    `json:"layer"`
	}
	links := make([]link, 0)
	nodeMap := map[string]node{}

	for l12Rows.Next() {
		var src, dst string
		var val float64
		if err := l12Rows.Scan(&src, &dst, &val); err != nil || val <= 0 {
			continue
		}
		srcID := "task:" + src
		dstID := "model:" + dst
		nodeMap[srcID] = node{ID: srcID, Label: src, Layer: 0}
		nodeMap[dstID] = node{ID: dstID, Label: dst, Layer: 1}
		links = append(links, link{Source: srcID, Target: dstID, Value: val})
	}
	l12Rows.Close()

	// Layer 2→3: outbound_model → provider_name
	l23Query := `
		SELECT outbound_model AS src,
		       COALESCE(p.name, 'unknown') AS dst,
		       COUNT(*)::float8 AS val
		FROM request_logs rl
		LEFT JOIN providers p ON p.id = COALESCE(rl.provider_id, (
		    SELECT cr.provider_id FROM credentials cr WHERE cr.id = rl.credential_id LIMIT 1
		))
		WHERE rl.is_auto_request = TRUE
		  AND rl.ts >= NOW() - $1::interval
		  AND rl.outbound_model IS NOT NULL
		GROUP BY outbound_model, p.name
	`
	l23Rows, err := h.db.Query(ctx, l23Query, intervalStr)
	if err != nil {
		writeInternalErr(w, err)
		return
	}
	for l23Rows.Next() {
		var src, dst string
		var val float64
		if err := l23Rows.Scan(&src, &dst, &val); err != nil || val <= 0 {
			continue
		}
		srcID := "model:" + src
		dstID := "prov:" + dst
		if _, ok := nodeMap[srcID]; !ok {
			nodeMap[srcID] = node{ID: srcID, Label: src, Layer: 1}
		}
		nodeMap[dstID] = node{ID: dstID, Label: dst, Layer: 2}
		links = append(links, link{Source: srcID, Target: dstID, Value: val})
	}
	l23Rows.Close()

	nodes := make([]node, 0, len(nodeMap))
	for _, n := range nodeMap {
		nodes = append(nodes, n)
	}
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].Layer != nodes[j].Layer {
			return nodes[i].Layer < nodes[j].Layer
		}
		return nodes[i].Label < nodes[j].Label
	})

	writeJSONOk(w, map[string]interface{}{
		"nodes": nodes,
		"links": links,
		"meta": map[string]string{
			"window": windowLabel,
		},
	})
}

// handleModelTaskIndex returns the latest model_task_index bucket,
// optionally filtered by task_type, with canonical names joined.
func (h *AnalyticsHandlers) handleModelTaskIndex(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	taskType := strings.TrimSpace(r.URL.Query().Get("task_type"))
	top := 20
	if v, err := strconv.Atoi(r.URL.Query().Get("top")); err == nil && v > 0 && v <= 500 {
		top = v
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	var latestBucket sql.NullTime
	if err := h.db.QueryRow(ctx, `SELECT MAX(bucket) FROM model_task_index`).Scan(&latestBucket); err != nil {
		writeInternalErr(w, err)
		return
	}
	if !latestBucket.Valid {
		writeJSONOk(w, map[string]interface{}{
			"bucket": nil,
			"items":  []map[string]interface{}{},
			"warning": "model_task_index is empty; awaiting first bg worker refresh",
		})
		return
	}
	bucket := latestBucket.Time

	query := `
		SELECT mti.canonical_id,
		       COALESCE(mc.canonical_name, '') AS canonical_name,
		       mti.task_type,
		       mti.sample_count,
		       mti.success_rate,
		       mti.avg_latency_ms,
		       mti.p95_latency_ms,
		       mti.avg_cost_per_1k_usd,
		       mti.primary_credential_id,
		       mti.updated_at
		FROM model_task_index mti
		LEFT JOIN models_canonical mc ON mc.id = mti.canonical_id
		WHERE mti.bucket = $1
	`
	args := []interface{}{bucket}
	if taskType != "" {
		args = append(args, taskType)
		query += fmt.Sprintf(" AND mti.task_type = $%d", len(args))
	}
	args = append(args, top)
	query += fmt.Sprintf(" ORDER BY mti.sample_count DESC LIMIT $%d", len(args))

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		writeInternalErr(w, err)
		return
	}
	defer rows.Close()

	items := make([]map[string]interface{}, 0)
	for rows.Next() {
		var canonID, sampleCount, avgLatency, p95Latency *int
		var primaryCredID *int
		var canonName, task string
		var successRate, avgCost *float64
		var updatedAt time.Time
		if err := rows.Scan(&canonID, &canonName, &task, &sampleCount,
			&successRate, &avgLatency, &p95Latency, &avgCost,
			&primaryCredID, &updatedAt); err != nil {
			continue
		}
		entry := map[string]interface{}{
			"task_type": task,
		}
		if canonID != nil {
			entry["canonical_id"] = *canonID
		}
		if canonName != "" {
			entry["canonical_name"] = canonName
		}
		if sampleCount != nil {
			entry["sample_count"] = *sampleCount
		}
		if successRate != nil {
			entry["success_rate"] = *successRate
		}
		if avgLatency != nil {
			entry["avg_latency_ms"] = *avgLatency
		}
		if p95Latency != nil {
			entry["p95_latency_ms"] = *p95Latency
		}
		if avgCost != nil {
			entry["avg_cost_per_1k_usd"] = *avgCost
		}
		if primaryCredID != nil {
			entry["primary_credential_id"] = *primaryCredID
		}
		entry["updated_at"] = updatedAt.Format(time.RFC3339)
		items = append(items, entry)
	}

	writeJSONOk(w, map[string]interface{}{
		"bucket": bucket.Format(time.RFC3339),
		"items":  items,
	})
}
