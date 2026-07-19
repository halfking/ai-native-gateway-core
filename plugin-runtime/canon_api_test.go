package pluginruntime

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestVerifyPluginContext_Valid(t *testing.T) {
	secret := []byte("s")
	ts := time.Now().Unix()
	called := false
	h := VerifyPluginContext(secret, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if TenantFromVerified(r) != "t-1" {
			t.Fatalf("tenant = %s", TenantFromVerified(r))
		}
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/_gateway/plugin/v1/sessions", nil)
	signAllHeaders(t, req.Header, secret, "ai-session-manager", "t-1", ts)
	h.ServeHTTP(rec, req)
	if !called {
		t.Fatal("handler not called")
	}
}

func TestVerifyPluginContext_RejectsBadSig(t *testing.T) {
	h := VerifyPluginContext([]byte("s"), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not call handler")
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Gateway-Plugin-ID", "p")
	req.Header.Set("X-Gateway-Tenant-ID", "t")
	req.Header.Set("X-Gateway-Context-Timestamp", strconv.FormatInt(time.Now().Unix(), 10))
	req.Header.Set("X-Gateway-Context-Nonce", "n")
	req.Header.Set("X-Gateway-Context-Signature", "bogus")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestVerifyPluginContext_RejectsExpired(t *testing.T) {
	secret := []byte("s")
	ts := time.Now().Add(-10 * time.Minute).Unix()
	h := VerifyPluginContext(secret, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not call handler")
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	signAllHeaders(t, req.Header, secret, "p", "t", ts)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

// signAllHeaders writes the 5 signed-context headers using the cross-module HMAC contract.
func signAllHeaders(t *testing.T, h http.Header, secret []byte, pluginID, tenantID string, ts int64) {
	t.Helper()
	nonce := "n"
	msg := fmt.Sprintf("%s|%s|%d|%s", pluginID, tenantID, ts, nonce)
	// reuse the package's signContext (HMAC-SHA256 hex) — same algorithm
	sig := signContext(secret, pluginID, tenantID, ts, nonce)
	h.Set("X-Gateway-Plugin-ID", pluginID)
	h.Set("X-Gateway-Tenant-ID", tenantID)
	h.Set("X-Gateway-Context-Timestamp", strconv.FormatInt(ts, 10))
	h.Set("X-Gateway-Context-Nonce", nonce)
	h.Set("X-Gateway-Context-Signature", sig)
	_ = msg
}
