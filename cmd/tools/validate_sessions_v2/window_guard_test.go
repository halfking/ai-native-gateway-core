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
