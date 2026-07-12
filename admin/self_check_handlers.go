package admin

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// SelfCheckHandler serves /api/self-check/* endpoints.
type SelfCheckHandler struct {
	db     *pgxpool.Pool
	worker interface {
		TriggerManualRun(model string)
	}
}

func NewSelfCheckHandler(db *pgxpool.Pool) *SelfCheckHandler {
	return &SelfCheckHandler{db: db}
}

// SetWorker is called after the worker is created to enable manual triggering.
func (h *SelfCheckHandler) SetWorker(w interface{ TriggerManualRun(model string) }) {
	h.worker = w
}

// RegisterRoutes registers the self-check admin routes.
func (h *SelfCheckHandler) RegisterRoutes(mux *http.ServeMux, admin, superAdmin func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("/api/self-check/runs", admin(h.handleListRuns))
	mux.HandleFunc("/api/self-check/runs/", admin(h.handleGetRun))
	mux.HandleFunc("/api/self-check/settings", admin(h.handleGetSettings))
	mux.HandleFunc("/api/self-check/settings/update", superAdmin(h.handleUpdateSettings))
	mux.HandleFunc("/api/self-check/trigger", superAdmin(h.handleTrigger))
	mux.HandleFunc("/api/self-check/stats", admin(h.handleStats))
	mux.HandleFunc("/api/self-check/models", admin(h.handleModels))
}

// --- List Runs ---

type scRun struct {
	ID                int64      `json:"id"`
	ModelName         string     `json:"model_name"`
	StartedAt         time.Time  `json:"started_at"`
	CompletedAt       *time.Time `json:"completed_at,omitempty"`
	DurationMs        int        `json:"duration_ms"`
	Status            string     `json:"status"`
	RoundsTotal       int        `json:"rounds_total"`
	RoundsSuccess     int        `json:"rounds_success"`
	HadToolCall       bool       `json:"had_tool_call"`
	TotalTokens       int        `json:"total_tokens"`
	AvgLatencyMs      int        `json:"avg_latency_ms"`
	ErrorType         string     `json:"error_type,omitempty"`
	ErrorDetail       string     `json:"error_detail,omitempty"`
	UpstreamTested    bool       `json:"upstream_tested"`
	UpstreamResult    string     `json:"upstream_result,omitempty"`
	UpstreamLatencyMs int        `json:"upstream_latency_ms,omitempty"`
	UpstreamError     string     `json:"upstream_error,omitempty"`
}

func (h *SelfCheckHandler) handleListRuns(w http.ResponseWriter, r *http.Request) {
	model := r.URL.Query().Get("model")
	status := r.URL.Query().Get("status")
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}

	query := `SELECT id, model_name, started_at, completed_at, duration_ms,
		status, rounds_total, rounds_success, had_tool_call,
		total_tokens, avg_latency_ms,
		COALESCE(error_type,'') AS error_type, COALESCE(error_detail,'') AS error_detail,
		upstream_tested, COALESCE(upstream_result,'') AS upstream_result,
		COALESCE(upstream_latency_ms,0) AS upstream_latency_ms,
		COALESCE(upstream_error,'') AS upstream_error
		FROM self_check_runs WHERE 1=1`
	args := []any{}
	argIdx := 1

	if model != "" {
		query += " AND model_name = $" + strconv.Itoa(argIdx)
		args = append(args, model)
		argIdx++
	}
	if status != "" {
		query += " AND status = $" + strconv.Itoa(argIdx)
		args = append(args, status)
		argIdx++
	}
	query += " ORDER BY started_at DESC LIMIT $" + strconv.Itoa(argIdx)
	args = append(args, limit)

	rows, err := h.db.Query(r.Context(), query, args...)
	if err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	defer rows.Close()

	var items []scRun
	for rows.Next() {
		var item scRun
		if err := rows.Scan(&item.ID, &item.ModelName, &item.StartedAt, &item.CompletedAt,
			&item.DurationMs, &item.Status, &item.RoundsTotal, &item.RoundsSuccess,
			&item.HadToolCall, &item.TotalTokens, &item.AvgLatencyMs,
			&item.ErrorType, &item.ErrorDetail, &item.UpstreamTested,
			&item.UpstreamResult, &item.UpstreamLatencyMs, &item.UpstreamError); err != nil {
			slog.Error("self_check: scan run failed", "error", err)
			continue
		}
		items = append(items, item)
	}

	// Total count.
	var total int
	h.db.QueryRow(r.Context(), `SELECT COUNT(*) FROM self_check_runs`).Scan(&total)

	writeJSON(w, 200, map[string]any{"items": items, "total": total})
}

// --- Get Run ---

func (h *SelfCheckHandler) handleGetRun(w http.ResponseWriter, r *http.Request) {
	// Extract ID from path: /api/self-check/runs/{id}
	idStr := r.URL.Path[len("/api/self-check/runs/"):]
	if idStr == "" {
		writeJSON(w, 400, map[string]any{"error": "missing run id"})
		return
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeJSON(w, 400, map[string]any{"error": "invalid run id"})
		return
	}

	// Get run.
	var run scRun
	err = h.db.QueryRow(r.Context(), `
		SELECT id, model_name, started_at, completed_at, duration_ms,
		status, rounds_total, rounds_success, had_tool_call,
		total_tokens, avg_latency_ms,
		COALESCE(error_type,'') AS error_type, COALESCE(error_detail,'') AS error_detail,
		upstream_tested, COALESCE(upstream_result,'') AS upstream_result,
		COALESCE(upstream_latency_ms,0) AS upstream_latency_ms,
		COALESCE(upstream_error,'') AS upstream_error
		FROM self_check_runs WHERE id=$1`, id).Scan(
		&run.ID, &run.ModelName, &run.StartedAt, &run.CompletedAt,
		&run.DurationMs, &run.Status, &run.RoundsTotal, &run.RoundsSuccess,
		&run.HadToolCall, &run.TotalTokens, &run.AvgLatencyMs,
		&run.ErrorType, &run.ErrorDetail, &run.UpstreamTested,
		&run.UpstreamResult, &run.UpstreamLatencyMs, &run.UpstreamError)
	if err != nil {
		writeJSON(w, 404, map[string]any{"error": "run not found"})
		return
	}

	// Get rounds.
	rows, err := h.db.Query(r.Context(), `
		SELECT id, round_index, is_ping, is_tool_call, latency_ms,
		prompt_tokens, completion_tokens, total_tokens,
		success, http_code, COALESCE(error_message,'') AS error_message,
		COALESCE(request_body,'') AS request_body,
		COALESCE(response_preview,'') AS response_preview,
		created_at
		FROM self_check_round_results WHERE run_id=$1 ORDER BY round_index`, id)
	if err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	defer rows.Close()

	type round struct {
		ID               int64     `json:"id"`
		RoundIndex       int       `json:"round_index"`
		IsPing           bool      `json:"is_ping"`
		IsToolCall       bool      `json:"is_tool_call"`
		LatencyMs        int       `json:"latency_ms"`
		PromptTokens     int       `json:"prompt_tokens"`
		CompletionTokens int       `json:"completion_tokens"`
		TotalTokens      int       `json:"total_tokens"`
		Success          bool      `json:"success"`
		HTTPCode         int       `json:"http_code"`
		ErrorMessage     string    `json:"error_message"`
		RequestBody      string    `json:"request_body"`
		ResponsePreview  string    `json:"response_preview"`
		CreatedAt        time.Time `json:"created_at"`
	}
	var rounds []round
	for rows.Next() {
		var rd round
		if err := rows.Scan(&rd.ID, &rd.RoundIndex, &rd.IsPing, &rd.IsToolCall,
			&rd.LatencyMs, &rd.PromptTokens, &rd.CompletionTokens, &rd.TotalTokens,
			&rd.Success, &rd.HTTPCode, &rd.ErrorMessage, &rd.RequestBody,
			&rd.ResponsePreview, &rd.CreatedAt); err != nil {
			continue
		}
		rounds = append(rounds, rd)
	}

	writeJSON(w, 200, map[string]any{"run": run, "rounds": rounds})
}

// --- Settings ---

func (h *SelfCheckHandler) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	var s struct {
		Enabled        bool            `json:"enabled"`
		NormalInterval int             `json:"normal_interval_seconds"`
		FaultInterval  int             `json:"fault_interval_seconds"`
		ModelSource    string          `json:"model_source"`
		MaxModels      int             `json:"max_models"`
		MaxTokens      int             `json:"max_tokens_per_run"`
		FeaturedModels json.RawMessage `json:"featured_model_ids"`
		UpdatedAt      time.Time       `json:"updated_at"`
		UpdatedBy      *string         `json:"updated_by,omitempty"`
	}
	err := h.db.QueryRow(r.Context(), `
		SELECT enabled, normal_interval_seconds, fault_interval_seconds,
		model_source, max_models, max_tokens_per_run, featured_model_ids,
		updated_at, updated_by
		FROM self_check_settings WHERE id=1`,
	).Scan(&s.Enabled, &s.NormalInterval, &s.FaultInterval,
		&s.ModelSource, &s.MaxModels, &s.MaxTokens, &s.FeaturedModels,
		&s.UpdatedAt, &s.UpdatedBy)
	if err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, 200, s)
}

func (h *SelfCheckHandler) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		writeJSON(w, 405, map[string]any{"error": "method not allowed"})
		return
	}
	var body struct {
		Enabled        *bool           `json:"enabled"`
		NormalInterval *int            `json:"normal_interval_seconds"`
		FaultInterval  *int            `json:"fault_interval_seconds"`
		ModelSource    *string         `json:"model_source"`
		MaxModels      *int            `json:"max_models"`
		MaxTokens      *int            `json:"max_tokens_per_run"`
		FeaturedModels json.RawMessage `json:"featured_model_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]any{"error": "invalid json"})
		return
	}

	// Build dynamic SET clauses.
	sets := []string{}
	args := []any{}
	argIdx := 1

	if body.Enabled != nil {
		sets = append(sets, "enabled=$"+strconv.Itoa(argIdx))
		args = append(args, *body.Enabled)
		argIdx++
	}
	if body.NormalInterval != nil {
		sets = append(sets, "normal_interval_seconds=$"+strconv.Itoa(argIdx))
		args = append(args, *body.NormalInterval)
		argIdx++
	}
	if body.FaultInterval != nil {
		sets = append(sets, "fault_interval_seconds=$"+strconv.Itoa(argIdx))
		args = append(args, *body.FaultInterval)
		argIdx++
	}
	if body.ModelSource != nil {
		sets = append(sets, "model_source=$"+strconv.Itoa(argIdx))
		args = append(args, *body.ModelSource)
		argIdx++
	}
	if body.MaxModels != nil {
		sets = append(sets, "max_models=$"+strconv.Itoa(argIdx))
		args = append(args, *body.MaxModels)
		argIdx++
	}
	if body.MaxTokens != nil {
		sets = append(sets, "max_tokens_per_run=$"+strconv.Itoa(argIdx))
		args = append(args, *body.MaxTokens)
		argIdx++
	}
	if len(body.FeaturedModels) > 0 {
		sets = append(sets, "featured_model_ids=$"+strconv.Itoa(argIdx))
		args = append(args, body.FeaturedModels)
		argIdx++
	}

	if len(sets) == 0 {
		writeJSON(w, 400, map[string]any{"error": "no fields to update"})
		return
	}

	sets = append(sets, "updated_at=now()")
	sets = append(sets, "updated_by=$"+strconv.Itoa(argIdx))
	args = append(args, "admin")
	argIdx++

	query := "UPDATE self_check_settings SET " + joinStringsSC(sets, ",") + " WHERE id=1"
	_, err := h.db.Exec(r.Context(), query, args...)
	if err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// --- Trigger ---

func (h *SelfCheckHandler) handleTrigger(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]any{"error": "method not allowed"})
		return
	}
	var body struct {
		Model string `json:"model"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	if h.worker == nil {
		writeJSON(w, 503, map[string]any{"error": "worker not available", "message": "self-check worker is not initialized"})
		return
	}
	h.worker.TriggerManualRun(body.Model)
	writeJSON(w, 200, map[string]any{"ok": true, "message": "manual trigger queued", "model": body.Model})
}

// --- Stats ---

func (h *SelfCheckHandler) handleStats(w http.ResponseWriter, r *http.Request) {
	rangeParam := r.URL.Query().Get("range")
	if rangeParam == "" {
		rangeParam = "24h"
	}

	var since time.Time
	switch rangeParam {
	case "1h":
		since = time.Now().Add(-1 * time.Hour)
	case "6h":
		since = time.Now().Add(-6 * time.Hour)
	case "24h":
		since = time.Now().Add(-24 * time.Hour)
	case "7d":
		since = time.Now().Add(-7 * 24 * time.Hour)
	default:
		since = time.Now().Add(-24 * time.Hour)
	}

	// Summary.
	type summary struct {
		TotalRuns   int     `json:"total_runs"`
		SuccessRuns int     `json:"success_runs"`
		PartialRuns int     `json:"partial_runs"`
		FailedRuns  int     `json:"failed_runs"`
		SuccessRate float64 `json:"success_rate"`
	}
	var s summary
	h.db.QueryRow(r.Context(), `
		SELECT COUNT(*),
		COUNT(*) FILTER (WHERE status='success'),
		COUNT(*) FILTER (WHERE status='partial'),
		COUNT(*) FILTER (WHERE status='failed')
		FROM self_check_runs WHERE started_at >= $1`, since).Scan(
		&s.TotalRuns, &s.SuccessRuns, &s.PartialRuns, &s.FailedRuns)
	if s.TotalRuns > 0 {
		s.SuccessRate = float64(s.SuccessRuns) / float64(s.TotalRuns)
	}

	// Per-model stats.
	rows, err := h.db.Query(r.Context(), `
		SELECT model_name, COUNT(*),
		COUNT(*) FILTER (WHERE status='success'),
		COUNT(*) FILTER (WHERE status='partial'),
		COUNT(*) FILTER (WHERE status='failed'),
		COALESCE(AVG(avg_latency_ms),0)::int
		FROM self_check_runs WHERE started_at >= $1
		GROUP BY model_name
		ORDER BY model_name`, since)
	if err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	defer rows.Close()

	type modelStat struct {
		Model       string  `json:"model_name"`
		Total       int     `json:"total"`
		Success     int     `json:"success"`
		Partial     int     `json:"partial"`
		Failed      int     `json:"failed"`
		SuccessRate float64 `json:"success_rate"`
		AvgLatency  int     `json:"avg_latency_ms"`
	}
	var byModel []modelStat
	for rows.Next() {
		var ms modelStat
		if err := rows.Scan(&ms.Model, &ms.Total, &ms.Success, &ms.Partial, &ms.Failed, &ms.AvgLatency); err != nil {
			continue
		}
		if ms.Total > 0 {
			ms.SuccessRate = float64(ms.Success) / float64(ms.Total)
		}
		byModel = append(byModel, ms)
	}

	// Error breakdown.
	errRows, _ := h.db.Query(r.Context(), `
		SELECT COALESCE(error_type,'none'), COUNT(*)
		FROM self_check_runs WHERE started_at >= $1 AND status != 'success'
		GROUP BY error_type ORDER BY COUNT(*) DESC`, since)
	type errStat struct {
		ErrorType string `json:"error_type"`
		Count     int    `json:"count"`
	}
	var errBreakdown []errStat
	if errRows != nil {
		defer errRows.Close()
		for errRows.Next() {
			var es errStat
			if errRows.Scan(&es.ErrorType, &es.Count) == nil {
				errBreakdown = append(errBreakdown, es)
			}
		}
	}

	// Trend (hourly buckets).
	trendRows, _ := h.db.Query(r.Context(), `
		SELECT date_trunc('hour', started_at) AS ts,
		COUNT(*) FILTER (WHERE status='success')::float / NULLIF(COUNT(*),0)::float AS success_rate,
		COUNT(*) AS total
		FROM self_check_runs WHERE started_at >= $1
		GROUP BY date_trunc('hour', started_at)
		ORDER BY ts`, since)
	type trendPoint struct {
		Timestamp   string  `json:"timestamp"`
		SuccessRate float64 `json:"success_rate"`
		Total       int     `json:"total"`
	}
	var trend []trendPoint
	if trendRows != nil {
		defer trendRows.Close()
		for trendRows.Next() {
			var tp trendPoint
			var ts time.Time
			if trendRows.Scan(&ts, &tp.SuccessRate, &tp.Total) == nil {
				tp.Timestamp = ts.Format(time.RFC3339)
				trend = append(trend, tp)
			}
		}
	}

	writeJSON(w, 200, map[string]any{
		"range":           rangeParam,
		"summary":         s,
		"by_model":        byModel,
		"error_breakdown": errBreakdown,
		"trend":           trend,
	})
}

// --- Models ---

func (h *SelfCheckHandler) handleModels(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.Query(r.Context(), `
		SELECT model_name, COUNT(*) AS total,
		COUNT(*) FILTER (WHERE status='success') AS success,
		COUNT(*) FILTER (WHERE status='failed') AS failed,
		MAX(started_at) AS last_run
		FROM self_check_runs
		GROUP BY model_name
		ORDER BY last_run DESC`)
	if err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	defer rows.Close()

	type modelInfo struct {
		Model   string     `json:"model_name"`
		Total   int        `json:"total"`
		Success int        `json:"success"`
		Failed  int        `json:"failed"`
		LastRun *time.Time `json:"last_run,omitempty"`
	}
	var models []modelInfo
	for rows.Next() {
		var m modelInfo
		if rows.Scan(&m.Model, &m.Total, &m.Success, &m.Failed, &m.LastRun) == nil {
			models = append(models, m)
		}
	}
	writeJSON(w, 200, map[string]any{"models": models})
}

// joinStringsSC is a package-local join helper.
func joinStringsSC(ss []string, sep string) string {
	if len(ss) == 0 {
		return ""
	}
	result := ss[0]
	for _, s := range ss[1:] {
		result += sep + s
	}
	return result
}
