package streaming

// no_candidate_cause_test.go —— 「候选为空」客户端可见归因的判据测试
// （2026-11-22，211 事故：claude-sonnet-5-5 的候选全部余额耗尽，客户端 23 笔
// 拿到的都是 "No available provider for model" + overloaded_error）。
//
// 三层一起钉（与模态不匹配轮同构）：
//  1. 归因函数本身：说什么、不说什么（不给 offer 计数）、解释不了就退回；
//  2. 信封：三个协议面、三个分支都带 cause / retryable，且无归因时逐字节不变；
//  3. 接线：文本守卫（调用存在且在 fallback 之前）+ 行为守卫（走真实管线，
//     控制流真的能到达归因）。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"
	"github.com/kaixuan/llm-gateway-go/i18n"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// resolverBase 实现 providerResolver 的五个方法（零候选 / 模型已知 / 无模态
// 问题）。它**不**实现 emptyCandidateDiagnoser —— 用来模拟「resolver 不具备
// 诊断能力」的旧替身。
type resolverBase struct{}

func (resolverBase) Enabled() bool { return true }
func (resolverBase) GetCandidates(context.Context, string, string, string) ([]provider.Candidate, *provider.Policy, error) {
	return nil, nil, nil
}
func (resolverBase) GetCandidatesByModality(context.Context, string, string, string, string) ([]provider.Candidate, *provider.Policy, error) {
	return nil, nil, nil
}
func (resolverBase) ModelKnown(context.Context, string) bool { return true }
func (resolverBase) ModelModalityMismatch(context.Context, string, string, string, string) (*provider.ModalityMismatch, error) {
	return nil, nil
}

// causeResolver 额外具备诊断能力（*provider.Client 的测试替身）。
type causeResolver struct {
	resolverBase
	breakdown provider.EmptyCandidateBreakdown
	err       error
}

func (r *causeResolver) DiagnoseEmptyCandidates(context.Context, string, string) (provider.EmptyCandidateBreakdown, error) {
	return r.breakdown, r.err
}

// sonnet55Breakdown 是 211 实测的 [candidate_diag] 计数：配额 8、供应商停用 5、
// offer 人工下线 2、凭据人工停用 1（一条 offer 可被多闸同时挡住）。
func sonnet55Breakdown() provider.EmptyCandidateBreakdown {
	return provider.EmptyCandidateBreakdown{
		OffersTotal:                       11,
		BlockedByCredentialManualDisabled: 1,
		BlockedByCredentialQuota:          8,
		BlockedByProviderDisabled:         5,
		BlockedByUnavailableReason:        2,
	}
}

// TestDiagnoseNoCandidateCause_SpeaksTheActualCause —— 211 事故形态：主因是
// 「上游额度/余额耗尽」，句子必须说出它，并明说重试没用。
func TestDiagnoseNoCandidateCause_SpeaksTheActualCause(t *testing.T) {
	h := &ChatHandler{provider: &causeResolver{breakdown: sonnet55Breakdown()}}

	zh := i18n.WithLocale(context.Background(), i18n.ZhCN)
	got := h.diagnoseNoCandidateCause(zh, "claude-sonnet-5-5", "default")
	if got.empty() {
		t.Fatal("归因为空：候选被状态闸挡住时必须给出原因")
	}
	// 令牌：按主因优先、按客户端可见类别去重（停用类三闸合成一个）。
	if want := "upstream_quota_exhausted+provider_disabled"; got.causeToken() != want {
		t.Errorf("cause token = %q, want %q", got.causeToken(), want)
	}
	for _, want := range []string{
		"claude-sonnet-5-5",
		"上游账户额度或余额已耗尽",    // 真因
		"供应商或凭据已被网关管理员停用", // 次因也要如实列出
		"原样重试没有用",
	} {
		if !strings.Contains(got.Message, want) {
			t.Errorf("zh-CN 文案缺少 %q\n实际：%s", want, got.Message)
		}
	}
	// ★ 不泄漏运营侧计数（有多少条 offer / 凭据）。
	for _, leak := range []string{"8 条", "11 offer", "5 条", "1 条"} {
		if strings.Contains(got.Message, leak) {
			t.Errorf("客户端文案不得带 offer 计数 %q：%s", leak, got.Message)
		}
	}
	// 英文（缺省 locale）同一结论。
	en := h.diagnoseNoCandidateCause(context.Background(), "claude-sonnet-5-5", "default")
	for _, want := range []string{"No available provider for model 'claude-sonnet-5-5'", "upstream account quota or balance exhausted", "Retrying the same request will not help"} {
		if !strings.Contains(en.Message, want) {
			t.Errorf("en 文案缺少 %q\n实际：%s", want, en.Message)
		}
	}
	// 旁键：机器可读原因 + 重试语义如实（不是「稍后再试」）。
	extras := got.extras()
	if extras["cause"] != "upstream_quota_exhausted+provider_disabled" || extras["retryable"] != false || extras["code"] != "no_candidate" {
		t.Errorf("extras = %v", extras)
	}
	// 多因并列的分隔符随语言走：中文用全角分号（211 活体验证时的半角分号读起来割裂）。
	if !strings.Contains(got.Message, "上游账户额度或余额已耗尽；供应商或凭据已被网关管理员停用") {
		t.Errorf("中文原因列表应使用全角分号：%s", got.Message)
	}
	// 请求日志：既有英文前缀逐字保留，归因附在后面。
	if want := "No available provider for model 'claude-sonnet-5-5' [cause=upstream_quota_exhausted+provider_disabled]"; got.logMessage("claude-sonnet-5-5") != want {
		t.Errorf("logMessage = %q\nwant %q", got.logMessage("claude-sonnet-5-5"), want)
	}
}

// TestDiagnoseNoCandidateCause_SingleGate —— 单闸形态逐一可读（每个客户端
// 可见类别都有对应的原因短语，未知令牌不会被编成词）。
func TestDiagnoseNoCandidateCause_SingleGate(t *testing.T) {
	cases := []struct {
		name     string
		b        provider.EmptyCandidateBreakdown
		cause    string
		contains string
	}{
		{"仅额度耗尽", provider.EmptyCandidateBreakdown{OffersTotal: 3, BlockedByCredentialQuota: 3}, provider.ClientCauseQuota, "上游账户额度或余额已耗尽"},
		{"仅供应商停用", provider.EmptyCandidateBreakdown{OffersTotal: 3, BlockedByProviderDisabled: 3}, provider.ClientCauseDisabled, "已被网关管理员停用"},
		{"凭据人工停用", provider.EmptyCandidateBreakdown{OffersTotal: 2, BlockedByCredentialManualDisabled: 1}, provider.ClientCauseDisabled, "已被网关管理员停用"},
		{"凭据缺失", provider.EmptyCandidateBreakdown{OffersTotal: 2, BlockedByCredentialMissing: 1}, provider.ClientCauseCredentialMissing, "凭据记录缺失"},
		{"其它租户", provider.EmptyCandidateBreakdown{OffersTotal: 2, BlockedByProviderTenant: 1}, provider.ClientCauseOtherTenant, "供应商属于其它租户"},
		{"未登记", provider.EmptyCandidateBreakdown{}, provider.ClientCauseNotRegistered, "模型未登记"},
	}
	ctx := i18n.WithLocale(context.Background(), i18n.ZhCN)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &ChatHandler{provider: &causeResolver{breakdown: tc.b}}
			got := h.diagnoseNoCandidateCause(ctx, "m", "default")
			if got.empty() || got.causeToken() != tc.cause {
				t.Fatalf("cause = %q (%v), want %q", got.causeToken(), got.Causes, tc.cause)
			}
			if !strings.Contains(got.Message, tc.contains) {
				t.Errorf("文案缺少 %q：%s", tc.contains, got.Message)
			}
		})
	}
}

// TestDiagnoseNoCandidateCause_FallsBackWhenUnexplained —— 诊断是改进，不是
// 新的失败方式：任何一步不成立都必须退回通用文案，**绝不编一个原因**。
func TestDiagnoseNoCandidateCause_FallsBackWhenUnexplained(t *testing.T) {
	cases := []struct {
		name string
		h    *ChatHandler
	}{
		{"handler 未接 resolver", &ChatHandler{}},
		{"resolver 不具备诊断能力（旧替身）", &ChatHandler{provider: resolverBase{}}},
		{"诊断查询出错", &ChatHandler{provider: &causeResolver{err: errors.New("db down")}}},
		{"状态闸全开（解释不了这次空）", &ChatHandler{provider: &causeResolver{breakdown: provider.EmptyCandidateBreakdown{OffersTotal: 5}}}},
	}
	const model = "claude-sonnet-5-5"
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.h.diagnoseNoCandidateCause(context.Background(), model, "default")
			if !got.empty() {
				t.Fatalf("解释不了时必须返回空归因，实际 %+v", got)
			}
			if got.Message != "" {
				t.Errorf("空归因不得带文案：%q", got.Message)
			}
			if got.extras() != nil {
				t.Errorf("空归因不得产生旁键：%v", got.extras())
			}
			if want := "No available provider for model '" + model + "'"; got.logMessage(model) != want {
				t.Errorf("空归因的日志句子必须是既有前缀：%q", got.logMessage(model))
			}
		})
	}
}

func decodeErrorObject(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body struct {
		Error map[string]any `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v (body=%s)", err, w.Body.String())
	}
	return body.Error
}

// TestWriteNoCandidateWithCause_Envelopes —— 三个分支 × 两个协议族：有归因
// 时文案与旁键到位；无归因时信封与改动前一致（不多任何键）。
func TestWriteNoCandidateWithCause_Envelopes(t *testing.T) {
	cause := noCandidateCause{
		Causes:  []string{provider.ClientCauseQuota, provider.ClientCauseDisabled},
		Message: "MSG-TRUTHFUL",
	}
	alts := ModelAlternativesResult{RequestedModel: "m", Alternatives: []ModelAlternative{{Model: "other-model"}}}
	type arm struct {
		name string
		path string
		alts ModelAlternativesResult
	}
	arms := []arm{
		{"openai 无替代", "/v1/chat/completions", ModelAlternativesResult{}},
		{"openai 带替代", "/v1/chat/completions", alts},
		{"anthropic 无替代", "/v1/messages", ModelAlternativesResult{}},
		{"anthropic 带替代", "/v1/messages", alts},
	}
	for _, a := range arms {
		t.Run(a.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, a.path, nil)
			w := httptest.NewRecorder()
			writeNoCandidateWithAlternatives(context.Background(), w, r, "req-c", "m", a.alts, cause)
			if w.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503", w.Code)
			}
			errObj := decodeErrorObject(t, w)
			if errObj["message"] != "MSG-TRUTHFUL" {
				t.Errorf("message = %v, want the truthful sentence", errObj["message"])
			}
			if errObj["cause"] != "upstream_quota_exhausted+provider_disabled" {
				t.Errorf("cause = %v", errObj["cause"])
			}
			if errObj["code"] != "no_candidate" {
				t.Errorf("code = %v, want no_candidate（Anthropic 面也要有机器可读的错误码）", errObj["code"])
			}
			if errObj["retryable"] != false {
				t.Errorf("retryable = %v, want false", errObj["retryable"])
			}
			anthropic := a.path == "/v1/messages"
			if anthropic && errObj["type"] != "overloaded_error" {
				t.Errorf("anthropic error.type = %v（本轮不改类型契约）", errObj["type"])
			}
			if !anthropic && (errObj["code"] != "no_candidate" || errObj["kind"] != "no_candidate") {
				t.Errorf("openai code/kind = %v/%v", errObj["code"], errObj["kind"])
			}
			_, hasAlts := errObj["alternatives"]
			if hasAlts != (len(a.alts.Alternatives) > 0) {
				t.Errorf("alternatives present = %v, want %v", hasAlts, len(a.alts.Alternatives) > 0)
			}
		})
	}

	// 无归因：两个协议族都不得出现 cause / retryable，文案是通用句。
	for _, path := range []string{"/v1/chat/completions", "/v1/messages"} {
		r := httptest.NewRequest(http.MethodPost, path, nil)
		w := httptest.NewRecorder()
		writeNoCandidateWithAlternatives(context.Background(), w, r, "req-legacy", "m", ModelAlternativesResult{}, noCandidateCause{})
		errObj := decodeErrorObject(t, w)
		for k := range errObj {
			if k == "cause" || k == "retryable" {
				t.Errorf("%s: 无归因时不得出现旁键 %q：%v", path, k, errObj)
			}
		}
		if errObj["message"] != "No available provider for model 'm'" {
			t.Errorf("%s: 无归因文案漂移：%v", path, errObj["message"])
		}
	}
}

// TestNoCandidateCauseWiringGuard —— 文本守卫：三个协议入口都必须在通用
// no_candidate 响应**之前**调用归因。调用被删/被挪到 fallback 之后 ⇒ 当场红
// 并指出文件。（沿用模态不匹配轮的接线守卫形态。）
func TestNoCandidateCauseWiringGuard(t *testing.T) {
	sites := []struct{ file, callNeedle, fallbackNeedle string }{
		{"handler.go", "h.diagnoseNoCandidateCause(", "writeNoCandidateWithAlternatives("},
		{"messages.go", "h.chatHandler.diagnoseNoCandidateCause(", `writeAnthropicError(w, http.StatusServiceUnavailable, "overloaded_error"`},
		{"responses.go", "h.chatHandler.diagnoseNoCandidateCause(", `writeResponsesError(w, http.StatusServiceUnavailable`},
	}
	for _, s := range sites {
		raw, err := os.ReadFile(s.file)
		if err != nil {
			t.Fatalf("%s unreadable: %v", s.file, err)
		}
		text := string(raw)
		callIdx := strings.Index(text, s.callNeedle)
		if callIdx < 0 {
			t.Fatalf("%s: 归因调用 %q 缺失 —— 零候选出口悄悄退回了笼统的 503", s.file, s.callNeedle)
		}
		if idx := strings.Index(text[callIdx:], s.fallbackNeedle); idx < 0 {
			t.Fatalf("%s: 归因调用之后找不到 fallback %q", s.file, s.fallbackNeedle)
		}
	}
}

// TestNoCandidateCause_BehaviouralWiring —— 行为守卫：走三个协议的**真实
// 入口管线**，断言归因真的回答了请求（控制流可达；文本守卫看不见「调用
// 被挪进死分支 / 之前被提前 return」）。
func TestNoCandidateCause_BehaviouralWiring(t *testing.T) {
	newChat := func() *ChatHandler {
		return &ChatHandler{
			provider: &causeResolver{breakdown: sonnet55Breakdown()},
			auditor:  &audit.LogSink{},
		}
	}
	assertTruthful := func(t *testing.T, w *httptest.ResponseRecorder) {
		t.Helper()
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503\nbody: %s", w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "upstream account quota or balance exhausted") {
			t.Errorf("响应没有说出真实原因\nbody: %s", w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "Retrying the same request will not help") {
			t.Errorf("响应没有说明重试无用\nbody: %s", w.Body.String())
		}
		errObj := decodeErrorObject(t, w)
		if errObj["cause"] == nil || errObj["retryable"] != false || errObj["code"] != "no_candidate" {
			t.Errorf("旁键缺失：cause=%v retryable=%v code=%v", errObj["cause"], errObj["retryable"], errObj["code"])
		}
	}

	t.Run("openai_chat_completions", func(t *testing.T) {
		h := newChat()
		body := `{"model":"claude-sonnet-5-5","messages":[{"role":"user","content":"hi"}]}`
		r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.serveWithExecutor(w, r, h.NewRequestLogContext(r, "req-nc-openai", time.Now()))
		assertTruthful(t, w)
	})
	t.Run("anthropic_messages", func(t *testing.T) {
		h := NewMessagesHandler(newChat())
		body := `{"model":"claude-sonnet-5-5","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`
		r := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.serveHTTPInner(w, r)
		assertTruthful(t, w)
	})
	t.Run("openai_responses", func(t *testing.T) {
		h := NewResponsesHandler(newChat())
		body := `{"model":"claude-sonnet-5-5","input":"hi"}`
		r := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.serveHTTPInner(w, r)
		assertTruthful(t, w)
	})
}

// TestNoCandidateCause_BehaviouralWiring_LegacyWithoutDiagnosis —— 阴性对照：
// resolver 不具备诊断能力时，三个协议面仍是既有的通用 503（不多旁键）。
// 同时钉住「行为通道真的走完了管线」。
func TestNoCandidateCause_BehaviouralWiring_LegacyWithoutDiagnosis(t *testing.T) {
	newChat := func() *ChatHandler {
		return &ChatHandler{provider: resolverBase{}, auditor: &audit.LogSink{}}
	}
	assertLegacy := func(t *testing.T, w *httptest.ResponseRecorder) {
		t.Helper()
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503\nbody: %s", w.Code, w.Body.String())
		}
		errObj := decodeErrorObject(t, w)
		if _, has := errObj["cause"]; has {
			t.Errorf("无诊断能力时不得编造 cause：%v", errObj)
		}
	}
	t.Run("openai_chat_completions", func(t *testing.T) {
		h := newChat()
		r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.serveWithExecutor(w, r, h.NewRequestLogContext(r, "req-nc-legacy", time.Now()))
		assertLegacy(t, w)
	})
	t.Run("anthropic_messages", func(t *testing.T) {
		h := NewMessagesHandler(newChat())
		r := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"m","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.serveHTTPInner(w, r)
		assertLegacy(t, w)
	})
}
