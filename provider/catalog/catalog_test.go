package catalog

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidate_OK(t *testing.T) {
	e := NewEntry("anthropic")
	e.Tier = Tier1
	e.Protocol = ProtocolAnthropicMessages
	e.BaseURLTemplate = "https://api.anthropic.com"
	e.DisplayName = "Anthropic"
	e.VendorName = "Anthropic"
	if err := Validate(e); err != nil {
		t.Fatalf("expected OK, got %v", err)
	}
}

func TestValidate_LocalAllowsHTTP(t *testing.T) {
	e := NewEntry("ollama")
	e.Tier = TierLocal
	e.Kind = KindLocal
	e.Protocol = ProtocolOllamaNative
	e.BaseURLTemplate = "http://localhost:11434"
	e.DisplayName = "Ollama"
	if err := Validate(e); err != nil {
		t.Fatalf("local http should be OK, got %v", err)
	}
}

func TestValidate_Rejects(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(CatalogEntry) CatalogEntry
		wantSub string
	}{
		{"empty code", func(e CatalogEntry) CatalogEntry { e.Code = ""; return e }, "code is empty"},
		{"unknown protocol", func(e CatalogEntry) CatalogEntry { e.Protocol = "grpc"; return e }, "unknown protocol"},
		{"unknown tier", func(e CatalogEntry) CatalogEntry { e.Tier = "tier9"; return e }, "unknown tier"},
		{"unknown kind", func(e CatalogEntry) CatalogEntry { e.Kind = "edge"; return e }, "unknown kind"},
		{"unknown category", func(e CatalogEntry) CatalogEntry { e.Category = "shady"; return e }, "unknown category"},
		{"unknown discovery", func(e CatalogEntry) CatalogEntry { e.DiscoveryStrategy = "magic"; return e }, "unknown discovery_strategy"},
		{"cloud http rejected", func(e CatalogEntry) CatalogEntry { e.BaseURLTemplate = "http://api.x.com"; return e }, "must be https://"},
		{"empty base url", func(e CatalogEntry) CatalogEntry { e.BaseURLTemplate = ""; return e }, "base_url_template is empty"},
		{"empty display name", func(e CatalogEntry) CatalogEntry { e.DisplayName = ""; return e }, "display_name is empty"},
		{"sk- secret in base url", func(e CatalogEntry) CatalogEntry { e.BaseURLTemplate = "https://api.x.com/?api_key=sk-abc"; return e }, "likely secret"},
		{"Bearer in notes", func(e CatalogEntry) CatalogEntry { e.Notes = "Bearer abc123"; return e }, "likely secret"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			e := NewEntry("test-provider")
			e.Tier = Tier1
			e.Protocol = ProtocolOpenAICompletions
			e.BaseURLTemplate = "https://api.test.com"
			e.DisplayName = "Test"
			e = tc.mutate(e)
			err := Validate(e)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("expected error containing %q, got %q", tc.wantSub, err.Error())
			}
		})
	}
}

func TestValidateSet_DuplicateCode(t *testing.T) {
	mk := func(code string) CatalogEntry {
		e := NewEntry(code)
		e.Tier = Tier1
		e.Protocol = ProtocolOpenAICompletions
		e.BaseURLTemplate = "https://api.test.com"
		e.DisplayName = "Test"
		return e
	}
	entries := []CatalogEntry{mk("dup"), mk("dup")}
	err := ValidateSet(entries)
	if err == nil {
		t.Fatal("expected duplicate error, got nil")
	}
	if !strings.Contains(err.Error(), "duplicate catalog code") {
		t.Fatalf("expected duplicate error, got %v", err)
	}
}

func TestGenerateSeed_IdempotentShape(t *testing.T) {
	e := NewEntry("anthropic")
	e.Tier = Tier1
	e.Protocol = ProtocolAnthropicMessages
	e.BaseURLTemplate = "https://api.anthropic.com"
	e.DisplayName = "Anthropic"
	e.VendorName = "Anthropic"
	e.ModelsManifestJSON = json.RawMessage(`[{"id":"claude-3-5-sonnet"}]`)

	sql := GenerateSeed([]CatalogEntry{e})
	checks := []string{
		"INSERT INTO public.provider_catalog (",
		"ON CONFLICT (code) DO UPDATE SET",
		"updated_at = now()",
		"'anthropic'",          // VALUES 里 code 字面量
		"'anthropic-messages'", // VALUES 里 protocol 字面量
		"https://api.anthropic.com",
		"protocol = EXCLUDED.protocol",
	}
	for _, c := range checks {
		if !strings.Contains(sql, c) {
			t.Errorf("generated seed missing %q\n---\n%s", c, sql)
		}
	}
	// 不得写死时间戳（必须用 now()）。
	if strings.Contains(sql, "2026-") {
		t.Errorf("generated seed should use now(), not hardcoded timestamp\n%s", sql)
	}
	// 不得用 positional VALUES（无列名）。
	if strings.Contains(sql, ") VALUES ('anthropic'") {
		t.Errorf("generated seed should use explicit column list, not positional VALUES")
	}
}

func TestGenerateSeed_EmptyJSONBecomesNull(t *testing.T) {
	e := NewEntry("x")
	e.Tier = Tier1
	e.Protocol = ProtocolOpenAICompletions
	e.BaseURLTemplate = "https://x.com"
	e.DisplayName = "X"
	e.ModelsManifestJSON = nil // 空
	sql := GenerateSeed([]CatalogEntry{e})
	// nil RawMessage → NULL（DB DEFAULT '[]' 兜底）
	if !strings.Contains(sql, "NULL") {
		t.Errorf("nil JSONB should become NULL, got:\n%s", sql)
	}
}
