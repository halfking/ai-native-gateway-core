package bg

import (
	"strings"
	"testing"
	"time"
)

// Tests for the 2026-09-05 audit D-2#7 watermark window and the E-#4
// minute→hour→day rollup chain in supplier_error_stats_aggregator.go.

func TestSupplierErrorStatsRollupWindowInitialLookback(t *testing.T) {
	a := &SupplierErrorStatsAggregator{interval: supplierErrorStatsDefaultInterval}
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)

	from, to, clamped := a.rollupWindow(now)
	if clamped {
		t.Fatal("initial window must not be clamped")
	}
	// No watermark yet: recompute the last 10 minutes (2 default intervals),
	// equivalent to the previous fixed [now-10min, now) window that covered
	// late-arriving detail rows.
	if want := 10 * time.Minute; now.Sub(from) != want {
		t.Fatalf("initial lookback = %v, want %v", now.Sub(from), want)
	}
	if !to.Equal(now) {
		t.Fatalf("window upper bound = %v, want %v", to, now)
	}
}

func TestSupplierErrorStatsRollupWindowAdvancesOnSuccess(t *testing.T) {
	a := &SupplierErrorStatsAggregator{interval: supplierErrorStatsDefaultInterval}
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)

	// First tick succeeds → watermark advances to its upper bound.
	_, to, _ := a.rollupWindow(now)
	a.watermark = to

	// Next tick recomputes only [watermark, now): late rows that arrived
	// since the last success are picked up, already-aggregated buckets are
	// refreshed idempotently via UPSERT.
	later := now.Add(5 * time.Minute)
	from2, to2, clamped := a.rollupWindow(later)
	if clamped {
		t.Fatal("healthy watermark must not clamp")
	}
	if !from2.Equal(now) {
		t.Fatalf("second window from = %v, want watermark %v", from2, now)
	}
	if !to2.Equal(later) {
		t.Fatalf("second window to = %v, want %v", to2, later)
	}
}

func TestSupplierErrorStatsRollupWindowRecomputesAfterFailure(t *testing.T) {
	a := &SupplierErrorStatsAggregator{interval: supplierErrorStatsDefaultInterval}
	base := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)

	// Tick 1 succeeds; ticks 2 and 3 fail without advancing the watermark.
	_, to, _ := a.rollupWindow(base)
	a.watermark = to

	// After a 40-minute outage (8 failed ticks) the next success must
	// recompute the whole gap from the last watermark — no missing buckets,
	// no duplicates (UPSERT collapses them).
	afterOutage := base.Add(40 * time.Minute)
	from, _, clamped := a.rollupWindow(afterOutage)
	if clamped {
		t.Fatal("40-minute gap must not clamp (under 48h max window)")
	}
	if !from.Equal(base) {
		t.Fatalf("post-outage window from = %v, want last watermark %v", from, base)
	}
	if got := afterOutage.Sub(from); got != 40*time.Minute {
		t.Fatalf("post-outage window width = %v, want 40m (the full gap)", got)
	}
}

func TestSupplierErrorStatsRollupWindowClampsToMaxWindow(t *testing.T) {
	a := &SupplierErrorStatsAggregator{interval: supplierErrorStatsDefaultInterval}
	base := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)

	_, to, _ := a.rollupWindow(base)
	a.watermark = to

	// #7 (2026-09-14): the base source reads hot ∪ historical parent, so a
	// 10h stall is fully recomputable — only a stall beyond the 48h cap is
	// clamped (catch-up backlog bound, not a data-reachability cliff).
	afterStall := base.Add(10 * time.Hour)
	from, _, clamped := a.rollupWindow(afterStall)
	if clamped {
		t.Fatal("10h stale watermark must not clamp (under 48h max window)")
	}
	if !from.Equal(to) {
		t.Fatalf("post-stall window from = %v, want last watermark %v", from, to)
	}

	// 50h stall: the catch-up window is clamped to 48h and flagged so
	// rollup() can warn.
	afterLongStall := base.Add(50 * time.Hour)
	from2, _, clamped2 := a.rollupWindow(afterLongStall)
	if !clamped2 {
		t.Fatal("50h stale watermark must clamp")
	}
	if want := 48 * time.Hour; afterLongStall.Sub(from2) != want {
		t.Fatalf("clamped window width = %v, want %v", afterLongStall.Sub(from2), want)
	}
}

// TestSupplierErrorStatsRollupSQLContract pins the three-level rollup
// chain: every level must upsert onto the V371 unique bucket key
// (idempotent redo) and the hour/day levels must derive from the
// next-finer granularity with a bucket-aligned lower bound so a partial
// window can never overwrite a complete bucket with truncated sums.
func TestSupplierErrorStatsRollupSQLContract(t *testing.T) {
	conflictKey := strings.ToUpper("ON CONFLICT (stat_time, granularity, supplier, credential_id, error_type, model)")

	minute := strings.ToUpper(supplierErrorStatsRollupSQL)
	for _, want := range []string{
		"FROM SUPPLIER_ERRORS_HOT",
		"DATE_BIN",
		conflictKey,
		"DO UPDATE SET",
		// E-#6 quality-of-service buckets.
		"RETRYABLE_COUNT",
		"STAGE_COUNTS",
		"JSONB_OBJECT_AGG",
		"SUM(CASE WHEN IS_RETRYABLE THEN 1 ELSE 0 END)",
	} {
		if !strings.Contains(minute, want) {
			t.Errorf("minute rollup SQL missing %q", want)
		}
	}

	// #7 (2026-09-14): the minute base source must span hot AND the
	// historical parent via UNION ALL — promote is an atomic DELETE+INSERT,
	// so a row lives on exactly one side (no double counting) and the
	// aggregator stays reachable across promoted windows.
	if got := strings.Count(minute, "FROM SUPPLIER_ERRORS"); got < 2 {
		t.Errorf("minute rollup base must read hot AND parent tables, got %d FROM SUPPLIER_ERRORS* references", got)
	}
	if !strings.Contains(minute, "UNION ALL") {
		t.Error("minute rollup base must UNION ALL hot with the historical parent")
	}

	hour := strings.ToUpper(supplierErrorStatsHourRollupSQL)
	for _, want := range []string{
		"GRANULARITY = 'MINUTE'",
		"DATE_TRUNC('HOUR', STAT_TIME)",
		"DATE_TRUNC('HOUR', $1::TIMESTAMPTZ)",
		"'HOUR'",
		conflictKey,
		// E-#6: bucket counts roll up from the finer granularity.
		"SUM(RETRYABLE_COUNT)::INT",
		"JSONB_EACH",
		"EXCLUDED.STAGE_COUNTS",
	} {
		if !strings.Contains(hour, want) {
			t.Errorf("hour rollup SQL missing %q", want)
		}
	}

	day := strings.ToUpper(supplierErrorStatsDayRollupSQL)
	for _, want := range []string{
		"GRANULARITY = 'HOUR'",
		"DATE_TRUNC('DAY', STAT_TIME)",
		"DATE_TRUNC('DAY', $1::TIMESTAMPTZ)",
		"'DAY'",
		conflictKey,
		"SUM(RETRYABLE_COUNT)::INT",
		"JSONB_EACH",
		"EXCLUDED.STAGE_COUNTS",
	} {
		if !strings.Contains(day, want) {
			t.Errorf("day rollup SQL missing %q", want)
		}
	}

	// E-#6: the UPSERT key stays on the V371 dimensions — retryable/stage are
	// bucket payloads, not key parts (keying on them would multiply rows and
	// change the hour/day rollup grouping semantics).
	if strings.Contains(minute, "RETRYABLE_COUNT,") && strings.Contains(minute, "ON CONFLICT (STAT_TIME, GRANULARITY, SUPPLIER, CREDENTIAL_ID, ERROR_TYPE, MODEL, RETRYABLE_COUNT") {
		t.Error("retryable_count must not join the UPSERT key")
	}
}
