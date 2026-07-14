package admin

import (
	"testing"
	"time"
)

func TestBoardTimeRangeTrendBucketMinutes(t *testing.T) {
	cases := []struct {
		name string
		tr   boardTimeRange
		want int
	}{
		{"today", boardTimeRange{Days: 1}, 5},
		{"7d", boardTimeRange{Days: 7}, 15},
		{"30d", boardTimeRange{Days: 30}, 60},
		{"custom short", boardTimeRange{
			Custom: true,
			Start:  time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
			End:    time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC),
		}, 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.tr.trendBucketMinutes(); got != tc.want {
				t.Fatalf("trendBucketMinutes() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestSqlTrendBucketUsesFloorInterval(t *testing.T) {
	sql := sqlTrendBucket("bucket", 15)
	if sql == "" {
		t.Fatal("expected non-empty SQL")
	}
	if !containsAll(sql, "FLOOR", "15", "interval") {
		t.Fatalf("unexpected bucket SQL: %s", sql)
	}
}

func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		if !containsFold(s, p) {
			return false
		}
	}
	return true
}

func containsFold(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		(len(s) > 0 && stringIndexFold(s, sub) >= 0))
}

func stringIndexFold(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		match := true
		for j := 0; j < len(sub); j++ {
			a, b := s[i+j], sub[j]
			if a >= 'A' && a <= 'Z' {
				a += 'a' - 'A'
			}
			if b >= 'A' && b <= 'Z' {
				b += 'a' - 'A'
			}
			if a != b {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}
