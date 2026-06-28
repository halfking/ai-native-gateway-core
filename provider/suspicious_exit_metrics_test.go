package provider

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/redis/go-redis/v9"
)

// suspiciousExitValue reads the current value of the suspicious-exit
// counter for a given outcome label. Pre-touching the series with
// .Add(0) keeps label sets stable across runs.
func suspiciousExitValue(t *testing.T, outcome string) float64 {
	t.Helper()
	if suspiciousExits == nil {
		t.Fatalf("suspiciousExits counter not initialised")
	}
	suspiciousExits.WithLabelValues(outcome).Add(0)
	m := &dto.Metric{}
	if err := suspiciousExits.WithLabelValues(outcome).Write(m); err != nil {
		t.Fatalf("counter write: %v", err)
	}
	return m.Counter.GetValue()
}

func TestSuspiciousExitMetric_DispatchedOnHit(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ctx := context.Background()
	if err := client.HSet(ctx, "llmgw:avail:11:glm-5.2", map[string]any{
		"state": "suspicious",
	}).Err(); err != nil {
		t.Fatalf("seed redis: %v", err)
	}

	c := NewClient()
	c.redis = client
	// Use a no-op async hook so we don't touch a DB pool in this test.
	called := int32(0)
	c.asyncExitSuspicious = func(credentialID int, rawModel string) {
		atomic.StoreInt32(&called, 1)
	}

	dispatchedBefore := suspiciousExitValue(t, "dispatched")
	noopBefore := suspiciousExitValue(t, "noop")

	start := time.Now()
	c.maybeExitSuspicious(11, "glm-5.2")
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("maybeExitSuspicious blocked hot path")
	}
	if atomic.LoadInt32(&called) != 1 {
		t.Fatal("expected asyncExitSuspicious to be dispatched")
	}
	if got := suspiciousExitValue(t, "dispatched") - dispatchedBefore; got != 1 {
		t.Fatalf("dispatched delta = %v, want 1", got)
	}
	if got := suspiciousExitValue(t, "noop") - noopBefore; got != 0 {
		t.Fatalf("noop delta = %v, want 0 (entry was suspicious)", got)
	}
}

func TestSuspiciousExitMetric_NoopOnHealthy(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ctx := context.Background()
	if err := client.HSet(ctx, "llmgw:avail:12:minimax-m3", map[string]any{
		"state": "healthy_confirmed",
	}).Err(); err != nil {
		t.Fatalf("seed redis: %v", err)
	}

	c := NewClient()
	c.redis = client
	c.asyncExitSuspicious = func(int, string) {
		t.Fatal("asyncExitSuspicious should NOT be called when state is healthy")
	}

	noopBefore := suspiciousExitValue(t, "noop")
	c.maybeExitSuspicious(12, "minimax-m3")
	if got := suspiciousExitValue(t, "noop") - noopBefore; got != 1 {
		t.Fatalf("noop delta = %v, want 1 (state was healthy)", got)
	}
}

func TestSuspiciousExitMetric_NoopOnMiss(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	c := NewClient()
	c.redis = client
	c.asyncExitSuspicious = func(int, string) {
		t.Fatal("asyncExitSuspicious should NOT be called when redis key is missing")
	}

	noopBefore := suspiciousExitValue(t, "noop")
	c.maybeExitSuspicious(13, "missing-model")
	if got := suspiciousExitValue(t, "noop") - noopBefore; got != 1 {
		t.Fatalf("noop delta = %v, want 1 (redis key missing)", got)
	}
}

func TestSuspiciousExitMetric_NoopWithoutRedisClient(t *testing.T) {
	// When redis is nil we should record nothing — the metric increments
	// only happen after we confirm the cache layer is present.
	noopBefore := suspiciousExitValue(t, "noop")
	c := NewClient()
	c.maybeExitSuspicious(14, "minimax-m3")
	if got := suspiciousExitValue(t, "noop") - noopBefore; got != 0 {
		t.Fatalf("noop delta = %v, want 0 (redis disabled)", got)
	}
}

// kept alive so the import set stays stable when we tweak test cases.
var _ = prometheus.NewCounterVec

func TestSuspiciousExitDBDurationHistogramObservesSamples(t *testing.T) {
	if suspiciousExitDBDuration == nil {
		t.Fatal("histogram not initialised")
	}

	// Capture pre-test sample count via the cumulative bucket counter for
	// the largest bucket (always >= any observation).
	m := &dto.Metric{}
	if err := suspiciousExitDBDuration.Write(m); err != nil {
		t.Fatalf("histogram write: %v", err)
	}
	preCount := m.Histogram.GetSampleCount()

	// Feed three samples across the bucket layout.
	for _, d := range []float64{0.001, 0.04, 0.6} {
		recordSuspiciousExitDBDuration(d)
	}

	after := &dto.Metric{}
	if err := suspiciousExitDBDuration.Write(after); err != nil {
		t.Fatalf("histogram write: %v", err)
	}
	if got := after.Histogram.GetSampleCount() - preCount; got != 3 {
		t.Fatalf("sample count delta = %v, want 3", got)
	}

	// At least one bucket should have non-zero cumulative count.
	buckets := after.Histogram.GetBucket()
	if len(buckets) == 0 {
		t.Fatal("histogram has no buckets")
	}
	hit := false
	for _, b := range buckets {
		if b.GetCumulativeCount() > 0 {
			hit = true
			break
		}
	}
	if !hit {
		t.Fatal("no bucket observed any samples")
	}
}
