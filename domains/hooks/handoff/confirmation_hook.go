package handoff

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

const (
	defaultConfirmationTTL    = 5 * time.Minute
	goalRestoreRequiredMarker = "goal-restore-required:v1"
)

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
	if result.GoalState != nil {
		if result.GoalState.TenantID == "" || result.GoalState.TenantID != record.TenantID {
			abortReservation()
			return nil, "", fmt.Errorf("handoff goal state tenant mismatch")
		}
		record.HandoffPrompt = goalRestoreRequiredMarker
	}
	proposal, token, err := NewConfirmationProposal(&record, apiKeyID, time.Now().Add(defaultConfirmationTTL))
	if err != nil {
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
// proposal. Goal restore is fail-closed and retryable with the same idempotency
// key; notifications are sent only after restoration succeeds.
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
		return result, err
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
	if h.config.GoalTrigger != nil && h.config.GoalTrigger.IsAcknowledged(input.ProposalID, input.TenantID) {
		return nil
	}
	var state *GoalState
	if h.config.GoalTrigger != nil {
		state = h.config.GoalTrigger.Peek(input.ProposalID, input.TenantID)
	}
	if state == nil {
		if result.Record.HandoffPrompt == goalRestoreRequiredMarker {
			return ErrGoalRestoreRetryable
		}
		return nil
	}
	if state.TenantID == "" || state.TenantID != input.TenantID {
		return fmt.Errorf("handoff goal restore tenant mismatch")
	}
	if err := h.config.GoalStateSerializer.Restore(ctx, result.NewSessionID, state); err != nil {
		return fmt.Errorf("%w: %v", ErrGoalRestoreRetryable, err)
	}
	if h.config.GoalTrigger != nil {
		h.config.GoalTrigger.Ack(input.ProposalID, input.TenantID)
	}
	return nil
}
