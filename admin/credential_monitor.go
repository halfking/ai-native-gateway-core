package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/kaixuan/llm-gateway-go/credentialhealth"
	"github.com/redis/go-redis/v9"
)

// monitorSummaryCache memoizes the monitor-summary response for 30s. The page
// auto-refreshes every 10-60s and the per-model LATERAL success-rate join is
// the heaviest part, so a short TTL keeps the UX responsive without serving
// stale data for long.
var monitorSummaryCache = &summaryCache{ttl: 30 * time.Second}

type summaryCache struct {
	mu      sync.RWMutex
	ttl     time.Duration
	entries map[string]summaryCacheEntry
}

type summaryCacheEntry struct {
	value   map[string]any
	expires time.Time
}

func (c *summaryCache) get(key string) (map[string]any, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.entries == nil {
		return nil, false
	}
	e, ok := c.entries[key]
	if !ok || time.Now().After(e.expires) {
		return nil, false
	}
	return e.value, true
}

func (c *summaryCache) set(key string, value map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[string]summaryCacheEntry)
	}
	c.entries[key] = summaryCacheEntry{value: value, expires: time.Now().Add(c.ttl)}
}

// CredentialMonitorHandlers provides credential monitoring and manual promotion/demotion endpoints.
type CredentialMonitorHandlers struct {
	h           *Handler
	recorder    *credentialhealth.Recorder
	redisClient *redis.Client
}

// NewCredentialMonitorHandlers creates monitor handlers.
// recorder is optional (for sliding window queries).
func NewCredentialMonitorHandlers(h *Handler, recorder *credentialhealth.Recorder, redis *redis.Client) *CredentialMonitorHandlers {
	return &CredentialMonitorHandlers{
		h:           h,
		recorder:    recorder,
		redisClient: redis,
	}
}

// RegisterMonitorRoutes registers credential monitor routes.
func (m *CredentialMonitorHandlers) RegisterMonitorRoutes(mux *http.ServeMux, wrap func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("/api/credentials/monitor-summary", wrap(m.handleMonitorSummary))
	mux.HandleFunc("/api/credentials/sliding-window", wrap(m.handleSlidingWindow))
	mux.HandleFunc("/api/credentials/promote", wrap(m.handlePromote))
	mux.HandleFunc("/api/credentials/demote", wrap(m.handleDemote))
	mux.HandleFunc("/api/credentials/set-concurrency-auto", wrap(m.handleSetConcurrencyAuto))
	// 2026-06-23: per-(credential, model) manual online/offline + state-change history.
	mux.HandleFunc("/api/credentials/model-toggle", wrap(m.handleModelToggle))
	mux.HandleFunc("/api/credentials/model-history", wrap(m.handleModelHistory))
	// 2026-06-23: credential routing decisions + clear manual_disabled
	mux.HandleFunc("/api/credentials/decisions", wrap(m.handleCredentialDecisions))
	mux.HandleFunc("/api/credentials/clear-manual-disabled", wrap(m.handleClearManualDisabled))
}

// CredentialMonitorSummary represents a credential's monitoring state.
type CredentialMonitorSummary struct {
	ID                    int     `json:"id"`
	ProviderID            int     `json:"provider_id"`
	ProviderName          string  `json:"provider_name"`
	Label                 string  `json:"label"`
	Status                string  `json:"status"`
	AvailabilityState     string  `json:"availability_state"`
	HealthStatus          string  `json:"health_status"`
	QuotaState            string  `json:"quota_state"`
	ConcurrencyLimit      *int    `json:"concurrency_limit"`
	ConcurrencyLimitAuto  *int    `json:"concurrency_limit_auto"`
	EffectiveConcurrency  int     `json:"effective_concurrency"`
	ManualDisabled        bool    `json:"manual_disabled"`
	ConsecutiveFailures   int     `json:"consecutive_failures"`
	AvailabilityRecoverAt *string `json:"availability_recover_at"`
	StateReasonCode       *string `json:"state_reason_code"`
	StateReasonDetail     *string `json:"state_reason_detail"`
	HealthCheckedAt       *string `json:"health_checked_at"`
	TotalRequests         int64   `json:"total_requests"`
	// Models is the per-(credential, model) availability breakdown. Replaces the
	// single-model recent_window_stats field (2026-06-22). Each entry carries
	// the probe state, offer/binding availability and the live last-N success
	// rate so the monitor page can show model-level health instead of one
	// aggregate. nil when the credential has no model_offers rows.
	Models []CredentialModelStatus `json:"models,omitempty"`
	// AggregatedSuccessRate is the min recent success rate across the
	// credential's models (conservative — a credential is only as healthy as
	// its worst routable model). nil when there are no samples.
	AggregatedSuccessRate *float64 `json:"aggregated_success_rate,omitempty"`
}

// CredentialModelStatus is the per-(credential, model) availability row used
// by the credential monitor drawer. Mirrors the columns the routing candidate
// loader (loadCandidatesDB) consults so operators see the same view the router
// uses to admit/exclude a binding.
type CredentialModelStatus struct {
	RawModelName             string  `json:"raw_model_name"`
	OfferAvailable           bool    `json:"offer_available"`
	OfferUnavailableReason   *string `json:"offer_unavailable_reason,omitempty"`
	BindingAvailable         bool    `json:"binding_available"`
	BindingUnavailableReason *string `json:"binding_unavailable_reason,omitempty"`
	// ProbeState: 'broken_confirmed' | 'healthy_confirmed' | 'recovering' | 'unknown' (no row).
	ProbeState         string   `json:"probe_state"`
	ProbeLastStatus    *string  `json:"probe_last_status,omitempty"`
	ProbeLastAttemptAt *string  `json:"probe_last_attempt_at,omitempty"`
	RecentSuccessRate  *float64 `json:"recent_success_rate,omitempty"`
	RecentSamples      int      `json:"recent_samples"`
}

// WindowStats aggregates recent sliding window data.
type WindowStats struct {
	Total       int            `json:"total"`
	Success     int            `json:"success"`
	Failed      int            `json:"failed"`
	FailureRate float64        `json:"failure_rate"`
	ErrorKinds  map[string]int `json:"error_kinds"`
	SampleModel string         `json:"sample_model,omitempty"`
}

// handleMonitorSummary returns all credentials with their monitoring state.
// GET /api/credentials/monitor-summary?provider_id=X
//
// 2026-06-22: rewritten to return a per-(credential, model) breakdown in the
// `models` array instead of the single most-common-model `recent_window_stats`.
// One SQL query carries both the credential rows and their models[] (via
// json_agg over a LATERAL recent_success_rate join), so there is no N+1.
// A 30s in-memory cache (same pattern as loadCandidatesDB) keeps the page
// refresh cheap.
func (m *CredentialMonitorHandlers) handleMonitorSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	providerID := queryInt(r, "provider_id", 0)

	// 30s cache: the page auto-refreshes every 10-60s and the per-model
	// LATERAL success-rate join is the heaviest part. Cache key excludes
	// nothing but provider_id (the only variable input).
	cacheKey := fmt.Sprintf("p%d", providerID)
	if cached, ok := monitorSummaryCache.get(cacheKey); ok {
		writeJSON(w, http.StatusOK, cached)
		return
	}

	// Single query: one row per credential, with models[] built via a
	// correlated LATERAL subquery that enumerates model_offers and computes
	// each model's live success rate + probe state + binding availability.
	// COALESCE(recent_success_rate(...)) is STABLE and uses
	// idx_request_logs_credential_ts, so the per-model cost is a 50-row index
	// descent.
	query := `
		SELECT
			c.id, c.provider_id,
			COALESCE(p.display_name, p.catalog_code, '') AS provider_name,
			COALESCE(c.label, '') AS label,
			COALESCE(c.status, 'active') AS status,
			COALESCE(c.availability_state, 'ready') AS availability_state,
			COALESCE(c.health_status, 'unknown') AS health_status,
			COALESCE(c.quota_state, 'ok') AS quota_state,
			c.concurrency_limit,
			c.concurrency_limit_auto,
			COALESCE(c.concurrency_limit, c.concurrency_limit_auto, 5) AS effective_concurrency,
			COALESCE(c.manual_disabled, FALSE) AS manual_disabled,
			COALESCE(c.consecutive_failures, 0) AS consecutive_failures,
			c.availability_recover_at,
			c.state_reason_code,
			c.state_reason_detail,
			c.health_checked_at,
			COALESCE((SELECT COUNT(*) FROM request_logs rl WHERE rl.credential_id = c.id), 0) AS total_requests,
			COALESCE((
				SELECT json_agg(row_to_json(t))
				FROM (
					SELECT
						mo.raw_model_name,
						COALESCE(mo.available, TRUE) AS offer_available,
						mo.unavailable_reason AS offer_unavailable_reason,
						COALESCE(cmb.available, TRUE) AS binding_available,
						cmb.unavailable_reason AS binding_unavailable_reason,
						COALESCE(mps.state, 'unknown') AS probe_state,
						mps.last_status AS probe_last_status,
						mps.last_attempt_at AS probe_last_attempt_at,
						rsr.rate   AS recent_success_rate,
						COALESCE(rsr.samples, 0) AS recent_samples
					FROM model_offers mo
					LEFT JOIN credential_model_bindings cmb
						ON cmb.credential_id = mo.credential_id
					   AND cmb.provider_model_id = (SELECT id FROM provider_models pm WHERE pm.raw_model_name = mo.raw_model_name AND pm.provider_id = c.provider_id LIMIT 1)
					LEFT JOIN model_probe_state mps
						ON mps.credential_id = mo.credential_id
					   AND mps.raw_model_name = mo.raw_model_name
					CROSS JOIN LATERAL recent_success_rate(c.id, mo.raw_model_name, 50) AS rsr
					WHERE mo.credential_id = c.id
					ORDER BY COALESCE(rsr.samples, 0) DESC, mo.raw_model_name
				) t
			), '[]'::json) AS models
		FROM credentials c
		LEFT JOIN providers p ON c.provider_id = p.id
		WHERE ($1 = 0 OR c.provider_id = $1)
		  AND c.lifecycle_status != 'retired'
		ORDER BY c.provider_id, c.id
	`

	rows, err := m.h.db.Query(ctx, query, providerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("query failed: %v", err))
		return
	}
	defer rows.Close()

	summaries := make([]CredentialMonitorSummary, 0)
	for rows.Next() {
		var s CredentialMonitorSummary
		var recoverAt, checkedAt *time.Time
		var modelsJSON []byte
		if err := rows.Scan(
			&s.ID, &s.ProviderID, &s.ProviderName, &s.Label,
			&s.Status, &s.AvailabilityState, &s.HealthStatus, &s.QuotaState,
			&s.ConcurrencyLimit, &s.ConcurrencyLimitAuto, &s.EffectiveConcurrency,
			&s.ManualDisabled, &s.ConsecutiveFailures,
			&recoverAt, &s.StateReasonCode, &s.StateReasonDetail,
			&checkedAt, &s.TotalRequests, &modelsJSON,
		); err != nil {
			continue
		}

		if recoverAt != nil {
			t := recoverAt.Format(time.RFC3339)
			s.AvailabilityRecoverAt = &t
		}
		if checkedAt != nil {
			t := checkedAt.Format(time.RFC3339)
			s.HealthCheckedAt = &t
		}

		// Decode the models JSON array into typed structs and compute the
		// aggregated (min) success rate across routable models.
		// Initialize as a non-nil slice so the JSON response always serializes
		// to "models":[] (never null). The SQL COALESCE(...,'[]'::json) already
		// prevents null, but this is belt-and-braces in case of future schema
		// drift.
		models := make([]CredentialModelStatus, 0)
		if len(modelsJSON) > 0 && string(modelsJSON) != "null" {
			_ = json.Unmarshal(modelsJSON, &models)
		}
		s.Models = models
		var minRate *float64
		for i := range models {
			ms := &models[i]
			// Parse probe timestamps back to RFC3339 strings for the API.
			if ms.RecentSuccessRate != nil {
				if minRate == nil || *ms.RecentSuccessRate < *minRate {
					r := *ms.RecentSuccessRate
					minRate = &r
				}
			}
			// probe_last_attempt_at comes through as a string in the JSON; keep as-is.
		}
		s.AggregatedSuccessRate = minRate

		summaries = append(summaries, s)
	}

	resp := map[string]any{
		"credentials": summaries,
		"count":       len(summaries),
	}
	monitorSummaryCache.set(cacheKey, resp)
	writeJSON(w, http.StatusOK, resp)
}

// handleSlidingWindow returns raw sliding window data for a credential.
// GET /api/credentials/sliding-window?credential_id=X&model=Y&minutes=60&limit=50
//
// 2026-06-22: Redis is not configured on every environment, so when the
// recorder is unavailable (or returns no data) we fall back to a direct
// request_logs query. The response carries `source` ("redis" or
// "request_logs") so the UI can show where the data came from.
func (m *CredentialMonitorHandlers) handleSlidingWindow(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Lazy init recorder if redis is available
	if m.recorder == nil && m.redisClient != nil {
		m.recorder = credentialhealth.NewRecorder(m.redisClient, 2*time.Hour, 100)
	}

	credentialID := queryInt(r, "credential_id", 0)
	model := queryString(r, "model")
	minutes := queryInt(r, "minutes", 60)
	limit := queryInt(r, "limit", 50) // default 50, show most recent 50 entries

	if credentialID == 0 {
		writeError(w, http.StatusBadRequest, "credential_id required")
		return
	}
	if model == "" {
		writeError(w, http.StatusBadRequest, "model required")
		return
	}
	if limit < 1 || limit > 500 {
		writeError(w, http.StatusBadRequest, "limit must be 1-500")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// ── Primary: Redis recorder (per-call granularity) ──────────────────
	source := "redis"
	// Initialize as a non-nil slice so the JSON response serializes to [] (not
	// null) when there are no entries — otherwise the frontend's
	// windowEntries.length throws "Cannot read properties of null".
	entries := make([]credentialhealth.CallEntry, 0)
	if m.recorder != nil && m.recorder.Enabled() {
		since := time.Now().Add(-time.Duration(minutes) * time.Minute)
		entries, _ = m.recorder.GetRecent(ctx, credentialID, model, since)
	}

	// ── Fallback: request_logs (when Redis is down or empty) ────────────
	if len(entries) == 0 {
		source = "request_logs"
		rlEntries, err := m.slidingWindowFromRequestLogs(ctx, credentialID, model, minutes, limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to get window: %v", err))
			return
		}
		entries = rlEntries
	}

	// Guard against nil (Redis GetRecent + the fallback both return nil when
	// empty). A nil slice serializes to JSON null, which crashes the frontend
	// (windowEntries.length). Force a non-nil empty slice.
	if entries == nil {
		entries = make([]credentialhealth.CallEntry, 0)
	}

	// Limit to requested count (entries are already newest-first from Redis)
	if len(entries) > limit {
		entries = entries[:limit]
	}

	stats := credentialhealth.ComputeStats(entries)

	writeJSON(w, http.StatusOK, map[string]any{
		"credential_id":  credentialID,
		"model":          model,
		"window_minutes": minutes,
		"limit":          limit,
		"source":         source,
		"total_returned": len(entries),
		"entries":        entries,
		"stats": map[string]any{
			"total":        stats.Total,
			"success":      stats.Success,
			"failed":       stats.Failed,
			"failure_rate": stats.FailureRate,
			"error_kinds":  stats.ErrorKinds,
		},
	})
}

// slidingWindowFromRequestLogs builds CallEntry timeline data directly from
// the request_logs table. Used when the Redis recorder is unavailable. Uses
// idx_request_logs_credential_ts (credential_id, ts DESC) so the LIMIT scan
// is an index descent.
func (m *CredentialMonitorHandlers) slidingWindowFromRequestLogs(ctx context.Context, credentialID int, model string, minutes, limit int) ([]credentialhealth.CallEntry, error) {
	rows, err := m.h.db.Query(ctx, `
		SELECT COALESCE(request_id, ''), EXTRACT(EPOCH FROM ts)::bigint * 1000,
		       success, COALESCE(latency_ms, 0), COALESCE(error_kind, '')
		FROM request_logs
		WHERE credential_id = $1
		  -- case-insensitive: request_logs.outbound_model can differ in case
		  -- from model_offers.raw_model_name (e.g. "MiniMax-M3" vs "minimax-m3"),
		  -- so a plain = misses most rows.
		  AND lower(COALESCE(outbound_model, client_model)) = lower($2)
		  AND ts > NOW() - ($3 || ' minutes')::interval
		ORDER BY ts DESC
		LIMIT $4
	`, credentialID, model, fmt.Sprintf("%d", minutes), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// Non-nil so the caller (and the JSON response) gets [] rather than null.
	out := make([]credentialhealth.CallEntry, 0)
	for rows.Next() {
		var e credentialhealth.CallEntry
		var ok bool
		var errKind string
		if err := rows.Scan(&e.RequestID, &e.Timestamp, &ok, &e.LatencyMs, &errKind); err != nil {
			continue
		}
		e.Success = ok
		if !ok && errKind != "" {
			e.ErrorKind = errKind
		}
		out = append(out, e)
	}
	return out, nil
}

// handlePromote manually promotes a credential (force recover).
// POST /api/credentials/promote
// Body: {"credential_id": 123, "reason": "手动恢复"}
func (m *CredentialMonitorHandlers) handlePromote(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req struct {
		CredentialID int    `json:"credential_id"`
		Reason       string `json:"reason"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	if req.CredentialID == 0 {
		writeError(w, http.StatusBadRequest, "credential_id required")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// Force recover: set availability_state = 'ready', clear recover_at
	_, err := m.h.db.Exec(ctx, `
		UPDATE credentials
		SET availability_state = 'ready',
		    availability_recover_at = NULL,
		    state_reason_code = NULL,
		    state_reason_detail = $1,
		    state_updated_at = NOW()
		WHERE id = $2
	`, "manual_promote: "+req.Reason, req.CredentialID)

	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("update failed: %v", err))
		return
	}

	m.h.auditLog("admin", "credential.promote", "credential", req.CredentialID, map[string]any{
		"reason": req.Reason,
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": "credential promoted",
	})
}

// handleDemote manually demotes a credential (set degraded + auto-recover).
// POST /api/credentials/demote
// Body: {"credential_id": 123, "reason": "手动降级", "recover_after_hours": 2}
func (m *CredentialMonitorHandlers) handleDemote(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req struct {
		CredentialID      int     `json:"credential_id"`
		Reason            string  `json:"reason"`
		RecoverAfterHours float64 `json:"recover_after_hours"` // default 2
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	if req.CredentialID == 0 {
		writeError(w, http.StatusBadRequest, "credential_id required")
		return
	}
	if req.RecoverAfterHours == 0 {
		req.RecoverAfterHours = 2
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	recoverAt := time.Now().Add(time.Duration(req.RecoverAfterHours * float64(time.Hour)))

	_, err := m.h.db.Exec(ctx, `
		UPDATE credentials
		SET availability_state = 'degraded',
		    availability_recover_at = $1,
		    state_reason_code = 'manual_demote',
		    state_reason_detail = $2,
		    state_updated_at = NOW()
		WHERE id = $3
	`, recoverAt, req.Reason, req.CredentialID)

	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("update failed: %v", err))
		return
	}

	m.h.auditLog("admin", "credential.demote", "credential", req.CredentialID, map[string]any{
		"reason":              req.Reason,
		"recover_after_hours": req.RecoverAfterHours,
		"recover_at":          recoverAt.Format(time.RFC3339),
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"success":    true,
		"message":    "credential demoted",
		"recover_at": recoverAt.Format(time.RFC3339),
	})
}

// handleSetConcurrencyAuto manually sets concurrency_limit_auto.
// POST /api/credentials/set-concurrency-auto
// Body: {"credential_id": 123, "concurrency_limit_auto": 10, "reason": "手动调整"}
func (m *CredentialMonitorHandlers) handleSetConcurrencyAuto(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req struct {
		CredentialID         int    `json:"credential_id"`
		ConcurrencyLimitAuto int    `json:"concurrency_limit_auto"`
		Reason               string `json:"reason"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	if req.CredentialID == 0 {
		writeError(w, http.StatusBadRequest, "credential_id required")
		return
	}
	if req.ConcurrencyLimitAuto < 1 {
		writeError(w, http.StatusBadRequest, "concurrency_limit_auto must be >= 1")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	_, err := m.h.db.Exec(ctx, `
		UPDATE credentials
		SET concurrency_limit_auto = $1,
		    state_updated_at = NOW()
		WHERE id = $2
	`, req.ConcurrencyLimitAuto, req.CredentialID)

	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("update failed: %v", err))
		return
	}

	m.h.auditLog("admin", "credential.set_concurrency_auto", "credential", req.CredentialID, map[string]any{
		"concurrency_limit_auto": req.ConcurrencyLimitAuto,
		"reason":                 req.Reason,
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": "concurrency_limit_auto updated",
	})
}

// ──────────────────────────────────────────────────────────────────────────────
// Per-(credential, model) manual online/offline toggle + state-change history
//
// Added 2026-06-23 for the credential monitor drawer. Operators can flip a
// single model binding off the candidate pool immediately (or back on) without
// touching the rest of the credential. The status is overlaid on top of the
// automatic probe consensus:
//
//   - "manual_offline" unavailable_reason is treated by the probe runner
//     cycle's SQL filter (AND COALESCE(cmb.unavailable_reason,'') <> 'manual')
//     and by applyResult's NOT LIKE 'manual%' guard as a sticky lock — the
//     auto probe will NOT touch it until the operator toggles it back to
//     "online".
//
//   - "online" clears unavailable_reason='manual_offline' and resets
//     model_probe_state to 'recovering' with next_retry_at=NOW() so the next
//     10-min probe cycle re-evaluates immediately. It does NOT touch bindings
//     with auto reasons (e.g. 'model_probe_broken') — those are owned by the
//     auto probe and only recover when consensus flips back to healthy.
//
// All operations are logged to routing_audit_log with action
// 'credential.model_toggle_online' / 'credential.model_toggle_offline' and
// target_type='credential_model' (the JSON payload carries credential_id +
// raw_model_name since the audit table's target_id is a single bigint).
// ──────────────────────────────────────────────────────────────────────────────

// modelToggleRequest is the POST body for /api/credentials/model-toggle.
type modelToggleRequest struct {
	CredentialID int    `json:"credential_id"`
	RawModel     string `json:"raw_model_name"`
	Action       string `json:"action"` // "online" | "offline"
	Reason       string `json:"reason"` // required, ≤500 chars; written to audit
}

// ModelToggleResponse is returned by /api/credentials/model-toggle.
type ModelToggleResponse struct {
	Success           bool    `json:"success"`
	Available         bool    `json:"available"`
	UnavailableReason *string `json:"unavailable_reason,omitempty"`
	PrevAvailable     bool    `json:"prev_available"`
	PrevReason        *string `json:"prev_reason,omitempty"`
	Action            string  `json:"action"`
}

// validateModelToggleRequest is the pure pre-DB validation for the
// /api/credentials/model-toggle body. Split out of handleModelToggle so
// unit tests can exercise it without standing up a pgxpool.
func validateModelToggleRequest(req *modelToggleRequest) error {
	if req.CredentialID == 0 || req.RawModel == "" {
		return fmt.Errorf("credential_id and raw_model_name required")
	}
	if req.Action != "online" && req.Action != "offline" {
		return fmt.Errorf(`action must be "online" or "offline"`)
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		return fmt.Errorf("reason is required")
	}
	if len(reason) > 500 {
		return fmt.Errorf("reason must be ≤500 chars")
	}
	return nil
}

// handleModelToggle manually toggles a (credential_id, raw_model_name) binding
// online or offline.
//
// POST /api/credentials/model-toggle
// Body: {"credential_id": 11, "raw_model_name": "gpt-4o", "action": "offline", "reason": "误判 broken"}
func (m *CredentialMonitorHandlers) handleModelToggle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if m.h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}

	var req modelToggleRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if err := validateModelToggleRequest(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	reason := strings.TrimSpace(req.Reason)

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	tx, err := m.h.db.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "begin tx: "+err.Error())
		return
	}
	defer tx.Rollback(context.Background())

	// Lock the binding row for the duration of the tx so concurrent toggles
	// and the auto probe runner cannot interleave.
	var prevAvailable bool
	var prevReason *string
	if err := tx.QueryRow(ctx, `
		SELECT cmb.available, cmb.unavailable_reason
		FROM credential_model_bindings cmb
		JOIN provider_models pm ON pm.id = cmb.provider_model_id
		WHERE cmb.credential_id = $1 AND pm.raw_model_name = $2
		FOR UPDATE OF cmb
	`, req.CredentialID, req.RawModel).Scan(&prevAvailable, &prevReason); err != nil {
		if err == pgx.ErrNoRows {
			writeError(w, http.StatusNotFound, "binding not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "lookup failed: "+err.Error())
		return
	}

	var newAvailable bool
	var newReason *string
	if req.Action == "offline" {
		offlineReason := "manual_offline"
		if _, err := tx.Exec(ctx, `
			UPDATE credential_model_bindings cmb
			SET available = FALSE,
			    unavailable_reason = $3,
			    unavailable_at = NOW()
			FROM provider_models pm
			WHERE cmb.provider_model_id = pm.id
			  AND cmb.credential_id = $1
			  AND pm.raw_model_name = $2
		`, req.CredentialID, req.RawModel, offlineReason); err != nil {
			writeError(w, http.StatusInternalServerError, "offline update failed: "+err.Error())
			return
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO model_probe_state
			    (credential_id, raw_model_name, state,
			     consecutive_successes, consecutive_failures, total_attempts,
			     last_attempt_at, next_retry_at, last_status)
			VALUES ($1, $2, 'unknown', 0, 0, 0, NOW(), NOW() + INTERVAL '100 years', 'manual_offline')
			ON CONFLICT (credential_id, raw_model_name) DO UPDATE SET
			    state = 'unknown',
			    consecutive_successes = 0,
			    consecutive_failures = 0,
			    last_attempt_at = NOW(),
			    next_retry_at = NOW() + INTERVAL '100 years',
			    last_status = 'manual_offline'
		`, req.CredentialID, req.RawModel); err != nil {
			writeError(w, http.StatusInternalServerError, "probe state reset failed: "+err.Error())
			return
		}
		newAvailable = false
		newReason = &offlineReason
	} else {
		// Online: only clear manual_offline locks. We refuse to override
		// auto reasons (e.g. model_probe_broken) — those are owned by the
		// probe consensus and should be re-evaluated by TriggerManual or
		// a natural cycle, not by an operator clicking "online".
		if prevReason == nil || *prevReason != "manual_offline" {
			writeError(w, http.StatusConflict,
				"binding is not in manual_offline state (current: "+
					strconvIfaceDeref(prevReason)+"); only manual_offline can be toggled back to online")
			return
		}
		if _, err := tx.Exec(ctx, `
			UPDATE credential_model_bindings cmb
			SET available = TRUE,
			    unavailable_reason = NULL,
			    unavailable_at = NULL
			FROM provider_models pm
			WHERE cmb.provider_model_id = pm.id
			  AND cmb.credential_id = $1
			  AND pm.raw_model_name = $2
		`, req.CredentialID, req.RawModel); err != nil {
			writeError(w, http.StatusInternalServerError, "online update failed: "+err.Error())
			return
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO model_probe_state
			    (credential_id, raw_model_name, state,
			     consecutive_successes, consecutive_failures, total_attempts,
			     last_attempt_at, next_retry_at, last_status)
			VALUES ($1, $2, 'recovering', 0, 0, 0, NOW(), NOW(), 'manual_online')
			ON CONFLICT (credential_id, raw_model_name) DO UPDATE SET
			    state = 'recovering',
			    consecutive_successes = 0,
			    consecutive_failures = 0,
			    last_attempt_at = NOW(),
			    next_retry_at = NOW(),
			    last_status = 'manual_online'
		`, req.CredentialID, req.RawModel); err != nil {
			writeError(w, http.StatusInternalServerError, "probe state reset failed: "+err.Error())
			return
		}
		newAvailable = true
		newReason = nil
	}

	if err := tx.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "commit failed: "+err.Error())
		return
	}

	// Audit log: target_id uses credential_id; raw_model_name + reason go
	// in the JSON payload. The handleModelHistory query keys on the JSON
	// fields so the composite lookup is symmetric.
	m.h.auditLog("admin",
		"credential.model_toggle_"+req.Action,
		"credential_model",
		req.CredentialID,
		map[string]any{
			"credential_id":  req.CredentialID,
			"raw_model_name": req.RawModel,
			"reason":         reason,
			"prev_available": prevAvailable,
			"prev_reason":    prevReason,
			"new_available":  newAvailable,
			"new_reason":     newReason,
		},
	)

	writeJSON(w, http.StatusOK, ModelToggleResponse{
		Success:           true,
		Available:         newAvailable,
		UnavailableReason: newReason,
		PrevAvailable:     prevAvailable,
		PrevReason:        prevReason,
		Action:            req.Action,
	})
}

// strconvIfaceDeref dereferences a *string for human-readable error messages;
// returns "<nil>" if p is nil.
func strconvIfaceDeref(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

// ModelHistoryEvent is one row in the response of /api/credentials/model-history.
//
// The "source" field is the operator-visible tag: "auto" for probe consensus
// transitions, "manual" for the operator toggles added 2026-06-23.
type ModelHistoryEvent struct {
	TS           string  `json:"ts"`
	Source       string  `json:"source"`
	TriggeredBy  *string `json:"triggered_by"`
	Event        string  `json:"event"`
	ProbeStatus  *string `json:"probe_status"`
	HTTPStatus   *int    `json:"http_status"`
	ErrorCode    *string `json:"error_code"`
	ErrorMessage *string `json:"error_message"`
	Actor        *string `json:"actor"`
	Reason       *string `json:"reason"`
}

// handleModelHistory returns the merged history of automatic probe state
// transitions and manual operator toggles for a single (credential, model)
// binding, ordered by timestamp DESC.
//
// GET /api/credentials/model-history?credential_id=X&raw_model_name=Y&limit=50
func (m *CredentialMonitorHandlers) handleModelHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if m.h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}

	credentialID := queryInt(r, "credential_id", 0)
	rawModel := queryString(r, "raw_model_name")
	limit := queryInt(r, "limit", 50)
	if credentialID == 0 {
		writeError(w, http.StatusBadRequest, "credential_id required")
		return
	}
	if rawModel == "" {
		writeError(w, http.StatusBadRequest, "raw_model_name required")
		return
	}
	if limit < 1 || limit > 200 {
		writeError(w, http.StatusBadRequest, "limit must be 1-200")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rows, err := m.h.db.Query(ctx, `
		WITH auto_events AS (
			SELECT
				mpr.created_at      AS ts,
				'auto'              AS source,
				mpr.triggered_by    AS triggered_by,
				mpr.state_change    AS event,
				mpr.status          AS probe_status,
				mpr.http_status     AS http_status,
				mpr.error_code      AS error_code,
				mpr.error_message   AS error_message,
				NULL::text          AS actor,
				NULL::text          AS reason
			FROM model_probe_runs mpr
			WHERE mpr.credential_id = $1
			  AND mpr.raw_model_name = $2
			  AND mpr.state_change IN ('recovered', 'broke')
		),
		manual_events AS (
			SELECT
				al.ts               AS ts,
				'manual'            AS source,
				NULL::text          AS triggered_by,
				CASE al.action
				    WHEN 'credential.model_toggle_online'  THEN 'online'
				    WHEN 'credential.model_toggle_offline' THEN 'offline'
				END                 AS event,
				NULL::text          AS probe_status,
				NULL::int           AS http_status,
				NULL::text          AS error_code,
				NULL::text          AS error_message,
				al.actor            AS actor,
				COALESCE(al.after_json->>'reason', '') AS reason
			FROM routing_audit_log al
			WHERE al.target_type = 'credential_model'
			  AND al.action IN ('credential.model_toggle_online', 'credential.model_toggle_offline')
			  AND (al.after_json->>'credential_id')::int = $1
			  AND al.after_json->>'raw_model_name' = $2
		)
		SELECT ts, source, triggered_by, event,
		       probe_status, http_status, error_code, error_message,
		       actor, reason
		FROM (
			SELECT * FROM auto_events
			UNION ALL
			SELECT * FROM manual_events
		) u
		ORDER BY ts DESC
		LIMIT $3
	`, credentialID, rawModel, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}
	defer rows.Close()

	events := make([]ModelHistoryEvent, 0)
	for rows.Next() {
		var (
			ev          ModelHistoryEvent
			probeStatus *string
			httpStatus  *int
			errCode     *string
			errMsg      *string
			actor       *string
			reason      *string
		)
		var ts time.Time
		if err := rows.Scan(&ts, &ev.Source, &ev.TriggeredBy, &ev.Event,
			&probeStatus, &httpStatus, &errCode, &errMsg,
			&actor, &reason); err != nil {
			continue
		}
		ev.TS = ts.UTC().Format(time.RFC3339)
		ev.ProbeStatus = probeStatus
		ev.HTTPStatus = httpStatus
		ev.ErrorCode = errCode
		ev.ErrorMessage = errMsg
		ev.Actor = actor
		if reason != nil && *reason != "" {
			ev.Reason = reason
		} else {
			ev.Reason = nil
		}
		events = append(events, ev)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"credential_id":  credentialID,
		"raw_model_name": rawModel,
		"events":         events,
		"count":          len(events),
	})
}

// handleCredentialDecisions returns recent routing decisions for a specific credential (2026-06-23).
// GET /api/credentials/decisions?credential_id=123&limit=50
//
// Returns routing_decision_log entries where chosen_credential_id matches.
// Useful for the credential detail drawer to show "what traffic is this credential handling".
func (m *CredentialMonitorHandlers) handleCredentialDecisions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	credentialID := queryInt(r, "credential_id", 0)
	if credentialID == 0 {
		writeError(w, http.StatusBadRequest, "credential_id required")
		return
	}

	limit := queryInt(r, "limit", 50)
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	// tenant_admin callers can only see decisions for their own tenant
	var tenantClause string
	var args []any
	if IsTenantAdmin(r) {
		tenantClause = "AND rdl.tenant_id = $2"
		args = []any{credentialID, GetTenantID(r), limit}
	} else {
		args = []any{credentialID, limit}
	}

	q := fmt.Sprintf(`
		SELECT rdl.ts, rdl.request_id::text, rdl.model, rdl.tier, rdl.success,
		       rdl.latency_ms, rdl.error_class, rdl.chosen_provider_id,
		       rdl.client_model, rdl.outbound_model, rdl.sticky_hit
		FROM routing_decision_log rdl
		WHERE rdl.chosen_credential_id = $1 %s
		ORDER BY rdl.ts DESC
		LIMIT $%d
	`, tenantClause, len(args))

	rows, err := m.h.db.Query(ctx, q, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}
	defer rows.Close()

	type Decision struct {
		TS               string  `json:"ts"`
		RequestID        string  `json:"request_id"`
		Model            string  `json:"model"`
		Tier             *int    `json:"tier"`
		Success          bool    `json:"success"`
		LatencyMs        *int    `json:"latency_ms"`
		ErrorClass       *string `json:"error_class"`
		ChosenProviderID *int    `json:"chosen_provider_id"`
		ClientModel      *string `json:"client_model"`
		OutboundModel    *string `json:"outbound_model"`
		StickyHit        *bool   `json:"sticky_hit"`
	}

	decisions := make([]Decision, 0)
	for rows.Next() {
		var d Decision
		var ts time.Time
		if err := rows.Scan(&ts, &d.RequestID, &d.Model, &d.Tier, &d.Success,
			&d.LatencyMs, &d.ErrorClass, &d.ChosenProviderID,
			&d.ClientModel, &d.OutboundModel, &d.StickyHit); err != nil {
			continue
		}
		d.TS = ts.UTC().Format(time.RFC3339)
		decisions = append(decisions, d)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"credential_id": credentialID,
		"decisions":     decisions,
		"total":         len(decisions),
	})
}

// handleClearManualDisabled clears manual_disabled flag on a credential (2026-06-23).
// POST /api/credentials/clear-manual-disabled
// Body: {"credential_id": 123, "reason": "supplier restored"}
//
// Sets manual_disabled = false and writes audit log. This is a quick "restore to pool" action
// for operators who need to undo a manual disable without going through the full promote flow.
func (m *CredentialMonitorHandlers) handleClearManualDisabled(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req struct {
		CredentialID int    `json:"credential_id"`
		Reason       string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.CredentialID == 0 {
		writeError(w, http.StatusBadRequest, "credential_id required")
		return
	}
	if req.Reason == "" {
		writeError(w, http.StatusBadRequest, "reason required")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// tenant_admin can only clear credentials in their tenant
	var tenantCheck string
	var checkArgs []any
	if IsTenantAdmin(r) {
		tenantCheck = "AND tenant_id = $2"
		checkArgs = []any{req.CredentialID, GetTenantID(r)}
	} else {
		checkArgs = []any{req.CredentialID}
	}

	// Check credential exists and get current state
	var exists bool
	var currentDisabled bool
	checkQ := fmt.Sprintf("SELECT true, COALESCE(manual_disabled, false) FROM credentials WHERE id = $1 %s", tenantCheck)
	if err := m.h.db.QueryRow(ctx, checkQ, checkArgs...).Scan(&exists, &currentDisabled); err != nil {
		if err == pgx.ErrNoRows {
			writeError(w, http.StatusNotFound, "credential not found")
		} else {
			writeError(w, http.StatusInternalServerError, "check failed: "+err.Error())
		}
		return
	}

	// Clear manual_disabled
	var updateArgs []any
	if IsTenantAdmin(r) {
		updateArgs = []any{req.CredentialID, GetTenantID(r)}
	} else {
		updateArgs = []any{req.CredentialID}
	}
	updateQ := fmt.Sprintf("UPDATE credentials SET manual_disabled = false WHERE id = $1 %s", tenantCheck)
	if _, err := m.h.db.Exec(ctx, updateQ, updateArgs...); err != nil {
		writeError(w, http.StatusInternalServerError, "update failed: "+err.Error())
		return
	}

	// Write audit log
	actor := r.RemoteAddr
	if actor == "" {
		actor = "unknown"
	}
	auditDetails := map[string]any{
		"credential_id":     req.CredentialID,
		"reason":            req.Reason,
		"previous_disabled": currentDisabled,
	}
	detailsJSON, _ := json.Marshal(auditDetails)
	//nolint:errcheck
	m.h.db.Exec(ctx, `
		INSERT INTO routing_audit_log (actor, action, target_type, target_id, after_json)
		VALUES ($1, $2, $3, $4, $5)
	`, actor, "credential.clear_manual_disabled", "credential", req.CredentialID, detailsJSON)

	// Invalidate cache
	monitorSummaryCache.mu.Lock()
	monitorSummaryCache.entries = nil
	monitorSummaryCache.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": fmt.Sprintf("manual_disabled cleared for credential %d", req.CredentialID),
	})
}
