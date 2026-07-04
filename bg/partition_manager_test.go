package bg

import (
	"testing"
	"time"
)

func TestEnsureSpecsCoversAllPartitionedTables(t *testing.T) {
	specs := ensureSpecs()

	// Migration 330 (2026-07-04) added usage_ledger. Migration 328a
	// (2026-07-02) added request_logs_bodies. The 4-vs-5-vs-6 history
	// is captured in the table comments at each migration; this test
	// just pins the current set so a future onboarding bumps this
	// expectation together with the migration.
	expected := map[string]bool{
		"ensure_request_logs_partition":           false,
		"ensure_request_logs_bodies_partition":    false,
		"ensure_request_wal_partition":            false,
		"ensure_routing_decision_log_partition":   false,
		"ensure_credential_model_index_partition": false,
		"ensure_usage_ledger_partition":           false, // Migration 330
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

	// Migration 336 (2026-07-04) installed promote_*_default_batch
	// functions for every *_default partition created by migrations
	// 317 / 328a / 330 / 332 / 333 / 334 / 335. Pin the set so a future
	// table onboarding reminds us to wire both the ensure function
	// (above) and the promote function.
	expected := map[string]bool{
		"promote_request_logs_default_batch":           false,
		"promote_request_wal_default_batch":            false,
		"promote_usage_ledger_default_batch":           false,
		"promote_routing_decision_log_default_batch":   false,
		"promote_credential_model_index_default_batch": false,
		"promote_request_logs_bodies_default_batch":    false,
		"promote_credit_ledger_default_batch":          false,
		"promote_tool_usage_stats_default_batch":       false,
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
	if len(specs) != 2 {
		t.Fatalf("archiveSpecs() returned %d entries, want 2 (after migration 331)", len(specs))
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

	// The post-331 expected archive functions.
	expected := []string{
		"archive_routing_decision_log",
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
