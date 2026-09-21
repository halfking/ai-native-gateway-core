package executors

// paramledger_integration_test.go — 参数协商闭环端到端（2026-09-22）：
// 客户端 reasoning effort "x-high" → grok-4（无 xhigh 档）出站降档 high →
// 上游响应回显 effort:"high" → 写回客户端前还原为 "x-high"。
// 覆盖 native responses 非流式主链路与账本记账。

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/credential"
	"github.com/kaixuan/llm-gateway-go/internal/paramledger"
	"github.com/kaixuan/llm-gateway-go/pool"
	"github.com/kaixuan/llm-gateway-go/provider"
)

func newParamLedgerExecutor() (*Executor, *paramledger.Ledger) {
	exec := NewExecutor(
		NewRouter(NewStickyCache(), credential.NewLimiter()),
		credential.NewManager(), credential.NewLimiter(),
		pool.NewPoolManager(nil), nil,
		func(chunk []byte, isStream bool) []byte { return chunk }, nil, nil,
	)
	ledger := paramledger.New(nil)
	exec.ParamLedger = ledger
	return exec, ledger
}

// Case 1: 非流式 native responses——x-high 降档 high 出站 + 回显还原。
func TestParamLedgerNativeResponsesEffortRoundTrip(t *testing.T) {
	var sawSentEffort string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if i := strings.Index(string(body), `"effort":"`); i >= 0 {
			rest := string(body)[i+len(`"effort":"`):]
			if j := strings.Index(rest, `"`); j >= 0 {
				sawSentEffort = rest[:j]
			}
		}
		w.Header().Set("Content-Type", "application/json")
		// 回显上游实际收到的 effort（降档后的 high）。
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","model":"grok-4","status":"completed","reasoning":{"effort":"high","summary":"auto"},"output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"hi"}]}]}`))
	}))
	defer upstream.Close()

	exec, ledger := newParamLedgerExecutor()
	cand := provider.Candidate{
		CredentialID: 101, ProviderID: 7, BaseURL: upstream.URL,
		Protocol: "openai-responses", CatalogCode: "xai",
		SupportsNativeResponses: true,
		RawModel:                "grok-4", Routable: true, LifecycleStatus: "active",
		AvailabilityState: "ready", QuotaState: "ok", CircuitState: "closed",
		APIKey: "sk-test", Weight: 100,
	}
	rec := httptest.NewRecorder()
	_, err := exec.executeOpenAI(&ExecParams{
		W:                  rec,
		R:                  httptest.NewRequest(http.MethodPost, "/v1/responses", nil),
		BodyBytes:          []byte(`{"model":"grok-4","messages":[],"reasoning_effort":"x-high"}`),
		ResponsesBodyBytes: []byte(`{"model":"grok-4","input":[{"role":"user","content":"hi"}],"reasoning":{"effort":"x-high"}}`),
		RequestID:          "req-ledger-1",
		ClientModel:        "grok-4", ClientProtocol: "openai-responses",
	}, cand, 0, time.Now(), nil)
	if err != nil {
		t.Fatalf("executeOpenAI: %v", err)
	}
	// 出站被降档为 high（grok-4 能力表无 xhigh）。
	if got := sawSentEffort; got != "high" {
		t.Fatalf("upstream saw effort = %q, want high (clamped from x-high)", got)
	}
	// 账本记录了 clamp。
	entry := ledger.Lookup("req-ledger-1")
	if entry == nil || len(entry.Adjustments) == 0 {
		t.Fatalf("ledger entry missing: %+v", entry)
	}
	foundClamp := false
	for _, adj := range entry.Adjustments {
		if adj.Field == "reasoning.effort" && adj.Original == "x-high" && adj.Sent == "high" && adj.Action == paramledger.ActionClamp {
			foundClamp = true
		}
	}
	if !foundClamp {
		t.Fatalf("clamp adjustment missing: %+v", entry.Adjustments)
	}
	// 客户端收到的回显被还原为 x-high。
	if !strings.Contains(rec.Body.String(), `"effort":"x-high"`) {
		t.Fatalf("client body missing restored effort: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `"effort":"high"`) {
		t.Fatalf("client body still shows clamped effort: %s", rec.Body.String())
	}
}

// Case 2: chat 形态出站（grok 候选走 chat/completions）——顶层
// reasoning_effort 降档 + 记账（chat 响应不回显，无需还原）。
func TestParamLedgerChatEffortClampRecorded(t *testing.T) {
	var sawSentEffort string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if i := strings.Index(string(body), `"reasoning_effort":"`); i >= 0 {
			rest := string(body)[i+len(`"reasoning_effort":"`):]
			if j := strings.Index(rest, `"`); j >= 0 {
				sawSentEffort = rest[:j]
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"c1","object":"chat.completion","model":"grok-4","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer upstream.Close()

	exec, ledger := newParamLedgerExecutor()
	cand := provider.Candidate{
		CredentialID: 101, ProviderID: 7, BaseURL: upstream.URL,
		Protocol: "openai-completions", CatalogCode: "xai",
		RawModel: "grok-4", Routable: true, LifecycleStatus: "active",
		AvailabilityState: "ready", QuotaState: "ok", CircuitState: "closed",
		APIKey: "sk-test", Weight: 100,
	}
	rec := httptest.NewRecorder()
	_, err := exec.executeOpenAI(&ExecParams{
		W:           rec,
		R:           httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
		BodyBytes:   []byte(`{"model":"grok-4","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"x-high"}`),
		RequestID:   "req-ledger-2",
		ClientModel: "grok-4", ClientProtocol: "openai-completions",
	}, cand, 0, time.Now(), nil)
	if err != nil {
		t.Fatalf("executeOpenAI: %v", err)
	}
	if got := sawSentEffort; got != "high" {
		t.Fatalf("upstream saw reasoning_effort = %q, want high", got)
	}
	entry := ledger.Lookup("req-ledger-2")
	if entry == nil {
		t.Fatal("ledger entry missing")
	}
	found := false
	for _, adj := range entry.Adjustments {
		if adj.Field == "reasoning_effort" && adj.Original == "x-high" && adj.Action == paramledger.ActionClamp {
			found = true
		}
	}
	if !found {
		t.Fatalf("clamp adjustment missing: %+v", entry.Adjustments)
	}
}
