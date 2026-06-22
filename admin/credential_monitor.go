package admin

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/kaixuan/llm-gateway-go/credentialhealth"
	"github.com/redis/go-redis/v9"
)

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
	ID                    int          `json:"id"`
	ProviderID            int          `json:"provider_id"`
	ProviderName          string       `json:"provider_name"`
	Label                 string       `json:"label"`
	Status                string       `json:"status"`
	AvailabilityState     string       `json:"availability_state"`
	HealthStatus          string       `json:"health_status"`
	QuotaState            string       `json:"quota_state"`
	ConcurrencyLimit      *int         `json:"concurrency_limit"`
	ConcurrencyLimitAuto  *int         `json:"concurrency_limit_auto"`
	EffectiveConcurrency  int          `json:"effective_concurrency"`
	ManualDisabled        bool         `json:"manual_disabled"`
	ConsecutiveFailures   int          `json:"consecutive_failures"`
	AvailabilityRecoverAt *string      `json:"availability_recover_at"`
	StateReasonCode       *string      `json:"state_reason_code"`
	StateReasonDetail     *string      `json:"state_reason_detail"`
	HealthCheckedAt       *string      `json:"health_checked_at"`
	TotalRequests         int64        `json:"total_requests"`
	RecentWindowStats     *WindowStats `json:"recent_window_stats,omitempty"`
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
// GET /api/credentials/monitor-summary?provider_id=X&include_window_stats=true
func (m *CredentialMonitorHandlers) handleMonitorSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	providerID := queryInt(r, "provider_id", 0)
	includeWindowStats := queryBool(r, "include_window_stats")

	// Lazy init recorder if redis is available
	if m.recorder == nil && m.redisClient != nil {
		m.recorder = credentialhealth.NewRecorder(m.redisClient, 2*time.Hour, 100)
	}

	query := `
		SELECT c.id, c.provider_id, COALESCE(p.display_name, p.catalog_code, '') AS provider_name,
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
		       COALESCE(
		           (SELECT COUNT(*) FROM request_logs rl WHERE rl.credential_id = c.id),
		           0
		       ) AS total_requests
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
		if err := rows.Scan(
			&s.ID, &s.ProviderID, &s.ProviderName, &s.Label,
			&s.Status, &s.AvailabilityState, &s.HealthStatus, &s.QuotaState,
			&s.ConcurrencyLimit, &s.ConcurrencyLimitAuto, &s.EffectiveConcurrency,
			&s.ManualDisabled, &s.ConsecutiveFailures,
			&recoverAt, &s.StateReasonCode, &s.StateReasonDetail,
			&checkedAt, &s.TotalRequests,
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

		// Optionally fetch recent window stats
		if includeWindowStats && m.recorder != nil && m.recorder.Enabled() {
			// Get most common model for this credential
			model := m.getMostCommonModel(ctx, s.ID)
			if model != "" {
				since := time.Now().Add(-1 * time.Hour)
				entries, _ := m.recorder.GetRecent(ctx, s.ID, model, since)
				if len(entries) > 0 {
					stats := credentialhealth.ComputeStats(entries)
					s.RecentWindowStats = &WindowStats{
						Total:       stats.Total,
						Success:     stats.Success,
						Failed:      stats.Failed,
						FailureRate: stats.FailureRate,
						ErrorKinds:  stats.ErrorKinds,
						SampleModel: model,
					}
				}
			}
		}

		summaries = append(summaries, s)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"credentials": summaries,
		"count":       len(summaries),
	})
}

// getMostCommonModel returns the most frequently used model for a credential (best-effort).
func (m *CredentialMonitorHandlers) getMostCommonModel(ctx context.Context, credentialID int) string {
	var model string
	_ = m.h.db.QueryRow(ctx, `
		SELECT raw_model
		FROM credential_model_call_history
		WHERE credential_id = $1
		  AND window_start > NOW() - INTERVAL '1 hour'
		GROUP BY raw_model
		ORDER BY SUM(total_calls) DESC
		LIMIT 1
	`, credentialID).Scan(&model)
	return model
}

// handleSlidingWindow returns raw sliding window data for a credential.
// GET /api/credentials/sliding-window?credential_id=X&model=Y&minutes=60&limit=50
func (m *CredentialMonitorHandlers) handleSlidingWindow(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Lazy init recorder if redis is available
	if m.recorder == nil && m.redisClient != nil {
		m.recorder = credentialhealth.NewRecorder(m.redisClient, 2*time.Hour, 100)
	}

	if m.recorder == nil || !m.recorder.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "recorder unavailable")
		return
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

	since := time.Now().Add(-time.Duration(minutes) * time.Minute)
	entries, err := m.recorder.GetRecent(ctx, credentialID, model, since)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to get window: %v", err))
		return
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
