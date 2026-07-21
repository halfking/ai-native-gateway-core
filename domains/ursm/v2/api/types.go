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

type NodeView struct {
	ProviderID    int       `json:"provider_id"`
	CredentialID  int       `json:"credential_id"`
	RawModel      string    `json:"raw_model"`
	CanonicalName string    `json:"canonical_name"`
	TenantID      string    `json:"tenant_id"`
	Available     bool      `json:"available"`
	Reason        string    `json:"reason,omitempty"`
	HealthStatus  string    `json:"health_status,omitempty"`
	FailStreak    int       `json:"fail_streak"`
	CoolUntil     time.Time `json:"cool_until,omitempty"`
	SR1m          float64   `json:"sr_1m"`
	SR5m          float64   `json:"sr_5m"`
	SR30m         float64   `json:"sr_30m"`
	Samples1m     int       `json:"samples_1m"`
	Samples5m     int       `json:"samples_5m"`
	Samples30m    int       `json:"samples_30m"`
	LatP50Ms      int       `json:"lat_p50_ms"`
	LatP95Ms      int       `json:"lat_p95_ms"`
	LatEWMA       int       `json:"lat_ewma_ms"`
	Score         float64   `json:"score"`
	PriceIn       float64   `json:"price_in_per_1m"`
	PriceOut      float64   `json:"price_out_per_1m"`
	BillingMode   string    `json:"billing_mode"`
	Trust         float64   `json:"trust"`
	BaseURLMs     int       `json:"baseurl_latency_ms"`
	ConcUsed      int       `json:"conc_used"`
	ConcLimit     int       `json:"conc_limit"`
	FPUsed        int       `json:"fp_used"`
	FPLimit       int       `json:"fp_limit"`
	RPMUsed       int       `json:"rpm_used"`
	RPMLimit      int       `json:"rpm_limit"`
	Generation    int64     `json:"generation"`
	SrcPriority   int       `json:"source_priority"`
	UpdatedAt     time.Time `json:"updated_at"`
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
	BillingMode  string
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
