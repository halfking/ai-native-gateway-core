package bg

import (
	"strings"
	"testing"
)

// TestReviveExpiredReadySQLGuards pins the invariants of the zombie-ready
// rescue added after the 2026-08-18 glm-5.2 incident, where 'ready' rows
// with an elapsed expires_at could never be claimed (Claim requires
// expires_at > now()) yet kept showing as pending on dashboards while the
// pipeline executed nothing.
func TestReviveExpiredReadySQLGuards(t *testing.T) {
	sql := reviveExpiredReadySQL()
	mustContain := []string{
		// only zombie ready rows are eligible
		"status='ready'",
		"expires_at <= now()",
		// rows with attempts left get a TTL covering the whole backoff hop
		"WHEN attempt < max_attempts THEN GREATEST(next_run_at, now()) + $1::interval",
		"WHEN attempt < max_attempts THEN status",
		// exhausted rows are terminal, not pending
		"ELSE 'expired'",
	}
	for _, want := range mustContain {
		if !strings.Contains(sql, want) {
			t.Fatalf("reviveExpiredReadySQL missing guard %q\nSQL: %s", want, sql)
		}
	}
}

// TestMisclassifiedPeriodicQuotaReclassSQLGuards pins the 存量 reclass that
// frees zhipu GLM Coding Plan credentials stuck in permanently_exhausted
// since before the 5h-window classification fix.
func TestMisclassifiedPeriodicQuotaReclassSQLGuards(t *testing.T) {
	sql := misclassifiedPeriodicQuotaReclassSQL()
	mustContain := []string{
		// only hard-quota rows with periodic evidence are touched
		"quota_state = 'permanently_exhausted'",
		"LIKE '[quota_periodic]%'",
		// downgrade target
		"quota_state = 'periodic_exhausted'",
		// recovery timers become eligible immediately, preserving any
		// already-scheduled time
		"COALESCE(quota_recover_at, now())",
		"COALESCE(availability_recover_at, now())",
	}
	for _, want := range mustContain {
		if !strings.Contains(sql, want) {
			t.Fatalf("misclassifiedPeriodicQuotaReclassSQL missing guard %q\nSQL: %s", want, sql)
		}
	}
	if strings.Contains(sql, "lifecycle_status") {
		t.Fatalf("reclass must not depend on lifecycle_status (suspended-by-quota rows must be freed for probing)")
	}
}

// TestCompleteProbeTaskRearmExtendsExpiry pins the re-arm half of the
// expires_at fix: a failure re-arm (status='ready' with a future
// next_run_at) must extend expires_at, otherwise the retry ladder (5s → 6h)
// always outlives the 5-minute task TTL and the task dies mid-ladder.
func TestCompleteProbeTaskRearmExtendsExpiry(t *testing.T) {
	// The SQL is inline in Complete; assert via the source-level contract by
	// scanning the method's SQL constant through the package symbol. Since
	// the SQL lives inline, mirror the guard here as a compile-time reminder:
	// if this test fails after a refactor, the CASE arm was lost.
	sql := completeProbeTaskSQL()
	for _, want := range []string{
		"expires_at=CASE WHEN $2='ready' THEN now() + $12::interval ELSE expires_at END",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("Complete() re-arm must extend expires_at; missing %q\nSQL: %s", want, sql)
		}
	}
}

func TestProbeQueueEnqueuePersistsScheduledRunTime(t *testing.T) {
	sql := probeEnqueueSQL()
	for _, want := range []string{"dedup_key, next_run_at, expires_at", "$17", "now()+$18"} {
		if !strings.Contains(sql, want) {
			t.Fatalf("probeEnqueueSQL missing %q\nSQL: %s", want, sql)
		}
	}
}
