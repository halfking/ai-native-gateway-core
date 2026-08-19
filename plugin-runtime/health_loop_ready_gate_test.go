package pluginruntime

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestHealthLoopDoesNotPromoteUnreadyPlugins(t *testing.T) {
	reg := NewRegistry()
	reg.SetPlugin(&PluginState{PluginID: "p1", Status: "failed"})
	var checks atomic.Int32
	loop := NewHealthLoop(reg, func(string) error {
		checks.Add(1)
		return nil
	}, HealthLoopConfig{Interval: 5 * time.Millisecond, FailureThreshold: 1})
	loop.Start()
	defer loop.Stop()
	time.Sleep(30 * time.Millisecond)
	if got := checks.Load(); got != 0 {
		t.Fatalf("failed plugin should not be health-checked, got %d checks", got)
	}
	if got := loop.currentStatus("p1"); got != "failed" {
		t.Fatalf("failed plugin was promoted to %q", got)
	}
}
