package bg

import (
	"strings"
	"testing"
	"time"
)

func TestTodaySuccessProbeCadenceIsBounded(t *testing.T) {
	if todaySuccessProbeInterval != 15*time.Minute {
		t.Fatalf("interval = %v, want 15m", todaySuccessProbeInterval)
	}
	if todaySuccessProbeBatch > 50 {
		t.Fatalf("batch = %d, want ≤50 so the scan stays cheap", todaySuccessProbeBatch)
	}
	// 2026-09-20 probe-volume policy: per-credential cap replaces the old
	// 1h healthy re-probe cadence (todaySuccessHealthySkipAfter). The scan is
	// a recovery re-verification: a credential contributes at most 2 pairs
	// per tick, which together with the two-success gate bounds the pass.
	if todaySuccessPerCredentialCap != 2 {
		t.Fatalf("per-credential cap = %d, want 2", todaySuccessPerCredentialCap)
	}
}

// TestTodaySuccessProbeSQLRecoveryOnly pins the 2026-09-20 re-scope: only
// pairs that carried real (non-probe) business success in 24h AND are
// currently judged unhealthy are submitted; the healthy-hourly re-probe arm
// must stay gone.
func TestTodaySuccessProbeSQLRecoveryOnly(t *testing.T) {
	sql := todaySuccessProbeSQL()
	for _, marker := range []string{
		"request_logs_hot",
		"now() - interval '24 hours'",
		"rl.success = TRUE",
		"NOT COALESCE('probe' = ANY(rl.quality_flags), FALSE)",
		// current-unhealthy signal (either side of the OR)
		"COALESCE(cmb.available, TRUE) = FALSE",
		"COALESCE(nps.last_direct_ok, TRUE) = FALSE",
		// INV-4 two-consecutive-success gate
		credentialTwoProbeSuccessGateSQL("unhealthy.id"),
		// per-credential cap
		"rn <= 2",
		"LIMIT $1",
	} {
		if !strings.Contains(sql, marker) {
			t.Fatalf("today-success SQL missing %q", marker)
		}
	}
	for _, banned := range []string{
		// the pre-2026-09-20 healthy re-probe arms
		"interval '60 minutes'",
		"nps.last_attempt_at IS NULL",
		"nps.credential_id IS NULL",
	} {
		if strings.Contains(sql, banned) {
			t.Fatalf("today-success SQL must not contain %q (healthy re-probe arm)", banned)
		}
	}
}
