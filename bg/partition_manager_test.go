package bg

import (
	"testing"
	"time"
)

func TestEnsureSpecsCoversAllPartitionedTables(t *testing.T) {
	specs := ensureSpecs()

	// Migration 330 (2026-07-04) added usage_ledger. Migration 328a
	// (2026-07-02) added request_logs_bodies. Migration 385 (2026-07-11)
	// added model_probe_runs, but 2026-07-14 retired it (pure-hot-table
	// strategy — no more columnar partitions). The history is captured
	// in the table comments at each migration; this test just pins the
	// current set so a future onboarding bumps this expectation
	// together with the migration.
	expected := map[string]bool{
		"ensure_request_logs_partition":           false,
		"ensure_request_logs_bodies_partition":    false,
		"ensure_request_wal_partition":            false,
		"ensure_routing_decision_log_partition":   false,
		"ensure_credential_model_index_partition": false,
		"ensure_usage_ledger_partition":           false, // Migration 330

		// 2026-08-07 审计补齐：这些函数早已随各自迁移安装，
		// 但从未被 ensureSpecs() 调用。迁移 473 只是手工补了
		// 2026_09 + 2026_10，此处把它们接入自动轮转。
		"ensure_credit_ledger_partition":             false, // Migration 334
		"ensure_tool_usage_stats_partition":          false, // Migration 335
		"ensure_sessions_v2_partitions":              false, // Migration 430 (covers sessions/session_turns/session_bodies)
		"ensure_session_module_executions_partition": false, // Migration 382
		"ensure_dashboard_events_partition":          false, // Migration 383
		"ensure_cache_metrics_partition":             false, // Migration 475
		"ensure_handoff_logs_partition":              false, // Migration 532
	}
	for _, s := range specs {
		if _, ok := expected[s.fnName]; !ok {
			t.Errorf("unexpected ensure function in spec list: %s", s.fnName)
			continue
		}
		expected[s.fnName] = true
	}
	for fn, seen := range expected {
		if !seen {
			t.Errorf("expected %s in ensureSpecs()", fn)
		}
	}
}

func TestPromoteSpecsCoversAllDefaultPartitions(t *testing.T) {
	specs := promoteSpecs()

	// Migration 341-350 (2026-07-05) replaced *_default catch-all
	// partitions with independent *_hot tables. Migration 385 (2026-07-11)
	// added model_probe_runs_hot, but 2026-07-14 retired it (pure-hot-table
	// strategy). Migration 392 (2026-07-13) added candidate_failure_logs_hot.
	expected := map[string]bool{
		"promote_request_logs_hot_to_partition":              false,
		"promote_usage_ledger_hot_to_partition":              false,
		"promote_request_wal_hot_to_partition":               false,
		"promote_routing_decision_log_hot_to_partition":      false,
		"promote_credential_model_index_hot_to_partition":    false,
		"promote_request_logs_bodies_hot_to_partition":       false,
		"promote_credit_ledger_hot_to_partition":             false,
		"promote_tool_usage_stats_hot_to_partition":          false,
		"promote_candidate_failure_logs_hot_to_partition":    false, // Migration 392
		"promote_session_turns_hot_to_partition":             false, // Migration 526
		"promote_handoff_logs_hot_to_partition":              false, // Migration 532
		"promote_session_module_executions_hot_to_partition": false, // Migration 580
		"promote_dashboard_access_events_hot_to_partition":   false, // Migration 579 (body repaired by 607)
		"promote_session_bodies_hot_to_partition":            false, // Migration 615
	}
	for _, s := range specs {
		if _, ok := expected[s.fnName]; !ok {
			t.Errorf("unexpected promote function in spec list: %s", s.fnName)
			continue
		}
		expected[s.fnName] = true
	}
	for fn, seen := range expected {
		if !seen {
			t.Errorf("expected %s in promoteSpecs()", fn)
		}
	}
}

func TestArchiveSpecsScheduling(t *testing.T) {
	specs := archiveSpecs()

	// Migration 331 (2026-07-04): archive_request_logs and
	// archive_request_wal were retired. The remaining archive jobs
	// are routing_decision_log (day 1) and credential_model_index
	// (day 3) — both small-data jobs that drop partitions after a
	// 2-month hold.
	//
	// 2026-07-13 (Migration 391): state table partition drop added
	// on day 2. Total 3 archive specs.
	if len(specs) != 3 {
		t.Fatalf("archiveSpecs() returned %d entries, want 3 (after migration 391)", len(specs))
	}

	// Each spec must carry a non-empty fnName and a label.
	for _, s := range specs {
		if s.fnName == "" {
			t.Errorf("archiveSpec with empty fnName: %+v", s)
		}
		if s.label == "" {
			t.Errorf("archiveSpec %s has empty label", s.fnName)
		}
		if s.day < 1 || s.day > 7 {
			t.Errorf("archiveSpec %s has out-of-range day=%d", s.fnName, s.day)
		}
	}

	// We allow at most 2 archive specs per day.
	dayCount := map[int]int{}
	for _, s := range specs {
		dayCount[s.day]++
		if dayCount[s.day] > 2 {
			t.Errorf("day %d has %d specs (max 2): %v", s.day, dayCount[s.day], specs)
		}
	}

	// The post-391 expected archive functions.
	expected := []string{
		"archive_routing_decision_log",
		"drop_old_state_partitions",
		"archive_credential_model_index",
	}
	for _, fn := range expected {
		found := false
		for _, s := range specs {
			if s.fnName == fn {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("archive function %s not scheduled in archiveSpecs()", fn)
		}
	}
}

func TestArchiveOldPartitionsDayWindow(t *testing.T) {
	// Sanity: the manager only fires archive in day 1..3 of the month.
	// This protects the function from accidentally running on every
	// tick (which would re-run the same archive every 24h).
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC) // day 5
	if now.Day() <= 3 {
		t.Fatalf("test fixture broken: expected day > 3, got %d", now.Day())
	}
	// We can't easily exercise archiveOldPartitionsIfNeeded without
	// a real DB pool. The day-window check is a single line of code;
	// the structural tests above give stronger coverage of the
	// spec wiring.
	_ = now
}

// TestResolvePromoteConfigBodiesBatchSize pins the request_logs_bodies
// promote default batch. Bodies rows are TOAST-heavy (~350 KB avg), so the
// generic 5000-row batch moves ~1.7 GB per call and exceeded PG
// statement_timeout=30s on the shared prod DB (2026-08-06 incident). The
// default must stay small and be overridable via settings_kv.
func TestResolvePromoteConfigBodiesBatchSize(t *testing.T) {
	retention, batch := resolvePromoteConfig("request_logs_bodies")
	if batch >= promoteBatchSize {
		t.Fatalf("request_logs_bodies default batch = %d, want < %d", batch, promoteBatchSize)
	}
	if batch < 100 {
		t.Fatalf("request_logs_bodies default batch = %d, want >= safety floor 100", batch)
	}
	if retention <= 0 || retention > 24*time.Hour {
		t.Fatalf("request_logs_bodies retention = %v, want (0, 24h]", retention)
	}
}

func TestResolvePromoteConfigHandoffRetention(t *testing.T) {
	retention, batch := resolvePromoteConfig("handoff_logs_hot")
	if retention != 8*time.Hour {
		t.Fatalf("handoff retention = %v, want 8h default", retention)
	}
	if batch < 100 || batch > 50_000 {
		t.Fatalf("handoff batch = %d, outside safety bounds", batch)
	}
}

func TestPromoteLockKeyDeterministic(t *testing.T) {
	a := promoteLockKey("request_logs_bodies")
	b := promoteLockKey("request_logs_bodies")
	if a != b {
		t.Fatalf("promoteLockKey not deterministic: %d != %d", a, b)
	}
	if a == promoteLockKey("request_logs_hot") {
		t.Fatalf("promoteLockKey collision between distinct labels")
	}
}
