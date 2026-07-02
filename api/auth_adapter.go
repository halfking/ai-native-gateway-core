package api

import (
	"net/http"

	"github.com/kaixuan/llm-gateway-go/admin"
)

// AdminAuthAdapter adapts admin.Handler's auth methods to the api.AuthService interface.
type AdminAuthAdapter struct{}

// NewAdminAuthAdapter creates a new auth adapter.
func NewAdminAuthAdapter() *AdminAuthAdapter {
	return &AdminAuthAdapter{}
}

// GetTenantID returns the tenant ID from the request context.
func (a *AdminAuthAdapter) GetTenantID(r *http.Request) string {
	return admin.GetTenantID(r)
}

// IsSuperAdmin returns true if the request is from a super admin.
func (a *AdminAuthAdapter) IsSuperAdmin(r *http.Request) bool {
	return admin.IsSuperAdminOrLegacy(r)
}

// GetUserID returns the user ID from the request context.
func (a *AdminAuthAdapter) GetUserID(r *http.Request) string {
	auth := admin.GetAuthContext(r)
	if auth != nil && auth.Username != "" {
		return auth.Username
	}
	return "admin"
}

// CanAccessApproval returns true if the user can access the approval request.
func (a *AdminAuthAdapter) CanAccessApproval(r *http.Request, approvalID string, tenantID string) bool {
	// Super admin can access everything
	if a.IsSuperAdmin(r) {
		return true
	}
	// Regular users can only access their own tenant's approvals
	return a.GetTenantID(r) == tenantID
}
