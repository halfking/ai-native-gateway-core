// Package api holds shared types and constants for URSM v2.
package api

import "time"

type RolloutMode string

const (
	ModeOff           RolloutMode = "off"
	ModeShadow        RolloutMode = "shadow"
	ModeCanary        RolloutMode = "canary"
	ModeAuthoritative RolloutMode = "authoritative"
)

type Scope int

const (
	ScopeProvider Scope = iota
	ScopeCredential
	ScopeBinding
	ScopeNode
	ScopeResource
)

// Source-priority ordering is the override hierarchy used by the
// reducer CAS: lower-priority writes cannot override higher-priority ones.
//
//	Seed    = static config; baseline
//	Request = live traffic; transient signals
//	Probe   = dedicated probe worker; durable health evidence
//	Recover = warmup / recovery boot
//	Admin   = manual operator action; never overridden by lower priorities
const (
	SourcePrioritySeed    = 0
	SourcePriorityRequest = 10
	SourcePriorityProbe   = 20
	SourcePriorityRecover = 30
	SourcePriorityAdmin   = 40
)

// Rich node health status vocabulary for the NodeView.HealthStatus bridge
// (P1-5 / T5-lite, FR-4 R4.1). These are NEUTRAL constants: this package
// deliberately does not import domains/nodehealth or domains/requestjourney
// to keep the URSM store contract dependency-free. The string values are
// identical to requestjourney's NodeHealthStatus vocabulary
// (requestjourney/contract.go:218-229, consumed by nodehealth.OutcomeReducer):
//
//	Healthy     ↔ requestjourney.NodeHealthHealthy
//	Suspect     ↔ requestjourney.NodeHealthSuspect
//	Degraded    ↔ requestjourney.NodeHealthDegraded
//	Quarantined ↔ requestjourney.NodeHealthQuarantined
//	Recovering  ↔ requestjourney.NodeHealthRecovering
//
// BOUNDARY: HealthStatus is display/observability only. It never
// participates in routing eligibility — routing eligibility is decided
// exclusively by the availability fields (available/disabled/cool_until_ms)
// adjudicated in record_request.lua / apply_probe.lua.
const (
	HealthStatusHealthy     = "healthy"
	HealthStatusSuspect     = "suspect"
	HealthStatusDegraded    = "degraded"
	HealthStatusQuarantined = "quarantined"
	HealthStatusRecovering  = "recovering"
)

// IsValidHealthStatus reports whether s is one of the bridge vocabulary
// values. record_request.lua refuses to overwrite an existing health field
// with anything outside this set (empty is treated as "not supplied").
func IsValidHealthStatus(s string) bool {
	switch s {
	case HealthStatusHealthy, HealthStatusSuspect, HealthStatusDegraded,
		HealthStatusQuarantined, HealthStatusRecovering:
		return true
	}
	return false
}

type NodeView struct {
	ProviderID           int       `json:"provider_id"`
	CredentialID         int       `json:"credential_id"`
	RawModel             string    `json:"raw_model"`
	CanonicalName        string    `json:"canonical_name"`
	TenantID             string    `json:"tenant_id"`
	Available            bool      `json:"available"`
	Reason               string    `json:"reason,omitempty"`
	HealthStatus         string    `json:"health_status,omitempty"`
	FailStreak           int       `json:"fail_streak"`
	CoolUntil            time.Time `json:"cool_until,omitempty"`
	SR1m                 float64   `json:"sr_1m"`
	SR5m                 float64   `json:"sr_5m"`
	SR30m                float64   `json:"sr_30m"`
	Samples1m            int       `json:"samples_1m"`
	Samples5m            int       `json:"samples_5m"`
	Samples30m           int       `json:"samples_30m"`
	EmptyResponses1m     int       `json:"empty_responses_1m"`
	EmptyResponses5m     int       `json:"empty_responses_5m"`
	EmptyResponses30m    int       `json:"empty_responses_30m"`
	EmptyResponseRate1m  float64   `json:"empty_response_rate_1m"`
	EmptyResponseRate5m  float64   `json:"empty_response_rate_5m"`
	EmptyResponseRate30m float64   `json:"empty_response_rate_30m"`
	LatP50Ms             int       `json:"lat_p50_ms"`
	LatP95Ms             int       `json:"lat_p95_ms"`
	LatEWMA              int       `json:"lat_ewma_ms"`
	Score                float64   `json:"score"`
	PriceIn              float64   `json:"price_in_per_1m"`
	PriceOut             float64   `json:"price_out_per_1m"`
	BillingMode          string    `json:"billing_mode"`
	Trust                float64   `json:"trust"`
	BaseURLMs            int       `json:"baseurl_latency_ms"`
	ConcUsed             int       `json:"conc_used"`
	ConcLimit            int       `json:"conc_limit"`
	FPUsed               int       `json:"fp_used"`
	FPLimit              int       `json:"fp_limit"`
	RPMUsed              int       `json:"rpm_used"`
	RPMLimit             int       `json:"rpm_limit"`
	Generation           int64     `json:"generation"`
	SrcPriority          int       `json:"source_priority"`
	UpdatedAt            time.Time `json:"updated_at"`
}

type RequestOutcome struct {
	CredentialID int
	RawModel     string
	Canonical    string
	TenantID     string
	ProviderID   int
	Success      bool
	LatencyMs    int
	ErrorKind    string
	RequestID    string
	// DedupKey identifies one outcome event. Empty preserves the legacy
	// request-level dedup contract; dispatch supplies an attempt-qualified key so
	// intermediate failures cannot suppress a later terminal success.
	DedupKey string
	// Terminal marks the request's final recordable dispatch outcome. The manager
	// assigns it a distinct dedup namespace from intermediate attempt outcomes.
	Terminal    bool
	BillingMode string
	// HealthStatus is the optional rich node-health enum (see the
	// HealthStatus* constants). Supplied by the executor from
	// nodehealth.OutcomeReducer via the EffectUpdateURSM channel; empty
	// keeps the previously persisted value in Redis. Display-only — it
	// never feeds routing eligibility (see the boundary note on the
	// HealthStatus* constants).
	HealthStatus string
	// AdminHold indicates whether a manual admin hold is currently set on
	// the (credential, raw_model) target. When true, the request outcome
	// MUST NOT mutate availability — admin priority dominates.
	AdminHold bool
}

type ProbeOutcome struct {
	CredentialID int
	RawModel     string
	Success      bool
	LatencyMs    int
}

type AdminAction struct {
	Scope          Scope
	ProviderID     int
	CredentialID   int
	RawModel       string
	TenantID       string
	ManualDisabled *bool
	Reason         string
	Actor          string
	IssuedAtMs     int64
}

type CandidateQuery struct {
	TenantID  string
	Canonical string
	Profile   string
	Modality  string
	MaxNodes  int
}

type Progress struct {
	Loaded         int
	Total          int
	LastCheckpoint string
}
