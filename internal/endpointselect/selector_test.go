package endpointselect

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/ir"
)

// Stage 1A — protocol AND family both match => Passthrough=true (per §3.5).
// This is the Ollama native client → /api/chat endpoint row scenario
// pinned in docs/供应商协议优化-实施规划.md §6.1.
func TestStage1A_PassthroughOllama(t *testing.T) {
	c := makeCandidate([]EndpointLite{
		{ID: 1, Protocol: "ollama-native", BaseURL: "http://s:11434", VendorNative: "ollama", IsPrimary: true, Enabled: true},
	})
	d := Select(c, ProtocolOllamaNative, "ollama")
	if d.Protocol != ProtocolOllamaNative {
		t.Errorf("Protocol = %q, want %q", d.Protocol, ProtocolOllamaNative)
	}
	if d.BaseURL != "http://s:11434" {
		t.Errorf("BaseURL = %q, want http://s:11434", d.BaseURL)
	}
	if !d.Passthrough {
		t.Errorf("Passthrough = false, want true (Stage 1A is the byte-faithful wire path)")
	}
	if d.MatchRule != MatchStage1A {
		t.Errorf("MatchRule = %q, want %q", d.MatchRule, MatchStage1A)
	}
	if d.EndpointID != 1 {
		t.Errorf("EndpointID = %d, want 1", d.EndpointID)
	}
	if d.VendorNative != "ollama" {
		t.Errorf("VendorNative = %q, want ollama", d.VendorNative)
	}
}

// Stage 1B — protocol matches but family is known and disagrees with the
// endpoint's vendor_native. Same protocol still wins (Stage 1 over Stage 2),
// but Passthrough is disabled because the IR bridge may need to massage
// fields the vendor expects differently.
func TestStage1B_SameProtocolDifferentFamily(t *testing.T) {
	c := makeCandidate([]EndpointLite{
		// vendor_native is "" (operator hasn't configured a vendor family
		// for the self-hosted Ollama instance), client family is claude.
		// Same protocol wins per §3.5 Stage 1 priority over family.
		{ID: 10, Protocol: "ollama-native", BaseURL: "http://s:11434", VendorNative: "", IsPrimary: true, Enabled: true},
	})
	d := Select(c, ProtocolOllamaNative, "anthropic-claude")
	if d.Protocol != ProtocolOllamaNative {
		t.Errorf("Protocol = %q, want %q", d.Protocol, ProtocolOllamaNative)
	}
	if d.Passthrough {
		t.Errorf("Passthrough = true, want false (Stage 1B is bridge-required)")
	}
	if d.MatchRule != MatchStage1B {
		t.Errorf("MatchRule = %q, want %q", d.MatchRule, MatchStage1B)
	}
}

// Stage 1C — protocol matches and family is unknown. Same protocol wins,
// Passthrough disabled as a safety net (the model may be reclassified
// later by an inbound inference that supplies a family hint).
func TestStage1C_SameProtocolNoFamily(t *testing.T) {
	c := makeCandidate([]EndpointLite{
		{ID: 1, Protocol: "ollama-native", BaseURL: "http://s:11434", VendorNative: "ollama", IsPrimary: true, Enabled: true},
	})
	d := Select(c, ProtocolOllamaNative, "")
	if d.Protocol != ProtocolOllamaNative {
		t.Errorf("Protocol = %q, want %q", d.Protocol, ProtocolOllamaNative)
	}
	if d.Passthrough {
		t.Errorf("Passthrough = true, want false (Stage 1C is bridge-required)")
	}
	if d.MatchRule != MatchStage1C {
		t.Errorf("MatchRule = %q, want %q", d.MatchRule, MatchStage1C)
	}
}

// Stage 4 — no Stage 1 match. To trigger Stage 4 we need NO
// endpoints that match wantProtocol; otherwise Stage 1 wins
// immediately. Here we configure an ollama-native endpoint (no
// openai-completions row), and the client asks for openai-completions:
// Stage 1 misses (no protocol match), Stage 2 misses (no family
// match on a different-protocol row), Stage 4 returns the legacy
// primary.
func TestStage4_ProtocolFallback(t *testing.T) {
	c := makeCandidate([]EndpointLite{
		{ID: 1, Protocol: "ollama-native", BaseURL: "http://s:11434", IsPrimary: true, Enabled: true, VendorNative: "ollama"},
	})
	d := Select(c, ProtocolOpenAICompletions, "openai-gpt")
	if d.Protocol != ProtocolOllamaNative {
		t.Errorf("Protocol = %q, want %q (legacy primary)", d.Protocol, ProtocolOllamaNative)
	}
	if d.MatchRule != MatchStage4 {
		t.Errorf("MatchRule = %q, want %q", d.MatchRule, MatchStage4)
	}
	if d.EndpointID != 0 {
		t.Errorf("EndpointID = %d, want 0 (primary fallback marker)", d.EndpointID)
	}
	if d.BaseURL != "http://s:11434" {
		t.Errorf("BaseURL = %q, want http://s:11434 (legacy primary)", d.BaseURL)
	}
}

// NoNativeEndpoints_OllamaOnlyProtocol: when a supplier's primary
// protocol is ollama-native and there are no native endpoints configured,
// the selector MUST 100% match legacy behavior (Stage 4 with the primary
// pair). This guards the "operators who haven't migrated yet" path.
func TestNoNativeEndpoints_OllamaOnlyProtocol(t *testing.T) {
	c := CandidateLite{
		ProviderID: 1,
		BaseURL:    "http://s:11434",
		Protocol:   "ollama-native",
	}
	d := Select(c, ProtocolOpenAICompletions, "openai-gpt")
	if d.Protocol != ProtocolOllamaNative {
		t.Errorf("Protocol = %q, want %q (primary fallback)", d.Protocol, ProtocolOllamaNative)
	}
	if d.MatchRule != MatchStage4 {
		t.Errorf("MatchRule = %q, want %q", d.MatchRule, MatchStage4)
	}
	if d.EndpointID != 0 {
		t.Errorf("EndpointID = %d, want 0", d.EndpointID)
	}
}

// TestStage1_DisabledEndpointSkipped ensures operators can disable a
// specific native row without affecting Stage 4 behavior.
func TestStage1_DisabledEndpointSkipped(t *testing.T) {
	c := makeCandidate([]EndpointLite{
		{ID: 1, Protocol: "ollama-native", BaseURL: "http://s:11434", VendorNative: "ollama", IsPrimary: true, Enabled: false},
	})
	d := Select(c, ProtocolOllamaNative, "ollama")
	if d.MatchRule != MatchStage4 {
		t.Errorf("MatchRule = %q, want %q (disabled endpoint must be skipped)", d.MatchRule, MatchStage4)
	}
	if d.EndpointID != 0 {
		t.Errorf("EndpointID = %d, want 0", d.EndpointID)
	}
}

// TestStage1_PrimaryWinsTie: when two Stage 1 endpoints match, the
// primary one wins (matches the historical "primary first" ordering).
func TestStage1_PrimaryWinsTie(t *testing.T) {
	c := makeCandidate([]EndpointLite{
		{ID: 1, Protocol: "ollama-native", BaseURL: "http://a:11434", VendorNative: "ollama", IsPrimary: false, Enabled: true, Weight: 100},
		{ID: 2, Protocol: "ollama-native", BaseURL: "http://b:11434", VendorNative: "ollama", IsPrimary: true, Enabled: true, Weight: 100},
	})
	d := Select(c, ProtocolOllamaNative, "ollama")
	if d.EndpointID != 2 {
		t.Errorf("EndpointID = %d, want 2 (primary wins)", d.EndpointID)
	}
	if d.BaseURL != "http://b:11434" {
		t.Errorf("BaseURL = %q, want http://b:11434", d.BaseURL)
	}
}

// TestStage1_WeightBreaksTie: when two primaries (rare, but valid) are
// configured, weight DESCENDING breaks the tie (r0924 fix-a task 6b:
// weight is a traffic share on the provider side — higher weight =
// higher priority, matching provider/client.go applyCapacityWeightedLB
// and executors/router.go promoteWeightedCandidate).
func TestStage1_WeightBreaksTie(t *testing.T) {
	c := makeCandidate([]EndpointLite{
		{ID: 1, Protocol: "ollama-native", BaseURL: "http://a:11434", VendorNative: "ollama", IsPrimary: true, Enabled: true, Weight: 200},
		{ID: 2, Protocol: "ollama-native", BaseURL: "http://b:11434", VendorNative: "ollama", IsPrimary: true, Enabled: true, Weight: 100},
	})
	d := Select(c, ProtocolOllamaNative, "ollama")
	if d.EndpointID != 1 {
		t.Errorf("EndpointID = %d, want 1 (higher weight = higher priority)", d.EndpointID)
	}
}

// TestIrNamespaceProtocolsHitStage1 locks the r0924 fix-a task 5 fix:
// wantProtocol arrives in the IR namespace (openai-chat / ollama-chat,
// per internal/ir/types.go) while endpoint rows carry catalog-namespace
// values. Before the ir→catalog normalization at the Select() entry,
// every ir-namespace request silently fell through to Stage 4.
func TestIrNamespaceProtocolsHitStage1(t *testing.T) {
	cases := []struct {
		name        string
		irProtocol  Protocol
		wantCatalog Protocol
	}{
		{"ollama-chat maps to ollama-native", "ollama-chat", ProtocolOllamaNative},
		{"openai-chat maps to openai-completions", "openai-chat", ProtocolOpenAICompletions},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := makeCandidate([]EndpointLite{
				{ID: 7, Protocol: string(tc.wantCatalog), BaseURL: "http://s:11434", VendorNative: "ollama", IsPrimary: true, Enabled: true},
			})
			d := Select(c, tc.irProtocol, "ollama")
			if d.MatchRule == MatchStage4 {
				t.Fatalf("MatchRule = stage4 for ir protocol %q — namespace normalization broken", tc.irProtocol)
			}
			if d.EndpointID != 7 {
				t.Errorf("EndpointID = %d, want 7 (stage 1 hit)", d.EndpointID)
			}
			if d.Protocol != tc.wantCatalog {
				t.Errorf("Protocol = %q, want %q (decision must carry the catalog value)", d.Protocol, tc.wantCatalog)
			}
		})
	}
}

// TestNormalizeWantProtocol_LockstepContract is the table-driven lockstep
// contract from r0924 fix-a task 5: EVERY ir protocol enum value
// (internal/ir/types.go) must map onto the matching catalog value AND hit
// the catalog-namespace endpoint at Stage 1 through Select(). Adding an ir
// protocol without extending irToCatalogProtocol fails this test — the
// symptom would be that protocol permanently pinned to Stage 4.
func TestNormalizeWantProtocol_LockstepContract(t *testing.T) {
	// All five ir enum values from internal/ir/types.go → catalog values
	// from provider/catalog/protocol.go.
	irToCatalog := []struct {
		ir      string
		catalog Protocol
	}{
		{ir.ProtocolOpenAIChat, ProtocolOpenAICompletions},
		{ir.ProtocolOpenAIResponses, ProtocolOpenAIResponses},
		{ir.ProtocolAnthropicMessages, ProtocolAnthropicMessages},
		{ir.ProtocolGeminiGenerate, ProtocolGeminiGenerate},
		{ir.ProtocolOllamaChat, ProtocolOllamaNative},
	}
	for _, tc := range irToCatalog {
		// 1) the mapping itself.
		if got := NormalizeWantProtocol(Protocol(tc.ir)); got != tc.catalog {
			t.Errorf("NormalizeWantProtocol(%q) = %q, want %q (ir→catalog lockstep broken)", tc.ir, got, tc.catalog)
		}
		// 2) the mapped value must actually HIT a catalog endpoint row
		//    (protocol + family match ⇒ Stage 1A, never Stage 4).
		c := makeCandidate([]EndpointLite{
			{ID: 42, Protocol: string(tc.catalog), BaseURL: "http://lockstep:11434", VendorNative: "lockstep-family", IsPrimary: true, Enabled: true},
		})
		d := Select(c, Protocol(tc.ir), "lockstep-family")
		if d.MatchRule != MatchStage1A {
			t.Errorf("ir protocol %q (mapped %q) → MatchRule = %q, want %q — catalog endpoint not reached", tc.ir, tc.catalog, d.MatchRule, MatchStage1A)
		}
		if d.EndpointID != 42 {
			t.Errorf("ir protocol %q → EndpointID = %d, want 42", tc.ir, d.EndpointID)
		}
	}
	// Catalog five-value enum must round-trip unchanged (idempotency,
	// including the three strings shared by both namespaces).
	for _, cat := range []Protocol{
		ProtocolOpenAICompletions, ProtocolOpenAIResponses, ProtocolAnthropicMessages,
		ProtocolGeminiGenerate, ProtocolOllamaNative,
	} {
		if got := NormalizeWantProtocol(cat); got != cat {
			t.Errorf("NormalizeWantProtocol(%q) = %q, want identity (catalog pass-through broken)", cat, got)
		}
	}
}

// TestNormalizeWantProtocol_UnknownPassthrough: unknown values must pass
// through unchanged (legacy behavior preserved during gradual rollout).
func TestNormalizeWantProtocol_UnknownPassthrough(t *testing.T) {
	if got := NormalizeWantProtocol("custom-protocol"); got != "custom-protocol" {
		t.Errorf("NormalizeWantProtocol(custom) = %q, want unchanged", got)
	}
}

// TestStage2_FamilyHitSortedPick locks the r0924 fix-a task 6a fix: the
// Stage 2 family group must be consumed through the shared (primary,
// weight desc, id) comparator, not raw slice order. Both family-matched
// rows miss Stage 1 (neither speaks openai-completions); the higher-weight
// row arrives LAST in the slice and must still win.
func TestStage2_FamilyHitSortedPick(t *testing.T) {
	c := makeCandidate([]EndpointLite{
		{ID: 1, Protocol: "anthropic-messages", BaseURL: "http://low:443", VendorNative: "openai-gpt", Enabled: true, Weight: 10},
		{ID: 2, Protocol: "ollama-native", BaseURL: "http://high:11434", VendorNative: "openai-gpt", Enabled: true, Weight: 900},
	})
	// Client speaks openai-chat (ir namespace → openai-completions): no
	// endpoint has that protocol, but both rows declare the openai-gpt
	// family — Stage 2. Within the family group the higher-weight endpoint
	// must win regardless of slice order.
	d := Select(c, "openai-chat", "openai-gpt")
	if d.MatchRule != MatchStage2 {
		t.Fatalf("MatchRule = %q, want %q", d.MatchRule, MatchStage2)
	}
	if d.EndpointID != 2 {
		t.Errorf("EndpointID = %d, want 2 (higher weight wins within the family group)", d.EndpointID)
	}
	if d.BaseURL != "http://high:11434" {
		t.Errorf("BaseURL = %q, want http://high:11434", d.BaseURL)
	}
}

// TestStage2_FamilyMatchProtocolMismatch: client speaks OpenAI, the supplier
// only has an Ollama-native endpoint with vendor_native="openai-gpt"
// (operator's intentional labeling). Family match wins because protocol
// match fails.
func TestStage2_FamilyMatchProtocolMismatch(t *testing.T) {
	c := makeCandidate([]EndpointLite{
		{ID: 1, Protocol: "ollama-native", BaseURL: "http://s:11434", VendorNative: "openai-gpt", Enabled: true},
	})
	d := Select(c, ProtocolOpenAICompletions, "openai-gpt")
	if d.Protocol != ProtocolOllamaNative {
		t.Errorf("Protocol = %q, want %q", d.Protocol, ProtocolOllamaNative)
	}
	if d.MatchRule != MatchStage2 {
		t.Errorf("MatchRule = %q, want %q", d.MatchRule, MatchStage2)
	}
	if d.Passthrough {
		t.Errorf("Passthrough = true, want false (Stage 2 is bridge-required)")
	}
}

// TestStage1_ProtocolStrictMatch: when the client's protocol does not match
// ANY endpoint, the selector MUST NOT silently pick a same-family endpoint
// (Stage 2 only kicks in when Stage 1 fails entirely). Documents the
// "protocol mismatch > family mismatch" priority.
func TestStage1_ProtocolStrictMatch(t *testing.T) {
	c := makeCandidate([]EndpointLite{
		{ID: 1, Protocol: "anthropic-messages", BaseURL: "http://s:anthropic", VendorNative: "openai-gpt", Enabled: true},
	})
	// Client speaks openai-completions, but no openai endpoint is
	// configured — Stage 1 misses, Stage 2 would match openai-gpt but
	// wantProtocol is openai-completions not anthropic-messages, so the
	// Stage 2 endpoint is also not selected (it's openai-gpt matched
	// but its protocol is anthropic-messages, which means we WOULD
	// pick it per the current Stage 2 rule). This test documents that
	// Stage 2 only filters out same-protocol matches.
	d := Select(c, ProtocolOpenAICompletions, "openai-gpt")
	// The anthropic-messages endpoint with vendor_native=openai-gpt
	// WILL be selected (Stage 2 family hit). This is by design — the
	// dispatcher is responsible for IR-bridging the request body from
	// openai-completions to anthropic-messages (a future P5 pass-through
	// scope).
	if d.MatchRule != MatchStage2 {
		t.Errorf("MatchRule = %q, want %q", d.MatchRule, MatchStage2)
	}
}

// TestStage4_EmptyCandidate: even an empty CandidateLite should return a
// valid Decision (Stage 4 fallback with zeroed EndpointID).
func TestStage4_EmptyCandidate(t *testing.T) {
	c := CandidateLite{}
	d := Select(c, ProtocolOllamaNative, "ollama")
	if d.MatchRule != MatchStage4 {
		t.Errorf("MatchRule = %q, want %q", d.MatchRule, MatchStage4)
	}
}

// TestStage1_ReasonIncludesFamily: the Decision.Reason string is the
// primary observability surface; document its shape so dashboards can
// be built against it.
func TestStage1_ReasonIncludesFamily(t *testing.T) {
	c := makeCandidate([]EndpointLite{
		{ID: 1, Protocol: "ollama-native", BaseURL: "http://s:11434", VendorNative: "ollama", IsPrimary: true, Enabled: true},
	})
	d := Select(c, ProtocolOllamaNative, "ollama")
	if d.Reason == "" {
		t.Error("Reason is empty; observability dashboards rely on it")
	}
	// The Stage 1A reason should NOT contain confusing text — keep it
	// short and action-oriented.
	if len(d.Reason) > 256 {
		t.Errorf("Reason too long (%d chars); keep under 256 for log readability", len(d.Reason))
	}
}

// makeCandidate wraps an endpoint slice in the CandidateLite shape with
// primary = BaseURL of the first enabled endpoint, for readability.
func makeCandidate(eps []EndpointLite) CandidateLite {
	c := CandidateLite{
		ProviderID:      1,
		CredentialID:    10,
		BaseURL:         "http://legacy:11434",
		Protocol:        "ollama-native",
		VendorNative:    "",
		NativeEndpoints: eps,
	}
	// If the first enabled endpoint is the "primary" we use its
	// base_url as the legacy fallback (matching how providers typically
	// populate candidates).
	for _, ep := range eps {
		if ep.IsPrimary && ep.Enabled {
			c.BaseURL = ep.BaseURL
			c.Protocol = ep.Protocol
			c.VendorNative = ep.VendorNative
			return c
		}
	}
	return c
}
