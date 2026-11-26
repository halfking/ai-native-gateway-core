package admin

import (
	"net/http/httptest"
	"testing"
)

// TestHandleMySessions_RoleBasedAccess 测试会话列表的角色访问控制
func TestHandleMySessions_RoleBasedAccess(t *testing.T) {
	tests := []struct {
		name           string
		auth           *AuthContext
		expectRejected bool
	}{
		{
			name:           "普通用户可以访问",
			auth:           &AuthContext{UserID: 42, Role: RoleUser, TenantID: "acme", Username: "alice"},
			expectRejected: false,
		},
		{
			name:           "tenant_admin 可以访问",
			auth:           &AuthContext{UserID: 10, Role: RoleTenantAdmin, TenantID: "acme", Username: "admin"},
			expectRejected: false,
		},
		{
			name:           "super_admin 可以访问",
			auth:           &AuthContext{UserID: 1, Role: RoleSuperAdmin, TenantID: "default", Username: "root"},
			expectRejected: false,
		},
		{
			name:           "未认证用户被拒绝",
			auth:           nil,
			expectRejected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest("GET", "/api/sessions/my", nil)
			
			if tt.auth != nil {
				r = SetAuthContext(r, tt.auth)
			}

			// 模拟 handler（实际测试需要完整的 Handler 和 DB mock）
			rejected := RequireUserOrHigher(w, r)
			
			if rejected != tt.expectRejected {
				t.Errorf("RequireUserOrHigher() rejected=%v, want %v", rejected, tt.expectRejected)
			}
			
			if tt.expectRejected && w.Code != 401 {
				t.Errorf("期望 HTTP 401，实际 %d", w.Code)
			}
		})
	}
}

// TestHandleMySessions_UserFiltering 测试用户级数据过滤逻辑
func TestHandleMySessions_UserFiltering(t *testing.T) {
	tests := []struct {
		name         string
		auth         *AuthContext
		expectUserID int // 0 表示不过滤（admin），>0 表示过滤到指定用户
	}{
		{
			name:         "普通用户只看自己",
			auth:         &AuthContext{UserID: 42, Role: RoleUser, TenantID: "acme"},
			expectUserID: 42,
		},
		{
			name:         "tenant_admin 看本租户全部",
			auth:         &AuthContext{UserID: 10, Role: RoleTenantAdmin, TenantID: "acme"},
			expectUserID: 0,
		},
		{
			name:         "super_admin 看全平台",
			auth:         &AuthContext{UserID: 1, Role: RoleSuperAdmin, TenantID: "default"},
			expectUserID: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/api/sessions/my", nil)
			r = SetAuthContext(r, tt.auth)

			userID := EffectiveUserID(r)
			if userID != tt.expectUserID {
				t.Errorf("EffectiveUserID() = %d, want %d", userID, tt.expectUserID)
			}
		})
	}
}

// TestHandleMyUsage_RoleBasedAccess 测试用量统计的角色访问控制
func TestHandleMyUsage_RoleBasedAccess(t *testing.T) {
	tests := []struct {
		name           string
		auth           *AuthContext
		expectRejected bool
	}{
		{
			name:           "普通用户可以访问",
			auth:           &AuthContext{UserID: 42, Role: RoleUser, TenantID: "acme"},
			expectRejected: false,
		},
		{
			name:           "tenant_admin 可以访问",
			auth:           &AuthContext{UserID: 10, Role: RoleTenantAdmin, TenantID: "acme"},
			expectRejected: false,
		},
		{
			name:           "未认证用户被拒绝",
			auth:           nil,
			expectRejected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest("GET", "/api/usage/my", nil)
			
			if tt.auth != nil {
				r = SetAuthContext(r, tt.auth)
			}

			rejected := RequireUserOrHigher(w, r)
			
			if rejected != tt.expectRejected {
				t.Errorf("RequireUserOrHigher() rejected=%v, want %v", rejected, tt.expectRejected)
			}
		})
	}
}

// TestMyEndpoints_TenantIsolation 测试租户隔离
func TestMyEndpoints_TenantIsolation(t *testing.T) {
	// 验证租户ID正确获取
	auth := &AuthContext{
		UserID:   42,
		Role:     RoleUser,
		TenantID: "acme",
		Username: "alice",
	}

	r := httptest.NewRequest("GET", "/api/sessions/my", nil)
	r = SetAuthContext(r, auth)

	tenantID := GetTenantID(r)
	if tenantID != "acme" {
		t.Errorf("GetTenantID() = %s, want acme", tenantID)
	}

	// 验证用户不能跨租户访问
	otherTenantID := "other-tenant"
	if !RequireTenantMatch(httptest.NewRecorder(), r, otherTenantID) {
		t.Error("期望跨租户访问被拒绝")
	}
}
