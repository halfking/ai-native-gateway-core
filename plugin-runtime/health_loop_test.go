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

func TestHealthLoop_RestartsDegradedPlugin(t *testing.T) {
	reg := NewRegistry()
	reg.SetPlugin(&PluginState{PluginID: "p1", Status: "ready"})

	check := func(pluginID string) error { return fmt.Errorf("down") }
	var restartCalls uint32
	restarter := func(pluginID string) error {
		atomic.AddUint32(&restartCalls, 1)
		return nil
	}

	loop := NewHealthLoop(reg, check, HealthLoopConfig{
		Interval:         10 * time.Millisecond,
		FailureThreshold: 1,
		Restarter:        restarter,
		MaxRestarts:      3,
		BackoffStart:     1 * time.Millisecond,
		BackoffMax:       5 * time.Millisecond,
	})
	loop.Start()
	defer loop.Stop()

	time.Sleep(200 * time.Millisecond)
	if atomic.LoadUint32(&restartCalls) == 0 {
		t.Fatal("Restarter should have been called for degraded plugin")
	}
}

func TestHealthLoop_MaxRestartsMarksFailed(t *testing.T) {
	reg := NewRegistry()
	reg.SetPlugin(&PluginState{PluginID: "p1", Status: "ready"})

	check := func(pluginID string) error { return fmt.Errorf("down") }
	var restartCalls uint32
	restarter := func(pluginID string) error {
		atomic.AddUint32(&restartCalls, 1)
		return nil
	}

	loop := NewHealthLoop(reg, check, HealthLoopConfig{
		Interval:         5 * time.Millisecond,
		FailureThreshold: 1,
		Restarter:        restarter,
		MaxRestarts:      2,
		BackoffStart:     1 * time.Millisecond,
		BackoffMax:       2 * time.Millisecond,
	})
	loop.Start()
	defer loop.Stop()

	// statusOf reads Status under the registry lock; otherwise the loop's
	// in-place SetPluginStatus write races with our read.
	statusOf := func() string {
		reg.mu.RLock()
		defer reg.mu.RUnlock()
		if st := reg.plugins["p1"]; st != nil {
			return st.Status
		}
		return ""
	}
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if statusOf() == "failed" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := statusOf(); got != "failed" {
		t.Fatalf("plugin should be failed after MaxRestarts, got %q", got)
	}
}

// D-S1（2026-11-14 审计轮）：管理性停用的插件必须跳过健康检查——否则空
// socket 连续失败触发 Restarter，把手动 deactivate 的插件复活。变异臂：
// 摘掉 ShouldCheck 门 ⇒ check 被调用 + Restarter 拉起 ⇒ 本测试红。
func TestHealthLoop_ShouldCheckGatesDisabledPlugin(t *testing.T) {
	reg := NewRegistry()
	reg.SetPlugin(&PluginState{PluginID: "stopped", Status: "ready"})
	reg.SetPlugin(&PluginState{PluginID: "live", Status: "ready"})

	var stoppedChecked, liveChecked, liveRestarts uint32
	check := func(id string) error {
		if id == "stopped" {
			atomic.AddUint32(&stoppedChecked, 1)
		} else {
			atomic.AddUint32(&liveChecked, 1)
		}
		return fmt.Errorf("socket gone") // both would fail if checked
	}
	loop := NewHealthLoop(reg, check, HealthLoopConfig{
		Interval:         20 * time.Millisecond,
		FailureThreshold: 1,
		Restarter: func(id string) error {
			if id == "live" {
				atomic.AddUint32(&liveRestarts, 1)
			}
			return nil
		},
		ShouldCheck: func(id string) bool { return id != "stopped" }, // admin-deactivated
	})
	loop.Start()
	defer loop.Stop()

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if atomic.LoadUint32(&liveRestarts) >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)

	if n := atomic.LoadUint32(&stoppedChecked); n != 0 {
		t.Fatalf("admin-deactivated plugin must not be health-checked, checked %d times", n)
	}
	if atomic.LoadUint32(&liveRestarts) == 0 {
		t.Fatal("live plugin should still be checked/restarted (guard must not swallow everything)")
	}
}
