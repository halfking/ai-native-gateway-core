package provider

import (
	"testing"

	providercatalog "github.com/kaixuan/llm-gateway-go/provider/catalog"
)

// TestResolveAdaptiveProtocols pins the candidate-exit resolution. This is the
// single point where the "adaptive" selector is dissolved into a concrete wire
// protocol, so its contract is what keeps executors from ever seeing the
// selector.
func TestResolveAdaptiveProtocols(t *testing.T) {
	cases := []struct {
		name             string
		cands            []Candidate
		allowed          map[int][]string
		wantProtocols    []string
		wantChanged      int
		wantRawPreserved []string
	}{
		{
			name: "no adaptive candidate is a no-op",
			cands: []Candidate{
				{ProviderID: 1, RawModel: "gpt-5", Protocol: providercatalog.ProtocolOpenAIResponses},
				{ProviderID: 1, RawModel: "claude-opus-4-5", Protocol: providercatalog.ProtocolOpenAICompletions},
			},
			allowed:          map[int][]string{1: {providercatalog.ProtocolOpenAIResponses}},
			wantProtocols:    []string{providercatalog.ProtocolOpenAIResponses, providercatalog.ProtocolOpenAICompletions},
			wantChanged:      0,
			wantRawPreserved: []string{"gpt-5", "claude-opus-4-5"},
		},
		{
			name: "adaptive with no declaration degrades to the repo default",
			cands: []Candidate{
				{ProviderID: 36, RawModel: "gpt-5.3-codex", Protocol: providercatalog.ProtocolAdaptive},
				{ProviderID: 36, RawModel: "claude-opus-4-5", Protocol: providercatalog.ProtocolAdaptive},
			},
			allowed:          nil,
			wantProtocols:    []string{providercatalog.ProtocolOpenAICompletions, providercatalog.ProtocolOpenAICompletions},
			wantChanged:      2,
			wantRawPreserved: []string{"gpt-5.3-codex", "claude-opus-4-5"},
		},
		{
			name: "adaptive honours the per-model family when declared",
			cands: []Candidate{
				{ProviderID: 36, RawModel: "gpt-5.3-codex", Protocol: providercatalog.ProtocolAdaptive},
				{ProviderID: 36, RawModel: "claude-opus-4-5", Protocol: providercatalog.ProtocolAdaptive},
				{ProviderID: 36, RawModel: "gemini-3-pro", Protocol: providercatalog.ProtocolAdaptive},
			},
			allowed: map[int][]string{36: {
				providercatalog.ProtocolOpenAICompletions,
				providercatalog.ProtocolOpenAIResponses,
				providercatalog.ProtocolAnthropicMessages,
			}},
			wantProtocols: []string{
				providercatalog.ProtocolOpenAIResponses,   // family hit + declared
				providercatalog.ProtocolAnthropicMessages, // family hit + declared
				providercatalog.ProtocolOpenAICompletions, // gemini not declared ⇒ base
			},
			wantChanged:      3,
			wantRawPreserved: []string{"gpt-5.3-codex", "claude-opus-4-5", "gemini-3-pro"},
		},
		{
			name: "providers resolve independently",
			cands: []Candidate{
				{ProviderID: 1, RawModel: "claude-opus-4-5", Protocol: providercatalog.ProtocolAdaptive},
				{ProviderID: 2, RawModel: "claude-opus-4-5", Protocol: providercatalog.ProtocolAdaptive},
				{ProviderID: 3, RawModel: "claude-opus-4-5", Protocol: providercatalog.ProtocolAdaptive},
			},
			allowed: map[int][]string{
				1: {providercatalog.ProtocolAnthropicMessages},
				2: {providercatalog.ProtocolOpenAICompletions},
				// 3 declares nothing
			},
			wantProtocols: []string{
				providercatalog.ProtocolAnthropicMessages,
				providercatalog.ProtocolOpenAICompletions,
				providercatalog.ProtocolOpenAICompletions,
			},
			wantChanged:      3,
			wantRawPreserved: []string{"claude-opus-4-5", "claude-opus-4-5", "claude-opus-4-5"},
		},
		{
			name: "case-folded selector from a dirty legacy row still resolves",
			cands: []Candidate{
				{ProviderID: 36, RawModel: "gpt-5", Protocol: "  Adaptive  "},
			},
			allowed:          map[int][]string{36: {providercatalog.ProtocolOpenAIResponses}},
			wantProtocols:    []string{providercatalog.ProtocolOpenAIResponses},
			wantChanged:      1,
			wantRawPreserved: []string{"gpt-5"},
		},
		{
			name:             "empty candidate slice",
			cands:            nil,
			allowed:          map[int][]string{36: {providercatalog.ProtocolOpenAIResponses}},
			wantProtocols:    nil,
			wantChanged:      0,
			wantRawPreserved: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			changed := resolveAdaptiveProtocols(tc.cands, tc.allowed)
			if changed != tc.wantChanged {
				t.Errorf("changed = %d, want %d", changed, tc.wantChanged)
			}
			if len(tc.cands) != len(tc.wantProtocols) {
				t.Fatalf("candidate count = %d, want %d", len(tc.cands), len(tc.wantProtocols))
			}
			for i, cand := range tc.cands {
				if cand.Protocol != tc.wantProtocols[i] {
					t.Errorf("cand[%d] (%s) protocol = %q, want %q",
						i, cand.RawModel, cand.Protocol, tc.wantProtocols[i])
				}
				// Invariant: the selector never survives the candidate exit.
				if providercatalog.IsAdaptiveProtocol(cand.Protocol) {
					t.Fatalf("cand[%d] still carries the selector %q", i, cand.Protocol)
				}
				// Invariant: resolution must not touch the model name — the
				// outbound body is built from RawModel later.
				if cand.RawModel != tc.wantRawPreserved[i] {
					t.Errorf("cand[%d] RawModel = %q, want %q (resolution must not rewrite it)",
						i, cand.RawModel, tc.wantRawPreserved[i])
				}
			}
		})
	}
}

// TestResolveAdaptiveProtocols_NegativeControl proves the guard test above can
// actually fail: if the resolver forgets to rewrite, the selector leaks and the
// invariant assertion must trip. This is the "御 is not always green" check.
func TestResolveAdaptiveProtocols_NegativeControl(t *testing.T) {
	leaked := []Candidate{{ProviderID: 36, RawModel: "gpt-5", Protocol: providercatalog.ProtocolAdaptive}}
	// Simulate a broken resolver: identical to production one, but skipped.
	changed := 0
	for _, cand := range leaked {
		if providercatalog.IsAdaptiveProtocol(cand.Protocol) {
			changed++ // would have been resolved here
		}
	}
	if changed == 0 {
		t.Fatal("negative control is vacuous: the fixture does not exercise the adaptive path")
	}
	// The invariant the production test asserts must be FALSE for the
	// unresolved fixture — i.e. the assertion has teeth.
	if !providercatalog.IsAdaptiveProtocol(leaked[0].Protocol) {
		t.Fatal("negative control is vacuous: selector not detected")
	}
}
