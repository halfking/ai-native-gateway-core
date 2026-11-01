package executors

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/nodehealth"
	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// 本文件钉住 2026-11-02「上游 4xx 被记成节点不健康」一轮在**执行器侧**的
// 四道闸门。这四处是事故放大链在请求路径上的落点，每一处都只看 ErrorKind、
// 不看 HTTP 状态码 ⇒ 分类口径一变，它们就跟着变。
//
// 事故链（211 生产，request_id 逐环可查）：
//
//	上游 400（Ollama：does not support chat）
//	→ ① 盲重试 ×3（executor_chat.go 的 attempt 梯子）
//	→ ② node_probe_state 写入 last_direct_ok=FALSE + next_retry_at=+5min
//	    （transientSuppressErrorCode 把 400 当成 "http_5xx"）
//	→ ③ node-health reducer 累计 3 连败 ⇒ EffectSetCredentialUnavailable
//	    ⇒ 整个凭据（其下全部 binding、全部租户）置 cooling
//	→ ④ 探针队列入队 ⇒ 探针再打一次 400 ⇒ root_cause=protocol ⇒ 退避 6h
//
// 本轮把这类 4xx 归入 errorsx.KindRequestRejected（请求侧错误族）⇒
// ①②③④ 全部不再发生，而**跨凭据故障转移保留**（约 44% 的在册模型由
// ≥2 个凭据服务）。
//
// 判据本体在 errorsx 包（errorsx/request_rejected_test.go）；本文件只钉
// 这四道闸门各自的入口。

// TestNodeHealthErrorKindRequestRejected 钉 ③ 的断点：请求侧错误 ⇒
// nodehealth.ErrorKindRequest ⇒ reducer 的 effectsFor 直接返回 nil ⇒
// 不写绑定、不写凭据不可用、不记熔断、不更新 URSM、不排探针，
// 且**不累计 consecutive_failures**（连败阈值就是被这一条跨过去的）。
func TestNodeHealthErrorKindRequestRejected(t *testing.T) {
	got := nodeHealthErrorKind(errorsx.KindRequestRejected)
	if got != nodehealth.ErrorKindRequest {
		t.Fatalf("nodeHealthErrorKind(KindRequestRejected) = %q, want %q",
			got, nodehealth.ErrorKindRequest)
	}
	// 既有成员不得被挤出去。
	for _, k := range []errorsx.ErrorKind{
		errorsx.KindClientBug, errorsx.KindToolCallIdMismatch,
		errorsx.KindUnsupportedFeature, errorsx.KindCanceled,
		errorsx.KindContentFilter, errorsx.KindContextLength,
	} {
		if got := nodeHealthErrorKind(k); got != nodehealth.ErrorKindRequest {
			t.Fatalf("nodeHealthErrorKind(%q) = %q, want %q", k, got, nodehealth.ErrorKindRequest)
		}
	}
	// 节点侧故障族必须在判据之外（否则真故障就不记了）。
	for _, k := range []errorsx.ErrorKind{
		errorsx.KindTransient, errorsx.KindNetwork, errorsx.KindTimeout,
		errorsx.KindAuth, errorsx.KindQuota, errorsx.KindRateLimit,
		errorsx.KindModelNotFound,
	} {
		if got := nodeHealthErrorKind(k); got == nodehealth.ErrorKindRequest {
			t.Fatalf("nodeHealthErrorKind(%q) = %q, want a node-side kind", k, got)
		}
	}
}

// TestTransientSuppressErrorCodeRequestRejected 钉 ② 的断点：
// transientSuppressErrorCode 是「把这次失败写进 node_probe_state」的闸门，
// 它把 (凭据,模型) 按错误码拉黑 5 分钟（付费）/ 1 分钟（免费）。
//
// 注意它此前把 **400 记成了 "http_5xx"**——它只看 kind、不看状态码，
// 于是「客户端错单」与「上游挂了」在这里完全无法区分。
//
// 新 kind 不在它的表里 ⇒ 不写 node_probe_state ⇒ 该 (凭据,模型) 对
// 不会被踢出候选池。
func TestTransientSuppressErrorCodeRequestRejected(t *testing.T) {
	code, ok := transientSuppressErrorCode(errorsx.KindRequestRejected)
	if ok {
		t.Fatalf("transientSuppressErrorCode(KindRequestRejected) = (%q, true), want (\"\", false) —— "+
			"请求侧拒绝不得写 node_probe_state（否则该对被拉黑 5 分钟 ⇒ 全租户 503）", code)
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

// TestIsTransientRouteNodeFailureRequestRejected 钉「不记 fp-slot 节点失败」。
//
// ★ 注意这个函数名的读法：调用方 RecordFailure 的判据是
//
//	if isTransientRouteNodeFailure(kind) { return }   // ← true = **不记**
//
// 也就是说返回 true 表示「这类失败**不该**算作该路由节点的失败样本」。
// 请求侧错误族（上游拒收这次请求）正是这种：节点是健康的，不能因为
// 客户端的一条坏请求就把该节点记进失败样本。
func TestIsTransientRouteNodeFailureRequestRejected(t *testing.T) {
	if !isTransientRouteNodeFailure(errorsx.KindRequestRejected) {
		t.Fatal("isTransientRouteNodeFailure(KindRequestRejected) = false —— " +
			"返回 false 意味着会把它记成该路由节点的失败样本（节点其实是健康的）")
	}
	// 对照：凭据级失败（鉴权）**要**记成节点失败。
	if isTransientRouteNodeFailure(errorsx.KindAuth) {
		t.Fatal("isTransientRouteNodeFailure(KindAuth) = true, want false（鉴权失败必须记节点失败）")
	}
}

// TestDispatchFailureIsCredentialHealthyRequestRejected 钉「凭据仍然健康」：
// 请求侧拒绝不构成凭据级证据。
func TestDispatchFailureIsCredentialHealthyRequestRejected(t *testing.T) {
	if !dispatchFailureIsCredentialHealthy(errorsx.KindRequestRejected, false) {
		t.Fatal("dispatchFailureIsCredentialHealthy(KindRequestRejected) = false, want true")
	}
	if dispatchFailureIsCredentialHealthy(errorsx.KindTransient, false) {
		t.Fatal("dispatchFailureIsCredentialHealthy(KindTransient) = true, want false（既有行为不得变）")
	}
}
