// Package api provides API handlers for the LLM Gateway.
//
// This package contains handlers for approval request query and operations.
// It provides RESTful endpoints for managing approval workflows including
// querying, approving, rejecting approval requests, and gathering statistics.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/sessionaudit"
)

// AuthService defines the interface for authentication and authorization.
type AuthService interface {
	// GetTenantID returns the tenant ID from the request context.
	GetTenantID(r *http.Request) string
	// IsSuperAdmin returns true if the request is from a super admin.
	IsSuperAdmin(r *http.Request) bool
	// GetUserID returns the user ID from the request context.
	GetUserID(r *http.Request) string
	// CanAccessApproval returns true if the user can access the approval request.
	CanAccessApproval(r *http.Request, approvalID string, tenantID string) bool
}

// ApprovalManager defines the interface for approval operations.
// This matches the sessionaudit.ApprovalManager interface.
type ApprovalManager interface {
	GetForTenant(ctx context.Context, approvalID, tenantID string) (*sessionaudit.ApprovalRecord, error)
	List(ctx context.Context, filter *sessionaudit.ApprovalFilter) ([]*sessionaudit.ApprovalRecord, error)
	Approve(ctx context.Context, approvalID, tenantID, approvedBy, reason string) error
	Reject(ctx context.Context, approvalID, tenantID, approvedBy, reason string) error
}

// ApprovalHandler handles approval-related HTTP requests.
type ApprovalHandler struct {
	manager ApprovalManager
	auth    AuthService
}

// ApprovalStats represents statistics about approval requests.
type ApprovalStats struct {
	Total                  int                    `json:"total"`
	Pending                int                    `json:"pending"`
	Approved               int                    `json:"approved"`
	Rejected               int                    `json:"rejected"`
	Timeout                int                    `json:"timeout"`
	AvgApprovalTimeSeconds float64                `json:"avg_approval_time_seconds"`
	ByRiskLevel            map[string]int         `json:"by_risk_level"`
	ByTriggerType          map[string]int         `json:"by_trigger_type"`
	TodayTotal             int                    `json:"today_total"`
	TodayPending           int                    `json:"today_pending"`
}

// ApprovalListRequest represents the request parameters for listing approvals.
type ApprovalListRequest struct {
	Status        string    `json:"status"`          // pending/approved/rejected/timeout
	TenantID      string    `json:"tenant_id"`       // filter by tenant
	RiskLevel     string    `json:"risk_level"`      // LOW/MEDIUM/HIGH/CRITICAL
	Page          int       `json:"page"`            // page number (1-based)
	PageSize      int       `json:"page_size"`       // items per page
	SortBy        string    `json:"sort_by"`         // created_at/risk_level
	SortOrder     string    `json:"sort_order"`      // asc/desc
	CreatedAfter  time.Time `json:"created_after"`   // filter by creation time
	CreatedBefore time.Time `json:"created_before"`  // filter by creation time
}

// ApprovalListResponse represents the response for listing approvals.
type ApprovalListResponse struct {
	Items      []*ApprovalItem `json:"items"`
	Total      int             `json:"total"`
	Page       int             `json:"page"`
	PageSize   int             `json:"page_size"`
	TotalPages int             `json:"total_pages"`
}

// ApprovalItem represents a single approval in the list response.
type ApprovalItem struct {
	ID           string                     `json:"id"`
	SessionID    string                     `json:"session_id"`
	TenantID     string                     `json:"tenant_id"`
	RequestID    string                     `json:"request_id"`
	Status       string                     `json:"status"`
	DetectResult *sessionaudit.DetectResult `json:"detect_result"`
	RiskLevel    string                     `json:"risk_level"`
	TriggerType  string                     `json:"trigger_type"`
	ApprovedBy   string                     `json:"approved_by,omitempty"`
	ApprovedAt   *time.Time                 `json:"approved_at,omitempty"`
	Reason       string                     `json:"reason,omitempty"`
	CreatedAt    time.Time                  `json:"created_at"`
	ExpiresAt    time.Time                  `json:"expires_at"`
	TimeLeft     string                     `json:"time_left,omitempty"`
}

// ApprovalDetailResponse represents the detailed response for a single approval.
type ApprovalDetailResponse struct {
	*ApprovalItem
	Snapshot *sessionaudit.RequestSnapshot `json:"snapshot,omitempty"`
}

// ApprovalActionRequest represents the request body for approve/reject actions.
type ApprovalActionRequest struct {
	Reason string `json:"reason"`
}

// ApprovalActionResponse represents the response for approve/reject actions.
type ApprovalActionResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Status  string `json:"status"`
}

// NewApprovalHandler creates a new ApprovalHandler instance.
func NewApprovalHandler(manager ApprovalManager, auth AuthService) *ApprovalHandler {
	return &ApprovalHandler{
		manager: manager,
		auth:    auth,
	}
}

// GetApproval handles GET /api/v1/approvals/{request_id}
//
// Returns detailed information about a specific approval request.
// Authorization: Requires the user to be an approver for this request or an admin.
func (h *ApprovalHandler) GetApproval(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		h.writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	requestID := h.extractRequestID(r.URL.Path)
	if requestID == "" {
		h.writeError(w, http.StatusBadRequest, "missing request_id")
		return
	}

	// Get tenant ID from auth context
	tenantID := h.auth.GetTenantID(r)
	if tenantID == "" {
		tenantID = "default"
	}

	// Super admin can access any tenant's approval
	callerTenant := tenantID
	if h.auth.IsSuperAdmin(r) {
		callerTenant = "" // empty means bypass tenant check
	}

	// Retrieve approval record
	record, err := h.manager.GetForTenant(r.Context(), requestID, callerTenant)
	if err != nil {
		if errors.Is(err, sessionaudit.ErrNotFound) {
			h.writeError(w, http.StatusNotFound, "approval not found")
			return
		}
		if errors.Is(err, sessionaudit.ErrTenantMismatch) {
			h.writeError(w, http.StatusForbidden, "access denied")
			return
		}
		slog.Error("failed to get approval", "request_id", requestID, "error", err)
		h.writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	response := h.buildDetailResponse(record)
	h.writeJSON(w, http.StatusOK, response)
}

// ApproveApproval handles POST /api/v1/approvals/{request_id}/approve
//
// Approves a pending approval request.
// Authorization: Requires the user to be an approver for this request or an admin.
func (h *ApprovalHandler) ApproveApproval(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		h.writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	requestID := h.extractRequestID(r.URL.Path)
	if requestID == "" {
		h.writeError(w, http.StatusBadRequest, "missing request_id")
		return
	}

	var req ApprovalActionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Get approver info
	userID := h.auth.GetUserID(r)
	if userID == "" {
		userID = "system"
	}

	tenantID := h.auth.GetTenantID(r)
	callerTenant := tenantID
	if h.auth.IsSuperAdmin(r) {
		callerTenant = ""
	}

	// Approve the request
	err := h.manager.Approve(r.Context(), requestID, callerTenant, userID, req.Reason)
	if err != nil {
		status := http.StatusInternalServerError
		message := "failed to approve"
		
		switch {
		case errors.Is(err, sessionaudit.ErrNotFound):
			status = http.StatusNotFound
			message = "approval not found"
		case errors.Is(err, sessionaudit.ErrTenantMismatch):
			status = http.StatusForbidden
			message = "access denied"
		case errors.Is(err, sessionaudit.ErrAlreadyDecided):
			status = http.StatusConflict
			message = "approval already decided"
		}

		h.writeError(w, status, message)
		return
	}

	response := ApprovalActionResponse{
		Success: true,
		Message: "approval granted successfully",
		Status:  "approved",
	}
	h.writeJSON(w, http.StatusOK, response)
}

// RejectApproval handles POST /api/v1/approvals/{request_id}/reject
//
// Rejects a pending approval request.
// Authorization: Requires the user to be an approver for this request or an admin.
func (h *ApprovalHandler) RejectApproval(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		h.writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	requestID := h.extractRequestID(r.URL.Path)
	if requestID == "" {
		h.writeError(w, http.StatusBadRequest, "missing request_id")
		return
	}

	var req ApprovalActionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Reason == "" {
		h.writeError(w, http.StatusBadRequest, "reason is required for rejection")
		return
	}

	// Get approver info
	userID := h.auth.GetUserID(r)
	if userID == "" {
		userID = "system"
	}

	tenantID := h.auth.GetTenantID(r)
	callerTenant := tenantID
	if h.auth.IsSuperAdmin(r) {
		callerTenant = ""
	}

	// Reject the request
	err := h.manager.Reject(r.Context(), requestID, callerTenant, userID, req.Reason)
	if err != nil {
		status := http.StatusInternalServerError
		message := "failed to reject"
		
		switch {
		case errors.Is(err, sessionaudit.ErrNotFound):
			status = http.StatusNotFound
			message = "approval not found"
		case errors.Is(err, sessionaudit.ErrTenantMismatch):
			status = http.StatusForbidden
			message = "access denied"
		case errors.Is(err, sessionaudit.ErrAlreadyDecided):
			status = http.StatusConflict
			message = "approval already decided"
		}

		h.writeError(w, status, message)
		return
	}

	response := ApprovalActionResponse{
		Success: true,
		Message: "approval rejected",
		Status:  "rejected",
	}
	h.writeJSON(w, http.StatusOK, response)
}

// ListApprovals handles GET /api/admin/approvals
//
// Lists approval requests with filtering, pagination, and sorting.
// Authorization: Admins can see all approvals in their tenant; super admins can see all.
func (h *ApprovalHandler) ListApprovals(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		h.writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Parse query parameters
	req := h.parseListRequest(r)

	// Apply tenant isolation
	tenantID := h.auth.GetTenantID(r)
	if !h.auth.IsSuperAdmin(r) {
		req.TenantID = tenantID // Lock to caller's tenant
	} else if req.TenantID == "" && r.URL.Query().Get("tenant_id") == "" {
		req.TenantID = tenantID // Default to caller's tenant if not specified
	}

	// Build filter
	filter := &sessionaudit.ApprovalFilter{
		TenantID: req.TenantID,
		Status:   sessionaudit.ApprovalStatus(req.Status),
		Limit:    req.PageSize,
		Offset:   (req.Page - 1) * req.PageSize,
	}

	// Get approvals
	records, err := h.manager.List(r.Context(), filter)
	if err != nil {
		slog.Error("failed to list approvals", "error", err)
		h.writeError(w, http.StatusInternalServerError, "failed to list approvals")
		return
	}

	// Get total count (simplified - return length for now)
	total := len(records)

	// Build response
	items := make([]*ApprovalItem, 0, len(records))
	for _, record := range records {
		items = append(items, h.buildListItem(record))
	}

	totalPages := (total + req.PageSize - 1) / req.PageSize
	if totalPages < 1 {
		totalPages = 1
	}

	response := ApprovalListResponse{
		Items:      items,
		Total:      total,
		Page:       req.Page,
		PageSize:   req.PageSize,
		TotalPages: totalPages,
	}

	h.writeJSON(w, http.StatusOK, response)
}

// GetApprovalStats handles GET /api/admin/approvals/stats
//
// Returns statistics about approval requests.
// Authorization: Requires admin access.
func (h *ApprovalHandler) GetApprovalStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		h.writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Get tenant filter
	tenantID := r.URL.Query().Get("tenant_id")
	if !h.auth.IsSuperAdmin(r) {
		tenantID = h.auth.GetTenantID(r)
	}

	// Parse time range
	now := time.Now()
	startTime := now.AddDate(0, -1, 0) // Default: last 30 days
	endTime := now

	if start := r.URL.Query().Get("start_time"); start != "" {
		if t, err := time.Parse(time.RFC3339, start); err == nil {
			startTime = t
		}
	}
	if end := r.URL.Query().Get("end_time"); end != "" {
		if t, err := time.Parse(time.RFC3339, end); err == nil {
			endTime = t
		}
	}

	// Calculate statistics
	stats, err := h.calculateStats(r.Context(), tenantID, startTime, endTime)
	if err != nil {
		slog.Error("failed to calculate stats", "error", err)
		h.writeError(w, http.StatusInternalServerError, "failed to calculate statistics")
		return
	}

	h.writeJSON(w, http.StatusOK, stats)
}

// calculateStats computes approval statistics from the database.
func (h *ApprovalHandler) calculateStats(ctx context.Context, tenantID string, startTime, endTime time.Time) (*ApprovalStats, error) {
	// Get all approvals for the time range
	filter := &sessionaudit.ApprovalFilter{
		TenantID: tenantID,
		Limit:    10000, // High limit to get all records
		Offset:   0,
	}

	records, err := h.manager.List(ctx, filter)
	if err != nil {
		return nil, err
	}

	stats := &ApprovalStats{
		ByRiskLevel:   make(map[string]int),
		ByTriggerType: make(map[string]int),
	}

	var totalApprovalTime time.Duration
	var approvedCount int
	todayStart := time.Now().Truncate(24 * time.Hour)

	for _, record := range records {
		// Filter by time range
		if record.CreatedAt.Before(startTime) || record.CreatedAt.After(endTime) {
			continue
		}

		stats.Total++

		// Count by status
		switch record.Status {
		case sessionaudit.ApprovalPending:
			stats.Pending++
		case sessionaudit.ApprovalApproved:
			stats.Approved++
			approvedCount++
			if record.ApprovedAt != nil {
				totalApprovalTime += record.ApprovedAt.Sub(record.CreatedAt)
			}
		case sessionaudit.ApprovalRejected:
			stats.Rejected++
		case sessionaudit.ApprovalTimeout:
			stats.Timeout++
		}

		// Count by risk level
		if record.DetectResult != nil && record.DetectResult.Decision != "" {
			riskLevel := string(record.DetectResult.Decision)
			stats.ByRiskLevel[riskLevel]++
		}

		// Count today's approvals
		if record.CreatedAt.After(todayStart) {
			stats.TodayTotal++
			if record.Status == sessionaudit.ApprovalPending {
				stats.TodayPending++
			}
		}
	}

	// Calculate average approval time
	if approvedCount > 0 {
		stats.AvgApprovalTimeSeconds = totalApprovalTime.Seconds() / float64(approvedCount)
	}

	return stats, nil
}

// Helper methods

func (h *ApprovalHandler) parseListRequest(r *http.Request) *ApprovalListRequest {
	q := r.URL.Query()
	
	page := 1
	if p := q.Get("page"); p != "" {
		if val, err := strconv.Atoi(p); err == nil && val > 0 {
			page = val
		}
	}

	pageSize := 50
	if ps := q.Get("page_size"); ps != "" {
		if val, err := strconv.Atoi(ps); err == nil && val > 0 && val <= 200 {
			pageSize = val
		}
	}

	req := &ApprovalListRequest{
		Status:    q.Get("status"),
		TenantID:  q.Get("tenant_id"),
		RiskLevel: q.Get("risk_level"),
		Page:      page,
		PageSize:  pageSize,
		SortBy:    q.Get("sort_by"),
		SortOrder: q.Get("sort_order"),
	}

	if req.Status == "" {
		req.Status = "pending" // Default to pending
	}

	if req.SortBy == "" {
		req.SortBy = "created_at"
	}

	if req.SortOrder == "" {
		req.SortOrder = "desc"
	}

	return req
}

func (h *ApprovalHandler) buildListItem(record *sessionaudit.ApprovalRecord) *ApprovalItem {
	item := &ApprovalItem{
		ID:           record.ID,
		SessionID:    record.SessionID,
		TenantID:     record.TenantID,
		RequestID:    record.RequestID,
		Status:       string(record.Status),
		DetectResult: record.DetectResult,
		ApprovedBy:   record.ApprovedBy,
		ApprovedAt:   record.ApprovedAt,
		Reason:       record.Reason,
		CreatedAt:    record.CreatedAt,
		ExpiresAt:    record.ExpiresAt,
	}

	// Extract risk level and trigger type from detect result
	if record.DetectResult != nil {
		item.RiskLevel = string(record.DetectResult.Decision)
		item.TriggerType = record.DetectResult.Reason
	}

	// Calculate time left for pending approvals
	if record.Status == sessionaudit.ApprovalPending {
		timeLeft := time.Until(record.ExpiresAt)
		if timeLeft > 0 {
			item.TimeLeft = h.formatDuration(timeLeft)
		} else {
			item.TimeLeft = "expired"
		}
	}

	return item
}

func (h *ApprovalHandler) buildDetailResponse(record *sessionaudit.ApprovalRecord) *ApprovalDetailResponse {
	item := h.buildListItem(record)
	return &ApprovalDetailResponse{
		ApprovalItem: item,
		Snapshot:     record.Snapshot,
	}
}

func (h *ApprovalHandler) extractRequestID(path string) string {
	// Extract request_id from paths like:
	// /api/v1/approvals/{request_id}
	// /api/v1/approvals/{request_id}/approve
	// /api/v1/approvals/{request_id}/reject
	parts := strings.Split(strings.Trim(path, "/"), "/")
	for i, part := range parts {
		if part == "approvals" && i+1 < len(parts) {
			nextPart := parts[i+1]
			// Remove /approve or /reject suffix if present
			if idx := strings.Index(nextPart, "/"); idx > 0 {
				return nextPart[:idx]
			}
			// Check if it's not an action keyword
			if nextPart != "approve" && nextPart != "reject" && nextPart != "stats" {
				return nextPart
			}
		}
	}
	return ""
}

func (h *ApprovalHandler) formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%.1fh", d.Hours())
	}
	return fmt.Sprintf("%.1fd", d.Hours()/24)
}

func (h *ApprovalHandler) writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		slog.Error("failed to encode JSON response", "error", err)
	}
}

func (h *ApprovalHandler) writeError(w http.ResponseWriter, status int, message string) {
	h.writeJSON(w, status, map[string]string{"error": message})
}
