// Package credentialstate — ports.go
//
// Consumer-facing interfaces exported by the credential-state domain.
//
// These exist so that consumer packages (bg, routing, streaming/executors,
// admin) can reference a SINGLE source of truth for the state-manager
// contract instead of each one re-declaring an inline interface whose
// method signatures can drift out of sync with *Manager (which is exactly
// the bug this file was created to fix — see 2026-06-30 audit).
//
// All interfaces are satisfied structurally by *Manager.
//
// IMPORTANT: the signatures here MUST stay byte-for-byte identical to the
// corresponding *Manager methods. If you change a *Manager method, update
// the matching interface here in the same commit.
//
// Deprecated: credentialstate is superseded by URSM v2 (domains/ursm/v2).
// It remains for legacy/off/canary modes only. Do not add new callers.
// In URSM_V2_MODE=authoritative, this package must have zero live reads/writes (spec §10 Step 5 C-1).
package credentialstate

import (
	"context"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// NoCandidatesSignal is the lightweight payload passed from
// streaming/executors to the state manager when the router returns zero
// available candidates (ErrNoAvailableNodes / "no_candidate_from_router").
//
// 2026-07-14 fix: previously this case never reached ActiveProbeWorker,
// so a 5-minute model outage on minimax-m3 produced zero probe rows and
// the realtime dashboard showed only the failed request with no
// follow-up investigation artifact. Surfacing the candidate list lets
// the manager fan out per-credential probes just like a single failed
// request would, but scoped to the most-likely-recoverable nodes.
type NoCandidatesSignal struct {
	// ClientModel is the canonical model the caller asked for (e.g.
	// "minimax-m3"). Used purely for logging / metrics.
	ClientModel string
	// TenantID is propagated onto the probe row's tenant_id so the
	// request_logs hot table shows the correct owner.
	TenantID string
	// RequestID is the request_id of the failed business request that
	// triggered the no-candidates path. Linked to the probe row's
	// parent_request_id so /request-logs can correlate.
	RequestID string
	// Candidates are the upstream candidates the router filtered out.
	// Only credentials with credential_id != 0 are forwarded to
	// ActiveProbeWorker.Submit so we never probe a phantom 0-id row.
	Candidates []NoCandidatesCandidate
}

// NoCandidatesCandidate is the minimum slice of provider.Candidate the
// state manager needs to fire an ActiveProbeWorker.Submit. Defined here
// (not in provider/) to keep credentialstate free of provider imports.
type NoCandidatesCandidate struct {
	CredentialID int
	ProviderID   int
	RawModel     string
	BillingMode  string // "free" | "per_token" | "" — used for soft/hard demote semantics
}

// StateObserver is the write-side contract: producers of state-change
// events (bg probes, request health tracker) call these methods to push
// updates into the state manager.
//
// Implemented by: *Manager
// Consumed by:     bg.CredentialProbeV2, bg.ModelProbeRunner,
//
//	routing.HealthTracker
type StateObserver interface {
	// UpdateOnSuccess records a successful real request and marks the
	// (credential, model) pair available + healthy.
	UpdateOnSuccess(ctx context.Context, credID int, model string, latencyMs int, requestID string)

	// UpdateOnFailure records a failed real request. After the configured
	// consecutive-failure threshold it triggers a fast re-probe via the
	// injected probe submitter. The supplied tenantID is propagated onto
	// the emitted request_logs row (and downstream SSE envelopes) so the
	// probe is attributed to the requesting tenant rather than "system".
	//
	// billingMode is the candidate's model_offers.billing_mode ("free",
	// "per_token", ...). When it is "free" the manager tolerates transient
	// failures (does not flip Available=false / cooling) so the credential
	// stays routable and is only soft-demoted by RecentSuccessRate.
	UpdateOnFailure(ctx context.Context, credID int, model string, errKind errorsx.ErrorKind, requestID, tenantID, billingMode string)

	// UpdateFromProbe applies an authoritative probe result (from a
	// background or manual probe). Probe results always win over
	// in-flight request-derived state.
	UpdateFromProbe(ctx context.Context, state *State)

	// OnNoCandidates (2026-07-14) fires when the router reports zero
	// available nodes for a request. The manager fans the signal out
	// to the registered active_probe submitter, restricted to
	// candidates with credential_id != 0. This is the missing piece
	// that previously left "no available node" outages un-probed
	// (minimax-m3 2026-07-14 incident).
	//
	// Safe to call when no active_probe submitter is wired: the
	// manager logs at debug and returns without touching state.
	OnNoCandidates(ctx context.Context, sig NoCandidatesSignal)
}

// StateProvider is the read-side contract: the router queries it to
// decide whether a (credential, model) candidate is currently routable
// and to read live metrics.
//
// Implemented by: *Manager
// Consumed by:     streaming/executors.Router
type StateProvider interface {
	// GetState returns the current state, traversing the memory ->
	// redis -> db cache hierarchy. Returns (nil, nil) when no state
	// exists for the pair (i.e. never probed + never served traffic).
	GetState(ctx context.Context, credID int, model string) (*State, error)

	// IsAvailable is a fast path returning (available, reason).
	// On any lookup error it fail-opens (returns true) so that the
	// state manager never becomes a single point of failure for routing.
	IsAvailable(ctx context.Context, credID int, model string) (bool, string)

	// Enabled reports whether the manager is wired and ready.
	Enabled() bool
}
