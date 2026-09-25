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
	// Primary Ollama with the native executor disabled must fail closed in the
	// dispatch switch instead of silently sending an OpenAI body to /api/chat.
	legacy := provider.Candidate{BaseURL: "http://native-primary", Protocol: "ollama-native"}
	got := selectDispatchEndpoint(legacy, "ollama-chat", &settings.P4FeatureFlags{EndpointSelectorEnabled: true})
	if got.Protocol != "ollama-native" {
		t.Fatalf("primary changed under disabled native executor: %+v", got)
	}
}
