package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kaixuan/llm-gateway-go/admin"
)

type fakePluginEntitlementAuthorizer struct {
	active   bool
	err      error
	calls    int
	tenantID string
	moduleID string
}

func (f *fakePluginEntitlementAuthorizer) Allowed(_ context.Context, tenantID, moduleID string) (bool, error) {
	f.calls++
	f.tenantID = tenantID
	f.moduleID = moduleID
	return f.active, f.err
}

func TestRegisterPluginAPIProxy_RequiresAuth(t *testing.T) {
	mux := http.NewServeMux()
	registerPluginAPIProxy(mux, []byte("s"), func(string) string { return "http://unix" }, func(string) (string, bool) {
		return "", false
	}, nil, nil, "adminsecret")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/plugins/ai-session-manager/api/plugin/handshake", nil)
	req.SetPathValue("pluginId", "ai-session-manager")
	mux.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatal("unauthenticated request must not reach plugin proxy (got 200)")
	}
}

func TestPluginAPIProxyDeniesInactiveEntitlement(t *testing.T) {
	t.Setenv("LLM_GATEWAY_PLUGIN_ENTITLEMENT_GATE", "true")
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	authorizer := &fakePluginEntitlementAuthorizer{active: false}
	mux := newPluginAPIProxyTestMux(t, upstream.URL, authorizer)

	rec := serveAuthenticatedPluginRequest(t, mux)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
	if upstreamCalls != 0 {
		t.Fatalf("upstream calls = %d, want 0", upstreamCalls)
	}
	if authorizer.calls != 1 || authorizer.tenantID != "tenant-a" || authorizer.moduleID != "session_manager" {
		t.Fatalf("authorizer calls=%d tenant=%q module=%q", authorizer.calls, authorizer.tenantID, authorizer.moduleID)
	}
}

func TestPluginAPIProxyFailsClosedWhenEntitlementUnavailable(t *testing.T) {
	t.Setenv("LLM_GATEWAY_PLUGIN_ENTITLEMENT_GATE", "true")
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	authorizer := &fakePluginEntitlementAuthorizer{err: errors.New("maintain unavailable")}
	mux := newPluginAPIProxyTestMux(t, upstream.URL, authorizer)

	rec := serveAuthenticatedPluginRequest(t, mux)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if upstreamCalls != 0 {
		t.Fatalf("upstream calls = %d, want 0", upstreamCalls)
	}
}

func TestPluginAPIProxyFailsClosedWithoutEntitlementAuthorizer(t *testing.T) {
	t.Setenv("LLM_GATEWAY_PLUGIN_ENTITLEMENT_GATE", "true")
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	mux := newPluginAPIProxyTestMux(t, upstream.URL, nil)

	rec := serveAuthenticatedPluginRequest(t, mux)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if upstreamCalls != 0 {
		t.Fatalf("upstream calls = %d, want 0", upstreamCalls)
	}
}

func TestPluginAPIProxyPassesActiveEntitlement(t *testing.T) {
	t.Setenv("LLM_GATEWAY_PLUGIN_ENTITLEMENT_GATE", "true")
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		if r.URL.Path != "/plugin/handshake" {
			t.Fatalf("upstream path = %q, want /plugin/handshake", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	authorizer := &fakePluginEntitlementAuthorizer{active: true}
	mux := newPluginAPIProxyTestMux(t, upstream.URL, authorizer)

	rec := serveAuthenticatedPluginRequest(t, mux)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if upstreamCalls != 1 {
		t.Fatalf("upstream calls = %d, want 1", upstreamCalls)
	}
}

func TestPluginAPIProxySkipsGateWhenFeatureDisabled(t *testing.T) {
	t.Setenv("LLM_GATEWAY_PLUGIN_ENTITLEMENT_GATE", "false")
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	authorizer := &fakePluginEntitlementAuthorizer{err: errors.New("should not be called")}
	mux := newPluginAPIProxyTestMux(t, upstream.URL, authorizer)

	rec := serveAuthenticatedPluginRequest(t, mux)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if upstreamCalls != 1 {
		t.Fatalf("upstream calls = %d, want 1", upstreamCalls)
	}
	if authorizer.calls != 0 {
		t.Fatalf("authorizer calls = %d, want 0", authorizer.calls)
	}
}

func newPluginAPIProxyTestMux(t *testing.T, upstreamURL string, authorizer pluginEntitlementAuthorizer) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	registerPluginAPIProxy(mux, []byte("proxy-secret"), func(string) string { return upstreamURL }, func(string) (string, bool) {
		return "session_manager", true
	}, authorizer, nil, "adminsecret")
	return mux
}

func serveAuthenticatedPluginRequest(t *testing.T, mux *http.ServeMux) *httptest.ResponseRecorder {
	t.Helper()
	token, _, err := admin.SignToken(1, "tenant-a", "admin", "tenant_admin", "adminsecret", false)
	if err != nil {
		t.Fatalf("sign admin token: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/plugins/plugin-id/api/plugin/handshake", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	mux.ServeHTTP(rec, req)
	return rec
}
