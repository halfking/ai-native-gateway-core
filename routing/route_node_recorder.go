package routing

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/sessions"
)

// RouteNodeRecorder encapsulates route node success/failure recording logic.
// Wraps RouteNodeStore + SessionPreference to provide a single API for executor.
//
// 2026-06-26: New component in V3.1 routing redesign.
// Decoupled from Executor to keep executor.go focused on execution flow.
type RouteNodeRecorder struct {
	store       *RouteNodeStore
	sessionPref *sessions.SessionPreference
	requestID   string
}

// NewRouteNodeRecorder creates a new recorder.
// Both store and sessionPref are optional (nil-safe).
func NewRouteNodeRecorder(store *RouteNodeStore, sessionPref *sessions.SessionPreference) *RouteNodeRecorder {
	return &RouteNodeRecorder{
		store:       store,
		sessionPref: sessionPref,
	}
}

// RecordSuccess records a successful request to RouteNode and updates SessionPreference.
// Per design spec (2026-06-26 V3.1, flow step 15):
// - route_node:<credID>:<model>.SuccessCount++
// - SlideWindow append success record
// - session_pref:<sessionID> = credentialID
//
// sessionID may be empty (for unauthenticated/test requests).
func (r *RouteNodeRecorder) RecordSuccess(
	ctx context.Context,
	credentialID int,
	model string,
	sessionID string,
) {
	if r == nil {
		return
	}

	// Record to RouteNodeStore
	if r.store != nil {
		if err := r.store.RecordSuccess(ctx, credentialID, model, r.requestID); err != nil {
			slog.Debug("route node success record failed",
				"error", err,
				"credential_id", credentialID,
				"model", model,
			)
		}
	}

	// Update SessionPreference (session → credential + model)
	if r.sessionPref != nil && sessionID != "" {
		if err := r.sessionPref.Set(ctx, sessionID, credentialID, model); err != nil {
			slog.Debug("session pref set failed",
				"error", err,
				"session_id", sessionID,
				"credential_id", credentialID,
				"model", model,
			)
		}
	}
}

// RecordFailure records a failed request to RouteNode.
// Per design spec (2026-06-26 V3.1, flow step 15):
// - Only "credential-level" failures affect RouteNodeState
// - Transient/network failures are NOT recorded
// - Fatal errors (auth/quota permanent) trigger ForceUnpin
//
// Fatal errors don't necessarily delete RouteNodeState (other sessions may still use it).
func (r *RouteNodeRecorder) RecordFailure(
	ctx context.Context,
	credentialID int,
	model string,
	kind errorsx.ErrorKind,
) {
	if r == nil || r.store == nil {
		return
	}

	// Skip transient failures
	if isTransientFailure(kind) {
		return
	}

	// Record to RouteNodeStore
	if err := r.store.RecordFailure(ctx, credentialID, model, r.requestID, string(kind)); err != nil {
		slog.Debug("route node failure record failed",
			"error", err,
			"credential_id", credentialID,
			"model", model,
			"error_kind", kind,
		)
	}
}

// RecordFatal records a fatal error (auth/quota permanent).
// Per design spec (2026-06-26 V3.1, flow step 15):
// - ForceUnpin the fp_slot holder (handled by executor separately)
// - RouteNodeState is NOT deleted (other sessions may still use it)
func (r *RouteNodeRecorder) RecordFatal(
	ctx context.Context,
	credentialID int,
	model string,
	kind errorsx.ErrorKind,
) {
	// For now, fatal errors are just regular failures (with longer cooldown)
	// TODO: Add explicit fatal tracking if needed
	r.RecordFailure(ctx, credentialID, model, kind)
}

// isTransientFailure determines if a failure should NOT affect RouteNodeState.
// Per design spec: only credential-level failures should count.
func isTransientFailure(kind errorsx.ErrorKind) bool {
	if kind == "" {
		return false
	}

	switch kind {
	case errorsx.KindCanceled,
		errorsx.KindNetwork,
		errorsx.KindTimeout,
		errorsx.KindUpstreamDown,
		errorsx.KindContextLength:
		return true
	}

	// Client bugs are transient from credential's perspective
	if errorsx.IsClientBug(kind) {
		return true
	}

	return false
}

// IsCredentialFatal checks if an error kind indicates a permanent credential issue.
// Per design spec (2026-06-26 V3.1): fatal errors trigger ForceUnpin.
func IsCredentialFatal(kind errorsx.ErrorKind) bool {
	return errorsx.IsCredentialFatal(kind)
}

// Helper to check if errorsx is available (compile-time check)
var _ = errors.New
var _ = time.Now
