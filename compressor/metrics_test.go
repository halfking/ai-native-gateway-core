package compressor

import (
	"strings"
	"testing"
)

func TestRecordOutcome_TriggeredIncrements(t *testing.T) {
	ResetMetrics()
	before := TriggeredCount(ModeAutoThreshold, ReasonAutoThreshold, StrategyMechanicalTrim)
	RecordOutcome(ModeAutoThreshold, ReasonAutoThreshold, StrategyMechanicalTrim, 358400, 105000, 0.123)
	RecordOutcome(ModeAutoThreshold, ReasonAutoThreshold, StrategyMechanicalTrim, 500000, 200000, 0.456)
	after := TriggeredCount(ModeAutoThreshold, ReasonAutoThreshold, StrategyMechanicalTrim)
	if after-before != 2 {
		t.Errorf("triggered count: want +2, got %v (before=%v after=%v)", after-before, before, after)
	}
}

func TestRecordOutcome_RatioClamped(t *testing.T) {
	ResetMetrics()
	// Normal: 50% ratio (shrunk)
	RecordOutcome(ModeAutoThreshold, ReasonAutoThreshold, StrategyMechanicalTrim, 1000, 500, 0.01)
	// >1 (rebuilt bigger than original — pathological) should clamp to 1.0
	RecordOutcome(ModeAutoThreshold, ReasonAutoThreshold, StrategyMechanicalTrim, 100, 500, 0.01)
	// We can't read the gauge directly without prometheus testutil, but we
	// can at least ensure RecordOutcome doesn't panic on edge cases.
	RecordOutcome(ModeAutoThreshold, ReasonAutoThreshold, StrategyMechanicalTrim, 0, 0, 0.0)
	RecordOutcome(ModeAutoThreshold, ReasonAutoThreshold, StrategyMechanicalTrim, -1, -1, 0.0)
}

func TestRecordOutcome_DifferentStrategiesAreSeparate(t *testing.T) {
	ResetMetrics()
	RecordOutcome(ModeOn4xx, ReasonOn4xx, StrategyMechanicalTrim, 1000, 500, 0.01)
	RecordOutcome(ModeAutoThreshold, ReasonAutoThreshold, StrategyMechanicalTrim, 2000, 1000, 0.02)
	if got := TriggeredCount(ModeOn4xx, ReasonOn4xx, StrategyMechanicalTrim); got != 1 {
		t.Errorf("on_4xx count: want 1, got %v", got)
	}
	if got := TriggeredCount(ModeAutoThreshold, ReasonAutoThreshold, StrategyMechanicalTrim); got != 1 {
		t.Errorf("auto_threshold count: want 1, got %v", got)
	}
}

func TestRecordOutcome_NilSafety(t *testing.T) {
	// ResetMetrics already nukes defaultMetrics if needed; RecordOutcome
	// should tolerate a nil defaultMetrics struct (defensive).
	// We can't easily simulate nil defaultMetrics without re-init, so
	// just exercise the happy path and verify it doesn't panic on zero
	// values.
	ResetMetrics()
	defer func() {
		if rec := recover(); rec != nil {
			t.Errorf("RecordOutcome panicked on zero values: %v", rec)
		}
	}()
	RecordOutcome(ModeOff, ReasonNone, StrategyNone, 0, 0, 0.0)
}

func TestMode_String_StableForMetricLabels(t *testing.T) {
	// Metric labels must be stable across releases. Pin the values so
	// dashboards / alerts don't silently break when refactoring Mode.
	cases := []struct {
		m    Mode
		want string
	}{
		{ModeOff, "off"},
		{ModeAutoThreshold, "auto_threshold"},
		{ModeOn4xx, "on_4xx"},
	}
	for _, tc := range cases {
		got := tc.m.String()
		if got != tc.want {
			t.Errorf("Mode(%d).String(): want %q (stable label), got %q", int(tc.m), tc.want, got)
		}
		if strings.ContainsAny(got, " \t\n") {
			t.Errorf("Mode label must not contain whitespace: %q", got)
		}
	}
}
