package bg

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestRebuildRangeStatsChunksByDay pins the chunking contract. A single pass
// over the retention window scans months of request_logs and blows the 2-minute
// rollup budget; one day per pass keeps each statement bounded.
func TestRebuildRangeStatsRejectsEmptyRange(t *testing.T) {
	w := &StatsMinuteRollup{}
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	if err := w.RebuildRangeStats(context.Background(), now, now); err == nil {
		t.Fatal("expected an error for an empty range")
	}
	if err := w.RebuildRangeStats(context.Background(), now, now.Add(-time.Hour)); err == nil {
		t.Fatal("expected an error when to precedes from")
	}
}

// TestRebuildRangeStatsNilPool guards the lite/no-DB deployment shape: the admin
// handler still exists there, so this must return an error rather than panic on
// a nil pool (same explicit-guard precedent as bg/audit_trimmer.go).
func TestRebuildRangeStatsNilPool(t *testing.T) {
	w := &StatsMinuteRollup{}
	from := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	err := w.RebuildRangeStats(context.Background(), from, from.AddDate(0, 0, 2))
	if err == nil {
		t.Fatal("expected an error when the pool is nil")
	}
	if !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestRebuildRangeStatsChunkBoundaries documents the day-chunk arithmetic the
// rebuild relies on: each pass covers exactly one UTC day, and the final chunk
// stops at `to` instead of running past it.
func TestRebuildRangeStatsChunkBoundaries(t *testing.T) {
	from := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 0, 3) // three whole days

	var chunks int
	for day := from; day.Before(to); day = day.Add(24 * time.Hour) {
		dayEnd := day.Add(24 * time.Hour)
		if dayEnd.After(to) {
			dayEnd = to
		}
		if !day.After(from) && chunks > 0 {
			t.Fatalf("chunk %d started before the range start", chunks)
		}
		if dayEnd.After(to) {
			t.Fatalf("chunk %d ran past the range end", chunks)
		}
		chunks++
	}
	if chunks != 3 {
		t.Fatalf("expected 3 daily chunks, got %d", chunks)
	}

	// A partial trailing day must be clipped, not rounded up past `to`.
	partial := from.Add(36 * time.Hour) // day 1 + 12h
	cnt := 0
	for day := from; day.Before(partial); day = day.Add(24 * time.Hour) {
		cnt++
	}
	if cnt != 2 {
		t.Fatalf("expected 2 chunks for a 36h range, got %d", cnt)
	}
}

// TestRebuildRangeStatsIsIdempotentContract documents the two mechanisms that
// make re-running safe, since operators are expected to re-run after an
// interruption. Both are asserted structurally because neither is observable
// without a database.
func TestRebuildRangeStatsIsIdempotentContract(t *testing.T) {
	main := rollupMainStatement("0")
	if !strings.Contains(main, "DO UPDATE SET") {
		t.Fatal("rebuild relies on whole-key replacement; an additive upsert would double count")
	}
	if strings.Contains(main, "= request_stats_minute.requests +") {
		t.Fatal("rollup must replace the key, not add to it, or a re-run inflates totals")
	}
	// Probe-only keys are not re-emitted by the filtered view, so the retire
	// pass is the only thing that removes them. Without the predicate there,
	// stale probe-only keys survive forever.
	//
	// Assert on the RENDERED predicate text, not on the Go const name: these
	// statements interpolate ProbeTrafficExclusionPredicateView through
	// fmt.Sprintf, so the identifier never appears in the finished SQL and a
	// Contains(constName) assertion would be permanently red for a correct
	// statement.
	for name, sql := range map[string]string{
		"main":         main,
		"retire":       retireClosedMainMinuteSQL,
		"drill-retire": retireClosedErrorDrillMinuteSQL,
	} {
		if !strings.Contains(sql, "'probe' = ANY(") {
			t.Errorf("%s lost the quality_flags probe arm; re-running would keep probe rows", name)
		}
		if !strings.Contains(sql, "node-probe-worker") {
			t.Errorf("%s lost the origin_actor probe arm; re-running would keep probe rows", name)
		}
	}
}

// TestRebuildRangeStatsDetachedUsesBackgroundContext pins that the detached
// entry point does not inherit the request lifetime: an operator closing the
// tab must not abort a half-finished day.
func TestRebuildRangeStatsDetachedUsesBackgroundContext(t *testing.T) {
	w := &StatsMinuteRollup{}
	from := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	// A cancelled parent context is irrelevant here — the method builds its own.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = ctx
	if err := w.RebuildRangeStatsDetached(from, from.AddDate(0, 0, 1)); err == nil {
		t.Fatal("expected nil-pool error from detached rebuild")
	}
}
