package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRegisterPluginAPIProxy_RequiresAuth(t *testing.T) {
	mux := http.NewServeMux()
	// nil pool: AdminMiddleware's auth path (token verify) does not touch the db for the
	// unauthenticated-rejection branch, so a no-auth request must still be rejected.
	registerPluginAPIProxy(mux, []byte("s"), func(string) string { return "http://unix" }, nil, "adminsecret")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/plugins/ai-session-manager/api/plugin/handshake", nil)
	req.SetPathValue("pluginId", "ai-session-manager")
	mux.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatal("unauthenticated request must not reach plugin proxy (got 200)")
	}
}
