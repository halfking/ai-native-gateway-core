package executors

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/credential"
	"github.com/kaixuan/llm-gateway-go/domains/nodehealth"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// ★ P0-a（2026-11-19 审计轮）生产路径同键钉测。
//
// 这些测试打的是**生产代码实际使用的入口**（dispatch 准入、nodehealth
// 适配器、协议腿记账），而不是直接调 Manager API——只有这样才能证明
// 「准入查的 breaker」与「记账写的 breaker」真的是同一把。
//
// 修复前：dispatch 准入用 AllowPerModel(provider, credential, RawModel)
// 而 nodehealth 记账用 RecordFailure*(provider, credential) ⇒ 两把
// breaker ⇒ chat 主链路熔断永不跳闸，管理面 circuit_state 恒显 closed。

// TestCircuitScopeModelUsesBindingIdentity 钉住作用域模型分量的取值：
// 必须是 offer 身份（BindingRawModel），而不是出站名（RawModel）。
//
// 为什么：provider/client.go 的注释明确「按 RawModel 建键会合并独立
// binding 的健康遥测」——同一出站名可被多个 binding 共享，用它建键会让
// 一个 binding 的失败连坐同凭据下的其他 binding。
func TestCircuitScopeModelUsesBindingIdentity(t *testing.T) {
	cases := []struct {
		name string
		cand provider.Candidate
		want string
	}{
		{
			name: "offer 身份优先",
			cand: provider.Candidate{RawModel: "gpt-4-0613", OfferRawModel: "gpt-4"},
			want: "gpt-4",
		},
		{
			name: "无 offer 时回落出站名",
			cand: provider.Candidate{RawModel: "gpt-4-0613"},
			want: "gpt-4-0613",
		},
		{
			name: "两边皆空 ⇒ 整凭据作用域",
			cand: provider.Candidate{},
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := circuitScopeModel(tc.cand); got != tc.want {
				t.Fatalf("circuitScopeModel = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestDispatchAdmissionAndNodeHealthAccountingShareBreaker 钉住主链路闭环：
// dispatch 准入 + nodehealth 失败记账必须作用在同一把 breaker 上 ⇒
// 连续失败后准入必须被拒（熔断真正跳闸）。
//
// 这是 P0-1 的回归门：修复前这条链路上熔断永不跳闸。
func TestDispatchAdmissionAndNodeHealthAccountingShareBreaker(t *testing.T) {
	mgr := credential.NewManager()
	exec := &Executor{Circuit: mgr}
	adapter := executorNodeHealthAdapter{executor: exec}

	cand := provider.Candidate{
		ProviderID:    1,
		CredentialID:  100,
		RawModel:      "gpt-4-0613",
		OfferRawModel: "gpt-4",
	}
	scope := circuitScopeModel(cand)

	// 准入（dispatch 热路径）。
	if !mgr.AllowPerModel(cand.ProviderID, cand.CredentialID, scope) {
		t.Fatal("首次准入必须放行")
	}

	// 失败记账（nodehealth 适配器 = 生产记账入口）。
	for i := 0; i < 2; i++ {
		decision := nodehealth.Decision{
			Node: nodehealth.NodeKey{
				ProviderID:   int64(cand.ProviderID),
				CredentialID: int64(cand.CredentialID),
				Model:        "standardized-model", // 刻意与 scope 不同
			},
			BreakerScopeModel: scope,
			ErrorKind:         nodehealth.ErrorKindNetwork,
			BillingMode:       "per_token",
			Effects:           []nodehealth.Effect{{Kind: nodehealth.EffectRecordCircuitFailure}},
		}
		if err := adapter.ApplyNodeHealthDecision(context.Background(), decision); err != nil {
			t.Fatalf("apply: %v", err)
		}
	}

	// 熔断必须真的跳闸：再次准入必须被拒。
	if mgr.AllowPerModel(cand.ProviderID, cand.CredentialID, scope) {
		t.Fatal("连续失败后准入必须被拒（准入与记账落在不同 breaker 上：熔断永不跳闸）")
	}
}

// TestNodeHealthAccountingUsesScopeModelNotNodeModel 钉住 adapter 用的是
// **熔断作用域**而不是节点健康键。
//
// 两者在退化候选上会不同（节点健康键允许回退到标准化名），若 adapter 误用
// Node.Model，记账就会落到准入查不到的那把 breaker 上 ⇒ 又一次分裂。
func TestNodeHealthAccountingUsesScopeModelNotNodeModel(t *testing.T) {
	mgr := credential.NewManager()
	exec := &Executor{Circuit: mgr}
	adapter := executorNodeHealthAdapter{executor: exec}

	decision := nodehealth.Decision{
		Node: nodehealth.NodeKey{
			ProviderID:   2,
			CredentialID: 200,
			Model:        "standardized-model",
		},
		BreakerScopeModel: "offer-model",
		ErrorKind:         nodehealth.ErrorKindNetwork,
		BillingMode:       "per_token",
		Effects:           []nodehealth.Effect{{Kind: nodehealth.EffectRecordCircuitFailure}},
	}
	if err := adapter.ApplyNodeHealthDecision(context.Background(), decision); err != nil {
		t.Fatalf("apply: %v", err)
	}

	b := mgr.GetPerModel(2, 200, "offer-model")
	if b == nil {
		t.Fatal("记账必须落在 BreakerScopeModel 作用域")
	}
	if got := b.ConsecutiveFailures(); got != 1 {
		t.Fatalf("作用域 breaker consecutive=%d, want 1", got)
	}
	if wrong := mgr.GetPerModel(2, 200, "standardized-model"); wrong != nil {
		t.Fatal("记账不得落在 Node.Model 作用域（否则与准入分裂）")
	}
}

// TestProtocolCircuitFailureSharesScopeWithAdmission 钉住协议腿（chat /
// anthropic / gemini / ollama 的流式中断路径）与准入同作用域。
func TestProtocolCircuitFailureSharesScopeWithAdmission(t *testing.T) {
	mgr := credential.NewManager()
	exec := &Executor{Circuit: mgr}

	cand := provider.Candidate{
		ProviderID:    3,
		CredentialID:  300,
		RawModel:      "gpt-4-0613",
		OfferRawModel: "gpt-4",
	}
	scope := circuitScopeModel(cand)

	for i := 0; i < 2; i++ {
		exec.recordProtocolCircuitFailure(nil, cand.ProviderID, cand.CredentialID, scope, errorsx.KindNetwork, "per_token")
	}

	if mgr.AllowPerModel(cand.ProviderID, cand.CredentialID, scope) {
		t.Fatal("协议腿记账后准入必须被拒（协议腿与准入落在不同 breaker 上）")
	}
}

// TestProtocolCircuitSuccessReleasesSameScope 钉住协议腿成功记账也在同一作用域。
func TestProtocolCircuitSuccessReleasesSameScope(t *testing.T) {
	mgr := credential.NewManager()
	exec := &Executor{Circuit: mgr}

	cand := provider.Candidate{ProviderID: 4, CredentialID: 400, RawModel: "gpt-4"}
	scope := circuitScopeModel(cand)

	exec.recordProtocolCircuitSuccess(nil, cand.ProviderID, cand.CredentialID, scope)

	b := mgr.GetPerModel(cand.ProviderID, cand.CredentialID, scope)
	if b == nil {
		t.Fatal("成功记账必须落在准入所查的 breaker 上")
	}
}
