package dispatch

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// 本文件钉住 2026-11-02「上游 4xx 被记成节点不健康」一轮在 **dispatch
// 闭集**上的登记。
//
// ★ 这是本轮最容易复发、且**失败时完全静默**的一处：
//
//	isCentralPolicyKind 是**闭集**——不在表里的 kind 会被下面这段
//	**重写**：
//
//	    if kind != "" && !isCentralPolicyKind(kind) {
//	        kind = errorsx.KindTransient
//	    }
//
// 而 KindTransient 正是 2026-11-02 事故的放大器形态（盲重试 + 把
// (凭据,模型) 按 http_5xx 拉黑 5 分钟 + 3 连败后整凭据 cooling）。
//
// ⇒ 漏登记 ⇒ 本轮修复**静默失效**：分类层已经改对，但 dispatch 层把 kind
// 换回 transient，一切照旧事故。⇒ 必须用行为测试钉死。

// TestIsCentralPolicyKindIncludesRequestRejected 直接钉闭集成员身份。
func TestIsCentralPolicyKindIncludesRequestRejected(t *testing.T) {
	if !isCentralPolicyKind(errorsx.KindRequestRejected) {
		t.Fatal("isCentralPolicyKind(KindRequestRejected) = false —— " +
			"漏登记会被重写成 KindTransient ⇒ 本轮修复静默失效")
	}
	// 既有成员不得被挤出去（重写路径对它们同样致命）。
	for _, k := range []errorsx.ErrorKind{
		errorsx.KindTransient, errorsx.KindTimeout, errorsx.KindNetwork,
		errorsx.KindRateLimit, errorsx.KindAuth, errorsx.KindQuota,
		errorsx.KindUpstreamDown, errorsx.KindCanceled, errorsx.KindClientBug,
		errorsx.KindConcurrent, errorsx.KindAuthRevoked, errorsx.KindQuotaPeriodic,
		errorsx.KindQuotaBalance, errorsx.KindQuotaPermanent, errorsx.KindModelNotFound,
		errorsx.KindStreamTimeout, errorsx.KindToolCallIdMismatch, errorsx.KindContextLength,
		errorsx.KindUnsupportedFeature, errorsx.KindModelDeprecated, errorsx.KindContentFilter,
		errorsx.KindEmptyResponse, errorsx.KindConversion, errorsx.KindUpstreamContextLoss,
		errorsx.KindUpstreamOverloaded, errorsx.KindNoAvailableChannel,
	} {
		if !isCentralPolicyKind(k) {
			t.Fatalf("isCentralPolicyKind(%q) = false, want true（既有成员被挤出去了）", k)
		}
	}
}

// TestPlanAfterFailureRequestRejectedSwitchesCredential 钉**行为**：
// 上游 4xx（请求侧拒绝）时，dispatch 不得做同凭据盲重试，必须落到
// 「换兄弟候选」。
//
// 观测点对照：
//   - KindTransient（= 漏登记被重写后的样子）⇒ NextActionRetrySameCred
//   - KindRequestRejected（本轮口径）        ⇒ Action 为空 = 落向兄弟候选
func TestPlanAfterFailureRequestRejectedSwitchesCredential(t *testing.T) {
	cfg := Config{RetryPerCredential: 2}

	qr := planQR("rr1")
	d := PlanAfterFailure(qr, ForwardOutcome{ErrorKind: string(errorsx.KindRequestRejected)}, cfg)
	if d.Action == NextActionRetrySameCred {
		t.Fatalf("request_rejected → %+v, want fall-through to credential switch "+
			"（同凭据盲重试 = 事故的放大环节）", d)
	}
	if d.Action != "" {
		t.Fatalf("request_rejected → %+v, want 空 Action（落向兄弟候选选择）", d)
	}
	if d.Reason != "upstream_request_rejected" {
		t.Fatalf("reason = %q, want %q", d.Reason, "upstream_request_rejected")
	}

	// 对照组：transient 仍然同凭据重试（既有行为不得变）。
	tr := planQR("rr2")
	td := PlanAfterFailure(tr, ForwardOutcome{ErrorKind: string(errorsx.KindTransient)}, cfg)
	if td.Action != NextActionRetrySameCred {
		t.Fatalf("transient → %+v, want retry_same_cred（既有行为不得变）", td)
	}
}
