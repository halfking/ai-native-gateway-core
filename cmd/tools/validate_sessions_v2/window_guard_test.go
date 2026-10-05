package main

import (
	"strings"
	"testing"
	"time"
)

// TestWindowExceedsV1Data is the control pair for the §9.222 truncation guard.
//
// Both directions are required. A guard seen only in its "did not fire" state
// is indistinguishable from a guard that cannot fire, and this one gates the
// report that would justify retiring request_logs.
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
		actual        V1TimeRange
		wantTruncated bool
		wantSubstring string
	}{
		{
			name:          "start before oldest row (the 2-month retention trap)",
			start:         time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
			end:           time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC),
			actual:        actual,
			wantTruncated: true,
			wantSubstring: "precedes the oldest v1 row",
		},
		{
			// Start is deliberately inside the data so this case isolates the END
			// arm. With the start outside as well, the start arm fires first and
			// the end arm is never exercised — which is how this case first
			// failed, and the failure was in the fixture, not in the guard.
			name:          "end after newest row, start inside the data",
			start:         time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
			end:           time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC),
			actual:        actual,
			wantTruncated: true,
			wantSubstring: "is after the newest v1 row",
		},
		{
			// Negative control: the window sits inside the data. This must NOT
			// fire, or the tool refuses every legitimate run.
			name:          "window inside the data",
			start:         time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
			end:           time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC),
			actual:        actual,
			wantTruncated: false,
		},
		{
			// Negative control: -end-date alone leaves start zero = unbounded
			// below, which is a legitimate "everything up to X" query.
			name:          "end-date only means unbounded below",
			start:         time.Time{},
			end:           time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC),
			actual:        actual,
			wantTruncated: false,
		},
		{
			// Negative control: an empty tenant is the zero-candidate path's
			// job; calling it truncation would be the wrong sentence.
			name:          "no v1 rows at all is not truncation",
			start:         time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
			end:           time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC),
			actual:        V1TimeRange{},
			wantTruncated: false,
		},
		{
			// Negative control: exact bounds equal to the data edges must pass.
			// An off-by-one here would refuse the one window that is precisely
			// the data, which is the most common operator input.
			name:          "window exactly equal to the data span",
			start:         actual.MinTS,
			end:           actual.MaxTS,
			actual:        actual,
			wantTruncated: false,
		},
		{
			// The 252 shape. The operator names 2026-10-04 and the newest row
			// is 2026-10-04 15:53:32Z, so the named day is present and this
			// must NOT be reported: the newest named day is routinely partial,
			// because "up to today" is the common input.
			//
			// This is a control, not a regression — and it is worth being
			// explicit about why, because it is the case the §9.222 handoff
			// expected the guard to start firing on. It must not. The hours
			// this case loses are lost by the query's half-open bound, and
			// they are fixed in endDayExclusiveBound; from the guard's point of
			// view the named day is simply there.
			name:          "newest named day is partial but present (252 shape)",
			start:         time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
			end:           time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC),
			actual:        actual,
			wantTruncated: false,
		},
		{
			// A whole named day is missing: the operator names 2026-10-04 and
			// the newest row is 2026-10-03 23:00Z. requestedEnd is that day's
			// midnight, so the instant comparison already catches this — this
			// is the guard working as designed, pinned so the end arm cannot
			// be quietly loosened to day-granular comparisons.
			name:  "a whole named day is missing",
			start: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
			end:   time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC),
			actual: V1TimeRange{
				MinTS: time.Date(2026, 9, 30, 18, 54, 28, 0, time.UTC),
				MaxTS: time.Date(2026, 10, 3, 23, 0, 0, 0, time.UTC),
				Rows:  31000,
			},
			wantTruncated: true,
			wantSubstring: "is after the newest v1 row",
		},
		{
			// One microsecond before the named day's midnight is still the
			// previous day, so it must fire. Together with the next case this
			// pins the end arm to the named day's midnight.
			name:  "newest row one microsecond before the named day",
			start: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
			end:   time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC),
			actual: V1TimeRange{
				MinTS: time.Date(2026, 9, 30, 18, 54, 28, 0, time.UTC),
				MaxTS: time.Date(2026, 10, 3, 23, 59, 59, 999999, time.UTC),
				Rows:  31000,
			},
			wantTruncated: true,
			wantSubstring: "is after the newest v1 row",
		},
		{
			// The first instant of the named day is present, so the day is
			// covered.
			name:  "newest row at the first instant of the named day",
			start: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
			end:   time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC),
			actual: V1TimeRange{
				MinTS: time.Date(2026, 9, 30, 18, 54, 28, 0, time.UTC),
				MaxTS: time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC),
				Rows:  31000,
			},
			wantTruncated: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, why := WindowExceedsV1Data(tc.start, tc.end, tc.actual)
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
		})
	}
}

// TestEndDayExclusiveBound pins the conversion the query bound depends on.
//
// This covers the arithmetic. TestQueryBoundsIncludeTheNamedEndDay below is
// what covers the wiring, and it is the one that fails if the bound is
// reverted.
func TestEndDayExclusiveBound(t *testing.T) {
	cases := []struct {
		name  string
		named time.Time
		want  time.Time
	}{
		{
			// The 252 case. The old bound was this same instant, and
			// `ts < 2026-10-04T00:00:00Z` dropped every row of 2026-10-04
			// (newest real row 15:53:32Z) while the report printed
			// 2026-10-04.
			name:  "2026-10-04 must include all of 2026-10-04",
			named: time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC),
			want:  time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC),
		},
		{
			name:  "month rollover",
			named: time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC),
			want:  time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			name:  "year rollover and leap day",
			named: time.Date(2024, 2, 29, 0, 0, 0, 0, time.UTC),
			want:  time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			// The start flag is a plain date too, and it is used with `ts >=`,
			// so its own midnight is already the correct bound. Converting it
			// as well would drop a whole day off the front of the window. This
			// case exists to say so.
			name:  "start day is already a correct >= bound and must not shift",
			named: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
			want:  time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := endDayExclusiveBound(tc.named); !got.Equal(tc.want) {
				t.Fatalf("endDayExclusiveBound(%s) = %s, want %s",
					tc.named.Format(time.RFC3339), got.Format(time.RFC3339), tc.want.Format(time.RFC3339))
			}
		})
	}
}

// TestQueryBoundsIncludeTheNamedEndDay is the load-bearing regression for
// §9.222's follow-up: the value bound to the half-open `ts < $3` must be the
// start of the day AFTER the named end day.
//
// This asserts the actual argument list that LoadSessionsInRange hands to
// SQL, not the text of the source. That distinction matters: an earlier
// version of this test grepped loader.go for the expected call expression, and
// `go test -overlay` does not affect what os.ReadFile sees — so that version
// stayed green against a reverted implementation and would have certified a
// fix that was not wired at all.
//
// The remaining gap, stated plainly: nothing here executes the SQL, because
// this package has no database in its default test run (no TEST_DATABASE_URL).
// The argument list is verified; the planner's use of it is not.
func TestQueryBoundsIncludeTheNamedEndDay(t *testing.T) {
	settleThreshold := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	namedEnd := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	namedStart := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

	query, args := buildSessionRangeQuery("tenant-a", namedStart, namedEnd, settleThreshold, 100)

	if len(args) != 5 {
		t.Fatalf("buildSessionRangeQuery returned %d args, want 5 (the SQL has $1..$5)", len(args))
	}
	if !strings.Contains(query, "ts < $3") {
		t.Fatal("the end bound is no longer half-open; the conversion below assumes `ts < $3`")
	}

	gotEnd, ok := args[2].(time.Time)
	if !ok {
		t.Fatalf("args[2] is %T, want time.Time", args[2])
	}
	wantEnd := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	if !gotEnd.Equal(wantEnd) {
		t.Fatalf("args[2] (the `ts < $3` bound) = %s, want %s — a bound of %s is what silently "+
			"excluded all of 2026-10-04",
			gotEnd.Format(time.RFC3339), wantEnd.Format(time.RFC3339), namedEnd.Format(time.RFC3339))
	}

	// The start bound must stay exactly as named: it is used with `ts >=`, so
	// shifting it would drop a day off the front instead.
	gotStart, ok := args[1].(time.Time)
	if !ok {
		t.Fatalf("args[1] is %T, want time.Time", args[1])
	}
	if !gotStart.Equal(namedStart) {
		t.Fatalf("args[1] (the `ts >= $2` bound) = %s, want the named start day %s",
			gotStart.Format(time.RFC3339), namedStart.Format(time.RFC3339))
	}
}
