package executors

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/dispatch"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/provider"
	upstreampkg "github.com/kaixuan/llm-gateway-go/upstream"
)

// ─── 出站格式门（protocolformat）错误的分类必须落到 client_bug（2026-11-11）───
//
// 现象（211 生产实测，2.5.9-532f5fa2）：
//
//	POST /v1/messages  {"model":"claude-fable-5-2","max_tokens":1024,
//	                    "messages":[{"content":"no role"}]}
//	⇒ 客户端拿到 **503 "All providers unavailable"**（误导为上游过载）
//	⇒ request_logs 落 error_kind=**model_not_found**（误导性分类）
//
// 对照组（入口层校验）：缺 max_tokens ⇒ 400 invalid_request，
// 落 error_kind=**missing_max_tokens**（精确）。
//
// ⇒ 两条路径不一致：入口层校验有精确分类，出站格式门没有。
//
// ★ 断点定位（2026-11-11 第三层实测）：格式门返回的
// *upstream.Error{Kind: KindClientBug, StatusCode: 400} 在 **executor 层**
// 分类是**正确**的（classifyExecError ⇒ client_bug；dispatchErrToExecuteError
// ⇒ LastKind=client_bug），真正的断点在**协议面层**：2026-11-10 的 P1-B 审计
// 只给 chat 面（handler.go）加了 client_bug 分支，anthropic-messages 面与
// responses 面漏了 ⇒ 落到通用兜底 503。面层的修复与端到端钉测见
// domains/streaming/request_shape_rejection_test.go。
//
// 本文件钉住 executor 层的**两端**：
//   ① 格式门本身必须返回 KindClientBug + 400（fail-closed，不许出网）；
//   ② dispatch 汇总后 kind 不得塌成 transient（否则会被盲重试）。

// TestDispatchErrToExecuteError_ClientBugKeepsKind pins that a request-side
// upstream error survives the dispatch aggregation with its real kind.
//
// ★ 为什么必须在 dispatch 汇总这一层钉：ExhaustedError.Cause 是最后一个具体
// 上游失败；若 dispatchErrToExecuteError 只认 rate_limit、其余一律 transient，
// 则 client_bug（永久畸形 ⇒ 重试无意义）会被塌成 transient ⇒
// EffectiveRetryable(transient)=true ⇒ 同一个坏请求被盲重试。
func TestDispatchErrToExecuteError_ClientBugKeepsKind(t *testing.T) {
	cause := &upstreampkg.Error{
		Kind:       errorsx.KindClientBug,
		Message:    "request failed anthropic-messages format validation",
		StatusCode: http.StatusBadRequest,
	}
	// 形态与 dispatch 汇总一致：ExhaustedError 包装最后一个具体上游失败。
	err := &dispatch.ExhaustedError{Cause: cause}

	ee := dispatchErrToExecuteError(err)
	if ee == nil {
		t.Fatal("dispatchErrToExecuteError returned nil")
	}
	if ee.LastKind != errorsx.KindClientBug {
		t.Fatalf("LastKind = %q, want %q (格式门错误必须保留 client_bug)", ee.LastKind, errorsx.KindClientBug)
	}
	if errorsx.EffectiveRetryable(ee.LastKind) {
		t.Fatalf("EffectiveRetryable(%q) = true — a permanently malformed request must not be retried", ee.LastKind)
	}
}

// TestPrepareAnthropicRequestBody_FormatGateRejectsLocally pins the gate
// itself: a body that violates the anthropic-messages wire shape must be
// rejected locally with KindClientBug + 400 (never sent upstream).
func TestPrepareAnthropicRequestBody_FormatGateRejectsLocally(t *testing.T) {
	body := []byte(`{"model":"claude-fable-5-2","max_tokens":1024,"messages":[{"content":"no role"}]}`)
	cand := provider.Candidate{
		ProviderID:   587,
		CredentialID: 31,
		Protocol:     "anthropic-messages",
		RawModel:     "claude-fable-5-2",
	}
	r := httptest.NewRequest("POST", "/v1/messages", nil)
	params := &ExecParams{
		R:              r,
		ClientModel:    "claude-fable-5-2",
		OutboundModel:  "claude-fable-5-2",
		ClientProtocol: "anthropic-messages",
	}

	executor := &Executor{}
	out, err := executor.prepareAnthropicRequestBody(params, cand, body)
	if err == nil {
		t.Fatalf("format gate must reject locally, got body=%s", out)
	}
	kind := classifyExecError(err)
	if kind != errorsx.KindClientBug {
		t.Fatalf("classifyExecError = %q, want %q (format gate error is client-side)", kind, errorsx.KindClientBug)
	}
	if errorsx.EffectiveRetryable(kind) {
		t.Fatalf("EffectiveRetryable(%q) = true — a permanently malformed request must not be retried", kind)
	}
}
