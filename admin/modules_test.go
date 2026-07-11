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
		"format_conversion", "disguise", "feishu_bot", "wechat_bot", "dingtalk_bot",
		"session_analytics", "memora",
	}

	for _, key := range required {
		if !keys[key] {
			t.Errorf("required module missing: %s", key)
		}
	}
}

func TestMemoraModuleDocumentsIntegrationAndDependencies(t *testing.T) {
	defs := allModuleDefinitions()
	var memora *ModuleDefinition
	for i := range defs {
		if defs[i].Key == "memora" {
			memora = &defs[i]
			break
		}
	}
	if memora == nil {
		t.Fatal("memora module not found")
	}
	if memora.DocsURL != "/docs/session-to-memora-pipeline.md" {
		t.Fatalf("Memora DocsURL = %q", memora.DocsURL)
	}
	if memora.Integration == nil || memora.Integration.Label == "" {
		t.Fatal("memora module must describe its kxmemory integration")
	}
	deps := make(map[string]bool, len(memora.Dependencies))
	for _, dep := range memora.Dependencies {
		deps[dep.Key] = true
	}
	for _, key := range []string{"cache", "compression", "session_analytics"} {
		if !deps[key] {
			t.Errorf("memora dependency %q missing", key)
		}
	}
}

func TestFeishuBotDependencies(t *testing.T) {
	defs := allModuleDefinitions()
	var fb *ModuleDefinition
	for i, m := range defs {
		if m.Key == "feishu_bot" {
			fb = &defs[i]
			break
		}
	}
	if fb == nil {
		t.Fatal("feishu_bot module not found")
	}
	expected := []string{"compression", "cache", "prompt_injection"}
	if len(fb.Dependencies) != len(expected) {
		t.Fatalf("feishu_bot.Dependencies length = %d, want %d", len(fb.Dependencies), len(expected))
	}
	for i, dep := range expected {
		if fb.Dependencies[i].Key != dep {
			t.Errorf("Dependencies[%d].Key = %q, want %q", i, fb.Dependencies[i].Key, dep)
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
	// Register specs so handleModulesTest/Config can find them.
	for _, sp := range settings.ModuleSpecs() {
		_ = settings.Global.RegisterSpec(sp)
	}

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
		{http.MethodPost, "/api/admin/modules/feishu_bot/test", http.StatusBadRequest},           // webhook_url empty in test
		{http.MethodGet, "/api/admin/modules/feishu_bot/config", http.StatusOK},                  // summary works without DB
		{http.MethodPost, "/api/admin/modules/unknown/test", http.StatusNotImplemented},
	}

	for _, tt := range tests {
		req := httptest.NewRequest(tt.method, tt.path, nil)
		w := httptest.NewRecorder()

		h.handleModulesRouter(w, req)

		if w.Code != tt.status {
			t.Errorf("%s %s: expected status %d, got %d (body: %s)",
				tt.method, tt.path, tt.status, w.Code, w.Body.String())
		}
	}
}

func TestFeishuBotConfigSummary(t *testing.T) {
	settings.Global = settings.NewRegistry()
	for _, sp := range settings.ModuleSpecs() {
		_ = settings.Global.RegisterSpec(sp)
	}
	h := &Handler{}

	req := httptest.NewRequest(http.MethodGet, "/api/admin/modules/feishu_bot/config", nil)
	w := httptest.NewRecorder()
	h.feishuBotConfigSummary(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// 关键字段都应存在
	for _, key := range []string{"enabled", "webhook_url_set", "card_template", "alert_severity_min"} {
		if _, ok := resp[key]; !ok {
			t.Errorf("missing field %q in summary", key)
		}
	}
	// 默认值校验
	if resp["card_template"] != "standard" {
		t.Errorf("card_template default = %v, want 'standard'", resp["card_template"])
	}
	if resp["alert_severity_min"] != "high" {
		t.Errorf("alert_severity_min default = %v, want 'high'", resp["alert_severity_min"])
	}
}

// TestHandoffModule_LocksDownContract (2026-07-09, post-merge-audit)
// Regression guard: feat(modules) cascade PR briefly reverted the handoff
// module definition to its pre-expansion 3-config-key form, breaking the
// 19-spec / 4-dependency contract that drives the ModulesView UI groups.
// This test re-asserts the contract after every change to admin/modules.go.
func TestHandoffModule_LocksDownContract(t *testing.T) {
	defs := allModuleDefinitions()
	var h *ModuleDefinition
	for i, m := range defs {
		if m.Key == "handoff" {
			h = &defs[i]
			break
		}
	}
	if h == nil {
		t.Fatal("handoff module not found")
	}

	// Spec surface must match settings/handoff_specs.go (19 keys).
	if got, want := len(h.ConfigKeys), 19; got != want {
		t.Errorf("ConfigKeys = %d, want %d (handoff_specs.go drifted from module definition)", got, want)
	}

	// Capability surface — keep hand-curated content fresh.
	if got := len(h.Capabilities); got < 8 {
		t.Errorf("Capabilities = %d, want >= 8 (handoff is a multi-capability module)", got)
	}

	// Dependency surface: 2 required (compression, cache) + optional goal/session_inspector.
	wantDeps := map[string]bool{
		"compression":       true,
		"cache":             true,
		"goal":              false,
		"session_inspector": false,
	}
	if got, want := len(h.Dependencies), len(wantDeps); got != want {
		t.Errorf("Dependencies = %d, want %d", got, want)
	}
	for _, d := range h.Dependencies {
		expected, known := wantDeps[d.Key]
		if !known {
			t.Errorf("Dependencies has unexpected key %q", d.Key)
			continue
		}
		if d.Required != expected {
			t.Errorf("Dependencies[%s].Required = %v, want %v", d.Key, d.Required, expected)
		}
		if d.Required && d.Key == "compression" {
			// compression is also used by feishu/wechat/dingtalk/disguise/session_inspector —
			// a safe canonical anchor for the cascade. Verify we use it consistently.
			if d.Icon == "" || d.Description == "" {
				t.Errorf("compression dep missing Icon/Description: %+v", d)
			}
		}
	}
}
