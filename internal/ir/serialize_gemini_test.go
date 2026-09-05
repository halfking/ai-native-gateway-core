package ir

import "testing"

// TestToolUseNameFromID pins the prefix/suffix-stripping contract used by the
// Gemini serializer when reconstructing functionResponse.name from a
// tool_use_id. The 2026-09-01 P0-1 fix appended "_<partIdx>" to disambiguate
// parallel functionCall parts; the serializer must strip that suffix on
// round-trip so the parsed name still matches.
func TestToolUseNameFromID(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"gemini_call_lookup", "lookup"},
		{"gemini_call_lookup_0", "lookup"},
		{"gemini_call_lookup_1", "lookup"},
		{"gemini_call_lookup_42", "lookup"},
		// Name itself contains underscore: "get_weather" — keep it intact.
		{"gemini_call_get_weather_0", "get_weather"},
		// No prefix: pass through.
		{"call_abc", "call_abc"},
		// Empty: pass through.
		{"", ""},
		// Prefix only (degenerate): pass through (no name to extract).
		{"gemini_call_", "gemini_call_"},
	}
	for _, tc := range cases {
		got := toolUseNameFromID(tc.in)
		if got != tc.want {
			t.Errorf("toolUseNameFromID(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
