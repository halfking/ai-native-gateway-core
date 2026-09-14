package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/domains/goalrun"
)

// goalrunKeyVerifier 与 domains/session 的鉴权抽象同形；真实现由 main.go
// 经适配器注入（hosted-task-delegation-design §0-F2：本 handler 曾直接信任
// X-Tenant-ID 头且 store 为 nil 会 panic，挂载 /v1/goal-runs 前必须补
// KeyVerifier 归属校验）。
type goalrunKeyVerifier interface {
	Enabled() bool
	Verify(ctx context.Context, rawKey string) (GoalRunKeyInfo, error)
}

// GoalRunKeyInfo 是验证后的 key 信息（导出以便 main.go 适配器实现接口）。
type GoalRunKeyInfo struct {
	ID       int
	TenantID string
}

// GoalRunHandler handles GoalRun status API requests.
type GoalRunHandler struct {
	store  *goalrun.Store
	logger *slog.Logger
	auth   goalrunKeyVerifier
}

// NewGoalRunHandler creates a new GoalRun handler with tenant/session ownership enforcement.
func NewGoalRunHandler(store *goalrun.Store, logger *slog.Logger) *GoalRunHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &GoalRunHandler{
		store:  store,
		logger: logger,
	}
}

// SetAuth 安装 KeyVerifier 抽象。未安装时请求 fail-closed（503）——
// tenant 必须来自验证后的 key，绝不信任请求头（§0-F2）。
func (h *GoalRunHandler) SetAuth(kv goalrunKeyVerifier) {
	h.auth = kv
}

// GoalRunStatusResponse represents the API response for goal run status.
type GoalRunStatusResponse struct {
	GoalRunID        string                 `json:"goal_run_id"`
	Status           string                 `json:"status"`
	TenantID         string                 `json:"tenant_id"`
	RootSessionID    string                 `json:"root_session_id"`
	CurrentSessionID string                 `json:"current_session_id,omitempty"`
	RootRequestID    string                 `json:"root_request_id"`
	LastRequestID    string                 `json:"last_request_id,omitempty"`
	PolicyVersion    int                    `json:"policy_version"`
	PolicySnapshot   map[string]interface{} `json:"policy_snapshot"`
	TurnCount        int                    `json:"turn_count"`
	FollowUpCount    int                    `json:"follow_up_count"`
	RetryCount       int                    `json:"retry_count"`
	TokensUsed       int64                  `json:"tokens_used"`
	TerminalReason   string                 `json:"terminal_reason,omitempty"`
	CreatedAt        time.Time              `json:"created_at"`
	UpdatedAt        time.Time              `json:"updated_at"`
	CompletedAt      *time.Time             `json:"completed_at,omitempty"`
	DeadlineAt       time.Time              `json:"deadline_at"`
}

// ErrorResponse represents an API error response.
type ErrorResponse struct {
	Error   string `json:"error"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// ServeHTTP handles GET /v1/goal-runs/{id} requests.
// Enforces tenant/session ownership and fail-closed on auth failures.
func (h *GoalRunHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		h.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "only GET is supported")
		return
	}

	// Extract goal_run_id from URL path: /v1/goal-runs/{id}
	goalRunID := h.extractGoalRunID(r.URL.Path)
	if goalRunID == "" {
		h.writeError(w, http.StatusBadRequest, "invalid_request", "goal_run_id required")
		return
	}

	// §0-F2 修复：tenant 一律取自 KeyVerifier 验证结果；verifier 未配置
	// 或 store 未配置均 fail-closed 503（此前直接信任 X-Tenant-ID 头，
	// store 为 nil 时 panic）。
	if h.store == nil {
		h.writeError(w, http.StatusServiceUnavailable, "store_unavailable", "goal run store not configured")
		return
	}
	if h.auth == nil || !h.auth.Enabled() {
		h.writeError(w, http.StatusServiceUnavailable, "auth_unavailable", "authentication not configured")
		return
	}
	ki, err := h.verifyKey(r)
	if err != nil {
		if _, ok := err.(*authentication.InvalidKeyError); ok {
			// 必须断言跨包类型（session handler.go:122 本地副本陷阱同源）。
			h.writeError(w, http.StatusUnauthorized, "unauthorized", "invalid or expired API key")
			return
		}
		h.writeError(w, http.StatusServiceUnavailable, "auth_unavailable", "authentication temporarily unavailable")
		return
	}
	tenantID := ki.TenantID

	sessionID := r.Header.Get("X-Session-ID")

	ctx := r.Context()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// Fetch GoalRun from repository
	run, err := h.store.GetGoalRun(ctx, goalRunID)
	if err != nil {
		if err == goalrun.ErrGoalRunNotFound {
			h.writeError(w, http.StatusNotFound, "not_found", "goal run not found")
			return
		}
		h.logger.ErrorContext(ctx, "failed to fetch goal run",
			slog.String("goal_run_id", goalRunID),
			slog.String("error", err.Error()))
		h.writeError(w, http.StatusInternalServerError, "internal_error", "failed to fetch goal run")
		return
	}

	// Enforce tenant ownership - fail-closed
	if run.TenantID != tenantID {
		h.logger.WarnContext(ctx, "tenant mismatch",
			slog.String("goal_run_id", goalRunID),
			slog.String("run_tenant_id", run.TenantID))
		h.writeError(w, http.StatusForbidden, "forbidden", "access denied")
		return
	}

	// Enforce session ownership if session_id provided
	if sessionID != "" && run.RootSessionID != sessionID && run.CurrentSessionID != sessionID {
		h.logger.WarnContext(ctx, "session mismatch",
			slog.String("goal_run_id", goalRunID),
			slog.String("request_session_id", sessionID),
			slog.String("root_session_id", run.RootSessionID),
			slog.String("current_session_id", run.CurrentSessionID))
		h.writeError(w, http.StatusForbidden, "forbidden", "session access denied")
		return
	}

	// Build response
	response := h.buildStatusResponse(run)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(response); err != nil {
		h.logger.ErrorContext(ctx, "failed to encode response",
			slog.String("goal_run_id", goalRunID),
			slog.String("error", err.Error()))
	}
}

// verifyKey 复用 Bearer/x-api-key 提取并校验 sk-* key。
func (h *GoalRunHandler) verifyKey(r *http.Request) (GoalRunKeyInfo, error) {
	rawKey := r.Header.Get("Authorization")
	switch {
	case strings.HasPrefix(rawKey, "Bearer "):
		rawKey = strings.TrimPrefix(rawKey, "Bearer ")
	case strings.HasPrefix(rawKey, "bearer "):
		rawKey = strings.TrimPrefix(rawKey, "bearer ")
	default:
		rawKey = ""
	}
	if rawKey == "" {
		rawKey = r.Header.Get("x-api-key")
	}
	if rawKey == "" {
		return GoalRunKeyInfo{}, &authentication.InvalidKeyError{Message: "missing key"}
	}
	return h.auth.Verify(r.Context(), rawKey)
}

// extractGoalRunID extracts goal_run_id from path like /v1/goal-runs/{id}
func (h *GoalRunHandler) extractGoalRunID(path string) string {
	// Path: /v1/goal-runs/{id}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) >= 3 && parts[0] == "v1" && parts[1] == "goal-runs" {
		return parts[2]
	}
	return ""
}

// buildStatusResponse converts GoalRun to API response format.
func (h *GoalRunHandler) buildStatusResponse(run *goalrun.GoalRun) GoalRunStatusResponse {
	var policySnapshot map[string]interface{}
	if len(run.PolicySnapshot) > 0 {
		// PolicySnapshot is stored as JSON bytes
		_ = json.Unmarshal(run.PolicySnapshot, &policySnapshot)
	}

	resp := GoalRunStatusResponse{
		GoalRunID:        run.ID,
		Status:           string(run.Status),
		TenantID:         run.TenantID,
		RootSessionID:    run.RootSessionID,
		CurrentSessionID: run.CurrentSessionID,
		RootRequestID:    run.RootRequestID,
		LastRequestID:    run.LastRequestID,
		PolicyVersion:    run.PolicyVersion,
		PolicySnapshot:   policySnapshot,
		TurnCount:        run.TurnCount,
		FollowUpCount:    run.FollowUpCount,
		RetryCount:       run.RetryCount,
		TokensUsed:       run.TokensUsed,
		TerminalReason:   run.TerminalReason,
		CreatedAt:        run.CreatedAt,
		UpdatedAt:        run.UpdatedAt,
		DeadlineAt:       run.DeadlineAt,
	}

	if !run.CompletedAt.IsZero() {
		resp.CompletedAt = &run.CompletedAt
	}

	return resp
}

// writeError writes a JSON error response.
func (h *GoalRunHandler) writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(ErrorResponse{
		Error:   code,
		Code:    code,
		Message: message,
	})
}
