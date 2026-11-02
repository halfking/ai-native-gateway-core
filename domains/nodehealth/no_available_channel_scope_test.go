package nodehealth_test

import (
	"fmt"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/nodehealth"
	"github.com/kaixuan/llm-gateway-go/domains/requestjourney"
)

// 本文件钉住 2026-11-02「第九轮（F-B）」的归责口径修正：**「无可用后端」要
// 按上游状态码分成两档**。
//
// ── 为什么 ────────────────────────────────────────────────────────────
//
// `errorsx.KindNoAvailableChannel`（"no backend can serve this"）此前只有
// 一个归责档位：`nodehealth.ErrorKindUpstream` ⇒ **不是绑定级** ⇒ 连败到
// 阈值（付费 3 / 免费 6）就 `EffectRecordCircuitFailure` +
// `EffectSetCredentialUnavailable` ⇒ **整个凭据 cooling** ⇒ 该凭据下**其它**
// 模型、其它租户全部 503。
//
// ★ 但「无可用后端」有两种**证据指向不同层**的事实：
//
//	4xx（中转的 "No enterprise provider is available for this request path:
//	  POST /v1/messages. Required model: claude-haiku-5-5; model series:
//	  claude."）⇒ 证据指向**这一对 (凭据,模型)**（这个凭据在这条路径上不
//	  提供这个模型系列）⇒ **绑定级**
//	5xx（中转分组的 "No available channel for model X under group Y
//	  (distributor)"）⇒ 证据指向**中转整体** ⇒ **凭据级**（既有且正确）
//
// ★ 生产证据（211，2026-10-13，45 天窗口内该形态只有 **1** 个事件）：
//   凭据 63 / claude-haiku-5-5 连续两次请求各打满 3 次同节点盲重试
//   （attempt_index 1→2→3），正文是上面那条 4xx 形态 ⇒ 那一对是**确定性**
//   不可用 ⇒ 正确处置是**只冷却这一对**，而不是连坐整个凭据。
//
// ★ 这也与规则自己的注释相悖：errorsx/classify.go 的 noEnterpriseProviderRe
//   写明「KindNoAvailableChannel means fail over to a sibling
//   credential/model, **not** blind same-node retries that **park a healthy
//   node**」⇒ 它明确不要「park 一个健康节点」，而旧归责恰恰 park 了整个凭据。
//
// ── 这道门断言什么 ────────────────────────────────────────────────────
//
//	门 G：4xx 的 no_available_channel ⇒ 绑定级 effect，**账号级 effect 一个
//	      都不许有**（连打 5 次跨过付费阈值也一样）。
//	门 H（阴性对照）：5xx 的 no_available_channel ⇒ **必须**仍是凭据级
//	      （连打 3 次必须出现 EffectSetCredentialUnavailable）⇒ 否则这道门
//	      就成了「什么都是绑定级」的假门。
//	门 I（保守默认）：状态码未知（0）⇒ 凭据级（不因缺信息而放过节点）。

// TestOutcomeReducerNoAvailableChannel4xxIsBindingScoped 钉门 G。
func TestOutcomeReducerNoAvailableChannel4xxIsBindingScoped(t *testing.T) {
	r := nodehealth.NewOutcomeReducer()
	node := nodehealth.NodeKey{CredentialID: 42, Model: "claude-haiku-5-5"}

	// 连打 5 次（付费阈值是 3）⇒ 账号级 effect 一次都不许出现。
	for i := 0; i < 5; i++ {
		decision, err := r.Reduce(nodehealth.Observation{
			Node: node, AttemptID: fmt.Sprintf("attempt-%d", i), Phase: nodehealth.PhaseRequest,
			Outcome: requestjourney.OutcomeFailure, ErrorKind: nodehealth.ErrorKindNoAvailableChannel,
			HTTPStatus: 400,
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, required := range []nodehealth.EffectKind{
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
}

// TestOutcomeReducerNoAvailableChannel5xxStaysCredentialScoped 是门 H
// （阴性对照）：5xx 的「无可用后端」必须**保持凭据级**。
//
// ★ 没有这条，门 G 可以在「把整个 no_available_channel 都改成绑定级」的
// 情况下照样绿 ⇒ 而那会把「中转分组整体没有通道」这种**真的该熔断凭据**
// 的故障降级成只冷却一对 ⇒ 反而制造新的放大器（中转整体挂了却不再熔断
// 凭据 ⇒ 请求持续打到挂掉的中转上）。
func TestOutcomeReducerNoAvailableChannel5xxStaysCredentialScoped(t *testing.T) {
	r := nodehealth.NewOutcomeReducer()
	node := nodehealth.NodeKey{CredentialID: 43, Model: "grok-4.7-build-fast"}

	var sawCredentialEffect bool
	for i := 0; i < 3; i++ {
		decision, err := r.Reduce(nodehealth.Observation{
			Node: node, AttemptID: fmt.Sprintf("attempt-%d", i), Phase: nodehealth.PhaseRequest,
			Outcome: requestjourney.OutcomeFailure, ErrorKind: nodehealth.ErrorKindNoAvailableChannel,
			HTTPStatus: 503,
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
		t.Fatal("5xx 的「无可用后端」必须仍是凭据级（连败到阈值要熔断凭据）—— " +
			"这道门失去对照，测试恒绿")
	}
}

// TestOutcomeReducerNoAvailableChannelUnknownStatusStaysCredentialScoped
// 是门 I（保守默认）：状态码未知（0，例如错误被重新包装过）⇒ 凭据级。
//
// ★ 理由：缺信息时**保守**（沿用既有处置），而不是乐观地降级成绑定级。
func TestOutcomeReducerNoAvailableChannelUnknownStatusStaysCredentialScoped(t *testing.T) {
	r := nodehealth.NewOutcomeReducer()
	node := nodehealth.NodeKey{CredentialID: 44, Model: "model-c"}

	var sawCredentialEffect bool
	for i := 0; i < 3; i++ {
		decision, err := r.Reduce(nodehealth.Observation{
			Node: node, AttemptID: fmt.Sprintf("attempt-%d", i), Phase: nodehealth.PhaseRequest,
			Outcome: requestjourney.OutcomeFailure, ErrorKind: nodehealth.ErrorKindNoAvailableChannel,
			HTTPStatus: 0,
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
		t.Fatal("状态码未知的「无可用后端」必须保守地留在凭据级")
	}
}
