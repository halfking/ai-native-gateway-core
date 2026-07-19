package alerting

import (
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

func TestRuleFiring(t *testing.T) {
	manager := NewManager()
	triggered := false

	rule := &Rule{
		Name:        "test_rule",
		Description: "Test rule",
		Condition:   func() bool { return triggered },
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

	triggered = true
	time.Sleep(150 * time.Millisecond)

	alerts = manager.GetFiringAlerts()
	if len(alerts) != 1 {
		t.Errorf("Expected 1 alert, got %d", len(alerts))
	}

	manager.Stop()
	t.Log("RuleFiring test passed")
}

func TestHighErrorRateRule(t *testing.T) {
	errorRate := 0.05
	rule := HighErrorRateRule(
		func() float64 { return errorRate },
		0.10,
	)

	if rule.Condition() {
		t.Error("Rule should not fire")
	}

	errorRate = 0.15
	if !rule.Condition() {
		t.Error("Rule should fire")
	}

	t.Log("HighErrorRateRule test passed")
}
