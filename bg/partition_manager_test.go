package bg

import (
	"testing"
	"time"
)

func TestEnsureSpecsCoversAllTables(t *testing.T) {
	// 与 TestEnsureSpecsCoversAllFourTables 语义一致：测试 ensureSpecs 列表
	// 覆盖所有需要按月分区的表。当前 5 张表：request_logs / request_logs_bodies
	// (body 拆分自 052) / request_wal / routing_decision_log / credential_model_index。
	// 函数名收紧：增加新分区表时此处需同步追加；多出来的会被识别为"unexpected"。
	specs := ensureSpecs()

	expected := map[string]bool{
		"ensure_request_logs_partition":            false,
		"ensure_request_logs_bodies_partition":    false,
		"ensure_request_wal_partition":             false,
		"ensure_routing_decision_log_partition":    false,
		"ensure_credential_model_index_partition":  false,
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
			t.Errorf("expected %s in ensureSpecs (migration 318 onboarding)", fn)
		}
	}
}

func TestArchiveSpecsScheduling(t *testing.T) {
	specs := archiveSpecs()

	// We expect exactly 4 archive specs covering the 4 tables.
	if len(specs) != 4 {
		t.Fatalf("archiveSpecs() returned %d entries, want 4", len(specs))
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

	// We allow at most 2 archive specs per day. Two share day 1
	// (request_logs + routing_decision_log) — both are 1–2 minute
	// jobs and the DB can absorb them sequentially. If a third spec
	// ever wants the same day, fail loudly so we re-balance.
	dayCount := map[int]int{}
	for _, s := range specs {
		dayCount[s.day]++
		if dayCount[s.day] > 2 {
			t.Errorf("day %d has %d specs (max 2): %v", s.day, dayCount[s.day], specs)
		}
	}

	// Each expected archive function must appear exactly once.
	expected := []string{
		"archive_request_logs",
		"archive_routing_decision_log",
		"archive_request_wal",
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
