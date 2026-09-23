package admin

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
		{"openai-completions", "openai-completions", false},
		{"openai-responses", "openai-responses", false},
		{"anthropic-messages", "anthropic-messages", false},
		{"gemini-generate", "gemini-generate", false},
		{"ollama-native", "ollama-native", false},

		// the incident alias + common misspellings
		{"openai-response", "openai-responses", false},
		{"OpenAI-Response", "openai-responses", false},
		{"openai_response", "openai-responses", false},
		{"responses", "openai-responses", false},

		// chat aliases
		{"openai", "openai-completions", false},
		{"openai_chat", "openai-completions", false},
		{"chat/completions-trim", "", true}, // unknown

		// other families
		{"anthropic", "anthropic-messages", false},
		{"claude", "anthropic-messages", false},
		{"gemini", "gemini-generate", false},
		{"ollama", "ollama-native", false},

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
