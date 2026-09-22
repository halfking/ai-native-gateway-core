package credentialhealth

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/redis/go-redis/v9"
)

// fakeProber is a configurable CredentialProber used by the
// benchmarks. Returns Success=true unconditionally so we exercise
// only the dominant-kind threshold resolution path.
type fakeProber struct{}

func (fakeProber) ProbeCredential(_ context.Context, _ int, _ string) ProbeResult {
	return ProbeResult{Success: true, Latency: 10 * time.Millisecond}
}

// BenchmarkChecker_DominantKindResolution exercises the
// 2026-08-23 hzx-2 audit path that picks the per-kind threshold.
// It runs the threshold-resolution branch (the hot inner loop in
// CheckAndUpdate) without the DB / probe side effects.
func BenchmarkChecker_DominantKindResolution(b *testing.B) {
	mr, err := miniredis.Run()
	if err != nil {
		b.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()
	redisClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer redisClient.Close()
	recorder := NewRecorder(redisClient, 1*time.Hour, 100)
	mockDB, err := pgxmock.NewPool()
	if err != nil {
		b.Fatalf("pgxmock: %v", err)
	}
	defer mockDB.Close()

	cfg := DefaultCheckerConfig()
	cfg.Prober = fakeProber{}
	c := NewChecker(recorder, mockDB, cfg)
	mr.Close() // intentionally break redis so GetRecent short-circuits to empty entries

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = c.CheckAndUpdate(context.Background(), 1, "m")
	}
}

// BenchmarkChecker_ResolveKindThreshold pins just the threshold-
// resolution helper. This is the function called inside the hot
// path; it's pure CPU so the benchmark reflects exactly what the
// production hzx-2 audit added.
func BenchmarkChecker_ResolveKindThreshold(b *testing.B) {
	mr, err := miniredis.Run()
	if err != nil {
		b.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()
	redisClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer redisClient.Close()
	recorder := NewRecorder(redisClient, 1*time.Hour, 100)
	mockDB, err := pgxmock.NewPool()
	if err != nil {
		b.Fatalf("pgxmock: %v", err)
	}
	defer mockDB.Close()

	cfg := DefaultCheckerConfig()
	c := NewChecker(recorder, mockDB, cfg)
	// Seed errorKinds in the proportion typical of a healthy
	// credential that occasionally trips rate_limit.
	kinds := map[string]int{
		"rate_limit":         12,
		"upstream_overloaded": 4,
		"upstream_down":      2,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = c.resolveKindThreshold(kinds)
	}
}

// BenchmarkChecker_DefaultKindThresholds pins the cost of
// instantiating the default per-kind map. Not on the hot path,
// but measured because DefaultCheckerConfig is called every time
// a new checker is constructed.
func BenchmarkChecker_DefaultKindThresholds(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = defaultKindThresholds()
	}
}

// BenchmarkChecker_ColdProbeSuccess exercises the cold-node
// active-probe fast path: no recent samples → ColdProber.ProbeCredential
// → success → return nil. This is the path the 2026-08-23 audit
// added; production triggers it on the first request that lands
// on a never-used credential.
func BenchmarkChecker_ColdProbeSuccess(b *testing.B) {
	mr, err := miniredis.Run()
	if err != nil {
		b.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()
	redisClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer redisClient.Close()
	recorder := NewRecorder(redisClient, 1*time.Hour, 100)
	mockDB, err := pgxmock.NewPool()
	if err != nil {
		b.Fatalf("pgxmock: %v", err)
	}
	defer mockDB.Close()
	coldProber := &fakeColdProber{
		results: map[string]ProbeResult{
			"m": {Success: true, Latency: 10 * time.Millisecond},
		},
	}
	cfg := DefaultCheckerConfig()
	cfg.ColdProber = coldProber
	c := NewChecker(recorder, mockDB, cfg)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = c.CheckAndUpdate(context.Background(), 1, "m")
	}
}