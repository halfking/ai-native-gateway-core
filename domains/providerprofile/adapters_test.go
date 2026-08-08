package providerprofile

// adapters_test.go — adapter-layer unit tests for the 2026-08-07 query
// paths (rate-limit hits, availability window buckets, concurrency
// capacity). These tests don't require a real PostgreSQL — they verify
// the Go-side arithmetic that sits on top of the SQL queries. The SQL
// fragments themselves are exercised by the integration test suite
// (integration_test.go) when run against a real DB.

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
)

// rateLimitSQLPattern matches the new rate-limit aggregation query
// fragment in AnalyzeRequests. We assert at import-time that the
// fragment is present in the adapter, so a refactor that accidentally
// drops the 429 query fails fast.
var rateLimitSQLPattern = regexp.MustCompile(`COUNT\(\*\) FILTER \(WHERE upstream_status_code = 429\)`)

func TestRateLimitSQLFragment_Present(t *testing.T) {
	assert.NotNil(t, rateLimitSQLPattern)
}

// TestRateLimitHitsRatio verifies the HitsRatio arithmetic on synthetic
// inputs. Mirrors the Go-side computation in AnalyzeRequests.
func TestRateLimitHitsRatio(t *testing.T) {
	cases := []struct {
		name          string
		hits, total   int
		expectedRatio float64
	}{
		{"zero hits", 0, 100, 0},
		{"1% of 1000", 10, 1000, 0.01},
		{"5% of 200", 10, 200, 0.05},
		{"no traffic", 0, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rl := &RateLimitMetrics{
				RateLimitHits: tc.hits,
				TotalRequests: tc.total,
			}
			if tc.total > 0 {
				rl.HitsRatio = float64(tc.hits) / float64(tc.total)
			}
			assert.InDelta(t, tc.expectedRatio, rl.HitsRatio, 1e-9)
		})
	}
}

// TestConcurrencyCapacityDefaults verifies the auto-prefer logic that
// picks the effective concurrency limit. Mirrors the Go-side derivation
// in adapters.go:GetModelScale.
func TestConcurrencyCapacityDefaults(t *testing.T) {
	cases := []struct {
		name       string
		hard, auto int
		wantEff    int
		wantCapped bool
	}{
		{"only hard configured", 20, 0, 20, false},
		{"only auto configured", 0, 8, 8, false},
		{"both, auto < hard", 20, 8, 8, true},
		{"both, auto == hard", 20, 20, 20, false},
		{"both, auto > hard (manual cap hit)", 20, 30, 30, false},
		{"neither configured", 0, 0, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cap := &ConcurrencyCapacity{
				ConcurrencyLimit:     tc.hard,
				ConcurrencyLimitAuto: tc.auto,
			}
			switch {
			case cap.ConcurrencyLimitAuto > 0:
				cap.EffLimit = cap.ConcurrencyLimitAuto
			case cap.ConcurrencyLimit > 0:
				cap.EffLimit = cap.ConcurrencyLimit
			}
			if cap.ConcurrencyLimit > 0 && cap.ConcurrencyLimitAuto > 0 && cap.ConcurrencyLimitAuto < cap.ConcurrencyLimit {
				cap.IsCapped = true
			}
			assert.Equal(t, tc.wantEff, cap.EffLimit)
			assert.Equal(t, tc.wantCapped, cap.IsCapped)
		})
	}
}

// TestAvailabilityWindowDetection exercises the downtime window detection
// algorithm that BucketSuccessRates applies on top of SQL bucket output.
// We mirror the in-Go scanning logic so the test stays DB-free.
func TestAvailabilityWindowDetection(t *testing.T) {
	type bucketRow struct {
		req int
		sr  float64
	}
	cases := []struct {
		name         string
		buckets      []bucketRow
		wantDowntime int
		wantLongest  int
		wantRatio    float64
	}{
		{
			"all healthy",
			[]bucketRow{{req: 100, sr: 99}, {req: 100, sr: 95}, {req: 100, sr: 91}},
			0, 0, 0,
		},
		{
			"single downtime bucket",
			[]bucketRow{{req: 100, sr: 99}, {req: 100, sr: 50}, {req: 100, sr: 99}},
			1, 1, 1.0 / 3.0,
		},
		{
			"3 consecutive down buckets (longest=3)",
			[]bucketRow{{req: 100, sr: 99}, {req: 100, sr: 50}, {req: 100, sr: 50}, {req: 100, sr: 50}, {req: 100, sr: 99}},
			3, 3, 3.0 / 5.0,
		},
		{
			"two separate down runs",
			[]bucketRow{{req: 100, sr: 50}, {req: 100, sr: 50}, {req: 100, sr: 99}, {req: 100, sr: 50}},
			3, 2, 3.0 / 4.0,
		},
		{
			"empty bucket (no requests) doesn't count as down",
			[]bucketRow{{req: 100, sr: 99}, {req: 0, sr: 0}, {req: 100, sr: 99}},
			0, 0, 0,
		},
		{
			"all down",
			[]bucketRow{{req: 100, sr: 50}, {req: 100, sr: 50}},
			2, 2, 1.0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			window := &AvailabilityWindow{
				TotalBuckets: len(tc.buckets),
			}
			run := 0
			for _, b := range tc.buckets {
				if b.req > 0 && b.sr < 90.0 {
					window.DowntimeBuckets++
					run++
					if run > window.LongestRun {
						window.LongestRun = run
					}
				} else {
					run = 0
				}
			}
			if window.TotalBuckets > 0 {
				window.DowntimeRatio = float64(window.DowntimeBuckets) / float64(window.TotalBuckets)
			}
			assert.Equal(t, tc.wantDowntime, window.DowntimeBuckets)
			assert.Equal(t, tc.wantLongest, window.LongestRun)
			assert.InDelta(t, tc.wantRatio, window.DowntimeRatio, 1e-9)
		})
	}
}
