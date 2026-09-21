package executors

// reqprobe_integration_test.go — executeOpenAI 请求侧探测的端到端验证
// （假上游 + 内存 reqprobe 存储）：
//  1. 参数剔除重试：grok 风格 "Invalid value for 'reasoning_effort'" 400
//     → 剔除参数免费重试 → 200；store 记录 param_rejected + recovered。
//  2. 探测重试后仍失败：始终 400 → 终端记录 recovered=false。
//  3. 前置学习：复用案例 1 的 store，第二次请求应在首发出站前就剔除
//     （上游只收到不带 reasoning_effort 的 body，且不返回 400）。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/credential"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"
	"github.com/kaixuan/llm-gateway-go/internal/reqprobe"
	"github.com/kaixuan/llm-gateway-go/pool"
	"github.com/kaixuan/llm-gateway-go/provider"
)

func newReqProbeExecutor(store reqprobe.Store) (*Executor, *reqprobe.Coordinator) {
	exec := NewExecutor(
		NewRouter(NewStickyCache(), credential.NewLimiter()),
		credential.NewManager(), credential.NewLimiter(),
		pool.NewPoolManager(nil), nil,
		func(chunk []byte, isStream bool) []byte { return chunk }, nil, nil,
	)
	coord := reqprobe.NewCoordinator(store)
	exec.RequestProbe = coord
	return exec, coord
}

func reqProbeTestCandidate(baseURL string) provider.Candidate {
	return provider.Candidate{
		CredentialID: 101, ProviderID: 7, Tier: 1,
		BaseURL: baseURL, Protocol: "openai-completions", CatalogCode: "xai",
		RawModel: "grok-4.6", Weight: 100, BillingMode: "token_plan",
		Routable: true, LifecycleStatus: "active", AvailabilityState: "ready",
		QuotaState: "ok", CircuitState: "closed", APIKey: "sk-test",
	}
}

const reqProbeRejectionBody = `{"error":{"message":"Invalid value for 'reasoning_effort': 'x-high' is not one of ['low','high']","type":"invalid_request_error"}}`

func waitForReqProbeRecord(t *testing.T, store reqprobe.Store, want int) []reqprobe.Record {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		recs, _, err := store.List(context.Background(), reqprobe.Filter{})
		if err == nil && len(recs) >= want {
			return recs
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("reqprobe records not persisted in time (want %d)", want)
	return nil
}

// Case 1: 参数剔除重试成功。
func TestExecuteOpenAIReqProbeParamStripRecovers(t *testing.T) {
	var calls atomic.Int32
	var sawEffortFirstCall atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		hasEffort := strings.Contains(string(body), "reasoning_effort")
		if n == 1 {
			sawEffortFirstCall.Store(hasEffort)
		}
		if hasEffort {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(reqProbeRejectionBody))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"c1","object":"chat.completion","model":"grok-4.6","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer upstream.Close()

	store := reqprobe.NewMemoryStore()
	exec, coord := newReqProbeExecutor(store)
	defer coord.Close()

	_, err := exec.executeOpenAI(&ExecParams{
		W:           httptest.NewRecorder(),
		R:           httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
		BodyBytes:   []byte(`{"model":"grok-4.6","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"x-high"}`),
		ClientModel: "grok-4.6", ClientProtocol: "openai-completions",
	}, reqProbeTestCandidate(upstream.URL), 0, time.Now(), nil)
	if err != nil {
		t.Fatalf("executeOpenAI: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("upstream calls = %d, want 2 (reject + strip retry)", got)
	}
	if !sawEffortFirstCall.Load() {
		t.Fatal("first upstream call should carry reasoning_effort")
	}
	recs := waitForReqProbeRecord(t, store, 1)
	rec := recs[0]
	if rec.Trigger != reqprobe.TriggerParamRejected || rec.Param != "reasoning_effort" {
		t.Fatalf("record = %+v", rec)
	}
	if rec.RecoveredCount != 1 {
		t.Fatalf("recoveredCount = %d, want 1", rec.RecoveredCount)
	}
	if rec.ProviderCode != "xai" || rec.OutboundModel != "grok-4.6" || rec.HTTPStatus != 400 {
		t.Fatalf("record meta = %+v", rec)
	}
}

// Case 2: 探测重试后仍失败 → 终端记录 recovered=false，错误照常上抛。
func TestExecuteOpenAIReqProbeStripFailsStillErrors(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(reqProbeRejectionBody))
	}))
	defer upstream.Close()

	store := reqprobe.NewMemoryStore()
	exec, coord := newReqProbeExecutor(store)
	defer coord.Close()

	_, err := exec.executeOpenAI(&ExecParams{
		W:           httptest.NewRecorder(),
		R:           httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
		BodyBytes:   []byte(`{"model":"grok-4.6","messages":[],"reasoning_effort":"x-high"}`),
		ClientModel: "grok-4.6", ClientProtocol: "openai-completions",
	}, reqProbeTestCandidate(upstream.URL), 0, time.Now(), nil)
	if err == nil {
		t.Fatal("expected error when strip retry also fails")
	}
	// 调用序列：①首发 400 → 剔除重试；②剔除后仍 400（transient 梯子
	// 再盲重试一次，既有行为）；③盲重试仍 400 → 终端记录。
	if got := calls.Load(); got != 3 {
		t.Fatalf("upstream calls = %d, want 3", got)
	}
	recs := waitForReqProbeRecord(t, store, 1)
	if recs[0].RecoveredCount != 0 || recs[0].Occurrences != 1 {
		t.Fatalf("record = %+v", recs[0])
	}
}

// Case 3: 学习规则前置剔除——同 store 的第二个请求首发就不带参数。
func TestExecuteOpenAIReqProbeLearnedStrip(t *testing.T) {
	var calls atomic.Int32
	var firstCallSawEffort atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		hasEffort := strings.Contains(string(body), "reasoning_effort")
		if n == 1 {
			firstCallSawEffort.Store(hasEffort)
			if hasEffort { // 首发带参数 → 拒绝，触发剔除重试并成功
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(reqProbeRejectionBody))
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"c1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer upstream.Close()

	store := reqprobe.NewMemoryStore()
	exec, coord := newReqProbeExecutor(store)
	defer coord.Close()
	cand := reqProbeTestCandidate(upstream.URL)

	newParams := func() *ExecParams {
		return &ExecParams{
			W:           httptest.NewRecorder(),
			R:           httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
			BodyBytes:   []byte(`{"model":"grok-4.6","messages":[],"reasoning_effort":"x-high"}`),
			ClientModel: "grok-4.6", ClientProtocol: "openai-completions",
		}
	}
	// 第一轮：400 → 剔除重试成功（2 calls）。
	if _, err := exec.executeOpenAI(newParams(), cand, 0, time.Now(), nil); err != nil {
		t.Fatalf("first executeOpenAI: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("first round calls = %d, want 2", got)
	}
	if !firstCallSawEffort.Load() {
		t.Fatal("first round first call should carry the param")
	}
	waitForReqProbeRecord(t, store, 1)

	// 第二轮：学习规则应在出站前剔除（首发即干净，总 calls=3）。
	if _, err := exec.executeOpenAI(newParams(), cand, 0, time.Now(), nil); err != nil {
		t.Fatalf("second executeOpenAI: %v", err)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("second round calls = %d, want 3 (learned strip → single clean call)", got)
	}
}

// Case 4: mode_mismatch 不可回退时仍记录（responses 客户端 + 非流式 +
// native 传输 404 → 只记录，不自动切换）。
func TestExecuteOpenAIReqProbeModeMismatchRecordsWithoutFallback(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"message":"not found"}}`))
	}))
	defer upstream.Close()

	store := reqprobe.NewMemoryStore()
	exec, coord := newReqProbeExecutor(store)
	defer coord.Close()

	cand := reqProbeTestCandidate(upstream.URL)
	cand.Protocol = "openai-responses"
	cand.SupportsNativeResponses = true

	_, err := exec.executeOpenAI(&ExecParams{
		W:                  httptest.NewRecorder(),
		R:                  httptest.NewRequest(http.MethodPost, "/v1/responses", nil),
		BodyBytes:          []byte(`{"model":"grok-4.6","messages":[],"stream":false}`),
		ResponsesBodyBytes: []byte(`{"model":"grok-4.6","input":[]}`),
		ClientModel:        "grok-4.6", ClientProtocol: "openai-responses",
	}, cand, 0, time.Now(), nil)
	if err == nil {
		t.Fatal("expected error for persistent 404")
	}
	// 非流式 responses 客户端不可安全回退 → 不重试。
	if got := calls.Load(); got != 2 {
		t.Fatalf("calls = %d, want 2 (mnf-free ladder; no mode fallback for non-stream responses client)", got)
	}
	recs := waitForReqProbeRecord(t, store, 1)
	if recs[0].Trigger != reqprobe.TriggerModeMismatch || recs[0].SuggestMode != "chat" {
		t.Fatalf("record = %+v", recs[0])
	}
	// JSON 契约抽查：字段名即前端 API 契约。
	blob, _ := json.Marshal(recs[0])
	for _, key := range []string{`"trigger":"mode_mismatch"`, `"suggest_mode":"chat"`, `"http_status":404`} {
		if !strings.Contains(string(blob), key) {
			t.Fatalf("record json missing %s: %s", key, blob)
		}
	}
}

// Case 5: 模式回退正向——native /responses 端点 404，自动切到 chat
// 端点重试成功（流式 + chat 客户端，响应侧有转换通道）。
func TestExecuteOpenAIReqProbeModeFallbackRecovers(t *testing.T) {
	var respCalls, chatCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "responses") {
			respCalls.Add(1)
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"message":"Unknown request URL"}`))
			return
		}
		chatCalls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	store := reqprobe.NewMemoryStore()
	exec, coord := newReqProbeExecutor(store)
	defer coord.Close()

	cand := reqProbeTestCandidate(upstream.URL)
	cand.Protocol = "openai-responses"
	cand.SupportsNativeResponsesStream = true
	// native 传输开启需要 executor 侧也有流桥接（生产由 main.go 接线）。
	exec.NativeResponsesStream = func(_ context.Context, _ http.ResponseWriter, _ *http.Response, _ string, _ *audit.StreamCapture, _ *atomic.Bool) StreamOutcome {
		return StreamOutcome{}
	}

	_, err := exec.executeOpenAI(&ExecParams{
		W:                  httptest.NewRecorder(),
		R:                  httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
		BodyBytes:          []byte(`{"model":"grok-4.6","messages":[],"stream":true}`),
		ResponsesBodyBytes: []byte(`{"model":"grok-4.6","input":[],"stream":true}`),
		IsStream:           true,
		ClientModel:        "grok-4.6", ClientProtocol: "openai-completions",
		StreamWrapper: func(http.ResponseWriter, *http.Response, NormalizerFunc, *audit.StreamCapture) StreamOutcome {
			return StreamOutcome{}
		},
	}, cand, 0, time.Now(), nil)
	if err != nil {
		t.Fatalf("executeOpenAI with mode fallback: %v", err)
	}
	if respCalls.Load() != 1 {
		t.Fatalf("responses endpoint calls = %d, want 1", respCalls.Load())
	}
	if chatCalls.Load() != 1 {
		t.Fatalf("chat endpoint calls = %d, want 1 (fallback retry)", chatCalls.Load())
	}
	recs := waitForReqProbeRecord(t, store, 1)
	if recs[0].Trigger != reqprobe.TriggerModeMismatch || recs[0].SuggestMode != "chat" || recs[0].RecoveredCount != 1 {
		t.Fatalf("record = %+v", recs[0])
	}
}
