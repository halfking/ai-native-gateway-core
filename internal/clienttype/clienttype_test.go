package clienttype

import "testing"

func TestNormalize(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want string
	}{
		{" Cursor ", "cursor"},
		{"CLAUDE-CODE", "claude-code"},
		{"my-custom-agent", Unknown},
		{"cursor|other", Unknown},
		{"", Unknown},
		{"   ", Unknown},
		// 2026-09-21 audit: domestic coding-agent clients (canonical spellings).
		{"MiniMax-Code", "minimax-code"},
		{"MINIMAX-CODE", "minimax-code"},
		{"DeepSeek-Code", "deepseek-code"},
		{"deepseek-code", "deepseek-code"},
	} {
		if got := Normalize(tt.in); got != tt.want {
			t.Errorf("Normalize(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestNormalize_DomesticClientsAcceptAllSpellings pins the canonical
// spellings accepted for the two domestic coding-agent clients added in
// the 2026-09-21 audit. Normalize is case-insensitive but strict on
// separators: "minimax_code" (snake_case) and "deepseek-code-cli"
// (suffixed) are NOT canonical and must NOT silently expand the
// Prometheus label cardinality.
//
// 2026-09-21 audit.
func TestNormalize_DomesticClientsAcceptAllSpellings(t *testing.T) {
	cases := map[string]string{
		// Canonical (lowercased)
		"minimax-code": "minimax-code",
		"deepseek-code": "deepseek-code",
		// Mixed case → lowercased canonical
		"MiniMax-Code": "minimax-code",
		"DeepSeek-Code": "deepseek-code",
		"MiniMax-CODE": "minimax-code",
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}

	// Anti-cases: these must NOT silently expand cardinality.
	antiCases := []string{
		"minimax_code",       // snake_case → unknown
		"deepseek_code",      // snake_case → unknown
		"deepseek-code-cli",  // suffixed → unknown
		"minimax-code-pro",   // suffixed → unknown
	}
	for _, in := range antiCases {
		if got := Normalize(in); got != Unknown {
			t.Errorf("Normalize(%q) = %q, want %q (silent cardinality expansion!)", in, got, Unknown)
		}
	}
}
