package executors

import (
	"context"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/credential"
	"github.com/kaixuan/llm-gateway-go/domains/nodehealth"
	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// P0-C 同层：请求路径入口 ApplyNodeHealthDecision / recordProtocolCircuitFailure
// 对 json-word（KindClientBug / ErrorKindRequest）不冷却。

func TestP0CNodeHealthRequestKindMapsToClientBug(t *testing.T) {
	got := errorKindFromNodeHealth(nodehealth.ErrorKindRequest)
	if got != errorsx.KindClientBug {
		t.Fatalf("errorKindFromNodeHealth(Request) = %s, want client_bug (round-trip)", got)
	}
	if errorsx.PenaltyOf(got).Target != errorsx.PenaltyNone {
		t.Fatalf("PenaltyOf(%s) = %v, want none", got, errorsx.PenaltyOf(got))
	}
	// 前提在场：其它 kind 不塌成 client_bug。
	if errorKindFromNodeHealth(nodehealth.ErrorKindNetwork) != errorsx.KindNetwork {
		t.Fatal("network mapping regress")
	}
}

func TestP0CApplyNodeHealthJsonWordDoesNotCool(t *testing.T) {
	// r1027 原文 → Classify → client_bug；经 nodeHealth 映射为 ErrorKindRequest。
	body := []byte(`{"error":{"message":"'messages' must contain the word 'json' in some form, to use 'response_format' of type 'json_object'.","type":"invalid_request_error","param":"messages","code":null}}`)
	if got := errorsx.ClassifyErrorWithBody(400, body); got != errorsx.KindClientBug {
		t.Fatalf("classify = %s, want client_bug (前提在场)", got)
	}
	nhKind := nodeHealthErrorKind(errorsx.KindClientBug)
	if nhKind != nodehealth.ErrorKindRequest {
		t.Fatalf("nodeHealthErrorKind(client_bug) = %s, want request", nhKind)
	}

	exec := &Executor{Circuit: credential.NewManager()}
	adapter := executorNodeHealthAdapter{executor: exec}
	// 故意附带 EffectRecordCircuitFailure：模拟误接线。映射修对后 PenaltyOf
	// 短路仍不得冷却（防御纵深）。
	d := p04CircuitDecision(nhKind, string(body))
	d.HTTPStatus = 400
	if err := adapter.ApplyNodeHealthDecision(context.Background(), d); err != nil {
		t.Fatalf("apply: %v", err)
	}
	brk := exec.Circuit.Get(9, 31)
	if brk == nil {
		// GetOrCreate 可能未触发——PenaltyNone 短路在 RecordFailure 内，
		// 也可能从未 GetOrCreate。两种都算不冷却；显式再 Record 验证。
		exec.recordProtocolCircuitFailure(nil, 9, 31, errorsx.KindClientBug, "paid")
		brk = exec.Circuit.Get(9, 31)
	}
	if brk != nil && brk.State() != credential.StateClosed {
		t.Fatalf("state=%s after json-word path, want closed", brk.State())
	}
	if brk != nil && brk.ConsecutiveFailures() != 0 {
		t.Fatalf("consecutive=%d, want 0", brk.ConsecutiveFailures())
	}
}

func TestP0CRecordProtocolCircuitFailureSkipsPenaltyNone(t *testing.T) {
	exec := &Executor{Circuit: credential.NewManager()}
	for i := 0; i < 5; i++ {
		exec.recordProtocolCircuitFailure(nil, 3, 7, errorsx.KindClientBug, "paid")
		exec.recordProtocolCircuitFailure(nil, 3, 7, errorsx.KindContextLength, "paid")
	}
	if brk := exec.Circuit.Get(3, 7); brk != nil && brk.State() != credential.StateClosed {
		t.Fatalf("PenaltyNone via recordProtocolCircuitFailure cooled breaker: %s", brk.State())
	}
	// 前提在场：同入口对 network 仍累计。
	exec.recordProtocolCircuitFailure(nil, 3, 8, errorsx.KindNetwork, "paid")
	exec.recordProtocolCircuitFailure(nil, 3, 8, errorsx.KindNetwork, "paid")
	brk := exec.Circuit.Get(3, 8)
	if brk == nil || (brk.State() == credential.StateClosed && brk.ConsecutiveFailures() == 0) {
		t.Fatal("network must still record via recordProtocolCircuitFailure (前提在场)")
	}
}

// TestP0CApplyNodeHealthResetsIn19MinAlignsCooling：规划卡「resets in 19min」
// 相对时长经 ApplyNodeHealthDecision → GradeQuotaRest → RecordFailureWithReset。
func TestP0CApplyNodeHealthResetsIn19MinAlignsCooling(t *testing.T) {
	detail := `{"error":{"message":"usage limit exceeded, resets in 0h 19min"}}`
	now := time.Now()
	hint := errorsx.GradeQuotaRest([]byte(detail), now)
	if !hint.HasReset {
		t.Fatal("前提在场：GradeQuotaRest must accept resets in 0h 19min")
	}
	skew := hint.ResetAt.Sub(now)
	if skew < 18*time.Minute || skew > 20*time.Minute {
		t.Fatalf("premise ResetAt skew=%v, want ≈19min", skew)
	}

	exec := &Executor{Circuit: credential.NewManager()}
	adapter := executorNodeHealthAdapter{executor: exec}
	d := p04CircuitDecision(nodehealth.ErrorKindQuota, detail)
	if err := adapter.ApplyNodeHealthDecision(context.Background(), d); err != nil {
		t.Fatalf("apply: %v", err)
	}
	brk := exec.Circuit.Get(9, 31)
	if brk == nil || brk.State() != credential.StateOpen {
		t.Fatalf("want OPEN after resets-in-19min quota, brk=%v state=%v", brk != nil, func() string {
			if brk == nil {
				return "nil"
			}
			return brk.State().String()
		}())
	}
	until := brk.CoolingUntil()
	lead := time.Until(until)
	if lead < 17*time.Minute || lead > 21*time.Minute {
		t.Fatalf("CoolingUntil lead=%v, want ≈19min (aligned via ApplyNodeHealthDecision)", lead)
	}
}
