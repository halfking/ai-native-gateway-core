// Package admin — probe_dashboard.go
//
// Model Health Dashboard APIs for unified probe scheduler monitoring.
//
// Provides real-time visibility into:
//   - Model state distribution across all credential nodes
//   - Probe queue priorities and sizes
//   - System health metrics
//
// Spec: 2026-06-28-model-health-dashboard
package admin

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// ── Data Models ─────────────────────────────────────────────────────────

// ModelHealthSummary represents the health status of a model across all credentials
type ModelHealthSummary struct {
	ProviderModelID int64  `json:"provider_model_id"`
	RawModelName    string `json:"raw_model_name"`
	OutboundModel   string `json:"outbound_model_name"`
	Protocol        string `json:"protocol"`
	ProviderName    string `json:"provider_name"`

	// State distribution
	TotalCredentials  int `json:"total_credentials"`
	HealthyCount      int `json:"healthy_count"`
	SuspiciousCount   int `json:"suspicious_count"`
	FailingCount      int `json:"failing_count"`
	ProbingCount      int `json:"probing_count"`
	HealthyPercentage float64 `json:"healthy_percentage"`
	FailingPercentage float64 `json:"failing_percentage"`

	// Priority distribution
	UrgentCount             int `json:"urgent_count"`
	SuspiciousPriorityCount int `json:"suspicious_priority_count"`
	FailingPriorityCount    int `json:"failing_priority_count"`
	WatchdogCount           int `json:"watchdog_count"`

	// Health metrics
	AvgSuccessRate7d         float64 `json:"avg_success_rate_7d"`
	AvgVerificationHours     float64 `json:"avg_verification_hours"`
	AvgConsecutiveSuccesses  float64 `json:"avg_consecutive_successes"`

	// Real request stats (24h)
	TotalRealSuccess24h   int     `json:"total_real_success_24h"`
	TotalRealFailure24h   int     `json:"total_real_failure_24h"`
	RealSuccessRate24h    *float64 `json:"real_success_rate_24h,omitempty"`

	// Timestamps
	LastVerifiedAt    *time.Time `json:"last_verified_at,omitempty"`
	LastRealRequestAt *time.Time `json:"last_real_request_at,omitempty"`
	NextProbeAt       *time.Time `json:"next_probe_at,omitempty"`

	// Alerts
	CriticalNodes     int    `json:"critical_nodes"`
	PendingProbes5min int    `json:"pending_probes_5min"`
	OverallHealth     string `json:"overall_health"` // critical, warning, degraded, healthy, unknown
}

// ProbeQueueSnapshot represents the current state of probe queues
type ProbeQueueSnapshot struct {
	ProbePriority   string     `json:"probe_priority"`
	State           string     `json:"state"`
	QueueSize       int        `json:"queue_size"`
	ReadyNow        int        `json:"ready_now"`
	Ready1min       int        `json:"ready_1min"`
	Ready5min       int        `json:"ready_5min"`
	EarliestRetryAt *time.Time `json:"earliest_retry_at,omitempty"`
	LatestRetryAt   *time.Time `json:"latest_retry_at,omitempty"`
	AvgWaitSeconds  *float64   `json:"avg_wait_seconds,omitempty"`
	MaxWaitSeconds  *float64   `json:"max_wait_seconds,omitempty"`
}

// ProbeSystemHealth represents overall system health metrics
type ProbeSystemHealth struct {
	// Overall stats
	TotalNodes      int `json:"total_nodes"`
	HealthyNodes    int `json:"healthy_nodes"`
	FailingNodes    int `json:"failing_nodes"`
	SuspiciousNodes int `json:"suspicious_nodes"`
	ProbingNodes    int `json:"probing_nodes"`

	// Queue sizes
	UrgentQueueSize     int `json:"urgent_queue_size"`
	SuspiciousQueueSize int `json:"suspicious_queue_size"`
	FailingQueueSize    int `json:"failing_queue_size"`
	WatchdogQueueSize   int `json:"watchdog_queue_size"`

	// Active probes
	ReadyProbes            int `json:"ready_probes"`
	CurrentProbing         int `json:"current_probing"`
	CredentialsBeingProbed int `json:"credentials_being_probed"`

	// Health metrics
	AvgSuccessRate7d *float64 `json:"avg_success_rate_7d,omitempty"`

	// Recent activity
	LastProbeAt       *time.Time `json:"last_probe_at,omitempty"`
	LastRealRequestAt *time.Time `json:"last_real_request_at,omitempty"`

	// 24h request stats
	TotalRealSuccess24h int `json:"total_real_success_24h"`
	TotalRealFailure24h int `json:"total_real_failure_24h"`

	// Alerts
	CriticalNodes      int `json:"critical_nodes"`
	PendingProbes5min  int `json:"pending_probes_5min"`

	SnapshotAt time.Time `json:"snapshot_at"`
}

// ModelNodeDetail represents detailed info for a single credential×model node
type ModelNodeDetail struct {
	RawModelName    string `json:"raw_model_name"`
	OutboundModel   string `json:"outbound_model_name"`
	ProbePriority   string `json:"probe_priority"`
	State           string `json:"state"`

	CredentialID    int64  `json:"credential_id"`
	CredentialLabel string `json:"credential_label"`
	ProviderName    string `json:"provider_name"`

	// Status
	LastVerifiedAt      *time.Time `json:"last_verified_at,omitempty"`
	NextRetryAt         *time.Time `json:"next_retry_at,omitempty"`
	MarkedSuspiciousAt  *time.Time `json:"marked_suspicious_at,omitempty"`
	ProbingStartedAt    *time.Time `json:"probing_started_at,omitempty"`

	// Stats
	ConsecutiveSuccesses        int      `json:"consecutive_successes"`
	ConsecutiveFailures         int      `json:"consecutive_failures"`
	ConsecutiveWatchdogSuccesses int     `json:"consecutive_watchdog_successes"`
	SuccessRate7d               *float64 `json:"success_rate_7d,omitempty"`
	VerificationInterval        string   `json:"verification_interval"`

	// Real requests (24h)
	RealSuccess24h      int        `json:"real_success_24h"`
	RealFailure24h      int        `json:"real_failure_24h"`
	LastRealRequestAt   *time.Time `json:"last_real_request_at,omitempty"`

	// Error info
	LastUnavailableReason *string `json:"last_unavailable_reason,omitempty"`
	LastErrCode           *string `json:"last_err_code,omitempty"`

	// Derived
	RetryIn              string  `json:"retry_in"`
	StateDurationMinutes float64 `json:"state_duration_minutes"`
}

// ModelStateBreakdown is returned by get_model_state_summary()
type ModelStateBreakdown struct {
	State              string   `json:"state"`
	Priority           string   `json:"priority"`
	Count              int      `json:"count"`
	AvgSuccessRate     *float64 `json:"avg_success_rate,omitempty"`
	NextProbeInSeconds *int     `json:"next_probe_in_seconds,omitempty"`
}

// ── API Handlers ────────────────────────────────────────────────────────

// GET /api/admin/probe/dashboard
// Returns model health summary for all models or filtered by model name
func (h *Handler) handleProbeDashboard(w http.ResponseWriter, r *http.Request) {
	modelFilter := r.URL.Query().Get("model") // optional filter

	query := `SELECT * FROM v_model_health_dashboard`
	args := []any{}

	if modelFilter != "" {
		query += ` WHERE raw_model_name ILIKE $1 OR outbound_model_name ILIKE $1`
		args = append(args, "%"+modelFilter+"%")
	}

	rows, err := h.db.Query(r.Context(), query, args...)
	if err != nil {
		http.Error(w, "database query failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var models []ModelHealthSummary
	for rows.Next() {
		var m ModelHealthSummary
		err := rows.Scan(
			&m.ProviderModelID,
			&m.RawModelName,
			&m.OutboundModel,
			&m.Protocol,
			&m.ProviderName,
			&m.TotalCredentials,
			&m.HealthyCount,
			&m.SuspiciousCount,
			&m.FailingCount,
			&m.ProbingCount,
			&m.HealthyPercentage,
			&m.FailingPercentage,
			&m.UrgentCount,
			&m.SuspiciousPriorityCount,
			&m.FailingPriorityCount,
			&m.WatchdogCount,
			&m.AvgSuccessRate7d,
			&m.AvgVerificationHours,
			&m.AvgConsecutiveSuccesses,
			&m.TotalRealSuccess24h,
			&m.TotalRealFailure24h,
			&m.RealSuccessRate24h,
			&m.LastVerifiedAt,
			&m.LastRealRequestAt,
			&m.NextProbeAt,
			&m.CriticalNodes,
			&m.PendingProbes5min,
			&m.OverallHealth,
		)
		if err != nil {
			http.Error(w, "scan failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		models = append(models, m)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"models": models,
		"total":  len(models),
	})
}

// GET /api/admin/probe/queue-snapshot
// Returns current state of all probe queues
func (h *Handler) handleProbeQueueSnapshot(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.Query(r.Context(), `SELECT * FROM v_probe_queue_snapshot`)
	if err != nil {
		http.Error(w, "database query failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var queues []ProbeQueueSnapshot
	for rows.Next() {
		var q ProbeQueueSnapshot
		err := rows.Scan(
			&q.ProbePriority,
			&q.State,
			&q.QueueSize,
			&q.ReadyNow,
			&q.Ready1min,
			&q.Ready5min,
			&q.EarliestRetryAt,
			&q.LatestRetryAt,
			&q.AvgWaitSeconds,
			&q.MaxWaitSeconds,
		)
		if err != nil {
			http.Error(w, "scan failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		queues = append(queues, q)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"queues": queues,
		"total":  len(queues),
	})
}

// GET /api/admin/probe/system-health
// Returns overall system health metrics
func (h *Handler) handleProbeSystemHealth(w http.ResponseWriter, r *http.Request) {
	var health ProbeSystemHealth
	err := h.db.QueryRow(r.Context(), `SELECT * FROM v_probe_system_health`).Scan(
		&health.TotalNodes,
		&health.HealthyNodes,
		&health.FailingNodes,
		&health.SuspiciousNodes,
		&health.ProbingNodes,
		&health.UrgentQueueSize,
		&health.SuspiciousQueueSize,
		&health.FailingQueueSize,
		&health.WatchdogQueueSize,
		&health.ReadyProbes,
		&health.CurrentProbing,
		&health.CredentialsBeingProbed,
		&health.AvgSuccessRate7d,
		&health.LastProbeAt,
		&health.LastRealRequestAt,
		&health.TotalRealSuccess24h,
		&health.TotalRealFailure24h,
		&health.CriticalNodes,
		&health.PendingProbes5min,
		&health.SnapshotAt,
	)
	if err != nil {
		http.Error(w, "database query failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(health)
}

// GET /api/admin/probe/model/{model}/nodes
// Returns detailed node information for a specific model
func (h *Handler) handleProbeModelNodes(w http.ResponseWriter, r *http.Request) {
	// Extract model name from path: /api/admin/probe/model/{model}/nodes
	path := strings.TrimPrefix(r.URL.Path, "/api/admin/probe/model/")
	parts := strings.Split(path, "/")
	if len(parts) < 2 || parts[1] != "nodes" {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	modelName := parts[0]

	rows, err := h.db.Query(r.Context(), `
		SELECT * FROM v_model_priority_details
		WHERE raw_model_name = $1
	`, modelName)
	if err != nil {
		http.Error(w, "database query failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var nodes []ModelNodeDetail
	for rows.Next() {
		var n ModelNodeDetail
		err := rows.Scan(
			&n.RawModelName,
			&n.OutboundModel,
			&n.ProbePriority,
			&n.State,
			&n.CredentialID,
			&n.CredentialLabel,
			&n.ProviderName,
			&n.LastVerifiedAt,
			&n.NextRetryAt,
			&n.MarkedSuspiciousAt,
			&n.ProbingStartedAt,
			&n.ConsecutiveSuccesses,
			&n.ConsecutiveFailures,
			&n.ConsecutiveWatchdogSuccesses,
			&n.SuccessRate7d,
			&n.VerificationInterval,
			&n.RealSuccess24h,
			&n.RealFailure24h,
			&n.LastRealRequestAt,
			&n.LastUnavailableReason,
			&n.LastErrCode,
			&n.RetryIn,
			&n.StateDurationMinutes,
		)
		if err != nil {
			http.Error(w, "scan failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		nodes = append(nodes, n)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"model": modelName,
		"nodes": nodes,
		"total": len(nodes),
	})
}

// GET /api/admin/probe/model/{model}/state-summary
// Returns state distribution summary for a specific model
func (h *Handler) handleProbeModelStateSummary(w http.ResponseWriter, r *http.Request) {
	// Extract model name from path
	path := strings.TrimPrefix(r.URL.Path, "/api/admin/probe/model/")
	parts := strings.Split(path, "/")
	if len(parts) < 2 || parts[1] != "state-summary" {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	modelName := parts[0]

	rows, err := h.db.Query(r.Context(), `SELECT * FROM get_model_state_summary($1)`, modelName)
	if err != nil {
		http.Error(w, "database query failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var breakdown []ModelStateBreakdown
	for rows.Next() {
		var b ModelStateBreakdown
		err := rows.Scan(
			&b.State,
			&b.Priority,
			&b.Count,
			&b.AvgSuccessRate,
			&b.NextProbeInSeconds,
		)
		if err != nil {
			http.Error(w, "scan failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		breakdown = append(breakdown, b)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"model":     modelName,
		"breakdown": breakdown,
	})
}

// GET /api/admin/probe/availability-timeline
// Returns 24h availability timeline for models
func (h *Handler) handleProbeAvailabilityTimeline(w http.ResponseWriter, r *http.Request) {
	modelFilter := r.URL.Query().Get("model")

	query := `SELECT * FROM v_model_availability_timeline`
	args := []any{}

	if modelFilter != "" {
		query += ` WHERE raw_model_name = $1`
		args = append(args, modelFilter)
	}

	query += ` ORDER BY raw_model_name, hour_bucket DESC LIMIT 500`

	rows, err := h.db.Query(r.Context(), query, args...)
	if err != nil {
		http.Error(w, "database query failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type TimelinePoint struct {
		RawModelName         string     `json:"raw_model_name"`
		OutboundModel        string     `json:"outbound_model_name"`
		HourBucket           time.Time  `json:"hour_bucket"`
		TotalProbes          int        `json:"total_probes"`
		SuccessfulProbes     int        `json:"successful_probes"`
		FailedProbes         int        `json:"failed_probes"`
		SuccessRate          float64    `json:"success_rate"`
		AvgLatencyMs         *float64   `json:"avg_latency_ms,omitempty"`
		ProbedCredentials    int        `json:"probed_credentials"`
		SuccessfulCredentials int       `json:"successful_credentials"`
		FailedCredentials    int        `json:"failed_credentials"`
	}

	var timeline []TimelinePoint
	for rows.Next() {
		var p TimelinePoint
		err := rows.Scan(
			&p.RawModelName,
			&p.OutboundModel,
			&p.HourBucket,
			&p.TotalProbes,
			&p.SuccessfulProbes,
			&p.FailedProbes,
			&p.SuccessRate,
			&p.AvgLatencyMs,
			&p.ProbedCredentials,
			&p.SuccessfulCredentials,
			&p.FailedCredentials,
		)
		if err != nil {
			http.Error(w, "scan failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		timeline = append(timeline, p)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"timeline": timeline,
		"total":    len(timeline),
	})
}

// ── Register Routes ─────────────────────────────────────────────────────

// RegisterProbeDashboardRoutes registers probe dashboard API routes
// Called from cmd/gateway/main.go during admin API setup
func (h *Handler) RegisterProbeDashboardRoutes(mux *http.ServeMux, adminWrap func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("/api/admin/probe/dashboard", adminWrap(h.handleProbeDashboard))
	mux.HandleFunc("/api/admin/probe/queue-snapshot", adminWrap(h.handleProbeQueueSnapshot))
	mux.HandleFunc("/api/admin/probe/system-health", adminWrap(h.handleProbeSystemHealth))
	mux.HandleFunc("/api/admin/probe/model/", adminWrap(h.handleProbeModelRoutes))
	mux.HandleFunc("/api/admin/probe/availability-timeline", adminWrap(h.handleProbeAvailabilityTimeline))
}

// handleProbeModelRoutes is a router for /api/admin/probe/model/* endpoints
func (h *Handler) handleProbeModelRoutes(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if strings.HasSuffix(path, "/nodes") {
		h.handleProbeModelNodes(w, r)
	} else if strings.HasSuffix(path, "/state-summary") {
		h.handleProbeModelStateSummary(w, r)
	} else {
		http.Error(w, "not found", http.StatusNotFound)
	}
}
