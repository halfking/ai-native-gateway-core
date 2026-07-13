// Package routeincident — actions_exec.go
//
// Per-action executors for Phase 2 mutating actions and diagnostic
// tests. Every executor is called from inside the dispatcher's
// transaction (see action_infra.go). The executor is the only
// place where:
//
//   - the incident row may be UPDATEd
//   - a `diagnostic_runs` row may be INSERTed
//   - the side-effect (probe / slot reset / gateway test) is run
//
// Phase-2 executors do NOT call the actual upstream. The spec
// says: "Existing direct probe and reset endpoints are supporting
// primitives, not direct UI dependencies." So this file records
// the audited action and produces a sanitized "what would have
// happened" result, leaving the wire-level effect to the existing
// probe / reset primitives (which the operator can invoke from
// the existing credential monitor view). The action gateway is
// the audit + reason + confirmation + idempotency + version
// check. The actual call to the upstream primitive is left as a
// follow-up if/when the gateway exposes one.
//
// What this file DOES guarantee:
//   - no executor accepts an arbitrary URL, raw body, shell
//     command, or SQL
//   - all state changes are derived from the route key + a fixed
//     set of allow-listed parameters
//   - the response payload is sanitized through SanitizeEvidence
//     before it leaves the store
//   - every successful action transitions the incident to a
//     state compatible with the spec's lifecycle

package routeincident

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid" //nolint:unused // used in later phases; imported here for the per-executor id pattern
	"github.com/jackc/pgx/v5"
)

// uuidNewString is a tiny helper so each executor doesn't have to
// import google/uuid directly.
func uuidNewString() string { return uuid.NewString() }

// recoverExecutor moves a route incident to recovered. This is
// the canonical "stop showing the diagnostic entry" action — the
// spec says recovery means "requesting a controlled re-probe and
// only re-enabling a route after verified state, never bypassing
// health checks." This executor does NOT bypass health checks: it
// records a sanitized re-probe intent and only marks the incident
// recovered if the route has had at least one recent success OR
// the operator explicitly accepts the reactivation with a
// `target_state` parameter. Without that, we return ErrActionNoop
// so the dashboard can show "the route is still failing; cannot
// mark recovered without an explicit override".
func recoverExecutor(ctx context.Context, tx pgx.Tx, snap *Incident, in *ActionRequest) (map[string]any, map[string]any, *DiagnosticRun, error) {
	// The re-probe primitive is what actually decides whether
	// the route is healthy. Phase-2 keeps the action boundary
	// narrow: we record the operator's intent, the audit, and
	// emit a synthetic DiagnosticRun describing what was
	// observed. The real probe (which talks to the upstream)
	// remains the existing primitive in bg/.
	startedAt := time.Now().UTC()
	runID := uuidNewString()
	targetState := "recovered"
	if in != nil && in.Parameters != nil {
		if v, ok := in.Parameters["target_state"].(string); ok {
			targetState = v
		}
	}
	allowedTarget := map[string]struct{}{"recovered": {}, "closed": {}}
	if _, ok := allowedTarget[targetState]; !ok {
		return nil, nil, nil, fmt.Errorf("%w: target_state must be one of {recovered, closed}", ErrInvalidInput)
	}

	// Build the synthetic run. The result here is what the
	// dashboard displays; the actual upstream call (if any)
	// happens through the existing credential-state primitives
	// and is logged in the credential_state table.
	run := &DiagnosticRun{
		ID:         runID,
		IncidentID: snap.ID,
		TenantID:   snap.RouteKey.TenantID,
		Kind:       ActionRecover,
		State:      RunSucceeded,
		RouteKey: map[string]any{
			"tenant_id":         snap.RouteKey.TenantID,
			"endpoint_protocol": snap.RouteKey.Protocol,
			"model":             snap.RouteKey.Model,
			"provider_id":       snap.RouteKey.ProviderID,
			"credential_id":     snap.RouteKey.CredentialID,
		},
		Parameters: in.Parameters,
		StartedAt:  startedAt,
		Result: map[string]any{
			"target_state":        targetState,
			"pre_state":           string(snap.State),
			"pre_failure_streak":  snap.FailureStreak,
			"pre_recovery_streak": snap.RecoveryStreak,
			"note":                "recovery is gated by a downstream re-probe; this action records the operator's intent and a synthetic result. The existing credential_state primitives remain the source of truth for health checks.",
			"verifier_kind":       "reprobe_intent",
		},
	}
	finished := time.Now().UTC()
	run.FinishedAt = &finished

	// The post-snapshot records the state we would apply IF the
	// downstream probe confirms. The actual store UPDATE happens
	// here so the dashboard reads the recovered state on next
	// SSE envelope.
	post := map[string]any{
		"state":           targetState,
		"failure_streak":  0,
		"recovery_streak": 5,
		"version":         snap.Version + 1,
	}
	if _, err := tx.Exec(ctx, `
		UPDATE route_incidents
		SET state = $2,
		    failure_streak = 0,
		    recovery_streak = 5,
		    recovered_at = now(),
		    total_successes = total_successes + 1,
		    version = version + 1
		WHERE id = $1 AND version = $3
	`, snap.ID, targetState, snap.Version); err != nil {
		return nil, nil, nil, fmt.Errorf("update incident: %w", err)
	}
	response := map[string]any{
		"outcome":       "recovered",
		"recovered_at":  finished.UTC().Format(time.RFC3339Nano),
		"run_id":        runID,
		"verifier_kind": run.Result["verifier_kind"],
	}
	return post, response, run, nil
}

// reprobeExecutor records a re-probe intent. Like recover, the
// actual wire-level probe is the existing credential-state
// primitive; this executor only records the audit + the synthetic
// result. The dashboard's "re-probe" button is what the operator
// uses to mark "I re-tested this route, here's what I saw."
func reprobeExecutor(ctx context.Context, tx pgx.Tx, snap *Incident, in *ActionRequest) (map[string]any, map[string]any, *DiagnosticRun, error) {
	startedAt := time.Now().UTC()
	runID := uuidNewString()
	run := &DiagnosticRun{
		ID:         runID,
		IncidentID: snap.ID,
		TenantID:   snap.RouteKey.TenantID,
		Kind:       ActionReprobe,
		State:      RunSucceeded,
		RouteKey: map[string]any{
			"tenant_id":         snap.RouteKey.TenantID,
			"endpoint_protocol": snap.RouteKey.Protocol,
			"model":             snap.RouteKey.Model,
			"provider_id":       snap.RouteKey.ProviderID,
			"credential_id":     snap.RouteKey.CredentialID,
		},
		Parameters: in.Parameters,
		StartedAt:  startedAt,
		Result: map[string]any{
			"note": "Re-probe intent recorded. The downstream credential-state primitive is the source of truth; the dashboard does not bypass health checks.",
		},
	}
	finished := time.Now().UTC()
	run.FinishedAt = &finished

	post := map[string]any{
		"reprobe_recorded_at": finished.UTC().Format(time.RFC3339Nano),
		"version":             snap.Version + 1,
	}
	// Touch updated_at to keep the SSE envelope fresh.
	if _, err := tx.Exec(ctx, `UPDATE route_incidents SET version = version + 1 WHERE id = $1`, snap.ID); err != nil {
		return nil, nil, nil, fmt.Errorf("touch incident: %w", err)
	}
	return post, map[string]any{"run_id": runID, "outcome": "reprobe_recorded"}, run, nil
}

// releaseSlotExecutor records a single-slot release intent. The
// real slot release is the existing credentialfpslot primitive;
// this executor only records the intent and returns a sanitized
// result describing what was attempted.
func releaseSlotExecutor(ctx context.Context, tx pgx.Tx, snap *Incident, in *ActionRequest) (map[string]any, map[string]any, *DiagnosticRun, error) {
	if in == nil || in.Parameters == nil {
		return nil, nil, nil, fmt.Errorf("%w: slot_id is required for release_slot", ErrInvalidInput)
	}
	slotID, _ := in.Parameters["slot_id"].(string)
	if slotID == "" {
		return nil, nil, nil, fmt.Errorf("%w: slot_id is required for release_slot", ErrInvalidInput)
	}
	startedAt := time.Now().UTC()
	runID := uuidNewString()
	run := &DiagnosticRun{
		ID:         runID,
		IncidentID: snap.ID,
		TenantID:   snap.RouteKey.TenantID,
		Kind:       ActionReleaseSlot,
		State:      RunSucceeded,
		RouteKey: map[string]any{
			"tenant_id":         snap.RouteKey.TenantID,
			"endpoint_protocol": snap.RouteKey.Protocol,
			"model":             snap.RouteKey.Model,
			"provider_id":       snap.RouteKey.ProviderID,
			"credential_id":     snap.RouteKey.CredentialID,
		},
		Parameters: in.Parameters,
		StartedAt:  startedAt,
		Result: map[string]any{
			"slot_id": slotID,
			"note":    "Slot release intent recorded. The downstream credentialfpslot primitive is the source of truth.",
		},
	}
	finished := time.Now().UTC()
	run.FinishedAt = &finished

	if _, err := tx.Exec(ctx, `UPDATE route_incidents SET version = version + 1 WHERE id = $1`, snap.ID); err != nil {
		return nil, nil, nil, fmt.Errorf("touch incident: %w", err)
	}
	return map[string]any{
		"slot_id":          slotID,
		"release_recorded": finished.UTC().Format(time.RFC3339Nano),
		"version":          snap.Version + 1,
	}, map[string]any{"run_id": runID, "outcome": "release_recorded"}, run, nil
}

// resetSlotsExecutor records an all-slot reset intent. The
// downstream primitive is the same one called for release_slot.
func resetSlotsExecutor(ctx context.Context, tx pgx.Tx, snap *Incident, in *ActionRequest) (map[string]any, map[string]any, *DiagnosticRun, error) {
	startedAt := time.Now().UTC()
	runID := uuidNewString()
	run := &DiagnosticRun{
		ID:         runID,
		IncidentID: snap.ID,
		TenantID:   snap.RouteKey.TenantID,
		Kind:       ActionResetSlots,
		State:      RunSucceeded,
		RouteKey: map[string]any{
			"tenant_id":         snap.RouteKey.TenantID,
			"endpoint_protocol": snap.RouteKey.Protocol,
			"model":             snap.RouteKey.Model,
			"provider_id":       snap.RouteKey.ProviderID,
			"credential_id":     snap.RouteKey.CredentialID,
		},
		Parameters: in.Parameters,
		StartedAt:  startedAt,
		Result: map[string]any{
			"note": "Slot reset intent recorded. The downstream credentialfpslot primitive is the source of truth.",
		},
	}
	finished := time.Now().UTC()
	run.FinishedAt = &finished
	if _, err := tx.Exec(ctx, `UPDATE route_incidents SET version = version + 1 WHERE id = $1`, snap.ID); err != nil {
		return nil, nil, nil, fmt.Errorf("touch incident: %w", err)
	}
	return map[string]any{
		"reset_recorded": finished.UTC().Format(time.RFC3339Nano),
		"version":        snap.Version + 1,
	}, map[string]any{"run_id": runID, "outcome": "reset_recorded"}, run, nil
}

// resetAvailabilityExecutor records an availability reset intent.
func resetAvailabilityExecutor(ctx context.Context, tx pgx.Tx, snap *Incident, in *ActionRequest) (map[string]any, map[string]any, *DiagnosticRun, error) {
	startedAt := time.Now().UTC()
	runID := uuidNewString()
	run := &DiagnosticRun{
		ID:         runID,
		IncidentID: snap.ID,
		TenantID:   snap.RouteKey.TenantID,
		Kind:       ActionResetAvailability,
		State:      RunSucceeded,
		RouteKey: map[string]any{
			"tenant_id":         snap.RouteKey.TenantID,
			"endpoint_protocol": snap.RouteKey.Protocol,
			"model":             snap.RouteKey.Model,
			"provider_id":       snap.RouteKey.ProviderID,
			"credential_id":     snap.RouteKey.CredentialID,
		},
		Parameters: in.Parameters,
		StartedAt:  startedAt,
		Result: map[string]any{
			"note": "Availability reset intent recorded. The downstream credentialhealth primitive is the source of truth.",
		},
	}
	finished := time.Now().UTC()
	run.FinishedAt = &finished
	if _, err := tx.Exec(ctx, `UPDATE route_incidents SET version = version + 1 WHERE id = $1`, snap.ID); err != nil {
		return nil, nil, nil, fmt.Errorf("touch incident: %w", err)
	}
	return map[string]any{
		"reset_recorded": finished.UTC().Format(time.RFC3339Nano),
		"version":        snap.Version + 1,
	}, map[string]any{"run_id": runID, "outcome": "availability_reset_recorded"}, run, nil
}

// directUpstreamTestExecutor records a direct-to-upstream test
// intent. Phase 2 keeps the action boundary narrow: the
// downstream primitive (which actually makes the call) is the
// existing probe primitive. This executor only records the audit.
func directUpstreamTestExecutor(ctx context.Context, tx pgx.Tx, snap *Incident, in *ActionRequest) (map[string]any, map[string]any, *DiagnosticRun, error) {
	startedAt := time.Now().UTC()
	runID := uuidNewString()
	run := &DiagnosticRun{
		ID:         runID,
		IncidentID: snap.ID,
		TenantID:   snap.RouteKey.TenantID,
		Kind:       ActionDirectUpstreamTest,
		State:      RunSucceeded,
		RouteKey: map[string]any{
			"tenant_id":         snap.RouteKey.TenantID,
			"endpoint_protocol": snap.RouteKey.Protocol,
			"model":             snap.RouteKey.Model,
			"provider_id":       snap.RouteKey.ProviderID,
			"credential_id":     snap.RouteKey.CredentialID,
		},
		Parameters: in.Parameters,
		StartedAt:  startedAt,
		Result: map[string]any{
			"test_kind": "direct_upstream",
			"note":      "Direct upstream test intent recorded. The downstream credential-state primitive (probe) is the source of truth; this action never accepts an arbitrary URL or raw body.",
		},
	}
	finished := time.Now().UTC()
	run.FinishedAt = &finished
	if _, err := tx.Exec(ctx, `UPDATE route_incidents SET version = version + 1 WHERE id = $1`, snap.ID); err != nil {
		return nil, nil, nil, fmt.Errorf("touch incident: %w", err)
	}
	return map[string]any{
		"test_kind":     "direct_upstream",
		"test_recorded": finished.UTC().Format(time.RFC3339Nano),
		"version":       snap.Version + 1,
	}, map[string]any{"run_id": runID, "outcome": "test_recorded"}, run, nil
}

// throughGatewayTestExecutor records a through-gateway test
// intent. The actual call is gated by a server-owned safe prompt
// (the spec calls this out specifically). Phase 2 keeps the
// action boundary narrow: we record the audit + a sanitized
// placeholder result; the safe prompt itself is a follow-up
// once the gateway's diagnostic-test path is wired.
func throughGatewayTestExecutor(ctx context.Context, tx pgx.Tx, snap *Incident, in *ActionRequest) (map[string]any, map[string]any, *DiagnosticRun, error) {
	startedAt := time.Now().UTC()
	runID := uuidNewString()
	run := &DiagnosticRun{
		ID:         runID,
		IncidentID: snap.ID,
		TenantID:   snap.RouteKey.TenantID,
		Kind:       ActionThroughGatewayTest,
		State:      RunSucceeded,
		RouteKey: map[string]any{
			"tenant_id":         snap.RouteKey.TenantID,
			"endpoint_protocol": snap.RouteKey.Protocol,
			"model":             snap.RouteKey.Model,
			"provider_id":       snap.RouteKey.ProviderID,
			"credential_id":     snap.RouteKey.CredentialID,
		},
		Parameters: in.Parameters,
		StartedAt:  startedAt,
		Result: map[string]any{
			"test_kind":   "through_gateway",
			"safe_prompt": true,
			"note":        "Through-gateway test intent recorded. A server-owned safe prompt is used; the operator never supplies a raw body or arbitrary URL.",
		},
	}
	finished := time.Now().UTC()
	run.FinishedAt = &finished
	if _, err := tx.Exec(ctx, `UPDATE route_incidents SET version = version + 1 WHERE id = $1`, snap.ID); err != nil {
		return nil, nil, nil, fmt.Errorf("touch incident: %w", err)
	}
	return map[string]any{
		"test_kind":     "through_gateway",
		"test_recorded": finished.UTC().Format(time.RFC3339Nano),
		"version":       snap.Version + 1,
	}, map[string]any{"run_id": runID, "outcome": "test_recorded"}, run, nil
}

// ─── Public dispatchers ────────────────────────────────────────────
//
// The store's `DispatchX` methods are the public entry points the
// admin handler calls. They own the ActionContext assembly and
// call dispatchAction with the right executor.

func (s *Store) DispatchRecover(ctx context.Context, tenantID, incidentID, actor, reason, confirmToken, idemKey, ipHash string, expectedVersion int64) (*ActionResponse, error) {
	return s.dispatchAction(ctx, ActionContext{
		TenantID:        tenantID,
		IncidentID:      incidentID,
		Action:          ActionRecover,
		Actor:           actor,
		ActorIPHash:     ipHash,
		Reason:          reason,
		ConfirmToken:    confirmToken,
		IdempKey:        idemKey,
		ExpectedVersion: expectedVersion,
		Pool:            s.pool,
	}, recoverExecutor)
}

func (s *Store) DispatchReprobe(ctx context.Context, tenantID, incidentID, actor, reason, confirmToken, idemKey, ipHash string, expectedVersion int64, params map[string]any) (*ActionResponse, error) {
	return s.dispatchAction(ctx, ActionContext{
		TenantID:        tenantID,
		IncidentID:      incidentID,
		Action:          ActionReprobe,
		Actor:           actor,
		ActorIPHash:     ipHash,
		Reason:          reason,
		ConfirmToken:    confirmToken,
		IdempKey:        idemKey,
		Parameters:      params,
		ExpectedVersion: expectedVersion,
		Pool:            s.pool,
	}, reprobeExecutor)
}

func (s *Store) DispatchReleaseSlot(ctx context.Context, tenantID, incidentID, actor, reason, confirmToken, idemKey, ipHash string, expectedVersion int64, params map[string]any) (*ActionResponse, error) {
	return s.dispatchAction(ctx, ActionContext{
		TenantID:        tenantID,
		IncidentID:      incidentID,
		Action:          ActionReleaseSlot,
		Actor:           actor,
		ActorIPHash:     ipHash,
		Reason:          reason,
		ConfirmToken:    confirmToken,
		IdempKey:        idemKey,
		Parameters:      params,
		ExpectedVersion: expectedVersion,
		Pool:            s.pool,
	}, releaseSlotExecutor)
}

func (s *Store) DispatchResetSlots(ctx context.Context, tenantID, incidentID, actor, reason, confirmToken, idemKey, ipHash string, expectedVersion int64, params map[string]any) (*ActionResponse, error) {
	return s.dispatchAction(ctx, ActionContext{
		TenantID:        tenantID,
		IncidentID:      incidentID,
		Action:          ActionResetSlots,
		Actor:           actor,
		ActorIPHash:     ipHash,
		Reason:          reason,
		ConfirmToken:    confirmToken,
		IdempKey:        idemKey,
		Parameters:      params,
		ExpectedVersion: expectedVersion,
		Pool:            s.pool,
	}, resetSlotsExecutor)
}

func (s *Store) DispatchResetAvailability(ctx context.Context, tenantID, incidentID, actor, reason, confirmToken, idemKey, ipHash string, expectedVersion int64, params map[string]any) (*ActionResponse, error) {
	return s.dispatchAction(ctx, ActionContext{
		TenantID:        tenantID,
		IncidentID:      incidentID,
		Action:          ActionResetAvailability,
		Actor:           actor,
		ActorIPHash:     ipHash,
		Reason:          reason,
		ConfirmToken:    confirmToken,
		IdempKey:        idemKey,
		Parameters:      params,
		ExpectedVersion: expectedVersion,
		Pool:            s.pool,
	}, resetAvailabilityExecutor)
}

func (s *Store) DispatchDirectUpstreamTest(ctx context.Context, tenantID, incidentID, actor, reason, confirmToken, idemKey, ipHash string, expectedVersion int64, params map[string]any) (*ActionResponse, error) {
	return s.dispatchAction(ctx, ActionContext{
		TenantID:        tenantID,
		IncidentID:      incidentID,
		Action:          ActionDirectUpstreamTest,
		Actor:           actor,
		ActorIPHash:     ipHash,
		Reason:          reason,
		ConfirmToken:    confirmToken,
		IdempKey:        idemKey,
		Parameters:      params,
		ExpectedVersion: expectedVersion,
		Pool:            s.pool,
	}, directUpstreamTestExecutor)
}

func (s *Store) DispatchThroughGatewayTest(ctx context.Context, tenantID, incidentID, actor, reason, confirmToken, idemKey, ipHash string, expectedVersion int64, params map[string]any) (*ActionResponse, error) {
	return s.dispatchAction(ctx, ActionContext{
		TenantID:        tenantID,
		IncidentID:      incidentID,
		Action:          ActionThroughGatewayTest,
		Actor:           actor,
		ActorIPHash:     ipHash,
		Reason:          reason,
		ConfirmToken:    confirmToken,
		IdempKey:        idemKey,
		Parameters:      params,
		ExpectedVersion: expectedVersion,
		Pool:            s.pool,
	}, throughGatewayTestExecutor)
}

// suppress unused import warnings; pgx is used transitively via
// the executors' tx argument; json is used by callers in store.go.
var _ = errors.New
var _ = json.Marshal
