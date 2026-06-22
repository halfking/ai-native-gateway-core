package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

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
}

// CredentialMonitorSummary represents a credential's monitoring state.
type CredentialMonitorSummary struct {
	ID                    int                   `json:"id"`
	ProviderID            int                   `json:"provider_id"`
	ProviderName          string                `json:"provider_name"`
	Label                 string                `json:"label"`
	Status                string                `json:"status"`
	AvailabilityState     string                `json:"availability_state"`
	HealthStatus          string                `json:"health_status"`
	QuotaState            string                `json:"quota_state"`
	ConcurrencyLimit      *int                  `json:"concurrency_limit"`
	ConcurrencyLimitAuto  *int                  `json:"concurrency_limit_auto"`
	EffectiveConcurrency  int                   `json:"effective_concurrency"`
	ManualDisabled        bool                  `json:"manual_disabled"`
	ConsecutiveFailures   int                   `json:"consecutive_failures"`
	AvailabilityRecoverAt *string               `json:"availability_recover_at"`
	StateReasonCode       *string               `json:"state_reason_code"`
	StateReasonDetail     *string               `json:"state_reason_detail"`
	HealthCheckedAt       *string               `json:"health_checked_at"`
	TotalRequests         int64                 `json:"total_requests"`
	// Models is the per-(credential, model) availability breakdown. Replaces the
	// single-model recent_window_stats field (2026-06-22). Each entry carries
	// the probe state, offer/binding availability and the live last-N success
	// rate so the monitor page can show model-level health instead of one
	// aggregate. nil when the credential has no model_offers rows.
	Models                []CredentialModelStatus `json:"models,omitempty"`
	// AggregatedSuccessRate is the min recent success rate across the
	// credential's models (conservative — a credential is only as healthy as
	// its worst routable model). nil when there are no samples.
	AggregatedSuccessRate *float64                `json:"aggregated_success_rate,omitempty"`
}

// CredentialModelStatus is the per-(credential, model) availability row used
// by the credential monitor drawer. Mirrors the columns the routing candidate
// loader (loadCandidatesDB) consults so operators see the same view the router
// uses to admit/exclude a binding.
type CredentialModelStatus struct {
	RawModelName       string   `json:"raw_model_name"`
	OfferAvailable     bool     `json:"offer_available"`
	OfferUnavailableReason *string `json:"offer_unavailable_reason,omitempty"`
	BindingAvailable   bool     `json:"binding_available"`
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
					ORDER BY mo.raw_model_name
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
		var models []CredentialModelStatus
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
