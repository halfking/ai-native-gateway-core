package dispatch

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// 本文件钉住 2026-11-02「4xx 归因族收口轮」新增的**绑定级**档位
// （errorsx.KindModelUnavailable）在 dispatch 闭集上的登记。
//
// ★ 这是最容易复发、且**失败时完全静默**的一处：
//
//	isCentralPolicyKind 是**闭集**——不在表里的 kind 会被
//
//	    if kind != "" && !isCentralPolicyKind(kind) {
//	        kind = errorsx.KindTransient
//	    }
//
//	**重写**成 KindTransient，而 KindTransient 正是 2026-11-02 事故的
//	放大器形态（盲重试 ×3 + 把 (凭据,模型) 按 http_5xx 拉黑 5 分钟 +
//	3 连败后整凭据 cooling ⇒ 全租户 503）。
//
// ⇒ 漏登记 ⇒ 分类层已经改对，但 dispatch 层把 kind 换回 transient，
//   一切照旧事故。⇒ 必须用行为测试钉死。

// TestIsCentralPolicyKindIncludesModelUnavailable 直接钉闭集成员身份。
func TestIsCentralPolicyKindIncludesModelUnavailable(t *testing.T) {
	if !isCentralPolicyKind(errorsx.KindModelUnavailable) {
		t.Fatal("isCentralPolicyKind(KindModelUnavailable) = false —— " +
			"漏登记会被重写成 KindTransient ⇒ 本轮修复静默失效")
	}
}

// TestPlanAfterFailureModelUnavailableSwitchesCredential 钉**行为**：
// 裸 404 时 dispatch 不得做同凭据盲重试（同 body 必然再败），必须落到
// 「换兄弟候选」。
//
// ★ 与请求侧族的**共同点**：保留跨凭据故障转移。
// ★ 与请求侧族的**区别**：本 kind 写绑定级冷却（归责层面），请求侧族不写。
func TestPlanAfterFailureModelUnavailableSwitchesCredential(t *testing.T) {
	cfg := Config{RetryPerCredential: 2}

	qr := planQR("mu1")
	d := PlanAfterFailure(qr, ForwardOutcome{ErrorKind: string(errorsx.KindModelUnavailable)}, cfg)
	if d.Action == NextActionRetrySameCred {
		t.Fatalf("model_unavailable → %+v, want fall-through to credential switch "+
			"（同凭据盲重试 = 事故的放大环节）", d)
	}
	if d.Action != "" {
		t.Fatalf("model_unavailable → %+v, want 空 Action（落向兄弟候选选择）", d)
	}
	if d.Reason != "upstream_model_unavailable" {
		t.Fatalf("reason = %q, want %q", d.Reason, "upstream_model_unavailable")
	}

	// 对照组：transient 仍然同凭据重试（既有行为不得变）。
	tr := planQR("mu2")
	td := PlanAfterFailure(tr, ForwardOutcome{ErrorKind: string(errorsx.KindTransient)}, cfg)
	if td.Action != NextActionRetrySameCred {
		t.Fatalf("transient → %+v, want retry_same_cred（既有行为不得变）", td)
	}
}
