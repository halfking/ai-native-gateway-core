package admin

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/kaixuan/llm-gateway-go/domains/session"
	"github.com/kaixuan/llm-gateway-go/domains/sessionaudit"
)

// HandleApprovalResume 处理 POST /api/v1/approvals/{id}/resume。
//
// 触发审批通过后的 LLM 调用恢复。该操作只允许 super_admin 或 legacy
// admin_key；全局管理员的空 tenant_id 由 ApprovalCallerTenantID 保留为
// 跨租户语义，不能由客户端 header 或 query 参数指定。
func (h *Handler) HandleApprovalResume(w http.ResponseWriter, r *http.Request) {
	if h.approvalResumeHandler == nil {
		http.Error(w, `{"error":"approval resume not configured"}`, http.StatusServiceUnavailable)
		return
	}

	if GetAuthContext(r) == nil {
		http.Error(w, `{"error":"authentication required"}`, http.StatusUnauthorized)
		return
	}
	if !IsSuperAdminOrLegacy(r) {
		http.Error(w, `{"error":"super_admin role required for this endpoint"}`, http.StatusForbidden)
		return
	}

	approvalID := extractApprovalID(r)
	if approvalID == "" {
		http.Error(w, `{"error":"missing approval_id"}`, http.StatusBadRequest)
		return
	}

	tenantID := ApprovalCallerTenantID(r)
	ctx := r.Context()
	slog.Info("approval resume requested",
		"approval_id", approvalID,
		"tenant_id", tenantID,
		"remote_addr", r.RemoteAddr)

	err := h.approvalResumeHandler.ResumeAfterApproval(ctx, approvalID, tenantID)
	if err != nil {
		// 错误分类
		if errors.Is(err, session.ErrResumeNotPending) {
			slog.Warn("approval resume failed: not pending",
				"approval_id", approvalID,
				"error", err)
			http.Error(w, `{"error":"approval not in pending state"}`, http.StatusBadRequest)
			return
		}
		if errors.Is(err, session.ErrResumeInProgress) {
			slog.Warn("approval resume already in progress",
				"approval_id", approvalID,
				"error", err)
			http.Error(w, `{"error":"approval resume already in progress"}`, http.StatusConflict)
			return
		}
		if errors.Is(err, session.ErrResumeLeaseLost) {
			slog.Warn("approval resume lease lost",
				"approval_id", approvalID,
				"error", err)
			http.Error(w, `{"error":"approval resume lease lost; retry"}`, http.StatusConflict)
			return
		}
		if errors.Is(err, sessionaudit.ErrResumeLeaseLost) {
			slog.Warn("approval resume persistence lease lost",
				"approval_id", approvalID,
				"error", err)
			http.Error(w, `{"error":"approval resume lease lost; retry"}`, http.StatusConflict)
			return
		}

		if errors.Is(err, session.ErrResumeRejected) {
			slog.Warn("approval resume failed: rejected",
				"approval_id", approvalID,
				"error", err)
			http.Error(w, `{"error":"approval was rejected"}`, http.StatusBadRequest)
			return
		}
		if errors.Is(err, session.ErrResumeTimeout) {
			slog.Warn("approval resume failed: timeout",
				"approval_id", approvalID,
				"error", err)
			http.Error(w, `{"error":"approval timed out"}`, http.StatusBadRequest)
			return
		}

		// 通用错误
		slog.Error("approval resume failed",
			"approval_id", approvalID,
			"tenant_id", tenantID,
			"error", err)
		http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
		return
	}

	// 成功
	slog.Info("approval resumed successfully",
		"approval_id", approvalID,
		"tenant_id", tenantID)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	resp := map[string]string{
		"status":      "resumed",
		"approval_id": approvalID,
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// ──────────────────────────────────────────────────────────────────────────────
// 辅助函数
// ──────────────────────────────────────────────────────────────────────────────

// extractApprovalID 从请求路径中提取 approval_id。
//
// 支持当前 API 路径和历史 admin 路径；query 中的 id 仅作为兼容回退。
func extractApprovalID(r *http.Request) string {
	for _, prefix := range []string{
		"/api/v1/approvals/",
		"/api/admin/approvals/",
	} {
		remaining, ok := strings.CutPrefix(r.URL.Path, prefix)
		if !ok || remaining == "" {
			continue
		}
		id, ok := strings.CutSuffix(remaining, "/resume")
		if !ok || id == "" || strings.Contains(id, "/") {
			continue
		}
		return id
	}

	return r.URL.Query().Get("id")
}
