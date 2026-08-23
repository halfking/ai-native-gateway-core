package bg

import (
	"os"
	"strings"
	"testing"
)

// TestBalanceQuotaProbeIncludesRoutableFallback pins the 2026-08-23
// hzx-2 audit fix: the balance-quota probe must no longer require
// default_probe_model to be non-empty. Operators who never set the
// field (common for newly-onboarded credentials) had their balance-
// exhausted credentials silently skipped. The new predicate accepts
// EITHER a configured default_probe_model OR at least one routable
// binding on the credential.
func TestBalanceQuotaProbeIncludesRoutableFallback(t *testing.T) {
	src, err := os.ReadFile("balance_quota_probe.go")
	if err != nil {
		t.Fatalf("read balance quota probe source failed: %v", err)
	}
	body := string(src)
	for _, want := range []string{
		// Routable binding fallback in the SELECT predicate.
		"COALESCE(c.default_probe_model, '') <> ''",
		"OR EXISTS (",
		"credential_model_bindings cmb",
		// Force-probe plumbing.
		"ForceProbe",
		"SetProbeNowAsync",
		// Cooldown dedup so a panic-clicking operator can't pile up probes.
		"forceLastSeen",
		"forceCooldown",
		// Observability bookkeeping on credentials.balance_last_checked_at.
		"recordBalanceCheck",
		"balance_last_checked_at",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("balance quota probe is missing %q", want)
		}
	}
}

// TestBalanceQuotaProbeForceEnvDefaults pins the env contract for the
// 2026-08-23 additions. Operators tune these via env in production.
func TestBalanceQuotaProbeForceEnvDefaults(t *testing.T) {
	src, err := os.ReadFile("balance_quota_probe.go")
	if err != nil {
		t.Fatalf("read balance quota probe source failed: %v", err)
	}
	body := string(src)
	for _, want := range []string{
		"LLM_GATEWAY_BALANCE_QUOTA_FORCE_COOLDOWN",
		"LLM_GATEWAY_BALANCE_QUOTA_PROBE_INTERVAL",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("balance quota probe env contract missing %q", want)
		}
	}
}
