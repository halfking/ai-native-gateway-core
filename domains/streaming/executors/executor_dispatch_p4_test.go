package executors

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/endpointselect"
	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/kaixuan/llm-gateway-go/settings"
)

func TestSelectDispatchEndpointFlagsAndFallback(t *testing.T) {
	primary := provider.Candidate{BaseURL: "http://primary", Protocol: "openai-completions", NativeEndpoints: []endpointselect.EndpointLite{
		{ID: 1, Protocol: "ollama-native", BaseURL: "http://ollama", Enabled: true},
		{ID: 2, Protocol: "anthropic-messages", BaseURL: "http://anthropic", Enabled: true},
	}}
	cases := []struct {
		name, client     string
		selector, native bool
		url, protocol    string
	}{
		{"selector off", "ollama-chat", false, true, "http://primary", "openai-completions"},
		{"native off", "ollama-chat", true, false, "http://primary", "openai-completions"},
		{"native on", "ollama-chat", true, true, "http://ollama", "ollama-native"},
		{"other protocol", "anthropic-messages", true, true, "http://anthropic", "anthropic-messages"},
		{"protocol fallback", "gemini-generate", true, true, "http://primary", "openai-completions"},
		{"default client", "", true, true, "http://primary", "openai-completions"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := selectDispatchEndpoint(primary, tc.client, &settings.P4FeatureFlags{EndpointSelectorEnabled: tc.selector, OllamaNativeEnabled: tc.native})
			if got.BaseURL != tc.url || got.Protocol != tc.protocol {
				t.Fatalf("selected (%s,%s), want (%s,%s)", got.BaseURL, got.Protocol, tc.url, tc.protocol)
			}
		})
	}
	// An ollama-native PRIMARY survives selector rewriting even when the
	// native executor is off: the flag is not allowed to silently re-point
	// the provider at a different base URL. What happens next to that
	// protocol is the switch's job, pinned by
	// TestDispatchRouteOllamaNative_FFGate.
	//
	// r0926: this comment used to claim the switch "must fail closed"
	// instead of sending an OpenAI body to /api/chat. That was the bug, not
	// the intent — see classifyDispatchRoute.
	legacy := provider.Candidate{BaseURL: "http://native-primary", Protocol: "ollama-native"}
	got := selectDispatchEndpoint(legacy, "ollama-chat", &settings.P4FeatureFlags{EndpointSelectorEnabled: true})
	if got.Protocol != "ollama-native" {
		t.Fatalf("primary changed under disabled native executor: %+v", got)
	}
}

// TestDispatchRouteOllamaNative_FFGate pins the FF contract at the SWITCH
// level — the level that actually decides which executor runs.
//
// r0926 audit finding #1: the P4 wiring (commit 3244c5af1) only had a test
// for selectDispatchEndpoint, a pure function that rewrites cand. Nothing
// covered the switch arm that consumes its output, so this regression
// shipped green: with FF_OLLAMA_NATIVE=false an ollama-native primary
// returned KindUnsupportedFeature, while pre-P4 dispatch sent the very same
// candidate to executeOpenAI against Ollama's OpenAI-compatible endpoint.
// That is an ungated outage — reachable with both flags off — and it made
// the documented rollback (docs §5.3) unable to restore service.
//
// Regression shape worth remembering: testing the pure decision helper is
// NOT testing the routing. The helper said "protocol stays ollama-native",
// which was true and useless; the damage was in what the switch did next.
func TestDispatchRouteOllamaNative_FFGate(t *testing.T) {
	cases := []struct {
		name          string
		protocol      string
		flags         *settings.P4FeatureFlags
		want          dispatchRoute
		wantExecutor  string
		wantRationale string
	}{
		{
			name:          "ollama native with flag ON routes to ollama executor",
			protocol:      "ollama-native",
			flags:         &settings.P4FeatureFlags{EndpointSelectorEnabled: true, OllamaNativeEnabled: true},
			want:          routeOllamaNative,
			wantExecutor:  "OllamaExecutor (/api/chat, NDJSON)",
			wantRationale: "flag on: the whole point of P4 is that these requests reach /api/chat",
		},
		{
			name:          "ollama native with flag OFF degrades to openai compat",
			protocol:      "ollama-native",
			flags:         &settings.P4FeatureFlags{EndpointSelectorEnabled: true, OllamaNativeEnabled: false},
			want:          routeOpenAI,
			wantExecutor:  "ChatExecutor (/v1/chat/completions)",
			wantRationale: "flag off must equal pre-P4 dispatch; Ollama serves OpenAI-compat, so this works and is the only documented rollback",
		},
		{
			name:          "ollama native with nil flags degrades to openai compat",
			protocol:      "ollama-native",
			flags:         nil,
			want:          routeOpenAI,
			wantExecutor:  "ChatExecutor (/v1/chat/completions)",
			wantRationale: "nil flags must not panic and must not fail the request",
		},
		{
			name:          "ollama native with both flags OFF degrades to openai compat",
			protocol:      "ollama-native",
			flags:         &settings.P4FeatureFlags{},
			want:          routeOpenAI,
			wantExecutor:  "ChatExecutor (/v1/chat/completions)",
			wantRationale: "this is the default shipped configuration — the exact case that regressed",
		},
		{
			name:          "anthropic unaffected by ollama flag",
			protocol:      "anthropic-messages",
			flags:         &settings.P4FeatureFlags{},
			want:          routeAnthropic,
			wantExecutor:  "AnthropicExecutor (/v1/messages)",
			wantRationale: "no P4 coupling with the Anthropic path",
		},
		{
			name:          "gemini stays explicitly unsupported",
			protocol:      "gemini-generate",
			flags:         &settings.P4FeatureFlags{EndpointSelectorEnabled: true, OllamaNativeEnabled: true},
			want:          routeUnsupportedGemini,
			wantExecutor:  "none (KindUnsupportedFeature)",
			wantRationale: "R60 still owns the Gemini executor; must not silently chat-compat",
		},
		{
			name:          "openai completions unaffected",
			protocol:      "openai-completions",
			flags:         &settings.P4FeatureFlags{EndpointSelectorEnabled: true, OllamaNativeEnabled: true},
			want:          routeOpenAI,
			wantExecutor:  "ChatExecutor (/v1/chat/completions)",
			wantRationale: "legacy default must be untouched by P4",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyDispatchRoute(tc.protocol, tc.flags)
			if got != tc.want {
				t.Fatalf("classifyDispatchRoute(%q, %+v) = %v (%s), want %v (%s) — %s",
					tc.protocol, tc.flags, got, routeName(got), tc.want, routeName(tc.want), tc.wantRationale)
			}
		})
	}
}

// TestDispatchRouteOllamaNative_DefaultFlagsAreOff is the guard on the guard:
// the whole FF design rests on both switches defaulting to false, so a stray
// default flip would silently put every deployment on the new path with no
// code change at all.
func TestDispatchRouteOllamaNative_DefaultFlagsAreOff(t *testing.T) {
	flags := settings.GetP4Flags()
	if flags.EndpointSelectorEnabled {
		t.Error("FF_ENDPOINT_SELECTOR must default to false")
	}
	if flags.OllamaNativeEnabled {
		t.Error("FF_OLLAMA_NATIVE must default to false")
	}
	// And the default configuration must not fail an ollama-native primary.
	if got := classifyDispatchRoute("ollama-native", flags); got != routeOpenAI {
		t.Errorf("default flags: ollama-native routed to %v (%s), want routeOpenAI (legacy compat)",
			got, routeName(got))
	}
}

func routeName(r dispatchRoute) string {
	switch r {
	case routeOpenAI:
		return "routeOpenAI"
	case routeAnthropic:
		return "routeAnthropic"
	case routeOllamaNative:
		return "routeOllamaNative"
	case routeUnsupportedGemini:
		return "routeUnsupportedGemini"
	default:
		return "unknown"
	}
}
