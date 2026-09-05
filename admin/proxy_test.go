package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

func TestProxyNodeViewIncludesFrontendFields(t *testing.T) {
	createdAt := time.Date(2026, 9, 3, 10, 11, 12, 0, time.UTC)
	updatedAt := createdAt.Add(time.Hour)
	node := &proxy.Node{ID: 1, Protocol: proxy.ProtocolHTTP, Server: "127.0.0.1", Port: 8080, CreatedAt: createdAt, UpdatedAt: updatedAt}
	view := toProxyNodeView(node)
	if view.LastHealthCheckAt != nil {
		t.Fatalf("zero last_health_check_at = %v, want nil", view.LastHealthCheckAt)
	}
	if !view.CreatedAt.Equal(createdAt) || !view.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("timestamps = %v/%v, want %v/%v", view.CreatedAt, view.UpdatedAt, createdAt, updatedAt)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	body := string(encoded)
	if !strings.Contains(body, `"last_health_check_at":null`) || !strings.Contains(body, `"created_at":"2026-09-03T10:11:12Z"`) {
		t.Fatalf("node view fields = %s", body)
	}
}
func TestProxySubscriptionViewRedactsSensitiveURLParts(t *testing.T) {
	sub := &proxy.Subscription{
		ID:           9,
		Name:         "private feed",
		SubscribeURL: "https://alice:secret@example.com/path-token/feed?token=opaque&key=another#fragment",
		Status:       "active",
	}
	view := toProxySubscriptionView(sub)
	if view.SubscribeURL != "https://example.com/redacted" {
		t.Fatalf("sanitized URL = %q, want https://example.com/redacted", view.SubscribeURL)
	}
	if view.LastFetchAt != nil {
		t.Fatalf("zero last_fetch_at = %v, want nil", view.LastFetchAt)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	body := string(encoded)
	for _, secret := range []string{"alice", "secret", "path-token", "opaque", "another", "fragment"} {
		if strings.Contains(body, secret) {
			t.Fatalf("subscription view leaked %q: %s", secret, body)
		}
	}
	if !strings.Contains(body, `"last_fetch_at":null`) {
		t.Fatalf("subscription view zero last_fetch_at = %s, want null", body)
	}
}

func TestProxySubscriptionViewRedactsLastError(t *testing.T) {
	sub := &proxy.Subscription{
		SubscribeURL: "https://example.com/path-token/feed?token=query-secret",
		LastError:    "fetch https://example.com/path-token/feed?token=query-secret failed password=body-secret",
	}
	view := toProxySubscriptionView(sub)
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	body := string(encoded)
	for _, secret := range []string{"path-token", "query-secret", "body-secret"} {
		if strings.Contains(body, secret) {
			t.Fatalf("subscription last_error leaked %q: %s", secret, body)
		}
	}
	if !strings.Contains(body, "https://example.com/redacted") || !strings.Contains(body, "[REDACTED]") {
		t.Fatalf("sanitized last_error = %s", body)
	}
}
func TestProxySubscriptionViewsEmptyIsJSONArray(t *testing.T) {
	views := toProxySubscriptionViews(nil)
	encoded, err := json.Marshal(views)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != "[]" {
		t.Fatalf("empty subscription views = %s, want []", encoded)
	}
}

func TestProxySubscriptionResponseEnvelope(t *testing.T) {
	views := toProxySubscriptionViews([]*proxy.Subscription{{ID: 1, SubscribeURL: "https://example.com/feed?token=secret"}})
	body := map[string]any{"items": views, "total": len(views)}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Items []json.RawMessage `json:"items"`
		Total int               `json:"total"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Items == nil || len(decoded.Items) != 1 || decoded.Total != 1 {
		t.Fatalf("envelope = %s, want one-item array and total 1", encoded)
	}
	if strings.Contains(string(encoded), "secret") {
		t.Fatalf("envelope leaked URL query credential: %s", encoded)
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
	for _, status := range []string{"active", "disabled", "error"} {
		if !isValidSubscriptionStatus(status) {
			t.Errorf("subscription status %q should be valid", status)
		}
	}
	if isValidSubscriptionStatus("unhealthy") {
		t.Error("subscription status unhealthy should be invalid")
	}
	for _, status := range []string{"active", "disabled", "unhealthy"} {
		if !isValidNodeStatus(status) {
			t.Errorf("node status %q should be valid", status)
		}
	}
	if isValidNodeStatus("error") {
		t.Error("node status error should be invalid")
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
		{http.MethodDelete, "/api/proxy/nodes/1"},
		{http.MethodGet, "/api/proxy/status"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			rec := httptest.NewRecorder()
			if strings.Contains(tc.path, "subscriptions") {
				h.handleProxySubscriptions(rec, req)
			} else if strings.Contains(tc.path, "status") {
				h.handleProxyStatus(rec, req)
			} else {
				h.handleProxyNodes(rec, req)
			}
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("path %s: status=%d body=%q, want %d",
					tc.path, rec.Code, rec.Body.String(), http.StatusServiceUnavailable)
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
