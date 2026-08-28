package bg

import (
	"os"
	"strings"
	"testing"
)

func TestPeriodicQuotaProbeSkipsDisabledCredentials(t *testing.T) {
	src, err := os.ReadFile("periodic_quota_probe.go")
	if err != nil {
		t.Fatalf("read periodic quota probe source failed: %v", err)
	}
	body := string(src)
	for _, want := range []string{
		"c.status = 'active'",
		"c.lifecycle_status = 'active'",
		"COALESCE(c.manual_disabled, FALSE) = FALSE",
		"COALESCE(p.manual_disabled, FALSE) = FALSE",
		"p.enabled = TRUE",
		"c.quota_recover_at IS NULL OR c.quota_recover_at <= now()",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("periodic quota probe is missing recovery guard %q", want)
		}
	}
	if strings.Contains(body, "c.auto_disabled_at IS NOT NULL") {
		t.Fatal("disabled credentials must not receive automatic quota probes")
	}
}

// TestPeriodicQuotaProbeLayeredStrategy pins the 2026-08-23 hzx-2
// audit additions: the worker must run a pre-probe (boundary-approaching)
// phase in addition to the post-expiry phase, and must clamp
// quota_recover_at values that drifted past the next 5h boundary.
func TestPeriodicQuotaProbeLayeredStrategy(t *testing.T) {
	src, err := os.ReadFile("periodic_quota_probe.go")
	if err != nil {
		t.Fatalf("read periodic quota probe source failed: %v", err)
	}
	body := string(src)
	for _, want := range []string{
		// Pre-probe phase runs before the legacy post-expiry path.
		"probePreExhausted",
		// Pre-probe window is configurable via env (default 60s).
		"LLM_GATEWAY_PERIODIC_QUOTA_PRE_PROBE_WINDOW",
		// Boundary-approaching predicate (recover_at > now() but <= now+window).
		"quota_recover_at <= now() + make_interval(secs => $1)",
		// Recover_at deviation guard clamps deviant rows.
		"recoverAtDeviationGuard",
		// Deviation guard uses an env-overridable linger window.
		"LLM_GATEWAY_PERIODIC_QUOTA_RECOVER_AT_MAX_LINGER",
		// The tick orchestrates the three layers in order.
		"func (p *PeriodicQuotaProbe) tick(",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("periodic quota probe layered strategy missing %q", want)
		}
	}
}

func TestCredentialProbeKeepsPeriodicRecoveryFlow(t *testing.T) {
	src, err := os.ReadFile("credential_probe_v2.go")
	if err != nil {
		t.Fatalf("read credential probe source failed: %v", err)
	}
	body := string(src)
	for _, want := range []string{
		"COALESCE(quota_state, 'ok') = 'periodic_exhausted'",
		"quota_recover_at IS NULL OR quota_recover_at <= now()",
		"lifecycle_status = CASE",
		"periodic_quota_probe_recovered",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("credential probe recovery contract is missing %q", want)
		}
	}
}
