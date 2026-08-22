package handoff

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

const defaultConfirmationTTL = 5 * time.Minute

// PrepareConfirmation persists a short-lived capability only for an explicit
// request-side handoff. Transparent handoffs never create pending state.
func (h *TriggerHook) PrepareConfirmation(ctx context.Context, result *RequestResult, apiKeyID int) (*ConfirmationProposal, string, error) {
	if h == nil || result == nil || !result.Triggered || !result.Explicit || result.Record == nil {
		return nil, "", fmt.Errorf("handoff result cannot be confirmed")
	}
	abortReservation := func() {
		if h.config.GoalTrigger != nil {
			h.config.GoalTrigger.Abort(result.ReservationID)
		}
	}
	store, ok := h.db.(ConfirmationStore)
	if !ok {
		abortReservation()
		return nil, "", fmt.Errorf("handoff confirmation store is unavailable")
	}
	record := *result.Record
	record.SummaryText = redactResumeSensitive(record.SummaryText)
	record.HandoffPrompt = redactResumeSensitive(record.HandoffPrompt)
	if result.GoalState != nil && (result.GoalState.TenantID == "" || result.GoalState.TenantID != record.TenantID) {
		abortReservation()
		return nil, "", fmt.Errorf("handoff goal state tenant mismatch")
	}
	proposal, token, err := NewConfirmationProposal(&record, apiKeyID, time.Now().Add(defaultConfirmationTTL))
	if err != nil {
		abortReservation()
		return nil, "", err
	}
	proposal.GoalState = cloneGoalState(result.GoalState)
	if snapshot, _, err := marshalPersistedGoalState(proposal.GoalState); err != nil {
		abortReservation()
		return nil, "", err
	} else if len(snapshot) > 0 {
		RecordPayload("snapshot", len(snapshot))
	}
	if err := store.SavePending(ctx, proposal); err != nil {
		abortReservation()
		RecordProposal(result.Reason, "save_failed")
		return nil, "", err
	}
	RecordProposal(result.Reason, "prepared")
	if h.config.GoalTrigger != nil {
		if result.GoalState != nil {
			h.config.GoalTrigger.Bind(proposal.ID, result.GoalState)
		}
		h.config.GoalTrigger.Commit(result.ReservationID)
	}
	return proposal, token, nil
}

// ConfirmRequest commits an already authenticated and ownership-checked
// proposal. Accounting is always fail-closed and exactly once; Goal restore is
// attempted after accounting commits and remains durably retryable. When Goal
// restore fails with a retryable or manual-required error, the accounting
// result is still returned together with the error so the caller (HTTP layer)
// can map it (e.g. ErrGoalRestoreRetryable -> 503) while the client retries the
// idempotent confirmation to complete restore. Non-restore errors remain
// fail-closed.
func (h *TriggerHook) ConfirmRequest(ctx context.Context, input ConfirmationInput) (*ConfirmationResult, error) {
	if h == nil {
		return nil, fmt.Errorf("handoff hook is unavailable")
	}
	store, ok := h.db.(ConfirmationStore)
	if !ok {
		return nil, fmt.Errorf("handoff confirmation store is unavailable")
	}
	input.MaxPerSession = h.loadInt(input.TenantID, "handoff.max_per_session", h.config.MaxPerSession)
	input.CooldownSeconds = h.loadInt(input.TenantID, "handoff.cooldown_seconds", h.config.CooldownSeconds)
	result, err := store.Confirm(ctx, input)
	if err != nil || result == nil {
		RecordConfirmation(confirmationResultLabel(err))
		return result, err
	}
	if result.Record.TenantID != input.TenantID {
		RecordConfirmation("invalid")
		return result, fmt.Errorf("handoff confirmation tenant mismatch")
	}
	// First confirmation is the only accounting commit; a subsequent call
	// with the same idempotency key is an idempotent replay of the already
	// confirmed proposal (both return 200, but only the first counts as
	// "first").
	if result.FirstConfirmation {
		RecordConfirmation("first")
	} else {
		RecordConfirmation("idempotent")
	}
	// Propagate retryable / manual-required restore failures so the HTTP
	// layer can surface them (503 for retryable). Accounting has already
	// committed, so the result is valid; the client retries the idempotent
	// confirmation to drive restore to completion.
	if restoreErr := h.restoreGoalState(ctx, input, result); restoreErr != nil {
		slog.Warn("handoff_goal_restore_failed", "session_id", result.NewSessionID, "error", restoreErr)
		if errors.Is(restoreErr, ErrGoalRestoreRetryable) || errors.Is(restoreErr, ErrGoalRestoreManualRequired) {
			// restore metrics already recorded inside restoreGoalState.
			return result, restoreErr
		}
	}
	if result.FirstConfirmation {
		level := NotifyLevel(h.loadString(result.Record.TenantID, "handoff.notify_level", string(h.config.NotifyLevel)))
		h.notify(ctx, level, &result.Record)
	}
	return result, nil
}

// confirmationResultLabel maps a store.Confirm error onto a low-cardinality
// result label for handoff_confirmation_total.
func confirmationResultLabel(err error) string {
	switch {
	case err == nil:
		return "first"
	case errors.Is(err, ErrConfirmationExpired):
		return "expired"
	case errors.Is(err, ErrConfirmationReplay):
		return "replay"
	default:
		return "invalid"
	}
}

func (h *TriggerHook) restoreGoalState(ctx context.Context, input ConfirmationInput, result *ConfirmationResult) error {
	if h.config.GoalStateSerializer == nil {
		return nil
	}
	start := time.Now()
	recordResult := func(result string) {
		RecordRestore(result, time.Since(start).Seconds())
	}
	var durable GoalRestoreStore
	if store, ok := h.db.(GoalRestoreStore); ok {
		durable = store
	}
	var state *GoalState
	var restoreStatus string
	if durable != nil {
		durableState, err := durable.GetGoalRestoreState(ctx, input.ProposalID, input.TenantID)
		if err != nil {
			if errors.Is(err, ErrGoalRestoreStateInvalid) {
				_ = durable.MarkGoalRestoreManualRequired(ctx, input.ProposalID, input.TenantID, err.Error())
				reason := classifyRestoreError(err)
				IncRestoreFailure(reason)
				recordResult(reason)
				return fmt.Errorf("%w: %v", ErrGoalRestoreManualRequired, err)
			}
			recordResult("retryable")
			IncRestoreFailure("retryable")
			return fmt.Errorf("%w: load durable goal restore state: %v", ErrGoalRestoreRetryable, err)
		}
		if durableState == nil {
			recordResult("no_snapshot")
			return nil
		}
		state = durableState.GoalState
		restoreStatus = durableState.Status
		if restoreStatus == confirmationStatusRestored || restoreStatus == confirmationStatusManualRequired {
			recordResult("no_snapshot")
			return nil
		}
	} else {
		if h.config.GoalTrigger != nil && h.config.GoalTrigger.IsAcknowledged(input.ProposalID, input.TenantID) {
			recordResult("no_snapshot")
			return nil
		}
		if h.config.GoalTrigger != nil {
			state = h.config.GoalTrigger.Peek(input.ProposalID, input.TenantID)
		}
	}
	if state == nil {
		recordResult("no_snapshot")
		return nil
	}
	if state.TenantID == "" || state.TenantID != input.TenantID {
		if durable != nil {
			_ = durable.MarkGoalRestoreManualRequired(ctx, input.ProposalID, input.TenantID, "goal state tenant mismatch")
		}
		IncRestoreFailure("tenant_mismatch")
		recordResult("manual_required")
		return fmt.Errorf("%w: goal restore tenant mismatch", ErrGoalRestoreManualRequired)
	}
	if durable != nil {
		_ = durable.MarkGoalRestoreAttempt(ctx, input.ProposalID, input.TenantID, "")
	}
	if err := h.config.GoalStateSerializer.Restore(ctx, result.NewSessionID, state); err != nil {
		if durable != nil {
			if isManualGoalRestoreError(err) {
				_ = durable.MarkGoalRestoreManualRequired(ctx, input.ProposalID, input.TenantID, err.Error())
			} else {
				_ = durable.MarkGoalRestoreAttempt(ctx, input.ProposalID, input.TenantID, err.Error())
			}
		}
		// Map manual-required restore failures (conflict / version mismatch)
		// onto ErrGoalRestoreManualRequired so the HTTP layer can distinguish
		// them from transient retryable failures.
		reason := classifyRestoreError(err)
		IncRestoreFailure(reason)
		recordResult(reason)
		if isManualGoalRestoreError(err) {
			return fmt.Errorf("%w: %v", ErrGoalRestoreManualRequired, err)
		}
		return fmt.Errorf("%w: %v", ErrGoalRestoreRetryable, err)
	}
	if durable != nil {
		if err := durable.MarkGoalRestored(ctx, input.ProposalID, input.TenantID); err != nil {
			recordResult("restored")
			return fmt.Errorf("mark goal restore complete: %w", err)
		}
	}
	if h.config.GoalTrigger != nil {
		h.config.GoalTrigger.Ack(input.ProposalID, input.TenantID)
	}
	recordResult("restored")
	return nil
}

// classifyRestoreError maps a goal-restore error onto a low-cardinality reason
// label for handoff_restore_failure_total. Unknown errors are bucketed as
// retryable so operators can alert on non-manual failures.
func classifyRestoreError(err error) string {
	switch {
	case errors.Is(err, ErrGoalRestoreConflict):
		return "conflict"
	case errors.Is(err, ErrGoalRestoreVersionMismatch):
		return "version_mismatch"
	case errors.Is(err, ErrGoalRestoreManualRequired):
		return "manual_required"
	case errors.Is(err, ErrGoalRestoreStateInvalid):
		return "manual_required"
	case errors.Is(err, ErrGoalRestoreRetryable):
		return "retryable"
	default:
		return "retryable"
	}
}

func isManualGoalRestoreError(err error) bool {
	if err == nil {
		return false
	}
	// Classify via errors.Is against typed sentinels rather than substring
	// matching, so callers that wrap the error (with %w) are still detected
	// and unrelated errors that happen to contain a phrase are not misrouted
	// to manual_required.
	return errors.Is(err, ErrGoalRestoreConflict) || errors.Is(err, ErrGoalRestoreVersionMismatch)
}
