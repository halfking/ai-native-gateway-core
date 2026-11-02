package executors

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/credential"
	"github.com/kaixuan/llm-gateway-go/domains/nodehealth"
	"github.com/kaixuan/llm-gateway-go/domains/requestjourney"
	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// ── 第十一轮 · F-C 的执行器侧闸门（2026-11-02）────────────────────────────
//
// 「无可用后端」这一档的**回写 kind 必须按归责层级分写**。
//
// ★ 为什么必须分写：4xx 与 5xx 的「无可用后端」在**下游策略上是相反的**：
//
//   4xx（中转 "No enterprise provider is available for this request path"）
//     ⇒ 绑定级 + **确定性** ⇒ 这一对 (凭据,模型) 确定不提供
//     ⇒ 冷却要长（绑定级 15min）、不该被当成瞬时噪声
//   5xx（中转 "No available channel for model X under group Y"）
//     ⇒ 凭据级 + **瞬时** ⇒ 中转分组暂时没有通道
//     ⇒ 冷却要短（凭据级 30s）、**免费档必须被容忍**（245 free-capacity
//        plan：免费供给「flaky 但偶尔可用」，不能连败 3 次就硬禁用）
//
// ★ 单一 kind 字符串**表达不了**这两种相反的策略。第九轮（F-B）把它统一
//   回写成 no_available_channel ⇒ 下游**按字符串分档**的闭集全部没有登记
//   这一档 ⇒ 连带四处行为漂移（详见审计档 §十四.2）。本轮把 5xx/未知那一支
//   **逐字回落**到 KindTransient ⇒ 全部恢复。
//
// ★ 分档的判据与 reducer 里 isBindingScoped(observation) **同源**
//   （同一个上游状态码、同一个 400..499 判据）。

// TestErrorKindFromNodeHealthNoAvailableChannelScopeSplit 钉门 S（映射级）：
// 4xx ⇒ 绑定级专属 kind；5xx / 状态码未知 ⇒ 瞬时 kind。
func TestErrorKindFromNodeHealthNoAvailableChannelScopeSplit(t *testing.T) {
	// 4xx（绑定级证据）：专属 kind（绑定级 15min 冷却，确定性证据不该被容忍）。
	for _, status := range []int{400, 404, 429, 499} {
		got := errorKindFromNodeHealth(nodehealth.ErrorKindNoAvailableChannel, status)
		if got != errorsx.KindNoAvailableChannel {
			t.Fatalf("errorKindFromNodeHealth(NoAvailableChannel, %d) = %q, want %q",
				status, got, errorsx.KindNoAvailableChannel)
		}
	}
	// 5xx（凭据级证据）：**瞬时**语义 ⇒ 必须回落 KindTransient。
	for _, status := range []int{500, 502, 503, 599} {
		got := errorKindFromNodeHealth(nodehealth.ErrorKindNoAvailableChannel, status)
		if got != errorsx.KindTransient {
			t.Fatalf("errorKindFromNodeHealth(NoAvailableChannel, %d) = %q, want %q "+
				"（凭据级语义是「中转暂时没有通道」⇒ 瞬时 ⇒ 必须与第九轮之前逐字一致）",
				status, got, errorsx.KindTransient)
		}
	}
	// 状态码未知（0）⇒ 与 5xx 同归责（凭据级）⇒ 同样回落瞬时。
	if got := errorKindFromNodeHealth(nodehealth.ErrorKindNoAvailableChannel, 0); got != errorsx.KindTransient {
		t.Fatalf("errorKindFromNodeHealth(NoAvailableChannel, 0) = %q, want %q",
			got, errorsx.KindTransient)
	}
}

// scopeSplitStateFake 捕获 nodehealth 写回路径上的两类写。
type scopeSplitStateFake struct {
	bindingKinds   []errorsx.ErrorKind
	credentialKind []errorsx.ErrorKind
}

func (f *scopeSplitStateFake) Enabled() bool { return true }

func (f *scopeSplitStateFake) RestoreOnSuccess(context.Context, int, string) error { return nil }

func (f *scopeSplitStateFake) WriteOnError(_ context.Context, _ int, _ string, failure credential.Failure) error {
	f.bindingKinds = append(f.bindingKinds, failure.Kind)
	return nil
}

func (f *scopeSplitStateFake) SetCredentialUnavailable(_ context.Context, _ int, failure credential.Failure) error {
	f.credentialKind = append(f.credentialKind, failure.Kind)
	return nil
}

// TestApplyNodeHealthDecisionNoAvailableChannel4xxWritesBindingOnly 钉门 S
// （行为级 · 4xx）：绑定级证据 ⇒ 只写**绑定**那一对，**不**写凭据级，
// 且写回的 kind 是专属 kind（确定性证据要长冷却）。
func TestApplyNodeHealthDecisionNoAvailableChannel4xxWritesBindingOnly(t *testing.T) {
	fake := &scopeSplitStateFake{}
	exec := &Executor{NodeHealthState: fake, Circuit: credential.NewManager()}
	adapter := executorNodeHealthAdapter{executor: exec}

	d := nodehealth.Decision{
		Node:        nodehealth.NodeKey{ProviderID: 1, CredentialID: 7, Model: "m"},
		Outcome:     requestjourney.OutcomeFailure,
		ErrorKind:   nodehealth.ErrorKindNoAvailableChannel,
		HTTPStatus:  400,
		BillingMode: "paid",
		ErrorDetail: "No enterprise provider is available for this request path",
		Effects: []nodehealth.Effect{
			{Kind: nodehealth.EffectSetBindingUnavailable},
			{Kind: nodehealth.EffectInvalidateCandidateCache},
		},
	}
	if err := adapter.ApplyNodeHealthDecision(context.Background(), d); err != nil {
		t.Fatalf("apply: %v", err)
	}

	if len(fake.credentialKind) != 0 {
		t.Fatalf("4xx 的「无可用后端」写了凭据级状态（%v）⇒ 连坐该凭据上的全部模型",
			fake.credentialKind)
	}
	if len(fake.bindingKinds) != 1 {
		t.Fatalf("绑定级写 = %v, want 恰好 1 次", fake.bindingKinds)
	}
	if fake.bindingKinds[0] != errorsx.KindNoAvailableChannel {
		t.Fatalf("绑定级写回的 kind = %q, want %q", fake.bindingKinds[0], errorsx.KindNoAvailableChannel)
	}
}

// TestApplyNodeHealthDecisionNoAvailableChannel5xxWritesTransientKind 钉门 S
// （行为级 · 5xx）：凭据级证据 ⇒ 写回**瞬时** kind。
//
// ★ 这是第九轮那条回归的**直接复现**：第九轮把这一支也回写成
//
//	no_available_channel ⇒ 凭据级冷却 30s → 15min、URSM 免费档被硬禁用、
//	断路器落进粘滞兜底、主动探针从第 1 次失败挪到第 2 次。
func TestApplyNodeHealthDecisionNoAvailableChannel5xxWritesTransientKind(t *testing.T) {
	fake := &scopeSplitStateFake{}
	exec := &Executor{NodeHealthState: fake, Circuit: credential.NewManager()}
	adapter := executorNodeHealthAdapter{executor: exec}

	d := nodehealth.Decision{
		Node:        nodehealth.NodeKey{ProviderID: 1, CredentialID: 7, Model: "m"},
		Outcome:     requestjourney.OutcomeFailure,
		ErrorKind:   nodehealth.ErrorKindNoAvailableChannel,
		HTTPStatus:  503,
		BillingMode: "paid",
		ErrorDetail: "No available channel for model m under group g",
		Effects: []nodehealth.Effect{
			{Kind: nodehealth.EffectRecordCircuitFailure},
			{Kind: nodehealth.EffectSetBindingUnavailable},
			{Kind: nodehealth.EffectSetCredentialUnavailable},
		},
	}
	if err := adapter.ApplyNodeHealthDecision(context.Background(), d); err != nil {
		t.Fatalf("apply: %v", err)
	}

	if len(fake.credentialKind) != 1 {
		t.Fatalf("凭据级写 = %v, want 恰好 1 次", fake.credentialKind)
	}
	if fake.credentialKind[0] != errorsx.KindTransient {
		t.Fatalf("凭据级写回的 kind = %q, want %q —— 5xx 的「无可用后端」是瞬时语义，"+
			"回写专属 kind 会让凭据级冷却从 30s 变成 15min、并让 URSM 免费档被硬禁用",
			fake.credentialKind[0], errorsx.KindTransient)
	}
	if len(fake.bindingKinds) != 1 || fake.bindingKinds[0] != errorsx.KindTransient {
		t.Fatalf("绑定级写 = %v, want 恰好 1 次且为 %q", fake.bindingKinds, errorsx.KindTransient)
	}
	// 断路器记的也必须是瞬时 kind（它在 defaultPolicies / freeTierPolicies /
	// transientFamily 三张表里都有档位）。
	brk := exec.Circuit.Get(1, 7)
	if brk == nil {
		t.Fatal("断路器没有记录这次失败")
	}
	if got, _ := brk.Stats()["last_error_kind"].(string); got != string(errorsx.KindTransient) {
		t.Fatalf("断路器记的 kind = %q, want %q", got, errorsx.KindTransient)
	}
}
