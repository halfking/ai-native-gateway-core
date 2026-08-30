package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/proxy"
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
func TestProxyNodeViewNeverExposesPassword(t *testing.T) {
	node := &proxy.Node{
		ID:             7,
		SubscriptionID: 3,
		Name:           "bridge",
		Protocol:       proxy.ProtocolHTTP,
		Server:         "127.0.0.1",
		Port:           7897,
		Username:       "operator",
		Password:       "super-secret-token",
		Status:         "active",
	}
	view := toProxyNodeView(node)
	if !view.HasPassword || !view.Dialable {
		t.Fatalf("view = %+v, want password marker and dialable node", view)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	body := string(encoded)
	if strings.Contains(body, "password") && strings.Contains(body, "super-secret-token") {
		t.Fatalf("view leaked password: %s", body)
	}
	if strings.Contains(body, "super-secret-token") {
		t.Fatalf("view leaked password value: %s", body)
	}
}

func TestProxyHelpersValidateSafeBoundaries(t *testing.T) {
	for _, raw := range []string{
		"", "ftp://example.com/feed", "https:///missing-host", "relative/path",
	} {
		if err := validateSubscribeURL(raw); err == nil {
			t.Errorf("validateSubscribeURL(%q) unexpectedly succeeded", raw)
		}
	}
	for _, raw := range []string{"http://example.com/feed", "https://example.com/feed?token=opaque"} {
		if err := validateSubscribeURL(raw); err != nil {
			t.Errorf("validateSubscribeURL(%q) = %v", raw, err)
		}
	}
	if got := undialableWarning(0, 0); got != "" {
		t.Fatalf("empty warning = %q", got)
	}
	if got := undialableWarning(3, 1); got != "" {
		t.Fatalf("partial dialable warning = %q", got)
	}
	if got := undialableWarning(3, 0); !strings.Contains(got, "mihomo/xray") {
		t.Fatalf("undialable warning lacks bridge guidance: %q", got)
	}
}

func TestProxyRuntimeDependentRoutesFailClosedWithoutDB(t *testing.T) {
	h := NewHandler(nil, "test-secret", nil)
	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/proxy/subscriptions/1/refresh"},
		{http.MethodPost, "/api/proxy/nodes/1/health-check"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			rec := httptest.NewRecorder()
			if strings.Contains(tc.path, "subscriptions") {
				h.handleProxySubscriptions(rec, req)
			} else {
				h.handleProxyNodes(rec, req)
			}
			if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "database not configured") {
				t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
			}
		})
	}
}

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
