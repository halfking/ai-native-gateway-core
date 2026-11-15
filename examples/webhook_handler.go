package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	
	"github.com/kaixuan/llm-gateway-go/internal/approval"
	"github.com/kaixuan/llm-gateway-go/internal/casdoor"
)

// ApprovalWebhookHandler handles Casdoor approval decision webhooks.
type ApprovalWebhookHandler struct {
	casdoorClient   *casdoor.Client
	casdoorProvider *approval.CasdoorProvider
	approvalRouter  *approval.Router
	sessionResumer  SessionResumer // interface to resume sessions
}

// SessionResumer is an interface for resuming sessions after approval.
type SessionResumer interface {
	ResumeSession(ctx context.Context, sessionID, requestID string) error
}

// NewApprovalWebhookHandler creates a new approval webhook handler.
func NewApprovalWebhookHandler(
	client *casdoor.Client,
	provider *approval.CasdoorProvider,
	router *approval.Router,
) *ApprovalWebhookHandler {
	return &ApprovalWebhookHandler{
		casdoorClient:   client,
		casdoorProvider: provider,
		approvalRouter:  router,
	}
}

// SetSessionResumer sets the session resumer (called after initialization).
func (h *ApprovalWebhookHandler) SetSessionResumer(resumer SessionResumer) {
	h.sessionResumer = resumer
}

// HandleCasdoorApprovalWebhook processes Casdoor approval decision webhooks.
//
// This endpoint receives POST requests from Casdoor when an approval decision
// is made. It verifies the webhook signature, updates the approval status,
// and resumes the session if approved.
//
// Security:
//   - Webhook signature (v2) is verified before processing
//   - Idempotency is guaranteed (already-processed events are skipped)
//   - Tenant isolation is maintained
func (h *ApprovalWebhookHandler) HandleCasdoorApprovalWebhook(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	
	// Only accept POST
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	
	// Parse and verify webhook signature
	event, err := h.casdoorClient.HandleWebhook(r)
	if err != nil {
		slog.Error("webhook signature verification failed",
			"error", err,
			"remote_addr", r.RemoteAddr,
		)
		http.Error(w, "signature verification failed", http.StatusUnauthorized)
		return
	}
	
	slog.Info("received casdoor approval webhook",
		"event_id", event.EventID,
		"task_id", event.TaskID,
		"status", event.Status,
		"external_id", event.ExternalID,
		"actor", event.Actor,
	)
	
	// Process webhook event
	if err := h.casdoorProvider.HandleWebhook(ctx, event); err != nil {
		slog.Error("webhook processing failed",
			"error", err,
			"event_id", event.EventID,
			"task_id", event.TaskID,
		)
		http.Error(w, "processing failed", http.StatusInternalServerError)
		return
	}
	
	// Resume session if approved
	if event.Status == "approved" {
		if err := h.resumeSession(ctx, event.ExternalID); err != nil {
			slog.Error("session resume failed",
				"error", err,
				"external_id", event.ExternalID,
				"event_id", event.EventID,
			)
			// Don't fail the webhook - approval is already recorded
			// The session can be resumed manually if needed
		} else {
			slog.Info("session resumed successfully",
				"external_id", event.ExternalID,
				"event_id", event.EventID,
			)
		}
	}
	
	// Record metrics
	approval.RecordApprovalDecision("casdoor", event.Status, 0) // duration tracked by provider
	
	// Respond with success
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"ok":true}`))
	
	slog.Info("webhook processed successfully",
		"event_id", event.EventID,
		"status", event.Status,
	)
}

// resumeSession extracts session and request IDs from externalID and resumes the session.
func (h *ApprovalWebhookHandler) resumeSession(ctx context.Context, externalID string) error {
	// Parse externalID: "sessionId:requestId"
	sessionID, requestID, err := parseExternalID(externalID)
	if err != nil {
		return fmt.Errorf("parse externalId: %w", err)
	}
	
	slog.Info("resuming session after approval",
		"session_id", sessionID,
		"request_id", requestID,
	)
	
	// Call session resumer if available
	if h.sessionResumer != nil {
		if err := h.sessionResumer.ResumeSession(ctx, sessionID, requestID); err != nil {
			return fmt.Errorf("resume session: %w", err)
		}
	} else {
		slog.Warn("session resumer not configured, session not resumed",
			"session_id", sessionID,
			"request_id", requestID,
		)
	}
	
	return nil
}

// parseExternalID splits "sessionId:requestId" format.
func parseExternalID(externalID string) (sessionID, requestID string, err error) {
	// Find last colon to handle session IDs that might contain colons
	lastColon := strings.LastIndex(externalID, ":")
	if lastColon == -1 {
		return "", "", fmt.Errorf("invalid externalId format (missing colon): %s", externalID)
	}
	
	sessionID = externalID[:lastColon]
	requestID = externalID[lastColon+1:]
	
	if sessionID == "" || requestID == "" {
		return "", "", fmt.Errorf("invalid externalId format (empty part): %s", externalID)
	}
	
	return sessionID, requestID, nil
}
