// bg/active_probe_emitter_test.go — unit tests for the probe telemetry
// emitter helpers, focused on the 2026-07-17 observability fixes:
//   - parseProbeUsage: replaces the hardcoded 1/0 token placeholder with
//     real usage parsed from the upstream response body
//
// The Emit() path itself needs a *telemetry.Client and is exercised
// end-to-end via the worker integration tests; here we cover the pure
// helpers that were previously untested.
package bg

import "testing"

func TestParseProbeUsage(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantPrompt int
		wantCompl  int
		wantOK     bool
	}{
		{
			name:       "openai usage",
			body:       `{"id":"x","choices":[],"usage":{"prompt_tokens":7,"completion_tokens":1}}`,
			wantPrompt: 7, wantCompl: 1, wantOK: true,
		},
		{
			name:       "anthropic usage (input/output tokens)",
			body:       `{"id":"msg_x","usage":{"input_tokens":3,"output_tokens":2}}`,
			wantPrompt: 3, wantCompl: 2, wantOK: true,
		},
		{
			name:   "empty body",
			body:   "",
			wantOK: false,
		},
		{
			name:   "non-json body",
			body:   "Internal Server Error",
			wantOK: false,
		},
		{
			name:   "json without usage object",
			body:   `{"error":"rate limited"}`,
			wantOK: false,
		},
		{
			name:   "usage object with all-zero tokens",
			body:   `{"usage":{"prompt_tokens":0,"completion_tokens":0}}`,
			wantOK: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pt, ct, ok := parseProbeUsage(c.body)
			if ok != c.wantOK {
				t.Fatalf("parseProbeUsage ok = %v, want %v", ok, c.wantOK)
			}
			if ok {
				if pt != c.wantPrompt || ct != c.wantCompl {
					t.Errorf("parseProbeUsage = (%d, %d), want (%d, %d)", pt, ct, c.wantPrompt, c.wantCompl)
				}
			}
		})
	}
}
