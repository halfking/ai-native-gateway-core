package distlock

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func metricCounterValue(t *testing.T, c interface{ Write(*dto.Metric) error }) float64 {
	t.Helper()
	m := &dto.Metric{}
	if err := c.Write(m); err != nil {
		t.Fatalf("counter.Write: %v", err)
	}
	return m.GetCounter().GetValue()
}

func metricHistogramCount(t *testing.T, observer prometheus.Observer) uint64 {
	t.Helper()
	metric, ok := observer.(interface{ Write(*dto.Metric) error })
	if !ok {
		t.Fatalf("histogram child does not implement Write: %T", observer)
	}
	m := &dto.Metric{}
	if err := metric.Write(m); err != nil {
		t.Fatalf("histogram.Write: %v", err)
	}
	return m.GetHistogram().GetSampleCount()
}

func waitHistogram(t *testing.T, scope string) prometheus.Observer {
	t.Helper()
	metric, err := distlockWaitSeconds.GetMetricWithLabelValues(scope)
	if err != nil {
		t.Fatalf("wait histogram labels: %v", err)
	}
	return metric
}

func TestNormalizeLockScopeBoundsLabels(t *testing.T) {
	for _, tc := range []struct {
		in, want string
	}{
		{"auto", lockScopeAuto},
		{"manual", lockScopeManual},
		{"", lockScopeUnknown},
		{"other", lockScopeUnknown},
	} {
		if got := normalizeLockScope(tc.in); got != tc.want {
			t.Fatalf("normalizeLockScope(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
func TestMetrics_LocalAcquireAndWait(t *testing.T) {
	m := NewLocalManager()
	leaderBefore := metricCounterValue(t, distlockAcquireTotal.WithLabelValues(lockScopeAuto, "leader"))
	followerBefore := metricCounterValue(t, distlockAcquireTotal.WithLabelValues(lockScopeAuto, "follower"))
	waitBefore := metricHistogramCount(t, waitHistogram(t, lockScopeAuto))

	leader, err := m.Acquire(context.Background(), AcquireOpts{Key: "metrics-local", Scope: lockScopeAuto})
	if err != nil {
		t.Fatalf("leader Acquire: %v", err)
	}
	follower, err := m.Acquire(context.Background(), AcquireOpts{Key: "metrics-local", Scope: lockScopeAuto})
	if err != nil {
		t.Fatalf("follower Acquire: %v", err)
	}

	waitDone := make(chan error, 1)
	go func() { waitDone <- follower.Wait(context.Background()) }()
	time.Sleep(10 * time.Millisecond)
	leader.Release(context.Background())
	if err := <-waitDone; err != nil {
		t.Fatalf("follower Wait: %v", err)
	}
	follower.Release(context.Background())

	if got := metricCounterValue(t, distlockAcquireTotal.WithLabelValues(lockScopeAuto, "leader")); got != leaderBefore+1 {
		t.Fatalf("leader acquire delta = %v, want 1", got-leaderBefore)
	}
	if got := metricCounterValue(t, distlockAcquireTotal.WithLabelValues(lockScopeAuto, "follower")); got != followerBefore+1 {
		t.Fatalf("follower acquire delta = %v, want 1", got-followerBefore)
	}
	if got := metricHistogramCount(t, waitHistogram(t, lockScopeAuto)); got != waitBefore+1 {
		t.Fatalf("wait histogram delta = %d, want 1", got-waitBefore)
	}
}

func TestMetrics_RedisAcquireError(t *testing.T) {
	m := NewRedisManager(nil)
	before := metricCounterValue(t, distlockAcquireTotal.WithLabelValues(lockScopeUnknown, "disabled"))
	_, err := m.Acquire(context.Background(), AcquireOpts{Key: "metrics-error"})
	if !errors.Is(err, ErrNotEnabled) {
		t.Fatalf("Acquire: want ErrNotEnabled, got %v", err)
	}
	if got := metricCounterValue(t, distlockAcquireTotal.WithLabelValues(lockScopeUnknown, "disabled")); got != before+1 {
		t.Fatalf("error acquire delta = %v, want 1", got-before)
	}
}

func TestMetrics_LeaderWaitNoop(t *testing.T) {
	m := NewLocalManager()
	leader, err := m.Acquire(context.Background(), AcquireOpts{Key: "metrics-leader-wait", Scope: lockScopeManual})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer leader.Release(context.Background())
	before := metricHistogramCount(t, waitHistogram(t, lockScopeManual))
	if err := leader.Wait(context.Background()); err != nil {
		t.Fatalf("leader Wait: %v", err)
	}
	if got := metricHistogramCount(t, waitHistogram(t, lockScopeManual)); got != before {
		t.Fatalf("leader Wait changed histogram by %d", got-before)
	}
}
