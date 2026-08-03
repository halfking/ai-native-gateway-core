// Package routeincident — types.go
//
// DTOs that flow between the database, the SSE hub, and the dashboard.
// Every field that could carry sensitive material MUST go through
// redact.go before it leaves the package boundary.
package routeincident

import (
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/events"
)

// Terminal status strings, mirroring request_logs.request_status.
const (
	TerminalSuccess = "success"
	TerminalFailure = "failure"
)

// RouteKey uniquely identifies an operational route. It mirrors the
// five pieces in the spec, with provider/credential optional because
// the upstream may not have been resolved when the request failed.
//
// tenant_id is the LLM-Gateway tenant id (matches request_logs.tenant_id).
// protocol is the egress protocol identifier (e.g. "openai_chat_completions",
// "anthropic_messages"); empty when the request never reached routing.
// model is the canonical_name when known, else outbound_model, else
// client_model — the most stable name available for the route.
type RouteKey struct {
	TenantID     string `json:"tenant_id"`
	Protocol     string `json:"endpoint_protocol"`
	Model        string `json:"model"`
	ProviderID   *int64 `json:"provider_id,omitempty"`
	CredentialID *int64 `json:"credential_id,omitempty"`
}

// IsZero reports whether the key is missing required fields. The
// dashboard's "Other" aggregate lane is identified by an empty model
// (or model == "__other__") and is NEVER allowed to open a diagnosis
// drawer.
func (k RouteKey) IsZero() bool { return k.TenantID == "" || k.Model == "" }

// Incident is the public projection of route_incidents. It is the
// shape returned by the read-only APIs and the body of the SSE
// `incident_update` envelope. Fields are explicit to keep the JSON
// contract stable for the frontend.
type Incident struct {
	ID               string     `json:"id"`
	RouteKey         RouteKey   `json:"route_key"`
	State            State      `json:"state"`
	FailureStreak    int        `json:"failure_streak"`
	RecoveryStreak   int        `json:"recovery_streak"`
	FirstFailureAt   time.Time  `json:"first_failure_at"`
	LastFailureAt    *time.Time `json:"last_failure_at,omitempty"`
	LastSuccessAt    *time.Time `json:"last_success_at,omitempty"`
	RecoveredAt      *time.Time `json:"recovered_at,omitempty"`
	TotalFailures    int64      `json:"total_failures"`
	TotalSuccesses   int64      `json:"total_successes"`
	LastErrorKind    *string    `json:"last_error_kind,omitempty"`
	LastFailureStage *string    `json:"last_failure_stage,omitempty"`
	Version          int64      `json:"version"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

// IncidentEvent is the public projection of one route_incident_events
// row. Only sanitized evidence is included (see redact.go).
type IncidentEvent struct {
	ID             int64          `json:"id"`
	IncidentID     string         `json:"incident_id"`
	EventType      EventType      `json:"event_type"`
	RequestID      *string        `json:"request_id,omitempty"`
	TerminalStatus *string        `json:"terminal_status,omitempty"`
	FailureKind    *string        `json:"failure_kind,omitempty"`
	FailureStage   *string        `json:"failure_stage,omitempty"`
	FailureStreak  *int           `json:"failure_streak,omitempty"`
	RecoveryStreak *int           `json:"recovery_streak,omitempty"`
	Evidence       map[string]any `json:"evidence,omitempty"`
	Actor          *string        `json:"actor,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
}

// IncidentTimelinePoint is one 5-minute bucket for the drawer's
// "Last 24 hours" section.
type IncidentTimelinePoint struct {
	BucketStart  time.Time `json:"bucket_start"`
	Requests     int64     `json:"requests"`
	Errors       int64     `json:"errors"`
	AvgLatencyMs *float64  `json:"avg_latency_ms,omitempty"`
	P99LatencyMs *float64  `json:"p99_latency_ms,omitempty"`
	Recoveries   int64     `json:"recoveries"`
}

// IncidentFinding is an evidence-backed commonality in the drawer's
// "Evidence summary" section. The spec requires every finding to
// carry its sample count, interval, and the request IDs that backed it.
type IncidentFinding struct {
	Kind        string   `json:"kind"`  // e.g. "error_kind_rate", "latency_drift"
	Label       string   `json:"label"` // human-readable summary
	SampleCount int      `json:"sample_count"`
	IntervalSec int      `json:"interval_sec"` // 0 when not applicable
	RequestIDs  []string `json:"request_ids,omitempty"`
	Note        string   `json:"note,omitempty"`
}

// IncidentDetail aggregates the projection above with the secondary
// sections the drawer needs in one round-trip.
type IncidentDetail struct {
	Incident         Incident                `json:"incident"`
	CurrentRoute     RouteSnapshot           `json:"current_route"`
	Findings         []IncidentFinding       `json:"findings"`
	SampleRequests   []string                `json:"sample_request_ids,omitempty"`
	RecoveryProgress RecoveryProgress        `json:"recovery_progress"`
	Timeline         []IncidentTimelinePoint `json:"timeline"`
	ResourceSnapshot ResourceSnapshot        `json:"resource_snapshot"`
	InsufficientData []string                `json:"insufficient_data,omitempty"`
}

// RecoveryProgress surfaces 1/5, 2/5, … to the dashboard so the swim
// lane can render "Recovery n/5" inline.
type RecoveryProgress struct {
	Current int `json:"current"`
	Target  int `json:"target"`
}

// RouteSnapshot describes the current route — what the gateway is
// sending traffic to, with the protocol, model mapping, provider,
// credential identifier (NOT the secret), and routing decision. No
// credential value, no request body, no auth header.
type RouteSnapshot struct {
	Protocol        string `json:"protocol"`
	CanonicalModel  string `json:"canonical_model"`
	OutboundModel   string `json:"outbound_model"`
	ProviderCode    string `json:"provider_code"`
	ProviderID      *int64 `json:"provider_id,omitempty"`
	CredentialID    *int64 `json:"credential_id,omitempty"`
	CredentialLabel string `json:"credential_label,omitempty"` // public label only
	RoutingDecision string `json:"routing_decision"`
	RetryPath       string `json:"retry_path,omitempty"`
	// Decision 是结构化的路由决策解释（GW-00 canonical metadata）。
	// 与上面的 RoutingDecision 字符串字段互补：字符串字段保留向后兼容，
	// Decision 是可向外发的低敏结构化解释。nil 时 omitempty 不输出。
	Decision *events.RoutingDecision `json:"decision,omitempty"`
}

// ResourceSnapshot mirrors the drawer's "Resource snapshot" section
// (slot and concurrency state, circuit/quota/availability, action
// history). Phase 1 exposes only read-only fields.
type ResourceSnapshot struct {
	SlotsInUse         *int64  `json:"slots_in_use,omitempty"`
	SlotsTotal         *int64  `json:"slots_total,omitempty"`
	LiveConcurrent     *int64  `json:"live_concurrent,omitempty"`
	CircuitState       *string `json:"circuit_state,omitempty"`
	QuotaRemaining     *int64  `json:"quota_remaining,omitempty"`
	AvailabilityState  *string `json:"availability_state,omitempty"`
	ActionHistoryCount int     `json:"action_history_count"` // 0 in phase 1
}

// AffectedLane is one of the dimensions the dashboard groups by
// (provider, vendor/model family) and the value that owns the
// incident — i.e. the dimension lane on which the diagnostic badge
// should appear. The frontend uses this to drive the SSE merge
// without needing the full route key on the wire.
type AffectedLane struct {
	Dimension string `json:"dimension"` // "provider" | "model" | "vendor"
	Value     string `json:"value"`
}

// IncidentUpdate is the public envelope body for the SSE
// `incident_update` event. Only display identity + sanitized status.
// No tenant id, no credential id, no full body.
type IncidentUpdate struct {
	Type           string          `json:"type"`
	IncidentID     string          `json:"incident_id"`
	State          State           `json:"state"`
	FailureStreak  int             `json:"failure_streak"`
	RecoveryStreak int             `json:"recovery_streak"`
	Visible        bool            `json:"visible"`
	AffectedLanes  []AffectedLane  `json:"affected_lanes,omitempty"`
	LastError      *SanitizedError `json:"last_error,omitempty"`
	RouteKey       RouteKey        `json:"route_key"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

// SanitizedError is a small, redacted view of the last failure that
// is safe to publish over SSE. Only kind + stage are exposed.
type SanitizedError struct {
	Kind  string `json:"kind"`
	Stage string `json:"stage,omitempty"`
}
