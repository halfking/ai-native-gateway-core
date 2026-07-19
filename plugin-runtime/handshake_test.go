package pluginruntime

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandshake_Ready(t *testing.T) {
	pluginSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/plugin/handshake" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"plugin_id":"p1","plugin_version":"0.1","api_contract":"gateway-plugin-v1","status":"ready"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer pluginSrv.Close()

	hs, err := Handshake(pluginSrv.Client(), pluginSrv.URL, "/plugin/handshake", &Manifest{PluginID: "p1"})
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if hs.Status != "ready" || hs.PluginID != "p1" {
		t.Fatalf("hs = %+v", hs)
	}
}

func TestHandshake_RejectsContractMismatch(t *testing.T) {
	pluginSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"plugin_id":"p1","api_contract":"weird","status":"ready"}`))
	}))
	defer pluginSrv.Close()
	_, err := Handshake(pluginSrv.Client(), pluginSrv.URL, "/plugin/handshake", &Manifest{PluginID: "p1"})
	if err == nil {
		t.Fatal("expected contract mismatch error")
	}
}

func TestCheckHealth_OK(t *testing.T) {
	pluginSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer pluginSrv.Close()
	if err := CheckHealth(pluginSrv.Client(), pluginSrv.URL, "/plugin/healthz"); err != nil {
		t.Fatalf("health: %v", err)
	}
}
