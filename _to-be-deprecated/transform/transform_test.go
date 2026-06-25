
package transform

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTestYAML(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "transform.yaml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadNonExistentPath(t *testing.T) {
	m := New("/nonexistent/path.yaml")
	if m == nil {
		t.Fatal("New should not return nil")
	}
	// Should not panic
	ctx := &TransformContext{ClientModel: "gpt-4o", ClientProfile: "default"}
	res := m.Resolve(ctx)
	if res == nil {
		t.Fatal("Resolve should not return nil")
	}
}

func TestBasicRuleMatch(t *testing.T) {
	yaml := `
defaults:
  request_mode: chat
  client: default
rules:
  - match:
      client: roocode
      mode: chat
    egress_preference: ["anthropic-messages"]
    outbound_model: "claude-sonnet-4-6"
`
	path := writeTestYAML(t, yaml)
	m := New(path)

	ctx := &TransformContext{
		ClientProfile: "roocode",
		RequestMode:   "chat",
		ClientModel:   "claude-sonnet-4-6",
	}
	res := m.Resolve(ctx)
	if res.MatchedRule != "rule_0" {
		t.Fatalf("expected rule_0, got %q", res.MatchedRule)
	}
	if len(res.EgressPreference) != 1 || res.EgressPreference[0] != "anthropic-messages" {
		t.Fatalf("unexpected egress: %v", res.EgressPreference)
	}
	if res.OutboundModel != "claude-sonnet-4-6" {
		t.Fatalf("unexpected outbound: %q", res.OutboundModel)
	}
}

func TestNoMatchFallsThroughToDefaults(t *testing.T) {
	yaml := `
defaults:
  request_mode: chat
  client: default
rules:
  - match:
      client: cursor
    egress_preference: ["openai-completions"]
`
	path := writeTestYAML(t, yaml)
	m := New(path)

	ctx := &TransformContext{
		ClientProfile: "unknown-client",
		RequestMode:   "chat",
	}
	res := m.Resolve(ctx)
	if res.MatchedRule != "" {
		t.Fatalf("expected no match, got %q", res.MatchedRule)
	}
	if len(res.EgressPreference) != 1 || res.EgressPreference[0] != "openai-completions" {
		t.Fatalf("unexpected default egress: %v", res.EgressPreference)
	}
}

func TestOutboundModelMap(t *testing.T) {
	yaml := `
defaults:
  request_mode: chat
rules:
  - match:
      client: roocode
    outbound_model_map:
      claude-sonnet-4-6: "claude-sonnet-4-6-20260523"
      gpt-4o: "azure/gpt-4o-2026-01-01"
`
	path := writeTestYAML(t, yaml)
	m := New(path)

	ctx := &TransformContext{
		ClientProfile:  "roocode",
		CanonicalName:  "claude-sonnet-4.6",
	}
	res := m.Resolve(ctx)
	if res.OutboundModel != "" {
		t.Fatalf("model map uses canonical not client_model, expected empty, got %q", res.OutboundModel)
	}

	ctx2 := &TransformContext{
		ClientProfile:  "roocode",
		ClientModel:    "claude-sonnet-4-6",
		CanonicalName:  "claude-sonnet-4-6",
	}
	res2 := m.Resolve(ctx2)
	if res2.OutboundModel != "claude-sonnet-4-6-20260523" {
		t.Fatalf("expected mapped model, got %q", res2.OutboundModel)
	}
}

func TestInjectHeaders(t *testing.T) {
	yaml := `
rules:
  - match:
      client: openclaw
    inject_headers:
      X-Provider: openai
      X-Custom: value
`
	path := writeTestYAML(t, yaml)
	m := New(path)

	ctx := &TransformContext{ClientProfile: "openclaw"}
	res := m.Resolve(ctx)
	if res.InjectHeaders["X-Provider"] != "openai" {
		t.Fatalf("expected injected header X-Provider=openai, got %q", res.InjectHeaders["X-Provider"])
	}
	if res.InjectHeaders["X-Custom"] != "value" {
		t.Fatalf("expected injected header X-Custom=value")
	}
}

func TestNormalizeMode(t *testing.T) {
	m := New("")
	tests := []struct{ in, expected string }{
		{"", "chat"},
		{"chat", "chat"},
		{"coder", "coder"},
		{"unknown", "chat"},
		{"  AGENT  ", "agent"},
	}
	for _, tc := range tests {
		got := m.NormalizeMode(tc.in)
		if got != tc.expected {
			t.Errorf("NormalizeMode(%q) = %q, want %q", tc.in, got, tc.expected)
		}
	}
}

func TestNormalizeClient(t *testing.T) {
	m := New("")
	tests := []struct{ in, expected string }{
		{"", "default"},
		{"roocode", "roocode"},
		{"CURSOR", "cursor"},
		{"unknown", "default"},
	}
	for _, tc := range tests {
		got := m.NormalizeClient(tc.in)
		if got != tc.expected {
			t.Errorf("NormalizeClient(%q) = %q, want %q", tc.in, got, tc.expected)
		}
	}
}

func TestRenderOutboundModel(t *testing.T) {
	tests := []struct {
		name     string
		template string
		offOut   string
		offRaw   string
		canon    string
		expected string
	}{
		{"empty template", "", "outbound-v1", "raw-v1", "", "outbound-v1"},
		{"simple replace", "prefix/{offer.outbound_model_name}/suffix", "gpt-4o", "gpt-4o-latest", "", "prefix/gpt-4o/suffix"},
		{"fallback syntax", "{offer.outbound_model_name|default-model}", "", "raw", "", "default-model"},
		{"multiple vars", "{canon}/{offer.outbound_model_name}", "out", "raw", "gpt-4o", "gpt-4o/out"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := RenderOutboundModel(tc.template, tc.offOut, tc.offRaw, tc.canon)
			if got != tc.expected {
				t.Errorf("RenderOutboundModel() = %q, want %q", got, tc.expected)
			}
		})
	}
}
