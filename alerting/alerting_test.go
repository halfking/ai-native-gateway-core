package alerting

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestNewManager(t *testing.T) {
	manager := NewManager()
	if manager == nil {
		t.Fatal("Manager should not be nil")
	}
	t.Log("NewManager test passed")
}

func TestAddRule(t *testing.T) {
	manager := NewManager()
	rule := &Rule{
		Name:        "test_rule",
		Description: "Test rule",
		Condition:   func() bool { return false },
		Severity:    SeverityInfo,
		Interval:    1 * time.Second,
	}
	manager.AddRule(rule)

	if len(manager.rules) != 1 {
		t.Errorf("Expected 1 rule, got %d", len(manager.rules))
	}
	t.Log("AddRule test passed")
}

// TestRuleFiring 验证规则触发逻辑
//
// 2026-08-08 audit fix: 使用 atomic.Bool 替代裸 bool 避免数据竞争。
// 旧实现中 `triggered` 变量在主 goroutine 和 Manager.runRule goroutine
// 间共享读写，go test -race 会报 "WARNING: DATA RACE"。
func TestRuleFiring(t *testing.T) {
	manager := NewManager()
	var triggered atomic.Bool

	rule := &Rule{
		Name:        "test_rule",
		Description: "Test rule",
		Condition:   func() bool { return triggered.Load() },
		Severity:    SeverityWarning,
		Interval:    100 * time.Millisecond,
	}

	manager.AddRule(rule)
	manager.AddNotifier(NewConsoleNotifier())
	manager.Start()

	time.Sleep(150 * time.Millisecond)

	alerts := manager.GetFiringAlerts()
	if len(alerts) != 0 {
		t.Errorf("Expected 0 alerts, got %d", len(alerts))
	}

	triggered.Store(true)
	time.Sleep(150 * time.Millisecond)

	alerts = manager.GetFiringAlerts()
	if len(alerts) != 1 {
		t.Errorf("Expected 1 alert, got %d", len(alerts))
	}

	manager.Stop()
	t.Log("RuleFiring test passed")
}

// TestHighErrorRateRule 验证高错误率规则的触发条件
//
// 2026-08-08 audit fix: 使用 atomic.Int64 把 errorRate 编码为 fixed-point
// 整数（精度 1e-4），避免浮点数据竞争风险。
func TestHighErrorRateRule(t *testing.T) {
	var errorRateBits atomic.Int64
	errorRateBits.Store(int64(0.05 * 1e4))
	rule := HighErrorRateRule(
		func() float64 { return float64(errorRateBits.Load()) / 1e4 },
		0.10,
	)

	if rule.Condition() {
		t.Error("Rule should not fire")
	}

	errorRateBits.Store(int64(0.15 * 1e4))
	if !rule.Condition() {
		t.Error("Rule should fire")
	}

	t.Log("HighErrorRateRule test passed")
}
