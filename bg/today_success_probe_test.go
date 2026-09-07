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
	// Healthy pairs already prove themselves through business traffic
	// (MarkNodeProbeHealthy stamps last_attempt_at). Re-probing them more
	// often than the 1h success cadence of node_probe_state is waste.
	if todaySuccessHealthySkipAfter != time.Hour {
		t.Fatalf("healthy skip = %v, want 1h", todaySuccessHealthySkipAfter)
	}
}

func TestTodaySuccessProbeSQLTargetsTodayBusinessSuccess(t *testing.T) {
	sql := todaySuccessProbeSQL()
	for _, marker := range []string{
		"request_logs_hot",
		"now() - interval '24 hours'",
		"rl.success = TRUE",
		"NOT COALESCE('probe' = ANY(rl.quality_flags), FALSE)",
		"last_direct_ok, FALSE) = FALSE",
		"COALESCE(cmb.available, FALSE) = FALSE",
		"interval '60 minutes'",
		"LIMIT $1",
	} {
		if !strings.Contains(sql, marker) {
			t.Fatalf("today-success SQL missing %q", marker)
		}
	}
	if strings.Contains(sql, "interval '3 days'") || strings.Contains(sql, "request_logs rl") {
		t.Fatal("today-success SQL must not reuse the 72h daily dump")
	}
}
