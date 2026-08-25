package modelname

import (
	"regexp"
	"strings"
	"testing"
)

func TestNormalizeRouteKey(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		// Date suffix stripping
		{in: "gpt-4o-2024-08-06", want: "gpt-4o"},
		{in: "claude-sonnet-4-5-20250929", want: "claude-sonnet-4-5"},
		{in: "glm-4-7-251222", want: "glm-4-7"},
		{in: "glm-4-7-20251201", want: "glm-4-7"},
		{in: "minimax-m2.7-20251201", want: "minimax-m2.7"},
		{in: "MiniMax-M2.7-251222", want: "minimax-m2.7"},

		// Version dot/dash: NO conversion (P2: zero hard-coded family rules)
		{in: "glm-4-7", want: "glm-4-7"},
		{in: "minimax-m4-7", want: "minimax-m4-7"},
		{in: "glm-4.7", want: "glm-4.7"},
		{in: "minimax-m4.7", want: "minimax-m4.7"},
		{in: "claude-opus-4-6", want: "claude-opus-4-6"},
		{in: "claude-sonnet-4.5", want: "claude-sonnet-4.5"},

		// Feature suffixes preserved
		{in: "glm-4.7-flash", want: "glm-4.7-flash"},
		{in: "glm-4.7-air", want: "glm-4.7-air"},
		{in: "minimax-m2.7-thinking", want: "minimax-m2.7-thinking"},
		{in: "minimax-m4.7-highspeed", want: "minimax-m4.7-highspeed"},

		// Vendor prefix stripping
		{in: "openai/gpt-4o-mini-2024-07-18", want: "gpt-4o-mini"},
		{in: "scnet/minimax-m2.5", want: "minimax-m2.5"},
		{in: "volcengine/glm-4-9b", want: "glm-4-9b"},

		// [1M] / [2M] suffix stripping
		{in: "claude-sonnet-4-5 [1M]", want: "claude-sonnet-4-5"},
		{in: "gpt-4o [2M]", want: "gpt-4o"},

		// Complex date + version combinations
		{in: "glm-4-5-air-20250728", want: "glm-4-5-air"},
		{in: "glm-4-5-pro-20251201", want: "glm-4-5-pro"},

		// Claude — NO tier-first conversion
		{in: "claude-opus-4-6", want: "claude-opus-4-6"},
		{in: "claude-sonnet-4-6", want: "claude-sonnet-4-6"},
		{in: "claude-haiku-4-5", want: "claude-haiku-4-5"},
		{in: "claude-opus-4-5", want: "claude-opus-4-5"},
		{in: "claude-opus-4-7", want: "claude-opus-4-7"},
		{in: "claude-instant-4-6", want: "claude-instant-4-6"},

		// Claude old pattern (number-first) — unchanged
		{in: "claude-3-5-sonnet", want: "claude-3-5-sonnet"},
		{in: "claude-3-7-sonnet", want: "claude-3-7-sonnet"},

		// Edge cases
		{in: "", want: ""},
		{in: "   ", want: ""},
		{in: "GPT-4O", want: "gpt-4o"},
		{in: "MiniMax-M2.7", want: "minimax-m2.7"},
		{in: "MiniMax-M3", want: "minimax-m3"},
		{in: "MINIMAX-M3", want: "minimax-m3"},
		{in: "  gpt-4o  ", want: "gpt-4o"},
	}
	for _, tc := range tests {
		if got := NormalizeRouteKey(tc.in); got != tc.want {
			t.Errorf("NormalizeRouteKey(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestCanonicalizeClientModel(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: ""},
		{name: "whitespace", in: "   ", want: ""},
		{name: "trim spaces", in: "  glm-5.2  ", want: "glm-5.2"},
		{name: "uppercase glm", in: "GLM-5.2", want: "glm-5.2"},
		{name: "mixed case minimax", in: "MiniMax-M3", want: "minimax-m3"},
		{name: "minimax short form", in: "MINIMAX-M3", want: "minimax-m3"},
		{name: "already lower", in: "glm-5.2", want: "glm-5.2"},
		{name: "dotted version preserved", in: "minimax-m2.7", want: "minimax-m2.7"},
		{name: "dash variant preserved", in: "glm-4-7", want: "glm-4-7"},
		{name: "GPT uppercase", in: "GPT-4O", want: "gpt-4o"},
		{name: "claude uppercase", in: "Claude-Sonnet-4-6", want: "claude-sonnet-4-6"},
		// 2026-07-14 fix: vendor prefix is stripped so NIM's
		// "z-ai/glm-5.2" collapses to the same canonical key as the
		// client's "glm-5.2". This is the property that makes
		// loadCandidatesByModalityDB's (1) canonical_raw_name = $1
		// clause match NIM offers.
		{name: "zhipu prefix", in: "Zhipu/glm-4.7", want: "glm-4.7"},
		{name: "nvidia z-ai prefix", in: "z-ai/glm-5.2", want: "glm-5.2"},
		{name: "nvidia minimaxai prefix", in: "minimaxai/minimax-m3", want: "minimax-m3"},
		{name: "nvidia z-ai uppercase", in: "Z-AI/GLM-5.2", want: "glm-5.2"},
		{name: "anthropic claude prefix", in: "anthropic/claude-3-5-sonnet", want: "claude-3-5-sonnet"},
		{name: "openai prefix", in: "openai/gpt-4o", want: "gpt-4o"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := CanonicalizeClientModel(tc.in); got != tc.want {
				t.Errorf("CanonicalizeClientModel(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestCanonicalizeClientModel_AlwaysLower pins the gateway-wide case
// contract: every input must collapse to a value that is byte-equal to
// its lowercase form. A regression here would re-introduce the
// mixed-case alias / canonical_name drift that triggered the
// 2026-07-14 audit.
func TestCanonicalizeClientModel_AlwaysLower(t *testing.T) {
	inputs := []string{
		"GLM-5.2", "glm-5.2", " GLM-5.2 ", "Z-AI/GLM-5.2",
		"MiniMax-M3", "minimax-m3", "MINIMAX-M3",
		"meta/llama-3.3-70b-instruct", "Meta/Llama-3.3-70B-Instruct",
		"Claude-Opus-4-6", "claude-opus-4-6", "CLAUDE-OPUS-4-6",
		"gpt-4o", "GPT-4O", "  Gpt-4O  ",
	}
	for _, in := range inputs {
		got := CanonicalizeClientModel(in)
		if got != strings.ToLower(strings.TrimSpace(got)) {
			t.Errorf("CanonicalizeClientModel(%q) = %q is not lowercase", in, got)
		}
		if strings.TrimSpace(in) == "" && got != "" {
			t.Errorf("CanonicalizeClientModel(%q) should return empty for whitespace input, got %q", in, got)
		}
	}
}

func TestNormalizeModelRef(t *testing.T) {
	tests := []struct {
		in            string
		wantProvider  string
		wantBaseModel string
		wantVersion   string
	}{
		// After Step 1: baseModel is just NormalizeRouteKey(in),
		// version is always "" (no family-specific extraction).
		{in: "glm-4.7", wantProvider: "", wantBaseModel: "glm-4.7", wantVersion: ""},
		{in: "minimax-m4.7", wantProvider: "", wantBaseModel: "minimax-m4.7", wantVersion: ""},
		{in: "openai/gpt-4o", wantProvider: "openai", wantBaseModel: "gpt-4o", wantVersion: ""},
		{in: "scnet/minimax-m2.5", wantProvider: "scnet", wantBaseModel: "minimax-m2.5", wantVersion: ""},
		{in: "glm-4-7-251222", wantProvider: "", wantBaseModel: "glm-4-7", wantVersion: ""},
		{in: "no-version-model", wantProvider: "", wantBaseModel: "no-version-model", wantVersion: ""},
		{in: "claude-opus-4-6", wantProvider: "", wantBaseModel: "claude-opus-4-6", wantVersion: ""},
		{in: "MINIMAX-M3", wantProvider: "", wantBaseModel: "minimax-m3", wantVersion: ""},
	}
	for _, tc := range tests {
		prov, base, ver := NormalizeModelRef(tc.in)
		if prov != tc.wantProvider || base != tc.wantBaseModel || ver != tc.wantVersion {
			t.Errorf("NormalizeModelRef(%q) = (%q, %q, %q), want (%q, %q, %q)",
				tc.in, prov, base, ver, tc.wantProvider, tc.wantBaseModel, tc.wantVersion)
		}
	}
}

func TestExtractFeatures(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{in: "glm-4.7-flash", want: []string{"flash"}},
		{in: "glm-4.7-air", want: []string{"air"}},
		{in: "minimax-m2.7-thinking", want: []string{"thinking"}},
		{in: "gpt-4o-mini", want: []string{"mini"}},
		{in: "claude-sonnet-4-5", want: []string{}},
		{in: "glm-4.7-flash-highspeed", want: []string{"flash", "highspeed"}},
	}
	for _, tc := range tests {
		got := ExtractFeatures(tc.in)
		if len(got) != len(tc.want) {
			t.Errorf("ExtractFeatures(%q) = %v, want %v", tc.in, got, tc.want)
			continue
		}
		for i, f := range got {
			if f != tc.want[i] {
				t.Errorf("ExtractFeatures(%q)[%d] = %q, want %q", tc.in, i, f, tc.want[i])
			}
		}
	}
}

func TestMatchModelOffer(t *testing.T) {
	tests := []struct {
		name        string
		client      string
		offer       string
		shouldMatch bool
	}{
		// Exact match (with and without case differences)
		{name: "exact_match", client: "gpt-4o", offer: "gpt-4o", shouldMatch: true},
		{name: "case_insensitive", client: "GPT-4O", offer: "gpt-4o", shouldMatch: true},
		{name: "minimax_case_insensitive", client: "minimax-m3", offer: "MiniMax-M3", shouldMatch: true},

		// Dot vs dash: NO match (caller must rely on model_aliases for cross-form)
		{name: "glm-4-7_does_not_match_glm-4.7", client: "glm-4-7", offer: "glm-4.7", shouldMatch: false},
		{name: "glm-4.7_does_not_match_glm-4-7", client: "glm-4.7", offer: "glm-4-7", shouldMatch: false},
		{name: "minimax-m4-7_does_not_match_minimax-m4.7", client: "minimax-m4-7", offer: "minimax-m4.7", shouldMatch: false},
		{name: "claude-opus-4-6_does_not_match_claude-opus-4.6", client: "claude-opus-4-6", offer: "claude-opus-4.6", shouldMatch: false},

		// Date suffix stripping — match
		{name: "glm-4-7-251222_matches_glm-4-7", client: "glm-4-7-251222", offer: "glm-4-7", shouldMatch: true},
		{name: "glm-4.7_matches_glm-4-7-251222", client: "glm-4.7", offer: "glm-4-7-251222", shouldMatch: false}, // dot vs dash differ

		// Feature matching: NOT a match (strict equality only)
		{name: "glm-4.7-flash_self_match", client: "glm-4.7-flash", offer: "glm-4.7-flash", shouldMatch: true},
		{name: "glm-4.7_does_not_match_glm-4.7-flash", client: "glm-4.7", offer: "glm-4.7-flash", shouldMatch: false},
		{name: "glm-4.7-flash_does_not_match_glm-4.7-air", client: "glm-4.7-flash", offer: "glm-4.7-air", shouldMatch: false},
		{name: "glm-4.7-air_does_not_match_glm-4.7", client: "glm-4.7-air", offer: "glm-4.7", shouldMatch: false},

		// Version mismatch
		{name: "glm-4.5_does_not_match_glm-4.7", client: "glm-4.5", offer: "glm-4.7", shouldMatch: false},
		{name: "minimax-m2.5_does_not_match_minimax-m2.7", client: "minimax-m2.5", offer: "minimax-m2.7", shouldMatch: false},
		{name: "glm-4-5-air_self_match", client: "glm-4-5-air", offer: "glm-4-5-air", shouldMatch: true},
		{name: "glm-4-5-air-20250728_matches_glm-4-5-air", client: "glm-4-5-air-20250728", offer: "glm-4-5-air", shouldMatch: true},

		// Family mismatch
		{name: "gpt-4o_does_not_match_gpt-4o-mini", client: "gpt-4o", offer: "gpt-4o-mini", shouldMatch: false},
		{name: "gpt-4o-mini_does_not_match_gpt-4o", client: "gpt-4o-mini", offer: "gpt-4o", shouldMatch: false},

		// Vendor prefix stripping
		{name: "scnet_minimax_m2.5_matches_minimax_m2.5", client: "scnet/minimax-m2.5", offer: "minimax-m2.5", shouldMatch: true},

		// Complex real-world (the SCNET bug scenario)
		{name: "minimax-m4.7_does_not_match_minimax-m2.5", client: "minimax-m4.7", offer: "minimax-m2.5", shouldMatch: false},
		{name: "minimax-m4.7_does_not_match_scnet_minimax_m2.5", client: "minimax-m4.7", offer: "scnet/minimax-m2.5", shouldMatch: false},
		{name: "minimax-m2.7_matches_minimax_m2.7", client: "minimax-m2.7", offer: "minimax-m2.7", shouldMatch: true},
		{name: "minimax-m2.7_matches_MiniMax-M2.7", client: "minimax-m2.7", offer: "MiniMax-M2.7", shouldMatch: true},

		// Claude — strict equality only
		{name: "claude-opus-4-6_does_not_match_claude-opus-4.6", client: "claude-opus-4-6", offer: "claude-opus-4.6", shouldMatch: false},
		{name: "claude-sonnet-4-5_self_match", client: "claude-sonnet-4-5", offer: "claude-sonnet-4-5", shouldMatch: true},
		{name: "claude-sonnet-4-5-20250929_matches_claude-sonnet-4-5", client: "claude-sonnet-4-5-20250929", offer: "claude-sonnet-4-5", shouldMatch: true},
		{name: "claude-opus-4.5_does_not_match_claude-opus-4.6", client: "claude-opus-4.5", offer: "claude-opus-4.6", shouldMatch: false},

		// Newly added scenarios
		{name: "minimax-m3_self_match", client: "minimax-m3", offer: "minimax-m3", shouldMatch: true},
		{name: "minimax-m3_matches_MiniMax-M3", client: "minimax-m3", offer: "MiniMax-M3", shouldMatch: true},
		{name: "minimax-m3_does_not_match_minimax-m2.7", client: "minimax-m3", offer: "minimax-m2.7", shouldMatch: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := MatchModelOffer(tc.client, tc.offer)
			if got != tc.shouldMatch {
				t.Errorf("MatchModelOffer(%q, %q) = %v, want %v",
					tc.client, tc.offer, got, tc.shouldMatch)
			}
		})
	}
}

func TestGenerateAliasVariants(t *testing.T) {
	variants := GenerateAliasVariants("anthropic/claude-opus-4-8-20251215", "claude-opus-4.8")
	seen := map[string]bool{}
	for _, v := range variants {
		seen[v] = true
	}
	for _, want := range []string{
		"claude-opus-4-8",
		"claude-opus-4.8",
		"anthropic/claude-opus-4-8-20251215",
	} {
		if !seen[want] {
			t.Fatalf("missing alias %q in %v", want, variants)
		}
	}

	variants = GenerateAliasVariants("thinking/claude-opus-4.8", "claude-opus-4.8")
	seen = map[string]bool{}
	for _, v := range variants {
		seen[v] = true
	}
	if !seen["claude-opus-4.8"] {
		t.Fatalf("wrapper variant did not normalize to base model: %v", variants)
	}
}

func TestStandardizeName(t *testing.T) {
	// StandardizeName is now an alias for NormalizeRouteKey.
	// The two should always agree.
	pairs := [][2]string{
		{"MiniMax-M3", "minimax-m3"},
		{"glm-4-7", "glm-4-7"},
		{"glm-4.7-flash", "glm-4.7-flash"},
		{"claude-opus-4-6", "claude-opus-4-6"},
		{"scnet/minimax-m2.5", "minimax-m2.5"},
		{"", ""},
	}
	for _, p := range pairs {
		got := StandardizeName(p[0])
		if got != p[1] {
			t.Errorf("StandardizeName(%q) = %q, want %q", p[0], got, p[1])
		}
	}
}

// TestNormalizeRouteKeyAliases covers the cross-form variant matrix
// used by the SQL resolver to bridge dot-vs-dash canonical / alias
// names.  The user-visible bug was that "claude-sonnet-4.6" failed
// to match a DB canonical "claude-sonnet-4-6" because the alias
// table was never seeded with the dot-form.  After this change the
// dot-form input emits the dash form as its second variant, which
// is exactly the bridge the SQL resolver needs.
//
// 2026-06-19: the contract is now **family-agnostic** — every input
// that contains "<digit>-<digit>" or "<digit>.<digit>" must emit
// both forms in its variant list, regardless of vendor.  We assert
// this for 6 families (claude / glm / MiniMax / gpt / deepseek /
// qwen) plus a few future-looking names so new vendors inherit the
// same rule without code changes.
func TestNormalizeRouteKeyAliases(t *testing.T) {
	cases := []struct {
		input    string
		mustHave []string
	}{
		// Real DB canonical names observed on 184 (2026-06-19 probe)
		{"claude-sonnet-4.6", []string{"claude-sonnet-4.6", "claude-sonnet-4-6"}},
		{"claude-sonnet-4-6", []string{"claude-sonnet-4-6", "claude-sonnet-4.6"}},
		{"claude-opus-4.8", []string{"claude-opus-4.8", "claude-opus-4-8"}},
		{"claude-opus-4-8", []string{"claude-opus-4-8", "claude-opus-4.8"}},
		{"claude-haiku-4.5", []string{"claude-haiku-4.5", "claude-haiku-4-5"}},
		{"claude-haiku-4-5", []string{"claude-haiku-4-5", "claude-haiku-4.5"}},
		{"glm-5.1", []string{"glm-5.1", "glm-5-1"}},
		{"glm-5-1", []string{"glm-5-1", "glm-5.1"}},
		{"minimax-m2.7", []string{"minimax-m2.7", "minimax-m2-7"}},
		{"minimax-m2-7", []string{"minimax-m2-7", "minimax-m2.7"}},
		{"gpt-5.1", []string{"gpt-5.1", "gpt-5-1"}},
		{"gpt-5-1", []string{"gpt-5-1", "gpt-5.1"}},
		{"deepseek-v3.1", []string{"deepseek-v3.1", "deepseek-v3-1"}},
		{"deepseek-v3-1", []string{"deepseek-v3-1", "deepseek-v3.1"}},
		// Family without version-number pair — no dot↔dash bridge applies
		{"qwen3-max", []string{"qwen3-max"}},
		{"minimax-m3", []string{"minimax-m3"}},
		// Empty input
		{"", nil},
	}
	for _, c := range cases {
		got := NormalizeRouteKeyAliases(c.input)
		if c.mustHave == nil {
			if len(got) != 0 {
				t.Errorf("NormalizeRouteKeyAliases(%q) = %v, want empty", c.input, got)
			}
			continue
		}
		have := map[string]bool{}
		for _, v := range got {
			have[v] = true
		}
		for _, want := range c.mustHave {
			if !have[want] {
				t.Errorf("NormalizeRouteKeyAliases(%q) = %v, missing %q", c.input, got, want)
			}
		}
	}
}

// TestNormalizeRouteKeyAliases_FamilyAgnostic is a contract test that
// proves the dot↔dash bridge is purely regex-driven (no family
// hard-coding).  Given an input with N number-pairs separated by
// either "." or "-", the output MUST contain the inverse form for
// every such pair — independent of the surrounding alphabetic
// tokens.  This is the property the user asked for: "no hard-coded
// rules, automatically adapts to future model versions".
func TestNormalizeRouteKeyAliases_FamilyAgnostic(t *testing.T) {
	// For each input, take the canonical NormalizeRouteKey and swap
	// each version punctuation both ways; the resolver variants must
	// contain every resulting form.
	inputs := []string{
		"claude-opus-4.8",
		"claude-opus-4-8",
		"claude-haiku-4.5",
		"glm-5.1",
		"glm-5-1",
		"minimax-m2.7",
		"minimax-m2-7",
		"gpt-5-1",
		"deepseek-v3-1",
		"qwen2.5-72b-instruct",
		"qwen2-5-72b-instruct",
	}
	for _, in := range inputs {
		base := NormalizeRouteKey(in)
		// Enumerate every form reachable by swapping the punctuation
		// of any digit-pair in `base`.  This is what the resolver
		// walks at SQL time.
		want := collectNumberPairVariants(base)
		got := NormalizeRouteKeyAliases(in)
		have := map[string]bool{}
		for _, v := range got {
			have[v] = true
		}
		for _, w := range want {
			if !have[w] {
				t.Errorf("NormalizeRouteKeyAliases(%q) = %v, missing %q (variant set should include every form reachable by digit-pair punctuation swap)", in, got, w)
			}
		}
	}
}

// collectNumberPairVariants enumerates every variant of `s` reachable
// by independently swapping "<digit>-<digit>" ↔ "<digit>.<digit>"
// for each pair in `s`.  The result is the set the SQL resolver is
// expected to walk — so the test asserts that NormalizeRouteKeyAliases
// produces a superset.
func collectNumberPairVariants(s string) []string {
	pairDash := regexp.MustCompile(`(^|[^0-9])(\d+)-(\d+)([^0-9]|$)`)
	pairDot := regexp.MustCompile(`(^|[^0-9])(\d+)\.(\d+)([^0-9]|$)`)
	seen := map[string]bool{s: true}
	queue := []string{s}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if pairDash.MatchString(cur) {
			n := pairDash.ReplaceAllString(cur, `${1}${2}.${3}${4}`)
			if !seen[n] {
				seen[n] = true
				queue = append(queue, n)
			}
		}
		if pairDot.MatchString(cur) {
			n := pairDot.ReplaceAllString(cur, `${1}${2}-${3}${4}`)
			if !seen[n] {
				seen[n] = true
				queue = append(queue, n)
			}
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	return out
}

func TestStripAliasPrefix(t *testing.T) {
	tests := []struct {
		name   string
		model  string
		prefix string
		want   string
	}{
		{
			name:   "kx prefix stripped",
			model:  "kx-gpt-5.6-terra",
			prefix: "kx-",
			want:   "gpt-5.6-terra",
		},
		{
			name:   "kx prefix case insensitive",
			model:  "KX-GPT-5.6-terra",
			prefix: "kx-",
			want:   "GPT-5.6-terra",
		},
		{
			name:   "no prefix unchanged",
			model:  "gpt-5.6-terra",
			prefix: "kx-",
			want:   "gpt-5.6-terra",
		},
		{
			name:   "empty prefix disabled",
			model:  "gpt-5.6-terra",
			prefix: "",
			want:   "gpt-5.6-terra",
		},
		{
			name:   "prefix longer than model",
			model:  "kx",
			prefix: "kx-",
			want:   "kx",
		},
		{
			name:   "custom prefix works",
			model:  "myalias-gpt-4",
			prefix: "myalias-",
			want:   "gpt-4",
		},
		{
			name:   "model equals prefix",
			model:  "kx-",
			prefix: "kx-",
			want:   "",
		},
		{
			name:   "different prefix not stripped",
			model:  "kx-gpt-5.6",
			prefix: "other-",
			want:   "kx-gpt-5.6",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := StripAliasPrefix(tc.model, tc.prefix)
			if got != tc.want {
				t.Errorf("StripAliasPrefix(%q, %q) = %q, want %q",
					tc.model, tc.prefix, got, tc.want)
			}
		})
	}
}
