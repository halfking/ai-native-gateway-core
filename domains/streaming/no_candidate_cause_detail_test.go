package streaming

// no_candidate_cause_detail_test.go —— 「候选为空」的归因要**进请求日志明细码**
// 的判据测试（2026-11-26，211 事故复盘·「系统改了错误原因后看不到详情」）。
//
// 事故形状：归因令牌（provider.ClientCause*）此前只进了**响应体**
// （extras 的 cause 旁键）⇒ 落库路径拿不到 ⇒ mapGatewayErrorToDetail(
// "no_candidate") 恒返回泛化码 "gw_no_candidate" ⇒ 211 真库 24h 内 164 笔
// no_candidate 行**全部同码**（openai-responses 66 / openai-chat 55 /
// anthropic-messages 43）⇒ 运维既分不出「上游额度耗尽」与「供应商被停用」，
// 也点不出主因。
//
// 三层一起钉：
//  1. 取值口径：只取**主因**（单值列可分组/可精确过滤），无归因时为空串
//     ⇒ 落库路径回到既有的泛化码；
//  2. 行为守卫：三个协议面各走**真实管线** ⇒ 落库行的 failure_detail_code
//     真的带上了归因令牌（文本守卫看不见「调用被挪进死分支」）；
//  3. 不变量：error_kind / failure_stage / 客户端状态码与文案**零变化**
//     ⇒ 只覆盖明细码，不牵动别的口径。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// TestNoCandidateCause_DetailCode_TakesPrimaryOnly —— 取值口径：明细码是
// **单值**列 ⇒ 取主因（命中 offer 数最多的那道闸）；无归因 ⇒ 空串
// （⇒ 落库路径保持既有的泛化码 gw_no_candidate，不编造原因）。
func TestNoCandidateCause_DetailCode_TakesPrimaryOnly(t *testing.T) {
	h := &ChatHandler{provider: &causeResolver{breakdown: sonnet55Breakdown()}}
	got := h.diagnoseNoCandidateCause(context.Background(), "claude-sonnet-5-5", "default")
	if got.empty() {
		t.Fatal("归因为空：夹具形态必须给出原因")
	}
	// 主因 = 配额耗尽（命中 8 条 offer，胜过停用类的 5 条）。
	if want := provider.ClientCauseQuota; got.detailCode() != want {
		t.Errorf("detailCode = %q, want %q（只取主因）", got.detailCode(), want)
	}
	// ★ 但**完整**令牌（含次因）不丢：causeToken 仍是两个令牌。
	if want := "upstream_quota_exhausted+provider_disabled"; got.causeToken() != want {
		t.Errorf("causeToken = %q, want %q（次因不丢）", got.causeToken(), want)
	}
	// 无归因 ⇒ 空串（调用方据此**不**覆盖明细码）。
	for _, empty := range []noCandidateCause{
		{},
		{Causes: nil, Message: ""},
	} {
		if code := empty.detailCode(); code != "" {
			t.Errorf("无归因时 detailCode 必须为空串，实际 %q", code)
		}
	}
}

// TestNoCandidateCause_FailureDetailCode_AllFaces —— 行为守卫：三个协议面各走
// 真实管线，落库行的 failure_detail_code 必须带上归因令牌；且
// error_kind / failure_stage / 客户端状态码**零变化**。
func TestNoCandidateCause_FailureDetailCode_AllFaces(t *testing.T) {
	newChat := func() *ChatHandler {
		return &ChatHandler{
			provider: &causeResolver{breakdown: sonnet55Breakdown()},
			auditor:  &audit.LogSink{},
		}
	}
	// 抓落库行：按 error_kind 定位 no_candidate 那一条（不假设它是第几条）。
	findNoCandidate := func(entries []*telemetry.RequestLogEntry) *telemetry.RequestLogEntry {
		for _, e := range entries {
			if e.ErrorKind != nil && *e.ErrorKind == "no_candidate" {
				return e
			}
		}
		return nil
	}
	assertAttributed := func(t *testing.T, entries []*telemetry.RequestLogEntry) {
		t.Helper()
		e := findNoCandidate(entries)
		if e == nil {
			t.Fatalf("没有落库的 no_candidate 行（entries=%d）", len(entries))
		}
		if e.FailureDetailCode == nil {
			t.Fatal("failure_detail_code 为空")
		}
		if got := *e.FailureDetailCode; got != provider.ClientCauseQuota {
			t.Errorf("failure_detail_code = %q, want %q（归因令牌要进明细码）", got, provider.ClientCauseQuota)
		}
		// ★ 不变量：只有明细码变。
		if e.FailureStage == nil || *e.FailureStage != "gateway" {
			t.Errorf("failure_stage = %v, want gateway（不变）", e.FailureStage)
		}
	}
	runFace := func(t *testing.T, face string) {
		t.Helper()
		h := newChat()
		var entries []*telemetry.RequestLogEntry
		h.requestLogHook = func(entry *telemetry.RequestLogEntry) {
			entries = append(entries, entry)
		}
		var w *httptest.ResponseRecorder
		switch face {
		case "openai-chat":
			body := `{"model":"claude-sonnet-5-5","messages":[{"role":"user","content":"hi"}]}`
			r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
			w = httptest.NewRecorder()
			h.serveWithExecutor(w, r, h.NewRequestLogContext(r, "req-detail-chat", time.Now()))
		case "anthropic-messages":
			body := `{"model":"claude-sonnet-5-5","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`
			r := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
			w = httptest.NewRecorder()
			NewMessagesHandler(h).serveHTTPInner(w, r)
		case "openai-responses":
			body := `{"model":"claude-sonnet-5-5","input":"hi"}`
			r := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
			w = httptest.NewRecorder()
			NewResponsesHandler(h).serveHTTPInner(w, r)
		default:
			t.Fatalf("未知协议面 %q", face)
		}
		// 客户端可见面**零变化**：主因=配额耗尽 ⇒ 仍是 429。
		if w.Code != http.StatusTooManyRequests {
			t.Errorf("status = %d, want 429（客户端语义不变）\nbody: %s", w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "upstream account quota or balance exhausted") {
			t.Errorf("客户端文案漂移：%s", w.Body.String())
		}
		assertAttributed(t, entries)
	}
	for _, face := range []string{"openai-chat", "anthropic-messages", "openai-responses"} {
		t.Run(face, func(t *testing.T) { runFace(t, face) })
	}

	// 阴性对照：resolver 不具备诊断能力 ⇒ 归因解释不了 ⇒ 明细码保持既有的
	// 泛化码 gw_no_candidate（不编原因）。
	t.Run("no-diagnosis-keeps-generic-code", func(t *testing.T) {
		h := &ChatHandler{provider: resolverBase{}, auditor: &audit.LogSink{}}
		var entries []*telemetry.RequestLogEntry
		h.requestLogHook = func(entry *telemetry.RequestLogEntry) {
			entries = append(entries, entry)
		}
		body := `{"model":"claude-sonnet-5-5","messages":[{"role":"user","content":"hi"}]}`
		r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.serveWithExecutor(w, r, h.NewRequestLogContext(r, "req-detail-nodiag", time.Now()))
		e := findNoCandidate(entries)
		if e == nil {
			t.Fatalf("没有落库的 no_candidate 行（entries=%d）", len(entries))
		}
		if e.FailureDetailCode == nil || *e.FailureDetailCode != "gw_no_candidate" {
			t.Errorf("failure_detail_code = %v, want gw_no_candidate（无归因时保持既有码）", e.FailureDetailCode)
		}
	})
}
