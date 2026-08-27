// Package api provides API handlers for the LLM Gateway.
//
// This file implements DingTalk callback handlers for approval notifications.
package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// dingTalkReplayTTL 是 P0-2 要求的事件去重 TTL（≥ 2 小时）。
const dingTalkReplayTTL = 2 * time.Hour

// DingTalkCallbackHandler handles DingTalk approval callbacks.
type DingTalkCallbackHandler struct {
	approvalManager ApprovalManager
	appSecret       string
	redisClient     *redis.Client // 可选；nil 表示跳过重放去重（开发环境）
	now             func() time.Time
}

// NewDingTalkCallbackHandler creates a new DingTalk callback handler.
func NewDingTalkCallbackHandler(manager ApprovalManager, appSecret string, redisClient *redis.Client) *DingTalkCallbackHandler {
	return &DingTalkCallbackHandler{
		approvalManager: manager,
		appSecret:       appSecret,
		redisClient:     redisClient,
		now:             time.Now,
	}
}

// DingTalkCallbackRequest represents the callback request from DingTalk.
type DingTalkCallbackRequest struct {
	// Event type: "approval_result"
	EventType string `json:"EventType"`

	// Timestamp (milliseconds since epoch, comes from the query string `timestamp`).
	// 解析回写到此处以便 handler 复用。
	TimeStamp int64 `json:"TimeStamp"`

	// EventID 是 P0-2 强制要求的事件唯一 ID；缺失时返 400 并写 `dingtalk.missing_event_id` 日志。
	EventID string `json:"event_id"`

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
//
// P0-2 修复要点：
//   1. HMAC 校验材料包含完整请求体（timestamp + "\n" + secret + "\n" + body）。
//   2. 签名比较改用 hmac.Equal（常量时间）。
//   3. 通过 Redis SETNX 持久化 event_id → HMAC(body)，TTL ≥ 2 小时。
//   4. 缺失 event_id 直接返 HTTP 400，并写 `dingtalk.missing_event_id` 日志。
func (h *DingTalkCallbackHandler) HandleApprovalCallback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Read body FIRST (limit to 1 MiB) so HMAC can cover the entire payload.
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		slog.Error("dingtalk callback: read body failed", "error", err)
		h.sendResponse(w, http.StatusBadRequest, 400, "Failed to read request body")
		return
	}

	// Verify signature (now includes body in HMAC material).
	if !h.verifySignature(r, body) {
		slog.Warn("dingtalk callback: invalid signature",
			"remote_addr", r.RemoteAddr,
			"user_agent", r.UserAgent())
		h.sendResponse(w, http.StatusUnauthorized, 401, "Invalid signature")
		return
	}

	// Parse callback request
	var req DingTalkCallbackRequest
	if err := json.Unmarshal(body, &req); err != nil {
		slog.Error("dingtalk callback: parse body failed", "error", err, "body_size", len(body))
		h.sendResponse(w, http.StatusBadRequest, 400, "Invalid request format")
		return
	}

	// P0-2: missing event_id → 400.
	if req.EventID == "" {
		slog.Warn("dingtalk.missing_event_id",
			"remote_addr", r.RemoteAddr,
			"approval_id", req.ApprovalID,
			"tenant_id", req.TenantID)
		h.sendResponse(w, http.StatusBadRequest, 400, "Missing event_id")
		return
	}

	// P0-2: replay dedup via Redis SETNX (event_id → HMAC(body)), TTL ≥ 2h.
	if h.redisClient != nil {
		digest := bodyHMAC(h.appSecret, body)
		key := "dingtalk:event:" + req.EventID
		ok, rerr := h.redisClient.SetNX(ctx, key, digest, dingTalkReplayTTL).Result()
		switch {
		case rerr != nil:
			// Redis 不可用 → fail-closed：避免重放绕过。
			slog.Error("dingtalk callback: replay dedup unavailable",
				"error", rerr,
				"event_id", req.EventID)
			h.sendResponse(w, http.StatusServiceUnavailable, 503, "Replay protection unavailable")
			return
		case !ok:
			// 已存在 → 重放。
			slog.Warn("dingtalk callback: replay rejected",
				"event_id", req.EventID,
				"approval_id", req.ApprovalID,
				"tenant_id", req.TenantID)
			h.sendResponse(w, http.StatusConflict, 409, "Duplicate event")
			return
		}
	}

	// Validate required business fields.
	if req.ApprovalID == "" || req.TenantID == "" || req.UserID == "" {
		slog.Warn("dingtalk callback: missing required fields",
			"approval_id", req.ApprovalID,
			"tenant_id", req.TenantID,
			"user_id", req.UserID)
		h.sendResponse(w, http.StatusBadRequest, 400, "Missing required fields")
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
		"result", req.Result,
		"event_id", req.EventID)
	h.sendResponse(w, http.StatusOK, 0, "success")
}

// bodyHMAC computes HMAC-SHA256(secret, body) and returns lowercase hex.
func bodyHMAC(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// verifySignature verifies the DingTalk callback signature.
//
// P0-2 修复：HMAC 校验材料现在包含完整请求体（timestamp + "\n" + secret + "\n" + body）；
// 签名比较改用 hmac.Equal（常量时间，避免侧信道时序攻击）。
func (h *DingTalkCallbackHandler) verifySignature(r *http.Request, body []byte) bool {
	// Get signature parameters from query string
	timestamp := r.URL.Query().Get("timestamp")
	sign := r.URL.Query().Get("sign")

	if timestamp == "" || sign == "" {
		return false
	}

	// Verify timestamp (within 1 hour)
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return false
	}

	nowMs := h.now().UnixMilli()
	if nowMs-ts > 3600000 || ts-nowMs > 3600000 {
		slog.Warn("dingtalk callback: timestamp out of range",
			"timestamp", ts,
			"now", nowMs,
			"diff_ms", nowMs-ts)
		return false
	}

	// Calculate expected signature: HMAC-SHA256(secret, timestamp + "\n" + secret + "\n" + body).
	// 注意：P0-2 前只用了 timestamp + "\n" + secret；现在要求把 body 也纳入 HMAC 输入，
	// 让篡改 body 的请求无法复用旧签名。
	stringToSign := timestamp + "\n" + h.appSecret + "\n" + string(body)
	mac := hmac.New(sha256.New, []byte(h.appSecret))
	mac.Write([]byte(stringToSign))
	expectedSign := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	// Constant-time comparison to avoid timing side-channels.
	return hmac.Equal([]byte(expectedSign), []byte(sign))
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
//
// P0-2：redisClient 用于 event_id 重放去重（SETNX event_id → HMAC(body)，TTL ≥ 2h）。
// 传入 nil 时跳过重放保护，仅用于本地开发 / 测试。
func RegisterDingTalkRoutes(mux *http.ServeMux, manager ApprovalManager, appSecret string, redisClient *redis.Client) {
	handler := NewDingTalkCallbackHandler(manager, appSecret, redisClient)
	mux.HandleFunc("POST /api/webhooks/dingtalk/approval-callback", handler.HandleApprovalCallback)
}
