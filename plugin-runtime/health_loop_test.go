package pluginruntime

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

func TestHealthLoop_MarksDegradedAfterFailures(t *testing.T) {
	reg := NewRegistry()
	reg.SetPlugin(&PluginState{PluginID: "p1", Status: "ready"})

	var failCount uint32
	check := func(pluginID string) error {
		atomic.AddUint32(&failCount, 1)
		return fmt.Errorf("simulated health failure")
	}

	loop := NewHealthLoop(reg, check, HealthLoopConfig{
		Interval:         20 * time.Millisecond,
		FailureThreshold: 2,
	})
	loop.Start()
	defer loop.Stop()

	// wait for at least 2 failures
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if atomic.LoadUint32(&failCount) >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// give SetPluginStatus a moment
	time.Sleep(50 * time.Millisecond)

	reg.mu.RLock()
	st := reg.plugins["p1"]
	reg.mu.RUnlock()
	if st == nil || st.Status != "degraded" {
		t.Fatalf("plugin should be degraded after %d failures, got %+v", atomic.LoadUint32(&failCount), st)
	}
}

func TestHealthLoop_RecoversWhenHealthy(t *testing.T) {
	reg := NewRegistry()
	reg.SetPlugin(&PluginState{PluginID: "p1", Status: "degraded"})
	check := func(pluginID string) error { return nil } // healthy
	loop := NewHealthLoop(reg, check, HealthLoopConfig{Interval: 20 * time.Millisecond, FailureThreshold: 1})
	loop.Start()
	defer loop.Stop()
	time.Sleep(80 * time.Millisecond)

	reg.mu.RLock()
	st := reg.plugins["p1"]
	reg.mu.RUnlock()
	if st == nil || st.Status != "ready" {
		t.Fatalf("degraded plugin should recover to ready when health check passes, got %+v", st)
	}
}
