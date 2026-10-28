package pluginruntime

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestHelloPluginManifest_LoadsAndHasSettings(t *testing.T) {
	p := filepath.Join("testdata", "hello-plugin", "plugin-manifest.json")
	m, err := LoadManifest(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if m.PluginID != "hello" || m.EffectiveAPI() != "^1" {
		t.Fatalf("id/api: %+v", m)
	}
	s := m.EffectiveSettingsSchema()
	if s == nil || s.Properties["greeting"] == nil {
		t.Fatalf("settings: %+v", s)
	}
	if !Compatible(m.EffectiveAPI()) {
		t.Fatal("hello should be API-compatible")
	}
}

func TestSettingsHandler_ReturnsSchema(t *testing.T) {
	m := &Manifest{PluginID: "hello", SettingsSchema: &SettingsSchema{
		Type: "object",
		Properties: map[string]*SettingsProperty{
			"greeting": {Type: "string", TitleKey: "plugin.hello.greeting", Default: "hi"},
		},
		Order: []string{"greeting"},
	}}
	h := SettingsHandler(func(id string) *Manifest {
		if id == "hello" {
			return m
		}
		return nil
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/plugins/hello/settings", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "greeting") {
		t.Fatalf("body=%s", rec.Body.String())
	}
}
