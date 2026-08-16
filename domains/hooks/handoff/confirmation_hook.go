package handoff

import (
	"context"
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
	if _, _, err := marshalPersistedGoalState(proposal.GoalState); err != nil {
		abortReservation()
		return nil, "", err
	}
	if err := store.SavePending(ctx, proposal); err != nil {
		abortReservation()
		return nil, "", err
	}
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
// fail-open after accounting and remains durably retryable.
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
		return result, err
	}
	if result.Record.TenantID != input.TenantID {
		return result, fmt.Errorf("handoff confirmation tenant mismatch")
	}
	if err := h.restoreGoalState(ctx, input, result); err != nil {
		slog.Warn("handoff_goal_restore_failed", "session_id", result.NewSessionID, "error", err)
	}
	if result.FirstConfirmation {
		level := NotifyLevel(h.loadString(result.Record.TenantID, "handoff.notify_level", string(h.config.NotifyLevel)))
		h.notify(ctx, level, &result.Record)
	}
	return result, nil
}

func (h *TriggerHook) restoreGoalState(ctx context.Context, input ConfirmationInput, result *ConfirmationResult) error {
	if h.config.GoalStateSerializer == nil {
		return nil
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
			return fmt.Errorf("load durable goal restore state: %w", err)
		}
		if durableState == nil {
			return nil
		}
		state = durableState.GoalState
		restoreStatus = durableState.Status
		if restoreStatus == confirmationStatusRestored || restoreStatus == confirmationStatusManualRequired {
			return nil
		}
	} else {
		if h.config.GoalTrigger != nil && h.config.GoalTrigger.IsAcknowledged(input.ProposalID, input.TenantID) {
			return nil
		}
		if h.config.GoalTrigger != nil {
			state = h.config.GoalTrigger.Peek(input.ProposalID, input.TenantID)
		}
	}
	if state == nil {
		return nil
	}
	if state.TenantID == "" || state.TenantID != input.TenantID {
		if durable != nil {
			_ = durable.MarkGoalRestoreManualRequired(ctx, input.ProposalID, input.TenantID, "goal state tenant mismatch")
		}
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
		return fmt.Errorf("%w: %v", ErrGoalRestoreRetryable, err)
	}
	if durable != nil {
		if err := durable.MarkGoalRestored(ctx, input.ProposalID, input.TenantID); err != nil {
			return fmt.Errorf("mark goal restore complete: %w", err)
		}
	}
	if h.config.GoalTrigger != nil {
		h.config.GoalTrigger.Ack(input.ProposalID, input.TenantID)
	}
	return nil
}

func isManualGoalRestoreError(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return len(message) > 0 && (containsRestoreConflict(message) || containsRestoreVersionError(message))
}

func containsRestoreConflict(message string) bool {
	return stringContains(message, "conflicts with handoff state") || stringContains(message, "tenant mismatch")
}

func containsRestoreVersionError(message string) bool {
	return stringContains(message, "unsupported goal state version") || stringContains(message, "invalid goal state snapshot")
}

func stringContains(value, part string) bool {
	for i := 0; i+len(part) <= len(value); i++ {
		if value[i:i+len(part)] == part {
			return true
		}
	}
	return false
}
