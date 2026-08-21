package dispatch

import "time"

// Lease is the immutable evidence returned by a Stage B RedisEnforce
// governor on a successful Acquire. It is opaque to the forwarder — the
// forwarder stores and forwards it back into Release for cleanup.
//
// Stage A only declares the type; Stage B is the first consumer. The
// fields are exported because Lease flows across the Redis wire and into
// admin/log projections, but the package contract is "treat as opaque";
// callers MUST NOT inspect Token or modify fields.
type Lease struct {
	// Token uniquely identifies this admission slot. Opaque to callers.
	Token string
	// CredentialID is denormalized for diagnostics (logs, metrics) — the
	// forwarder already knows it, so this only aids observability.
	CredentialID int
	// Backend is "redis_enforce" for Stage B; reserved for future
	// cluster-shared backends. Stage A backends return "" or
	// string(BackendLocal).
	Backend string
	// IssuedAt and ExpiresAt are populated by the backend; Release
	// computes refresh-required from the residual TTL. Zero values are
	// valid for backends without leasing (LocalBackend, RedisShadow).
	IssuedAt  time.Time
	ExpiresAt time.Time
	// SpecRevision is the GovernorSpec.Revision under which this lease
	// was issued. Stage B uses it to refuse Release under a newer spec.
	SpecRevision uint64
}