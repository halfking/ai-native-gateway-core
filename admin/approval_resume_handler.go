package admin

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/kaixuan/llm-gateway-go/domains/session"
)

// HandleApprovalResume 处理 POST /api/admin/approvals/:id/resume
//
// 触发审批通过后的 LLM 调用恢复。
//
// 请求：POST /api/admin/approvals/{approval_id}/resume
// 响应：
//   - 200: {"status": "resumed", "approval_id": "..."}
//   - 400: {"error": "approval not in pending state"}
//   - 404: {"error": "approval not found"}
//   - 500: {"error": "..."}
//
// 认证：需要 super_admin 权限
func (h *Handler) HandleApprovalResume(w http.ResponseWriter, r *http.Request) {
	if h.approvalResumeHandler == nil {
		http.Error(w, `{"error":"approval resume not configured"}`, http.StatusServiceUnavailable)
		return
	}

	// 提取 approval_id
	approvalID := extractApprovalID(r)
	if approvalID == "" {
		http.Error(w, `{"error":"missing approval_id"}`, http.StatusBadRequest)
		return
	}

	// 获取 tenant_id（从认证上下文）
	tenantID := getTenantIDFromRequest(r)
	if tenantID == "" {
		http.Error(w, `{"error":"missing tenant_id"}`, http.StatusUnauthorized)
		return
	}

	// 调用 ResumeAfterApproval
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
		if errors.Is(err, session.ErrResumeSnapshotMissing) {
			slog.Error("approval resume failed: snapshot missing",
				"approval_id", approvalID,
				"error", err)
			http.Error(w, `{"error":"snapshot missing (cache expired?)"}`, http.StatusInternalServerError)
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
// 支持以下路径格式：
//   - /api/admin/approvals/{id}/resume
//   - /api/admin/approvals/:id/resume
func extractApprovalID(r *http.Request) string {
	// 尝试从路径参数提取（gorilla/mux 或类似的路由器）
	// 如果使用标准库 http.ServeMux，需要手动解析路径

	// 方式 1: 从 URL path 提取（假设路径格式为 /api/admin/approvals/{id}/resume）
	path := r.URL.Path
	// 移除前缀 /api/admin/approvals/
	prefix := "/api/admin/approvals/"
	if len(path) > len(prefix) {
		remaining := path[len(prefix):]
		// 提取到 /resume 之前的部分
		for i := 0; i < len(remaining); i++ {
			if remaining[i] == '/' {
				return remaining[:i]
			}
		}
		return remaining
	}

	// 方式 2: 从 query 参数提取（备用）
	if id := r.URL.Query().Get("id"); id != "" {
		return id
	}

	return ""
}

// getTenantIDFromRequest 从请求上下文中提取 tenant_id。
//
// 假设认证中间件已将 tenant_id 写入上下文或 header。
func getTenantIDFromRequest(r *http.Request) string {
	// 方式 1: 从上下文提取（如果有认证中间件）
	if tenantID, ok := r.Context().Value("tenant_id").(string); ok && tenantID != "" {
		return tenantID
	}

	// 方式 2: 从 header 提取
	if tenantID := r.Header.Get("X-Tenant-ID"); tenantID != "" {
		return tenantID
	}

	// 方式 3: 从 query 参数提取（不推荐，仅用于测试）
	if tenantID := r.URL.Query().Get("tenant_id"); tenantID != "" {
		return tenantID
	}

	return ""
}
