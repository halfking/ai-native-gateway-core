package discovery

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/modelname"
)

func ref(id int, name string) canonicalRef { return canonicalRef{id: id, name: name} }

// bestCanonicalMatch must only return matches at or above the 0.85
// AutoLinkThreshold, and must resolve the matched NAME back to the exact
// catalog row it came from (id equality).
func TestBestCanonicalMatch_ConfidentMatches(t *testing.T) {
	catalog := []canonicalRef{
		ref(11, "claude-opus-5"),
		ref(12, "claude-sonnet-4.6"),
		ref(13, "grok-4.6"),
		ref(14, "opus-5"), // junk row seeded by the old prefix-stripping code
	}

	tests := []struct {
		raw    string
		wantID int
	}{
		{"cluade/opus-5", 11}, // junk "opus-5" (0.99 base-exact) must lose to the re-joined typo match
		{"grok/4.6", 13},
		{"grok-4.6", 13},
		{"anthropic/claude-opus-5", 11},
	}
	for _, tc := range tests {
		got, ok := bestCanonicalMatch(tc.raw, catalog)
		if !ok {
			t.Fatalf("bestCanonicalMatch(%q) = no match, want id %d", tc.raw, tc.wantID)
		}
		if got.id != tc.wantID {
			t.Fatalf("bestCanonicalMatch(%q) = id %d (%s), want id %d", tc.raw, got.id, got.name, tc.wantID)
		}
	}
}

func TestBestCanonicalMatch_BelowThresholdNotMatched(t *testing.T) {
	catalog := []canonicalRef{
		ref(21, "grok-4.6"),
		ref(22, "glm-4.6"),
		ref(23, "o3-mini"),
	}

	// Bare version segment: no family signal, must not auto-link.
	if got, ok := bestCanonicalMatch("4.6", catalog); ok {
		t.Fatalf("bare version must not match, got %+v", got)
	}
	// Different generation (digit guard).
	if got, ok := bestCanonicalMatch("o4-mini", catalog); ok {
		t.Fatalf("wrong generation must not match, got %+v", got)
	}
	// Suffix variant must not fold into the base standard row.
	if got, ok := bestCanonicalMatch("grok-4.6-fast", catalog); ok {
		t.Fatalf("variant must not fold into base, got %+v", got)
	}
}

func TestBestCanonicalMatch_EmptyAndAbsent(t *testing.T) {
	catalog := []canonicalRef{ref(31, "grok-4.6")}
	if _, ok := bestCanonicalMatch("  ", catalog); ok {
		t.Fatal("blank raw must not match")
	}
	if _, ok := bestCanonicalMatch("qwen3-max", catalog); ok {
		t.Fatal("unrelated raw must not match")
	}
	if _, ok := bestCanonicalMatch("grok/4.6", nil); ok {
		t.Fatal("empty catalog must not match")
	}
}

// The threshold used by discovery must stay the shared modelname constant.
func TestBestCanonicalMatch_UsesSharedThreshold(t *testing.T) {
	if modelname.AutoLinkThreshold != 0.85 {
		t.Fatalf("AutoLinkThreshold changed to %v; re-audit discovery linking policy", modelname.AutoLinkThreshold)
	}
}
