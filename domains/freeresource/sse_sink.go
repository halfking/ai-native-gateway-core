package freeresource

import "time"

// QuotaEvent is the free-pool quota lifecycle event published to SSE clients
// (the FreePoolView admin page) whenever a credential's quota state changes.
//
// Mirrors the structural-sink pattern used by bg.ProbeEventSink: defining the
// sink interface here (in domains/freeresource) lets QuotaTracker publish
// events WITHOUT importing the admin package, avoiding a circular dependency.
// The admin-side FreePoolSSEHub satisfies this interface structurally.
//
// Type values (low-cardinality, used as SSE event names by the frontend):
//   - "rate_limited"     — transient 429, short cooldown (KindRateLimit)
//   - "quota_exhausted"  — periodic quota window exhausted, resets at AutoResetAt
//     (KindQuotaPeriodic)
//   - "quota_permanent"  — balance exhausted, no auto-recovery (KindQuotaPermanent)
//   - "recovered"        — quota window reset / credential became healthy again
type QuotaEvent struct {
	Type         string     `json:"type"`
	CredentialID int64      `json:"credential_id"`
	ProviderCode string     `json:"provider_code,omitempty"`
	ModelID      string     `json:"model_id,omitempty"`
	AutoResetAt  *time.Time `json:"auto_reset_at,omitempty"`
	Ts           time.Time  `json:"ts"`
}

// QuotaEventSink is the structural interface that *admin.FreePoolSSEHub
// satisfies. QuotaTracker holds an optional sink; when set, CorrectFromHeaders
// and the reset worker publish events so the FreePoolView page refreshes in
// real time instead of waiting for the next manual/interval poll.
type QuotaEventSink interface {
	PublishQuotaEvent(evt QuotaEvent)
}
