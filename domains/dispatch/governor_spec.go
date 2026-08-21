package dispatch

import "time"

// GovernorSpec is the immutable, versioned input to a GovernorBackend.
// Producers MUST NOT mutate a GovernorSpec after construction; the
// management layer produces a fresh GovernorSpec on every policy change
// and the backend compares Spec.Revision for cache invalidation.
//
// Mode ∈ {ModeConcurrency, ModeRPM, ModeTPM, ModeDisabled} mirroring the
// constants in queued_request.go. The backend is responsible for honoring
// the mode semantics; no validation happens here (Stage A: contracts only).
//
// Limit is Mode-dependent:
//   - ModeConcurrency → in-flight cap (mirrors CredentialRef.ConcurrencyLimit)
//   - ModeRPM         → requests per minute (mirrors CredentialRef.RPMLimit)
//   - ModeTPM         → tokens per minute (mirrors CredentialRef.TPMLimit)
//   - ModeDisabled    → ignored
type GovernorSpec struct {
	CredentialID int
	ProviderID   int
	Mode         string
	Limit        int
	// RPMLimit and TPMLimit mirror the per-mode fallback values used by
	// CredentialRef. Stage B RedisEnforce may consume these for the
	// sliding-window key shape; Stage A leaves them as informational
	// copies for diagnostic parity with queued_request.go.
	RPMLimit int
	TPMLimit int
	// LeaseTTL is the Stage B Redis lease duration. Stage A: declared but
	// ignored by all backends. Zero is treated as "backend default".
	LeaseTTL time.Duration
	// Backend names the target GovernorBackendKind for this spec. Stage A:
	// informational; the management layer dispatches by it. Stage B
	// backends will use it to route New(spec) to the right pool.
	Backend GovernorBackendKind
	// Revision monotonically increases per (CredentialID, ProviderID).
	// LocalBackend ignores it. Used by Stage B backends and by
	// ApplyPolicySnapshot to detect miss-by-one publication.
	Revision uint64
}