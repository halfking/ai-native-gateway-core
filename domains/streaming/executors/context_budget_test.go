package executors

import (
	"bytes"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/provider"
)

func contextBudgetCandidate(window int) provider.Candidate {
	return provider.Candidate{CredentialID: 7, ProviderID: 3, RawModel: "test-model", ContextWindow: &window}
}

func contextBudgetBody(content string) []byte {
	return []byte(`{"model":"test-model","messages":[{"role":"user","content":"` + content + `"}]}`)
}

func TestContextBudgetRejectsBeforePhysicalAttempt(t *testing.T) {
	e := &Executor{EnforceContextBudget: true}
	params := &ExecParams{R: httptest.NewRequest("POST", "/v1/chat/completions", nil), UpstreamAttempts: NewUpstreamAttemptBudget(3)}
	err := e.beginUpstreamAttempt(params, contextBudgetCandidate(128000), "openai-completions", contextBudgetBody(strings.Repeat("x", 70000)))
	if !IsContextBudgetError(err) || params.UpstreamAttempts.Used() != 0 {
		t.Fatalf("got err=%v physical attempts=%d", err, params.UpstreamAttempts.Used())
	}
	if got := classifyExecError(err); got != errorsx.KindRequestRejected {
		t.Fatalf("local budget kind=%q, want request-side rejection", got)
	}
}

func TestContextBudgetFitStartsFromEachImmutableCandidateSource(t *testing.T) {
	source := contextBudgetBody(strings.Repeat("x", 120000))
	before := append([]byte(nil), source...)
	var limits []int
	e := &Executor{EnforceContextBudget: true, FitContext: func(body []byte, limit int) ([]byte, error) {
		if !bytes.Equal(body, before) {
			t.Fatal("candidate fit received another candidate's compacted body")
		}
		limits = append(limits, limit)
		// A hook may reuse its input; it must never alter shared request bytes.
		body[0] = ' '
		return contextBudgetBody("important task and value=42"), nil
	}}
	for _, window := range []int{128000, 64000} {
		out, err := e.fitCandidateContextBudget(nil, contextBudgetCandidate(window), "openai-completions", source)
		if err != nil || !bytes.Contains(out, []byte("value=42")) {
			t.Fatalf("window=%d body=%q err=%v", window, out, err)
		}
	}
	if len(limits) != 2 || limits[1] >= limits[0] || !bytes.Equal(source, before) {
		t.Fatalf("limits=%v source changed=%t", limits, !bytes.Equal(source, before))
	}
}

func TestContextBudgetUnknownWindowFallsBackToGlobalCaps(t *testing.T) {
	// 2026-11-10 改判（opus-5-5 422 事故）：未知窗口不再硬拒——生产 ~90% 模型
	// context_window=NULL，旧 strict 行为把数据缺口放大成 pre-dispatch 全平台
	// 拒绝。未知窗口回落包级全局保守上限（MaxInputTokens / DefaultMaxJSONBytes），
	// 已知窗口的精确执法不变（下方 known-window 断言）。「本地预算错误必须是
	// 本地终态且映射保真」的契约保留，触发器从 unknown-window 换成
	// known-tiny-window + 超限体。
	e := &Executor{EnforceContextBudget: true}
	unknown := provider.Candidate{CredentialID: 5, RawModel: "unknown"}
	if err := e.checkCandidateContextBudget(nil, unknown, "chat", contextBudgetBody("hello")); err != nil {
		t.Fatalf("unknown window with in-cap body must pass under global caps: %v", err)
	}
	// Known tiny window + oversized body still yields a local budget error, and
	// the dispatch mapping must preserve it as a context-length final error.
	tiny := contextBudgetCandidate(64)
	err := e.checkCandidateContextBudget(nil, tiny, "chat", contextBudgetBody(strings.Repeat("x", 4096)))
	if !IsContextBudgetError(err) {
		t.Fatalf("known tiny window must still reject oversized body: %v", err)
	}
	mapped := dispatchErrToExecuteError(errors.Join(errors.New("exhausted"), err))
	if !IsContextBudgetError(mapped) || mapped.LastKind != errorsx.KindContextLength {
		t.Fatalf("lost local budget error: %#v", mapped)
	}
	legacy := &Executor{}
	if err := legacy.checkCandidateContextBudget(nil, tiny, "chat", contextBudgetBody(strings.Repeat("x", 4096))); err != nil {
		t.Fatalf("legacy zero-value executor must remain compatible: %v", err)
	}
}

func TestContextBudgetToolsCountAnd500KCeiling(t *testing.T) {
	e := &Executor{EnforceContextBudget: true}
	for _, body := range [][]byte{
		[]byte(`{"model":"test","messages":[],"tools":[{"type":"function","function":{"name":"f","description":"` + strings.Repeat("x", 70000) + `"}}]}`),
		contextBudgetBody(strings.Repeat("x", 510000)),
	} {
		window := 128000
		if len(body) > 500000 {
			window = 2000000
		}
		if err := e.checkCandidateContextBudget(nil, contextBudgetCandidate(window), "chat", body); !IsContextBudgetError(err) {
			t.Fatalf("oversized tools/input accepted: %v", err)
		}
	}
}

func TestContextBudgetKnownTinyWindowForwardIsUnmeteredLocalRejection(t *testing.T) {
	// 2026-11-10 改判：触发器从 unknown-window（现回落全局上限、放行）换成
	// known-tiny-window + 超限体，并直接钉在 beginUpstreamAttempt ——
	// 核心不变量不变：本地预算拒绝是 request_rejected 形态、不消耗物理
	// 尝试、不伪装成供应商流量。（forwardForDispatch 全链需要 Router 等
	// 依赖；body 级预算检查位于 beginUpstreamAttempt，见
	// TestContextBudgetRejectsBeforePhysicalAttempt 的同一断言法。）
	e := &Executor{EnforceContextBudget: true}
	params := &ExecParams{R: httptest.NewRequest("POST", "/v1/chat/completions", nil), UpstreamAttempts: NewUpstreamAttemptBudget(3)}
	err := e.beginUpstreamAttempt(params, contextBudgetCandidate(64), "openai-completions", contextBudgetBody(strings.Repeat("x", 4096)))
	if !IsContextBudgetError(err) || params.UpstreamAttempts.Used() != 0 {
		t.Fatalf("got err=%v physical attempts=%d", err, params.UpstreamAttempts.Used())
	}
	if got := classifyExecError(err); got != errorsx.KindRequestRejected {
		t.Fatalf("local budget kind=%q, want request-side rejection", got)
	}
}

func TestContextBudgetCannotFitAwayUnknownMediaAccounting(t *testing.T) {
	e := &Executor{EnforceContextBudget: true, FitContext: func([]byte, int) ([]byte, error) {
		t.Fatal("an uncountable input must not invoke lossy fitting")
		return nil, nil
	}}
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.invalid/image"}}]}]}`)
	if _, err := e.fitCandidateContextBudget(nil, contextBudgetCandidate(128000), "chat", body); !IsContextBudgetError(err) {
		t.Fatalf("unknown media bound accepted: %v", err)
	}
}
