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
//
// NOTE: All SELECTs on tenant-scoped tables (request_logs etc.) in this file
// are wrapped with tenantLogsClause() (admin/session_tenant.go) — adds
// "AND tenant_id = $N" for tenant_admin callers on non-default tenants.
// Super-admin / legacy admin_key / default-tenant callers see all rows.
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
	"github.com/kaixuan/llm-gateway-go/i18n"
	"github.com/kaixuan/llm-gateway-go/internal/jsonbody"
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
	// Explicit default routing (task_type × profile × tier × tenant)
	mux.HandleFunc("/api/admin/auto-route/defaults", adminWrap(h.HandleDefaultRoutingCollection))
	mux.HandleFunc("/api/admin/auto-route/defaults/", adminWrap(h.HandleDefaultRoutingItem))
	// Migration 478: learned affinity ranking read-only endpoints.
	mux.HandleFunc("/api/admin/auto-route/affinity", adminWrap(h.HandleAffinityRanking))
	mux.HandleFunc("/api/admin/auto-route/affinity/selections", adminWrap(h.handleAffinitySelections))
}

// handleDecisions returns the most recent N auto-route decisions from
// request_logs (filter: is_auto_request = TRUE).
//
// Query params:
//   - limit : max rows (default 50, max 500)
//   - task  : filter by task_type (optional)
//   - work_type : filter by work_type column (optional)
//   - model : filter by outbound_model — accepts canonical or raw alias (optional)
//   - profile : filter by auto_profile (optional)
func (h *AutoRouteHandlers) handleDecisions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONErrCtx(w, r, http.StatusMethodNotAllowed, "admin_method_not_allowed")
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
		FROM request_logs_with_current_month_without_customer_id
		WHERE is_auto_request = TRUE
		  AND ts >= NOW() - INTERVAL '7 days'
	`
	args := []interface{}{}
	tenantFrag, tenantArgs, _ := tenantLogsClause(r, len(args)+1)
	if tenantFrag != "" {
		query += tenantFrag
		args = append(args, tenantArgs...)
	}
	if task != "" {
		args = append(args, task)
		query += fmt.Sprintf(" AND task_type = $%d", len(args))
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
		query += fmt.Sprintf(" AND outbound_model = ANY($%d)", len(args))
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
		var reqID, taskType, prof, clientModel, outbound string
		var workTypeVal sql.NullString
		var apiKeyID, credentialID *int
		var confidence *float64
		var decision *string
		var success bool
		var latency *int
		if err := rows.Scan(&ts, &reqID, &apiKeyID, &taskType, &prof,
			&confidence, &clientModel, &outbound, &credentialID, &decision,
			&success, &latency, &workTypeVal); err != nil {
			continue
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
		writeJSONErrCtx(w, r, http.StatusMethodNotAllowed, "admin_method_not_allowed")
		return
	}
	canonicalID := r.URL.Query().Get("canonical_id")
	top := 100
	if v, err := strconv.Atoi(r.URL.Query().Get("top")); err == nil && v > 0 && v <= 1000 {
		top = v
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	// Check whether the index has any data at all. Read from the union
	// view (hot + cold) so we see the same rows the in-memory refresh sees.
	// Use a per-pair latest-bucket CTE (matching refreshIndexSQL) rather
	// than a single global MAX(bucket): the rollup dedup makes individual
	// buckets sparse, so a global MAX lands on whatever bucket had the
	// last metric change — typically 1-2 rows — and hides the rest of the
	// index. The per-pair CTE collects the freshest row for EVERY
	// (credential_id, raw_model) pair.
	var anyRows bool
	if err := h.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM credential_model_index_with_current_month)`).Scan(&anyRows); err != nil {
		writeInternalErr(w, err)
		return
	}
	if !anyRows {
		// Empty — return [] with warning so the admin UI shows
		// "waiting for first refresh" instead of an error.
		writeJSONOk(w, []map[string]interface{}{
			{"warning": "credential_model_index is empty; awaiting first bg worker refresh (within 5 minutes of gateway start)"},
		})
		return
	}

	query := `
		WITH latest_bucket AS (
		    SELECT credential_id, raw_model, MAX(bucket) AS bucket
		    FROM credential_model_index_with_current_month
		    GROUP BY credential_id, raw_model
		)
		SELECT cmi.credential_id, cmi.raw_model, cmi.canonical_id,
		       COALESCE(mc.canonical_name, ''), cmi.billing_mode,
		       cmi.unit_price_in_per_1m, cmi.unit_price_out_per_1m,
		       cmi.context_window, cmi.success_rate, cmi.p95_latency_ms,
		       cmi.active_sessions, cmi.concurrency_limit, cmi.pressure_ratio,
		       cmi.score_smart, cmi.score_speed_first, cmi.score_cost_first,
		       cmi.updated_at, cmi.bucket
		FROM credential_model_index_with_current_month cmi
		JOIN latest_bucket lb
		  ON lb.credential_id = cmi.credential_id
		 AND lb.raw_model     = cmi.raw_model
		 AND lb.bucket        = cmi.bucket
		LEFT JOIN models_canonical mc ON mc.id = cmi.canonical_id
		WHERE 1=1
	`
	args := []interface{}{}
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
		var updatedAt, rowBucket time.Time
		if err := rows.Scan(&credID, &rawModel, &canonicalIDVal, &canonicalName, &billingMode,
			&priceIn, &priceOut, &contextWindow, &successRate, &p95,
			&activeSessions, &concurrencyLimit, &pressureRatio,
			&scoreSmart, &scoreSpeed, &scoreCost, &updatedAt, &rowBucket); err != nil {
			continue
		}
		entry := map[string]interface{}{
			"bucket":        rowBucket.Format(time.RFC3339),
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
		writeJSONErrCtx(w, r, http.StatusMethodNotAllowed, "admin_method_not_allowed")
		return
	}

	apiKeyIDStr := r.URL.Query().Get("api_key_id")
	profile := r.URL.Query().Get("profile")
	if r.Method == http.MethodPut || r.Method == http.MethodPost {
		var body struct {
			APIKeyID int    `json:"api_key_id"`
			Profile  string `json:"profile"`
		}
		if err := jsonbody.DecodeRequest(r, &body, jsonbody.MaxOptionalBody, false); err != nil {
			writeJSONErr(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if body.APIKeyID > 0 {
			apiKeyIDStr = strconv.Itoa(body.APIKeyID)
		}
		if body.Profile != "" {
			profile = body.Profile
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
//
// 2026-06-24 fix: every per-row filter that previously used
// `is_auto_request = FALSE` was NULL-unsafe (PostgreSQL three-valued
// logic excludes rows where the bool column is NULL). Historical
// request_logs rows have is_auto_request IS NULL because the writer
// learned to set TRUE/FALSE only later. The query now uses
// `is_auto_request IS NOT TRUE` so NULL rows are admitted alongside
// TRUE auto requests and counted as specified_model_requests. Without
// this fix, the analytics tab rendered "暂无数据" / empty heatmap even
// though hundreds of explicit-model rows sat in the table.
func (h *AutoRouteHandlers) handleAudit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONErrCtx(w, r, http.StatusMethodNotAllowed, "admin_method_not_allowed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	out := map[string]interface{}{}

	// Total + success rate — over BOTH auto and specified-model requests
	// so the headline KPI reflects actual gateway volume.
	//
	// 2026-06-24 NULL-safe counters: `NOT is_auto_request` is NULL when the
	// bool column is NULL, which silently zeroes the SUM (PG aggregates
	// ignore NULL). Historical rows have is_auto_request IS NULL — they
	// are explicit-model requests, so they belong in totalSpecified and
	// in total. Use `COALESCE(is_auto_request, FALSE)` so the arithmetic
	// reads the NULL as FALSE instead of dropping it.
	var total, successes, totalAuto, totalSpecified int64
	auditTenantFrag, auditTenantArgs, _ := tenantLogsClause(r, 1)

	// 2026-08-31: try the audit-summary materialized view first (migration
	// 632 routing_audit_summary_7d). Tenant isolation: the MV is grouped by
	// tenant_id, and tenantLogsClause hands us the id as a *string* — if a
	// tenant-scoped caller's id cannot be extracted we MUST NOT take the
	// MV path (its nil-tenant branch sums every tenant); we fall back to
	// the base query which carries auditTenantFrag verbatim.
	var tenantID *string
	mvEligible := true
	if auditTenantFrag != "" {
		mvEligible = false
		if len(auditTenantArgs) > 0 {
			if tid, ok := auditTenantArgs[0].(string); ok && tid != "" {
				tenantID = &tid
				mvEligible = true
			}
		}
	}

	var mvTotal, mvSuccesses, mvAuto, mvSpecified int64
	var mvFound bool
	if mvEligible {
		mvTotal, mvSuccesses, mvAuto, mvSpecified, mvFound = getAuditSummaryMaterialized(ctx, h.db, tenantID)
	}
	if mvFound {
		total, successes, totalAuto, totalSpecified = mvTotal, mvSuccesses, mvAuto, mvSpecified
	} else {
		// Fallback to base view
		// 2026-08-31: query the _without_customer_id view. The customer_id LATERAL
		// join added by migration 575 makes every aggregation a 10s+ Seq Scan over
		// 314K rows in the August 2026 columnar partition. None of the audit
		// breakdowns (total, task_dist, profile_dist, top_models) consume
		// customer_id, so we skip the LATERAL JOIN entirely. The renamed view
		// was kept by migration 575 specifically for this purpose.
		var totalInt, successesInt, autoInt, specifiedInt int
		err := h.db.QueryRow(ctx, `
			SELECT
			  COUNT(*),
			  COALESCE(SUM(CASE WHEN success THEN 1 ELSE 0 END), 0),
			  COALESCE(SUM(CASE WHEN is_auto_request THEN 1 ELSE 0 END), 0),
			  COALESCE(SUM(CASE WHEN NOT COALESCE(is_auto_request, FALSE) THEN 1 ELSE 0 END), 0)
			FROM request_logs_with_current_month_without_customer_id
			WHERE ts >= NOW() - INTERVAL '7 days'
			  AND (
			    is_auto_request = TRUE
			    OR (is_auto_request IS NOT TRUE AND client_model IS NOT NULL AND client_model <> '')
			  )`+auditTenantFrag+`
		`, auditTenantArgs...).Scan(&totalInt, &successesInt, &autoInt, &specifiedInt)
		if err != nil {
			writeInternalErr(w, err)
			return
		}
		total, successes, totalAuto, totalSpecified = int64(totalInt), int64(successesInt), int64(autoInt), int64(specifiedInt)
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
		FROM request_logs_with_current_month_without_customer_id
		WHERE ts >= NOW() - INTERVAL '7 days'
		  AND (
		    is_auto_request = TRUE
		    OR (is_auto_request IS NOT TRUE AND client_model IS NOT NULL AND client_model <> '')
		  )`+auditTenantFrag+`
		GROUP BY (%s)
		ORDER BY COUNT(*) DESC
		LIMIT 20
	`, taskExpr, taskExpr), auditTenantArgs...)
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
		FROM request_logs_with_current_month_without_customer_id
		WHERE is_auto_request = TRUE
		  AND ts >= NOW() - INTERVAL '7 days'`+auditTenantFrag+`
		GROUP BY p
		ORDER BY COUNT(*) DESC
		LIMIT 10
	`, auditTenantArgs...)
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
		FROM request_logs_with_current_month_without_customer_id
		WHERE ts >= NOW() - INTERVAL '7 days'
		  AND COALESCE(NULLIF(outbound_model, ''), client_model) IS NOT NULL
		  AND (
		    is_auto_request = TRUE
		    OR (is_auto_request IS NOT TRUE AND client_model IS NOT NULL AND client_model <> '')
		  )`+auditTenantFrag+`
		GROUP BY m
		ORDER BY c DESC
		LIMIT 10
	`, auditTenantArgs...)
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
		writeJSONErrCtx(w, r, http.StatusMethodNotAllowed, "admin_method_not_allowed")
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
		writeJSONErrCtx(w, r, http.StatusMethodNotAllowed, "admin_method_not_allowed")
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
		writeJSONErrCtx(w, r, http.StatusMethodNotAllowed, "admin_method_not_allowed")
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
//
// DEPRECATED: prefer writeJSONErrCtx for i18n-aware responses.
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

// writeJSONErrCtx serialises an i18n-aware error envelope. The message is
// translated for the locale carried by r.Context() (set by the locale
// middleware). The messageKey should be one of the i18n message constants.
//
// For calls that need interpolation (e.g. "Model {{.Model}} not found"),
// pass templateData as the optional 4th argument.
func writeJSONErrCtx(w http.ResponseWriter, r *http.Request, status int, messageKey string, templateData ...map[string]any) {
	msg := i18n.T(r.Context(), messageKey, templateData...)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]string{
			"message": msg,
			"code":    messageKey, // stable machine-readable token
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

// ─────────────────────────────────────────────────────────────────────────
// Migration 478: affinity ranking endpoints
// ─────────────────────────────────────────────────────────────────────────

// affinityRankingRow is one row from task_model_affinity + view metadata.
// Mirrors v_task_model_ranking's column list; trimmed to what an operator
// reads: ranking, model, the numbers that explain it, currently_routable.
type affinityRankingRow struct {
	TaskType          string  `json:"task_type"`
	Profile           string  `json:"profile"`
	TenantID          string  `json:"tenant_id"`
	Rank              *int    `json:"rank,omitempty"`
	CanonicalID       int64   `json:"canonical_id"`
	CanonicalModel    string  `json:"canonical_model"`
	Affinity          float64 `json:"affinity"`
	Confidence        float64 `json:"confidence"`
	SampleCount       int     `json:"sample_count"`
	SuccessRate       float64 `json:"success_rate,omitempty"`
	AvgReward         float64 `json:"avg_reward,omitempty"`
	EMAReward         float64 `json:"ema_reward,omitempty"`
	AvgLatencyMs      *int    `json:"avg_latency_ms,omitempty"`
	AvgCostUSD        float64 `json:"avg_cost_usd,omitempty"`
	AvgHealth         float64 `json:"avg_health,omitempty"`
	CurrentlyRoutable bool    `json:"currently_routable"`
	LastSampledAt     string  `json:"last_sampled_at,omitempty"`
	UpdatedAt         string  `json:"updated_at,omitempty"`
}

// HandleAffinityRanking returns the learned task→model ranking.
//
// Query params:
//
//	task_type — required (e.g. "code", "chat")
//	profile   — optional (default: all profiles)
//	tenant_id — optional (default: "" = platform-level)
//
// Default limit is 50 rows (operator UI). Higher limits require an explicit
// `limit` param. Validation: profile must be one of "", smart, speed_first,
// cost_first when present.
func (h *AutoRouteHandlers) HandleAffinityRanking(w http.ResponseWriter, r *http.Request) {
	taskType := strings.TrimSpace(r.URL.Query().Get("task_type"))
	if taskType == "" {
		writeJSONErr(w, http.StatusBadRequest, "task_type query param is required")
		return
	}
	profile := r.URL.Query().Get("profile")
	if profile != "" && profile != "smart" && profile != "speed_first" && profile != "cost_first" {
		writeJSONErr(w, http.StatusBadRequest, "profile must be one of '', smart, speed_first, cost_first")
		return
	}
	tenantID := strings.TrimSpace(r.URL.Query().Get("tenant_id"))

	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 500 {
			writeJSONErr(w, http.StatusBadRequest, "limit must be 1..500")
			return
		}
		limit = n
	}

	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	rows, err := h.db.Query(ctx, `
			SELECT task_type, profile, tenant_id, rank,
			       canonical_id, canonical_model,
			       affinity, confidence,
			       sample_count, COALESCE(success_rate, 0),
			       COALESCE(avg_reward, 0), COALESCE(ema_reward, 0),
			       avg_latency_ms, COALESCE(avg_cost_usd, 0),
			       COALESCE(avg_health, 0), currently_routable,
			       COALESCE(last_sampled_at::text, ''), COALESCE(updated_at::text, '')
			FROM v_task_model_ranking
			WHERE task_type = $1
			  AND ($2 = '' OR profile = $2)
			  AND ($3 = '' OR tenant_id = $3)
			ORDER BY affinity DESC, sample_count DESC
			LIMIT $4
		`, taskType, profile, tenantID, limit)
	if err != nil {
		writeInternalErr(w, err)
		return
	}
	defer rows.Close()

	out := make([]affinityRankingRow, 0, limit)
	for rows.Next() {
		var r affinityRankingRow
		if err := rows.Scan(
			&r.TaskType, &r.Profile, &r.TenantID, &r.Rank,
			&r.CanonicalID, &r.CanonicalModel,
			&r.Affinity, &r.Confidence,
			&r.SampleCount, &r.SuccessRate,
			&r.AvgReward, &r.EMAReward,
			&r.AvgLatencyMs, &r.AvgCostUSD,
			&r.AvgHealth, &r.CurrentlyRoutable,
			&r.LastSampledAt, &r.UpdatedAt,
		); err != nil {
			writeInternalErr(w, err)
			return
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		writeInternalErr(w, err)
		return
	}

	writeJSONOk(w, map[string]any{
		"task_type": taskType,
		"profile":   profile,
		"tenant_id": tenantID,
		"rows":      out,
	})
}

// handleAffinitySelections returns the most recent auto_route_selections rows
// for a session, with the recorded decision snapshot and outcome. Used to
// trace the loop on a per-session basis during debugging.
//
// Query params:
//
//	session_id — required
//	limit      — optional, default 50, max 500
func (h *AutoRouteHandlers) handleAffinitySelections(w http.ResponseWriter, r *http.Request) {
	sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
	if sessionID == "" {
		writeJSONErr(w, http.StatusBadRequest, "session_id query param is required")
		return
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 500 {
			writeJSONErr(w, http.StatusBadRequest, "limit must be 1..500")
			return
		}
		limit = n
	}

	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	rows, err := h.db.Query(ctx, `
			SELECT request_id, task_type, profile, classifier,
			       confidence, canonical_id, chosen_model, candidate_rank,
			       composite_score, affinity_score, affinity_applied, explore,
			       fallback_used,
			       success, latency_ms, cost_usd, reward, reward_source,
			       ts::text, settled_at::text
			FROM auto_route_selections
			WHERE session_id = $1
			ORDER BY ts DESC
			LIMIT $2
		`, sessionID, limit)
	if err != nil {
		writeInternalErr(w, err)
		return
	}
	defer rows.Close()

	type selRow struct {
		RequestID       string   `json:"request_id"`
		TaskType        string   `json:"task_type"`
		Profile         string   `json:"profile"`
		Classifier      string   `json:"classifier"`
		Confidence      float64  `json:"confidence"`
		CanonicalID     *int64   `json:"canonical_id"`
		ChosenModel     string   `json:"chosen_model"`
		CandidateRank   int      `json:"candidate_rank"`
		CompositeScore  *float64 `json:"composite_score"`
		AffinityScore   *float64 `json:"affinity_score"`
		AffinityApplied bool     `json:"affinity_applied"`
		Explore         bool     `json:"explore"`
		FallbackUsed    bool     `json:"fallback_used"`
		Success         *bool    `json:"success"`
		LatencyMs       *int     `json:"latency_ms"`
		CostUSD         *float64 `json:"cost_usd"`
		Reward          *float64 `json:"reward"`
		RewardSource    *string  `json:"reward_source"`
		TS              string   `json:"ts"`
		SettledAt       string   `json:"settled_at,omitempty"`
	}
	out := make([]selRow, 0, limit)
	for rows.Next() {
		var x selRow
		if err := rows.Scan(
			&x.RequestID, &x.TaskType, &x.Profile, &x.Classifier,
			&x.Confidence, &x.CanonicalID, &x.ChosenModel, &x.CandidateRank,
			&x.CompositeScore, &x.AffinityScore, &x.AffinityApplied, &x.Explore,
			&x.FallbackUsed,
			&x.Success, &x.LatencyMs, &x.CostUSD, &x.Reward, &x.RewardSource,
			&x.TS, &x.SettledAt,
		); err != nil {
			writeInternalErr(w, err)
			return
		}
		out = append(out, x)
	}
	if err := rows.Err(); err != nil {
		writeInternalErr(w, err)
		return
	}

	writeJSONOk(w, map[string]any{
		"session_id": sessionID,
		"rows":       out,
	})
}
