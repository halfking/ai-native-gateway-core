// Package admin — auto-route handlers (v2.0).
//
// Exposes the autoroute subsystem to the admin UI and curl:
//
//	GET    /api/admin/auto-route/decisions    — recent auto-route decisions (request_logs)
//	GET    /api/admin/auto-route/index        — current model × task × profile index snapshot
//	PUT    /api/admin/auto-route/profile      — set API key's sticky profile
//	GET    /api/admin/auto-route/audit        — aggregated stats (task distribution, top models)
//	POST   /api/admin/auto-route/refresh      — manually trigger credential_model_index refresh
//
// All routes are mounted via RegisterAutoRouteRoutes (called from
// admin/handler.go).
package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// AutoRouteHandlers groups the 5 admin endpoints for autoroute.
//
// indexRefresher is an interface so tests can stub the refresh action
// without running the full bg worker.
type AutoRouteHandlers struct {
	db *pgxpool.Pool

	// indexRefresher is optional. When nil, the /refresh endpoint
	// returns 503 with a clear message. Production wires this from
	// the bg.AutoIndexRefresher constructed in cmd/gateway/main.go.
	indexRefresher interface {
		RefreshOnce(ctx context.Context) error
	}

	// feedbackAnalyzer is optional. When set, the tuning/admin
	// endpoints can trigger on-demand analyzer runs.
	// v2.1 — wired from cmd/gateway/main.go when in full mode.
	feedbackAnalyzer interface {
		AnalyzeOnce(ctx context.Context) error
	}
}

// NewAutoRouteHandlers constructs the handler set.
func NewAutoRouteHandlers(db *pgxpool.Pool) *AutoRouteHandlers {
	return &AutoRouteHandlers{db: db}
}

// SetIndexRefresher wires the live bg worker so the /refresh endpoint
// can trigger an immediate refresh.
func (h *AutoRouteHandlers) SetIndexRefresher(r interface {
	RefreshOnce(ctx context.Context) error
}) {
	h.indexRefresher = r
}

// SetFeedbackAnalyzer wires the daily analyzer so the tuning endpoints
// can trigger an immediate analysis run.
func (h *AutoRouteHandlers) SetFeedbackAnalyzer(a interface {
	AnalyzeOnce(ctx context.Context) error
}) {
	h.feedbackAnalyzer = a
}

// RegisterAutoRouteRoutes mounts the endpoints onto the admin mux.
// adminWrap is the bearer-token middleware (shared with peak handlers).
func (h *AutoRouteHandlers) RegisterAutoRouteRoutes(mux *http.ServeMux, adminWrap func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("/api/admin/auto-route/decisions", adminWrap(h.handleDecisions))
	mux.HandleFunc("/api/admin/auto-route/index", adminWrap(h.handleIndexSnapshot))
	mux.HandleFunc("/api/admin/auto-route/profile", adminWrap(h.handleSetProfile))
	mux.HandleFunc("/api/admin/auto-route/audit", adminWrap(h.handleAudit))
	mux.HandleFunc("/api/admin/auto-route/refresh", adminWrap(h.handleRefresh))
	// v2.0.1 — per-API-Key customer cost dashboard
	mux.HandleFunc("/api/admin/auto-route/cost/customer", adminWrap(h.handleCustomerCost))
	mux.HandleFunc("/api/admin/auto-route/cost/model", adminWrap(h.handleModelCost))
	// v2.1 — Phase 7.6 routing overrides endpoints
	mux.HandleFunc("/api/admin/routing/overrides", adminWrap(h.handleRoutingOverridesCollection))
	mux.HandleFunc("/api/admin/routing/overrides/", adminWrap(h.handleRoutingOverrideItem))
	// v2.1 — Phase 7.2 auto-route correlation analysis endpoint
	h.RegisterAutoRouteCorrelationsRoute(mux, adminWrap)
	// v2.1 — Phase 5 tuning feedback endpoints
	tuning := NewTuningHandlers(h)
	tuning.SetAnalyzer(h.feedbackAnalyzer)
	tuning.RegisterTuningRoutes(mux, adminWrap)
	// v2.1 — P8.2 quality correlations endpoint
	mux.HandleFunc("/api/admin/auto-route/quality-correlations",
		adminWrap(h.handleQualityCorrelations))
}

// handleDecisions returns the most recent N auto-route decisions from
// request_logs. Includes both auto requests and explicit-model requests
// (where the client specified a model directly).
//
// Query params:
//   - limit : max rows (default 50, max 500)
//   - task  : filter by task_type (optional)
//   - work_type : filter by work_type column (optional)
//   - model : filter by outbound_model — accepts canonical or raw alias (optional)
//   - profile : filter by auto_profile (optional)
func (h *AutoRouteHandlers) handleDecisions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	limit := 50
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 && v <= 500 {
		limit = v
	}
	task := r.URL.Query().Get("task")
	workType := strings.TrimSpace(r.URL.Query().Get("work_type"))
	model := strings.TrimSpace(r.URL.Query().Get("model"))
	profile := r.URL.Query().Get("profile")

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	query := `
		SELECT ts, request_id, api_key_id, task_type, auto_profile,
		       auto_confidence, client_model, outbound_model,
		       credential_id, auto_decision, success, latency_ms, work_type
		FROM request_logs
		WHERE (is_auto_request = TRUE OR (is_auto_request = FALSE AND client_model IS NOT NULL AND client_model <> ''))
		  AND ts >= NOW() - INTERVAL '7 days'
	`
	args := []interface{}{}
	if task != "" {
		if task == SpecifiedModelTaskKey {
			// The synthetic __specified__ task key represents non-auto
			// requests; their task_type column is NULL in the DB.
			query += " AND is_auto_request = FALSE"
		} else {
			args = append(args, task)
			query += fmt.Sprintf(" AND task_type = $%d", len(args))
		}
	}
	if workType != "" {
		args = append(args, workType)
		query += fmt.Sprintf(" AND work_type = $%d", len(args))
	}
	if model != "" {
		names, _ := expandModelFilter(ctx, h.db, model)
		if len(names) == 0 {
			names = []string{model}
		}
		args = append(args, names)
		// Use COALESCE to match both auto (outbound_model) and
		// specified-model (client_model) requests.
		query += fmt.Sprintf(" AND COALESCE(NULLIF(outbound_model, ''), client_model) = ANY($%d)", len(args))
	}
	if profile != "" {
		args = append(args, profile)
		query += fmt.Sprintf(" AND auto_profile = $%d", len(args))
	}
	args = append(args, limit)
	query += fmt.Sprintf(" ORDER BY ts DESC LIMIT $%d", len(args))

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		writeInternalErr(w, err)
		return
	}
	defer rows.Close()

	out := make([]map[string]interface{}, 0)
	for rows.Next() {
		var ts time.Time
		var reqID, prof, clientModel, outbound string
		var workTypeVal, taskTypeVal sql.NullString
		var apiKeyID, credentialID *int
		var confidence *float64
		var decision *string
		var success bool
		var latency *int
		if err := rows.Scan(&ts, &reqID, &apiKeyID, &taskTypeVal, &prof,
			&confidence, &clientModel, &outbound, &credentialID, &decision,
			&success, &latency, &workTypeVal); err != nil {
			continue
		}
		// For specified model requests, task_type is NULL; use the
		// synthetic __specified__ key so the frontend can identify them.
		taskType := taskTypeVal.String
		if taskType == "" {
			taskType = SpecifiedModelTaskKey
		}
		entry := map[string]interface{}{
			"ts":             ts.Format(time.RFC3339),
			"request_id":     reqID,
			"task_type":      taskType,
			"auto_profile":   prof,
			"client_model":   clientModel,
			"outbound_model": outbound,
			"success":        success,
		}
		if workTypeVal.Valid && workTypeVal.String != "" {
			entry["work_type"] = workTypeVal.String
		}
		if apiKeyID != nil {
			entry["api_key_id"] = *apiKeyID
		}
		if credentialID != nil {
			entry["credential_id"] = *credentialID
		}
		if confidence != nil {
			entry["auto_confidence"] = *confidence
		}
		if latency != nil {
			entry["latency_ms"] = *latency
		}
		if decision != nil {
			var parsed map[string]interface{}
			if err := json.Unmarshal([]byte(*decision), &parsed); err == nil {
				entry["auto_decision"] = parsed
			}
		}
		out = append(out, entry)
	}
	writeJSONOk(w, out)
}

// handleIndexSnapshot returns the current credential_model_index snapshot
// (the latest 5-min bucket). Used by the admin UI to visualise the live
// index state and by curl scripts to debug routing decisions.
//
// Query params:
//   - canonical_id : filter by canonical_id (optional)
//   - top          : limit to top-N by composite score (default 100)
func (h *AutoRouteHandlers) handleIndexSnapshot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	canonicalID := r.URL.Query().Get("canonical_id")
	top := 100
	if v, err := strconv.Atoi(r.URL.Query().Get("top")); err == nil && v > 0 && v <= 1000 {
		top = v
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	// Get the latest bucket (NULL-safe: returns zero value when empty)
	var latestBucket sql.NullTime
	if err := h.db.QueryRow(ctx, `SELECT MAX(bucket) FROM credential_model_index`).Scan(&latestBucket); err != nil {
		writeInternalErr(w, err)
		return
	}
	if !latestBucket.Valid {
		// Empty table — return [] with warning so the admin UI shows
		// "waiting for first refresh" instead of an error.
		writeJSONOk(w, []map[string]interface{}{
			{"warning": "credential_model_index is empty; awaiting first bg worker refresh (within 5 minutes of gateway start)"},
		})
		return
	}
	bucket := latestBucket.Time

	query := `
		SELECT cmi.credential_id, cmi.raw_model, cmi.canonical_id,
		       COALESCE(mc.canonical_name, ''), cmi.billing_mode,
		       cmi.unit_price_in_per_1m, cmi.unit_price_out_per_1m,
		       cmi.context_window, cmi.success_rate, cmi.p95_latency_ms,
		       cmi.active_sessions, cmi.concurrency_limit, cmi.pressure_ratio,
		       cmi.score_smart, cmi.score_speed_first, cmi.score_cost_first,
		       cmi.updated_at
		FROM credential_model_index cmi
		LEFT JOIN models_canonical mc ON mc.id = cmi.canonical_id
		WHERE cmi.bucket = $1
	`
	args := []interface{}{bucket}
	if canonicalID != "" {
		args = append(args, canonicalID)
		query += fmt.Sprintf(" AND cmi.canonical_id = $%d", len(args))
	}
	args = append(args, top)
	query += fmt.Sprintf(" ORDER BY cmi.score_smart DESC LIMIT $%d", len(args))

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		writeInternalErr(w, err)
		return
	}
	defer rows.Close()

	out := make([]map[string]interface{}, 0)
	for rows.Next() {
		var credID int64
		var rawModel, canonicalName, billingMode string
		var canonicalIDVal *int
		var priceIn, priceOut, successRate, pressureRatio *float64
		var contextWindow, p95, activeSessions, concurrencyLimit *int
		var scoreSmart, scoreSpeed, scoreCost *float64
		var updatedAt time.Time
		if err := rows.Scan(&credID, &rawModel, &canonicalIDVal, &canonicalName, &billingMode,
			&priceIn, &priceOut, &contextWindow, &successRate, &p95,
			&activeSessions, &concurrencyLimit, &pressureRatio,
			&scoreSmart, &scoreSpeed, &scoreCost, &updatedAt); err != nil {
			continue
		}
		entry := map[string]interface{}{
			"bucket":        bucket.Format(time.RFC3339),
			"credential_id": credID,
			"raw_model":     rawModel,
		}
		if canonicalIDVal != nil {
			entry["canonical_id"] = *canonicalIDVal
		}
		if canonicalName != "" {
			entry["canonical_name"] = canonicalName
		}
		if billingMode != "" {
			entry["billing_mode"] = billingMode
		}
		if priceIn != nil {
			entry["unit_price_in_per_1m"] = *priceIn
		}
		if priceOut != nil {
			entry["unit_price_out_per_1m"] = *priceOut
		}
		if contextWindow != nil {
			entry["context_window"] = *contextWindow
		}
		if successRate != nil {
			entry["success_rate"] = *successRate
		}
		if p95 != nil {
			entry["p95_latency_ms"] = *p95
		}
		if activeSessions != nil {
			entry["active_sessions"] = *activeSessions
		}
		if concurrencyLimit != nil {
			entry["concurrency_limit"] = *concurrencyLimit
		}
		if pressureRatio != nil {
			entry["pressure_ratio"] = *pressureRatio
		}
		if scoreSmart != nil {
			entry["score_smart"] = *scoreSmart
		}
		if scoreSpeed != nil {
			entry["score_speed_first"] = *scoreSpeed
		}
		if scoreCost != nil {
			entry["score_cost_first"] = *scoreCost
		}
		entry["updated_at"] = updatedAt.Format(time.RFC3339)
		out = append(out, entry)
	}
	writeJSONOk(w, out)
}

// handleSetProfile sets an API key's sticky profile preference.
// Body: {"api_key_id": 42, "profile": "cost_first"}
// Or query params: ?api_key_id=42&profile=cost_first
//
// Persists to api_key_auto_profile. Idempotent.
func (h *AutoRouteHandlers) handleSetProfile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut && r.Method != http.MethodPost {
		writeJSONErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	apiKeyIDStr := r.URL.Query().Get("api_key_id")
	profile := r.URL.Query().Get("profile")
	if r.Method == http.MethodPut || r.Method == http.MethodPost {
		var body struct {
			APIKeyID int    `json:"api_key_id"`
			Profile  string `json:"profile"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
			if body.APIKeyID > 0 {
				apiKeyIDStr = strconv.Itoa(body.APIKeyID)
			}
			if body.Profile != "" {
				profile = body.Profile
			}
		}
	}
	apiKeyID, err := strconv.Atoi(apiKeyIDStr)
	if err != nil || apiKeyID <= 0 {
		writeJSONErr(w, http.StatusBadRequest, "api_key_id must be a positive integer")
		return
	}
	if profile != "smart" && profile != "speed_first" && profile != "cost_first" {
		writeJSONErr(w, http.StatusBadRequest, "profile must be one of: smart, speed_first, cost_first")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	_, err = h.db.Exec(ctx, `
		INSERT INTO api_key_auto_profile (api_key_id, profile, first_chosen_at, last_used_at, updated_at)
		VALUES ($1, $2, NOW(), NOW(), NOW())
		ON CONFLICT (api_key_id) DO UPDATE SET
		    profile = EXCLUDED.profile,
		    last_used_at = NOW(),
		    updated_at = NOW()
	`, apiKeyID, profile)
	if err != nil {
		writeInternalErr(w, err)
		return
	}
	writeJSONOk(w, map[string]interface{}{
		"api_key_id": apiKeyID,
		"profile":    profile,
		"updated":    true,
	})
}

// handleAudit returns aggregated stats over the auto-route decisions
// (last 7 days). Output:
//
//	{
//	  "total_auto_requests":          int,   // auto requests only
//	  "specified_model_requests":     int,   // client specified a model (non-auto)
//	  "total_requests":               int,   // auto + specified
//	  "success_rate":                 float64, // over total_requests
//	  "task_distribution":            { "code": 120, "__specified__": 80, ... },
//	  "profile_distribution":         { "smart": 100, ... },
//	  "top_chosen_models":            [{ "model": "...", "count": 100 }, ...]
//	}
//
// Task distribution spans both L1-classified task_types (auto) and the
// synthetic __specified__ key (explicit-model requests), giving a single
// uniform view of routing volume.
func (h *AutoRouteHandlers) handleAudit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	out := map[string]interface{}{}

	// Total + success rate — over BOTH auto and specified-model requests
	// so the headline KPI reflects actual gateway volume.
	var total, successes, totalAuto, totalSpecified int
	err := h.db.QueryRow(ctx, `
		SELECT
		  COUNT(*),
		  COALESCE(SUM(CASE WHEN success THEN 1 ELSE 0 END), 0),
		  COALESCE(SUM(CASE WHEN is_auto_request THEN 1 ELSE 0 END), 0),
		  COALESCE(SUM(CASE WHEN NOT is_auto_request THEN 1 ELSE 0 END), 0)
		FROM request_logs
		WHERE ts >= NOW() - INTERVAL '7 days'
		  AND (
		    is_auto_request = TRUE
		    OR (is_auto_request = FALSE AND client_model IS NOT NULL AND client_model <> '')
		  )
	`).Scan(&total, &successes, &totalAuto, &totalSpecified)
	if err != nil {
		writeInternalErr(w, err)
		return
	}
	out["total_requests"] = total
	out["total_auto_requests"] = totalAuto
	out["specified_model_requests"] = totalSpecified
	if total > 0 {
		out["success_rate"] = float64(successes) / float64(total)
	} else {
		out["success_rate"] = 0.0
	}

	// Task distribution — unified view spanning L1 task_types and the
	// synthetic __specified__ key. Auto-only profile distribution
	// follows below.
	taskDist := map[string]int{}
	taskExpr := fmt.Sprintf(`COALESCE(NULLIF(task_type, ''), CASE WHEN is_auto_request THEN 'unknown' ELSE '%s' END)`, SpecifiedModelTaskKey)
	rows, err := h.db.Query(ctx, fmt.Sprintf(`
		SELECT %s AS task_type, COUNT(*)
		FROM request_logs
		WHERE ts >= NOW() - INTERVAL '7 days'
		  AND (
		    is_auto_request = TRUE
		    OR (is_auto_request = FALSE AND client_model IS NOT NULL AND client_model <> '')
		  )
		GROUP BY (%s)
		ORDER BY COUNT(*) DESC
		LIMIT 20
	`, taskExpr, taskExpr))
	if err == nil {
		for rows.Next() {
			var t string
			var c int
			if err := rows.Scan(&t, &c); err == nil {
				taskDist[t] = c
			}
		}
		rows.Close()
		out["task_distribution"] = taskDist
	}

	// Profile distribution
	profileDist := map[string]int{}
	rows, err = h.db.Query(ctx, `
		SELECT COALESCE(auto_profile, 'unknown') AS p, COUNT(*)
		FROM request_logs
		WHERE is_auto_request = TRUE
		  AND ts >= NOW() - INTERVAL '7 days'
		GROUP BY p
		ORDER BY COUNT(*) DESC
		LIMIT 10
	`)
	if err == nil {
		for rows.Next() {
			var p string
			var c int
			if err := rows.Scan(&p, &c); err == nil {
				profileDist[p] = c
			}
		}
		rows.Close()
		out["profile_distribution"] = profileDist
	}

	// Top chosen models — union auto and specified so users can see
	// which explicit models are consuming volume.
	rows, err = h.db.Query(ctx, `
		SELECT COALESCE(NULLIF(outbound_model, ''), client_model) AS m, COUNT(*) AS c
		FROM request_logs
		WHERE ts >= NOW() - INTERVAL '7 days'
		  AND COALESCE(NULLIF(outbound_model, ''), client_model) IS NOT NULL
		  AND (
		    is_auto_request = TRUE
		    OR (is_auto_request = FALSE AND client_model IS NOT NULL AND client_model <> '')
		  )
		GROUP BY m
		ORDER BY c DESC
		LIMIT 10
	`)
	if err == nil {
		topModels := make([]map[string]interface{}, 0)
		for rows.Next() {
			var m string
			var c int
			if err := rows.Scan(&m, &c); err == nil {
				topModels = append(topModels, map[string]interface{}{
					"model": m,
					"count": c,
				})
			}
		}
		rows.Close()
		out["top_chosen_models"] = topModels
	}

	writeJSONOk(w, out)
}

// handleRefresh triggers an immediate credential_model_index refresh.
// Returns 503 when no refresher is wired (e.g. when running in test
// mode without bg workers).
func (h *AutoRouteHandlers) handleRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.indexRefresher == nil {
		writeJSONErr(w, http.StatusServiceUnavailable,
			"index refresher not wired; running outside gateway main?")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := h.indexRefresher.RefreshOnce(ctx); err != nil {
		writeInternalErr(w, err)
		return
	}
	writeJSONOk(w, map[string]interface{}{
		"refreshed":    true,
		"refreshed_at": time.Now().UTC().Format(time.RFC3339),
	})
}

// handleCustomerCost returns per-API-Key customer cost dashboard.
// Reads from customer_cost_view (v2.0.1 SQL migration).
//
// Query params:
//   - api_key_id : filter by API key (optional)
//   - top        : limit rows (default 50)
func (h *AutoRouteHandlers) handleCustomerCost(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	apiKeyID := r.URL.Query().Get("api_key_id")
	top := 50
	if v, err := strconv.Atoi(r.URL.Query().Get("top")); err == nil && v > 0 && v <= 500 {
		top = v
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	query := `
		SELECT api_key_id, key_alias, tenant_id, application_id,
		       cost_usd_1h, cost_usd_24h, cost_usd_7d,
		       total_auto_requests, total_auto_success,
		       active_concurrent, avg_pressure_1h,
		       best_score_smart, best_score_speed_first, best_score_cost_first,
		       last_request_at
		FROM customer_cost_view
		WHERE 1=1
	`
	args := []interface{}{}
	if apiKeyID != "" {
		args = append(args, apiKeyID)
		query += fmt.Sprintf(" AND api_key_id = $%d", len(args))
	}
	args = append(args, top)
	query += fmt.Sprintf(" ORDER BY cost_usd_24h DESC NULLS LAST LIMIT $%d", len(args))

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		writeInternalErr(w, err)
		return
	}
	defer rows.Close()

	out := make([]map[string]interface{}, 0)
	for rows.Next() {
		var keyID int
		var keyAlias, tenantID *string
		var appID *int
		var cost1h, cost24h, cost7d, avgPressure *float64
		var totalReqs, totalSuccess, activeConcurrent *int
		var bestSmart, bestSpeed, bestCost *float64
		var lastReqAt *time.Time
		if err := rows.Scan(&keyID, &keyAlias, &tenantID, &appID,
			&cost1h, &cost24h, &cost7d,
			&totalReqs, &totalSuccess,
			&activeConcurrent, &avgPressure,
			&bestSmart, &bestSpeed, &bestCost,
			&lastReqAt); err != nil {
			continue
		}
		entry := map[string]interface{}{
			"api_key_id": keyID,
		}
		if keyAlias != nil {
			entry["key_alias"] = *keyAlias
		}
		if tenantID != nil {
			entry["tenant_id"] = *tenantID
		}
		if appID != nil {
			entry["application_id"] = *appID
		}
		if cost1h != nil {
			entry["cost_usd_1h"] = *cost1h
		}
		if cost24h != nil {
			entry["cost_usd_24h"] = *cost24h
		}
		if cost7d != nil {
			entry["cost_usd_7d"] = *cost7d
		}
		if totalReqs != nil {
			entry["total_auto_requests"] = *totalReqs
		}
		if totalSuccess != nil {
			entry["total_auto_success"] = *totalSuccess
		}
		if activeConcurrent != nil {
			entry["active_concurrent"] = *activeConcurrent
		}
		if avgPressure != nil {
			entry["avg_pressure_1h"] = *avgPressure
		}
		if bestSmart != nil {
			entry["best_score_smart"] = *bestSmart
		}
		if bestSpeed != nil {
			entry["best_score_speed_first"] = *bestSpeed
		}
		if bestCost != nil {
			entry["best_score_cost_first"] = *bestCost
		}
		if lastReqAt != nil {
			entry["last_request_at"] = lastReqAt.Format(time.RFC3339)
		}
		out = append(out, entry)
	}
	writeJSONOk(w, out)
}

// handleModelCost returns per-model aggregated cost (last 7 days).
// Reads from model_cost_per_task_view.
//
// Query params:
//   - canonical_id : filter by canonical_id (optional)
//   - top          : limit rows (default 50)
func (h *AutoRouteHandlers) handleModelCost(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	canonicalID := r.URL.Query().Get("canonical_id")
	top := 50
	if v, err := strconv.Atoi(r.URL.Query().Get("top")); err == nil && v > 0 && v <= 500 {
		top = v
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	query := `
		SELECT canonical_id, raw_model, total_cost_usd, total_tokens,
		       avg_cost_per_1m_usd, success_rate, avg_latency_ms,
		       total_requests, unique_api_keys
		FROM model_cost_per_task_view
		WHERE 1=1
	`
	args := []interface{}{}
	if canonicalID != "" {
		args = append(args, canonicalID)
		query += fmt.Sprintf(" AND canonical_id = $%d", len(args))
	}
	args = append(args, top)
	query += fmt.Sprintf(" ORDER BY total_cost_usd DESC LIMIT $%d", len(args))

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		writeInternalErr(w, err)
		return
	}
	defer rows.Close()

	out := make([]map[string]interface{}, 0)
	for rows.Next() {
		var canonID *int
		var rawModel string
		var totalCost, avgCost1M *float64
		var totalTokens *int64
		var successRate, avgLatency *float64
		var totalReqs, uniqueKeys *int
		if err := rows.Scan(&canonID, &rawModel, &totalCost, &totalTokens,
			&avgCost1M, &successRate, &avgLatency,
			&totalReqs, &uniqueKeys); err != nil {
			continue
		}
		entry := map[string]interface{}{
			"raw_model": rawModel,
		}
		if canonID != nil {
			entry["canonical_id"] = *canonID
		}
		if totalCost != nil {
			entry["total_cost_usd"] = *totalCost
		}
		if totalTokens != nil {
			entry["total_tokens"] = *totalTokens
		}
		if avgCost1M != nil {
			entry["avg_cost_per_1m_usd"] = *avgCost1M
		}
		if successRate != nil {
			entry["success_rate"] = *successRate
		}
		if avgLatency != nil {
			entry["avg_latency_ms"] = *avgLatency
		}
		if totalReqs != nil {
			entry["total_requests"] = *totalReqs
		}
		if uniqueKeys != nil {
			entry["unique_api_keys"] = *uniqueKeys
		}
		out = append(out, entry)
	}
	writeJSONOk(w, out)
}

// writeJSONOk serialises v as JSON and writes 200. Errors are swallowed.
func writeJSONOk(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// writeJSONErr serialises an error envelope and writes the given status.
//
// v2.0.3 audit fix #13: never echo raw err.Error() to the client.
// pgx error messages can leak schema/table/column names — useful for
// an attacker doing reconnaissance. The generic message goes to the
// client; the detailed err is logged server-side via writeInternalErr.
func writeJSONErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]string{
			"message": msg,
			"type":    "admin_error",
		},
	})
}

// writeInternalErr logs the full error and writes a sanitised message
// to the client. Use for any 5xx path that would otherwise echo
// err.Error() directly.
func writeInternalErr(w http.ResponseWriter, err error) {
	slog.Error("admin auto-route internal error", "error", err.Error())
	writeJSONErr(w, http.StatusInternalServerError, "internal error (see server logs)")
}
