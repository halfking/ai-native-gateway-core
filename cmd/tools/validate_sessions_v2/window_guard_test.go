package main

import (
	"strings"
	"testing"
	"time"
)

// TestWindowExceedsV1Data is the control set for the §9.222 + §R44/移交.1
// truncation guard.
//
// Every arm needs both directions. A guard seen only in its "did not fire"
// state is indistinguishable from a guard that cannot fire, and this one gates
// the report that would justify retiring request_logs.
//
// The `probe` field of each case is what HasV1RowsInRange(tenant, end,
// endDayCutoff(end)) returns in that world — the guard consumes it as measured
// input, so the fixtures state it explicitly instead of implying it.
func TestWindowExceedsV1Data(t *testing.T) {
	// Measured read-only on 252 on 2026-10-05, for tenant rows that carry a
	// session header (§9.220/§9.221/§9.222):
	//
	//   request_logs    2026-09-30 18:54:28 .. 2026-10-04 15:53:32   (4 days)
	//   session_turns   2026-09-06 11:03:50 .. 2026-10-04 15:53:33   (24 days earlier)
	//
	// The retention policy allows 2 months, but the data that actually exists
	// spans four days. That gap is the reason this guard measures the range
	// instead of comparing against a constant: any constant baked in would be
	// wrong here, and wrong in the direction that matters — it would let a
	// 24-day window through on a v1 surface that only holds 4 days of it.
	actual := V1TimeRange{
		MinTS: time.Date(2026, 9, 30, 18, 54, 28, 0, time.UTC),
		MaxTS: time.Date(2026, 10, 4, 15, 53, 32, 0, time.UTC),
		Rows:  31496,
	}

	cases := []struct {
		name          string
		start, end    time.Time
		probe         bool // hasRowsInEndDay as the LIMIT-1 probe would return it
		actual        V1TimeRange
		wantTruncated bool
		wantSubstring string
	}{
		{
			name:          "start before oldest row (the 2-month retention trap)",
			start:         time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
			end:           time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC),
			probe:         false, // day 2026-10-05 is empty: MaxTS is 2026-10-04 15:53:32
			actual:        actual,
			wantTruncated: true,
			wantSubstring: "precedes the oldest v1 row",
		},
		{
			// fe5003034's end-side arm, kept (narrowed by windowEndSlack): the
			// window promises full days beyond the newest v1 row. Start is
			// deliberately inside the data so this case isolates the END arm —
			// with the start outside as well, the start arm fires first and the
			// end arm is never exercised. The gap here is 15 days, far beyond
			// the day-granular slack, so the narrowing must not swallow it.
			name:          "end after newest row, start inside the data",
			start:         time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
			end:           time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC),
			probe:         false, // day 2026-10-20 is empty
			actual:        actual,
			wantTruncated: true,
			wantSubstring: "is after the newest v1 row",
		},
		{
			// §R44/移交.1 — the defect itself, with the audit's measured numbers.
			// -end-date 2026-10-04 parses to 2026-10-04T00:00:00Z and the load is
			// half-open (ts < end), so the source's rows up to 15:53:32 that day —
			// 15h53m32s of real data — never reach the report. The old guard
			// compared actual.MaxTS.Before(requestedEnd), the opposite direction,
			// and reported truncated=false. The probe now measures the remainder
			// of the named day and the guard fails closed.
			name:          "end boundary silently cuts off the rest of the end day (R44 移交.1)",
			start:         time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
			end:           time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC),
			probe:         true, // rows exist in [2026-10-04 00:00, 2026-10-05 00:00): through 15:53:32
			actual:        actual,
			wantTruncated: true,
			wantSubstring: "cuts off newer v1 rows",
		},
		{
			// Same trap via -end-date alone: a zero start is legitimately
			// "unbounded below" (never start truncation), but the zero start does
			// not excuse the end boundary — the named day is still dropped
			// silently. The old fixture blessed exactly this shape as legal.
			name:          "end-date only, end day still has rows",
			start:         time.Time{},
			end:           time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC),
			probe:         true,
			actual:        actual,
			wantTruncated: true,
			wantSubstring: "cuts off newer v1 rows",
		},
		{
			// Negative control: an empty tenant is the zero-candidate path's
			// job; calling it truncation would be the wrong sentence.
			name:          "no v1 rows at all is not truncation",
			start:         time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
			end:           time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC),
			probe:         false,
			actual:        V1TimeRange{},
			wantTruncated: false,
		},
		{
			// Former negative control at window_guard_test.go:90, flipped by
			// §R44/移交.1. end == MaxTS still excludes the row AT ts == end from
			// the half-open load — the same "rows the window cannot see" mechanism
			// as the 15h53m32s case, one row instead of a day of them. The old
			// comment called this "the most common operator input", but a
			// date-form flag can never produce a non-midnight end; the fixture
			// blessed a shape the CLI cannot even express while the real common
			// input (a midnight end inside the data) stayed unprotected.
			name:          "window exactly equal to the data span still leaves the boundary row unloaded",
			start:         actual.MinTS,
			end:           actual.MaxTS,
			probe:         true, // the row at ts == end is inside [end, endDayCutoff(end))
			actual:        actual,
			wantTruncated: true,
			wantSubstring: "cuts off newer v1 rows",
		},
		{
			// 用例 B — the legitimate run must stay open: -end-date set to the day
			// AFTER the last data day. The named day is empty (nothing of it can
			// be dropped) and the newest row sits inside the last fully covered
			// day, so the window matches the data. This is also the zero-start
			// case: had the start arm fired on "unbounded below", this would be
			// truncated with a "precedes the oldest v1 row" reason.
			name:          "end day after the last data day is clean (zero start)",
			start:         time.Time{},
			end:           time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC),
			probe:         false, // day 2026-10-05 is empty
			actual:        actual,
			wantTruncated: false,
		},
		{
			// Day-scope negative control, on purpose: rows exist AFTER the end's
			// day (Oct 5–6) but none inside the named day itself. The probe is
			// scoped to [end, endDayCutoff(end)), not the literal [end, +∞):
			// days past the named end are the operator's explicit bound, and a
			// global probe would fire on every legitimate historical window for
			// any tenant with ongoing traffic — leaving the guard with no clean
			// path and therefore no discriminating power at all.
			name:  "rows beyond the end's own day are the operator's explicit bound",
			start: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
			end:   time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC),
			probe: false, // day 2026-10-04 is empty in this world
			actual: V1TimeRange{
				MinTS: time.Date(2026, 9, 30, 18, 54, 28, 0, time.UTC),
				MaxTS: time.Date(2026, 10, 6, 10, 0, 0, 0, time.UTC),
				Rows:  40000,
			},
			wantTruncated: false,
		},
		{
			// Grace boundary, open side: the newest row sits exactly at
			// end-24h — i.e. inside the last day the half-open window fully
			// covers. The window promises whole days up to end-1 and day end-1
			// has data, so this is the live-source afternoon shape and must pass.
			// Start is deliberately inside the data (MinTS is earlier) so the
			// end arm is isolated — with start before MinTS the start arm fires
			// first and this case proves nothing about the grace.
			name:  "newest row inside the last covered day passes (grace boundary)",
			start: time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC),
			end:   time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC),
			probe: false,
			actual: V1TimeRange{
				MinTS: time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC),
				MaxTS: time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC),
				Rows:  12000,
			},
			wantTruncated: false,
		},
		{
			// Grace boundary, firing side: the newest row is before end-24h, so
			// the window promises the full day end-1 (and every day before it
			// back to start) but that day has no data at all. fe5003034's end
			// arm, expressed at the day granularity the flags actually have.
			// Start inside the data, as above, to isolate the end arm.
			name:  "newest row before the last covered day fires (window wider than data)",
			start: time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC),
			end:   time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC),
			probe: false,
			actual: V1TimeRange{
				MinTS: time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC),
				MaxTS: time.Date(2026, 10, 3, 23, 59, 59, 0, time.UTC),
				Rows:  11000,
			},
			wantTruncated: true,
			wantSubstring: "is after the newest v1 row",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, why := WindowExceedsV1Data(tc.start, tc.end, tc.actual, tc.probe)
			if got != tc.wantTruncated {
				t.Fatalf("WindowExceedsV1Data = %v, want %v (why=%q)", got, tc.wantTruncated, why)
			}
			if tc.wantSubstring != "" && !strings.Contains(why, tc.wantSubstring) {
				t.Errorf("reason %q does not mention %q — the operator needs to know which edge is short",
					why, tc.wantSubstring)
			}
			if !tc.wantTruncated && why != "" {
				t.Errorf("no truncation should be reported, but got reason %q", why)
			}
			// The end-day arm's sentence must hand the operator the corrected
			// flag value, not just a diagnosis.
			if tc.wantTruncated && tc.probe && !tc.end.IsZero() && !strings.Contains(why, "re-run with -end-date") {
				t.Errorf("end-day reason %q does not tell the operator what to re-run with", why)
			}
		})
	}
}

// TestEndDayCutoff pins the probe range arithmetic the end-day arm depends on:
// for a midnight end it is the named day itself; for a non-midnight end it is
// the stretch up to the next midnight, which still contains a row sitting
// exactly at `end` — the former :90 fixture's boundary row.
func TestEndDayCutoff(t *testing.T) {
	midnight := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	if got := endDayCutoff(midnight); !got.Equal(time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("endDayCutoff(midnight) = %s, want 2026-10-05T00:00:00Z", got)
	}
	midDay := time.Date(2026, 10, 4, 15, 53, 32, 0, time.UTC)
	if got := endDayCutoff(midDay); !got.Equal(time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("endDayCutoff(mid-day) = %s, want 2026-10-05T00:00:00Z", got)
	}
	// The probe range is [end, cutoff); a row sitting exactly at ts == end is
	// inside it exactly when end < cutoff strictly. That is what keeps the
	// flipped "window exactly equal to the data span" case consistent between
	// the pure fixture and the LIMIT-1 probe on a real database.
	if !midDay.Before(endDayCutoff(midDay)) {
		t.Fatalf("end must be strictly before its own cutoff, got end=%s cutoff=%s",
			midDay, endDayCutoff(midDay))
	}
}
