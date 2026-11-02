package bg

import "testing"

func TestIsTrustedSkip(t *testing.T) {
	entries := normalizeTrustedSkip([]string{
		"gpt-4o",
		"openai/claude-sonnet-4-5", // weird but allowed
		"Anthropic:claude-sonnet-4-5",
		"  ",
	})
	cases := []struct {
		prov, model string
		want        bool
	}{
		{"", "gpt-4o", true},
		{"openai", "GPT-4O", true},
		{"openai", "claude-sonnet-4-5", true},
		{"anthropic", "claude-sonnet-4-5", true},
		{"", "claude-sonnet-4-5", false}, // provider/model needs provider
		{"openai", "gpt-4.1", false},
		{"", "", false},
	}
	for _, tc := range cases {
		got := isTrustedSkip(tc.prov, tc.model, entries)
		if got != tc.want {
			t.Errorf("isTrustedSkip(%q,%q)=%v want %v", tc.prov, tc.model, got, tc.want)
		}
	}
}

func TestFilterTrustedSkipTargets(t *testing.T) {
	// Without settings.Global, loadTrustedSkipEntries returns nil → no filter.
	w := &ModelCredibilityWorker{}
	in := []struct{ n int }{{1}}
	_ = in
	// Direct unit of the matcher via filter with injected empty list path:
	// filterTrustedSkipTargets with no Global is identity.
	out := w.filterTrustedSkipTargets(nil)
	if out != nil {
		t.Fatalf("nil in → nil out, got %#v", out)
	}
}

func TestNormalizeTrustedSkip(t *testing.T) {
	got := normalizeTrustedSkip([]string{"  GPT-4o ", "", "OpenAI/X"})
	if len(got) != 2 || got[0] != "gpt-4o" || got[1] != "openai/x" {
		t.Fatalf("got %#v", got)
	}
}
