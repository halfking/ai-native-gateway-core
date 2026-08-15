package streaming

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/handoff"
)

const maxHandoffConfirmationBodyBytes = 16 << 10

type handoffConfirmationRequest struct {
	HandoffID         string `json:"handoff_id"`
	ConfirmationToken string `json:"confirmation_token"`
	NewSessionID      string `json:"new_session_id"`
}

// HandleHandoffConfirmation confirms an explicit handoff using the same
// verified data-plane API key that received the original 202 proposal.
func (h *ChatHandler) HandleHandoffConfirmation(w http.ResponseWriter, r *http.Request) {
	requestID := generateRequestID()
	if r.Method != http.MethodPost {
		writeErrorJSON(w, http.StatusMethodNotAllowed, requestID, "method not allowed", "handoff_error", "method_not_allowed")
		return
	}
	if h == nil || h.handoffHook == nil || h.handoffSessionGetter == nil || h.keyVerifier == nil || !h.keyVerifier.Enabled() {
		writeErrorJSON(w, http.StatusServiceUnavailable, requestID, "handoff confirmation is unavailable", "handoff_error", "handoff_confirmation_unavailable")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxHandoffConfirmationBodyBytes)
	defer r.Body.Close()
	var payload handoffConfirmationRequest
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, requestID, "invalid confirmation request", "handoff_error", "handoff_confirmation_invalid")
		return
	}
	if _, err := uuid.Parse(payload.HandoffID); err != nil || len(payload.ConfirmationToken) < 32 || len(payload.ConfirmationToken) > 256 || !validGatewaySessionID(payload.NewSessionID) {
		writeErrorJSON(w, http.StatusBadRequest, requestID, "invalid confirmation request", "handoff_error", "handoff_confirmation_invalid")
		return
	}
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if len(idempotencyKey) < 16 || len(idempotencyKey) > 256 {
		writeErrorJSON(w, http.StatusBadRequest, requestID, "Idempotency-Key is required", "handoff_error", "handoff_idempotency_key_required")
		return
	}
	rawKey := extractBearerToken(r)
	if rawKey == "" {
		writeErrorJSON(w, http.StatusUnauthorized, requestID, "missing api key", "authentication_error", "authentication_error")
		return
	}
	keyInfo, err := h.keyVerifier.Verify(r.Context(), rawKey)
	if err != nil || keyInfo == nil {
		writeErrorJSON(w, http.StatusUnauthorized, requestID, "invalid api key", "authentication_error", "authentication_error")
		return
	}
	target, err := h.handoffSessionGetter.Get(r.Context(), payload.NewSessionID)
	if err != nil || target == nil || target.CreatedAt.IsZero() || target.TenantID != keyInfo.TenantID || target.APIKeyID != keyInfo.ID {
		writeErrorJSON(w, http.StatusNotFound, requestID, "handoff target session is unavailable", "handoff_error", "handoff_target_not_created")
		return
	}
	result, err := h.handoffHook.ConfirmRequest(r.Context(), handoff.ConfirmationInput{
		ProposalID: payload.HandoffID, TenantID: keyInfo.TenantID, APIKeyID: keyInfo.ID,
		Token: payload.ConfirmationToken, NewSessionID: payload.NewSessionID,
		TargetCreatedAt: target.CreatedAt, IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		status, code := handoffConfirmationError(err)
		writeErrorJSON(w, status, requestID, "handoff confirmation was rejected", "handoff_error", code)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "handoff_confirmed", "handoff_id": result.ProposalID,
		"previous_session_id": result.PreviousSessionID, "new_session_id": result.NewSessionID,
		"confirmed_at": result.ConfirmedAt,
	})
}

func validGatewaySessionID(value string) bool {
	if !strings.HasPrefix(value, "gw_") || len(value) <= 3 || len(value) > 255 {
		return false
	}
	for _, r := range value[3:] {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

func handoffConfirmationError(err error) (int, string) {
	switch {
	case errors.Is(err, handoff.ErrConfirmationExpired):
		return http.StatusGone, "handoff_confirmation_expired"
	case errors.Is(err, handoff.ErrConfirmationBudgetExhausted):
		return http.StatusConflict, "handoff_budget_exhausted"
	case errors.Is(err, handoff.ErrConfirmationCooldownActive):
		return http.StatusConflict, "handoff_cooldown_active"
	case errors.Is(err, handoff.ErrConfirmationReplay):
		return http.StatusConflict, "handoff_confirmation_replayed"
	default:
		return http.StatusNotFound, "handoff_confirmation_invalid"
	}
}
