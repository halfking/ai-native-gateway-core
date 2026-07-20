package pluginruntime

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestPluginAPIProxy_ForwardsWithSignedContext(t *testing.T) {
	var gotPluginID, gotTenant, gotSig string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPluginID = r.Header.Get("X-Gateway-Plugin-ID")
		gotTenant = r.Header.Get("X-Gateway-Tenant-ID")
		gotSig = r.Header.Get("X-Gateway-Context-Signature")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "plugin-response")
	}))
	defer upstream.Close()

	secret := []byte("proxy-secret")
	h := PluginAPIProxy(upstream.URL, secret)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/plugins/ai-session-manager/api/v1/sessions", nil)
	req.SetPathValue("pluginId", "ai-session-manager")
	req.Header.Set("X-Caller-Tenant", "t-77")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "plugin-response") {
		t.Fatalf("body = %s", rec.Body.String())
	}
	if gotPluginID != "ai-session-manager" {
		t.Fatalf("upstream plugin id = %q", gotPluginID)
	}
	if gotTenant != "t-77" {
		t.Fatalf("upstream tenant = %q, want t-77", gotTenant)
	}
	if gotSig == "" {
		t.Fatal("upstream missing signature header")
	}
}

func TestPluginAPIProxy_StripsApiPrefix(t *testing.T) {
	var seenPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	h := PluginAPIProxy(upstream.URL, []byte("s"))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/plugins/ai-session-manager/api/v1/sessions", nil)
	req.SetPathValue("pluginId", "ai-session-manager")
	req.Header.Set("X-Caller-Tenant", "t")
	h.ServeHTTP(rec, req)
	// upstream should receive /v1/sessions (the /plugins/{id}/api prefix stripped)
	if seenPath != "/v1/sessions" {
		t.Fatalf("upstream path = %q, want /v1/sessions", seenPath)
	}
}

func TestPluginAPIProxy_DialsUnixSocket(t *testing.T) {
	// Use a short path under /tmp: on macOS the sun_path limit (~104 bytes)
	// means t.TempDir()'s deep path produces "bind: invalid argument".
	socketPath := fmt.Sprintf("/tmp/p5-sock-%d-%d.sock", os.Getpid(), time.Now().UnixNano())
	os.Remove(socketPath)
	defer os.Remove(socketPath)

	var gotPluginID, gotTenant string
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPluginID = r.Header.Get("X-Gateway-Plugin-ID")
		gotTenant = r.Header.Get("X-Gateway-Tenant-ID")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "from-socket")
	})}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	defer server.Close()
	go server.Serve(listener)

	h := PluginAPIProxy("unix://"+socketPath, []byte("s"))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/plugins/ai-session-manager/api/plugin/handshake", nil)
	req.SetPathValue("pluginId", "ai-session-manager")
	req.Header.Set("X-Caller-Tenant", "t-socket")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "from-socket") {
		t.Fatalf("body = %s", rec.Body.String())
	}
	if gotPluginID != "ai-session-manager" || gotTenant != "t-socket" {
		t.Fatalf("upstream plugin=%q tenant=%q", gotPluginID, gotTenant)
	}
}
