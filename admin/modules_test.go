package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kaixuan/llm-gateway-go/settings"
)

func TestAllModuleDefinitions(t *testing.T) {
	defs := allModuleDefinitions()

	if len(defs) != 18 {
		t.Errorf("expected 18 modules, got %d", len(defs))
	}

	// Check required fields for each module
	for _, m := range defs {
		if m.Key == "" {
			t.Errorf("module missing key: %+v", m)
		}
		if m.Name == "" {
			t.Errorf("module %s missing name", m.Key)
		}
		if m.Icon == "" {
			t.Errorf("module %s missing icon", m.Key)
		}
		if m.Category == "" {
			t.Errorf("module %s missing category", m.Key)
		}
	}

	// Check specific modules exist
	keys := make(map[string]bool)
	for _, m := range defs {
		keys[m.Key] = true
	}

	required := []string{
		"compression", "cache", "handoff", "goal", "audit",
		"prompt_injection", "output_compliance", "session_audit",
		"session_inspector", "security", "rate_limit",
		"format_conversion", "disguise", "feishu_bot", "wechat_bot", "dingtalk_bot", "memora",
	}

	for _, key := range required {
		if !keys[key] {
			t.Errorf("required module missing: %s", key)
		}
	}
}

func TestResolveModuleEnabled(t *testing.T) {
	// Test module without setting key
	m := ModuleDefinition{Key: "test", SettingKey: ""}
	enabled, src := resolveModuleEnabled(m)
	if !enabled || src != "default" {
		t.Errorf("module without setting key should default to enabled=true, source=default, got enabled=%v, source=%s", enabled, src)
	}
}

func TestHandleModulesList(t *testing.T) {
	// Setup mock settings registry
	settings.Global = settings.NewRegistry()

	// Create test handler
	h := &Handler{}

	req := httptest.NewRequest(http.MethodGet, "/api/admin/modules", nil)
	w := httptest.NewRecorder()

	h.handleModulesList(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	items, ok := resp["items"].([]any)
	if !ok {
		t.Fatalf("response missing items array")
	}

	if len(items) != 18 {
		t.Errorf("expected 18 modules in response, got %d", len(items))
	}
}

func TestHandleModulesGet(t *testing.T) {
	settings.Global = settings.NewRegistry()

	h := &Handler{}

	req := httptest.NewRequest(http.MethodGet, "/api/admin/modules/compression", nil)
	w := httptest.NewRecorder()

	h.handleModulesGet(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	module, ok := resp["module"].(map[string]any)
	if !ok {
		t.Fatalf("response missing module object")
	}

	if module["key"] != "compression" {
		t.Errorf("expected module key 'compression', got %v", module["key"])
	}
}

func TestHandleModulesGetNotFound(t *testing.T) {
	settings.Global = settings.NewRegistry()

	h := &Handler{}

	req := httptest.NewRequest(http.MethodGet, "/api/admin/modules/nonexistent", nil)
	w := httptest.NewRecorder()

	h.handleModulesGet(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", w.Code)
	}
}

func TestHandleModulesToggle(t *testing.T) {
	// This test requires full DB setup, so we just test the validation logic
	settings.Global = settings.NewRegistry()

	h := &Handler{} // db is nil, should return 503

	body := map[string]bool{"enabled": false}
	bodyJSON, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPut, "/api/admin/modules/compression/toggle", bytes.NewReader(bodyJSON))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.handleModulesToggle(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected status 503 when DB not available, got %d", w.Code)
	}
}

func TestHandleModulesRouter(t *testing.T) {
	settings.Global = settings.NewRegistry()

	h := &Handler{}

	tests := []struct {
		method string
		path   string
		status int
	}{
		{http.MethodGet, "/api/admin/modules/compression", http.StatusOK},
		{http.MethodGet, "/api/admin/modules/nonexistent", http.StatusNotFound},
		{http.MethodPut, "/api/admin/modules/compression/toggle", http.StatusServiceUnavailable}, // no DB
		{http.MethodPost, "/api/admin/modules/compression/toggle", http.StatusNotFound},          // wrong method
		{http.MethodGet, "/api/admin/modules/", http.StatusNotFound},                             // empty key
	}

	for _, tt := range tests {
		req := httptest.NewRequest(tt.method, tt.path, nil)
		w := httptest.NewRecorder()

		h.handleModulesRouter(w, req)

		if w.Code != tt.status {
			t.Errorf("%s %s: expected status %d, got %d", tt.method, tt.path, tt.status, w.Code)
		}
	}
}
