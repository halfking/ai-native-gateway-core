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

// TestPeriodicQuotaProbeDefaultProbeModelFallback pins the 2026-08-31
// hzx-2 round-4 audit: PeriodicQuotaProbe must mirror BalanceQuotaProbe
// by treating credentials with no default_probe_model as probeable
// when they have at least one routable binding on credential_model_bindings.
// Without this fallback, freshly-onboarded credentials (hzx-2) whose
// operator never set default_probe_model were silently skipped by every
// probe path (pre-probe, post-expiry, ProbeNow).
func TestPeriodicQuotaProbeDefaultProbeModelFallback(t *testing.T) {
	src, err := os.ReadFile("periodic_quota_probe.go")
	if err != nil {
		t.Fatalf("read periodic quota probe source failed: %v", err)
	}
	body := string(src)
	// Both SELECTs (pre + post) must include the OR-EXISTS fallback to
	// cmb.available=TRUE. A plain "<> ''" alone is the pre-fix shape.
	for _, want := range []string{
		"COALESCE(c.default_probe_model, '') <> ''",
		"credential_model_bindings cmb",
		"cmb.credential_id = c.id",
		"COALESCE(cmb.available, FALSE) = TRUE",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("periodic quota probe default_probe_model fallback missing %q", want)
		}
	}
}

// TestPeriodicQuotaProbeDeviationGuardClampsToGridBoundary pins the
// 2026-08-31 round-6 corrected deviation-guard contract:
//
//  1. THRESHOLD stays at max(recoverAtMaxLinger, 5h). An earlier round-4
//     draft used max(preProbeWindow, linger)=30min as the threshold,
//     which clamped CORRECTLY classified 5h rows (recover_at up to 5h
//     away) and re-introduced the 2026-08-08 probe-success death loop
//     (probe model succeeds → quota cleared early → business 429 →
//     re-suspend → re-clamp, ~35min period). The 5h floor is what keeps
//     healthy 5h credentials untouched.
//  2. TARGET is the next 5h grid boundary passed as $2 (computed in Go
//     via nextFiveHourBoundaryCST), not now()+INTERVAL — the legacy
//     now()+'5 hours' target added a full extra window of delay to
//     misclassified rows whose real reset sits ON the grid boundary.
//  3. Weekly/monthly-worded rows are excluded so the guard cannot
//     compress a 7-day/30-day window into a 5h re-probe loop.
func TestPeriodicQuotaProbeDeviationGuardClampsToGridBoundary(t *testing.T) {
	src, err := os.ReadFile("periodic_quota_probe.go")
	if err != nil {
		t.Fatalf("read periodic quota probe source failed: %v", err)
	}
	full := string(src)

	// Slice out just the recoverAtDeviationGuard function body (excluding
	// its doc comment, which legitimately MENTIONS the legacy SQL shape
	// it replaced). Assertions about SQL shape run against this slice;
	// the helper-existence check runs against the whole file.
	start := strings.Index(full, "func (p *PeriodicQuotaProbe) recoverAtDeviationGuard")
	if start < 0 {
		t.Fatal("recoverAtDeviationGuard function not found")
	}
	rest := full[start:]
	end := strings.Index(rest[4:], "\nfunc ") // next top-level func after this one
	if end >= 0 {
		rest = rest[:end+4]
	}

	// Guard 1: threshold floor of 5h in the Go code.
	if !strings.Contains(rest, "5 * time.Hour") {
		t.Fatal("deviation guard threshold must floor at 5h — a tighter threshold clamps correctly-classified 5h rows and re-introduces the probe-success death loop")
	}
	// Guard 2: clamp writes the Go-computed boundary parameter ($2),
	// NOT now() + INTERVAL. Hard-coded intervals are the regression
	// shape (both the legacy +5h target and the round-4 +30min target).
	if strings.Contains(rest, "INTERVAL '5 hours'") {
		t.Fatal("deviation guard must not hard-code INTERVAL '5 hours' as the clamp target")
	}
	for _, want := range []string{
		"quota_recover_at = $2",
		"c.quota_recover_at > now() + make_interval(secs => $1)",
		// Only shrink, never extend.
		"c.quota_recover_at > $2",
		// Grid-boundary helper is invoked for the clamp target.
		"nextFiveHourBoundaryCST(time.Now())",
		// Guard 3: weekly/monthly wording excludes a row from clamping.
		"NOT LIKE '%week%'",
		"NOT LIKE '%month%'",
		"NOT LIKE '%周%'",
		"NOT LIKE '%月%'",
		"next_5h_boundary_grid_cap",
	} {
		if !strings.Contains(rest, want) {
			t.Fatalf("deviation guard corrected contract missing %q", want)
		}
	}
	if !strings.Contains(full, "func nextFiveHourBoundaryCST(now time.Time) time.Time") {
		t.Fatal("nextFiveHourBoundaryCST helper (mirrors domains/credential/writer.go) must exist")
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
