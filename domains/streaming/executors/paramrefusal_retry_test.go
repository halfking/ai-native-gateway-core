package executors

// paramrefusal_retry_test.go — P0-1（2026-10-14）动态可选字段剔除记忆的
// 同层端到端判据（httptest 假上游直驱 executeOpenAI）。
//
// 判据纪律：
//   - 前提在场配对：断言「重试成功」前先断言第 1 个请求体确实带了被拒
//     字段、上游确实 400 点名了它；
//   - 记忆生效判据：第二次执行第 1 个请求就被剥（对照：空记忆时第 1 个
//     请求带着字段）；
//   - 黑名单：400 指名 messages → 前提（提取器确实提出 messages）在场，
//     但不重试、不记忆；
//   - 预算：每请求 1 次，第二次指名不再剥。
//
// 与 reqprobe_integration_test.go 的分工：那边测 reqprobe 通道；这边测
// paramrefusal 通道（RequestProbe 故意不接线 = 隔离变量；service_tier 在
// reqprobe 骨架保护集里，另一个用例专门验证两机制并存的生产行为）。

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/identity"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/internal/paramrefusal"
	"github.com/kaixuan/llm-gateway-go/internal/reqprobe"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// paramRefusalChatCandidate：openai-chat 线格式候选（非 native responses），
// catalog "xai"（ApplyCapabilitySanitizer 无该分支，客户端字段原样出站）。
func paramRefusalChatCandidate(baseURL string) provider.Candidate {
	return provider.Candidate{
		CredentialID: 201, ProviderID: 88, Tier: 1,
		BaseURL: baseURL, Protocol: "openai-completions", CatalogCode: "xai",
		RawModel: "grok-4.6", Weight: 100, BillingMode: "token_plan",
		Routable: true, LifecycleStatus: "active", AvailabilityState: "ready",
		QuotaState: "ok", CircuitState: "closed", APIKey: "sk-refusal-test",
	}
}

const paramRefusalOptionalBody = `{"model":"grok-4.6","messages":[{"role":"user","content":"hi"}],"stream":false,"service_tier":"default"}`

const paramRefusalChatOK = `{"id":"c1","object":"chat.completion","model":"grok-4.6","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`

// paramRefusalRecorder 捕获上游请求体（互斥保护；httptest handler 在独立
// goroutine 上跑）。
type paramRefusalRecorder struct {
	mu     sync.Mutex
	bodies []string
}

func (p *paramRefusalRecorder) record(r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	p.mu.Lock()
	p.bodies = append(p.bodies, strings.TrimSpace(string(body)))
	p.mu.Unlock()
}

func (p *paramRefusalRecorder) snapshot() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.bodies...)
}

func (p *paramRefusalRecorder) len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.bodies)
}

// ── 用例 1：400 点名可选字段 → 剔除重试一次成功 + 记忆 ──────────────────

func TestParamRefusalStripRetryRecovers(t *testing.T) {
	rec := &paramRefusalRecorder{}
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		rec.record(r)
		// 前提在场之一：首发（带 service_tier）被 400 点名拒绝。
		if strings.Contains(rec.snapshot()[rec.len()-1], `"service_tier"`) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Unknown parameter: 'service_tier'.","type":"invalid_request_error","param":"service_tier","code":"unknown_parameter"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(paramRefusalChatOK))
	}))
	defer upstream.Close()

	exec := newOverloadTestExecutor()
	store := paramrefusal.NewStore()
	exec.ParamRefusal = store

	var metered atomic.Int32
	clientRec := httptest.NewRecorder()
	_, err := exec.executeOpenAI(&ExecParams{
		W:                 httptest.NewRecorder(),
		R:                 httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
		BodyBytes:         []byte(paramRefusalOptionalBody),
		ClientModel:       "grok-4.6",
		ClientProtocol:    "openai-completions",
		ClientID:          identity.ClientIdentity{IdentityHash: "paramrefusal-retry"},
		ExtraUpstreamCall: func() { metered.Add(1) },
	}, paramRefusalChatCandidate(upstream.URL), 0, time.Now(), nil)
	_ = clientRec
	if err != nil {
		t.Fatalf("executeOpenAI: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("upstream calls = %d, want 2 (reject + strip retry)", got)
	}
	bodies := rec.snapshot()
	// 前提在场之二：第 1 个请求体确实带了被拒字段。
	if !strings.Contains(bodies[0], `"service_tier"`) {
		t.Fatalf("premise missing: first upstream body lacks service_tier: %s", bodies[0])
	}
	// 剔除后重试体不再携带。
	if strings.Contains(bodies[1], `"service_tier"`) {
		t.Fatalf("retry body still carries service_tier: %s", bodies[1])
	}
	if !strings.Contains(bodies[1], `"messages"`) {
		t.Fatalf("retry body lost messages (over-strip): %s", bodies[1])
	}
	// R51-F15：剔除重试补记一次准入内额外上游调用。
	if got := metered.Load(); got != 1 {
		t.Fatalf("extra-call meter = %d, want 1", got)
	}
	// 记忆已写入 (provider, model, wire) 键。
	if got := store.StrippableFields(88, "grok-4.6", "openai-completions"); len(got) != 1 || got[0] != "service_tier" {
		t.Fatalf("memory = %v, want [service_tier]", got)
	}
}

// ── 用例 2：记忆生效——第二次执行首发即剥 ──────────────────────────────

func TestParamRefusalMemoryAppliedOnSecondExecution(t *testing.T) {
	rec := &paramRefusalRecorder{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		if strings.Contains(rec.snapshot()[rec.len()-1], `"service_tier"`) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Unknown parameter: 'service_tier'.","type":"invalid_request_error","code":"unknown_parameter"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(paramRefusalChatOK))
	}))
	defer upstream.Close()

	store := paramrefusal.NewStore()
	cand := paramRefusalChatCandidate(upstream.URL)
	newParams := func() *ExecParams {
		return &ExecParams{
			W:              httptest.NewRecorder(),
			R:              httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
			BodyBytes:      []byte(paramRefusalOptionalBody),
			ClientModel:    "grok-4.6",
			ClientProtocol: "openai-completions",
			ClientID:       identity.ClientIdentity{IdentityHash: "paramrefusal-mem"},
		}
	}

	// 第一轮（全新 executor + 共享 store）：400 → 剥除重试成功（2 calls）。
	exec1 := newOverloadTestExecutor()
	exec1.ParamRefusal = store
	if _, err := exec1.executeOpenAI(newParams(), cand, 0, time.Now(), nil); err != nil {
		t.Fatalf("first executeOpenAI: %v", err)
	}
	if rec.len() != 2 {
		t.Fatalf("first round calls = %d, want 2", rec.len())
	}

	// 第二轮（全新 executor + 同一 store）：首发即剥（总 3 calls，本轮 1）。
	exec2 := newOverloadTestExecutor()
	exec2.ParamRefusal = store
	if _, err := exec2.executeOpenAI(newParams(), cand, 0, time.Now(), nil); err != nil {
		t.Fatalf("second executeOpenAI: %v", err)
	}
	if rec.len() != 3 {
		t.Fatalf("second round total calls = %d, want 3 (memory → single clean call)", rec.len())
	}
	third := rec.snapshot()[2]
	if strings.Contains(third, `"service_tier"`) {
		t.Fatalf("memory not applied: third request still carries service_tier: %s", third)
	}
}

// ── 用例 2b：前提不在场（点名字段不在出站体）→ 不重试不记忆 ────────────

func TestParamRefusalNoPremiseNoRetry(t *testing.T) {
	// 出站体不带 thinking，但上游 400 点名 thinking（提取误报/字段来自
	// 别的层的形态）。前提在场（同层）：提取器确实能从该错误体提出
	// thinking，且它在可选家族里——「不重试」只能是前提检查的裁决。
	rejection := `{"error":{"message":"Unknown parameter: 'thinking'.","type":"invalid_request_error","code":"unknown_parameter"}}`
	if f, ok := errorsx.RefusedOptionalParam([]byte(rejection)); !ok || f != "thinking" {
		t.Fatalf("premise missing: RefusedOptionalParam = (%q,%v), want (thinking,true)", f, ok)
	}

	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(rejection))
	}))
	defer upstream.Close()

	exec := newOverloadTestExecutor()
	store := paramrefusal.NewStore()
	exec.ParamRefusal = store

	_, err := exec.executeOpenAI(&ExecParams{
		W:              httptest.NewRecorder(),
		R:              httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
		BodyBytes:      []byte(`{"model":"grok-4.6","messages":[{"role":"user","content":"hi"}],"stream":false}`),
		ClientModel:    "grok-4.6",
		ClientProtocol: "openai-completions",
		ClientID:       identity.ClientIdentity{IdentityHash: "paramrefusal-nopremise"},
	}, paramRefusalChatCandidate(upstream.URL), 0, time.Now(), nil)
	if err == nil {
		t.Fatal("expected the 400 to surface (refused field was never in the outbound body)")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("upstream calls = %d, want 1 (absent field → pointless same-body retry suppressed)", got)
	}
	if store.Len() != 0 {
		t.Fatalf("memory polluted by absent-field refusal: Len = %d", store.Len())
	}
}

// ── 用例 3：黑名单——400 点名 messages 不重试不记忆 ────────────────────

func TestParamRefusalBlacklistCoreFieldNoRetryNoMemory(t *testing.T) {
	// 前提在场（同层）：提取器对同一错误体确实能提出 "messages"——
	// 「不重试」必须是黑名单裁决，不能是「看不见」。
	rejection := `{"error":{"message":"Unknown parameter: 'messages'.","type":"invalid_request_error","param":"messages","code":"unknown_parameter"}}`
	if f, ok := errorsx.RefusedOptionalParam([]byte(rejection)); !ok || f != "messages" {
		t.Fatalf("premise missing: RefusedOptionalParam = (%q,%v), want (messages,true)", f, ok)
	}

	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(rejection))
	}))
	defer upstream.Close()

	exec := newOverloadTestExecutor()
	store := paramrefusal.NewStore()
	exec.ParamRefusal = store

	_, err := exec.executeOpenAI(&ExecParams{
		W:              httptest.NewRecorder(),
		R:              httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
		BodyBytes:      []byte(`{"model":"grok-4.6","messages":[{"role":"user","content":"hi"}],"stream":false}`),
		ClientModel:    "grok-4.6",
		ClientProtocol: "openai-completions",
		ClientID:       identity.ClientIdentity{IdentityHash: "paramrefusal-blacklist"},
	}, paramRefusalChatCandidate(upstream.URL), 0, time.Now(), nil)
	if err == nil {
		t.Fatal("expected the 400 to surface (core field must not be stripped into a doomed retry)")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("upstream calls = %d, want 1 (blacklisted field → no paramrefusal retry)", got)
	}
	if store.Len() != 0 {
		t.Fatalf("memory polluted with blacklisted field: Len = %d", store.Len())
	}
}

// ── 用例 4：每请求预算 1 次（不分字段） ────────────────────────────────

func TestParamRefusalBudgetOnePerRequest(t *testing.T) {
	rec := &paramRefusalRecorder{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		last := rec.snapshot()[rec.len()-1]
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(last, `"metadata"`):
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Unknown parameter: 'metadata'.","type":"invalid_request_error","code":"unknown_parameter"}}`))
		case strings.Contains(last, `"service_tier"`):
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Unknown parameter: 'service_tier'.","type":"invalid_request_error","code":"unknown_parameter"}}`))
		default:
			_, _ = w.Write([]byte(paramRefusalChatOK))
		}
	}))
	defer upstream.Close()

	exec := newOverloadTestExecutor()
	store := paramrefusal.NewStore()
	exec.ParamRefusal = store

	_, err := exec.executeOpenAI(&ExecParams{
		W:              httptest.NewRecorder(),
		R:              httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
		BodyBytes:      []byte(`{"model":"grok-4.6","messages":[{"role":"user","content":"hi"}],"stream":false,"metadata":{"u":"1"},"service_tier":"auto"}`),
		ClientModel:    "grok-4.6",
		ClientProtocol: "openai-completions",
		ClientID:       identity.ClientIdentity{IdentityHash: "paramrefusal-budget"},
	}, paramRefusalChatCandidate(upstream.URL), 0, time.Now(), nil)
	if err == nil {
		t.Fatal("expected final error when both refusals cannot be resolved within one budget")
	}
	bodies := rec.snapshot()
	metadataCalls := 0
	for _, b := range bodies {
		if strings.Contains(b, `"metadata"`) {
			metadataCalls++
		}
	}
	if metadataCalls != 1 {
		t.Fatalf("metadata appeared in %d calls, want exactly 1 (first attempt)", metadataCalls)
	}
	// 预算判定：service_tier 从未被剥（第二次 400 点名它时预算已花光）。
	for i, b := range bodies {
		if i > 0 && !strings.Contains(b, `"service_tier"`) {
			t.Fatalf("call %d lost service_tier — budget-1 was violated (second strip fired)", i)
		}
	}
	// 记忆只含第一次被拒的字段。
	if got := store.StrippableFields(88, "grok-4.6", "openai-completions"); len(got) != 1 || got[0] != "metadata" {
		t.Fatalf("memory = %v, want [metadata] only (budget exhausted before second refusal)", got)
	}
}

// ── 用例 5：native responses 透传腿 choke point 主动剔除（记忆驱动） ────

func TestParamRefusalNativeChokePointAppliesMemory(t *testing.T) {
	const nativeBody = `{"model":"grok-4.6","stream":false,"metadata":{"user_id":"u1"},"input":[{"role":"user","content":"回复 ok"}]}`

	run := func(seedMemory bool) []string {
		var bodies []string
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			buf := make([]byte, 1<<16)
			n, _ := r.Body.Read(buf)
			bodies = append(bodies, strings.TrimSpace(string(buf[:n])))
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(upstreamResponsesOK))
		}))
		defer upstream.Close()

		exec := newOverloadTestExecutor()
		store := paramrefusal.NewStore()
		if seedMemory {
			store.Remember(36, "grok-4.6", "openai-responses", "metadata")
		}
		exec.ParamRefusal = store

		clientRec := httptest.NewRecorder()
		_, err := exec.executeOpenAI(&ExecParams{
			R:                  httptest.NewRequest(http.MethodPost, "/v1/responses", nil),
			W:                  clientRec,
			BodyBytes:          []byte(`{"model":"grok-4.6","messages":[{"role":"user","content":"回复 ok"}],"stream":false}`),
			ResponsesBodyBytes: []byte(nativeBody),
			ClientProtocol:     "openai-responses",
			ClientModel:        "grok-4.6",
			ClientID:           identity.ClientIdentity{IdentityHash: "paramrefusal-native"},
		}, verifiedVapeurCandidate(upstream.URL, "grok-4.6"), 0, time.Now(), nil)
		if err != nil {
			t.Fatalf("executeOpenAI: %v", err)
		}
		return bodies
	}

	// 对照（前提在场）：空记忆时 metadata 原样到上游——证明剔除只能来自记忆。
	control := run(false)
	if len(control) != 1 || !strings.Contains(control[0], `"metadata"`) {
		t.Fatalf("control (empty memory) should carry metadata: %v", control)
	}
	// 记忆在场：首发即剥 metadata，input/model 保留。
	seeded := run(true)
	if len(seeded) != 1 {
		t.Fatalf("seeded run calls = %d, want 1", len(seeded))
	}
	if strings.Contains(seeded[0], `"metadata"`) {
		t.Fatalf("native choke point did not apply memory: %s", seeded[0])
	}
	if !strings.Contains(seeded[0], `"input"`) || !strings.Contains(seeded[0], `"grok-4.6"`) {
		t.Fatalf("native body lost payload (over-strip): %s", seeded[0])
	}
}

// ── 用例 6：reactive 模式回退重试体同样应用记忆 ────────────────────────

func TestParamRefusalReactiveRetryBodyAppliesMemory(t *testing.T) {
	const responsesBody = `{"model":"grok-4.6","stream":false,"metadata":{"user_id":"u1"},"input":[{"role":"user","content":"回复 ok"}]}`

	rec := &paramRefusalRecorder{}
	var paths []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		paths = append(paths, r.URL.Path)
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"This model requires the Responses API. Use the Responses API.","code":4006}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(upstreamResponsesOK))
	}))
	defer upstream.Close()

	exec := newOverloadTestExecutor()
	store := paramrefusal.NewStore()
	store.Remember(36, "grok-4.6", "openai-responses", "metadata")
	exec.ParamRefusal = store

	cand := verifiedVapeurCandidate(upstream.URL, "grok-4.6")
	cand.SupportsNativeResponses = false
	cand.SupportsNativeResponsesKnown = false
	cand.SupportsNativeResponsesStream = false

	_, err := exec.executeOpenAI(&ExecParams{
		R:                  httptest.NewRequest(http.MethodPost, "/v1/responses", nil),
		W:                  httptest.NewRecorder(),
		BodyBytes:          []byte(`{"model":"grok-4.6","messages":[{"role":"user","content":"回复 ok"}],"stream":false}`),
		ResponsesBodyBytes: []byte(responsesBody),
		ClientProtocol:     "openai-responses",
		ClientModel:        "grok-4.6",
		ClientID:           identity.ClientIdentity{IdentityHash: "paramrefusal-reactive"},
	}, cand, 0, time.Now(), nil)
	if err != nil {
		t.Fatalf("executeOpenAI: %v", err)
	}
	if len(paths) < 2 || !strings.HasSuffix(paths[len(paths)-1], "/responses") {
		t.Fatalf("paths = %v, want chat attempt then /responses retry", paths)
	}
	retryBody := rec.snapshot()[len(rec.snapshot())-1]
	if strings.Contains(retryBody, `"metadata"`) {
		t.Fatalf("reactive retry body carries remembered field: %s", retryBody[:min(len(retryBody), 300)])
	}
	if !strings.Contains(retryBody, `"input"`) {
		t.Fatalf("reactive retry body lost input (over-strip): %s", retryBody[:min(len(retryBody), 300)])
	}
}

// ── 用例 7（P0-B）：RequestProbe 与 paramrefusal 并存时，非骨架可选字段
// （service_tier）仍能剥参重试；学习写入 RequestProbe（拒绝即学习）。────

func TestParamRefusalCoexistsWithReqprobeSkeletonField(t *testing.T) {
	rec := &paramRefusalRecorder{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		if strings.Contains(rec.snapshot()[rec.len()-1], `"service_tier"`) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Unknown parameter: 'service_tier'.","type":"invalid_request_error","param":"service_tier","code":"unknown_parameter"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(paramRefusalChatOK))
	}))
	defer upstream.Close()

	exec := newOverloadTestExecutor()
	coord := reqprobe.NewCoordinator(reqprobe.NewMemoryStore())
	defer coord.Close()
	exec.RequestProbe = coord
	store := paramrefusal.NewStore()
	exec.ParamRefusal = store

	_, err := exec.executeOpenAI(&ExecParams{
		W:              httptest.NewRecorder(),
		R:              httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
		BodyBytes:      []byte(paramRefusalOptionalBody),
		ClientModel:    "grok-4.6",
		ClientProtocol: "openai-completions",
		ClientID:       identity.ClientIdentity{IdentityHash: "paramrefusal-coexist"},
	}, paramRefusalChatCandidate(upstream.URL), 0, time.Now(), nil)
	if err != nil {
		t.Fatalf("executeOpenAI: %v", err)
	}
	bodies := rec.snapshot()
	if len(bodies) != 2 {
		t.Fatalf("calls = %d, want 2 (named optional strip + retry)", len(bodies))
	}
	if strings.Contains(bodies[1], `"service_tier"`) {
		t.Fatalf("retry body still carries service_tier: %s", bodies[1])
	}
}

// ── 用例 8（P0-B）：chat 线注入的 stream_options 被拒时不再剥参重试
// （usage 契约骨架保护）。前提：首发确实带 stream_options。──────────────

func TestParamRefusalStreamingChatStripRetry(t *testing.T) {
	rec := &paramRefusalRecorder{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"Unknown parameter: 'stream_options'.","type":"invalid_request_error","code":"unknown_parameter"}}`))
	}))
	defer upstream.Close()

	exec := newOverloadTestExecutor()
	store := paramrefusal.NewStore()
	exec.ParamRefusal = store

	clientRec := httptest.NewRecorder()
	_, err := exec.executeOpenAI(&ExecParams{
		W:              clientRec,
		R:              httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
		BodyBytes:      []byte(`{"model":"grok-4.6","messages":[{"role":"user","content":"hi"}],"stream":true}`),
		IsStream:       true,
		ClientModel:    "grok-4.6",
		ClientProtocol: "openai-completions",
		ClientID:       identity.ClientIdentity{IdentityHash: "paramrefusal-stream"},
	}, paramRefusalChatCandidate(upstream.URL), 0, time.Now(), nil)
	_ = err
	bodies := rec.snapshot()
	if len(bodies) < 1 {
		t.Fatal("expected at least one upstream call")
	}
	// 前提在场：executor 注入了 stream_options。
	if !strings.Contains(bodies[0], `"stream_options"`) {
		t.Fatalf("premise missing: first body lacks injected stream_options: %s", bodies[0])
	}
	// P0-B：chat 线不剥 stream_options → 不应出现第二轮「已剥」请求。
	for i, b := range bodies {
		if i == 0 {
			continue
		}
		if !strings.Contains(b, `"stream_options"`) {
			t.Fatalf("call %d stripped stream_options on chat wire (P0-B forbids): %s", i+1, b)
		}
	}
	if got := store.StrippableFields(88, "grok-4.6", "openai-completions"); len(got) != 0 {
		t.Fatalf("chat wire must not remember stream_options, got %v", got)
	}
}
