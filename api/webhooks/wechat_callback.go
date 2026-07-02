// Package webhooks provides webhook handlers for external integrations.
//
// This package handles callbacks from third-party services like WeChat Work,
// Feishu, DingTalk, etc. when users interact with approval notifications.
package webhooks

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
)

// WeChatCallbackHandler handles WeChat Work approval callbacks.
//
// WeChat Work sends callbacks in two scenarios:
//  1. Verification request (GET) - for URL verification
//  2. Event notification (POST) - for user interactions and approval actions
type WeChatCallbackHandler struct {
	manager ApprovalManager
	token   string // Callback verification token
	aesKey  string // AES encryption key (optional)
}

// NewWeChatCallbackHandler creates a new WeChat callback handler.
func NewWeChatCallbackHandler(manager ApprovalManager, token, aesKey string) *WeChatCallbackHandler {
	return &WeChatCallbackHandler{
		manager: manager,
		token:   token,
		aesKey:  aesKey,
	}
}

// HandleCallback handles POST /api/webhooks/wechat/approval-callback
//
// This endpoint processes WeChat Work callbacks including:
//   - URL verification (GET request)
//   - Approval actions (POST request with JSON/XML payload)
func (h *WeChatCallbackHandler) HandleCallback(w http.ResponseWriter, r *http.Request) {
	// Handle URL verification (GET request)
	if r.Method == http.MethodGet {
		h.handleVerification(w, r)
		return
	}

	// Handle event notifications (POST request)
	if r.Method == http.MethodPost {
		h.handleEvent(w, r)
		return
	}

	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

// handleVerification handles WeChat Work URL verification request.
//
// WeChat sends: GET /callback?msg_signature=xxx&timestamp=xxx&nonce=xxx&echostr=xxx
// We should:
//  1. Verify signature
//  2. Decrypt echostr
//  3. Return decrypted echostr
func (h *WeChatCallbackHandler) handleVerification(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	msgSignature := query.Get("msg_signature")
	timestamp := query.Get("timestamp")
	nonce := query.Get("nonce")
	echoStr := query.Get("echostr")

	// Verify signature
	if !h.verifySignature(h.token, timestamp, nonce, echoStr, msgSignature) {
		slog.Error("WeChat callback verification failed: invalid signature")
		http.Error(w, "signature verification failed", http.StatusUnauthorized)
		return
	}

	// For simple mode (no encryption), return echostr directly
	// For encryption mode, decrypt echostr first
	// TODO: Implement AES decryption if h.aesKey is set
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(echoStr))
}

// handleEvent handles WeChat Work event notifications.
//
// Event payload format (XML):
//
//	<xml>
//	  <ToUserName><![CDATA[corp_id]]></ToUserName>
//	  <FromUserName><![CDATA[user_id]]></FromUserName>
//	  <CreateTime>1234567890</CreateTime>
//	  <MsgType><![CDATA[event]]></MsgType>
//	  <Event><![CDATA[click]]></Event>
//	  <EventKey><![CDATA[approval_action]]></EventKey>
//	  <AgentID>1000001</AgentID>
//	</xml>
//
// Or JSON format:
//
//	{
//	  "action": "approve",
//	  "approval_id": "req-xxx",
//	  "tenant_id": "tenant-123",
//	  "user_id": "user-456",
//	  "reason": "approved by manager"
//	}
func (h *WeChatCallbackHandler) handleEvent(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		slog.Error("failed to read callback body", "error", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	// Try JSON format first
	if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		h.handleJSONEvent(w, r, body)
		return
	}

	// Try XML format
	h.handleXMLEvent(w, r, body)
}

// handleJSONEvent handles JSON-formatted callback events.
func (h *WeChatCallbackHandler) handleJSONEvent(w http.ResponseWriter, r *http.Request, body []byte) {
	var event struct {
		Action     string `json:"action"`      // "approve" or "reject"
		ApprovalID string `json:"approval_id"` // Approval request ID
		TenantID   string `json:"tenant_id"`   // Tenant ID
		UserID     string `json:"user_id"`     // User who performed the action
		Reason     string `json:"reason"`      // Optional reason
	}

	if err := json.Unmarshal(body, &event); err != nil {
		slog.Error("failed to parse JSON callback", "error", err)
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	// Validate required fields
	if event.ApprovalID == "" || event.TenantID == "" || event.UserID == "" {
		slog.Error("missing required fields in callback",
			"approval_id", event.ApprovalID,
			"tenant_id", event.TenantID,
			"user_id", event.UserID)
		http.Error(w, "missing required fields", http.StatusBadRequest)
		return
	}

	// Process approval action
	ctx := r.Context()
	var err error

	switch event.Action {
	case "approve":
		err = h.manager.Approve(ctx, event.ApprovalID, event.TenantID, event.UserID, event.Reason)
		if err != nil {
			slog.Error("failed to approve via callback",
				"approval_id", event.ApprovalID,
				"user_id", event.UserID,
				"error", err)
			http.Error(w, "failed to approve", http.StatusInternalServerError)
			return
		}
		slog.Info("approval approved via WeChat callback",
			"approval_id", event.ApprovalID,
			"user_id", event.UserID)

	case "reject":
		if event.Reason == "" {
			event.Reason = "rejected via WeChat"
		}
		err = h.manager.Reject(ctx, event.ApprovalID, event.TenantID, event.UserID, event.Reason)
		if err != nil {
			slog.Error("failed to reject via callback",
				"approval_id", event.ApprovalID,
				"user_id", event.UserID,
				"error", err)
			http.Error(w, "failed to reject", http.StatusInternalServerError)
			return
		}
		slog.Info("approval rejected via WeChat callback",
			"approval_id", event.ApprovalID,
			"user_id", event.UserID)

	default:
		slog.Error("unknown callback action", "action", event.Action)
		http.Error(w, "unknown action", http.StatusBadRequest)
		return
	}

	// Return success response
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"message": fmt.Sprintf("approval %s successfully", event.Action),
	})
}

// handleXMLEvent handles XML-formatted callback events.
func (h *WeChatCallbackHandler) handleXMLEvent(w http.ResponseWriter, r *http.Request, body []byte) {
	var event struct {
		XMLName      xml.Name `xml:"xml"`
		ToUserName   string   `xml:"ToUserName"`
		FromUserName string   `xml:"FromUserName"`
		CreateTime   int64    `xml:"CreateTime"`
		MsgType      string   `xml:"MsgType"`
		Event        string   `xml:"Event"`
		EventKey     string   `xml:"EventKey"`
		AgentID      int      `xml:"AgentID"`
	}

	if err := xml.Unmarshal(body, &event); err != nil {
		slog.Error("failed to parse XML callback", "error", err)
		http.Error(w, "invalid XML", http.StatusBadRequest)
		return
	}

	// Log the event
	slog.Info("received WeChat XML event",
		"msg_type", event.MsgType,
		"event", event.Event,
		"event_key", event.EventKey,
		"from_user", event.FromUserName)

	// Parse EventKey to extract approval action
	// Format: "approval:approve:req-xxx:tenant-123"
	parts := strings.Split(event.EventKey, ":")
	if len(parts) < 4 || parts[0] != "approval" {
		slog.Warn("invalid EventKey format", "event_key", event.EventKey)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("success"))
		return
	}

	action := parts[1]        // "approve" or "reject"
	approvalID := parts[2]    // "req-xxx"
	tenantID := parts[3]      // "tenant-123"
	userID := event.FromUserName

	// Process approval action
	ctx := r.Context()
	var err error

	switch action {
	case "approve":
		err = h.manager.Approve(ctx, approvalID, tenantID, userID, "approved via WeChat")
		if err != nil {
			slog.Error("failed to approve via XML callback",
				"approval_id", approvalID,
				"user_id", userID,
				"error", err)
		} else {
			slog.Info("approval approved via WeChat XML callback",
				"approval_id", approvalID,
				"user_id", userID)
		}

	case "reject":
		err = h.manager.Reject(ctx, approvalID, tenantID, userID, "rejected via WeChat")
		if err != nil {
			slog.Error("failed to reject via XML callback",
				"approval_id", approvalID,
				"user_id", userID,
				"error", err)
		} else {
			slog.Info("approval rejected via WeChat XML callback",
				"approval_id", approvalID,
				"user_id", userID)
		}

	default:
		slog.Error("unknown callback action in XML", "action", action)
	}

	// WeChat expects "success" response for XML events
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("success"))
}

// verifySignature verifies WeChat Work callback signature.
//
// Algorithm:
//  1. Sort token, timestamp, nonce, echostr lexicographically
//  2. Concatenate them
//  3. Calculate SHA1 hash
//  4. Compare with msg_signature
func (h *WeChatCallbackHandler) verifySignature(token, timestamp, nonce, echoStr, signature string) bool {
	// Sort parameters
	params := []string{token, timestamp, nonce, echoStr}
	sort.Strings(params)

	// Concatenate
	raw := strings.Join(params, "")

	// Calculate SHA1
	hash := sha1.Sum([]byte(raw))
	calculated := hex.EncodeToString(hash[:])

	// Compare
	return calculated == signature
}

// writeJSON writes a JSON response.
func (h *WeChatCallbackHandler) writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		slog.Error("failed to encode JSON response", "error", err)
	}
}
