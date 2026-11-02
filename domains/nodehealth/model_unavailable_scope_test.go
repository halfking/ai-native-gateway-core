package nodehealth_test

import (
	"fmt"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/nodehealth"
	"github.com/kaixuan/llm-gateway-go/domains/requestjourney"
)

// 本文件钉住 2026-11-02「4xx 归因族收口轮」新增的**绑定级**档位
// （ErrorKindModelUnavailable）在 reducer 里的处置。
//
// ★ 这一档是 2026-11-02 事故之后补上的第三类处置：
//
//	请求侧（ErrorKindRequest）：什么都不写（不罚节点）
//	绑定级（ErrorKindModelUnavailable）：只写这一对（不罚账号）
//	节点级（ErrorKindUpstream 等）：罚节点 ⇒ 3 连败 ⇒ 整凭据 cooling
//
// ★ 事故本体（211，2026-11-02）：一条客户端错单 ⇒ 上游 400 ⇒ 落
//   KindTransient ⇒ reducer 累计连败 ⇒ EffectSetCredentialUnavailable ⇒
//   **整个凭据**（其下全部 binding、全部租户）置 cooling ⇒ /v1/embeddings
//   对所有租户 503。
//
// ★ 所以这道门的核心断言是：**绑定级档位绝不能产生账号级 effect**。

// TestOutcomeReducerModelUnavailableIsBindingScoped 钉「只写这一对」。
func TestOutcomeReducerModelUnavailableIsBindingScoped(t *testing.T) {
	r := nodehealth.NewOutcomeReducer()
	node := nodehealth.NodeKey{CredentialID: 42, Model: "model-a"}

	decision, err := r.Reduce(nodehealth.Observation{
		Node: node, AttemptID: "attempt-1", Phase: nodehealth.PhaseRequest,
		Outcome: requestjourney.OutcomeFailure, ErrorKind: nodehealth.ErrorKindModelUnavailable,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []nodehealth.EffectKind{
		nodehealth.EffectPersistNodeStatus,
		nodehealth.EffectUpdateURSM,
		nodehealth.EffectSetBindingUnavailable,
		nodehealth.EffectInvalidateCandidateCache,
		nodehealth.EffectScheduleProbe,
	} {
		assertEffect(t, decision, required)
	}
	// ★ 账号级 effect 一个都不许有：这就是「全租户 503」的那一跳。
	for _, forbidden := range []nodehealth.EffectKind{
		nodehealth.EffectRecordCircuitFailure,
		nodehealth.EffectSetCredentialUnavailable,
	} {
		assertNoEffect(t, decision, forbidden)
	}
}

// TestOutcomeReducerModelUnavailableDoesNotAccumulateCredentialFailures 钉
// 「连败跨过阈值也不碰账号」。
//
// ★ 关键在于 effectsFor 的分支顺序：绑定级档位**先**命中
//
//	isBindingScoped 分支 ⇒ 账号级那条（isPermanent || 连败 >= 阈值 ⇒
//	EffectRecordCircuitFailure + EffectSetCredentialUnavailable）**永远不会被
//	评估**。本测试把这件事钉死：连打 5 次（付费阈值 3），账号级 effect
//	一次都不许出现。
//
// ★ 对照组用 ErrorKindNetwork（节点级）：同样连打 3 次，**必须**出现
//
//	EffectSetCredentialUnavailable —— 否则这道门就成了「恒绿」的假门。
func TestOutcomeReducerModelUnavailableDoesNotAccumulateCredentialFailures(t *testing.T) {
	r := nodehealth.NewOutcomeReducer()
	node := nodehealth.NodeKey{CredentialID: 42, Model: "model-a"}

	// 连打 5 次（跨过付费阈值 3）。
	for i := 0; i < 5; i++ {
		decision, err := r.Reduce(nodehealth.Observation{
			Node: node, AttemptID: fmt.Sprintf("attempt-%d", i), Phase: nodehealth.PhaseRequest,
			Outcome: requestjourney.OutcomeFailure, ErrorKind: nodehealth.ErrorKindModelUnavailable,
		})
		if err != nil {
			t.Fatal(err)
		}
		assertNoEffect(t, decision, nodehealth.EffectRecordCircuitFailure)
		assertNoEffect(t, decision, nodehealth.EffectSetCredentialUnavailable)
	}

	// 对照组：节点级失败连打 3 次 ⇒ 必须出现账号级 effect
	// （证明上面那三行不是因为「这一档根本不产生 effect」而恒绿）。
	r2 := nodehealth.NewOutcomeReducer()
	node2 := nodehealth.NodeKey{CredentialID: 43, Model: "model-b"}
	var sawCredentialEffect bool
	for i := 0; i < 3; i++ {
		decision, err := r2.Reduce(nodehealth.Observation{
			Node: node2, AttemptID: fmt.Sprintf("net-%d", i), Phase: nodehealth.PhaseRequest,
			Outcome: requestjourney.OutcomeFailure, ErrorKind: nodehealth.ErrorKindNetwork,
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, effect := range decision.Effects {
			if effect.Kind == nodehealth.EffectSetCredentialUnavailable {
				sawCredentialEffect = true
			}
		}
	}
	if !sawCredentialEffect {
		t.Fatal("对照组：节点级失败连打 3 次仍没有 EffectSetCredentialUnavailable —— " +
			"这道门失去对照，测试恒绿")
	}
}

// TestOutcomeReducerModelUnavailableKeepsSiblingModelServing 钉「不连坐」。
//
// ★ 事故的验收口径 ②：同一时刻该凭据上**其它**模型仍可路由。
func TestOutcomeReducerModelUnavailableKeepsSiblingModelServing(t *testing.T) {
	r := nodehealth.NewOutcomeReducer()
	modelA := nodehealth.NodeKey{CredentialID: 42, Model: "model-a"}
	modelB := nodehealth.NodeKey{CredentialID: 42, Model: "model-b"}

	if _, err := r.Reduce(nodehealth.Observation{
		Node: modelA, AttemptID: "attempt-a", Phase: nodehealth.PhaseRequest,
		Outcome: requestjourney.OutcomeFailure, ErrorKind: nodehealth.ErrorKindModelUnavailable,
	}); err != nil {
		t.Fatal(err)
	}

	// 兄弟模型的一次成功必须仍然是成功（没有账号级污染）。
	decision, err := r.Reduce(nodehealth.Observation{
		Node: modelB, AttemptID: "attempt-b", Phase: nodehealth.PhaseRequest,
		Outcome: requestjourney.OutcomeSuccess, ErrorKind: nodehealth.ErrorKind(""),
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Status != requestjourney.NodeHealthHealthy {
		t.Fatalf("兄弟模型的状态 = %q, want %q", decision.Status, requestjourney.NodeHealthHealthy)
	}
	assertNoEffect(t, decision, nodehealth.EffectSetCredentialUnavailable)
}
