package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/sessionaudit"
)

// ApprovalListRequest 审批列表请求
type ApprovalListRequest struct {
	TenantID string `json:"tenant_id"`
	Status   string `json:"status"` // pending/approved/rejected/timeout
	Limit    int    `json:"limit"`
	Offset   int    `json:"offset"`
}

// ApprovalListResponse 审批列表响应
type ApprovalListResponse struct {
	Approvals []*ApprovalItem `json:"approvals"`
	Total     int             `json:"total"`
	Limit     int             `json:"limit"`
	Offset    int             `json:"offset"`
}

// ApprovalItem 审批项（API 响应）
type ApprovalItem struct {
	ID           string                     `json:"id"`
	SessionID    string                     `json:"session_id"`
	TenantID     string                     `json:"tenant_id"`
	Status       string                     `json:"status"`
	DetectResult *sessionaudit.DetectResult `json:"detect_result"`
	ClientInfo   *sessionaudit.ClientInfo   `json:"client_info,omitempty"`
	ApprovedBy   string                     `json:"approved_by,omitempty"`
	ApprovedAt   *time.Time                 `json:"approved_at,omitempty"`
	Reason       string                     `json:"reason,omitempty"`
	CreatedAt    time.Time                  `json:"created_at"`
	ExpiresAt    time.Time                  `json:"expires_at"`
	TimeLeft     string                     `json:"time_left"` // 剩余时间（人类可读）
}

// ApprovalActionRequest 审批操作请求
type ApprovalActionRequest struct {
	Reason string `json:"reason"`
}

// ApprovalActionResponse 审批操作响应
type ApprovalActionResponse struct {
	Success    bool   `json:"success"`
	ApprovalID string `json:"approval_id"`
	Status     string `json:"status"`
	Message    string `json:"message"`
}

// ApprovalStatusResponse 审批状态响应（客户端轮询）
type ApprovalStatusResponse struct {
	ApprovalID string `json:"approval_id"`
	Status     string `json:"status"`
	Reason     string `json:"reason,omitempty"`
	TimeLeft   string `json:"time_left,omitempty"`
}

// handleApprovalList 查询待审批列表
//
// GET /api/admin/session-approvals?status=pending&limit=50&offset=0
func (h *Handler) handleApprovalList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// 解析查询参数
	query := r.URL.Query()
	req := &ApprovalListRequest{
		TenantID: query.Get("tenant_id"),
		Status:   query.Get("status"),
		Limit:    50,
		Offset:   0,
	}

	if req.Status == "" {
		req.Status = "pending" // 默认查询待审批
	}

	if limitStr := query.Get("limit"); limitStr != "" {
		if limit, err := strconv.Atoi(limitStr); err == nil && limit > 0 && limit <= 200 {
			req.Limit = limit
		}
	}

	if offsetStr := query.Get("offset"); offsetStr != "" {
		if offset, err := strconv.Atoi(offsetStr); err == nil && offset >= 0 {
			req.Offset = offset
		}
	}

	// 租户隔离：admin 调用方的 tenant 必须与请求参数 tenant_id 一致，
	// 否则拒绝（防止 tenant_admin 跨租户窥探）。
	// super_admin / admin_key 可查任意 tenant；tenant_admin 锁定自己。
	callerTenant := GetTenantID(r)
	isSuper := IsSuperAdminOrLegacy(r)
	if !isSuper && req.TenantID != "" && req.TenantID != callerTenant {
		writeError(w, http.StatusForbidden, "cross-tenant access denied")
		return
	}
	// tenant_admin 的 ?tenant_id= 缺失 → 锁定自己 tenant
	if !isSuper && req.TenantID == "" {
		req.TenantID = callerTenant
	}
	// 查询审批记录
	filter := &sessionaudit.ApprovalFilter{
		TenantID: req.TenantID,
		Status:   sessionaudit.ApprovalStatus(req.Status),
		Limit:    req.Limit,
		Offset:   req.Offset,
	}

	records, err := h.approvalMgr.List(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("list approvals failed: %v", err))
		return
	}

	// 转换为 API 响应格式
	items := make([]*ApprovalItem, 0, len(records))
	for _, rec := range records {
		item := &ApprovalItem{
			ID:           rec.ID,
			SessionID:    rec.SessionID,
			TenantID:     rec.TenantID,
			Status:       string(rec.Status),
			DetectResult: rec.DetectResult,
			ApprovedBy:   rec.ApprovedBy,
			ApprovedAt:   rec.ApprovedAt,
			Reason:       rec.Reason,
			CreatedAt:    rec.CreatedAt,
			ExpiresAt:    rec.ExpiresAt,
		}

		// 提取客户端信息
		if rec.Snapshot != nil {
			item.ClientInfo = &rec.Snapshot.ClientInfo
		}

		// 计算剩余时间
		if rec.Status == sessionaudit.ApprovalPending {
			timeLeft := time.Until(rec.ExpiresAt)
			if timeLeft > 0 {
				item.TimeLeft = formatDurationCN(timeLeft)
			} else {
				item.TimeLeft = "已超时"
			}
		}

		items = append(items, item)
	}

	writeJSON(w, http.StatusOK, &ApprovalListResponse{
		Approvals: items,
		Total:     len(items), // 简化实现，未查询总数
		Limit:     req.Limit,
		Offset:    req.Offset,
	})
}

// handleApprovalGet 获取单个审批记录
//
// GET /api/admin/session-approvals/:id
//
// 租户隔离：admin 调用方的 tenant 必须等于行 tenant，否则 403。
func (h *Handler) handleApprovalGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	approvalID := extractPathParam(r.URL.Path, "/api/admin/session-approvals/")
	if approvalID == "" {
		writeError(w, http.StatusBadRequest, "missing approval_id")
		return
	}

	// super_admin 传空 tenant → 跨租户查询；其他角色锁自己 tenant
	record, err := h.approvalMgr.GetForTenant(r.Context(), approvalID, ApprovalCallerTenantID(r))
	if err != nil {
		status := http.StatusNotFound
		switch {
		case errors.Is(err, sessionaudit.ErrTenantMismatch):
			status = http.StatusForbidden
		case errors.Is(err, sessionaudit.ErrNotFound):
			status = http.StatusNotFound
		}
		writeError(w, status, fmt.Sprintf("approval not found: %v", err))
		return
	}

	item := &ApprovalItem{
		ID:           record.ID,
		SessionID:    record.SessionID,
		TenantID:     record.TenantID,
		Status:       string(record.Status),
		DetectResult: record.DetectResult,
		ApprovedBy:   record.ApprovedBy,
		ApprovedAt:   record.ApprovedAt,
		Reason:       record.Reason,
		CreatedAt:    record.CreatedAt,
		ExpiresAt:    record.ExpiresAt,
	}

	if record.Snapshot != nil {
		item.ClientInfo = &record.Snapshot.ClientInfo
	}

	writeJSON(w, http.StatusOK, item)
}

// handleApprovalApprove 批准审批
//
// POST /api/admin/session-approvals/:id/approve
// Body: {"reason": "reviewed and approved"}
func (h *Handler) handleApprovalApprove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	approvalID := extractPathParam(r.URL.Path, "/api/admin/session-approvals/")
	approvalID = extractPathParam(approvalID, "/approve")
	if approvalID == "" {
		writeError(w, http.StatusBadRequest, "missing approval_id")
		return
	}

	// 解析请求体
	var req ApprovalActionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// 获取审批人（从 JWT 或 session）
	approvedBy := extractAdminUser(r)
	if approvedBy == "" {
		approvedBy = "system"
	}

	// 执行批准（带租户校验：调用方 tenant 必须等于行 tenant）
	err := h.approvalMgr.Approve(r.Context(), approvalID, ApprovalCallerTenantID(r), approvedBy, req.Reason)
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, sessionaudit.ErrTenantMismatch):
			status = http.StatusForbidden
		case errors.Is(err, sessionaudit.ErrNotFound):
			status = http.StatusNotFound
		case errors.Is(err, sessionaudit.ErrAlreadyDecided):
			status = http.StatusConflict
		}
		writeError(w, status, fmt.Sprintf("approve failed: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, &ApprovalActionResponse{
		Success:    true,
		ApprovalID: approvalID,
		Status:     "approved",
		Message:    "Approval granted successfully",
	})
}

// handleApprovalReject 拒绝审批
//
// POST /api/admin/session-approvals/:id/reject
// Body: {"reason": "violates policy"}
func (h *Handler) handleApprovalReject(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	approvalID := extractPathParam(r.URL.Path, "/api/admin/session-approvals/")
	approvalID = extractPathParam(approvalID, "/reject")
	if approvalID == "" {
		writeError(w, http.StatusBadRequest, "missing approval_id")
		return
	}

	// 解析请求体
	var req ApprovalActionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// 获取审批人
	approvedBy := extractAdminUser(r)
	if approvedBy == "" {
		approvedBy = "system"
	}

	// 执行拒绝（带租户校验）
	err := h.approvalMgr.Reject(r.Context(), approvalID, ApprovalCallerTenantID(r), approvedBy, req.Reason)
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, sessionaudit.ErrTenantMismatch):
			status = http.StatusForbidden
		case errors.Is(err, sessionaudit.ErrNotFound):
			status = http.StatusNotFound
		case errors.Is(err, sessionaudit.ErrAlreadyDecided):
			status = http.StatusConflict
		}
		writeError(w, status, fmt.Sprintf("reject failed: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, &ApprovalActionResponse{
		Success:    true,
		ApprovalID: approvalID,
		Status:     "rejected",
		Message:    "Approval rejected",
	})
}

// handleApprovalStatus 查询审批状态（客户端轮询接口）
//
// GET /v1/approvals/:id/status
//
// 此端点面向客户端轮询，不强制 admin 鉴权（approval_id 是
// 不可猜测的 UUID）。但调用方可携带 X-Tenant-ID header，handler
// 会用它做行租户比对以阻止跨租户窥探（缺失 header → 仅返回
// status 字段，不返回 detect_result / snapshot）。
func (h *Handler) handleApprovalStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	approvalID := extractPathParam(r.URL.Path, "/v1/approvals/")
	approvalID = extractPathParam(approvalID, "/status")
	if approvalID == "" {
		writeError(w, http.StatusBadRequest, "missing approval_id")
		return
	}

	requestedTenant := r.Header.Get("X-Tenant-ID")
	record, err := h.approvalMgr.GetForTenant(r.Context(), approvalID, requestedTenant)
	if err != nil {
		status := http.StatusNotFound
		switch {
		case errors.Is(err, sessionaudit.ErrTenantMismatch):
			status = http.StatusForbidden
		case errors.Is(err, sessionaudit.ErrNotFound):
			status = http.StatusNotFound
		}
		writeError(w, status, "approval not accessible")
		return
	}

	resp := &ApprovalStatusResponse{
		ApprovalID: record.ID,
		Status:     string(record.Status),
		Reason:     record.Reason,
	}

	if record.Status == sessionaudit.ApprovalPending {
		timeLeft := time.Until(record.ExpiresAt)
		if timeLeft > 0 {
			resp.TimeLeft = formatDuration(timeLeft)
		} else {
			resp.TimeLeft = "已超时"
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// formatDuration 已被提取到 admin/helpers.go，避免与 session_list.go
// 的同名定义冲突（2026-06-27 audit fix）。

// handleApprovalSubrouter 把 /api/admin/session-approvals/{id}[/{action}]
// 分发到 Get / Approve / Reject handler。
//   - GET    /api/admin/session-approvals/{id}              → handleApprovalGet
//   - POST   /api/admin/session-approvals/{id}/approve     → handleApprovalApprove
//   - POST   /api/admin/session-approvals/{id}/reject      → handleApprovalReject
func (h *Handler) handleApprovalSubrouter(w http.ResponseWriter, r *http.Request) {
	if h.approvalMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "approval manager not configured")
		return
	}
	rest := extractPathParam(r.URL.Path, "/api/admin/session-approvals/")
	if rest == "" {
		writeError(w, http.StatusBadRequest, "missing approval_id")
		return
	}
	switch {
	case r.Method == http.MethodGet && !strings.Contains(rest, "/"):
		h.handleApprovalGet(w, r)
	case strings.HasSuffix(rest, "/approve"):
		h.handleApprovalApprove(w, r)
	case strings.HasSuffix(rest, "/reject"):
		h.handleApprovalReject(w, r)
	default:
		writeError(w, http.StatusNotFound, "unknown action")
	}
}

func extractAdminUser(r *http.Request) string {
	// 从 JWT 或 session 提取管理员用户名。
	// 注：认证由 AdminMiddleware 完成；此函数仅在已认证后回填审计字段。
	if user := r.Header.Get("X-Admin-User"); user != "" {
		return user
	}
	return "admin"
}
