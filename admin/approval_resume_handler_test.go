package admin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/session"
)

// ──────────────────────────────────────────────────────────────────────────────
// Test doubles
// ──────────────────────────────────────────────────────────────────────────────

type mockResumeHandler struct {
	resumeFunc func(ctx context.Context, approvalID, tenantID string) error
	calls      []resumeCall
}

type resumeCall struct {
	approvalID string
	tenantID   string
}

func (m *mockResumeHandler) ResumeAfterApproval(ctx context.Context, approvalID, tenantID string) error {
	m.calls = append(m.calls, resumeCall{approvalID, tenantID})
	if m.resumeFunc != nil {
		return m.resumeFunc(ctx, approvalID, tenantID)
	}
	return nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Tests
// ──────────────────────────────────────────────────────────────────────────────

func TestHandleApprovalResume_NotConfigured(t *testing.T) {
	h := &Handler{} // approvalResumeHandler is nil

	req := httptest.NewRequest("POST", "/api/admin/approvals/test-id/resume", nil)
	rec := httptest.NewRecorder()

	h.HandleApprovalResume(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected status 503, got %d", rec.Code)
	}
}

func TestHandleApprovalResume_MissingApprovalID(t *testing.T) {
	mock := &mockResumeHandler{}
	h := &Handler{approvalResumeHandler: mock}

	req := httptest.NewRequest("POST", "/api/admin/approvals//resume", nil) // empty ID
	rec := httptest.NewRecorder()

	h.HandleApprovalResume(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", rec.Code)
	}
}

func TestHandleApprovalResume_MissingTenantID(t *testing.T) {
	mock := &mockResumeHandler{}
	h := &Handler{approvalResumeHandler: mock}

	req := httptest.NewRequest("POST", "/api/admin/approvals/test-id/resume", nil)
	// no tenant_id in context or header
	rec := httptest.NewRecorder()

	h.HandleApprovalResume(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", rec.Code)
	}
}

func TestHandleApprovalResume_Success(t *testing.T) {
	mock := &mockResumeHandler{}
	h := &Handler{approvalResumeHandler: mock}

	req := httptest.NewRequest("POST", "/api/admin/approvals/test-approval-id/resume", nil)
	req.Header.Set("X-Tenant-ID", "test-tenant")
	rec := httptest.NewRecorder()

	h.HandleApprovalResume(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	if len(mock.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(mock.calls))
	}

	if mock.calls[0].approvalID != "test-approval-id" {
		t.Errorf("expected approval_id 'test-approval-id', got %q", mock.calls[0].approvalID)
	}
	if mock.calls[0].tenantID != "test-tenant" {
		t.Errorf("expected tenant_id 'test-tenant', got %q", mock.calls[0].tenantID)
	}
}

func TestHandleApprovalResume_NotPending(t *testing.T) {
	mock := &mockResumeHandler{
		resumeFunc: func(_ context.Context, _, _ string) error {
			return session.ErrResumeNotPending
		},
	}
	h := &Handler{approvalResumeHandler: mock}

	req := httptest.NewRequest("POST", "/api/admin/approvals/test-id/resume", nil)
	req.Header.Set("X-Tenant-ID", "test-tenant")
	rec := httptest.NewRecorder()

	h.HandleApprovalResume(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", rec.Code)
	}
}

func TestHandleApprovalResume_SnapshotMissing(t *testing.T) {
	mock := &mockResumeHandler{
		resumeFunc: func(_ context.Context, _, _ string) error {
			return session.ErrResumeSnapshotMissing
		},
	}
	h := &Handler{approvalResumeHandler: mock}

	req := httptest.NewRequest("POST", "/api/admin/approvals/test-id/resume", nil)
	req.Header.Set("X-Tenant-ID", "test-tenant")
	rec := httptest.NewRecorder()

	h.HandleApprovalResume(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected status 500, got %d", rec.Code)
	}
}

func TestHandleApprovalResume_Rejected(t *testing.T) {
	mock := &mockResumeHandler{
		resumeFunc: func(_ context.Context, _, _ string) error {
			return session.ErrResumeRejected
		},
	}
	h := &Handler{approvalResumeHandler: mock}

	req := httptest.NewRequest("POST", "/api/admin/approvals/test-id/resume", nil)
	req.Header.Set("X-Tenant-ID", "test-tenant")
	rec := httptest.NewRecorder()

	h.HandleApprovalResume(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", rec.Code)
	}
}

func TestHandleApprovalResume_Timeout(t *testing.T) {
	mock := &mockResumeHandler{
		resumeFunc: func(_ context.Context, _, _ string) error {
			return session.ErrResumeTimeout
		},
	}
	h := &Handler{approvalResumeHandler: mock}

	req := httptest.NewRequest("POST", "/api/admin/approvals/test-id/resume", nil)
	req.Header.Set("X-Tenant-ID", "test-tenant")
	rec := httptest.NewRecorder()

	h.HandleApprovalResume(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", rec.Code)
	}
}

func TestHandleApprovalResume_GenericError(t *testing.T) {
	mock := &mockResumeHandler{
		resumeFunc: func(_ context.Context, _, _ string) error {
			return errors.New("database connection failed")
		},
	}
	h := &Handler{approvalResumeHandler: mock}

	req := httptest.NewRequest("POST", "/api/admin/approvals/test-id/resume", nil)
	req.Header.Set("X-Tenant-ID", "test-tenant")
	rec := httptest.NewRecorder()

	h.HandleApprovalResume(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected status 500, got %d", rec.Code)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Helper function tests
// ──────────────────────────────────────────────────────────────────────────────

func TestExtractApprovalID(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"/api/admin/approvals/abc123/resume", "abc123"},
		{"/api/admin/approvals/test-id-456/resume", "test-id-456"},
		{"/api/admin/approvals//resume", ""},
		{"/api/admin/approvals/", ""},
	}

	for _, tt := range tests {
		req := httptest.NewRequest("POST", tt.path, nil)
		got := extractApprovalID(req)
		if got != tt.want {
			t.Errorf("extractApprovalID(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

func TestApprovalResumeTenantScoping(t *testing.T) {
	// R29: tenant_admin（及其它非超管角色）钉死认证上下文租户——伪造
	// X-Tenant-ID / tenant_id query 不得改变目标租户（跨租户 resume 越权）。
	newTenantAdminReq := func(tenant string) *http.Request {
		req := httptest.NewRequest("POST", "/api/admin/approvals/test/resume", nil)
		req = SetAuthContext(req, &AuthContext{TenantID: tenant, Role: "tenant_admin"})
		return req
	}

	req := newTenantAdminReq("own-tenant")
	req.Header.Set("X-Tenant-ID", "victim-tenant")
	q := req.URL.Query()
	q.Set("tenant_id", "victim-tenant")
	req.URL.RawQuery = q.Encode()
	if got := tenantFromQueryOrContext(req); got != "own-tenant" {
		t.Errorf("non-super role must be pinned to own tenant, got %q", got)
	}

	// super_admin 显式指定目标租户仍合法。
	super := httptest.NewRequest("POST", "/api/admin/approvals/test/resume", nil)
	super = SetAuthContext(super, &AuthContext{TenantID: "own-tenant", Role: "super_admin"})
	super.Header.Set("X-Tenant-ID", "target-tenant")
	if got := tenantFromQueryOrContext(super); got != "target-tenant" {
		t.Errorf("super_admin explicit override expected, got %q", got)
	}
}
