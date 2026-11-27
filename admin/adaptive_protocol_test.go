package admin

import (
	"testing"

	providercatalog "github.com/kaixuan/llm-gateway-go/provider/catalog"
)

// TestNormalizeProviderProtocolInput pins the admin write-side contract for the
// adaptive selector.
//
// createProvider (admin/providers.go:898) and the PATCH path (:1116) shared one
// normalizer, and the R59 audit specifically closed a PATCH-side normalization
// bypass. Adding "adaptive" must not reopen a bypass: it is accepted as an
// EXACT selector value only, and everything else keeps the old strict contract
// (aliases normalized, unknown values 400).
func TestNormalizeProviderProtocolInput(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		// selector accepted, folded to the canonical spelling
		{"adaptive lowercase", "adaptive", providercatalog.ProtocolAdaptive, false},
		{"adaptive mixed case", "Adaptive", providercatalog.ProtocolAdaptive, false},
		{"adaptive padded", "  ADAPTIVE  ", providercatalog.ProtocolAdaptive, false},

		// concrete protocols still go through the normalizer
		{"canonical completions", "openai-completions", providercatalog.ProtocolOpenAICompletions, false},
		{"alias responses", "openai-response", providercatalog.ProtocolOpenAIResponses, false},
		{"alias claude", "claude", providercatalog.ProtocolAnthropicMessages, false},

		// junk still rejected — the selector must not become a wildcard
		{"empty", "", "", true},
		{"blank", "   ", "", true},
		{"unknown", "grpc", "", true},
		{"prefix only", "adapt", "", true},
		{"selector with suffix", "adaptive-v2", "", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeProviderProtocolInput(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("normalizeProviderProtocolInput(%q) = %q, want error", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeProviderProtocolInput(%q) error = %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("normalizeProviderProtocolInput(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
