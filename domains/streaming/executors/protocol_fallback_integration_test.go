package executors

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/reqprobe"
)

// TestExecuteOpenAIProtocolFallbackRetriesOnBaseProtocol — A3 闸门 2 集成腿
// （2026-11-29 L157 轮补，配合守门 4 的收口）：
//
//   - 负腿：TestExecuteOpenAIReqProbeModeMismatchRecordsWithoutFallback 钉住
//     「非流式 openai-responses 客户端 404 → calls=1」（守门 4：线格式切换
//     对它不安全，2026-11-02 盲重试教训）。
//   - 正腿（本测试）：非 responses 客户端（openai-completions）+ 候选协议
//     anthropic-messages 上游 404 unsupported ⇒ A3 协议回退保留：负向记忆
//     落一条 + 基协议重试 ⇒ calls=2。守门 4 只收窄 responses 面，不砍 A3。
func TestExecuteOpenAIProtocolFallbackRetriesOnBaseProtocol(t *testing.T) {
	var calls atomic.Int32
	var secondPath atomic.Value
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			secondPath.Store(r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"message":"endpoint does not exist"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-ok","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer upstream.Close()

	store := reqprobe.NewMemoryStore()
	exec, coord := newReqProbeExecutor(store)
	defer coord.Close()

	cand := reqProbeTestCandidate(upstream.URL)
	cand.Protocol = "anthropic-messages"

	_, err := exec.executeOpenAI(&ExecParams{
		W:           httptest.NewRecorder(),
		R:           httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
		BodyBytes:   []byte(`{"model":"grok-4.6","messages":[]}`),
		ClientModel: "grok-4.6", ClientProtocol: "openai-completions",
	}, cand, 0, time.Now(), nil)
	if err != nil {
		t.Fatalf("protocol fallback retry should succeed on second call: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("calls = %d, want 2 (A3 协议回退：非 responses 客户端 404 unsupported ⇒ 基协议重试)", got)
	}
	if p, _ := secondPath.Load().(string); p != "/v1/messages" {
		t.Fatalf("first call path = %q, want /v1/messages (候选协议 anthropic-messages)", p)
	}
}
