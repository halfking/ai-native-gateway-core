// Package webhooks provides webhook handlers for external service callbacks.
//
// This package contains handlers for processing callbacks from notification
// channels like Feishu, WeChat Work, and DingTalk.
package webhooks

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"
)

// FeishuCallbackHandler handles Feishu approval callbacks.
type FeishuCallbackHandler struct {
	manager     ApprovalManager
	verifyToken string // Feishu app verification token
	encryptKey  string // Feishu app encrypt key (optional)
}

// FeishuCallbackConfig contains configuration for Feishu callback handler.
type FeishuCallbackConfig struct {
	Manager     ApprovalManager
	VerifyToken string
	EncryptKey  string
}

// NewFeishuCallbackHandler creates a new Feishu callback handler.
func NewFeishuCallbackHandler(config FeishuCallbackConfig) *FeishuCallbackHandler {
	return &FeishuCallbackHandler{
		manager:     config.Manager,
		verifyToken: config.VerifyToken,
		encryptKey:  config.EncryptKey,
	}
}

// HandleCallback handles POST /api/webhooks/feishu/approval-callback
//
// This endpoint receives callbacks from Feishu when users click on approval buttons.
// It supports both URL verification challenges and actual approval actions.
func (h *FeishuCallbackHandler) HandleCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		h.writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Read request body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		slog.Error("failed to read callback body", "error", err)
		h.writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	defer func() { _ = r.Body.Close() }()

	// Parse callback payload
	var callback FeishuCallback
	if err := json.Unmarshal(body, &callback); err != nil {
		slog.Error("failed to parse callback body", "error", err, "body", string(body))
		h.writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	// Handle URL verification challenge
	if callback.Type == "url_verification" {
		h.handleURLVerification(w, &callback)
		return
	}

	// Verify signature if configured
	if h.verifyToken != "" {
		if !h.verifySignature(r, body) {
			slog.Warn("invalid signature", "remote_addr", r.RemoteAddr)
			h.writeError(w, http.StatusUnauthorized, "invalid signature")
			return
		}
	}

	// Handle different event types
	switch callback.Type {
	case "event_callback":
		h.handleEventCallback(w, r, &callback)
	default:
		slog.Warn("unknown callback type", "type", callback.Type)
		h.writeJSON(w, http.StatusOK, map[string]string{"status": "ignored"})
	}
}

// handleURLVerification handles Feishu URL verification challenge.
func (h *FeishuCallbackHandler) handleURLVerification(w http.ResponseWriter, callback *FeishuCallback) {
	response := map[string]string{
		"challenge": callback.Challenge,
	}
	h.writeJSON(w, http.StatusOK, response)
	slog.Info("handled URL verification", "challenge", callback.Challenge)
}

// handleEventCallback processes actual button click events from Feishu.
func (h *FeishuCallbackHandler) handleEventCallback(w http.ResponseWriter, r *http.Request, callback *FeishuCallback) {
	// Extract action and request_id from query parameters (set in button URL)
	query := r.URL.Query()
	action := query.Get("action")
	requestID := query.Get("request_id")

	if requestID == "" {
		slog.Error("missing request_id in callback")
		h.writeError(w, http.StatusBadRequest, "missing request_id")
		return
	}

	if action != "approve" && action != "reject" {
		slog.Error("invalid action", "action", action)
		h.writeError(w, http.StatusBadRequest, "invalid action")
		return
	}

	// Extract user info from callback
	var userID string
	var userName string

	if callback.Event != nil {
		userID = callback.Event.UserID
		// Try to get user open_id as well
		if userID == "" && callback.Event.Sender != nil {
			userID = callback.Event.Sender.OpenID
			if userID == "" {
				userID = callback.Event.Sender.UserID
			}
		}
	}

	if userID == "" {
		userID = "feishu_user" // Fallback
	}

	// Get tenant ID for this approval request
	tenantID, err := h.manager.GetApprovalByRequestID(r.Context(), requestID)
	if err != nil {
		slog.Error("failed to get approval tenant", "request_id", requestID, "error", err)
		h.writeError(w, http.StatusNotFound, "approval not found")
		return
	}

	// Execute action
	ctx := r.Context()
	switch action {
	case "approve":
		reason := fmt.Sprintf("Approved via Feishu by %s", userName)
		if userName == "" {
			reason = fmt.Sprintf("Approved via Feishu (user: %s)", userID)
		}

		err = h.manager.Approve(ctx, requestID, tenantID, userID, reason)
		if err != nil {
			slog.Error("failed to approve", "request_id", requestID, "error", err)
			h.writeError(w, http.StatusInternalServerError, "failed to approve")
			return
		}

		slog.Info("approval approved via Feishu", "request_id", requestID, "user_id", userID)
		h.writeJSON(w, http.StatusOK, map[string]interface{}{
			"success": true,
			"message": "Approval granted successfully",
			"action":  "approved",
		})

	case "reject":
		reason := fmt.Sprintf("Rejected via Feishu by %s", userName)
		if userName == "" {
			reason = fmt.Sprintf("Rejected via Feishu (user: %s)", userID)
		}

		err = h.manager.Reject(ctx, requestID, tenantID, userID, reason)
		if err != nil {
			slog.Error("failed to reject", "request_id", requestID, "error", err)
			h.writeError(w, http.StatusInternalServerError, "failed to reject")
			return
		}

		slog.Info("approval rejected via Feishu", "request_id", requestID, "user_id", userID)
		h.writeJSON(w, http.StatusOK, map[string]interface{}{
			"success": true,
			"message": "Approval rejected",
			"action":  "rejected",
		})
	}
}

// verifySignature verifies the Feishu request signature.
//
// Feishu uses different signature methods depending on the API version.
// This implementation supports the common timestamp + nonce + body signature.
func (h *FeishuCallbackHandler) verifySignature(r *http.Request, body []byte) bool {
	// Get signature from header
	signature := r.Header.Get("X-Lark-Signature")
	if signature == "" {
		// Try alternative header name
		signature = r.Header.Get("X-Feishu-Signature")
	}

	if signature == "" {
		slog.Warn("missing signature header")
		return false
	}

	// Get timestamp and nonce
	timestamp := r.Header.Get("X-Lark-Request-Timestamp")
	nonce := r.Header.Get("X-Lark-Request-Nonce")

	if timestamp == "" || nonce == "" {
		slog.Warn("missing timestamp or nonce header")
		return false
	}

	// Verify timestamp is recent (within 5 minutes)
	unixSeconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		slog.Warn("invalid timestamp", "timestamp", timestamp)
		return false
	}
	if skew := time.Since(time.Unix(unixSeconds, 0)); skew > 5*time.Minute || skew < -5*time.Minute {
		slog.Warn("timestamp too old", "timestamp", timestamp)
		return false
	}

	// Feishu signs timestamp + nonce + encrypt key + body. Fall back to the
	// verification token for existing deployments that have not set an encrypt key.
	signingKey := h.encryptKey
	if signingKey == "" {
		signingKey = h.verifyToken
	}
	data := timestamp + nonce + signingKey + string(body)
	hash := sha256.Sum256([]byte(data))
	expected := hex.EncodeToString(hash[:])

	if subtle.ConstantTimeCompare([]byte(signature), []byte(expected)) != 1 {
		slog.Warn("signature mismatch")
		return false
	}

	return true
}

// Helper methods

func (h *FeishuCallbackHandler) writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		slog.Error("failed to encode JSON response", "error", err)
	}
}

func (h *FeishuCallbackHandler) writeError(w http.ResponseWriter, status int, message string) {
	h.writeJSON(w, status, map[string]string{"error": message})
}

// Feishu callback data structures

// FeishuCallback represents the structure of a Feishu event callback.
type FeishuCallback struct {
	Type      string       `json:"type"`      // url_verification, event_callback
	Challenge string       `json:"challenge"` // For URL verification
	Token     string       `json:"token"`     // App verification token
	Event     *FeishuEvent `json:"event"`     // Event data
}

// FeishuEvent represents the event data in a callback.
type FeishuEvent struct {
	Type   string        `json:"type"`    // card.action.trigger, etc.
	UserID string        `json:"user_id"` // User who triggered the action
	OpenID string        `json:"open_id"` // Alternative user ID
	Action *FeishuAction `json:"action"`  // Action details
	Sender *FeishuSender `json:"sender"`  // Sender information
	Token  string        `json:"token"`   // Event token
}

// FeishuAction represents button action details.
type FeishuAction struct {
	Value map[string]string `json:"value"` // Button value data
	Tag   string            `json:"tag"`   // Action tag
}

// FeishuSender represents sender information.
type FeishuSender struct {
	SenderID  string `json:"sender_id"`
	OpenID    string `json:"open_id"`
	UserID    string `json:"user_id"`
	TenantKey string `json:"tenant_key"`
}
