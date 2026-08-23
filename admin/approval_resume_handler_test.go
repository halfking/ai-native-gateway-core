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

func withAuth(req *http.Request, role, tenantID string) *http.Request {
	return SetAuthContext(req, &AuthContext{Role: role, TenantID: tenantID, IsJWT: true})
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

	req := withAuth(httptest.NewRequest("POST", "/api/v1/approvals//resume", nil), "super_admin", "admin-tenant")
	rec := httptest.NewRecorder()

	h.HandleApprovalResume(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", rec.Code)
	}
}

func TestHandleApprovalResume_RequiresAuthentication(t *testing.T) {
	mock := &mockResumeHandler{}
	h := &Handler{approvalResumeHandler: mock}

	req := httptest.NewRequest("POST", "/api/v1/approvals/test-id/resume", nil)
	rec := httptest.NewRecorder()

	h.HandleApprovalResume(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", rec.Code)
	}
	if len(mock.calls) != 0 {
		t.Fatalf("expected no resume call, got %d", len(mock.calls))
	}
}

func TestHandleApprovalResume_RequiresSuperAdmin(t *testing.T) {
	mock := &mockResumeHandler{}
	h := &Handler{approvalResumeHandler: mock}

	req := withAuth(httptest.NewRequest("POST", "/api/v1/approvals/test-id/resume?tenant_id=forged", nil), "tenant_admin", "tenant-a")
	req.Header.Set("X-Tenant-ID", "forged-header")
	rec := httptest.NewRecorder()

	h.HandleApprovalResume(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected status 403, got %d", rec.Code)
	}
	if len(mock.calls) != 0 {
		t.Fatalf("expected no resume call, got %d", len(mock.calls))
	}
}

func TestHandleApprovalResume_Success(t *testing.T) {
	mock := &mockResumeHandler{}
	h := &Handler{approvalResumeHandler: mock}

	req := withAuth(httptest.NewRequest("POST", "/api/v1/approvals/test-approval-id/resume?tenant_id=forged", nil), "super_admin", "admin-tenant")
	req.Header.Set("X-Tenant-ID", "forged-header")
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
	if mock.calls[0].tenantID != "" {
		t.Errorf("expected empty tenant_id for global admin, got %q", mock.calls[0].tenantID)
	}
}

func TestHandleApprovalResume_LegacyAdminIgnoresTenantOverride(t *testing.T) {
	mock := &mockResumeHandler{}
	h := &Handler{approvalResumeHandler: mock}

	req := withAuth(httptest.NewRequest("POST", "/api/v1/approvals/test-approval-id/resume?tenant_id=forged", nil), "admin_key", "admin-tenant")
	req.Header.Set("X-Tenant-ID", "forged-header")
	rec := httptest.NewRecorder()

	h.HandleApprovalResume(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}
	if len(mock.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(mock.calls))
	}
	if mock.calls[0].tenantID != "" {
		t.Errorf("expected empty tenant_id for legacy admin, got %q", mock.calls[0].tenantID)
	}
}

func TestHandleApprovalResume_NotPending(t *testing.T) {
	mock := &mockResumeHandler{
		resumeFunc: func(_ context.Context, _, _ string) error {
			return session.ErrResumeNotPending
		},
	}
	h := &Handler{approvalResumeHandler: mock}

	req := withAuth(httptest.NewRequest("POST", "/api/v1/approvals/test-id/resume", nil), "super_admin", "admin-tenant")
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

	req := withAuth(httptest.NewRequest("POST", "/api/v1/approvals/test-id/resume", nil), "super_admin", "admin-tenant")
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

	req := withAuth(httptest.NewRequest("POST", "/api/v1/approvals/test-id/resume", nil), "super_admin", "admin-tenant")
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

	req := withAuth(httptest.NewRequest("POST", "/api/v1/approvals/test-id/resume", nil), "super_admin", "admin-tenant")
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

	req := withAuth(httptest.NewRequest("POST", "/api/v1/approvals/test-id/resume", nil), "super_admin", "admin-tenant")
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
		{"/api/v1/approvals/abc123/resume", "abc123"},
		{"/api/v1/approvals/test-id-456/resume", "test-id-456"},
		{"/api/admin/approvals/legacy-id/resume", "legacy-id"},
		{"/api/v1/approvals//resume", ""},
		{"/api/v1/approvals/abc123", ""},
		{"/api/other/abc123/resume", ""},
		{"/api/v1/approvals/", ""},
	}

	for _, tt := range tests {
		req := httptest.NewRequest("POST", tt.path, nil)
		got := extractApprovalID(req)
		if got != tt.want {
			t.Errorf("extractApprovalID(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

func TestApprovalCallerTenantIDForResume(t *testing.T) {
	tests := []struct {
		name     string
		role     string
		tenantID string
		want     string
	}{
		{name: "super admin bypass", role: "super_admin", tenantID: "tenant-a", want: ""},
		{name: "legacy admin bypass", role: "admin_key", tenantID: "tenant-a", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := withAuth(httptest.NewRequest("POST", "/api/v1/approvals/test/resume?tenant_id=forged", nil), tt.role, tt.tenantID)
			req.Header.Set("X-Tenant-ID", "forged-header")
			if got := ApprovalCallerTenantID(req); got != tt.want {
				t.Errorf("ApprovalCallerTenantID() = %q, want %q", got, tt.want)
			}
		})
	}
}
