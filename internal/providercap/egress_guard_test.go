package providercap

// egress_guard_test.go — 2026-09-09 audit round 3 (#10): the probe/quota
// plane must never hand a credential's API key to a cloud-metadata endpoint,
// and must keep working for self-hosted private gateways by default.

import (
	"strings"
	"testing"
)

func TestEgressBlocked(t *testing.T) {
	cases := []struct {
		url     string
		blocked bool
		reason  string // substring expected in the reason; empty = don't check
	}{
		{"", false, ""},
		{"https://api.openai.com/v1", false, ""},
		{"http://internal.llm.example.com:8000/v1", false, ""},
		// DNS names are out of scope for this preflight.
		{"https://example.com/x", false, ""},
		{"ftp://api.example.com", true, "scheme"},
		{"file:///etc/passwd", true, "scheme"},
		{"https://169.254.169.254/latest/meta-data", true, "metadata"},
		{"https://100.100.100.200/latest/meta-data", true, "metadata"},
		{"http://[fe80::1]/v1", true, "metadata"},
		{"http://metadata.google.internal", false, ""}, // DNS name: dial-hook's job
		// Private IP literals stay allowed under the default policy
		// (self-hosted vLLM/one-api gateways) — the deny side of the
		// PROVIDERCAP_EGRESS_ALLOW_PRIVATE opt-out is env-cached and thus
		// not unit-testable alongside the default.
		{"http://192.168.1.10:8000/v1", false, ""},
		{"http://127.0.0.1:11434/v1", false, ""},
	}
	for _, c := range cases {
		blocked, reason := EgressBlocked(c.url)
		if blocked != c.blocked {
			t.Errorf("EgressBlocked(%q) = %v (reason %q), want %v", c.url, blocked, reason, c.blocked)
		}
		if c.blocked && c.reason != "" && !strings.Contains(reason, c.reason) {
			t.Errorf("EgressBlocked(%q) reason %q missing substring %q", c.url, reason, c.reason)
		}
	}
}
