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
//
// R22 (2026-09-13): the legacy recordBalanceCheck / balance_last_checked_at
// checks are gone — chat probes no longer touch that column (R21 P2
// leftover #1: the probe path was forging the guard's 15-min freshness
// gate). The freshness column is now exclusively written by
// bg/balance_floor_guard.refreshBalance.
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
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("balance quota probe is missing %q", want)
		}
	}
}

// TestBalanceQuotaProbeDoesNotForgeFreshness pins the R21 P2-leftover #1
// fix (2026-09-13): chat probes from this worker must NOT bump
// credentials.balance_last_checked_at. That column is the guard's
// 15-min freshness gate (bg/balance_floor_guard.sweepCurrencyFloors
// pass A); a chat probe doesn't refresh balance_usd, so a stale
// balance_usd paired with a fresh balance_last_checked_at would let
// pass B treat stale evidence as fresh and let pass A skip the row.
//
// The assertion targets SQL `SET balance_last_checked_at` and any
// helper that would reintroduce the legacy write — comments that
// merely reference the column for documentation are allowed (and
// necessary so future readers don't re-add the helper).
func TestBalanceQuotaProbeDoesNotForgeFreshness(t *testing.T) {
	src, err := os.ReadFile("balance_quota_probe.go")
	if err != nil {
		t.Fatalf("read balance quota probe source failed: %v", err)
	}
	body := string(src)
	if strings.Contains(body, "SET balance_last_checked_at") {
		t.Fatalf("balance quota probe must not write balance_last_checked_at " +
			"— that column is owned by bg/balance_floor_guard.refreshBalance (R21 P2 leftover #1)")
	}
	// recordBalanceCheck was removed entirely; re-introducing it without
	// removing the SET balance_last_checked_at write would re-introduce
	// the bug (its original purpose was exactly that write).
	if strings.Contains(body, "func (p *BalanceQuotaProbe) recordBalanceCheck(") {
		t.Fatalf("recordBalanceCheck must not be reintroduced (R21 P2 leftover #1)")
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

// TestBalanceQuotaProbeForceProbeCooldownReleaseOnNotEligible pins the
// 2026-08-23 hzx-2 audit follow-up fix (BUG 2): ForceProbe must release
// the cooldown mark when the credential isn't eligible (state already
// recovered, DB error, or worker un-wired), so an operator who clicks
// during a transient state isn't locked out for forceCooldown seconds.
func TestBalanceQuotaProbeForceProbeCooldownReleaseOnNotEligible(t *testing.T) {
	src, err := os.ReadFile("balance_quota_probe.go")
	if err != nil {
		t.Fatalf("read balance quota probe source failed: %v", err)
	}
	body := string(src)
	for _, want := range []string{
		// All non-dispatch paths must call releaseCooldown.
		"releaseCooldown()",
		// The DB-error branch must release the cooldown (BUG 3).
		`"balance_quota_probe: force probe eligibility check failed"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("ForceProbe cooldown-release plumbing missing %q", want)
		}
	}
}

// TestBalanceQuotaProbeEligibilityErrorPropagation pins BUG 3: the
// eligibility check must return (false, err) on any non-ErrNoRows error
// instead of silently treating the unknown state as eligible.
func TestBalanceQuotaProbeEligibilityErrorPropagation(t *testing.T) {
	src, err := os.ReadFile("balance_quota_probe.go")
	if err != nil {
		t.Fatalf("read balance quota probe source failed: %v", err)
	}
	body := string(src)
	for _, want := range []string{
		// Signature changed from bool to (bool, error).
		"credentialEligibleForForceProbe(credID int) (bool, error)",
		// pgx.ErrNoRows is the ONLY error that means "recovered".
		"errors.Is(err, pgx.ErrNoRows)",
		// All other errors propagate; caller does not dispatch.
		`return false, err`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("eligibility check error propagation missing %q", want)
		}
	}
}

// TestBalanceQuotaProbeForceProbeDataRaceFix pins RISK 1: the GC
// trigger reads len(p.forceLastSeen) outside the lock in the original
// implementation. The fix moves both the len() check and the GC loop
// inside the lock so concurrent ForceProbe calls don't race on the
// map. The test fails if the standalone gcForceHistory helper is
// reintroduced (the helper is dead code after the fix).
func TestBalanceQuotaProbeForceProbeDataRaceFix(t *testing.T) {
	src, err := os.ReadFile("balance_quota_probe.go")
	if err != nil {
		t.Fatalf("read balance quota probe source failed: %v", err)
	}
	body := string(src)
	if strings.Contains(body, "func (p *BalanceQuotaProbe) gcForceHistory(") {
		t.Fatalf("gcForceHistory must be removed; GC is inlined into ForceProbe under the lock")
	}
	// The len() check must happen while holding forceMu.
	if !strings.Contains(body, "mapLen := len(p.forceLastSeen)") {
		t.Fatalf("len(p.forceLastSeen) must be captured under forceMu to avoid data race")
	}
}

// TestBalanceQuotaProbeExemptsBalanceFloorPulled pins the 2026-09-13
// balance_floor guard collaboration contract: rows pulled from the pool by
// bg/balance_floor_guard (state_reason_code='balance_floor') must NOT be
// chat-probed by this worker. They still have buffer quota, so a chat probe
// would succeed and writeHealth would flip them straight back to ok/ready —
// a 2-minute ping-pong. Recovery for those rows belongs to the guard's own
// balance/plan re-checks.
func TestBalanceQuotaProbeExemptsBalanceFloorPulled(t *testing.T) {
	src, err := os.ReadFile("balance_quota_probe.go")
	if err != nil {
		t.Fatalf("read balance quota probe source failed: %v", err)
	}
	body := string(src)
	for _, want := range []string{
		`COALESCE(c.state_reason_code, '') <> 'balance_floor'`,
		"balance_floor guard exemption",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("balance quota probe is missing %q", want)
		}
	}
}
