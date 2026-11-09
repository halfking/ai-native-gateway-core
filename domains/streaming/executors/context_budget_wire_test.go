package executors

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/contextbudget"
)

func TestContextBudgetFinalWirePaths(t *testing.T) {
	for _, protocol := range []string{"openai-completions", "anthropic-messages", "gemini-generate", "ollama-native"} {
		t.Run(protocol, func(t *testing.T) {
			var received []byte
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				received, _ = io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "application/json")
				reply := `{"id":"chat","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":1}}`
				switch protocol {
				case "anthropic-messages":
					reply = `{"id":"msg","type":"message","role":"assistant","model":"test-model","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":1}}`
				case "gemini-generate":
					reply = `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":1}}`
				case "ollama-native":
					reply = `{"model":"test-model","message":{"role":"assistant","content":"ok"},"done":true,"done_reason":"stop","prompt_eval_count":5,"eval_count":1}`
				}
				_, _ = io.WriteString(w, reply)
			}))
			defer srv.Close()
			source := contextBudgetBody(strings.Repeat("x", 330000))
			clientProtocol := "openai-completions"
			if protocol == "anthropic-messages" {
				// anthropic-messages 官方规范要求 max_tokens；同协议格式门按
				// 官方形状校验，夹具补齐（2026-11-10）。
				clientProtocol = protocol
				source = []byte(`{"model":"test-model","max_tokens":512,"messages":[{"role":"user","content":"` + strings.Repeat("x", 330000) + `"}]}`)
			}
			before := append([]byte(nil), source...)
			params := &ExecParams{R: httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(source)), W: httptest.NewRecorder(), BodyBytes: source,
				ClientProtocol: clientProtocol, ClientModel: "test-model", OutboundModel: "test-model", Model: "test-model", RequestID: "budget-wire"}
			cand := contextBudgetCandidate(128000)
			cand.Protocol, cand.BaseURL = protocol, srv.URL
			e := newTestExecutor()
			e.EnforceContextBudget = true
			var fitted bool
			e.FitContext = func(_ []byte, _ int) ([]byte, error) {
				fitted = true
				if protocol == "gemini-generate" {
					return []byte(`{"contents":[{"role":"user","parts":[{"text":"value=42"}]}]}`), nil
				}
				return []byte(`{"model":"test-model","max_tokens":512,"messages":[{"role":"user","content":"value=42"}]}`), nil
			}
			var result *ExecuteResult
			var err error
			switch protocol {
			case "anthropic-messages":
				result, err = e.executeAnthropic(params, cand, 0, time.Now(), nil)
			case "gemini-generate":
				result, err = e.executeGemini(params, cand, 0, time.Now(), nil)
			case "ollama-native":
				result, err = e.executeOllama(params, cand, 0, time.Now(), nil)
			default:
				result, err = e.executeOpenAI(params, cand, 0, time.Now(), nil)
			}
			if err != nil || result == nil {
				t.Fatalf("execution: result=%v err=%v", result, err)
			}
			if !fitted || !bytes.Contains(received, []byte("value=42")) || !bytes.Equal(source, before) {
				t.Fatalf("fitted=%t received=%q sourceChanged=%t", fitted, received, !bytes.Equal(source, before))
			}
			if _, err := contextbudget.Check(received, contextbudget.Spec{ContextWindow: 128000}); err != nil {
				t.Fatalf("actual upstream body escaped budget: %v", err)
			}
			if !bytes.Equal(received, result.RequestBody) {
				t.Fatal("recorded request body differs from the body actually sent")
			}
		})
	}
}

func TestContextBudgetContinuationCannotAddUnbudgetedPrefill(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer srv.Close()
	req, _ := http.NewRequestWithContext(context.Background(), "POST", srv.URL, strings.NewReader("{}"))
	params := &ExecParams{R: httptest.NewRequest("POST", "/v1/chat/completions", nil), UpstreamAttempts: NewUpstreamAttemptBudget(3)}
	s := &chatStreamContinuation{e: &Executor{EnforceContextBudget: true}, cand: contextBudgetCandidate(128000), params: params,
		body: contextBudgetBody("hello"), mode: "partial", req: req, client: srv.Client()}
	_, err := s.invoke(strings.Repeat("x", 330000))
	if !IsContextBudgetError(err) || calls.Load() != 0 || params.UpstreamAttempts.Used() != 0 {
		t.Fatalf("err=%v calls=%d physicalAttempts=%d", err, calls.Load(), params.UpstreamAttempts.Used())
	}
}
