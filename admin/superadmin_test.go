package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// fakeVerifyAdminAuthStubs is a small interface for testing the middleware.
// In real code, verifyAdminAuth is a closure that queries DB; for tests we
// can't easily stub it, so the test focuses on SuperAdminMiddleware logic
// (JWT role check) which is the new critical path.
func TestSuperAdminMiddleware_RejectsTenantAdminJWT(t *testing.T) {
	// Build a JWT with role=tenant_admin and a known secret
	token, _, err := SignToken(42, "default", "testuser", "tenant_admin", "test-secret")
	if err != nil {
		t.Fatalf("SignToken: %v", err)
	}

	var nextCalled bool; _ = nextCalled
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusOK)
	})

	mw := SuperAdminMiddleware(next, nil, "test-secret")
	req := httptest.NewRequest("GET", "/api/users", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	mw(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403 for tenant_admin JWT, got %d body=%s", rr.Code, rr.Body.String())
	}
	if nextCalled {
		t.Error("next handler should not have been called")
	}
}

func TestSuperAdminMiddleware_AllowsSuperAdminJWT(t *testing.T) {
	token, _, err := SignToken(1, "default", "admin", "super_admin", "test-secret")
	if err != nil {
		t.Fatalf("SignToken: %v", err)
	}

	var nextCalled bool; _ = nextCalled
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusOK)
	})

	mw := SuperAdminMiddleware(next, nil, "test-secret")
	req := httptest.NewRequest("GET", "/api/users", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	mw(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200 for super_admin JWT, got %d body=%s", rr.Code, rr.Body.String())
	}
	if !nextCalled {
		t.Error("next handler should have been called")
	}
}

func TestSuperAdminMiddleware_RejectsInvalidJWT(t *testing.T) {
	var nextCalled bool; _ = nextCalled
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
	})
	_ = next

	mw := SuperAdminMiddleware(next, nil, "test-secret")
	req := httptest.NewRequest("GET", "/api/users", nil)
	req.Header.Set("Authorization", "Bearer invalid.token.here")
	rr := httptest.NewRecorder()
	mw(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for invalid JWT, got %d", rr.Code)
	}
	if nextCalled {
		t.Error("next handler should not have been called")
	}
}

func TestSuperAdminMiddleware_RejectsNoAuth(t *testing.T) {
	var nextCalled bool; _ = nextCalled
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
	})

	mw := SuperAdminMiddleware(next, nil, "test-secret")
	req := httptest.NewRequest("GET", "/api/users", nil)
	rr := httptest.NewRecorder()
	mw(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for no auth, got %d", rr.Code)
	}
}

func TestAdminMiddleware_AllowsTenantAdminJWT(t *testing.T) {
	token, _, err := SignToken(42, "default", "testuser", "tenant_admin", "test-secret")
	if err != nil {
		t.Fatalf("SignToken: %v", err)
	}

	var nextCalled bool; _ = nextCalled
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
	})

	mw := AdminMiddleware(next, nil, "test-secret")
	req := httptest.NewRequest("GET", "/api/keys", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	mw(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200 for tenant_admin in admin middleware, got %d body=%s", rr.Code, rr.Body.String())
	}
	if !nextCalled {
		t.Error("next handler should have been called")
	}
}

func TestAdminMiddleware_RejectsExpiredJWT(t *testing.T) {
	// Sign a token that expires immediately by setting expiry to 1ns
	//nolint:errcheck // test env, non-critical
	os.Setenv("LLM_GATEWAY_JWT_EXPIRY", "1ns")
	//nolint:errcheck // test env, non-critical
	defer os.Unsetenv("LLM_GATEWAY_JWT_EXPIRY")

	token, _, err := SignToken(42, "default", "testuser", "tenant_admin", "test-secret")
	if err != nil {
		t.Fatalf("SignToken: %v", err)
	}
	// Sleep so the token expires
	time.Sleep(10 * time.Millisecond)

	var nextCalled bool; _ = nextCalled
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
	})

	mw := AdminMiddleware(next, nil, "test-secret")
	req := httptest.NewRequest("GET", "/api/keys", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	mw(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for expired JWT, got %d body=%s", rr.Code, rr.Body.String())
	}
	if nextCalled {
		t.Error("next handler should not have been called")
	}
}

func TestRateLimiter_Allows5ThenBlocks(t *testing.T) {
	rl := NewRateLimiter(5, time.Minute)
	for i := 1; i <= 5; i++ {
		if !rl.Allow("192.168.1.1") {
			t.Errorf("attempt %d: should be allowed", i)
		}
	}
	// 6th attempt should be blocked
	if rl.Allow("192.168.1.1") {
		t.Error("6th attempt should be blocked")
	}
	// Different IP should be allowed
	if !rl.Allow("192.168.1.2") {
		t.Error("different IP should be allowed")
	}
}

func TestRateLimiter_Reset(t *testing.T) {
	rl := NewRateLimiter(2, time.Minute)
	rl.Allow("1.1.1.1")
	rl.Allow("1.1.1.1")
	if rl.Allow("1.1.1.1") {
		t.Error("3rd should be blocked")
	}
	rl.Reset("1.1.1.1")
	if !rl.Allow("1.1.1.1") {
		t.Error("after reset should be allowed again")
	}
}

func TestRateLimiter_WindowExpires(t *testing.T) {
	rl := NewRateLimiter(1, 50*time.Millisecond)
	if !rl.Allow("k") {
		t.Error("1st should be allowed")
	}
	if rl.Allow("k") {
		t.Error("2nd should be blocked")
	}
	time.Sleep(60 * time.Millisecond)
	if !rl.Allow("k") {
		t.Error("after window, should be allowed again")
	}
}

func TestClientIPFromRequest(t *testing.T) {
	tests := []struct {
		name    string
		headers map[string]string
		addr    string
		want    string
	}{
		{"xff preferred", map[string]string{"X-Forwarded-For": "10.0.0.1, 10.0.0.2"}, "1.1.1.1:1234", "10.0.0.1"},
		{"xri fallback", map[string]string{"X-Real-IP": "10.0.0.3"}, "1.1.1.1:1234", "10.0.0.3"},
		{"remote addr fallback", nil, "1.1.1.1:1234", "1.1.1.1:1234"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/", nil)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}
			req.RemoteAddr = tt.addr
			got := clientIPFromRequest(req)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAuthContextEffectiveTenantID(t *testing.T) {
	tests := []struct {
		name string
		auth *AuthContext
		want string
	}{
		{"nil returns default", nil, "default"},
		{"tenant_admin scoped", &AuthContext{Role: "tenant_admin", TenantID: "acme"}, "acme"},
		{"super_admin gets default", &AuthContext{Role: "super_admin", TenantID: "default"}, "default"},
		{"admin_key gets default", &AuthContext{Role: "admin_key", TenantID: "default"}, "default"},
		{"empty tenant_id returns default", &AuthContext{Role: "tenant_admin", TenantID: ""}, "default"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/", nil)
			if tt.auth != nil {
				req = SetAuthContext(req, tt.auth)
			}
			if got := EffectiveTenantID(req); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAuditLogCtxFallsBackToUnknown(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	if got := GetAuthContext(req); got != nil {
		t.Errorf("expected nil AuthContext, got %+v", got)
	}
	// No auth context means actor is "unknown" — but we can't actually
	// call auditLog() without DB. This test just verifies the nil case.
}

// Reference imports to keep the compiler happy when we don't use them
// in all test functions above.
var _ = context.Background
var _ = json.Marshal
var _ = pgxpool.New
