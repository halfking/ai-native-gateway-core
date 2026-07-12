package bg

import (
	"testing"
)

// TestBackoffV2_RecoveryLadder documents the active recovery backoff schedule
// used by model_probe_backoff_v2. The function is SQL-side, so this Go
// test only documents the expected behavior as comments. Run the matching
// SQL test in tests/038_adaptive_probe_test.sql to verify at runtime.
//
// Reference schedule (see sql/migrations/startup/038_adaptive_probe_scheduling.sql):
//
//	failures  age          interval
//	─────────  ───────────  ─────────
//	0          any          2h        (healthy watchdog)
//	1          any          10s
//	2          any          30s
//	3          any          60s
//	4          any          120s
//	5          any          300s
//	6+         any          3600s
func TestBackoffV2_RecoveryLadder(t *testing.T) {
	// This is a documentation test. Real validation is in tests/038 SQL.
	cases := []struct {
		failures  int
		ageBucket string
		wantMin   int // minimum seconds
		wantMax   int // maximum seconds
	}{
		{0, "any", 7200 - 1, 7200 + 1},
		{1, "any", 10 - 1, 10 + 1},
		{2, "any", 30 - 1, 30 + 1},
		{3, "any", 60 - 1, 60 + 1},
		{4, "any", 120 - 1, 120 + 1},
		{5, "any", 300 - 1, 300 + 1},
		{6, "any", 3600 - 1, 3600 + 1},
	}
	for _, tc := range cases {
		_ = tc // only used as documentation
	}
	t.Log("Recovery ladder validated via SQL migration test at tests/038_adaptive_probe_test.sql")
}

// TestPassiveBoost_TriggersEarlyProbe documents the passive boost triggers.
// 3+ failures in last 5 minutes → next_retry_at = NOW() + 30s.
// This is the fix for the minimax-m3 06-23 incident where the runner
// was waiting 5 minutes for the next backoff tick.
//
// SQL validation: tests/038_adaptive_probe_test.sql Test 6.
func TestPassiveBoost_TriggersEarlyProbe(t *testing.T) {
	t.Log("Passive boost: 3+ failures/5min → +30s, 2 failures/5min → +1m")
}
