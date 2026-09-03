package admin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// selfCheckDB is the subset of a pgx pool used by self-check handlers.
// Keeping this dependency narrow lets handler tests exercise database error
// paths without requiring a running PostgreSQL instance.
type selfCheckDB interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// SelfCheckHandler serves /api/self-check/* endpoints.
type SelfCheckHandler struct {
	db     selfCheckDB
	worker interface {
		TriggerManualRun(model string) error
	}
	// probeEnqueue (2026-08-18): under the new probe mode the legacy
	// featured-model worker is retired, but operators still need a working
	// "手动触发" button. When wired, the trigger fans a node_probe task out
	// to every credential bound to the model through the durable
	// credential_probe_queue (the same path POST /api/admin/probe/tasks
	// uses), so the 自检 page drives real probes instead of 410-ing.
	probeEnqueue func(ctx context.Context, model string) (int, error)
}

func NewSelfCheckHandler(db *pgxpool.Pool) *SelfCheckHandler {
	return &SelfCheckHandler{db: db}
}

// SetWorker is called after the worker is created to enable manual triggering.
func (h *SelfCheckHandler) SetWorker(w interface{ TriggerManualRun(model string) error }) {
	h.worker = w
}

// SetProbeEnqueue wires the new-probe-mode trigger path (durable queue
// fan-out). Called from main.go after the probe queue is constructed.
func (h *SelfCheckHandler) SetProbeEnqueue(fn func(ctx context.Context, model string) (int, error)) {
	h.probeEnqueue = fn
}

// RegisterRoutes registers the self-check admin routes.
func (h *SelfCheckHandler) RegisterRoutes(mux *http.ServeMux, admin, superAdmin func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("/api/self-check/runs", admin(h.handleListRuns))
	mux.HandleFunc("/api/self-check/runs/", admin(h.handleGetRun))
	mux.HandleFunc("/api/self-check/settings", admin(h.handleGetSettings))
	mux.HandleFunc("/api/self-check/settings/update", superAdmin(h.handleUpdateSettings))
	mux.HandleFunc("/api/self-check/trigger/availability", admin(h.handleTriggerAvailability))
	mux.HandleFunc("/api/self-check/trigger", superAdmin(h.handleTrigger))
	mux.HandleFunc("/api/self-check/stats", admin(h.handleStats))
	mux.HandleFunc("/api/self-check/models", admin(h.handleModels))
}

// credentialIDFromSelfCheckLabel parses the synthetic "cred-<int>" model
// label that the credential selfcheck worker writes so dashboards can
// distinguish a per-credential probe run from a real upstream request.
// Returns nil when the label does not match the convention.
func credentialIDFromSelfCheckLabel(modelName string) *int64 {
	const prefix = "cred-"
	if !strings.HasPrefix(modelName, prefix) {
		return nil
	}
	id, err := strconv.ParseInt(modelName[len(prefix):], 10, 64)
	if err != nil || id <= 0 {
		return nil
	}
	return &id
}

// --- List Runs ---

type scRun struct {
	ID                int64           `json:"id"`
	ModelName         string          `json:"model_name"`
	CredentialID      *int64          `json:"credential_id,omitempty"`
	StartedAt         time.Time       `json:"started_at"`
	CompletedAt       *time.Time      `json:"completed_at,omitempty"`
	DurationMs        int             `json:"duration_ms"`
	Status            string          `json:"status"`
	RoundsTotal       int             `json:"rounds_total"`
	RoundsSuccess     int             `json:"rounds_success"`
	HadToolCall       bool            `json:"had_tool_call"`
	TotalTokens       int             `json:"total_tokens"`
	AvgLatencyMs      int             `json:"avg_latency_ms"`
	ErrorType         string          `json:"error_type,omitempty"`
	ErrorDetail       string          `json:"error_detail,omitempty"`
	UpstreamTested    bool            `json:"upstream_tested"`
	UpstreamResult    string          `json:"upstream_result,omitempty"`
	UpstreamLatencyMs int             `json:"upstream_latency_ms,omitempty"`
	UpstreamError     string          `json:"upstream_error,omitempty"`
	SelectionStrategy string          `json:"selection_strategy,omitempty"`
	AttemptedModels   json.RawMessage `json:"attempted_models,omitempty"`
}

func (h *SelfCheckHandler) handleListRuns(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	model := r.URL.Query().Get("model")
	status := r.URL.Query().Get("status")
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}

	where := ` WHERE 1=1`
	args := []any{}
	argIdx := 1
	if model != "" {
		where += " AND model_name = $" + strconv.Itoa(argIdx)
		args = append(args, model)
		argIdx++
	}
	if status != "" {
		where += " AND status = $" + strconv.Itoa(argIdx)
		args = append(args, status)
		argIdx++
	}

	query := `SELECT id, model_name, started_at, completed_at, duration_ms,
		status, rounds_total, rounds_success, had_tool_call,
		total_tokens, avg_latency_ms,
		COALESCE(error_type,'') AS error_type, COALESCE(error_detail,'') AS error_detail,
		upstream_tested, COALESCE(upstream_result,'') AS upstream_result,
		COALESCE(upstream_latency_ms,0) AS upstream_latency_ms,
		COALESCE(upstream_error,'') AS upstream_error,
		COALESCE(selection_strategy,'') AS selection_strategy,
		COALESCE(attempted_models,'[]'::jsonb) AS attempted_models
		FROM self_check_runs` + where
	query += " ORDER BY started_at DESC LIMIT $" + strconv.Itoa(argIdx)
	args = append(args, limit)

	rows, err := h.db.Query(r.Context(), query, args...)
	if err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	defer rows.Close()

	// Non-nil empty slice so JSON encodes as [] not null (frontend reads .length).
	items := make([]scRun, 0)
	for rows.Next() {
		var item scRun
		if err := rows.Scan(&item.ID, &item.ModelName, &item.StartedAt, &item.CompletedAt,
			&item.DurationMs, &item.Status, &item.RoundsTotal, &item.RoundsSuccess,
			&item.HadToolCall, &item.TotalTokens, &item.AvgLatencyMs,
			&item.ErrorType, &item.ErrorDetail, &item.UpstreamTested,
			&item.UpstreamResult, &item.UpstreamLatencyMs, &item.UpstreamError,
			&item.SelectionStrategy, &item.AttemptedModels); err != nil {
			slog.Error("self_check: scan run failed", "error", err)
			continue
		}
		item.CredentialID = credentialIDFromSelfCheckLabel(item.ModelName)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	// Total count.
	var total int
	countArgs := append([]any(nil), args[:len(args)-1]...)
	if err := h.db.QueryRow(r.Context(), `SELECT COUNT(*) FROM self_check_runs`+where, countArgs...).Scan(&total); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	writeJSON(w, 200, map[string]any{"items": items, "total": total})
}

// --- Get Run ---

func (h *SelfCheckHandler) handleGetRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
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
		COALESCE(upstream_error,'') AS upstream_error,
		COALESCE(selection_strategy,'') AS selection_strategy,
		COALESCE(attempted_models,'[]'::jsonb) AS attempted_models
		FROM self_check_runs WHERE id=$1`, id).Scan(
		&run.ID, &run.ModelName, &run.StartedAt, &run.CompletedAt,
		&run.DurationMs, &run.Status, &run.RoundsTotal, &run.RoundsSuccess,
		&run.HadToolCall, &run.TotalTokens, &run.AvgLatencyMs,
		&run.ErrorType, &run.ErrorDetail, &run.UpstreamTested,
		&run.UpstreamResult, &run.UpstreamLatencyMs, &run.UpstreamError,
		&run.SelectionStrategy, &run.AttemptedModels)
	if err != nil {
		writeJSON(w, 404, map[string]any{"error": "run not found"})
		return
	}
	run.CredentialID = credentialIDFromSelfCheckLabel(run.ModelName)

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
	rounds := make([]round, 0)
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
	if err := rows.Err(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	writeJSON(w, 200, map[string]any{"run": run, "rounds": rounds})
}

// --- Settings ---

// defaultFeaturedModelsSC is the default featured model list used when seeding
// the single-row self_check_settings table (matches migration 338).
const defaultFeaturedModelsSC = `["minimax-m2.7","glm-5.2","mimo-v2.5","claude-sonnet-5","gpt-5.4","gpt-5.6-luna","deepseek-v4-pro"]`

// ensureSelfCheckSettingsRow guarantees the single settings row (id=1) exists,
// seeding it with defaults when missing. This keeps the settings endpoint from
// 500-ing when the migration's seed insert hasn't run yet.
func (h *SelfCheckHandler) ensureSelfCheckSettingsRow(ctx context.Context) error {
	_, err := h.db.Exec(ctx, `
		INSERT INTO self_check_settings (id, featured_model_ids)
		VALUES (1, $1::jsonb)
		ON CONFLICT (id) DO NOTHING`, defaultFeaturedModelsSC)
	return err
}

func (h *SelfCheckHandler) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
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
		// If the settings row is missing, seed it with defaults and retry once
		// instead of returning 500 (migration seed may not have run).
		if errors.Is(err, pgx.ErrNoRows) {
			if seedErr := h.ensureSelfCheckSettingsRow(r.Context()); seedErr != nil {
				slog.Error("self_check: seed default settings failed", "error", seedErr)
				writeJSON(w, 500, map[string]any{"error": "settings not initialized: " + seedErr.Error()})
				return
			}
			// Re-query after seeding.
			err = h.db.QueryRow(r.Context(), `
				SELECT enabled, normal_interval_seconds, fault_interval_seconds,
				model_source, max_models, max_tokens_per_run, featured_model_ids,
				updated_at, updated_by
				FROM self_check_settings WHERE id=1`,
			).Scan(&s.Enabled, &s.NormalInterval, &s.FaultInterval,
				&s.ModelSource, &s.MaxModels, &s.MaxTokens, &s.FeaturedModels,
				&s.UpdatedAt, &s.UpdatedBy)
		}
		if err != nil {
			// Still failing — likely the table itself doesn't exist yet.
			slog.Error("self_check: load settings failed", "error", err)
			writeJSON(w, 500, map[string]any{"error": "self_check_settings table unavailable: " + err.Error()})
			return
		}
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
	if err := readJSONRequired(r, &body); err != nil {
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
		if *body.NormalInterval < 10 || *body.NormalInterval > 600 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "normal interval must be between 10 and 600 seconds"})
			return
		}
		sets = append(sets, "normal_interval_seconds=$"+strconv.Itoa(argIdx))
		args = append(args, *body.NormalInterval)
		argIdx++
	}
	if body.FaultInterval != nil {
		if *body.FaultInterval < 5 || *body.FaultInterval > 300 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "fault interval must be between 5 and 300 seconds"})
			return
		}
		sets = append(sets, "fault_interval_seconds=$"+strconv.Itoa(argIdx))
		args = append(args, *body.FaultInterval)
		argIdx++
	}
	if body.ModelSource != nil {
		if *body.ModelSource != "top10" && *body.ModelSource != "featured" && *body.ModelSource != "both" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "model source must be top10, featured, or both"})
			return
		}
		sets = append(sets, "model_source=$"+strconv.Itoa(argIdx))
		args = append(args, *body.ModelSource)
		argIdx++
	}
	if body.MaxModels != nil {
		if *body.MaxModels < 1 || *body.MaxModels > 50 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "max models must be between 1 and 50"})
			return
		}
		sets = append(sets, "max_models=$"+strconv.Itoa(argIdx))
		args = append(args, *body.MaxModels)
		argIdx++
	}
	if body.MaxTokens != nil {
		if *body.MaxTokens < 1000 || *body.MaxTokens > 1000000 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "max tokens must be between 1000 and 1000000"})
			return
		}
		sets = append(sets, "max_tokens_per_run=$"+strconv.Itoa(argIdx))
		args = append(args, *body.MaxTokens)
		argIdx++
	}
	if len(body.FeaturedModels) > 0 {
		var featured []string
		if string(body.FeaturedModels) == "null" || json.Unmarshal(body.FeaturedModels, &featured) != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "featured_model_ids must be a JSON string array"})
			return
		}
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

// handleTriggerAvailability reports whether POST /api/self-check/trigger can
// accept runs right now. Two paths exist: the legacy featured-model worker
// (when instantiated) or, since 2026-08-18, the durable-queue fan-out under
// the new probe mode (LLM_GATEWAY_USE_NEW_PROBE_MODE=true) — previously this
// endpoint hard-disabled the button, which left operators with no manual
// self-check exactly when the automated pipeline was dead.
//
// 2026-09-03: reason 字段改成中文友好版，便于 UI 直接显示在 tooltip 上。
// error_code 保持稳定，UI 用它决定"未启用 vs 异常"的按钮文案。
//
// Route: GET /api/self-check/trigger/availability (admin).
func (h *SelfCheckHandler) handleTriggerAvailability(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	available := h.worker != nil || h.probeEnqueue != nil
	resp := map[string]any{
		"available":      available,
		"new_probe_mode": scNewProbeMode(),
	}
	if h.probeEnqueue != nil {
		resp["mode"] = "probe_queue"
	} else if h.worker != nil {
		resp["mode"] = "legacy_worker"
	}
	if !available {
		if scNewProbeMode() {
			// 新探测模式下 trigger 已迁移到节点探测队列，UI 触发按钮有意禁用；
			// 这不是故障，运维需要知道"为什么禁用"而不是"触发挂了"。
			resp["reason"] = "自检触发已迁移到节点探测队列（probe queue），手动触发按钮已禁用。如需手动触发，请走节点操作 API 或重启用 legacy selfcheck。"
			resp["error_code"] = "self_check.trigger.no_probe_path"
		} else {
			resp["reason"] = "self-check worker is not initialized (server may still be starting)"
			resp["error_code"] = "self_check.trigger.worker_unavailable"
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *SelfCheckHandler) handleTrigger(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]any{"error": "method not allowed"})
		return
	}
	var body struct {
		Model string `json:"model"`
	}
	if err := readJSONRequired(r, &body); err != nil && err != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json"})
		return
	}
	if h.worker == nil {
		// New probe mode: fan a node_probe task out to every credential
		// bound to the model through the durable queue. This resurrects the
		// 自检 page's manual trigger (previously 410 Gone since the legacy
		// worker was retired — during the 2026-08-18 glm-5.2 incident that
		// left operators with no way to demand fresh evidence).
		if h.probeEnqueue != nil {
			ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
			defer cancel()

			// When model is empty, trigger self-check for all models based on settings.
			models := []string{}
			if body.Model == "" {
				// Load self-check settings to determine which models to test.
				var settings struct {
					Enabled        bool
					ModelSource    string
					MaxModels      int
					FeaturedModels json.RawMessage
				}
				err := h.db.QueryRow(ctx, `
					SELECT enabled, model_source, max_models, featured_model_ids
					FROM self_check_settings WHERE id=1`,
				).Scan(&settings.Enabled, &settings.ModelSource, &settings.MaxModels, &settings.FeaturedModels)
				if err != nil {
					if errors.Is(err, pgx.ErrNoRows) {
						// Seed default settings if missing.
						if seedErr := h.ensureSelfCheckSettingsRow(ctx); seedErr != nil {
							writeJSON(w, 500, map[string]any{"error": "trigger failed", "message": "settings not initialized: " + seedErr.Error()})
							return
						}
						// Retry after seeding.
						err = h.db.QueryRow(ctx, `
							SELECT enabled, model_source, max_models, featured_model_ids
							FROM self_check_settings WHERE id=1`,
						).Scan(&settings.Enabled, &settings.ModelSource, &settings.MaxModels, &settings.FeaturedModels)
					}
					if err != nil {
						writeJSON(w, 500, map[string]any{"error": "trigger failed", "message": "failed to load settings: " + err.Error()})
						return
					}
				}
				if !settings.Enabled {
					writeJSON(w, http.StatusConflict, map[string]any{"error": "trigger failed", "message": "self-check is disabled in current settings"})
					return
				}
				if settings.MaxModels <= 0 {
					writeJSON(w, http.StatusBadRequest, map[string]any{"error": "trigger failed", "message": "max_models must be greater than zero"})
					return
				}
				if settings.ModelSource != "featured" && settings.ModelSource != "top10" && settings.ModelSource != "both" {
					writeJSON(w, http.StatusBadRequest, map[string]any{"error": "trigger failed", "message": "invalid model_source in settings: " + settings.ModelSource})
					return
				}

				// Build candidates using the same eligibility gates as the actual
				// probe fan-out. Preserve configured/ranked order and deduplicate.
				eligible := make(map[string]struct{})
				rows, err := h.db.Query(ctx, `
					SELECT DISTINCT pm.raw_model_name
					FROM provider_models pm
					JOIN credential_model_bindings cmb ON cmb.provider_model_id = pm.id
					JOIN credentials c ON c.id = cmb.credential_id
					JOIN providers p ON p.id = c.provider_id
					WHERE COALESCE(c.status, 'active') = 'active'
					  AND COALESCE(c.manual_disabled, FALSE) = FALSE
					  AND COALESCE(p.enabled, TRUE) = TRUE
					  AND COALESCE(p.manual_disabled, FALSE) = FALSE`)
				if err != nil {
					writeJSON(w, 500, map[string]any{"error": "trigger failed", "message": "failed to select eligible models: " + err.Error()})
					return
				}
				for rows.Next() {
					var model string
					if err := rows.Scan(&model); err != nil {
						rows.Close()
						writeJSON(w, 500, map[string]any{"error": "trigger failed", "message": "failed to read eligible models: " + err.Error()})
						return
					}
					if strings.TrimSpace(model) != "" {
						eligible[model] = struct{}{}
					}
				}
				if err := rows.Err(); err != nil {
					rows.Close()
					writeJSON(w, 500, map[string]any{"error": "trigger failed", "message": "failed to read eligible models: " + err.Error()})
					return
				}
				rows.Close()

				seen := make(map[string]struct{}, settings.MaxModels)
				addModel := func(model string) {
					if len(models) >= settings.MaxModels || strings.TrimSpace(model) == "" {
						return
					}
					if _, ok := eligible[model]; !ok {
						return
					}
					if _, ok := seen[model]; ok {
						return
					}
					seen[model] = struct{}{}
					models = append(models, model)
				}
				var featured []string
				if err := json.Unmarshal(settings.FeaturedModels, &featured); err != nil && len(settings.FeaturedModels) > 0 {
					writeJSON(w, 500, map[string]any{"error": "trigger failed", "message": "invalid featured_model_ids: " + err.Error()})
					return
				}
				if settings.ModelSource == "featured" || settings.ModelSource == "both" {
					for _, model := range featured {
						addModel(model)
					}
				}
				if settings.ModelSource == "top10" || settings.ModelSource == "both" {
					rows, err := h.db.Query(ctx, `
						SELECT pm.raw_model_name
						FROM provider_models pm
						JOIN credential_model_bindings cmb ON cmb.provider_model_id = pm.id
						JOIN credentials c ON c.id = cmb.credential_id
						JOIN providers p ON p.id = c.provider_id
						WHERE COALESCE(c.status, 'active') = 'active'
						  AND COALESCE(c.manual_disabled, FALSE) = FALSE
						  AND COALESCE(p.enabled, TRUE) = TRUE
						  AND COALESCE(p.manual_disabled, FALSE) = FALSE
						GROUP BY pm.raw_model_name
						ORDER BY COUNT(DISTINCT cmb.credential_id) DESC, pm.raw_model_name
						LIMIT $1`, settings.MaxModels)
					if err != nil {
						writeJSON(w, 500, map[string]any{"error": "trigger failed", "message": "failed to select top models: " + err.Error()})
						return
					}
					for rows.Next() {
						var model string
						if err := rows.Scan(&model); err != nil {
							rows.Close()
							writeJSON(w, 500, map[string]any{"error": "trigger failed", "message": "failed to read top models: " + err.Error()})
							return
						}
						addModel(model)
					}
					if err := rows.Err(); err != nil {
						rows.Close()
						writeJSON(w, 500, map[string]any{"error": "trigger failed", "message": "failed to read top models: " + err.Error()})
						return
					}
					rows.Close()
				}
				if len(models) == 0 {
					writeJSON(w, 400, map[string]any{"error": "trigger failed", "message": "no models to test based on current settings"})
					return
				}

			} else {
				// Single model specified.
				models = []string{body.Model}
			}

			// Trigger self-check for each selected model.

			totalEnqueued := 0
			failedModels := 0
			results := make(map[string]any)
			firstError := ""
			for _, model := range models {
				n, err := h.probeEnqueue(ctx, model)
				if err != nil {
					failedModels++
					results[model] = map[string]any{"error": err.Error(), "enqueued": 0}
					if firstError == "" {
						firstError = err.Error()
					}
				} else {
					results[model] = map[string]any{"enqueued": n}
					totalEnqueued += n
				}
			}

			// Per bc48559b4 audit contract: when every model fails to enqueue,
			// surface 503 so the UI doesn't render a misleading 200 OK banner.
			// Partial failures still return 200 with per-model details so callers
			// see every outcome in one response.
			if failedModels == len(models) {
				writeJSON(w, http.StatusServiceUnavailable, map[string]any{
					"error":         "trigger failed",
					"message":       firstError,
					"mode":          "probe_queue",
					"enqueued":      0,
					"models_tested": len(models),
					"models_failed": failedModels,
					"models":        models,
					"results":       results,
				})
				return
			}

			writeJSON(w, http.StatusOK, map[string]any{
				"ok":            true,
				"mode":          "probe_queue",
				"enqueued":      totalEnqueued,
				"models_tested": len(models),
				"models_failed": failedModels,
				"message":       "node_probe tasks enqueued",
				"models":        models,
				"results":       results,
			})
			return
		}
		// 410 Gone: this endpoint is intentionally retired under the new
		// probe mode (no longer transiently unavailable). 503 misled the UI
		// into a red "服务不可用" banner that wasn't actionable.
		resp := map[string]any{
			"error":   "trigger endpoint retired",
			"message": "self-check worker is not initialized",
		}
		if scNewProbeMode() {
			resp["message"] = "self-check is disabled in new probe mode and the durable probe queue is not wired."
			resp["error_code"] = "self_check.trigger.no_probe_path"
		} else {
			resp["error_code"] = "self_check.trigger.worker_unavailable"
		}
		writeJSON(w, http.StatusGone, resp)
		return
	}
	if err := h.worker.TriggerManualRun(body.Model); err != nil {
		writeJSON(w, 503, map[string]any{"error": "trigger failed", "message": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "message": "manual trigger queued", "model": body.Model})
}

// scNewProbeMode mirrors cmd/gateway.useNewProbeMode() without taking on a
// cross-package dependency. Keeps admin/self_check_handlers.go self-contained
// so availability + 410 reason text can be derived from the live env.
func scNewProbeMode() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("LLM_GATEWAY_USE_NEW_PROBE_MODE")))
	if v == "" {
		return true // default since 2026-07-14
	}
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

// --- Stats ---

func (h *SelfCheckHandler) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
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
	// Non-nil empty slices so JSON encodes as [] not null.
	byModel := make([]modelStat, 0)
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
	errBreakdown := make([]errStat, 0)
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
	trend := make([]trendPoint, 0)
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

	// Probe-system health (2026-08-18): the 自检 page previously showed only
	// self_check_runs, which stopped updating a month before the glm-5.2
	// incident while the real probe pipeline was silently dead. Surface the
	// durable-queue liveness signals here so "page green, pipeline dead"
	// can't happen again: unclaimable-ready > 0 or a stale last_activity
	// means probes are not executing even if old runs look fine.
	probeSystem := map[string]any{}
	var qs struct {
		Ready            int        `json:"-"`
		ReadyExpired     int        `json:"-"`
		Running          int        `json:"-"`
		LastActivity     *time.Time `json:"-"`
		LastProbeAttempt *time.Time `json:"-"`
		DueStates        int        `json:"-"`
	}
	_ = h.db.QueryRow(r.Context(), `
		SELECT
			COUNT(*) FILTER (WHERE status='ready' AND expires_at > now()),
			COUNT(*) FILTER (WHERE status='ready' AND expires_at <= now()),
			COUNT(*) FILTER (WHERE status='running'),
			MAX(updated_at)
		FROM credential_probe_queue`).Scan(
		&qs.Ready, &qs.ReadyExpired, &qs.Running, &qs.LastActivity)
	_ = h.db.QueryRow(r.Context(), `
		SELECT MAX(last_attempt_at), COUNT(*) FILTER (WHERE paused=FALSE AND next_retry_at <= now())
		FROM node_probe_state`).Scan(&qs.LastProbeAttempt, &qs.DueStates)
	probeSystem["queue_ready"] = qs.Ready
	probeSystem["queue_ready_unclaimable"] = qs.ReadyExpired
	probeSystem["queue_running"] = qs.Running
	probeSystem["queue_last_activity_at"] = qs.LastActivity
	probeSystem["last_probe_attempt_at"] = qs.LastProbeAttempt
	probeSystem["due_states"] = qs.DueStates
	probeSystem["executing"] = qs.Running > 0
	probeSystem["healthy"] = qs.ReadyExpired == 0 &&
		(qs.Running > 0 || qs.LastActivity == nil || time.Since(*qs.LastActivity) < 15*time.Minute)

	writeJSON(w, 200, map[string]any{
		"range":           rangeParam,
		"summary":         s,
		"by_model":        byModel,
		"error_breakdown": errBreakdown,
		"trend":           trend,
		"probe_system":    probeSystem,
	})
}

// --- Models ---

func (h *SelfCheckHandler) handleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
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
	models := make([]modelInfo, 0)
	for rows.Next() {
		var m modelInfo
		if rows.Scan(&m.Model, &m.Total, &m.Success, &m.Failed, &m.LastRun) == nil {
			models = append(models, m)
		}
	}
	if err := rows.Err(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
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
