package executors

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/nodehealth"
	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// 本文件钉住 2026-11-02「4xx 归因族收口轮」在**执行器侧**的闸门。
//
// 判据本体在 errorsx 包（errorsx/model_unavailable_test.go）；本文件钉
// 各调用点的入口——这些入口只看 ErrorKind、不看 HTTP 状态码 ⇒ 分类口径
// 一变，它们就跟着变。
//
// ── 被钉住的那一档（绑定级）───────────────────────────────────────────
//
// 裸 404 = 「上游确定性地不提供这个 (凭据,模型) 对」。它是**第三类**处置：
//
//	请求侧（request_rejected）：证据指向这次请求 ⇒ 什么都不写
//	绑定级（model_unavailable）：证据指向这一对   ⇒ 只写这一对的冷却
//	节点/凭据级（transient/auth/…）：证据指向节点  ⇒ 罚节点/账号
//
// ★ 绑定级这一档此前**不存在**：裸 404 只能落请求侧（什么都不写）或节点侧
//   （罚节点 ⇒ 3 连败整凭据 cooling ⇒ 全租户 503，2026-11-02 事故本体）。
//
// ★ 它也**不能**借用 KindModelNotFound：那是终态语义 ⇒ 放弃跨凭据故障转移。

// TestNodeHealthErrorKindModelUnavailable 钉 reducer 的档位：
// model_unavailable ⇒ ErrorKindModelUnavailable（**绑定级**），
// 不是 ErrorKindRequest（请求侧 ⇒ 一个字节的共享状态都不写），
// 也不是 ErrorKindUpstream（节点级 ⇒ 3 连败 ⇒ 整凭据 cooling）。
func TestNodeHealthErrorKindModelUnavailable(t *testing.T) {
	got := nodeHealthErrorKind(errorsx.KindModelUnavailable)
	if got != nodehealth.ErrorKindModelUnavailable {
		t.Fatalf("nodeHealthErrorKind(KindModelUnavailable) = %q, want %q",
			got, nodehealth.ErrorKindModelUnavailable)
	}
	if got == nodehealth.ErrorKindRequest {
		t.Fatal("nodeHealthErrorKind(KindModelUnavailable) = ErrorKindRequest ⇒ " +
			"绑定级处置被降级成请求侧，该对不会被拉黑（§六.7 登记的缺口原样回来）")
	}
	if got == nodehealth.ErrorKindUpstream {
		t.Fatal("nodeHealthErrorKind(KindModelUnavailable) = ErrorKindUpstream ⇒ " +
			"会累计账号连败 ⇒ 3 连败后整凭据 cooling ⇒ 全租户 503（事故本体）")
	}
	// 请求侧族与凭据级族不得被挤出去。
	for _, k := range []errorsx.ErrorKind{
		errorsx.KindRequestRejected, errorsx.KindClientBug,
		errorsx.KindModelNotFound, errorsx.KindAuth,
	} {
		if got := nodeHealthErrorKind(k); got == nodehealth.ErrorKindModelUnavailable {
			t.Fatalf("nodeHealthErrorKind(%q) = %q —— 既有 kind 被新 kind 抢走了", k, got)
		}
	}
}

// TestTransientSuppressErrorCodeModelUnavailable 钉「请求路径不写
// node_probe_state」。
//
// ★ 为什么不写：绑定级冷却走的是 credential_model_bindings（cmb.available
//   - unavailable_reason + unavailable_recover_at，30 分钟起步的分级桶），
//     那才是路由的事实源。node_probe_state 那一行是**探针侧**的事实源，
//     请求路径去写它等于和探针抢同一行（§6.4 ② 记录的「竞争同一行」），
//     而且那里的抑制码语义是「节点临时抖动」（http_5xx / timeout / …），
//     放一个绑定级的 404 进去只会让两边的语义互相污染。
//
// ⇒ 断言：model_unavailable **不**进 transientSuppressErrorCode 的表。
func TestTransientSuppressErrorCodeModelUnavailable(t *testing.T) {
	if code, ok := transientSuppressErrorCode(errorsx.KindModelUnavailable); ok {
		t.Fatalf("transientSuppressErrorCode(KindModelUnavailable) = (%q, true), want (\"\", false) —— "+
			"绑定级处置走 cmb 分级冷却；写 node_probe_state 会与探针侧抢同一行", code)
	}
	// 真故障仍然要被抑制。
	for _, k := range []errorsx.ErrorKind{
		errorsx.KindTransient, errorsx.KindTimeout, errorsx.KindNetwork,
		errorsx.KindRateLimit, errorsx.KindUpstreamDown,
	} {
		if _, ok := transientSuppressErrorCode(k); !ok {
			t.Fatalf("transientSuppressErrorCode(%q) 未抑制 —— 真故障必须仍被抑制", k)
		}
	}
}

// TestIsTransientRouteNodeFailureModelUnavailable 钉「记不记路由节点失败样本」。
//
// 该函数的读法：返回 true = **不记**成该路由节点的失败样本。
// 请求侧族（上游拒收这次请求）不记——节点是健康的。
//
// ★ model_unavailable 要**记**：这不是「客户端的错单」，是这一对
//
//	(凭据,模型) 真的失败了。与 model_not_found 同判（它也不在跳过表里）。
func TestIsTransientRouteNodeFailureModelUnavailable(t *testing.T) {
	if isTransientRouteNodeFailure(errorsx.KindModelUnavailable) {
		t.Fatal("isTransientRouteNodeFailure(KindModelUnavailable) = true —— " +
			"这一对真的失败了，必须记成路由节点失败样本（与 model_not_found 同判）")
	}
	// 请求侧族仍然不记。
	if !isTransientRouteNodeFailure(errorsx.KindRequestRejected) {
		t.Fatal("isTransientRouteNodeFailure(KindRequestRejected) = false —— " +
			"请求侧错误不得记成路由节点失败样本（节点其实是健康的）")
	}
}

// TestDispatchFailureIsCredentialHealthyModelUnavailable 钉「账号仍然健康」。
//
// model_unavailable 是**绑定级**证据：这一对不可用，账号本身健康
// ⇒ 与 model_not_found 同判（不扣凭据健康）。
func TestDispatchFailureIsCredentialHealthyModelUnavailable(t *testing.T) {
	if !dispatchFailureIsCredentialHealthy(errorsx.KindModelUnavailable, false) {
		t.Fatal("dispatchFailureIsCredentialHealthy(KindModelUnavailable) = false, want true —— " +
			"绑定级证据不指向账号，扣凭据健康会连坐兄弟模型")
	}
	if dispatchFailureIsCredentialHealthy(errorsx.KindTransient, false) {
		t.Fatal("dispatchFailureIsCredentialHealthy(KindTransient) = true, want false（既有行为不得变）")
	}
}
