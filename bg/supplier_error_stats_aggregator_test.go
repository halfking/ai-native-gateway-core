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
		t.Fatal("40-minute gap must not clamp (under 8h max window)")
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

	// Aggregator stalled for 10h: rows older than 8h may already be
	// promoted out of hot, so the catch-up window is clamped to 8h and
	// flagged so rollup() can warn.
	afterStall := base.Add(10 * time.Hour)
	from, _, clamped := a.rollupWindow(afterStall)
	if !clamped {
		t.Fatal("10h stale watermark must clamp")
	}
	if want := 8 * time.Hour; afterStall.Sub(from) != want {
		t.Fatalf("clamped window width = %v, want %v", afterStall.Sub(from), want)
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
	} {
		if !strings.Contains(minute, want) {
			t.Errorf("minute rollup SQL missing %q", want)
		}
	}

	hour := strings.ToUpper(supplierErrorStatsHourRollupSQL)
	for _, want := range []string{
		"GRANULARITY = 'MINUTE'",
		"DATE_TRUNC('HOUR', STAT_TIME)",
		"DATE_TRUNC('HOUR', $1::TIMESTAMPTZ)",
		"'HOUR'",
		conflictKey,
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
	} {
		if !strings.Contains(day, want) {
			t.Errorf("day rollup SQL missing %q", want)
		}
	}
}
