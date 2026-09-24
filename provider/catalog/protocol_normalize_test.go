package catalog

import "testing"

// TestNormalizeProviderProtocol covers the 2026-09-23 vapeur incident: a
// provider configured as type "openai-response" (singular) must normalize
// to the canonical "openai-responses" instead of being persisted verbatim
// into the CHECK-less providers.protocol column.
func TestNormalizeProviderProtocol(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		// canonical pass-through
		{"openai-completions", ProtocolOpenAICompletions, false},
		{"openai-responses", ProtocolOpenAIResponses, false},
		{"anthropic-messages", ProtocolAnthropicMessages, false},
		{"gemini-generate", ProtocolGeminiGenerate, false},
		{"ollama-native", ProtocolOllamaNative, false},

		// the incident alias + common misspellings
		{"openai-response", ProtocolOpenAIResponses, false},
		{"OpenAI-Response", ProtocolOpenAIResponses, false},
		{"openai_response", ProtocolOpenAIResponses, false},
		{"responses", ProtocolOpenAIResponses, false},

		// chat aliases
		{"openai", ProtocolOpenAICompletions, false},
		{"openai_chat", ProtocolOpenAICompletions, false},
		{"OPENAI CHAT", "", true},

		// other families
		{"anthropic", ProtocolAnthropicMessages, false},
		{"claude", ProtocolAnthropicMessages, false},
		{"gemini", ProtocolGeminiGenerate, false},
		{"ollama", ProtocolOllamaNative, false},

		// junk
		{"", "", true},
		{"   ", "", true},
		{"grpc", "", true},
	}
	for _, tc := range cases {
		got, err := NormalizeProviderProtocol(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("NormalizeProviderProtocol(%q) = %q, want error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("NormalizeProviderProtocol(%q) error = %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("NormalizeProviderProtocol(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestRecommendedProtocolForModel locks the docs/vendor-formats baseline:
// OpenAI new-gen models default to the Responses API, Anthropic to Messages,
// Gemini to generateContent, everything else to OpenAI-compatible chat.
func TestRecommendedProtocolForModel(t *testing.T) {
	cases := []struct {
		model      string
		want       string
		wantFamily bool
	}{
		{"gpt-5", ProtocolOpenAIResponses, true},
		{"gpt-5-mini", ProtocolOpenAIResponses, true},
		{"gpt-5.2-turbo", ProtocolOpenAIResponses, true},
		{"o1", ProtocolOpenAIResponses, true},
		{"o1-preview", ProtocolOpenAIResponses, true},
		{"o3", ProtocolOpenAIResponses, true},
		{"o3-mini", ProtocolOpenAIResponses, true},
		{"o4-mini", ProtocolOpenAIResponses, true},
		{"codex-max", ProtocolOpenAIResponses, true},

		// chat-era OpenAI models stay on chat — no o-series false hits
		{"gpt-4o", ProtocolOpenAICompletions, false},
		{"gpt-4.1-mini", ProtocolOpenAICompletions, false},
		{"ollama3", ProtocolOpenAICompletions, false},

		{"claude-sonnet-4-5", ProtocolAnthropicMessages, true},
		{"claude", ProtocolAnthropicMessages, true},
		{"gemini-2.5-flash", ProtocolGeminiGenerate, true},
		{"gemini", ProtocolGeminiGenerate, true},

		{"deepseek-chat", ProtocolOpenAICompletions, false},
		{"qwen-max", ProtocolOpenAICompletions, false},
		{"glm-4.7", ProtocolOpenAICompletions, false},
		{"grok-4.6", ProtocolOpenAICompletions, false},
		{"", ProtocolOpenAICompletions, false},
	}
	for _, tc := range cases {
		got, ok := RecommendedProtocolForModel(tc.model)
		if got != tc.want {
			t.Errorf("RecommendedProtocolForModel(%q) = %q, want %q", tc.model, got, tc.want)
		}
		if ok != tc.wantFamily {
			t.Errorf("RecommendedProtocolForModel(%q) family-hit = %v, want %v", tc.model, ok, tc.wantFamily)
		}
	}
}

func TestRecommendedProtocolForBaseURL(t *testing.T) {
	cases := []struct {
		baseURL    string
		want       string
		wantFamily bool
	}{
		{"https://api.anthropic.com", ProtocolAnthropicMessages, true},
		{"https://api.openai.com/v1", ProtocolOpenAICompletions, true},
		{"https://generativelanguage.googleapis.com/v1beta", ProtocolGeminiGenerate, true},
		{"http://127.0.0.1:11434", ProtocolOllamaNative, true},
		{"http://localhost:11434", ProtocolOllamaNative, true},
		{"https://api.vapeur.ai/v1", ProtocolOpenAICompletions, false},
		{"", ProtocolOpenAICompletions, false},
	}
	for _, tc := range cases {
		got, ok := RecommendedProtocolForBaseURL(tc.baseURL)
		if got != tc.want {
			t.Errorf("RecommendedProtocolForBaseURL(%q) = %q, want %q", tc.baseURL, got, tc.want)
		}
		if ok != tc.wantFamily {
			t.Errorf("RecommendedProtocolForBaseURL(%q) family-hit = %v, want %v", tc.baseURL, ok, tc.wantFamily)
		}
	}
}

// TestRecommendedProtocolForBaseURL_F8HostPreciseRegression guards the
// F8 (r0924 supplier-protocol-optimization §3.9) fix: the pre-F8
// substring match was unsafe and would falsely classify look-alike
// domains. The post-F8 rule is host-precise.
func TestRecommendedProtocolForBaseURL_F8HostPreciseRegression(t *testing.T) {
	cases := []struct {
		name       string
		baseURL    string
		want       string
		wantFamily bool
	}{
		// Substring attacks that previously matched "anthropic.com" but
		// should NOT (post-F8 rule: exact hostname match required).
		{"anthropic-in-path", "https://attacker.example/anthropic.com/login", ProtocolOpenAICompletions, false},
		{"anthropic-as-subdomain", "https://api.anthropic.com.attacker.example", ProtocolOpenAICompletions, false},
		{"openai-as-subdomain", "https://api.openai.com.attacker.example", ProtocolOpenAICompletions, false},
		{"gemini-as-path", "https://attacker.example/generativelanguage.googleapis.com", ProtocolOpenAICompletions, false},

		// Ollama-port-only on a non-localhost host should NOT classify as
		// Ollama — operators can override via explicit catalog.
		{"non-localhost on 11434", "http://my-app.example.com:11434", ProtocolOpenAICompletions, false},

		// localhost / loopback variants still trigger the heuristic.
		{"localhost with scheme", "http://localhost:11434", ProtocolOllamaNative, true},
		{"loopback IP with scheme", "http://127.0.0.1:11434", ProtocolOllamaNative, true},
		{"docker internal", "http://host.docker.internal:11434", ProtocolOllamaNative, true},

		// Userinfo handling: a URL like "http://user:pw@api.openai.com/v1"
		// should still match api.openai.com (the userinfo must NOT be
		// confused with the host).
		{"openai with userinfo", "http://user:pass@api.openai.com/v1", ProtocolOpenAICompletions, true},

		// Path on the canonical host still matches.
		{"anthropic with path", "https://api.anthropic.com/v1/messages", ProtocolAnthropicMessages, true},

		// Empty / whitespace inputs still resolve to the safe default.
		{"whitespace", "   ", ProtocolOpenAICompletions, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := RecommendedProtocolForBaseURL(tc.baseURL)
			if got != tc.want {
				t.Errorf("RecommendedProtocolForBaseURL(%q) = %q, want %q", tc.baseURL, got, tc.want)
			}
			if ok != tc.wantFamily {
				t.Errorf("RecommendedProtocolForBaseURL(%q) family-hit = %v, want %v", tc.baseURL, ok, tc.wantFamily)
			}
		})
	}
}
