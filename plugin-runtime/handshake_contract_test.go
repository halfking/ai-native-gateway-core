package pluginruntime

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandshakeContext_AcceptsV2Contract(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"plugin_id":"ai-session-manager","plugin_version":"0.2.0","api_contract":"gateway-plugin-v2","status":"ready"}`))
	}))
	defer srv.Close()
	m := &Manifest{
		PluginID: "ai-session-manager", PluginVersion: "0.2.0",
		GatewayCompatibility: GatewayCompatibility{APIContract: SupportedAPIContractV2},
	}
	if _, err := HandshakeContext(t.Context(), srv.Client(), srv.URL, "/plugin/handshake", m); err != nil {
		t.Fatal(err)
	}
}
