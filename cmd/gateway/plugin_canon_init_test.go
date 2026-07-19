package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/plugin-runtime"
)

func TestRegisterPluginCanonRoutes_DetailRouteRegistered(t *testing.T) {
	mux := http.NewServeMux()
	var called bool
	var seenTenant, seenPath string
	upstream := func(w http.ResponseWriter, r *http.Request) {
		called = true
		seenTenant = pluginruntime.TenantFromVerified(r)
		seenPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}
	secret := []byte("s")
	// list and detail both use the same fake upstream for this routing test
	registerPluginCanonRoutes(mux, secret, upstream, upstream)

	ts := time.Now().Unix()
	sig := signContextForTest(secret, "ai-session-manager", "tenant-A", ts, "n")
	req := httptest.NewRequest(http.MethodGet, "/_gateway/plugin/v1/sessions/sess-42", nil)
	req.Header.Set("X-Gateway-Plugin-ID", "ai-session-manager")
	req.Header.Set("X-Gateway-Tenant-ID", "tenant-A")
	req.Header.Set("X-Gateway-Context-Timestamp", strconv.FormatInt(ts, 10))
	req.Header.Set("X-Gateway-Context-Nonce", "n")
	req.Header.Set("X-Gateway-Context-Signature", sig)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if !called {
		t.Fatal("detail upstream not called; route may not be registered or signature failed")
	}
	if seenTenant != "tenant-A" {
		t.Fatalf("detail saw tenant %q, want tenant-A", seenTenant)
	}
	// Path must be rewritten so admin.HandleSessionAnalyticsDetail's pathSegment can parse it
	if seenPath != "/api/admin/session-analytics/sess-42" {
		t.Fatalf("detail path rewrite wrong: got %q", seenPath)
	}
}

// signContextForTest mirrors pluginruntime.signContext (HMAC-SHA256 hex over "%s|%s|%d|%s").
func signContextForTest(secret []byte, pluginID, tenantID string, ts int64, nonce string) string {
	msg := fmt.Sprintf("%s|%s|%d|%s", pluginID, tenantID, ts, nonce)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(msg))
	return hex.EncodeToString(mac.Sum(nil))
}
