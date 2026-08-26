package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/sessionaudit"
)

// Mock implementations

type mockApprovalManager struct {
	getFunc     func(ctx context.Context, approvalID, tenantID string) (*sessionaudit.ApprovalRecord, error)
	listFunc    func(ctx context.Context, filter *sessionaudit.ApprovalFilter) ([]*sessionaudit.ApprovalRecord, error)
	approveFunc func(ctx context.Context, approvalID, tenantID, approvedBy, reason string) error
	rejectFunc  func(ctx context.Context, approvalID, tenantID, approvedBy, reason string) error
}

func (m *mockApprovalManager) GetForTenant(ctx context.Context, approvalID, tenantID string) (*sessionaudit.ApprovalRecord, error) {
	if m.getFunc != nil {
		return m.getFunc(ctx, approvalID, tenantID)
	}
	return nil, sessionaudit.ErrNotFound
}

func (m *mockApprovalManager) List(ctx context.Context, filter *sessionaudit.ApprovalFilter) ([]*sessionaudit.ApprovalRecord, error) {
	if m.listFunc != nil {
		return m.listFunc(ctx, filter)
	}
	return nil, nil
}

func (m *mockApprovalManager) Approve(ctx context.Context, approvalID, tenantID, approvedBy, reason string) error {
	if m.approveFunc != nil {
		return m.approveFunc(ctx, approvalID, tenantID, approvedBy, reason)
	}
	return nil
}

func (m *mockApprovalManager) Reject(ctx context.Context, approvalID, tenantID, approvedBy, reason string) error {
	if m.rejectFunc != nil {
		return m.rejectFunc(ctx, approvalID, tenantID, approvedBy, reason)
	}
	return nil
}

func (m *mockApprovalManager) Create(ctx context.Context, req *sessionaudit.ApprovalRequest) (string, error) {
	return "test-id", nil
}

func (m *mockApprovalManager) MarkTimeout(ctx context.Context) (int, error) {
	return 0, nil
}

type mockAuthService struct {
	tenantID    string
	isSuperAdmin bool
	userID      string
}

func (m *mockAuthService) GetTenantID(r *http.Request) string {
	if m.tenantID != "" {
		return m.tenantID
	}
	return "default"
}

func (m *mockAuthService) IsSuperAdmin(r *http.Request) bool {
	return m.isSuperAdmin
}

func (m *mockAuthService) GetUserID(r *http.Request) string {
	if m.userID != "" {
		return m.userID
	}
	return "test-user"
}

func (m *mockAuthService) CanAccessApproval(r *http.Request, approvalID string, tenantID string) bool {
	return true
}

// Test cases

func TestGetApproval_Success(t *testing.T) {
	now := time.Now()
	expiresAt := now.Add(1 * time.Hour)
	
	mockMgr := &mockApprovalManager{
		getFunc: func(ctx context.Context, approvalID, tenantID string) (*sessionaudit.ApprovalRecord, error) {
			if approvalID != "test-approval-123" {
				return nil, sessionaudit.ErrNotFound
			}
			return &sessionaudit.ApprovalRecord{
				ID:        "test-approval-123",
				SessionID: "session-456",
				TenantID:  "tenant-1",
				RequestID: "req-789",
				Status:    sessionaudit.ApprovalPending,
				DetectResult: &sessionaudit.DetectResult{
					Score:    8,
					Decision: sessionaudit.DecisionNeedApproval,
					Reason:   "high risk content",
				},
				CreatedAt: now,
				ExpiresAt: expiresAt,
			}, nil
		},
	}

	auth := &mockAuthService{
		tenantID: "tenant-1",
		userID:   "user-1",
	}

	handler := NewApprovalHandler(mockMgr, auth)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/approvals/test-approval-123", nil)
	w := httptest.NewRecorder()

	handler.GetApproval(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var resp ApprovalDetailResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.ID != "test-approval-123" {
		t.Errorf("expected ID test-approval-123, got %s", resp.ID)
	}

	if resp.Status != "pending" {
		t.Errorf("expected status pending, got %s", resp.Status)
	}
}

func TestGetApproval_NotFound(t *testing.T) {
	mockMgr := &mockApprovalManager{
		getFunc: func(ctx context.Context, approvalID, tenantID string) (*sessionaudit.ApprovalRecord, error) {
			return nil, sessionaudit.ErrNotFound
		},
	}

	auth := &mockAuthService{tenantID: "tenant-1"}
	handler := NewApprovalHandler(mockMgr, auth)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/approvals/nonexistent", nil)
	w := httptest.NewRecorder()

	handler.GetApproval(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", w.Code)
	}
}

func TestGetApproval_TenantMismatch(t *testing.T) {
	mockMgr := &mockApprovalManager{
		getFunc: func(ctx context.Context, approvalID, tenantID string) (*sessionaudit.ApprovalRecord, error) {
			return nil, sessionaudit.ErrTenantMismatch
		},
	}

	auth := &mockAuthService{tenantID: "tenant-1"}
	handler := NewApprovalHandler(mockMgr, auth)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/approvals/test-approval-123", nil)
	w := httptest.NewRecorder()

	handler.GetApproval(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected status 403, got %d", w.Code)
	}
}

func TestApproveApproval_Success(t *testing.T) {
	approveCallCount := 0
	mockMgr := &mockApprovalManager{
		approveFunc: func(ctx context.Context, approvalID, tenantID, approvedBy, reason string) error {
			approveCallCount++
			if approvalID != "test-approval-123" {
				t.Errorf("unexpected approval ID: %s", approvalID)
			}
			if approvedBy != "user-1" {
				t.Errorf("unexpected approver: %s", approvedBy)
			}
			return nil
		},
	}

	auth := &mockAuthService{
		tenantID: "tenant-1",
		userID:   "user-1",
	}

	handler := NewApprovalHandler(mockMgr, auth)

	reqBody := ApprovalActionRequest{
		Reason: "looks good",
	}
	bodyBytes, _ := json.Marshal(reqBody)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/approvals/test-approval-123/approve", bytes.NewReader(bodyBytes))
	w := httptest.NewRecorder()

	handler.ApproveApproval(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	if approveCallCount != 1 {
		t.Errorf("expected approve to be called once, got %d", approveCallCount)
	}

	var resp ApprovalActionResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if !resp.Success {
		t.Error("expected success to be true")
	}

	if resp.Status != "approved" {
		t.Errorf("expected status approved, got %s", resp.Status)
	}
}

func TestApproveApproval_AlreadyDecided(t *testing.T) {
	mockMgr := &mockApprovalManager{
		approveFunc: func(ctx context.Context, approvalID, tenantID, approvedBy, reason string) error {
			return sessionaudit.ErrAlreadyDecided
		},
	}

	auth := &mockAuthService{tenantID: "tenant-1", userID: "user-1"}
	handler := NewApprovalHandler(mockMgr, auth)

	reqBody := ApprovalActionRequest{Reason: "approve"}
	bodyBytes, _ := json.Marshal(reqBody)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/approvals/test-approval-123/approve", bytes.NewReader(bodyBytes))
	w := httptest.NewRecorder()

	handler.ApproveApproval(w, req)

	if w.Code != http.StatusConflict {
		t.Errorf("expected status 409, got %d", w.Code)
	}
}

func TestRejectApproval_Success(t *testing.T) {
	rejectCallCount := 0
	mockMgr := &mockApprovalManager{
		rejectFunc: func(ctx context.Context, approvalID, tenantID, approvedBy, reason string) error {
			rejectCallCount++
			if reason == "" {
				t.Error("reason should not be empty")
			}
			return nil
		},
	}

	auth := &mockAuthService{tenantID: "tenant-1", userID: "user-1"}
	handler := NewApprovalHandler(mockMgr, auth)

	reqBody := ApprovalActionRequest{Reason: "violates policy"}
	bodyBytes, _ := json.Marshal(reqBody)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/approvals/test-approval-123/reject", bytes.NewReader(bodyBytes))
	w := httptest.NewRecorder()

	handler.RejectApproval(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	if rejectCallCount != 1 {
		t.Errorf("expected reject to be called once, got %d", rejectCallCount)
	}
}

func TestRejectApproval_MissingReason(t *testing.T) {
	mockMgr := &mockApprovalManager{}
	auth := &mockAuthService{tenantID: "tenant-1", userID: "user-1"}
	handler := NewApprovalHandler(mockMgr, auth)

	reqBody := ApprovalActionRequest{Reason: ""} // Empty reason
	bodyBytes, _ := json.Marshal(reqBody)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/approvals/test-approval-123/reject", bytes.NewReader(bodyBytes))
	w := httptest.NewRecorder()

	handler.RejectApproval(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", w.Code)
	}
}

func TestListApprovals_Success(t *testing.T) {
	now := time.Now()
	
	mockMgr := &mockApprovalManager{
		listFunc: func(ctx context.Context, filter *sessionaudit.ApprovalFilter) ([]*sessionaudit.ApprovalRecord, error) {
			// Verify filter parameters
			if filter.TenantID != "tenant-1" {
				t.Errorf("expected tenant-1, got %s", filter.TenantID)
			}
			if filter.Status != sessionaudit.ApprovalPending {
				t.Errorf("expected pending status, got %s", filter.Status)
			}
			if filter.Limit != 50 {
				t.Errorf("expected limit 50, got %d", filter.Limit)
			}

			return []*sessionaudit.ApprovalRecord{
				{
					ID:        "approval-1",
					SessionID: "session-1",
					TenantID:  "tenant-1",
					RequestID: "req-1",
					Status:    sessionaudit.ApprovalPending,
					DetectResult: &sessionaudit.DetectResult{
						Score:    7,
						Decision: sessionaudit.DecisionNeedApproval,
						Reason:   "medium risk",
					},
					CreatedAt: now,
					ExpiresAt: now.Add(1 * time.Hour),
				},
				{
					ID:        "approval-2",
					SessionID: "session-2",
					TenantID:  "tenant-1",
					RequestID: "req-2",
					Status:    sessionaudit.ApprovalPending,
					DetectResult: &sessionaudit.DetectResult{
						Score:    9,
						Decision: sessionaudit.DecisionNeedApproval,
						Reason:   "high risk",
					},
					CreatedAt: now,
					ExpiresAt: now.Add(2 * time.Hour),
				},
			}, nil
		},
	}

	auth := &mockAuthService{tenantID: "tenant-1"}
	handler := NewApprovalHandler(mockMgr, auth)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/approvals?status=pending&page=1&page_size=50", nil)
	w := httptest.NewRecorder()

	handler.ListApprovals(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var resp ApprovalListResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(resp.Items) != 2 {
		t.Errorf("expected 2 items, got %d", len(resp.Items))
	}

	if resp.Total != 2 {
		t.Errorf("expected total 2, got %d", resp.Total)
	}

	if resp.Page != 1 {
		t.Errorf("expected page 1, got %d", resp.Page)
	}
}

func TestListApprovals_TenantIsolation(t *testing.T) {
	// Non-super admin should only see their own tenant
	mockMgr := &mockApprovalManager{
		listFunc: func(ctx context.Context, filter *sessionaudit.ApprovalFilter) ([]*sessionaudit.ApprovalRecord, error) {
			if filter.TenantID != "tenant-1" {
				t.Errorf("tenant isolation failed: expected tenant-1, got %s", filter.TenantID)
			}
			return []*sessionaudit.ApprovalRecord{}, nil
		},
	}

	auth := &mockAuthService{
		tenantID:    "tenant-1",
		isSuperAdmin: false,
	}
	handler := NewApprovalHandler(mockMgr, auth)

	// Try to query a different tenant (should be overridden)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/approvals?tenant_id=tenant-2", nil)
	w := httptest.NewRecorder()

	handler.ListApprovals(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}
}

func TestListApprovals_SuperAdminCanAccessAll(t *testing.T) {
	// Super admin can query any tenant
	mockMgr := &mockApprovalManager{
		listFunc: func(ctx context.Context, filter *sessionaudit.ApprovalFilter) ([]*sessionaudit.ApprovalRecord, error) {
			if filter.TenantID != "tenant-2" {
				t.Errorf("super admin should be able to access tenant-2, got %s", filter.TenantID)
			}
			return []*sessionaudit.ApprovalRecord{}, nil
		},
	}

	auth := &mockAuthService{
		tenantID:    "tenant-1",
		isSuperAdmin: true,
	}
	handler := NewApprovalHandler(mockMgr, auth)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/approvals?tenant_id=tenant-2", nil)
	w := httptest.NewRecorder()

	handler.ListApprovals(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}
}

func TestGetApprovalStats_Success(t *testing.T) {
	now := time.Now()
	yesterday := now.Add(-24 * time.Hour)
	approvedTime := now.Add(-30 * time.Minute)

	mockMgr := &mockApprovalManager{
		listFunc: func(ctx context.Context, filter *sessionaudit.ApprovalFilter) ([]*sessionaudit.ApprovalRecord, error) {
			return []*sessionaudit.ApprovalRecord{
				{
					ID:        "approval-1",
					Status:    sessionaudit.ApprovalPending,
					DetectResult: &sessionaudit.DetectResult{Decision: sessionaudit.DecisionNeedApproval},
					CreatedAt: now,
				},
				{
					ID:         "approval-2",
					Status:     sessionaudit.ApprovalApproved,
					DetectResult: &sessionaudit.DetectResult{Decision: sessionaudit.DecisionWarn},
					CreatedAt:  yesterday,
					ApprovedAt: &approvedTime,
				},
				{
					ID:        "approval-3",
					Status:    sessionaudit.ApprovalRejected,
					DetectResult: &sessionaudit.DetectResult{Decision: sessionaudit.DecisionBlock},
					CreatedAt: yesterday,
				},
				{
					ID:        "approval-4",
					Status:    sessionaudit.ApprovalTimeout,
					DetectResult: &sessionaudit.DetectResult{Decision: sessionaudit.DecisionNeedApproval},
					CreatedAt: yesterday,
				},
			}, nil
		},
	}

	auth := &mockAuthService{tenantID: "tenant-1"}
	handler := NewApprovalHandler(mockMgr, auth)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/approvals/stats", nil)
	w := httptest.NewRecorder()

	handler.GetApprovalStats(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var stats ApprovalStats
	if err := json.NewDecoder(w.Body).Decode(&stats); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if stats.Total != 4 {
		t.Errorf("expected total 4, got %d", stats.Total)
	}

	if stats.Pending != 1 {
		t.Errorf("expected pending 1, got %d", stats.Pending)
	}

	if stats.Approved != 1 {
		t.Errorf("expected approved 1, got %d", stats.Approved)
	}

	if stats.Rejected != 1 {
		t.Errorf("expected rejected 1, got %d", stats.Rejected)
	}

	if stats.Timeout != 1 {
		t.Errorf("expected timeout 1, got %d", stats.Timeout)
	}

	if stats.AvgApprovalTimeSeconds <= 0 {
		t.Error("expected positive avg approval time")
	}

	if len(stats.ByRiskLevel) == 0 {
		t.Error("expected by_risk_level to have entries")
	}
}

func TestExtractRequestID(t *testing.T) {
	handler := &ApprovalHandler{}

	tests := []struct {
		path     string
		expected string
	}{
		{"/api/v1/approvals/test-123", "test-123"},
		{"/api/v1/approvals/test-123/approve", "test-123"},
		{"/api/v1/approvals/test-123/reject", "test-123"},
		{"/api/v1/approvals/", ""},
		{"/api/v1/approvals/stats", ""},
		{"/api/admin/approvals", ""},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			result := handler.extractRequestID(tt.path)
			if result != tt.expected {
				t.Errorf("extractRequestID(%s) = %s, want %s", tt.path, result, tt.expected)
			}
		})
	}
}

func TestFormatDuration(t *testing.T) {
	handler := &ApprovalHandler{}

	tests := []struct {
		duration time.Duration
		expected string
	}{
		{30 * time.Second, "30s"},
		{90 * time.Second, "1m"},
		{45 * time.Minute, "45m"},
		{2 * time.Hour, "2.0h"},
		{25 * time.Hour, "1.0d"},
		{48 * time.Hour, "2.0d"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			result := handler.formatDuration(tt.duration)
			if result != tt.expected {
				t.Errorf("formatDuration(%v) = %s, want %s", tt.duration, result, tt.expected)
			}
		})
	}
}

func TestParseListRequest_Defaults(t *testing.T) {
	handler := &ApprovalHandler{}
	
	req := httptest.NewRequest(http.MethodGet, "/api/admin/approvals", nil)
	parsed := handler.parseListRequest(req)

	if parsed.Page != 1 {
		t.Errorf("expected default page 1, got %d", parsed.Page)
	}

	if parsed.PageSize != 50 {
		t.Errorf("expected default page_size 50, got %d", parsed.PageSize)
	}

	if parsed.Status != "pending" {
		t.Errorf("expected default status pending, got %s", parsed.Status)
	}

	if parsed.SortBy != "created_at" {
		t.Errorf("expected default sort_by created_at, got %s", parsed.SortBy)
	}

	if parsed.SortOrder != "desc" {
		t.Errorf("expected default sort_order desc, got %s", parsed.SortOrder)
	}
}

func TestParseListRequest_CustomValues(t *testing.T) {
	handler := &ApprovalHandler{}
	
	req := httptest.NewRequest(http.MethodGet, "/api/admin/approvals?page=2&page_size=100&status=approved&sort_by=risk_level&sort_order=asc", nil)
	parsed := handler.parseListRequest(req)

	if parsed.Page != 2 {
		t.Errorf("expected page 2, got %d", parsed.Page)
	}

	if parsed.PageSize != 100 {
		t.Errorf("expected page_size 100, got %d", parsed.PageSize)
	}

	if parsed.Status != "approved" {
		t.Errorf("expected status approved, got %s", parsed.Status)
	}

	if parsed.SortBy != "risk_level" {
		t.Errorf("expected sort_by risk_level, got %s", parsed.SortBy)
	}

	if parsed.SortOrder != "asc" {
		t.Errorf("expected sort_order asc, got %s", parsed.SortOrder)
	}
}

func TestParseListRequest_InvalidPageSize(t *testing.T) {
	handler := &ApprovalHandler{}
	
	// Page size exceeding limit should be capped
	req := httptest.NewRequest(http.MethodGet, "/api/admin/approvals?page_size=500", nil)
	parsed := handler.parseListRequest(req)

	if parsed.PageSize != 50 {
		t.Errorf("expected page_size to be capped at 50, got %d", parsed.PageSize)
	}
}

// Benchmark tests

func BenchmarkGetApproval(b *testing.B) {
	now := time.Now()
	mockMgr := &mockApprovalManager{
		getFunc: func(ctx context.Context, approvalID, tenantID string) (*sessionaudit.ApprovalRecord, error) {
			return &sessionaudit.ApprovalRecord{
				ID:        approvalID,
				SessionID: "session-1",
				TenantID:  tenantID,
				Status:    sessionaudit.ApprovalPending,
				DetectResult: &sessionaudit.DetectResult{
					Score:    5,
					Decision: sessionaudit.DecisionNeedApproval,
				},
				CreatedAt: now,
				ExpiresAt: now.Add(1 * time.Hour),
			}, nil
		},
	}

	auth := &mockAuthService{tenantID: "tenant-1"}
	handler := NewApprovalHandler(mockMgr, auth)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/approvals/test-approval-123", nil)
		w := httptest.NewRecorder()
		handler.GetApproval(w, req)
	}
}

func BenchmarkListApprovals(b *testing.B) {
	now := time.Now()
	records := make([]*sessionaudit.ApprovalRecord, 50)
	for i := 0; i < 50; i++ {
		records[i] = &sessionaudit.ApprovalRecord{
			ID:        fmt.Sprintf("approval-%d", i),
			SessionID: fmt.Sprintf("session-%d", i),
			TenantID:  "tenant-1",
			Status:    sessionaudit.ApprovalPending,
			DetectResult: &sessionaudit.DetectResult{
				Score:    5,
				Decision: sessionaudit.DecisionNeedApproval,
			},
			CreatedAt: now,
			ExpiresAt: now.Add(1 * time.Hour),
		}
	}

	mockMgr := &mockApprovalManager{
		listFunc: func(ctx context.Context, filter *sessionaudit.ApprovalFilter) ([]*sessionaudit.ApprovalRecord, error) {
			return records, nil
		},
	}

	auth := &mockAuthService{tenantID: "tenant-1"}
	handler := NewApprovalHandler(mockMgr, auth)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/admin/approvals", nil)
		w := httptest.NewRecorder()
		handler.ListApprovals(w, req)
	}
}
