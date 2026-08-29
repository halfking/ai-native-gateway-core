package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestProxyRuntimeNilDBLifecycle verifies nil-DB lifecycle calls are repeatable
// and do not panic.
func TestProxyRuntimeNilDBLifecycle(t *testing.T) {
	h := NewHandler(nil, "test-secret", nil)

	for i := 0; i < 3; i++ {
		h.StartProxyRuntime()
		h.StopProxyRuntime()
	}
}

// TestProxyRoutesAuth 验证代理管理路由的超级管理员鉴权：
// super_admin 通过中间件；tenant_admin/无 token 被拒绝。
// 这里只测鉴权层，不测实际 handler 逻辑（那些依赖真实 DB/manager）。
func TestProxyRoutesAuth(t *testing.T) {
	secretKey := "test-secret"

	tests := []struct {
		name       string
		role       string
		wantStatus int
	}{
		{"super_admin allowed", "super_admin", 200},
		{"tenant_admin forbidden", "tenant_admin", 403},
		{"no token → 401", "", 401},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/proxy/status", nil)
			if tt.role != "" {
				token, _, err := SignToken(1, "default", "test-user", tt.role, secretKey, false)
				if err != nil {
					t.Fatalf("SignToken: %v", err)
				}
				req.Header.Set("Authorization", "Bearer "+token)
			}

			rec := httptest.NewRecorder()
			// SuperAdminMiddleware 包装一个返回 200 的 handler；鉴权通过才执行
			handler := SuperAdminMiddleware(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}, nil, secretKey)
			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Errorf("got status %d, want %d; body: %s", rec.Code, tt.wantStatus, rec.Body.String())
			}
		})
	}
}
