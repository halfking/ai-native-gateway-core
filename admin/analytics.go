// Package admin — auto-route analytics endpoints (Phase 2a/2b).
//
//	GET /api/admin/auto-route/analytics/matrix
//	GET /api/admin/auto-route/analytics/flow
//	GET /api/admin/auto-route/analytics/model-task-index
//	GET /api/admin/auto-route/analytics/decision/{request_id}
//	GET /api/admin/auto-route/analytics/funnel
package admin

import (
	"context"
	"database/sql"
	"encoding/json"
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
	mux.HandleFunc("/api/admin/auto-route/analytics/funnel", adminWrap(h.handleFunnel))
	mux.HandleFunc("/api/admin/auto-route/analytics/decision/", adminWrap(h.handleDecisionReplay))
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

// handleMatrix returns a row_dim × canonical_model heatmap.
//
// Query params: window=24h|7d, row=task_type|work_type, metric=count|success_rate|p95_ms|cost_usd
// Column keys are canonical model names; meta.col_aliases maps canonical → raw outbound names.
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
	if rowDim != "task_type" && rowDim != "work_type" {
		writeJSONErr(w, http.StatusBadRequest, "row must be task_type or work_type")
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

	rowExpr := "COALESCE(task_type, 'unknown')"
	rowNullFilter := "task_type IS NOT NULL"
	if rowDim == "work_type" {
		rowExpr = "COALESCE(NULLIF(work_type, ''), 'unknown')"
		rowNullFilter = "work_type IS NOT NULL AND work_type <> ''"
	}

	query := fmt.Sprintf(`
		SELECT %s AS row_key,
		       outbound_model AS col_key,
		       %s AS val
		FROM request_logs
		WHERE is_auto_request = TRUE
		  AND ts >= NOW() - $1::interval
		  AND %s
		  AND outbound_model IS NOT NULL
		GROUP BY row_key, outbound_model
	`, rowExpr, metricExpr, rowNullFilter)

	intervalStr := fmt.Sprintf("%d seconds", int(windowDur.Seconds()))
	rows, err := h.db.Query(ctx, query, intervalStr)
	if err != nil {
		writeInternalErr(w, err)
		return
	}
	defer rows.Close()

	type cellKey struct{ row, col string }
	cellMap := map[cellKey]float64{}
	rawColSet := map[string]struct{}{}

	for rows.Next() {
		var rowKey, colKey string
		var val float64
		if err := rows.Scan(&rowKey, &colKey, &val); err != nil {
			continue
		}
		rawColSet[colKey] = struct{}{}
		cellMap[cellKey{rowKey, colKey}] = val
	}

	aliasIdx, _ := loadModelAliasIndex(ctx, h.db)
	canonColAliases := map[string][]string{}
	canonColSet := map[string]struct{}{}
	rawToCanon := map[string]string{}
	for raw := range rawColSet {
		canon := raw
		if aliasIdx != nil {
			canon = aliasIdx.canonicalFor(raw)
		}
		if canon == "" {
			canon = normalizeModelName(raw)
		}
		rawToCanon[raw] = canon
		canonColSet[canon] = struct{}{}
		canonColAliases[canon] = appendUnique(canonColAliases[canon], raw)
	}

	rowSet := map[string]struct{}{}
	for k := range cellMap {
		rowSet[k.row] = struct{}{}
	}
	rowKeys := make([]string, 0, len(rowSet))
	for k := range rowSet {
		rowKeys = append(rowKeys, k)
	}
	sort.Strings(rowKeys)

	colKeys := make([]string, 0, len(canonColSet))
	for k := range canonColSet {
		colKeys = append(colKeys, k)
	}
	sort.Strings(colKeys)

	cells := make([][]float64, len(rowKeys))
	for i, rk := range rowKeys {
		cells[i] = make([]float64, len(colKeys))
		for j, ck := range colKeys {
			var sum float64
			for raw, canon := range rawToCanon {
				if canon != ck {
					continue
				}
				sum += cellMap[cellKey{rk, raw}]
			}
			cells[i][j] = sum
		}
	}

	writeJSONOk(w, map[string]interface{}{
		"rows":  rowKeys,
		"cols":  colKeys,
		"cells": cells,
		"meta": map[string]interface{}{
			"window":      windowLabel,
			"metric":      metric,
			"row":         rowDim,
			"col_aliases": canonColAliases,
		},
	})
}

// handleFlow returns a 3-layer Sankey dataset:
// task_type → canonical_model → provider_name.
// Middle column uses standard/canonical model names (same as matrix cols), not raw outbound names.
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

	aliasIdx, _ := loadModelAliasIndex(ctx, h.db)
	canonModel := func(raw string) string {
		canon := raw
		if aliasIdx != nil {
			canon = aliasIdx.canonicalFor(raw)
		}
		if canon == "" {
			canon = normalizeModelName(raw)
		}
		return canon
	}

	intervalStr := fmt.Sprintf("%d seconds", int(windowDur.Seconds()))

	// Layer 1→2: task_type → outbound_model (aggregated to canonical in Go)
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
		Source   string  `json:"source"`
		Target   string  `json:"target"`
		Value    float64 `json:"value"`
		TaskType string  `json:"task_type"`
	}
	type node struct {
		ID    string `json:"id"`
		Label string `json:"label"`
		Layer int    `json:"layer"`
	}
	type linkKey struct {
		source, target string
	}
	type l23Key struct {
		source, target, taskType string
	}
	links := make([]link, 0)
	nodeMap := map[string]node{}
	l12Agg := map[linkKey]float64{}

	for l12Rows.Next() {
		var src, dstRaw string
		var val float64
		if err := l12Rows.Scan(&src, &dstRaw, &val); err != nil || val <= 0 {
			continue
		}
		dstCanon := canonModel(dstRaw)
		srcID := "task:" + src
		dstID := "model:" + dstCanon
		nodeMap[srcID] = node{ID: srcID, Label: src, Layer: 0}
		nodeMap[dstID] = node{ID: dstID, Label: dstCanon, Layer: 1}
		k := linkKey{source: srcID, target: dstID}
		l12Agg[k] += val
	}
	l12Rows.Close()
	for k, val := range l12Agg {
		taskType := strings.TrimPrefix(k.source, "task:")
		links = append(links, link{Source: k.source, Target: k.target, Value: val, TaskType: taskType})
	}

	// Layer 2→3: task_type × canonical_model → provider_name
	// Grouped by task_type so each link carries the original task color.
	// NOTE: providers table column is display_name, NOT name.
	l23Query := `
		SELECT COALESCE(rl.task_type, 'unknown') AS task_type,
		       rl.outbound_model AS src,
		       COALESCE(p.display_name, 'unknown') AS dst,
		       COUNT(*)::float8 AS val
		FROM request_logs rl
		LEFT JOIN providers p ON p.id = COALESCE(rl.provider_id, (
		    SELECT cr.provider_id FROM credentials cr WHERE cr.id = rl.credential_id LIMIT 1
		))
		WHERE rl.is_auto_request = TRUE
		  AND rl.ts >= NOW() - $1::interval
		  AND rl.outbound_model IS NOT NULL
		  AND rl.task_type IS NOT NULL
		GROUP BY rl.task_type, rl.outbound_model, p.display_name
	`
	l23Rows, err := h.db.Query(ctx, l23Query, intervalStr)
	if err != nil {
		writeInternalErr(w, err)
		return
	}
	l23Agg := map[l23Key]float64{}
	for l23Rows.Next() {
		var taskType, srcRaw, dst string
		var val float64
		if err := l23Rows.Scan(&taskType, &srcRaw, &dst, &val); err != nil || val <= 0 {
			continue
		}
		srcCanon := canonModel(srcRaw)
		srcID := "model:" + srcCanon
		dstID := "prov:" + dst
		nodeMap[srcID] = node{ID: srcID, Label: srcCanon, Layer: 1}
		nodeMap[dstID] = node{ID: dstID, Label: dst, Layer: 2}
		k := l23Key{source: srcID, target: dstID, taskType: taskType}
		l23Agg[k] += val
	}
	l23Rows.Close()
	for k, val := range l23Agg {
		links = append(links, link{Source: k.source, Target: k.target, Value: val, TaskType: k.taskType})
	}

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

// handleDecisionReplay returns L1 (auto_decision) + L2 (routing_decision_log) for one request.
//
// Path: /api/admin/auto-route/analytics/decision/{request_id}
func (h *AnalyticsHandlers) handleDecisionReplay(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	prefix := "/api/admin/auto-route/analytics/decision/"
	reqID := strings.TrimSpace(strings.TrimPrefix(r.URL.Path, prefix))
	if reqID == "" {
		writeJSONErr(w, http.StatusBadRequest, "request_id required")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	var ts time.Time
	var taskType, prof, clientModel, outbound string
	var apiKeyID, credentialID *int
	var confidence *float64
	var autoDecision *string
	var success bool
	var latency *int

	err := h.db.QueryRow(ctx, `
		SELECT ts, task_type, auto_profile, auto_confidence,
		       client_model, outbound_model, api_key_id, credential_id,
		       auto_decision, success, latency_ms
		FROM request_logs
		WHERE request_id = $1::uuid
		LIMIT 1
	`, reqID).Scan(
		&ts, &taskType, &prof, &confidence,
		&clientModel, &outbound, &apiKeyID, &credentialID,
		&autoDecision, &success, &latency,
	)
	if err == sql.ErrNoRows {
		writeJSONErr(w, http.StatusNotFound, "request not found")
		return
	}
	if err != nil {
		writeInternalErr(w, err)
		return
	}

	out := map[string]interface{}{
		"request_id":     reqID,
		"ts":             ts.Format(time.RFC3339),
		"success":        success,
		"client_model":   clientModel,
		"outbound_model": outbound,
	}
	if apiKeyID != nil {
		out["api_key_id"] = *apiKeyID
	}
	if credentialID != nil {
		out["credential_id"] = *credentialID
	}
	if latency != nil {
		out["latency_ms"] = *latency
	}

	l1 := map[string]interface{}{
		"task_type": taskType,
		"profile":   prof,
	}
	if confidence != nil {
		l1["confidence"] = *confidence
	}
	if autoDecision != nil {
		var parsed map[string]interface{}
		if err := json.Unmarshal([]byte(*autoDecision), &parsed); err == nil {
			for k, v := range parsed {
				l1[k] = v
			}
		}
	}
	out["l1"] = l1

	var rdlTS time.Time
	var chosenCredID, chosenProvID, tier, candidatesTried *int
	var rdlSuccess bool
	var resolutionPath, canonicalModel *string
	var decisionTrace *string

	rdlErr := h.db.QueryRow(ctx, `
		SELECT ts, chosen_credential_id, chosen_provider_id, tier,
		       candidates_tried, success, resolution_path, canonical_model,
		       decision_trace::text
		FROM routing_decision_log
		WHERE request_id = $1::uuid
		ORDER BY ts DESC
		LIMIT 1
	`, reqID).Scan(
		&rdlTS, &chosenCredID, &chosenProvID, &tier,
		&candidatesTried, &rdlSuccess, &resolutionPath, &canonicalModel,
		&decisionTrace,
	)
	if rdlErr == nil {
		l2 := map[string]interface{}{
			"ts":      rdlTS.Format(time.RFC3339),
			"success": rdlSuccess,
		}
		if chosenCredID != nil {
			l2["chosen_credential_id"] = *chosenCredID
		}
		if chosenProvID != nil {
			l2["chosen_provider_id"] = *chosenProvID
		}
		if tier != nil {
			l2["tier"] = *tier
		}
		if candidatesTried != nil {
			l2["candidates_tried"] = *candidatesTried
		}
		if resolutionPath != nil {
			l2["resolution_path"] = *resolutionPath
		}
		if canonicalModel != nil {
			l2["canonical_model"] = *canonicalModel
		}
		if decisionTrace != nil && *decisionTrace != "" && *decisionTrace != "{}" {
			var trace map[string]interface{}
			if err := json.Unmarshal([]byte(*decisionTrace), &trace); err == nil {
				l2["decision_trace"] = trace
			}
		}
		out["l2"] = l2
	} else if rdlErr != sql.ErrNoRows {
		writeInternalErr(w, rdlErr)
		return
	}

	writeJSONOk(w, out)
}

// handleFunnel aggregates L2 credential funnel stats for a model in a time window.
//
// Statistics口径:
//   - exact (routing_decision_log): sums decision_trace.planned_candidates / blocked_candidates
//     per request; routable = planned − blocked; success = rows where success=true.
//   - approximate (request_logs): used when no RDL rows match; planned≈requests×3, routable≈routed×2.
//   - mixed: RDL has request count but empty traces; request_logs supplements stage totals.
//
// Query params: model (required, canonical or raw alias), window=24h|7d
func (h *AnalyticsHandlers) handleFunnel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	model := strings.TrimSpace(r.URL.Query().Get("model"))
	if model == "" {
		writeJSONErr(w, http.StatusBadRequest, "model parameter required")
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

	cacheKey := funnelCacheKey(model, windowLabel)
	if cached, ok := globalFunnelCache.get(cacheKey); ok {
		writeJSONOk(w, cached)
		return
	}

	modelNames, _ := expandModelFilter(ctx, h.db, model)

	type funnelRow struct {
		requests     int
		traceRows    int
		totalPlanned int
		totalBlocked int
		routable     int
		chosen       int
		success      int
	}
	var fr funnelRow
	dataSource := "exact"

	// Primary: routing_decision_log decision_trace aggregates.
	rdlQuery := `
		SELECT
			COUNT(*)::int,
			COUNT(*) FILTER (
				WHERE decision_trace IS NOT NULL
				  AND decision_trace <> '{}'::jsonb
				  AND jsonb_array_length(COALESCE(decision_trace->'planned_candidates', '[]'::jsonb)) > 0
			)::int,
			COALESCE(SUM(jsonb_array_length(COALESCE(decision_trace->'planned_candidates', '[]'::jsonb))), 0)::int,
			COALESCE(SUM(jsonb_array_length(COALESCE(decision_trace->'blocked_candidates', '[]'::jsonb))), 0)::int,
			COALESCE(SUM(GREATEST(
				jsonb_array_length(COALESCE(decision_trace->'planned_candidates', '[]'::jsonb))
				- jsonb_array_length(COALESCE(decision_trace->'blocked_candidates', '[]'::jsonb)),
				0
			)), 0)::int,
			COUNT(*) FILTER (WHERE chosen_credential_id IS NOT NULL)::int,
			COUNT(*) FILTER (WHERE success IS TRUE)::int
		FROM routing_decision_log
		WHERE ts >= NOW() - $1::interval
		  AND (
		    outbound_model = ANY($2) OR canonical_model = ANY($2)
		    OR client_model = ANY($2) OR model = ANY($2)
		  )
	`
	_ = h.db.QueryRow(ctx, rdlQuery, intervalStr, modelNames).Scan(
		&fr.requests, &fr.traceRows, &fr.totalPlanned, &fr.totalBlocked,
		&fr.routable, &fr.chosen, &fr.success,
	)

	if fr.requests == 0 {
		dataSource = "approximate"
		var autoReq, routed, ok int
		if err := h.db.QueryRow(ctx, `
			SELECT
				COUNT(*)::int,
				COUNT(*) FILTER (WHERE credential_id IS NOT NULL)::int,
				COUNT(*) FILTER (WHERE success IS TRUE)::int
			FROM request_logs
			WHERE is_auto_request = TRUE
			  AND ts >= NOW() - $1::interval
			  AND outbound_model = ANY($2)
		`, intervalStr, modelNames).Scan(&autoReq, &routed, &ok); err != nil {
			writeInternalErr(w, err)
			return
		}
		fr.requests = autoReq
		fr.chosen = routed
		fr.success = ok
		if autoReq > 0 {
			fr.totalPlanned = autoReq * 3
			fr.routable = routed * 2
			if fr.routable < routed {
				fr.routable = routed
			}
		}
	} else if fr.totalPlanned == 0 {
		// RDL rows exist but traces empty — supplement from request_logs.
		dataSource = "mixed"
		var autoReq, routed, ok int
		_ = h.db.QueryRow(ctx, `
			SELECT
				COUNT(*)::int,
				COUNT(*) FILTER (WHERE credential_id IS NOT NULL)::int,
				COUNT(*) FILTER (WHERE success IS TRUE)::int
			FROM request_logs
			WHERE is_auto_request = TRUE
			  AND ts >= NOW() - $1::interval
			  AND outbound_model = ANY($2)
		`, intervalStr, modelNames).Scan(&autoReq, &routed, &ok)
		if fr.requests == 0 {
			fr.requests = autoReq
		}
		if fr.chosen == 0 {
			fr.chosen = routed
		}
		if fr.success == 0 {
			fr.success = ok
		}
		if fr.totalPlanned == 0 && autoReq > 0 {
			fr.totalPlanned = autoReq * 3
			fr.routable = routed * 2
			if fr.routable < routed {
				fr.routable = routed
			}
		}
	}

	stages := []map[string]interface{}{
		{"key": "candidates", "label": "总候选", "value": fr.totalPlanned, "hint": "L2 计划候选数累计"},
		{"key": "routable", "label": "可路由", "value": fr.routable, "hint": "计划候选 − 被阻断"},
		{"key": "success", "label": "执行成功", "value": fr.success, "hint": "最终成功请求数"},
	}
	if fr.totalPlanned == 0 && fr.requests > 0 {
		stages[0]["value"] = fr.requests
		stages[0]["hint"] = "无 trace 时以请求数为基准"
	}

	confidence := "low"
	confidenceHint := "样本不足或仅有 request_logs 近似估算"
	sampleN := fr.requests
	traceRatio := 0.0
	if fr.requests > 0 {
		traceRatio = float64(fr.traceRows) / float64(fr.requests)
	}
	switch {
	case dataSource == "exact" && fr.requests >= 30 && traceRatio >= 0.8:
		confidence = "high"
		confidenceHint = fmt.Sprintf("n=%d，%.0f%% 含完整 decision_trace", fr.requests, traceRatio*100)
	case dataSource == "exact" && fr.requests >= 10:
		confidence = "medium"
		confidenceHint = fmt.Sprintf("n=%d，trace 覆盖率 %.0f%%", fr.requests, traceRatio*100)
	case dataSource == "mixed":
		confidence = "medium"
		confidenceHint = fmt.Sprintf("n=%d，RDL 与 request_logs 混合估算", fr.requests)
	default:
		if fr.requests > 0 {
			confidenceHint = fmt.Sprintf("n=%d，数据为近似估算", fr.requests)
		}
	}

	out := map[string]interface{}{
		"model":    model,
		"window":   windowLabel,
		"requests": fr.requests,
		"stages":   stages,
		"meta": map[string]interface{}{
			"approximate":     dataSource != "exact",
			"data_source":     dataSource,
			"blocked":         fr.totalBlocked,
			"chosen":          fr.chosen,
			"sample_n":        sampleN,
			"trace_rows":      fr.traceRows,
			"trace_ratio":     traceRatio,
			"confidence":      confidence,
			"confidence_hint": confidenceHint,
		},
	}
	globalFunnelCache.set(cacheKey, out)
	writeJSONOk(w, out)
}
