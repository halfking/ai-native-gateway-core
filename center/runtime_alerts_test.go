package center

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/collector"
)

func TestEvaluateRuntimeAlertCandidates_CPUHigh(t *testing.T) {
	candidates := EvaluateRuntimeAlertCandidates(collector.RuntimeMetrics{
		InstanceID:  "gw-1",
		CPUUsagePct: 96,
	})
	if len(candidates) != 1 || candidates[0].RuleKey != RuleCPUHigh {
		t.Fatalf("expected cpu_high alert, got %+v", candidates)
	}
}

func TestEvaluateRuntimeAlertCandidates_DiskHigh(t *testing.T) {
	candidates := EvaluateRuntimeAlertCandidates(collector.RuntimeMetrics{
		InstanceID:  "gw-1",
		DiskUsedGB:  95,
		DiskTotalGB: 100,
	})
	if len(candidates) != 1 || candidates[0].RuleKey != RuleDiskHigh {
		t.Fatalf("expected disk_high alert, got %+v", candidates)
	}
}

func TestEvaluateRuntimeAlertCandidates_LowSuccess(t *testing.T) {
	candidates := EvaluateRuntimeAlertCandidates(collector.RuntimeMetrics{
		InstanceID:         "gw-1",
		Last5MinSuccessPct: 85,
	})
	if len(candidates) != 1 || candidates[0].RuleKey != RuleLowSuccessRate {
		t.Fatalf("expected low_success_rate alert, got %+v", candidates)
	}
}

func TestEvaluateRuntimeAlertCandidates_Healthy(t *testing.T) {
	candidates := EvaluateRuntimeAlertCandidates(collector.RuntimeMetrics{
		InstanceID:         "gw-1",
		CPUUsagePct:        40,
		DiskUsedGB:         10,
		DiskTotalGB:        100,
		Last5MinSuccessPct: 99,
	})
	if len(candidates) != 0 {
		t.Fatalf("expected no alerts, got %+v", candidates)
	}
}

func TestParseRuntimeAlertID(t *testing.T) {
	id, err := parseRuntimeAlertID("runtime-42")
	if err != nil || id != 42 {
		t.Fatalf("parseRuntimeAlertID failed: id=%d err=%v", id, err)
	}
	if _, err := parseRuntimeAlertID("center-offline-x"); err == nil {
		t.Fatal("expected unsupported alert id error")
	}
}
