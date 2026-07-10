// Package api provides API handlers for the LLM Gateway.
//
// This file implements DingTalk callback handlers for approval notifications.
package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	maxDingTalkCallbackBodyBytes = 1 << 20
	dingTalkCallbackMaxAge       = 10 * time.Minute
)

// DingTalkCallbackHandler handles DingTalk approval callbacks.
type DingTalkCallbackHandler struct {
	approvalManager ApprovalManager
	secret          func() string
	allowUser       func(string) bool
}

// NewDingTalkCallbackHandler creates a new DingTalk callback handler.
// An empty allowedUsers list permits every signed callback for backward compatibility.
func NewDingTalkCallbackHandler(manager ApprovalManager, appSecret string, allowedUsers []string) *DingTalkCallbackHandler {
	allowed := make(map[string]struct{}, len(allowedUsers))
	for _, userID := range allowedUsers {
		if userID = strings.TrimSpace(userID); userID != "" {
			allowed[userID] = struct{}{}
		}
	}
	return newDingTalkCallbackHandler(manager, func() string { return appSecret }, func(userID string) bool {
		if len(allowed) == 0 {
			return true
		}
		_, ok := allowed[userID]
		return ok
	})
}

func newDingTalkCallbackHandler(manager ApprovalManager, secret func() string, allowUser func(string) bool) *DingTalkCallbackHandler {
	return &DingTalkCallbackHandler{approvalManager: manager, secret: secret, allowUser: allowUser}
}

// DingTalkCallbackRequest represents the callback request from DingTalk.
type DingTalkCallbackRequest struct {
	// Event type: "approval_result"
	EventType string `json:"EventType"`

	// Timestamp
	TimeStamp int64 `json:"TimeStamp"`

	// Approval result data
	ApprovalID string `json:"approval_id"`
	TenantID   string `json:"tenant_id"`
	UserID     string `json:"user_id"`
	Result     string `json:"result"` // "agree" or "refuse"
	Comment    string `json:"comment"`
}

// DingTalkCallbackResponse represents the response to DingTalk.
type DingTalkCallbackResponse struct {
	ErrCode int    `json:"errcode"`
	ErrMsg  string `json:"errmsg"`
}

// HandleApprovalCallback handles DingTalk approval callback.
// POST /api/webhooks/dingtalk/approval-callback
func (h *DingTalkCallbackHandler) HandleApprovalCallback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Verify signature
	if !h.verifySignature(r) {
		slog.Warn("dingtalk callback: invalid signature",
			"remote_addr", r.RemoteAddr,
			"user_agent", r.UserAgent())
		h.sendResponse(w, http.StatusUnauthorized, 401, "Invalid signature")
		return
	}

	// Read request body (limit to 1MB)
	r.Body = http.MaxBytesReader(w, r.Body, maxDingTalkCallbackBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		slog.Error("dingtalk callback: read body failed", "error", err)
		h.sendResponse(w, http.StatusBadRequest, 400, "Failed to read request body")
		return
	}

	// Parse callback request
	var req DingTalkCallbackRequest
	if err := json.Unmarshal(body, &req); err != nil {
		slog.Error("dingtalk callback: parse body failed", "error", err, "body_size", len(body))
		h.sendResponse(w, http.StatusBadRequest, 400, "Invalid request format")
		return
	}

	// Validate request
	if req.ApprovalID == "" || req.TenantID == "" || req.UserID == "" {
		slog.Warn("dingtalk callback: missing required fields",
			"approval_id", req.ApprovalID,
			"tenant_id", req.TenantID,
			"user_id", req.UserID)
		h.sendResponse(w, http.StatusBadRequest, 400, "Missing required fields")
		return
	}
	if !h.isAllowed(req.UserID) {
		slog.Warn("dingtalk callback: user rejected by allowlist", "user_id", req.UserID)
		h.sendResponse(w, http.StatusForbidden, http.StatusForbidden, "User is not allowed")
		return
	}

	// Process the approval result
	if err := h.processApprovalResult(ctx, &req); err != nil {
		slog.Error("dingtalk callback: process approval failed",
			"error", err,
			"approval_id", req.ApprovalID,
			"tenant_id", req.TenantID,
			"user_id", req.UserID,
			"result", req.Result)
		h.sendResponse(w, http.StatusInternalServerError, 500, "Failed to process approval")
		return
	}

	// Success response
	slog.Info("dingtalk callback: approval processed",
		"approval_id", req.ApprovalID,
		"tenant_id", req.TenantID,
		"user_id", req.UserID,
		"result", req.Result)
	h.sendResponse(w, http.StatusOK, 0, "success")
}

// verifySignature verifies the DingTalk callback signature.
func (h *DingTalkCallbackHandler) verifySignature(r *http.Request) bool {
	// Get signature parameters from query string
	timestamp := r.URL.Query().Get("timestamp")
	sign := r.URL.Query().Get("sign")

	if timestamp == "" || sign == "" {
		return false
	}

	// Verify timestamp within the replay-protection window.
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return false
	}

	now := time.Now().Unix() * 1000 // DingTalk uses milliseconds
	maxAgeMillis := dingTalkCallbackMaxAge.Milliseconds()
	if now-ts > maxAgeMillis || ts-now > maxAgeMillis {
		slog.Warn("dingtalk callback: timestamp out of range",
			"timestamp", ts,
			"now", now,
			"diff_ms", now-ts)
		return false
	}

	// Calculate expected signature
	// DingTalk signature: HMAC-SHA256(timestamp + "\n" + app_secret)
	secret := h.secret()
	if secret == "" {
		return false
	}
	stringToSign := timestamp + "\n" + secret
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(stringToSign))
	expectedSign := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	// Note: query parameters are automatically URL-decoded by Go's HTTP server,
	// so we compare the base64-encoded signature directly without URL encoding.
	return hmac.Equal([]byte(sign), []byte(expectedSign))
}

func (h *DingTalkCallbackHandler) isAllowed(userID string) bool {
	if h.allowUser == nil {
		return true
	}
	return h.allowUser(userID)
}

// processApprovalResult processes the approval result from DingTalk.
func (h *DingTalkCallbackHandler) processApprovalResult(ctx context.Context, req *DingTalkCallbackRequest) error {
	switch req.Result {
	case "agree":
		// Approve the request
		return h.approvalManager.Approve(
			ctx,
			req.ApprovalID,
			req.TenantID,
			req.UserID,
			req.Comment,
		)

	case "refuse":
		// Reject the request
		return h.approvalManager.Reject(
			ctx,
			req.ApprovalID,
			req.TenantID,
			req.UserID,
			req.Comment,
		)

	default:
		return fmt.Errorf("unknown approval result: %s", req.Result)
	}
}

// sendResponse sends a JSON response to DingTalk.
func (h *DingTalkCallbackHandler) sendResponse(w http.ResponseWriter, httpStatus, errCode int, errMsg string) {
	resp := DingTalkCallbackResponse{
		ErrCode: errCode,
		ErrMsg:  errMsg,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(httpStatus)
	json.NewEncoder(w).Encode(resp)
}

// RegisterDingTalkRoutes registers DingTalk callback routes.
func RegisterDingTalkRoutes(mux *http.ServeMux, manager ApprovalManager, secret func() string, isAllowed func(string) bool) {
	handler := newDingTalkCallbackHandler(manager, secret, isAllowed)
	mux.HandleFunc("POST /api/webhooks/dingtalk/approval-callback", handler.HandleApprovalCallback)
}
