package streaming

// large_ingress_truthful_errors_test.go —— 入口拒绝「如实返回」的判据测试
// （2026-11-22，211 事故）。
//
// 事故样本（A 测试实录，7.7MB 请求体在回退态的生产上）：
//
//	503  {"error":{"type":"invalid_request_error",
//	      "gateway_debug":{"retryable":true,"stage":"ingress"},
//	      "message":"request preprocessing exceeded its time limit"}}
//
// 三处误导：① 句子不说请求体多大、也不说重试有没有用；② 503 的错误 type 是
// invalid_request_error（「请求不合法」）；③ 事实数据（大小、时限）只在网关内部。
//
// 契约（本文件钉住）：
//  1. 客户端句子带事实与处置（本地化）：请求体大小、时限/上限、重试是否有用；
//  2. 事实数据以旁键给机器：body_bytes / preprocess_budget_seconds / limit_bytes；
//  3. 错误 type 跟状态码说同一件事（503 不再叫 invalid_request_error）；
//  4. 状态码与 retryable 语义不变（超时＝网关侧处理能力 503 可重试；放不下＝
//     输入问题 422 不可重试——TestLargeIngressProcessingDeadlineIsRetryable 的合同）；
//  5. 日志句子与客户端句子两分开（ingressClientFacing）：日志侧不被本地化替换；
//  6. Anthropic 信封带机器可读的 code（此前只有 type + message）。

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/i18n"
	"github.com/kaixuan/llm-gateway-go/internal/largebody"
)

func newIngressHandler(t *testing.T, guard func(context.Context, func() io.Reader) (bool, error)) *ChatHandler {
	t.Helper()
	h := NewChatHandler(nil, nil, nil, nil, nil, nil)
	h.SetHistoryGuardFactory(func(context.Context) func(context.Context, func() io.Reader) (bool, error) { return guard })
	return h
}

// serveIngress 走 LargeIngress 的真实 Wrap（鉴权门在测试里按既有用例的形态放行）。
func serveIngress(t *testing.T, h *ChatHandler, path, locale string, cfg LargeIngressConfig, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	store, err := largebody.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	req = req.WithContext(i18n.WithLocale(req.Context(), i18n.Locale(locale)))
	w := httptest.NewRecorder()
	NewLargeIngress(h, store, cfg).Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("rejected request was forwarded to the handler")
	})).ServeHTTP(w, req)
	return w
}

// decodeIngressError 取出 error 对象；OpenAI 与 Anthropic 两种信封的 error 对象都在
// 顶层 "error" 键下，事实旁键在 error.gateway_debug 里。
func decodeIngressError(t *testing.T, w *httptest.ResponseRecorder) (map[string]any, map[string]any, string) {
	t.Helper()
	var body struct {
		Error map[string]json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v (body=%s)", err, w.Body.String())
	}
	var debug map[string]any
	if raw, ok := body.Error["gateway_debug"]; ok {
		_ = json.Unmarshal(raw, &debug)
	}
	var msg string
	_ = json.Unmarshal(body.Error["message"], &msg)
	fields := map[string]any{}
	for k, v := range body.Error {
		var val any
		if json.Unmarshal(v, &val) == nil {
			fields[k] = val
		}
	}
	return fields, debug, msg
}

// TestIngressTimeoutSpeaksTheFacts —— 211 事故的那条错误：句子要说清请求体多大、
// 时限多长、这是什么性质的问题、重试有没有用；type 不再是 invalid_request_error。
func TestIngressTimeoutSpeaksTheFacts(t *testing.T) {
	source := `{"messages":[{"role":"user","content":"first task"},{"role":"assistant","content":"` + strings.Repeat("x", 1_100_000) + `"},{"role":"user","content":"current task"}]}`
	timedOut := func(context.Context, func() io.Reader) (bool, error) { return false, context.DeadlineExceeded }

	t.Run("anthropic_zh", func(t *testing.T) {
		h := newIngressHandler(t, timedOut)
		r := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(source))
		w := serveIngress(t, h, "/v1/messages", "zh-CN", LargeIngressConfig{}, r)
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503\nbody: %s", w.Code, w.Body.String())
		}
		errObj, debug, msg := decodeIngressError(t, w)
		// ① 事实 + 处置，本地化。
		for _, want := range []string{"120s", "1.0 MiB", "网关侧的处理能力限制", "可以稍后重试", "新开会话"} {
			if !strings.Contains(msg, want) {
				t.Errorf("句子缺少 %q\n实际：%s", want, msg)
			}
		}
		if strings.Contains(msg, "exceeded its time limit") {
			t.Errorf("仍是旧的笼统句子：%s", msg)
		}
		// ② 事实旁键。
		if errObj["code"] != "request_processing_timeout" {
			t.Errorf("code = %v", errObj["code"])
		}
		if debug["body_bytes"] != float64(len(source)) || debug["preprocess_budget_seconds"] != float64(120) {
			t.Errorf("事实旁键不全：body_bytes=%v preprocess_budget_seconds=%v", debug["body_bytes"], debug["preprocess_budget_seconds"])
		}
		// ③ type 跟状态码一致；④ 既有合同不变。
		if errObj["type"] != "overloaded_error" {
			t.Errorf("type = %v, want overloaded_error（503 不得叫 invalid_request_error）", errObj["type"])
		}
		if debug["retryable"] != true {
			t.Errorf("retryable = %v, want true（超时＝网关侧处理能力，合同不变）", debug["retryable"])
		}
	})

	t.Run("openai_en", func(t *testing.T) {
		h := newIngressHandler(t, timedOut)
		r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(source))
		w := serveIngress(t, h, "/v1/chat/completions", "en", LargeIngressConfig{}, r)
		errObj, _, msg := decodeIngressError(t, w)
		for _, want := range []string{"within 120s", "1.0 MiB", "gateway-side capacity limit", "start a new session"} {
			if !strings.Contains(msg, want) {
				t.Errorf("句子缺少 %q\n实际：%s", want, msg)
			}
		}
		if errObj["type"] != "server_error" || errObj["code"] != "request_processing_timeout" {
			t.Errorf("type/code = %v/%v", errObj["type"], errObj["code"])
		}
	})
}

// TestIngressBudgetExceededSpeaksTheFacts —— 「放不下」是输入问题：说清是哪一部分
// 放不下、上限多少、为什么重试没用（422 不可重试）。
func TestIngressBudgetExceededSpeaksTheFacts(t *testing.T) {
	// 最后一条用户任务 5KB > 有界上下文 4KB：受保护部分本身放不下，无法自动缩减。
	source := `{"messages":[{"role":"user","content":"first task"},{"role":"assistant","content":"` + strings.Repeat("x", 300_000) + `"},{"role":"user","content":"` + strings.Repeat("y", 5000) + `"}]}`
	pass := func(context.Context, func() io.Reader) (bool, error) { return true, nil }
	h := newIngressHandler(t, pass)
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(source))
	w := serveIngress(t, h, "/v1/messages", "zh-CN", LargeIngressConfig{ContextBytes: 4096}, r)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422\nbody: %s", w.Code, w.Body.String())
	}
	errObj, debug, msg := decodeIngressError(t, w)
	for _, want := range []string{"放不进网关", "4.0 KiB", "无法自动缩减", "原样重试会再次失败"} {
		if !strings.Contains(msg, want) {
			t.Errorf("句子缺少 %q\n实际：%s", want, msg)
		}
	}
	if errObj["code"] != "context_budget_exceeded" || errObj["type"] != "invalid_request_error" {
		t.Errorf("code/type = %v/%v", errObj["code"], errObj["type"])
	}
	if debug["retryable"] != false || debug["body_bytes"] != float64(len(source)) {
		t.Errorf("retryable/body_bytes = %v/%v", debug["retryable"], debug["body_bytes"])
	}
}

// TestIngressTooLargeSpeaksTheFacts —— 超过 100MB 上限：说清实际大小与上限，
// type 是 request_too_large（Anthropic 类型集里请求体过大另有其名）。
func TestIngressTooLargeSpeaksTheFacts(t *testing.T) {
	h := newIngressHandler(t, func(context.Context, func() io.Reader) (bool, error) { return true, nil })
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	r.ContentLength = largebody.LimitBytes + 1 // 该分支只看声明长度，不读请求体
	w := serveIngress(t, h, "/v1/messages", "zh-CN", LargeIngressConfig{}, r)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413\nbody: %s", w.Code, w.Body.String())
	}
	errObj, debug, msg := decodeIngressError(t, w)
	if !strings.Contains(msg, "超过了网关上限") || !strings.Contains(msg, "95.4 MiB") {
		t.Errorf("句子缺少事实：%s", msg)
	}
	if errObj["type"] != "request_too_large" || errObj["code"] != "prompt_too_large" {
		t.Errorf("type/code = %v/%v", errObj["type"], errObj["code"])
	}
	if debug["limit_bytes"] != float64(largebody.LimitBytes) || debug["retryable"] != false {
		t.Errorf("limit_bytes/retryable = %v/%v", debug["limit_bytes"], debug["retryable"])
	}
}

// TestIngressErrorTypeFollowsStatus —— 入口拒绝的错误 type 与状态码说同一件事
// （此前一律 invalid_request_error）。Anthropic 面经 anthropicErrorType 映射到
// SDK 认识的闭集。
func TestIngressErrorTypeFollowsStatus(t *testing.T) {
	cases := []struct {
		status int
		openai string
		anth   string
	}{
		{http.StatusUnauthorized, "authentication_error", "authentication_error"},
		{http.StatusForbidden, "permission_error", "permission_error"},
		{http.StatusRequestTimeout, "request_timeout", "request_timeout"},
		{http.StatusRequestEntityTooLarge, "request_too_large", "request_too_large"},
		{http.StatusTooManyRequests, "rate_limit_error", "rate_limit_error"},
		{http.StatusServiceUnavailable, "server_error", "overloaded_error"},
		{http.StatusUnprocessableEntity, "invalid_request_error", "invalid_request_error"},
		{http.StatusBadRequest, "invalid_request_error", "invalid_request_error"},
		{http.StatusUnsupportedMediaType, "invalid_request_error", "invalid_request_error"},
	}
	for _, tc := range cases {
		got := ingressErrorType(tc.status)
		if got != tc.openai {
			t.Errorf("status %d: ingressErrorType = %q, want %q", tc.status, got, tc.openai)
		}
		if a := anthropicErrorType(got, ""); a != tc.anth {
			t.Errorf("status %d: anthropic type = %q, want %q", tc.status, a, tc.anth)
		}
	}
}

// TestIngressClientFacingKeepsLogAndClientApart —— 日志句子与客户端句子两分开：
// 没有覆盖时客户端看到日志那句；有覆盖时只换客户端句子、只并旁键，日志句子
// 不被本地化文案替换（排障/看板按稳定英文检索）。
func TestIngressClientFacingKeepsLogAndClientApart(t *testing.T) {
	const logMsg = "request preprocessing exceeded its time limit"

	got, extras := ingressClientFacing(logMsg, 503, nil)
	if got != logMsg {
		t.Fatalf("无覆盖时客户端句子应等于日志句子：%q", got)
	}
	if extras["stage"] != "ingress" || extras["retryable"] != true {
		t.Errorf("默认旁键 = %v", extras)
	}

	got, extras = ingressClientFacing(logMsg, 503, []ingressRejection{{
		clientMessage: "网关未能在 120s 内预处理完这个请求",
		extra:         map[string]any{"body_bytes": int64(7_757_461), "preprocess_budget_seconds": int64(120)},
	}})
	if got != "网关未能在 120s 内预处理完这个请求" {
		t.Errorf("客户端句子 = %q", got)
	}
	if extras["body_bytes"] != int64(7_757_461) || extras["preprocess_budget_seconds"] != int64(120) || extras["retryable"] != true {
		t.Errorf("旁键 = %v", extras)
	}
	if _, leaked := extras["message"]; leaked {
		t.Errorf("本地化句子不得混进旁键：%v", extras)
	}
	// 422：输入问题，不可重试。
	if _, extras := ingressClientFacing(logMsg, 422, nil); extras["retryable"] != false {
		t.Errorf("422 的 retryable = %v, want false", extras["retryable"])
	}
}

// TestHumanByteSize —— 量级给人、精确值给对账。
func TestHumanByteSize(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{512, "512 bytes"},
		{4096, "4.0 KiB (4096 bytes)"},
		{7_757_461, "7.4 MiB (7757461 bytes)"},
		{100_000_000, "95.4 MiB (100000000 bytes)"},
	}
	for _, tc := range cases {
		if got := humanByteSize(tc.in); got != tc.want {
			t.Errorf("humanByteSize(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
