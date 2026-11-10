package nodehealth_test

// 断链①（2026-11-11）：KindEmptyResponse 持久降级的梯子测试。
// 语义边界：
//   - streak < 阈值 ⇒ 只发 EffectUpdateURSM（既有「短促突发软信号」语义不变）
//   - streak ≥ 阈值 ⇒ 追加 EffectSetBindingUnavailable + EffectInvalidateCandidateCache，
//     且永不出现 EffectRecordCircuitFailure / EffectSetCredentialUnavailable
//     （绑定位作用域 —— 不连坐同凭据兄弟模型，不进账号级断路器）
//   - 任何成功（含探针）或任何非空失败 ⇒ streak 归零
//   - 同一 attempt 的重复观测不重复计数（dedup）

import (
	"fmt"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/nodehealth"
	"github.com/kaixuan/llm-gateway-go/domains/requestjourney"
)

func emptyObservation(node nodehealth.NodeKey, attempt string) nodehealth.Observation {
	return nodehealth.Observation{
		Node: node, AttemptID: attempt, Phase: nodehealth.PhaseRequest,
		Outcome: requestjourney.OutcomeFailure, ErrorKind: nodehealth.ErrorKindEmptyResponse,
	}
}

func effectKinds(d nodehealth.Decision) map[nodehealth.EffectKind]int {
	out := make(map[nodehealth.EffectKind]int)
	for _, e := range d.Effects {
		out[e.Kind]++
	}
	return out
}

// 阈值以下保持 URSM-only：既有语义（短促突发不摘容量）不被本次演进改变。
func TestEmptyResponseStreakBelowThresholdStaysURSMOnly(t *testing.T) {
	threshold := 5
	r := nodehealth.NewOutcomeReducerWithConfig(nodehealth.ReducerConfig{
		EmptyResponseStreakThreshold: func() int { return threshold },
	})
	node := nodehealth.NodeKey{TenantID: "t", CredentialID: 7, Model: "glm-5.3-flash"}

	for i := 1; i < threshold; i++ {
		d, err := r.Reduce(emptyObservation(node, fmt.Sprintf("a%d", i)))
		if err != nil {
			t.Fatal(err)
		}
		if d.EmptyResponseStreak != i {
			t.Fatalf("attempt %d: streak=%d want %d", i, d.EmptyResponseStreak, i)
		}
		kinds := effectKinds(d)
		if kinds[nodehealth.EffectUpdateURSM] != 1 || len(d.Effects) != 1 {
			t.Fatalf("below threshold: effects=%v want exactly [update_ursm]", d.Effects)
		}
		for _, forbidden := range []nodehealth.EffectKind{
			nodehealth.EffectSetBindingUnavailable,
			nodehealth.EffectSetCredentialUnavailable,
			nodehealth.EffectRecordCircuitFailure,
		} {
			if kinds[forbidden] > 0 {
				t.Fatalf("below threshold: forbidden effect %s fired at streak %d", forbidden, i)
			}
		}
		if d.ConsecutiveFailures != 0 || d.Status != requestjourney.NodeHealthHealthy {
			t.Fatalf("empty response must not change node health: %+v", d)
		}
	}
}

// 达阈值 ⇒ 绑定级降级；且仍然是绑定位作用域（无断路器、无凭据级）。
func TestEmptyResponseStreakAtThresholdDemotesBindingOnly(t *testing.T) {
	r := nodehealth.NewOutcomeReducerWithConfig(nodehealth.ReducerConfig{
		EmptyResponseStreakThreshold: func() int { return 3 },
	})
	node := nodehealth.NodeKey{TenantID: "t", CredentialID: 7, Model: "glm-5.3-flash"}

	var d nodehealth.Decision
	for i := 1; i <= 3; i++ {
		var err error
		d, err = r.Reduce(emptyObservation(node, fmt.Sprintf("a%d", i)))
		if err != nil {
			t.Fatal(err)
		}
	}
	kinds := effectKinds(d)
	if kinds[nodehealth.EffectSetBindingUnavailable] != 1 {
		t.Fatalf("threshold reached: set_binding_unavailable missing, effects=%v", d.Effects)
	}
	if kinds[nodehealth.EffectInvalidateCandidateCache] != 1 {
		t.Fatalf("threshold reached: candidate cache invalidation missing, effects=%v", d.Effects)
	}
	if kinds[nodehealth.EffectUpdateURSM] != 1 {
		t.Fatalf("threshold reached: URSM update missing, effects=%v", d.Effects)
	}
	for _, forbidden := range []nodehealth.EffectKind{
		nodehealth.EffectRecordCircuitFailure,
		nodehealth.EffectSetCredentialUnavailable,
		nodehealth.EffectScheduleProbe,
		nodehealth.EffectQuarantine,
	} {
		if kinds[forbidden] > 0 {
			t.Fatalf("binding-scoped demotion must not fire %s", forbidden)
		}
	}
	if d.EmptyResponseStreak != 3 {
		t.Fatalf("streak=%d want 3", d.EmptyResponseStreak)
	}
	// 节点健康状态本身仍不被空响应改变（质量信号≠宕机信号）。
	if d.Status != requestjourney.NodeHealthHealthy || d.ConsecutiveFailures != 0 {
		t.Fatalf("empty streak must not change node health: %+v", d)
	}
}

// 请求成功 ⇒ streak 归零；下一轮空响应重新从 1 计数。
func TestEmptyResponseStreakResetsOnSuccess(t *testing.T) {
	r := nodehealth.NewOutcomeReducerWithConfig(nodehealth.ReducerConfig{
		EmptyResponseStreakThreshold: func() int { return 3 },
	})
	node := nodehealth.NodeKey{TenantID: "t", CredentialID: 7, Model: "m"}

	for i := 1; i <= 2; i++ {
		if _, err := r.Reduce(emptyObservation(node, fmt.Sprintf("e%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	d, err := r.Reduce(nodehealth.Observation{
		Node: node, AttemptID: "ok", Phase: nodehealth.PhaseRequest,
		Outcome: requestjourney.OutcomeSuccess,
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.EmptyResponseStreak != 0 {
		t.Fatalf("success must reset streak, got %d", d.EmptyResponseStreak)
	}
	d, err = r.Reduce(emptyObservation(node, "e-after"))
	if err != nil {
		t.Fatal(err)
	}
	if d.EmptyResponseStreak != 1 {
		t.Fatalf("streak after reset=%d want 1", d.EmptyResponseStreak)
	}
	if effectKinds(d)[nodehealth.EffectSetBindingUnavailable] > 0 {
		t.Fatalf("demotion must not fire right after reset")
	}
}

// 直连探针成功也复位 streak（探测是「上游又能出内容」的直接证据）。
func TestEmptyResponseStreakResetsOnDirectProbeSuccess(t *testing.T) {
	r := nodehealth.NewOutcomeReducerWithConfig(nodehealth.ReducerConfig{
		EmptyResponseStreakThreshold: func() int { return 2 },
	})
	node := nodehealth.NodeKey{TenantID: "t", CredentialID: 7, Model: "m"}
	if _, err := r.Reduce(emptyObservation(node, "e1")); err != nil {
		t.Fatal(err)
	}
	d, err := r.Reduce(nodehealth.Observation{
		Node: node, AttemptID: "probe-ok", Phase: nodehealth.PhaseDirectProbe,
		Outcome: requestjourney.OutcomeSuccess,
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.EmptyResponseStreak != 0 {
		t.Fatalf("probe success must reset streak, got %d", d.EmptyResponseStreak)
	}
}

// 非空失败打断「连续」性质 ⇒ streak 归零（它有自己的惩罚链）。
func TestEmptyResponseStreakResetsOnOtherFailureKind(t *testing.T) {
	r := nodehealth.NewOutcomeReducerWithConfig(nodehealth.ReducerConfig{
		EmptyResponseStreakThreshold: func() int { return 3 },
	})
	node := nodehealth.NodeKey{TenantID: "t", CredentialID: 7, Model: "m"}
	for i := 1; i <= 2; i++ {
		if _, err := r.Reduce(emptyObservation(node, fmt.Sprintf("e%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.Reduce(nodehealth.Observation{
		Node: node, AttemptID: "net", Phase: nodehealth.PhaseRequest,
		Outcome: requestjourney.OutcomeFailure, ErrorKind: nodehealth.ErrorKindNetwork,
	}); err != nil {
		t.Fatal(err)
	}
	d, err := r.Reduce(emptyObservation(node, "e-after-net"))
	if err != nil {
		t.Fatal(err)
	}
	if d.EmptyResponseStreak != 1 {
		t.Fatalf("streak after intervening network failure=%d want 1", d.EmptyResponseStreak)
	}
}

// 兄弟模型（同凭据不同模型）不连坐：A 模型连击降级后 B 模型照常服务。
func TestEmptyResponseStreakDoesNotPolluteSiblingModel(t *testing.T) {
	r := nodehealth.NewOutcomeReducerWithConfig(nodehealth.ReducerConfig{
		EmptyResponseStreakThreshold: func() int { return 2 },
	})
	modelA := nodehealth.NodeKey{TenantID: "t", CredentialID: 7, Model: "glm-5.3-flash"}
	modelB := nodehealth.NodeKey{TenantID: "t", CredentialID: 7, Model: "glm-5.2"}

	for i := 1; i <= 2; i++ {
		if _, err := r.Reduce(emptyObservation(modelA, fmt.Sprintf("a%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	d, err := r.Reduce(nodehealth.Observation{
		Node: modelB, AttemptID: "b1", Phase: nodehealth.PhaseRequest,
		Outcome: requestjourney.OutcomeFailure, ErrorKind: nodehealth.ErrorKindNetwork,
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.ConsecutiveFailures != 1 || d.Status != requestjourney.NodeHealthSuspect {
		t.Fatalf("model-b polluted by model-a empty streak: %+v", d)
	}
	if d.EmptyResponseStreak != 0 {
		t.Fatalf("model-b inherited streak: %d", d.EmptyResponseStreak)
	}
}

// 同一 attempt 重复观测（outbox 重放）不重复计数。
func TestEmptyResponseStreakDuplicateObservationDoesNotDoubleCount(t *testing.T) {
	r := nodehealth.NewOutcomeReducerWithConfig(nodehealth.ReducerConfig{
		EmptyResponseStreakThreshold: func() int { return 100 },
	})
	node := nodehealth.NodeKey{TenantID: "t", CredentialID: 7, Model: "m"}
	if _, err := r.Reduce(emptyObservation(node, "same-attempt")); err != nil {
		t.Fatal(err)
	}
	d, err := r.Reduce(emptyObservation(node, "same-attempt"))
	if err != nil {
		t.Fatal(err)
	}
	if !d.Duplicate || d.Accepted {
		t.Fatalf("expected duplicate decision: %+v", d)
	}
	d2, err := r.Reduce(emptyObservation(node, "next-attempt"))
	if err != nil {
		t.Fatal(err)
	}
	if d2.EmptyResponseStreak != 2 {
		t.Fatalf("streak after duplicate replay=%d want 2", d2.EmptyResponseStreak)
	}
}

// 零值配置（无注入阈值）⇒ DefaultEmptyResponseStreakThreshold=5 生效。
func TestEmptyResponseStreakDefaultThresholdIsFive(t *testing.T) {
	if nodehealth.DefaultEmptyResponseStreakThreshold != 5 {
		t.Fatalf("DefaultEmptyResponseStreakThreshold=%d want 5", nodehealth.DefaultEmptyResponseStreakThreshold)
	}
	r := nodehealth.NewOutcomeReducer()
	node := nodehealth.NodeKey{TenantID: "t", CredentialID: 7, Model: "m"}
	var d nodehealth.Decision
	for i := 1; i <= 5; i++ {
		var err error
		d, err = r.Reduce(emptyObservation(node, fmt.Sprintf("a%d", i)))
		if err != nil {
			t.Fatal(err)
		}
	}
	if effectKinds(d)[nodehealth.EffectSetBindingUnavailable] != 1 {
		t.Fatalf("default threshold 5 must demote at 5th empty, effects=%v", d.Effects)
	}
}
