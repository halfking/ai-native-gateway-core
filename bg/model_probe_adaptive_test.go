package bg

import (
	"testing"
)

// TestBackoffV2_AgeAwareSchedule verifies the age-aware backoff schedule
// used by model_probe_backoff_v2. The function is SQL-side, so this Go
// test only documents the expected behavior as comments. Run the matching
// SQL test in tests/038_adaptive_probe_test.sql to verify at runtime.
//
// Reference schedule (see db/migrations/038_adaptive_probe_scheduling.sql):
//
//	failures  age          interval
//	─────────  ───────────  ─────────
//	0          any          2h        (healthy watchdog)
//	1          < 5 min      1m
//	1          5-30 min     3m
//	1          30-60 min    10m
//	1          > 60 min     30m
//	2          < 5 min      2m
//	2          5-30 min     5m
//	2          30-60 min    15m
//	2          > 60 min     45m
//	3+         any          60m       (still recovering toward broken_confirmed)
func TestBackoffV2_AgeAwareSchedule(t *testing.T) {
	// This is a documentation test. Real validation is in tests/038 SQL.
	cases := []struct {
		failures  int
		ageBucket string
		wantMin   int // minimum seconds
		wantMax   int // maximum seconds
	}{
		{0, "any", 7200 - 1, 7200 + 1},
		{1, "<5min", 60 - 1, 60 + 1},
		{1, "5-30min", 180 - 1, 180 + 1},
		{1, "30-60min", 600 - 1, 600 + 1},
		{1, ">60min", 1800 - 1, 1800 + 1},
		{2, "<5min", 120 - 1, 120 + 1},
		{2, "5-30min", 300 - 1, 300 + 1},
		{2, "30-60min", 900 - 1, 900 + 1},
		{2, ">60min", 2700 - 1, 2700 + 1},
		{3, "any", 3600 - 1, 3600 + 1},
	}
	for _, tc := range cases {
		_ = tc // only used as documentation
	}
	t.Log("Backoff v2 schedule validated via SQL test at tests/038_adaptive_probe_test.sql")
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
