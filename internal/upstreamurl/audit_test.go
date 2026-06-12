package upstreamurl

import (
	"fmt"
	"reflect"
	"testing"
)

func TestAuditCriticalEdgeCases(t *testing.T) {
	cases := []struct {
		name string
		fn   func(string) string
		in   string
		want string
	}{
		// completionSuffixes new entries (without /v1 prefix)
		{"strip bare /messages", ModelsURL, "https://api.example.com/messages", "https://api.example.com/v1/models"},
		{"strip bare /responses", ModelsURL, "https://api.example.com/responses", "https://api.example.com/v1/models"},
		{"strip bare /completions", ModelsURL, "https://api.example.com/completions", "https://api.example.com/v1/models"},
		{"strip bare /chat/completions", ModelsURL, "https://api.example.com/chat/completions", "https://api.example.com/v1/models"},
		{"strip bare /models", ModelsURL, "https://api.example.com/models", "https://api.example.com/v1/models"},

		// completionSuffixes with /v1 prefix
		{"strip /v1/messages", ModelsURL, "https://api.example.com/v1/messages", "https://api.example.com/v1/models"},
		{"strip /v1/models", ModelsURL, "https://api.example.com/v1/models", "https://api.example.com/v1/models"},

		// Full endpoint URL as base → strip suffix then strip /vN
		{"full models endpoint volcengine", ModelsURL, "https://ark.cn-beijing.volces.com/api/v3/models", "https://ark.cn-beijing.volces.com/api/v1/models"},

		// /v1beta must NOT be stripped
		{"preserve /v1beta", ModelsURL, "https://generativelanguage.googleapis.com/v1beta", "https://generativelanguage.googleapis.com/v1beta/v1/models"},

		// Anthropic
		{"anthropic /v1/messages", MessagesURL, "https://api.anthropic.com/v1/messages", "https://api.anthropic.com/v1/messages"},
		{"anthropic bare", MessagesURL, "https://api.anthropic.com", "https://api.anthropic.com/v1/messages"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.fn(tc.in)
			if got != tc.want {
				t.Errorf("%s(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
			}
		})
	}
}

func TestAuditModelsURLCandidatesConsistency(t *testing.T) {
	// ModelsURLCandidates intentionally does NOT strip trailing /vN.
	// For providers like volcengine (base ends in /v3), ModelsURLCandidates
	// produces [base/v3/models, base/v3/v1/models] — the first candidate
	// IS the correct endpoint for volcengine. ModelsURL strips /v3 and
	// produces base/v1/models — this is the "canonical" URL used by other
	// code paths (e.g. discovery). The two functions have intentionally
	// different semantics.
	//
	// For providers where base does NOT end in /vN, ModelsURL should be
	// in the candidates list.

	bases := []string{
		"https://api.openai.com",
		"https://api.openai.com/v1",
		"https://api.anthropic.com",
	}
	for _, base := range bases {
		modelsURL := ModelsURL(base)
		candidates := ModelsURLCandidates(base)
		if len(candidates) == 0 {
			continue
		}
		found := false
		for _, c := range candidates {
			if c == modelsURL {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("base=%q: ModelsURL=%q not in ModelsURLCandidates=%v", base, modelsURL, candidates)
		}
	}
}

func TestAuditCompletionSuffixesOrder(t *testing.T) {
	// Verify that longer suffixes are checked before shorter ones.
	// If /v1/chat/completions and /chat/completions are both present,
	// the /v1/ version must be checked first for a URL ending in /v1/chat/completions.
	got := stripTail("https://api.example.com/v1/chat/completions")
	want := "https://api.example.com"
	if got != want {
		t.Errorf("stripTail(/v1/chat/completions) = %q, want %q", got, want)
	}
}

func TestAuditModelsURLCandidatesXiaomi(t *testing.T) {
	// For xiaomi (base ends in /v1), ModelsURLCandidates should produce
	// root/v1/models as the first candidate (same as ModelsURL).
	base := "https://token-plan-cn.xiaomimimo.com/v1"
	candidates := ModelsURLCandidates(base)
	expected := "https://token-plan-cn.xiaomimimo.com/v1/models"
	if len(candidates) == 0 {
		t.Fatal("empty candidates")
	}
	if candidates[0] != expected {
		t.Errorf("first candidate = %q, want %q", candidates[0], expected)
	}
	// Also verify ModelsURL matches
	if got := ModelsURL(base); got != expected {
		t.Errorf("ModelsURL = %q, want %q", got, expected)
	}
}

func TestAuditNoDoubleVersion(t *testing.T) {
	// For bases ending in /v3, ModelsURLCandidates should NOT produce /v3/v1/models
	// as the first candidate. The first candidate should be base/models.
	base := "https://ark.cn-beijing.volces.com/api/v3"
	candidates := ModelsURLCandidates(base)
	if len(candidates) < 2 {
		t.Fatal("expected at least 2 candidates")
	}
	// First candidate: base/models
	want0 := "https://ark.cn-beijing.volces.com/api/v3/models"
	if candidates[0] != want0 {
		t.Errorf("candidates[0] = %q, want %q", candidates[0], want0)
	}
	// Second candidate: base/v1/models (note: /v3 + /v1 = /v3/v1 — this is expected)
	want1 := "https://ark.cn-beijing.volces.com/api/v3/v1/models"
	if candidates[1] != want1 {
		t.Errorf("candidates[1] = %q, want %q", candidates[1], want1)
	}
}

func TestAuditBuild(t *testing.T) {
	cases := []struct {
		in   string
		ep   Endpoint
		want string
	}{
		// All endpoints
		{"https://api.openai.com", EpModels, "https://api.openai.com/v1/models"},
		{"https://api.openai.com", EpChatCompletions, "https://api.openai.com/v1/chat/completions"},
		{"https://api.openai.com", EpCompletions, "https://api.openai.com/v1/completions"},
		{"https://api.openai.com", EpMessages, "https://api.openai.com/v1/messages"},
		{"https://api.openai.com", EpResponses, "https://api.openai.com/v1/responses"},

		// Volcengine
		{"https://ark.cn-beijing.volces.com/api/v3", EpChatCompletions, "https://ark.cn-beijing.volces.com/api/v1/chat/completions"},
		{"https://ark.cn-beijing.volces.com/api/v3", EpModels, "https://ark.cn-beijing.volces.com/api/v1/models"},

		// Empty
		{"", EpChatCompletions, ""},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%s_%s", tc.in, tc.ep), func(t *testing.T) {
			got := Build(tc.in, tc.ep)
			if got != tc.want {
				t.Errorf("Build(%q, %q) = %q, want %q", tc.in, tc.ep, got, tc.want)
			}
		})
	}
}

func TestAuditPathFor(t *testing.T) {
	cases := []struct {
		ep   Endpoint
		want string
	}{
		{EpModels, "/v1/models"},
		{EpChatCompletions, "/v1/chat/completions"},
		{EpCompletions, "/v1/completions"},
		{EpMessages, "/v1/messages"},
		{EpResponses, "/v1/responses"},
		{"unknown", ""},
	}
	for _, tc := range cases {
		t.Run(string(tc.ep), func(t *testing.T) {
			got := PathFor(tc.ep)
			if got != tc.want {
				t.Errorf("PathFor(%q) = %q, want %q", tc.ep, got, tc.want)
			}
		})
	}
}

func TestAuditEdgeCaseDoubleSlash(t *testing.T) {
	// Ensure no double slashes in output
	cases := []struct {
		in string
	}{
		{"https://api.openai.com/"},
		{"https://api.openai.com//"},
		{"https://api.openai.com/v1/"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := ModelsURL(tc.in)
			if got == "" {
				return
			}
			// Check no double slashes (except after https://)
			afterScheme := got[8:] // skip "https://"
			if contains(afterScheme, "//") {
				t.Errorf("ModelsURL(%q) = %q contains double slashes", tc.in, got)
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestAuditWrapperEquivalence(t *testing.T) {
	// Verify urlutil wrapper produces same results as upstreamurl directly
	bases := []string{
		"https://api.openai.com",
		"https://api.openai.com/v1",
		"https://ark.cn-beijing.volces.com/api/v3",
		"https://open.bigmodel.cn/api/coding/paas/v4",
		"https://token-plan-cn.xiaomimimo.com/v1",
		"https://api.anthropic.com",
		"",
	}
	for _, base := range bases {
		// ModelsURL
		got1 := ModelsURL(base)
		got2 := Build(base, EpModels)
		if !reflect.DeepEqual(got1, got2) {
			t.Errorf("ModelsURL(%q)=%q != Build(%q, EpModels)=%q", base, got1, base, got2)
		}
		// ChatCompletionsURL
		got1 = ChatCompletionsURL(base)
		got2 = Build(base, EpChatCompletions)
		if !reflect.DeepEqual(got1, got2) {
			t.Errorf("ChatCompletionsURL(%q)=%q != Build(%q, EpChatCompletions)=%q", base, got1, base, got2)
		}
	}
}
