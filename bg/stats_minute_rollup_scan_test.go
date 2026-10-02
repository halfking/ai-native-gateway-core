package bg

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestRollupScanSinceTruncatesToUTCMinute(t *testing.T) {
	in := time.Date(2026, 9, 30, 10, 27, 40, 123, time.FixedZone("CST", 8*3600))
	got := rollupScanSince(in)
	want := time.Date(2026, 9, 30, 2, 27, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("rollupScanSince(%s) = %s, want %s", in, got, want)
	}
}

func TestRollupScanSinceKeepsExactMinute(t *testing.T) {
	in := time.Date(2026, 9, 30, 2, 27, 0, 0, time.UTC)
	if got := rollupScanSince(in); !got.Equal(in) {
		t.Fatalf("rollupScanSince(%s) = %s, want the same instant", in, got)
	}
}

func TestRollupScanSinceZero(t *testing.T) {
	if !rollupScanSince(time.Time{}).IsZero() {
		t.Fatal("zero cursor must stay zero")
	}
}

func TestRollupPredicatesIncludeMinuteStart(t *testing.T) {
	raw, err := os.ReadFile("stats_minute_rollup.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	if strings.Contains(src, "r.ts > $1") {
		t.Fatal("exclusive lower bound drops the row on the minute boundary and, with upsert replace, keeps only the tail")
	}
	if strings.Count(src, "r.ts >= $1 AND r.ts <= $2") != 3 {
		t.Fatal("main, dim, and error drill must all scan from the inclusive minute start")
	}
}
