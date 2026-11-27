package catalog

import (
	"reflect"
	"testing"
)

// TestIsAdaptiveProtocol pins the selector recognizer. It must accept the
// alias-normalized spellings (case/underscore/space folded, same fold as
// NormalizeProviderProtocol) and must NOT accept any concrete protocol.
func TestIsAdaptiveProtocol(t *testing.T) {
	yes := []string{"adaptive", "Adaptive", "ADAPTIVE", " adaptive ", "\tAdaptive\n"}
	for _, in := range yes {
		if !IsAdaptiveProtocol(in) {
			t.Errorf("IsAdaptiveProtocol(%q) = false, want true", in)
		}
	}
	no := []string{
		"", "   ",
		ProtocolOpenAICompletions,
		ProtocolOpenAIResponses,
		ProtocolAnthropicMessages,
		ProtocolGeminiGenerate,
		ProtocolOllamaNative,
		"openai",
		"auto",  // nearby word that must not alias
		"adapt", // prefix only
	}
	for _, in := range no {
		if IsAdaptiveProtocol(in) {
			t.Errorf("IsAdaptiveProtocol(%q) = true, want false", in)
		}
	}
}

// TestNormalizeProviderProtocol_RejectsAdaptive is the A10 guard: "adaptive"
// is a SELECTION STRATEGY, not a wire protocol. It must never become a member
// of the canonical enum, because Protocols is shared with
// provider_catalog.protocol's CHECK semantics (Validate) — admitting
// "adaptive" there would let a bogus catalog row pass validation and would
// let an unresolved selector leak into executors.
func TestNormalizeProviderProtocol_RejectsAdaptive(t *testing.T) {
	for _, in := range []string{"adaptive", "Adaptive", " adaptive "} {
		if got, err := NormalizeProviderProtocol(in); err == nil {
			t.Errorf("NormalizeProviderProtocol(%q) = %q, want error (adaptive must stay out of the canonical enum)", in, got)
		}
	}
	for _, canonical := range Protocols {
		if canonical == ProtocolAdaptive {
			t.Fatalf("Protocols must not contain %q", ProtocolAdaptive)
		}
	}
}

// TestResolveAdaptiveProtocol is the core contract of the adaptive selector.
//
// Semantics under test:
//   - empty allowed            ⇒ inert: returns openai-completions (the repo
//     default), i.e. an undeclared provider behaves exactly as it did before
//     adaptive existed.
//   - allowed[0] is the base   ⇒ used whenever the model's recommended
//     protocol is not declared usable.
//   - recommendation honoured  ⇒ only when it appears in allowed.
//   - result is always concrete ⇒ never "adaptive", never ""; always a member
//     of Protocols.
func TestResolveAdaptiveProtocol(t *testing.T) {
	// vapeur-shaped declaration: the upstream serves the Responses API and the
	// chat-compatible API, but NOT /v1/messages or generateContent.
	responsesAndChat := []string{ProtocolOpenAIResponses, ProtocolOpenAICompletions}

	cases := []struct {
		name    string
		model   string
		allowed []string
		want    string
	}{
		// ---- gate 1: no declaration ⇒ inert, byte-identical to today ----
		{"no declaration, gpt-5", "gpt-5.3-codex", nil, ProtocolOpenAICompletions},
		{"no declaration, claude", "claude-opus-4-5", nil, ProtocolOpenAICompletions},
		{"no declaration, gemini", "gemini-3-pro", nil, ProtocolOpenAICompletions},
		{"empty slice, gpt-5", "gpt-5", []string{}, ProtocolOpenAICompletions},

		// ---- recommendation honoured when declared usable ----
		{"gpt-5 → responses (declared)", "gpt-5.3-codex", responsesAndChat, ProtocolOpenAIResponses},
		{"o3 → responses (declared)", "o3", responsesAndChat, ProtocolOpenAIResponses},
		{"claude → anthropic (declared)", "claude-opus-4-5", []string{ProtocolOpenAIResponses, ProtocolAnthropicMessages}, ProtocolAnthropicMessages},
		{"gemini → generate (declared)", "gemini-3-pro", []string{ProtocolOpenAIResponses, ProtocolGeminiGenerate}, ProtocolGeminiGenerate},

		// ---- NO family hit ⇒ operator's base wins, never a guess ----
		// RecommendedProtocolForModel reports ok=false for deepseek/qwen/kimi/
		// grok and for unrecognised names. Those must fall through to the base
		// rather than being pushed onto a "chat is probably fine" guess: a
		// Claude-only proxy whose models are named e.g. "my-proxy-v2" would
		// otherwise be silently moved off anthropic-messages.
		{"deepseek has no family hit → base", "deepseek-v4", responsesAndChat, ProtocolOpenAIResponses},
		{"unknown model → base", "some-local-model", responsesAndChat, ProtocolOpenAIResponses},

		// ---- recommendation NOT declared ⇒ fall back to allowed[0] ----
		// This is the vapeur case the user accepted: claude/gemini stay on the
		// base protocol instead of being pushed at an endpoint that 404s.
		{"claude not declared → base", "claude-opus-4-5", responsesAndChat, ProtocolOpenAIResponses},
		{"gemini not declared → base", "gemini-3-pro", responsesAndChat, ProtocolOpenAIResponses},
		{"claude not declared, chat base", "claude-opus-4-5", []string{ProtocolOpenAICompletions}, ProtocolOpenAICompletions},

		// ---- allowed[0] is the documented base, whatever it is ----
		{"anthropic base, no family hit", "some-local-model", []string{ProtocolAnthropicMessages, ProtocolOpenAICompletions}, ProtocolAnthropicMessages},
		{"ollama base, no family hit", "llama-3", []string{ProtocolOllamaNative, ProtocolOpenAICompletions}, ProtocolOllamaNative},
		// A family hit that IS declared still beats the base.
		{"gpt-5 beats ollama base when declared", "gpt-5", []string{ProtocolOllamaNative, ProtocolOpenAIResponses}, ProtocolOpenAIResponses},

		// ---- ordering matters: base is allowed[0], not the "safest" entry ----
		{"base is allowed[0] not the last", "claude-opus-4-5", []string{ProtocolOpenAICompletions, ProtocolOpenAIResponses}, ProtocolOpenAICompletions},

		// ---- dirty allowed entries are normalized, junk is dropped ----
		{"alias in allowed", "gpt-5", []string{"openai-response", ProtocolOpenAICompletions}, ProtocolOpenAIResponses},
		{"junk in allowed is skipped", "claude-opus-4-5", []string{"grpc", ProtocolOpenAICompletions}, ProtocolOpenAICompletions},
		{"all junk ⇒ inert", "gpt-5", []string{"grpc", "telepathy"}, ProtocolOpenAICompletions},
		{"empty strings in allowed", "gpt-5", []string{"", ProtocolOpenAIResponses}, ProtocolOpenAIResponses},

		// ---- model name hygiene ----
		{"empty model", "", responsesAndChat, ProtocolOpenAIResponses},
		{"uppercase model", "GPT-5.3-CODEX", responsesAndChat, ProtocolOpenAIResponses},
		{"padded model", "  claude-opus-4-5  ", responsesAndChat, ProtocolOpenAIResponses},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveAdaptiveProtocol(tc.model, tc.allowed)
			if got != tc.want {
				t.Errorf("ResolveAdaptiveProtocol(%q, %v) = %q, want %q", tc.model, tc.allowed, got, tc.want)
			}
			// Invariant: the result is always a concrete wire protocol.
			if IsAdaptiveProtocol(got) {
				t.Fatalf("resolved to the selector itself (%q) — would leak into executors", got)
			}
			if got == "" {
				t.Fatalf("resolved to empty string")
			}
			if !containsProtocol(Protocols, got) {
				t.Fatalf("resolved to %q, which is not a member of Protocols", got)
			}
		})
	}
}

// TestNormalizeAdaptiveAllowed pins the setting parser: an ordered JSON array
// of protocol strings, normalized to canonical values, order preserved,
// duplicates collapsed, junk dropped.
func TestNormalizeAdaptiveAllowed(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"empty", nil, nil},
		{"canonical pass-through", []string{ProtocolOpenAIResponses, ProtocolOpenAICompletions},
			[]string{ProtocolOpenAIResponses, ProtocolOpenAICompletions}},
		{"aliases normalized", []string{"openai-response", "claude"}, []string{ProtocolOpenAIResponses, ProtocolAnthropicMessages}},
		{"order preserved", []string{ProtocolOpenAICompletions, ProtocolOpenAIResponses},
			[]string{ProtocolOpenAICompletions, ProtocolOpenAIResponses}},
		{"duplicates collapsed", []string{"openai-responses", ProtocolOpenAIResponses},
			[]string{ProtocolOpenAIResponses}},
		{"junk dropped", []string{"grpc", ProtocolOpenAICompletions}, []string{ProtocolOpenAICompletions}},
		{"adapter selector dropped", []string{ProtocolAdaptive, ProtocolOpenAICompletions}, []string{ProtocolOpenAICompletions}},
		{"blank dropped", []string{"", "   ", ProtocolGeminiGenerate}, []string{ProtocolGeminiGenerate}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NormalizeAdaptiveAllowed(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("NormalizeAdaptiveAllowed(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestAdaptiveEquivalence_NoDeclarationIsByteIdentical is the safety property
// the whole design rests on: with no declaration, "adaptive" must resolve to
// exactly what the pre-existing default would have been. If this ever fails,
// turning the selector on for an undeclared provider would silently change
// live traffic.
func TestAdaptiveEquivalence_NoDeclarationIsByteIdentical(t *testing.T) {
	// A representative slice of vapeur's real 474-model catalogue, spanning
	// every family the recommender knows about.
	models := []string{
		"claude-opus-4-5", "claude-sonnet-4-5", "claude-haiku-4-5", "claude-fable",
		"gpt-5.3-codex", "gpt-5", "gpt-4o", "gpt-6.2-sol", "gpt-chat-latest",
		"o3", "o3-mini", "o4-mini", "codex-max",
		"gemini-3-pro", "gemini-2.5-flash",
		"deepseek-v4", "deepseek-chat", "qwen3.8-max", "qwen4-max",
		"doubao-seed-2.0", "grok-4.6", "kimi-k2.5", "kimi-k3", "MiniMax-M2.5",
		"", "some-unknown-model",
	}
	for _, model := range models {
		got := ResolveAdaptiveProtocol(model, nil)
		if got != ProtocolOpenAICompletions {
			t.Errorf("undeclared adaptive resolved %q → %q, want the repo default %q", model, got, ProtocolOpenAICompletions)
		}
	}
}

// containsProtocol is the local membership helper for tests.
func containsProtocol(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
