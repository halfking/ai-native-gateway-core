package dispatch

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// TestPlanAfterFailureFormatGuardSwitchBudget 钉格式守卫换节点预算：
//
//	首失败（AttemptCount=1）⇒ 仍允许换兄弟；
//	已试两个候选（AttemptCount>=2）再撞格式守卫 ⇒ 终态，不再空耗候选链。
func TestPlanAfterFailureFormatGuardSwitchBudget(t *testing.T) {
	cfg := Config{RetryPerCredential: 0}
	fmtErr := &errorsx.OutputPolicyError{
		RuleID: errorsx.GuardRuleRedactInvalid,
		Reason: "redacted body is not valid JSON",
	}
	out := ForwardOutcome{
		Err:       fmtErr,
		ErrorKind: string(errorsx.KindRequestRejected),
	}

	first := planQR("fg-budget-1")
	first.AttemptCount = 1
	d1 := PlanAfterFailure(first, out, cfg)
	if d1.Action == NextActionFailed {
		t.Fatalf("first format-guard failure must still allow sibling switch, got %+v", d1)
	}
	if d1.Reason == "format_guard_switch_budget" {
		t.Fatal("AttemptCount=1 must not trip format_guard_switch_budget")
	}

	second := planQR("fg-budget-2")
	second.AttemptCount = 2
	d2 := PlanAfterFailure(second, out, cfg)
	if d2.Action != NextActionFailed {
		t.Fatalf("second format-guard failure must be terminal, got %+v", d2)
	}
	if d2.Reason != "format_guard_switch_budget" {
		t.Fatalf("reason = %q, want format_guard_switch_budget", d2.Reason)
	}

	// 阴性：非格式守卫的 request_rejected 不受此预算约束。
	plain := planQR("fg-budget-3")
	plain.AttemptCount = 2
	d3 := PlanAfterFailure(plain, ForwardOutcome{
		ErrorKind: string(errorsx.KindRequestRejected),
	}, cfg)
	if d3.Action == NextActionFailed && d3.Reason == "format_guard_switch_budget" {
		t.Fatal("non-format request_rejected must not trip format_guard_switch_budget")
	}
}
