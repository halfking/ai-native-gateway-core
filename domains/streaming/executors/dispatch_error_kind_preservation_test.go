package executors

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/dispatch"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	upstreampkg "github.com/kaixuan/llm-gateway-go/upstream"
)

// ─── 请求侧错误不得塌成 transient（2026-11-11 claude-fable-5-2 事故轮 · 第二层）───
//
// 事故形态（211 生产实证）：
//
//	上游 400 "claude-fable-5-2 requires adaptive thinking; ..."
//	⇒ dispatchErrToExecuteError 的 default 分支把真实 kind 塌成 KindTransient
//	⇒ EffectiveRetryable(KindTransient) = true
//	⇒ 同一个坏请求被**盲重试**（211 日志：all 2 candidates failed —— 两个
//	   凭据各被试一次，纯浪费且放大上游压力）
//	⇒ request_logs 的 error_kind 记成 transient（甚至兜底 model_not_found）
//	   ⇒ 运维读到的是误导性分类，排查方向被带偏。
//
// 本测试钉住：ExhaustedError.Cause 里带的**请求侧** kind 必须原样透出。

func TestDispatchErrToExecuteError_RequestSideKindNotCollapsed(t *testing.T) {
	// 上游 400 请求形态错误（本次事故的真实形态）。
	cause := &upstreampkg.Error{
		Kind:       errorsx.KindRequestRejected,
		StatusCode: 400,
		Body:       []byte(`{"error":{"message":"claude-fable-5-2 requires adaptive thinking","type":"invalid_request_error"}}`),
		Message:    "upstream 400",
	}
	exhausted := &dispatch.ExhaustedError{Cause: cause}

	ee := dispatchErrToExecuteError(exhausted)
	if ee == nil {
		t.Fatal("dispatchErrToExecuteError returned nil")
	}
	if ee.LastKind != errorsx.KindRequestRejected {
		t.Fatalf("LastKind = %q, want %q (request-side kind must not collapse to transient)", ee.LastKind, errorsx.KindRequestRejected)
	}
	// ★ 关键行为断言：请求侧错误**不可重试** ⇒ 不得盲重试。
	if errorsx.EffectiveRetryable(ee.LastKind) {
		t.Fatalf("EffectiveRetryable(%q) = true — a malformed request would be blindly retried", ee.LastKind)
	}
}

// 阴性对照：transient（节点/容量类）必须**保持** transient —— 本轮不许改变
// 既有的重试与恢复语义（零回归面）。
func TestDispatchErrToExecuteError_TransientStaysTransient(t *testing.T) {
	cause := &upstreampkg.Error{
		Kind:       errorsx.KindTransient,
		StatusCode: 502,
		Message:    "upstream 502",
	}
	exhausted := &dispatch.ExhaustedError{Cause: cause}

	ee := dispatchErrToExecuteError(exhausted)
	if ee == nil {
		t.Fatal("dispatchErrToExecuteError returned nil")
	}
	if ee.LastKind != errorsx.KindTransient {
		t.Fatalf("LastKind = %q, want %q (transient semantics must not change)", ee.LastKind, errorsx.KindTransient)
	}
	if !errorsx.EffectiveRetryable(ee.LastKind) {
		t.Fatalf("EffectiveRetryable(%q) = false — transient must stay retryable", ee.LastKind)
	}
}

// 阴性对照：rate_limit 的既有特例必须保持（429 + Retry-After 契约）。
func TestDispatchErrToExecuteError_RateLimitUnchanged(t *testing.T) {
	cause := &upstreampkg.Error{
		Kind:       errorsx.KindRateLimit,
		StatusCode: 429,
		Message:    "upstream 429",
	}
	exhausted := &dispatch.ExhaustedError{Cause: cause}

	ee := dispatchErrToExecuteError(exhausted)
	if ee == nil {
		t.Fatal("dispatchErrToExecuteError returned nil")
	}
	if ee.LastKind != errorsx.KindRateLimit {
		t.Fatalf("LastKind = %q, want %q", ee.LastKind, errorsx.KindRateLimit)
	}
}

// 阴性对照：凭据级致命错误（auth）**不在**请求侧族 ⇒ 不得被本规则改写。
func TestDispatchErrToExecuteError_CredentialFatalNotReclassified(t *testing.T) {
	cause := &upstreampkg.Error{
		Kind:       errorsx.KindAuth,
		StatusCode: 401,
		Message:    "upstream 401",
	}
	exhausted := &dispatch.ExhaustedError{Cause: cause}

	ee := dispatchErrToExecuteError(exhausted)
	if ee == nil {
		t.Fatal("dispatchErrToExecuteError returned nil")
	}
	if ee.LastKind == errorsx.KindRequestRejected {
		t.Fatalf("LastKind = %q — credential-fatal must not be reclassified as request-side", ee.LastKind)
	}
}

// 非 upstream 错误（无 Cause kind）必须保持 transient。
func TestDispatchErrToExecuteError_NoUpstreamCauseStaysTransient(t *testing.T) {
	ee := dispatchErrToExecuteError(fmt.Errorf("boom"))
	if ee == nil {
		t.Fatal("dispatchErrToExecuteError returned nil")
	}
	if ee.LastKind != errorsx.KindTransient {
		t.Fatalf("LastKind = %q, want %q", ee.LastKind, errorsx.KindTransient)
	}
}

// upstreamRequestSideKind 的族边界自身要钉住：只放行请求侧族。
//
// ★ 族的真实构成（errorsx.IsRequestSideError = IsClientBug ∪
//
//	{request_rejected, rate_limit_observed}）——
//	IsClientBug = {client_bug, tool_call_id_mismatch, unsupported_feature,
//	canceled}。
//
// ⚠ context_length / content_filter **不在**请求侧族（它们有各自的专门
// 分支：context budget 在 dispatchErrToExecuteError 的第一个 case 里已被
// 单独映射成 KindContextLength）⇒ 本断言按代码实际族写，不臆造成员。
func TestUpstreamRequestSideKind_Boundary(t *testing.T) {
	yes := []errorsx.ErrorKind{
		errorsx.KindRequestRejected,
		errorsx.KindClientBug,
		errorsx.KindToolCallIdMismatch,
		errorsx.KindUnsupportedFeature,
		errorsx.KindCanceled,
		errorsx.KindRateLimitObserved,
	}
	for _, k := range yes {
		err := &upstreampkg.Error{Kind: k, StatusCode: 400}
		if got := upstreamRequestSideKind(err); got != k {
			t.Fatalf("upstreamRequestSideKind(%q) = %q, want %q", k, got, k)
		}
	}
	no := []errorsx.ErrorKind{
		errorsx.KindTransient,
		errorsx.KindNetwork,
		errorsx.KindAuth,
		errorsx.KindQuota,
		errorsx.KindModelNotFound,
		errorsx.KindContextLength,
		errorsx.KindContentFilter,
	}
	for _, k := range no {
		err := &upstreampkg.Error{Kind: k, StatusCode: 500}
		if got := upstreamRequestSideKind(err); got != "" {
			t.Fatalf("upstreamRequestSideKind(%q) = %q, want empty", k, got)
		}
	}
	// 非 upstream 错误 → 空。
	if got := upstreamRequestSideKind(errors.New("plain")); got != "" {
		t.Fatalf("upstreamRequestSideKind(plain) = %q, want empty", got)
	}
	// 空 kind → 空。
	if got := upstreamRequestSideKind(&upstreampkg.Error{Kind: ""}); got != "" {
		t.Fatalf("upstreamRequestSideKind(empty kind) = %q, want empty", got)
	}
	_ = context.Canceled
}
