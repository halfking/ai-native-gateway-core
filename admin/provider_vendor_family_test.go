package admin

import "testing"

// TestFamilyForProviderRefresh pins the family mapping that
// discoverAndUpsertForCredential writes into models_canonical when a
// vendor API returns a new model name. The previous implementation
// hard-coded 'unknown' (admin/provider_vendor.go:392) which made the
// /models page family chip filter and the routing_policy.featured_models
// whitelist silently miss every model row the vendor-refresh path
// produced — claude-sonnet-5 / claude-fable-5 on apiclaude shipped with
// family='unknown' and family-tag absent in 252-DB until 2026-07-09.
//
// Contract: family must come from discovery.InferFamily, never a
// hard-coded literal. The discovery package is the single source of
// truth for vendor-prefix collapsing (see discovery/normalize.go
// vendorCanonicalFamilies — 'claude' → 'anthropic-claude').
func TestFamilyForProviderRefresh(t *testing.T) {
	tests := []struct {
		rawModelName string
		wantFamily   string
		why          string
	}{
		// Anthropic: leading "claude" must collapse to "anthropic-claude"
		// — this is the regression case for the claude-sonnet-5 outage.
		{"claude-sonnet-5", "anthropic-claude", "claude-sonnet-5 regression"},
		{"claude-fable-5", "anthropic-claude", "claude-fable-5 regression"},
		{"claude-opus-4-8", "anthropic-claude", "claude-opus-4-8 regression"},
		{"claude-3-5-sonnet-20241022", "anthropic-claude", "date-suffixed name"},
		{"CLAUDE-OPUS-4-6", "anthropic-claude", "case-insensitive"},

		// OpenAI: gpt/o1/o3/o4 all collapse to "openai-gpt".
		{"gpt-4o", "openai-gpt", "gpt prefix"},
		{"o3-mini", "openai-gpt", "o3 prefix"},

		// Bare-token families (1:1 prefix mapping): no collapse.
		{"minimax-m3", "minimax", "minimax stays minimax"},
		{"qwen-max", "qwen", "qwen stays qwen"},
		{"mimo-v2.5-pro", "mimo", "mimo stays mimo"},

		// Unknown vendor: leading token returned as-is so a brand-new
		// vendor doesn't need a code change to be inserted (per
		// discovery/normalize.go:36 "unknown tokens fall through").
		{"kuae-1.0", "kuae", "fallback to bare token"},

		// Defensive: empty name still yields "unknown" (mirrors
		// discovery.InferFamily's behaviour and prevents a NULL family).
		{"", "unknown", "defensive empty"},
	}
	for _, tc := range tests {
		t.Run(tc.rawModelName, func(t *testing.T) {
			got := familyForProviderRefresh(tc.rawModelName)
			if got != tc.wantFamily {
				t.Fatalf("familyForProviderRefresh(%q) = %q, want %q  // %s",
					tc.rawModelName, got, tc.wantFamily, tc.why)
			}
		})
	}
}

// TestFamilyForProviderRefresh_NoLiteralUnknown guards against the
// specific regression where someone re-introduces the hardcoded
// 'unknown' literal that shipped in the original bug. Any "unknown"
// in this path is only permitted when the input itself is empty or
// whitespace; vendor-API-derived names must never go in as 'unknown'.
func TestFamilyForProviderRefresh_NoLiteralUnknownForNonEmpty(t *testing.T) {
	nonEmpty := []string{
		"claude-sonnet-5", "claude-fable-5", "gpt-4o", "minimax-m3",
		"kuae-1.0", "o3-mini", "qwen-max", "glm-5.1",
	}
	for _, name := range nonEmpty {
		got := familyForProviderRefresh(name)
		if got == "unknown" {
			t.Errorf("familyForProviderRefresh(%q) must not be 'unknown' for non-empty vendor name (regression of provider_vendor.go:392)",
				name)
		}
	}
}
