package admin

import (
	"net/http/httptest"
	"testing"
)

// TestContextEnhanced_EffectiveUserID 测试用户级查询的 user_id 过滤。
func TestContextEnhanced_EffectiveUserID(t *testing.T) {
	tests := []struct {
		name     string
		auth     *AuthContext
		wantID   int
	}{
		{
			name:   "user role returns own user_id",
			auth:   &AuthContext{UserID: 42, Role: RoleUser, TenantID: "acme"},
			wantID: 42,
		},
		{
			name:   "tenant_admin returns 0 (can see all users in tenant)",
			auth:   &AuthContext{UserID: 10, Role: RoleTenantAdmin, TenantID: "acme"},
			wantID: 0,
		},
		{
			name:   "super_admin returns 0 (can see all users)",
			auth:   &AuthContext{UserID: 1, Role: RoleSuperAdmin, TenantID: "default"},
			wantID: 0,
		},
		{
			name:   "nil auth returns 0",
			auth:   nil,
			wantID: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			if tt.auth != nil {
				r = SetAuthContext(r, tt.auth)
			}
			got := EffectiveUserID(r)
			if got != tt.wantID {
				t.Errorf("EffectiveUserID() = %d, want %d", got, tt.wantID)
			}
		})
	}
}

// TestContextEnhanced_CanAccessTenant 测试租户访问权限。
func TestContextEnhanced_CanAccessTenant(t *testing.T) {
	tests := []struct {
		name     string
		auth     *AuthContext
		tenantID string
		want     bool
	}{
		{
			name:     "super_admin can access any tenant",
			auth:     &AuthContext{UserID: 1, Role: RoleSuperAdmin, TenantID: "default"},
			tenantID: "acme",
			want:     true,
		},
		{
			name:     "admin_key (legacy) can access any tenant",
			auth:     &AuthContext{UserID: 0, Role: "admin_key", TenantID: "default"},
			tenantID: "acme",
			want:     true,
		},
		{
			name:     "tenant_admin can access own tenant",
			auth:     &AuthContext{UserID: 10, Role: RoleTenantAdmin, TenantID: "acme"},
			tenantID: "acme",
			want:     true,
		},
		{
			name:     "tenant_admin cannot access other tenant",
			auth:     &AuthContext{UserID: 10, Role: RoleTenantAdmin, TenantID: "acme"},
			tenantID: "other",
			want:     false,
		},
		{
			name:     "user cannot access tenant-level data directly",
			auth:     &AuthContext{UserID: 42, Role: RoleUser, TenantID: "acme"},
			tenantID: "acme",
			want:     false,
		},
		{
			name:     "nil auth cannot access anything",
			auth:     nil,
			tenantID: "acme",
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			if tt.auth != nil {
				r = SetAuthContext(r, tt.auth)
			}
			got := CanAccessTenant(r, tt.tenantID)
			if got != tt.want {
				t.Errorf("CanAccessTenant(%s) = %v, want %v", tt.tenantID, got, tt.want)
			}
		})
	}
}

// TestContextEnhanced_CanAccessUserData 测试用户数据访问权限。
func TestContextEnhanced_CanAccessUserData(t *testing.T) {
	tests := []struct {
		name             string
		auth             *AuthContext
		targetUserID     int
		targetTenantID   string
		want             bool
	}{
		{
			name:           "super_admin can access any user",
			auth:           &AuthContext{UserID: 1, Role: RoleSuperAdmin, TenantID: "default"},
			targetUserID:   99,
			targetTenantID: "acme",
			want:           true,
		},
		{
			name:           "tenant_admin can access users in own tenant",
			auth:           &AuthContext{UserID: 10, Role: RoleTenantAdmin, TenantID: "acme"},
			targetUserID:   42,
			targetTenantID: "acme",
			want:           true,
		},
		{
			name:           "tenant_admin cannot access users in other tenant",
			auth:           &AuthContext{UserID: 10, Role: RoleTenantAdmin, TenantID: "acme"},
			targetUserID:   99,
			targetTenantID: "other",
			want:           false,
		},
		{
			name:           "user can access own data",
			auth:           &AuthContext{UserID: 42, Role: RoleUser, TenantID: "acme"},
			targetUserID:   42,
			targetTenantID: "acme",
			want:           true,
		},
		{
			name:           "user cannot access other user's data",
			auth:           &AuthContext{UserID: 42, Role: RoleUser, TenantID: "acme"},
			targetUserID:   99,
			targetTenantID: "acme",
			want:           false,
		},
		{
			name:           "user cannot access data across tenant boundary",
			auth:           &AuthContext{UserID: 42, Role: RoleUser, TenantID: "acme"},
			targetUserID:   42,
			targetTenantID: "other",
			want:           false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			if tt.auth != nil {
				r = SetAuthContext(r, tt.auth)
			}
			got := CanAccessUserData(r, tt.targetUserID, tt.targetTenantID)
			if got != tt.want {
				t.Errorf("CanAccessUserData(user=%d, tenant=%s) = %v, want %v",
					tt.targetUserID, tt.targetTenantID, got, tt.want)
			}
		})
	}
}

// TestContextEnhanced_RequireUserOrHigher 测试用户认证要求。
func TestContextEnhanced_RequireUserOrHigher(t *testing.T) {
	tests := []struct {
		name         string
		auth         *AuthContext
		wantRejected bool
	}{
		{
			name:         "authenticated user passes",
			auth:         &AuthContext{UserID: 42, Role: RoleUser, TenantID: "acme"},
			wantRejected: false,
		},
		{
			name:         "tenant_admin passes",
			auth:         &AuthContext{UserID: 10, Role: RoleTenantAdmin, TenantID: "acme"},
			wantRejected: false,
		},
		{
			name:         "nil auth is rejected",
			auth:         nil,
			wantRejected: true,
		},
		{
			name:         "legacy admin_key with UserID=0 is rejected",
			auth:         &AuthContext{UserID: 0, Role: "admin_key", TenantID: "default"},
			wantRejected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest("GET", "/", nil)
			if tt.auth != nil {
				r = SetAuthContext(r, tt.auth)
			}
			rejected := RequireUserOrHigher(w, r)
			if rejected != tt.wantRejected {
				t.Errorf("RequireUserOrHigher() rejected=%v, want %v", rejected, tt.wantRejected)
			}
			if tt.wantRejected && w.Code != 401 {
				t.Errorf("expected 401 status, got %d", w.Code)
			}
		})
	}
}

// TestContextEnhanced_RequireTenantMatch 测试租户归属检查。
func TestContextEnhanced_RequireTenantMatch(t *testing.T) {
	tests := []struct {
		name             string
		auth             *AuthContext
		resourceTenantID string
		wantRejected     bool
	}{
		{
			name:             "super_admin can access any tenant",
			auth:             &AuthContext{UserID: 1, Role: RoleSuperAdmin, TenantID: "default"},
			resourceTenantID: "acme",
			wantRejected:     false,
		},
		{
			name:             "tenant_admin can access own tenant",
			auth:             &AuthContext{UserID: 10, Role: RoleTenantAdmin, TenantID: "acme"},
			resourceTenantID: "acme",
			wantRejected:     false,
		},
		{
			name:             "tenant_admin cannot access other tenant",
			auth:             &AuthContext{UserID: 10, Role: RoleTenantAdmin, TenantID: "acme"},
			resourceTenantID: "other",
			wantRejected:     true,
		},
		{
			name:             "user is rejected for wrong tenant",
			auth:             &AuthContext{UserID: 42, Role: RoleUser, TenantID: "acme"},
			resourceTenantID: "other",
			wantRejected:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest("GET", "/", nil)
			if tt.auth != nil {
				r = SetAuthContext(r, tt.auth)
			}
			rejected := RequireTenantMatch(w, r, tt.resourceTenantID)
			if rejected != tt.wantRejected {
				t.Errorf("RequireTenantMatch() rejected=%v, want %v", rejected, tt.wantRejected)
			}
			if tt.wantRejected && w.Code != 403 {
				t.Errorf("expected 403 status, got %d", w.Code)
			}
		})
	}
}
