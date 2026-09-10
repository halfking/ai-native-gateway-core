package modelname

import (
	"testing"
)

func TestMatchStandardModels_UserReportedCases(t *testing.T) {
	catalog := []string{
		"claude-opus-5",
		"claude-sonnet-4.6",
		"claude-haiku-4.5",
		"grok-4.6",
		"grok-3",
		"gpt-5.4",
		"o3-mini",
		"o4-mini",
		"glm-5.2",
		"deepseek-v4",
	}

	tests := []struct {
		name      string
		raw       string
		wantName  string
		wantScore float64 // minimum expected score
	}{
		{
			name:      "misspelled vendor prefix rejoined and typo-corrected",
			raw:       "cluade/opus-5",
			wantName:  "claude-opus-5",
			wantScore: AutoLinkThreshold,
		},
		{
			name:      "version-only base joined with vendor prefix",
			raw:       "grok/4.6",
			wantName:  "grok-4.6",
			wantScore: AutoLinkThreshold,
		},
		{
			name:      "exact raw name",
			raw:       "grok-4.6",
			wantName:  "grok-4.6",
			wantScore: 1.0,
		},
		{
			name:      "vendor-qualified standard name",
			raw:       "anthropic/claude-opus-5",
			wantName:  "claude-opus-5",
			wantScore: AutoLinkThreshold,
		},
		{
			name:      "nim-style vendor slug keeps base exact",
			raw:       "z-ai/glm-5.2",
			wantName:  "glm-5.2",
			wantScore: 0.99,
		},
		{
			name:      "dot/dash cross form",
			raw:       "grok/4-6",
			wantName:  "grok-4.6",
			wantScore: 0.98,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			best := BestStandardModelMatch(tc.raw, catalog)
			if best == nil {
				t.Fatalf("no match for %q, want %q", tc.raw, tc.wantName)
			}
			if best.Name != tc.wantName {
				t.Fatalf("BestStandardModelMatch(%q) = %q (score %.3f), want %q",
					tc.raw, best.Name, best.Score, tc.wantName)
			}
			if best.Score < tc.wantScore-1e-9 {
				t.Fatalf("score for %q = %.3f, want >= %.3f", tc.raw, best.Score, tc.wantScore)
			}
		})
	}
}

func TestMatchStandardModels_RejectsWrongGeneration(t *testing.T) {
	catalog := []string{"o3-mini", "grok-3", "claude-opus-4.5"}

	// Same-shape typo but a DIFFERENT model generation must never
	// auto-link: digits are identity, not typos.
	for _, raw := range []string{"o4-mini", "grok-4", "claude/opus-5"} {
		if best := BestStandardModelMatch(raw, catalog); best != nil && best.Score >= AutoLinkThreshold {
			t.Fatalf("raw %q must not auto-link, matched %q score %.3f", raw, best.Name, best.Score)
		}
	}
}

// 2026-09-11 audit: discovery pre-2026-09-10 seeded junk canonical rows by
// stripping the vendor prefix ("cluade/opus-5" → canonical "opus-5"). With
// such a row present it scores 0.99 (base-exact) and would outrank the
// correct "claude-opus-5" (0.886). The vendor-prefix disambiguation must
// promote the re-joined typo match above the junk base row.
func TestMatchStandardModels_JunkBaseRowDoesNotWin(t *testing.T) {
	catalog := []string{"opus-5", "claude-opus-5", "4.6", "grok-4.6"}

	best := BestStandardModelMatch("cluade/opus-5", catalog)
	if best == nil || best.Name != "claude-opus-5" {
		t.Fatalf("junk base row must not win, got %+v", best)
	}
	matches := MatchStandardModels("cluade/opus-5", catalog)
	if matches[0].Name != "claude-opus-5" {
		t.Fatalf("ranked[0] = %q, want claude-opus-5 promoted over junk opus-5", matches[0].Name)
	}
	for i := 1; i < len(matches); i++ {
		if matches[i].Score > matches[i-1].Score {
			t.Fatalf("ranking not monotonic after demotion: %v", matches)
		}
	}

	// Dash-form raw with a junk "4-6" row: cross-form re-join to the real
	// standard must win too.
	if best := BestStandardModelMatch("grok/4-6", catalog); best == nil || best.Name != "grok-4.6" {
		t.Fatalf("cross-form re-join must beat junk base row, got %+v", best)
	}
}

// A true vendor-qualified name must keep preferring the established BASE
// row — exact joined hits never trigger the junk demotion.
func TestMatchStandardModels_VendorSlugStillPrefersBase(t *testing.T) {
	catalog := []string{"glm-5.2", "z-ai-glm-5.2"}
	best := BestStandardModelMatch("z-ai/glm-5.2", catalog)
	if best == nil || best.Name != "glm-5.2" {
		t.Fatalf("vendor-slug raw must prefer established base row, got %+v", best)
	}
}

// Suffix VARIANTS of a base model are distinct models: when only the base
// standard row exists, a "-thinking"/"-fast" raw name must keep seeding its
// own canonical instead of silently folding into the base (containment is
// capped below AutoLinkThreshold for non-suffix-aligned candidates).
func TestMatchStandardModels_VariantDoesNotFoldIntoBase(t *testing.T) {
	catalog := []string{"claude-opus-5", "grok-4.6"}
	for _, raw := range []string{"claude-opus-5-thinking", "claude/opus-5-thinking", "grok-4.6-fast"} {
		if best := BestStandardModelMatch(raw, catalog); best != nil && best.Score >= AutoLinkThreshold {
			t.Fatalf("variant %q must not auto-fold into base, matched %+v", raw, best)
		}
	}
}

// Vendor-qualified full names keep the suffix-aligned containment channel
// (0.90): "anthropic/" is a prefix, not a variant suffix.
func TestMatchStandardModels_VendorQualifiedStillAutoLinks(t *testing.T) {
	catalog := []string{"claude-opus-5", "claude-opus-5-thinking"}
	best := BestStandardModelMatch("anthropic/claude-opus-5", catalog)
	if best == nil || best.Name != "claude-opus-5" || best.Score < AutoLinkThreshold {
		t.Fatalf("vendor-qualified raw must auto-link to base, got %+v", best)
	}
}

func TestMatchStandardModels_PrefersExactOverVariants(t *testing.T) {
	catalog := []string{"claude-opus-5-thinking", "claude-opus-5", "claude-opus-5-lab"}
	best := BestStandardModelMatch("cluade/opus-5", catalog)
	if best == nil || best.Name != "claude-opus-5" {
		t.Fatalf("want base model claude-opus-5, got %+v", best)
	}
}

func TestMatchStandardModels_RankedOutput(t *testing.T) {
	catalog := []string{"grok-4.6", "grok-4.6-fast", "grok-3", "deepseek-v4"}
	matches := MatchStandardModels("grok/4.6", catalog)
	if len(matches) < 2 {
		t.Fatalf("expected ranked candidates, got %d", len(matches))
	}
	if matches[0].Name != "grok-4.6" || matches[0].Score != 1.0 {
		t.Fatalf("top match = %+v, want grok-4.6 @ 1.0", matches[0])
	}
	for i := 1; i < len(matches); i++ {
		if matches[i].Score > matches[i-1].Score {
			t.Fatalf("ranking not monotonic: %v", matches)
		}
	}
	// Unrelated catalog entries stay below the floor.
	for _, m := range matches {
		if m.Name == "deepseek-v4" {
			t.Fatalf("unrelated model %q must not appear", m.Name)
		}
	}
}

func TestMatchStandardModels_NoPrefixFallback(t *testing.T) {
	// Bare name without vendor prefix behaves like an exact lookup.
	catalog := []string{"deepseek-v4", "deepseek-v4-exp"}
	best := BestStandardModelMatch("deepseek-v4", catalog)
	if best == nil || best.Name != "deepseek-v4" || best.Score != 1.0 {
		t.Fatalf("want deepseek-v4 @ 1.0, got %+v", best)
	}

	// A bare version segment alone must NOT confidently link — no family
	// signal to justify it.
	if best := BestStandardModelMatch("4.6", catalog); best != nil && best.Score >= AutoLinkThreshold {
		t.Fatalf("bare version must not auto-link, got %+v", best)
	}
}

func TestMatchStandardModels_EmptyInputs(t *testing.T) {
	if got := MatchStandardModels("", []string{"grok-4.6"}); len(got) != 0 {
		t.Fatalf("empty raw must not match, got %v", got)
	}
	if got := MatchStandardModels("grok/4.6", nil); len(got) != 0 {
		t.Fatalf("empty catalog must not match, got %v", got)
	}
	if BestStandardModelMatch("grok/4.6", []string{"", "  "}) != nil {
		t.Fatal("blank canonical names must be ignored")
	}
}

func TestDamerauSimilarityTransposition(t *testing.T) {
	// "cluade" → "claude" is ONE transposition, not two edits.
	if d := optimalStringAlignment("cluade-opus-5", "claude-opus-5"); d != 1 {
		t.Fatalf("OSA distance = %d, want 1", d)
	}
	if s := damerauSimilarity("cluade-opus-5", "claude-opus-5"); s < 0.9 {
		t.Fatalf("similarity = %.3f, want >= 0.9", s)
	}
}
