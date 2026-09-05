package admin

import (
	"testing"
	"time"
)

func TestBucketStateFromRates(t *testing.T) {
	cases := []struct {
		name    string
		ok, n   int
		want    string
	}{
		{"empty", 0, 0, "empty"},
		{"all_ok", 100, 100, "ok"},
		{"border_ok", 95, 100, "ok"},
		{"degraded_high", 94, 100, "degraded"},
		{"degraded_low", 50, 100, "degraded"},
		{"down_border", 49, 100, "down"},
		{"all_fail", 0, 10, "down"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := bucketStateFromCounts(tc.ok, tc.n)
			if got != tc.want {
				t.Fatalf("bucketStateFromCounts(%d,%d)=%q want %q", tc.ok, tc.n, got, tc.want)
			}
		})
	}
}

func TestClassifyModelStatus(t *testing.T) {
	cases := []struct {
		name   string
		reqs   int
		avail  float64
		want   string
	}{
		{"no_data", 0, 0, "no_data"},
		{"interrupted_zero", 12, 0, "interrupted"},
		{"interrupted_near_zero", 100, 0.5, "interrupted"},
		{"healthy_partial", 100, 50, "healthy"},
		{"healthy_full", 100, 99.86, "healthy"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyModelStatus(tc.reqs, tc.avail)
			if got != tc.want {
				t.Fatalf("classifyModelStatus(%d, %.2f)=%q want %q", tc.reqs, tc.avail, got, tc.want)
			}
		})
	}
}

func TestBuildModelStatusSummary(t *testing.T) {
	models := []modelStatusRow{
		{Name: "a", Status: "healthy", AvailabilityPct: 100, RequestCount: 10},
		{Name: "b", Status: "healthy", AvailabilityPct: 80, RequestCount: 5},
		{Name: "c", Status: "interrupted", AvailabilityPct: 0, RequestCount: 3},
		{Name: "d", Status: "no_data", AvailabilityPct: 0, RequestCount: 0},
		{Name: "e", Status: "no_data", AvailabilityPct: 0, RequestCount: 0},
	}
	sum := buildModelStatusSummary(models)
	if sum.TotalModels != 5 {
		t.Fatalf("total=%d want 5", sum.TotalModels)
	}
	if sum.Healthy != 2 || sum.Interrupted != 1 || sum.NoData != 2 {
		t.Fatalf("counts healthy=%d interrupted=%d no_data=%d", sum.Healthy, sum.Interrupted, sum.NoData)
	}
	// avg over models with data only: (100+80+0)/3 = 60
	if sum.AvgAvailabilityPct < 59.9 || sum.AvgAvailabilityPct > 60.1 {
		t.Fatalf("avg_availability_pct=%v want ~60", sum.AvgAvailabilityPct)
	}
}

func TestAssembleHourBuckets_Fills24Hours(t *testing.T) {
	now := time.Date(2026, 8, 24, 10, 30, 0, 0, time.UTC)
	// Only two hours have data: current truncated hour and 2h ago.
	cur := now.Truncate(time.Hour)
	ago2 := cur.Add(-2 * time.Hour)
	points := map[time.Time]hourAgg{
		cur:  {Success: 10, Total: 10},
		ago2: {Success: 1, Total: 10},
	}
	buckets := assembleHourBuckets(now, 24, points)
	if len(buckets) != 24 {
		t.Fatalf("len=%d want 24", len(buckets))
	}
	// Oldest first (left = older), newest last.
	if buckets[0].State != "empty" {
		t.Fatalf("oldest bucket state=%q want empty", buckets[0].State)
	}
	if buckets[23].State != "ok" {
		t.Fatalf("newest bucket state=%q want ok", buckets[23].State)
	}
	if buckets[21].State != "down" { // ago2 is index 21 when newest is 23
		t.Fatalf("ago2 bucket state=%q want down (index 21)", buckets[21].State)
	}
}

func TestMergeModelStatusRows_SortsByRequestCountDesc(t *testing.T) {
	catalog := []string{"idle-model", "hot-model", "mid-model"}
	stats := map[string]modelAgg{
		"hot-model": {Success: 99, Total: 100, LatencySumMs: 5000, LatencyN: 99},
		"mid-model": {Success: 0, Total: 5, LatencySumMs: 0, LatencyN: 0},
		"extra-log": {Success: 10, Total: 10, LatencySumMs: 1000, LatencyN: 10},
	}
	hourPoints := map[string]map[time.Time]hourAgg{}
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	rows := mergeModelStatusRows(now, catalog, stats, hourPoints)
	if len(rows) != 4 {
		t.Fatalf("len=%d want 4 (catalog ∪ log-only)", len(rows))
	}
	if rows[0].Name != "hot-model" || rows[1].Name != "extra-log" || rows[2].Name != "mid-model" {
		t.Fatalf("order=%v,%v,%v want hot, extra, mid", rows[0].Name, rows[1].Name, rows[2].Name)
	}
	if rows[0].Status != "healthy" || rows[2].Status != "interrupted" || rows[3].Status != "no_data" {
		t.Fatalf("statuses=%q,%q,%q,%q", rows[0].Status, rows[1].Status, rows[2].Status, rows[3].Status)
	}
	if rows[0].AvailabilityPct < 98.9 || rows[0].AvailabilityPct > 99.1 {
		t.Fatalf("hot availability=%v want ~99", rows[0].AvailabilityPct)
	}
}
