package pluginruntime

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNavAPI_ReturnsEntries(t *testing.T) {
	r := NewRegistry()
	r.SetPlugin(&PluginState{PluginID: "p1", PluginVersion: "0.1", Status: "ready"})
	r.SetNav("p1", "0.1", []Page{{Path: "sessions", Type: "data", Nav: &Nav{Group: "requests-sessions", LabelKey: "k", Order: 1}}})

	h := NavHandler(r, func(*http.Request) ViewerOpts {
		return ViewerOpts{IsSuper: true}
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/plugin-nav", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var resp struct {
		Items []NavEntry `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Items) != 1 || resp.Items[0].PluginID != "p1" {
		t.Fatalf("items = %+v", resp.Items)
	}
}
