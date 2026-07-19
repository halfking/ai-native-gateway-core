package pluginruntime

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestNavAPI_EmptyItemsIsArrayNotNull(t *testing.T) {
	r := NewRegistry() // no plugins
	h := NavHandler(r, func(*http.Request) ViewerOpts { return ViewerOpts{} })
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/plugin-nav", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"items":[]`) {
		t.Fatalf("expected items:[] (not null), got %s", body)
	}
	if strings.Contains(body, `"items":null`) {
		t.Fatalf("items must not be null, got %s", body)
	}
}

func TestNavAPI_NilViewerIsSafe(t *testing.T) {
	r := NewRegistry()
	r.SetPlugin(&PluginState{PluginID: "p", PluginVersion: "1", Status: "ready"})
	r.SetNav("p", "1", []Page{{Path: "s", Type: "data", Nav: &Nav{Group: "g", LabelKey: "k"}}})
	h := NavHandler(r, nil) // nil viewer
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/plugin-nav", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
}
