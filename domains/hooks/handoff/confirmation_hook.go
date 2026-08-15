package handoff

import (
	"context"
	"fmt"
	"time"
)

const defaultConfirmationTTL = 5 * time.Minute

// PrepareConfirmation persists a short-lived capability only for an explicit
// request-side handoff. Transparent handoffs never create pending state.
func (h *TriggerHook) PrepareConfirmation(ctx context.Context, result *RequestResult, apiKeyID int) (*ConfirmationProposal, string, error) {
	if h == nil || result == nil || !result.Triggered || !result.Explicit || result.Record == nil {
		return nil, "", fmt.Errorf("handoff result cannot be confirmed")
	}
	store, ok := h.db.(ConfirmationStore)
	if !ok {
		return nil, "", fmt.Errorf("handoff confirmation store is unavailable")
	}
	record := *result.Record
	record.SummaryText = redactResumeSensitive(record.SummaryText)
	record.HandoffPrompt = redactResumeSensitive(record.HandoffPrompt)
	proposal, token, err := NewConfirmationProposal(&record, apiKeyID, time.Now().Add(defaultConfirmationTTL))
	if err != nil {
		return nil, "", err
	}
	if err := store.SavePending(ctx, proposal); err != nil {
		return nil, "", err
	}
	return proposal, token, nil
}

// ConfirmRequest commits an already authenticated and ownership-checked
// proposal. Only the initial successful confirmation sends notifications.
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
	if err != nil || result == nil || !result.FirstConfirmation {
		return result, err
	}
	level := NotifyLevel(h.loadString(result.Record.TenantID, "handoff.notify_level", string(h.config.NotifyLevel)))
	h.notify(ctx, level, &result.Record)
	return result, nil
}
