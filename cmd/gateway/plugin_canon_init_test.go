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
	noop := func(http.ResponseWriter, *http.Request) {}
	registerPluginCanonRoutes(mux, secret, CanonHandlers{List: upstream, Detail: upstream, Turns: noop})

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

func TestRegisterPluginCanonRoutes_DetailRejectsBadSessionID(t *testing.T) {
	mux := http.NewServeMux()
	upstream := func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("upstream should NOT be called for invalid session id, path=%s", r.URL.Path)
	}
	secret := []byte("s")
	noop := func(http.ResponseWriter, *http.Request) {}
	registerPluginCanonRoutes(mux, secret, CanonHandlers{List: upstream, Detail: upstream, Turns: noop})

	ts := time.Now().Unix()
	req := httptest.NewRequest(http.MethodGet, "/_gateway/plugin/v1/sessions/..%2Fadmin", nil)
	req.Header.Set("X-Gateway-Plugin-ID", "ai-session-manager")
	req.Header.Set("X-Gateway-Tenant-ID", "tenant-A")
	req.Header.Set("X-Gateway-Context-Timestamp", strconv.FormatInt(ts, 10))
	req.Header.Set("X-Gateway-Context-Nonce", "n")
	req.Header.Set("X-Gateway-Context-Signature", signContextForTest(secret, "ai-session-manager", "tenant-A", ts, "n"))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid session id, got %d", rec.Code)
	}
}

func TestRegisterPluginCanonRoutes_TurnsRouteRegistered(t *testing.T) {
	mux := http.NewServeMux()
	var called bool
	var seenTenant, seenQuerySessionID string
	turnsUpstream := func(w http.ResponseWriter, r *http.Request) {
		called = true
		seenTenant = pluginruntime.TenantFromVerified(r)
		seenQuerySessionID = r.URL.Query().Get("session_id")
		w.WriteHeader(http.StatusOK)
	}
	noop := func(http.ResponseWriter, *http.Request) {}
	secret := []byte("s")
	registerPluginCanonRoutes(mux, secret, CanonHandlers{List: noop, Detail: noop, Turns: turnsUpstream})

	ts := time.Now().Unix()
	req := httptest.NewRequest(http.MethodGet, "/_gateway/plugin/v1/sessions/sess-7/turns", nil)
	req.Header.Set("X-Gateway-Plugin-ID", "ai-session-manager")
	req.Header.Set("X-Gateway-Tenant-ID", "tenant-A")
	req.Header.Set("X-Gateway-Context-Timestamp", strconv.FormatInt(ts, 10))
	req.Header.Set("X-Gateway-Context-Nonce", "n")
	req.Header.Set("X-Gateway-Context-Signature", signContextForTest(secret, "ai-session-manager", "tenant-A", ts, "n"))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if !called {
		t.Fatal("turns upstream not called; route not registered or sig failed")
	}
	if seenTenant != "tenant-A" {
		t.Fatalf("turns saw tenant %q, want tenant-A", seenTenant)
	}
	if seenQuerySessionID != "sess-7" {
		t.Fatalf("turns upstream session_id query = %q, want sess-7", seenQuerySessionID)
	}
}

func TestCanonRoutes_AnalyticsEndpoints(t *testing.T) {
	type capture struct {
		tenant string
		called bool
	}
	captures := map[string]*capture{
		"panorama":   {},
		"breakdown":  {},
		"timeseries": {},
		"top":        {},
		"clusters":   {},
	}

	mux := http.NewServeMux()
	secret := []byte("s")
	ts := time.Now().Unix()

	mkUpstream := func(key string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			c := captures[key]
			c.called = true
			c.tenant = pluginruntime.TenantFromVerified(r)
			w.WriteHeader(http.StatusOK)
		}
	}

	registerPluginCanonRoutes(mux, secret, CanonHandlers{
		List:       mkUpstream("list"),
		Detail:     mkUpstream("detail"),
		Turns:      mkUpstream("turns"),
		Panorama:   mkUpstream("panorama"),
		Breakdown:  mkUpstream("breakdown"),
		Timeseries: mkUpstream("timeseries"),
		Top:        mkUpstream("top"),
		Clusters:   mkUpstream("clusters"),
	})

	mkReq := func(path string) *http.Request {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("X-Gateway-Plugin-ID", "asm")
		req.Header.Set("X-Gateway-Tenant-ID", "tenant-A")
		req.Header.Set("X-Gateway-Context-Timestamp", strconv.FormatInt(ts, 10))
		req.Header.Set("X-Gateway-Context-Nonce", "n")
		req.Header.Set("X-Gateway-Context-Signature", signContextForTest(secret, "asm", "tenant-A", ts, "n"))
		return req
	}

	// Panorama (path-rewrite)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, mkReq("/_gateway/plugin/v1/sessions/sess-1/panorama"))
	if !captures["panorama"].called {
		t.Fatal("panorama not called")
	}
	if captures["panorama"].tenant != "tenant-A" {
		t.Fatalf("panorama tenant=%q", captures["panorama"].tenant)
	}

	// Analytics (passthrough)
	for _, tc := range []struct{ key, path string }{
		{"breakdown", "/_gateway/plugin/v1/analytics/breakdown?metric=model"},
		{"timeseries", "/_gateway/plugin/v1/analytics/timeseries"},
		{"top", "/_gateway/plugin/v1/analytics/top?metric=cost"},
		{"clusters", "/_gateway/plugin/v1/analytics/clusters?page=1"},
	} {
		// each request needs a unique nonce for the nonce cache
		req := mkReq(tc.path)
		req.Header.Set("X-Gateway-Context-Nonce", tc.key)
		req.Header.Set("X-Gateway-Context-Signature", signContextForTest(secret, "asm", "tenant-A", ts, tc.key))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if !captures[tc.key].called {
			t.Fatalf("%s not called", tc.key)
		}
		if captures[tc.key].tenant != "tenant-A" {
			t.Fatalf("%s tenant=%q", tc.key, captures[tc.key].tenant)
		}
	}
}
