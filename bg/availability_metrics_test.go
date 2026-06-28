package bg

import (
	"context"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/redis/go-redis/v9"
)

// counterValue fetches the float value of a single CounterVec sample.
// Note: a CounterVec returns 0 for a label combination that has never
// been observed. Pre-touch with .Add(0) so the series exists.
func counterValue(t *testing.T, c *prometheus.CounterVec, labelValues ...string) float64 {
	t.Helper()
	m := &dto.Metric{}
	if err := c.WithLabelValues(labelValues...).Write(m); err != nil {
		t.Fatalf("counter write: %v", err)
	}
	return m.Counter.GetValue()
}

func TestAvailabilityMetricsCacheWriteCounter(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cache := NewModelAvailabilityCache(client, time.Hour)

	ctx := context.Background()
	availabilityCacheWrites.WithLabelValues("model_probe", "healthy_confirmed").Add(0)
	before := counterValue(t, availabilityCacheWrites, "model_probe", "healthy_confirmed")

	if err := cache.Set(ctx, 1, "minimax-m3", ModelAvailabilityFields(
		1, "minimax-m3", "healthy_confirmed", true, "ok", 3, 0,
		nil, "model_probe",
	)); err != nil {
		t.Fatalf("cache.Set: %v", err)
	}

	after := counterValue(t, availabilityCacheWrites, "model_probe", "healthy_confirmed")
	if after-before != 1 {
		t.Fatalf("counter delta = %v, want 1", after-before)
	}
}

func TestAvailabilityMetricsCacheReadCounter(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	reader := NewModelAvailabilityReader(client)

	ctx := context.Background()
	availabilityCacheReads.WithLabelValues("availability_reader", "hit").Add(0)
	availabilityCacheReads.WithLabelValues("availability_reader", "miss").Add(0)
	hitsBefore := counterValue(t, availabilityCacheReads, "availability_reader", "hit")
	missesBefore := counterValue(t, availabilityCacheReads, "availability_reader", "miss")

	// Miss path
	if _, err := reader.Read(ctx, 1, "missing-model"); err != nil {
		t.Fatalf("reader.Read miss: %v", err)
	}
	// Hit path
	if err := client.HSet(ctx, "llmgw:avail:1:present-model", map[string]any{
		"state": "healthy_confirmed",
	}).Err(); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := reader.Read(ctx, 1, "present-model"); err != nil {
		t.Fatalf("reader.Read hit: %v", err)
	}

	hitsAfter := counterValue(t, availabilityCacheReads, "availability_reader", "hit")
	missesAfter := counterValue(t, availabilityCacheReads, "availability_reader", "miss")
	if hitsAfter-hitsBefore != 1 {
		t.Fatalf("hits delta = %v, want 1", hitsAfter-hitsBefore)
	}
	if missesAfter-missesBefore != 1 {
		t.Fatalf("misses delta = %v, want 1", missesAfter-missesBefore)
	}
}

func TestAvailabilityMetricsBackfillCounters(t *testing.T) {
	availabilityBackfillRuns.WithLabelValues("manual").Add(0)
	availabilityBackfillRows.WithLabelValues("manual").Add(0)
	runsBefore := counterValue(t, availabilityBackfillRuns, "manual")
	rowsBefore := counterValue(t, availabilityBackfillRows, "manual")

	w := &AvailabilityCacheBackfill{
		cache:     NewModelAvailabilityCache(nil, 0),
		reader:    NewModelAvailabilityReader(nil),
		batchSize: 200,
		lookback:  time.Hour,
		interval:  time.Minute,
		done:      make(chan struct{}),
	}
	if _, err := w.RunOnceWithTrigger(context.Background(), "manual"); err != nil {
		t.Fatalf("RunOnceWithTrigger: %v", err)
	}

	runsAfter := counterValue(t, availabilityBackfillRuns, "manual")
	rowsAfter := counterValue(t, availabilityBackfillRows, "manual")

	if runsAfter-runsBefore != 1 {
		t.Fatalf("runs delta = %v, want 1", runsAfter-runsBefore)
	}
	if rowsAfter != rowsBefore {
		t.Fatalf("rows delta = %v, want 0 (cache disabled)", rowsAfter-rowsBefore)
	}
}
