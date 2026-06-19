// admin/probe_history.go — probe-run history endpoints
//
// Endpoints, all behind superAdmin():
//
//   GET    /api/providers/:id/probe-history?limit=50
//   GET    /api/providers/:id/probe-history/recent-failures
//   POST   /api/providers/:id/probe-history/trigger
//   GET    /api/providers/:id/probe-states?state=recovering
//   GET    /api/routing/recent-model-failures  (used by model discovery badge)
//
// Spec: 2026-06-18-model-probe-rounds
package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"
)

// probeRunResponse is the row shape sent to the UI.
type probeRunResponse struct {
	ID            int64     `json:"id"`
	CredentialID  int       `json:"credential_id"`
	RawModel      string    `json:"raw_model_name"`
	Status        string    `json:"status"`
	HTTPStatus    *int      `json:"http_status"`
	ErrorCode     string    `json:"error_code"`
	ErrorMessage  string    `json:"error_message"`
	LatencyMs     int       `json:"latency_ms"`
	StateChange   string    `json:"state_change"`
	StateApplied  bool      `json:"state_applied"`
	TriggeredBy   string    `json:"triggered_by"`
	CreatedAt     time.Time `json:"created_at"`
}

// handleProviderProbeHistory returns the most recent probe runs for any
// credential × model belonging to this provider.  Filters: limit (1-200,
// default 50), status (optional: ok | http_4xx | http_5xx | network | auth | skipped).
func (h *Handler) handleProviderProbeHistory(w http.ResponseWriter, r *http.Request, providerID int) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	statusFilter := r.URL.Query().Get("status")

	args := []any{providerID, limit}
	statusClause := ""
	if statusFilter != "" {
		statusClause = " AND mpr.status = $3"
		args = append(args, statusFilter)
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rows, err := h.db.Query(ctx, `
		SELECT mpr.id, mpr.credential_id, mpr.raw_model_name, mpr.status,
		       mpr.http_status, COALESCE(mpr.error_code, ''), COALESCE(mpr.error_message, ''),
		       mpr.latency_ms, COALESCE(mpr.state_change, 'unchanged'), mpr.state_applied,
		       mpr.triggered_by, mpr.created_at
		FROM model_probe_runs mpr
		JOIN credentials c ON c.id = mpr.credential_id
		JOIN providers p ON p.id = c.provider_id
		WHERE p.id = $1`+statusClause+`
		ORDER BY mpr.created_at DESC
		LIMIT $2
	`, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}
	defer rows.Close()

	out := make([]probeRunResponse, 0, limit)
	for rows.Next() {
		var r probeRunResponse
		if err := rows.Scan(
			&r.ID, &r.CredentialID, &r.RawModel, &r.Status,
			&r.HTTPStatus, &r.ErrorCode, &r.ErrorMessage,
			&r.LatencyMs, &r.StateChange, &r.StateApplied,
			&r.TriggeredBy, &r.CreatedAt,
		); err != nil {
			continue
		}
		out = append(out, r)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"provider_id": providerID,
		"count":       len(out),
		"runs":        out,
	})
}

// handleProviderProbeHistoryRecentFailures returns aggregated last-6h
// failure counts per (model, credential) for the model-discovery badge.
func (h *Handler) handleProviderProbeHistoryRecentFailures(w http.ResponseWriter, r *http.Request, providerID int) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rows, err := h.db.Query(ctx, `
		SELECT raw_model_name,
		       COUNT(*) AS failed_count,
		       MAX(created_at) AS last_failed_at,
		       MIN(error_code) AS sample_error_code
		FROM model_probe_runs
		WHERE credential_id IN (
		    SELECT id FROM credentials WHERE provider_id = $1
		)
		  AND status <> 'ok'
		  AND status <> 'skipped'
		  AND created_at > NOW() - INTERVAL '6 hours'
		GROUP BY raw_model_name
		ORDER BY failed_count DESC, last_failed_at DESC
	`, providerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}
	defer rows.Close()

	type entry struct {
		RawModel      string    `json:"raw_model_name"`
		FailedCount   int       `json:"failed_count"`
		LastFailedAt  time.Time `json:"last_failed_at"`
		SampleErrCode string    `json:"sample_error_code"`
	}
	out := make([]entry, 0)
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.RawModel, &e.FailedCount, &e.LastFailedAt, &e.SampleErrCode); err != nil {
			continue
		}
		out = append(out, e)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"provider_id": providerID,
		"window":      "6h",
		"models":      out,
	})
}

// handleProviderProbeHistoryTrigger fires one off-schedule probe for a
// specific (credential, model) pair.  Body: {"credential_id": 11,
// "raw_model_name": "glm-5.1"}.
func (h *Handler) handleProviderProbeHistoryTrigger(w http.ResponseWriter, r *http.Request, providerID int) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.modelProbe == nil {
		writeError(w, http.StatusServiceUnavailable, "model probe runner not configured")
		return
	}
	var req struct {
		CredentialID  int    `json:"credential_id"`
		RawModelName  string `json:"raw_model_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	if req.CredentialID == 0 || req.RawModelName == "" {
		writeError(w, http.StatusBadRequest, "credential_id and raw_model_name required")
		return
	}
	// Ensure the credential belongs to this provider.
	var ok bool
	if err := h.db.QueryRow(r.Context(),
		`SELECT EXISTS (SELECT 1 FROM credentials WHERE id = $1 AND provider_id = $2)`,
		req.CredentialID, providerID).Scan(&ok); err != nil || !ok {
		writeError(w, http.StatusNotFound, "credential not found under provider")
		return
	}
	if err := h.modelProbe.TriggerManual(r.Context(), req.CredentialID, req.RawModelName); err != nil {
		writeError(w, http.StatusInternalServerError, "trigger failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"triggered": true})
}

// handleProviderProbeHistoryTriggerAll fires synchronous probes for ALL
// (credential, model) bindings under a provider and returns real-time
// per-binding results. It does NOT modify model_probe_state — only returns
// live probe results so operators can see actual availability without
// changing any state. Provider-side errors (network/auth/5xx/rate_limit)
// are reported separately from genuine model unavailability.
func (h *Handler) handleProviderProbeHistoryTriggerAll(w http.ResponseWriter, r *http.Request, providerID int) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.modelProbe == nil {
		writeError(w, http.StatusServiceUnavailable, "model probe runner not configured")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()

	results, err := h.modelProbe.TriggerAllSync(ctx, providerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "probe failed: "+err.Error())
		return
	}

	var ok, modelUnavailable, providerError, skipped int
	for _, r := range results {
		switch r.Category {
		case "ok":
			ok++
		case "model_unavailable":
			modelUnavailable++
		case "provider_error":
			providerError++
		case "skipped":
			skipped++
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"triggered":         true,
		"total":             len(results),
		"ok":                ok,
		"model_unavailable": modelUnavailable,
		"provider_error":    providerError,
		"skipped":           skipped,
		"results":           results,
	})
}

// handleProviderProbeStates returns the current consensus state for
// every (credential, model) under a provider.  Used by the providers-
// page "自动测试" tab to show "2/3 successful — next attempt in 4m".
// Optional ?state=recovering filter.
func (h *Handler) handleProviderProbeStates(w http.ResponseWriter, r *http.Request, providerID int) {
	if h.modelProbe == nil {
		writeError(w, http.StatusServiceUnavailable, "model probe runner not configured")
		return
	}
	stateFilter := r.URL.Query().Get("state")
	rows, err := h.modelProbe.ListStates(r.Context(), providerID, stateFilter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"provider_id":  providerID,
		"state_filter": stateFilter,
		"states":       rows,
	})
}

// handleRoutingRecentModelFailures is the global "model discovery"
// recent-failures endpoint — powers the failed-count badge that sits at
// the right end of the model-discovery column.
//
// v5 (2026-06-20): UNION ALL three data sources:
//   - model_probe_runs (active L1+L2+L4 probes)
//   - passive_probe_state (Layer 5 passive observation)
//   - request_logs (real traffic failures)
func (h *Handler) handleRoutingRecentModelFailures(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	rows, err := h.db.Query(ctx, `
		WITH active_failures AS (
			SELECT raw_model_name,
			       COUNT(DISTINCT credential_id) AS creds_affected,
			       COUNT(*) AS total_failures,
			       MAX(created_at) AS last_failed_at,
			       MIN(error_code) AS sample_error_code
			FROM model_probe_runs
			WHERE status NOT IN ('ok', 'skipped')
			  AND created_at > NOW() - INTERVAL '6 hours'
			GROUP BY raw_model_name
		),
		passive_failures AS (
			SELECT raw_model_name,
			       COUNT(DISTINCT credential_id) AS creds_affected,
			       SUM(consecutive_count) AS total_failures,
			       MAX(last_seen_at) AS last_failed_at,
			       MIN(error_kind) AS sample_error_code
			FROM passive_probe_state
			WHERE error_kind IN (
			    'model_not_found', 'quota_periodic', 'quota_balance',
			    'quota_permanent', 'rate_limit', 'auth', 'auth_revoked', 'upstream_down'
			)
			  AND (in_reviewing = TRUE OR final_marked_at > NOW() - INTERVAL '6 hours')
			  AND last_seen_at > NOW() - INTERVAL '6 hours'
			GROUP BY raw_model_name
		),
		request_log_failures AS (
			SELECT outbound_model AS raw_model_name,
			       COUNT(DISTINCT credential_id) AS creds_affected,
			       COUNT(*) AS total_failures,
			       MAX(ts) AS last_failed_at,
			       MIN(failure_detail_code) AS sample_error_code
			FROM request_logs
			WHERE success = FALSE
			  AND error_kind IN (
			    'model_not_found', 'quota', 'quota_periodic', 'quota_balance',
			    'quota_permanent', 'rate_limit', 'auth', 'auth_revoked', 'upstream_down'
			  )
			  AND ts > NOW() - INTERVAL '6 hours'
			  AND credential_id IS NOT NULL
			  AND outbound_model IS NOT NULL
			GROUP BY outbound_model
		),
		all_failures AS (
			SELECT 'active_probe' AS source, raw_model_name, creds_affected,
			       total_failures, last_failed_at, sample_error_code
			FROM active_failures
			UNION ALL
			SELECT 'passive_probe' AS source, raw_model_name, creds_affected,
			       total_failures, last_failed_at, sample_error_code
			FROM passive_failures
			UNION ALL
			SELECT 'request_logs' AS source, raw_model_name, creds_affected,
			       total_failures, last_failed_at, sample_error_code
			FROM request_log_failures
		),
		aggregated AS (
			SELECT
			    raw_model_name,
			    SUM(creds_affected) AS creds_affected,
			    SUM(total_failures) AS total_failures,
			    MAX(last_failed_at) AS last_failed_at,
			    MIN(sample_error_code) AS sample_error_code,
			    COALESCE(SUM(CASE WHEN source = 'active_probe' THEN total_failures ELSE 0 END), 0) AS active_probe_count,
			    COALESCE(SUM(CASE WHEN source = 'passive_probe' THEN total_failures ELSE 0 END), 0) AS passive_probe_count,
			    COALESCE(SUM(CASE WHEN source = 'request_logs' THEN total_failures ELSE 0 END), 0) AS request_logs_count
			FROM all_failures
			GROUP BY raw_model_name
		)
		SELECT
		    agg.raw_model_name,
		    agg.creds_affected,
		    agg.total_failures,
		    agg.last_failed_at,
		    agg.sample_error_code,
		    agg.active_probe_count,
		    agg.passive_probe_count,
		    agg.request_logs_count,
		    COALESCE(EXISTS(
		        SELECT 1 FROM passive_probe_state pps
		        WHERE pps.raw_model_name = agg.raw_model_name
		          AND pps.in_reviewing = TRUE
		    ), FALSE) AS in_reviewing,
		    mc.canonical_name
		FROM aggregated agg
		LEFT JOIN model_canonical mc ON mc.raw_model_name = agg.raw_model_name
		ORDER BY agg.total_failures DESC, agg.last_failed_at DESC
		LIMIT $1
	`, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}
	defer rows.Close()

	type sourceBreakdown struct {
		ActiveProbe  int `json:"active_probe"`
		PassiveProbe int `json:"passive_probe"`
		RequestLogs  int `json:"request_logs"`
	}
	type entry struct {
		RawModel       string          `json:"raw_model_name"`
		CanonicalName  *string         `json:"canonical_name"`
		CredsAffected  int             `json:"creds_affected"`
		TotalFailures  int             `json:"total_failures"`
		LastFailedAt   time.Time       `json:"last_failed_at"`
		SampleErrCode  string          `json:"sample_error_code"`
		Sources        sourceBreakdown `json:"sources"`
		InReviewing    bool            `json:"in_reviewing"`
	}
	out := make([]entry, 0, limit)
	var totalFails, totalCreds, totalModels, totalReviewing int
	for rows.Next() {
		var e entry
		var canon *string
		if err := rows.Scan(
			&e.RawModel, &e.CredsAffected, &e.TotalFailures, &e.LastFailedAt,
			&e.SampleErrCode,
			&e.Sources.ActiveProbe, &e.Sources.PassiveProbe, &e.Sources.RequestLogs,
			&e.InReviewing, &canon,
		); err != nil {
			continue
		}
		if canon != nil {
			e.CanonicalName = canon
		}
		out = append(out, e)
		totalFails += e.TotalFailures
		totalCreds += e.CredsAffected
		totalModels++
		if e.InReviewing {
			totalReviewing++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"window": "6h",
		"models": out,
		"totals": map[string]int{
			"total_failures":     totalFails,
			"models_affected":    totalModels,
			"creds_affected":     totalCreds,
			"models_in_reviewing": totalReviewing,
		},
	})
}
