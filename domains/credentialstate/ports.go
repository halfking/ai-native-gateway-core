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
package credentialstate

import (
	"context"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

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
	// injected probe submitter.
	UpdateOnFailure(ctx context.Context, credID int, model string, errKind errorsx.ErrorKind, requestID string)

	// UpdateFromProbe applies an authoritative probe result (from a
	// background or manual probe). Probe results always win over
	// in-flight request-derived state.
	UpdateFromProbe(ctx context.Context, state *State)
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

// StateCacheResetter is used by an operator-triggered recovery before an
// authoritative probe verifies the credential again.
type StateCacheResetter interface {
	ClearCredentialCache(ctx context.Context, credID int)
}
