package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func serviceToken(t *testing.T, secret, issuer, audience, tenant, scope string) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, SessionServiceClaims{
		TenantID: tenant,
		Scope:    scope,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "service:ai-session-manager",
			Issuer:    issuer,
			Audience:  jwt.ClaimStrings{audience},
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(5 * time.Minute)),
		},
	})
	signed, err := token.SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

func TestSessionAnalyticsMiddlewareInjectsTenantContext(t *testing.T) {
	token := serviceToken(t, "service-secret", "ai-session-manager", "llm-gateway-session-analytics", "tenant-a", "session:read")
	var got *AuthContext
	wrapped := SessionAnalyticsMiddleware(func(w http.ResponseWriter, r *http.Request) {
		got = GetAuthContext(r)
		w.WriteHeader(http.StatusNoContent)
	}, nil, "admin-secret", "service-secret", "ai-session-manager", "llm-gateway-session-analytics")

	req := httptest.NewRequest(http.MethodGet, "/api/admin/session-analytics", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp := httptest.NewRecorder()
	wrapped(resp, req)

	if resp.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.Code)
	}
	if got == nil || got.TenantID != "tenant-a" || got.Role != "tenant_admin" || got.Username != "service:ai-session-manager" {
		t.Fatalf("unexpected auth context: %+v", got)
	}
}

func TestSessionAnalyticsMiddlewareRejectsMissingTenantOrScope(t *testing.T) {
	for _, test := range []struct {
		name   string
		tenant string
		scope  string
	}{
		{name: "missing tenant", scope: "session:read"},
		{name: "missing scope", tenant: "tenant-a"},
	} {
		t.Run(test.name, func(t *testing.T) {
			token := serviceToken(t, "service-secret", "ai-session-manager", "llm-gateway-session-analytics", test.tenant, test.scope)
			called := false
			wrapped := SessionAnalyticsMiddleware(func(http.ResponseWriter, *http.Request) { called = true }, nil, "admin-secret", "service-secret", "ai-session-manager", "llm-gateway-session-analytics")
			req := httptest.NewRequest(http.MethodGet, "/api/admin/session-analytics", nil)
			req.Header.Set("Authorization", "Bearer "+token)
			resp := httptest.NewRecorder()
			wrapped(resp, req)
			if resp.Code != http.StatusForbidden || called {
				t.Fatalf("status=%d called=%v, want 403/not called", resp.Code, called)
			}
		})
	}
}

func TestSessionAnalyticsMiddlewareWrongAudienceDoesNotBecomeService(t *testing.T) {
	token := serviceToken(t, "service-secret", "ai-session-manager", "wrong-audience", "tenant-a", "session:read")
	wrapped := SessionAnalyticsMiddleware(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }, nil, "admin-secret", "service-secret", "ai-session-manager", "llm-gateway-session-analytics")
	req := httptest.NewRequest(http.MethodGet, "/api/admin/session-analytics", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp := httptest.NewRecorder()
	wrapped(resp, req)
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.Code)
	}
}
