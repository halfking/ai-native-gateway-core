// P0-3 (2026-10-14): eof_without_done 断流原地续问 —— 同层端到端判据。
//
// 分层口径（判据纪律第 1 条）：
//   - 本文件跑在 package executors_test（外部测试包，可同时 import
//     streaming 与 executors，不构成环），用真实生产读循环
//     streaming.StreamChatWithPendingCapture 驱动假上游，覆盖
//     executeOpenAI 装钩 → stream.go §11.6 splice → 续问帧拼进同一条
//     出站管道的全链。
//   - 前提在场断言（纪律第 2 条）：允许侧用例（拼接成功）必须同时证明
//     「续问请求真的带了 assistant prefill」与「请求总数=2」——否则
//     「没断流」可能是假绿（检测器根本没看见 EOF）。
package executors_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/credential"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"
	"github.com/kaixuan/llm-gateway-go/domains/identity"
	"github.com/kaixuan/llm-gateway-go/domains/streaming"
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/pool"
	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// ── settings 钉扎（kill switch 必须可翻） ─────────────────────────────

type p03SettingsBackend struct {
	store map[string][]byte
}

func (f p03SettingsBackend) Get(_ settings.Scope, key string) ([]byte, error) {
	return f.store[key], nil
}

func (f p03SettingsBackend) Set(_ settings.Scope, _ string, _ any) ([]byte, error) {
	return nil, nil
}

func (f p03SettingsBackend) GetTenant(_, _ string) ([]byte, error) {
	return nil, nil
}

func (f p03SettingsBackend) SetTenant(_, _ string, _ any) ([]byte, error) {
	return nil, nil
}

func withP03ContinuationSetting(t *testing.T, enabled bool) {
	t.Helper()
	prevGlobal := settings.Global
	t.Cleanup(func() { settings.Global = prevGlobal })

	registry := settings.NewRegistry()
	registry.RegisterBackend(settings.ScopePlatform, p03SettingsBackend{
		store: map[string][]byte{
			settings.KeyStreamContinuationEnabled: []byte(map[bool]string{true: "true", false: "false"}[enabled]),
		},
	})
	for _, spec := range settings.StreamContinuationSpecs() {
		registry.MustRegisterSpec(spec)
	}
	settings.Global = registry
}

// ── 假上游 ───────────────────────────────────────────────────────────

type p03UpstreamLog struct {
	mu     sync.Mutex
	bodies []string
	auths  []string
	paths  []string
}

func (l *p03UpstreamLog) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.bodies)
}

func (l *p03UpstreamLog) body(i int) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.bodies[i]
}

func (l *p03UpstreamLog) auth(i int) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.auths[i]
}

// newP03Upstream serves: leg 1 = leg1Frames then close WITHOUT [DONE]
// (eof_without_done 形态)；leg 2+ = leg2Frames + data: [DONE].
func newP03Upstream(t *testing.T, leg1Frames, leg2Frames []string) (*httptest.Server, *p03UpstreamLog) {
	t.Helper()
	log := &p03UpstreamLog{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := make([]byte, 0, 4096)
		chunk := make([]byte, 1024)
		for {
			n, err := r.Body.Read(chunk)
			raw = append(raw, chunk[:n]...)
			if err != nil {
				break
			}
		}
		log.mu.Lock()
		log.bodies = append(log.bodies, string(raw))
		log.auths = append(log.auths, r.Header.Get("Authorization"))
		log.paths = append(log.paths, r.URL.Path)
		leg := len(log.bodies)
		log.mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		frames := leg2Frames
		if leg == 1 {
			frames = leg1Frames
		}
		for _, f := range frames {
			_, _ = fmt.Fprint(w, f)
		}
		if flusher != nil {
			flusher.Flush()
		}
		// leg 1 returns without [DONE] —— 上游断流。
	}))
	t.Cleanup(upstream.Close)
	return upstream, log
}

// ── 客户端流解析 ─────────────────────────────────────────────────────

type p03ClientStream struct {
	text      string
	doneCount int
	roleFrame int
	errFrames int
	raw       string
}

func parseP03ClientStream(body string) p03ClientStream {
	out := p03ClientStream{raw: body}
	var text strings.Builder
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
		if payload == "[DONE]" {
			out.doneCount++
			continue
		}
		var frame struct {
			Error  json.RawMessage `json:"error"`
			Choice []struct {
				Delta struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &frame); err != nil {
			continue
		}
		if len(frame.Error) > 0 {
			out.errFrames++
		}
		if len(frame.Choice) > 0 {
			if frame.Choice[0].Delta.Role != "" {
				out.roleFrame++
			}
			text.WriteString(frame.Choice[0].Delta.Content)
		}
	}
	out.text = text.String()
	return out
}

// ── 执行器装配（照 glm52_stream_failover_integration_test.go 形状） ────

func newP03Executor(t *testing.T, upstreamURL string) *executors.Executor {
	t.Helper()
	limiter := credential.NewLimiter()
	t.Cleanup(limiter.Stop)
	exec := executors.NewExecutor(
		executors.NewRouter(executors.NewStickyCache(), limiter),
		credential.NewManager(),
		limiter,
		pool.NewPoolManager(nil),
		nil,
		func(chunk []byte, _ bool) []byte { return chunk },
		func(ctx context.Context, w http.ResponseWriter, resp *http.Response, clientModel, outboundModel, _ string, _ executors.NormalizerFunc, capture *audit.StreamCapture, toolsRequested bool) executors.StreamOutcome {
			out := streaming.StreamChatWithPendingCapture(ctx, w, resp, clientModel, outboundModel, streaming.NewNormalizer(), capture, toolsRequested, nil, nil)
			return executors.StreamOutcome{
				Interrupted:      out.Interrupted,
				Reason:           out.Reason,
				Resumable:        out.Resumable,
				ChunkCount:       out.ChunkCount,
				Kind:             out.Kind,
				TerminalRendered: out.TerminalRendered,
			}
		},
		nil,
	)
	pipeline := exec.NewDispatchPipeline(nil)
	pipeline.Start()
	t.Cleanup(pipeline.Stop)
	exec.SetDispatchPipeline(pipeline)
	return exec
}

func p03Execute(t *testing.T, exec *executors.Executor, upstreamURL string) (*httptest.ResponseRecorder, *executors.ExecuteResult, error) {
	t.Helper()
	return p03ExecuteCandidate(t, exec, upstreamURL, "https://api.deepseek.com/v1", "deepseek-chat")
}

type p03Transport func(*http.Request) (*http.Response, error)

func (f p03Transport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func p03ExecuteCandidate(t *testing.T, exec *executors.Executor, upstreamURL, baseURL, model string) (*httptest.ResponseRecorder, *executors.ExecuteResult, error) {
	t.Helper()
	// Keep the candidate's real capability identity; only route network I/O to
	// the test server. No test request can reach the public vendor.
	previous := http.DefaultClient
	target, err := url.Parse(upstreamURL)
	if err != nil {
		t.Fatal(err)
	}
	http.DefaultClient = &http.Client{Transport: p03Transport(func(req *http.Request) (*http.Response, error) {
		clone := req.Clone(req.Context())
		u := *req.URL
		u.Scheme, u.Host = target.Scheme, target.Host
		clone.URL = &u
		return http.DefaultTransport.RoundTrip(clone)
	})}
	defer func() { http.DefaultClient = previous }()
	exec.Pools = nil
	recorder := httptest.NewRecorder()
	cand := provider.Candidate{
		ProviderID:        18,
		CredentialID:      209423,
		BaseURL:           baseURL,
		Protocol:          "openai-completions",
		CatalogCode:       "openai",
		Tier:              1,
		Weight:            100,
		RawModel:          model,
		OfferRawModel:     model,
		APIKey:            "p03-key",
		BillingMode:       "token_plan",
		Routable:          true,
		LifecycleStatus:   "active",
		AvailabilityState: "ready",
		QuotaState:        "ok",
		CircuitState:      "closed",
	}
	result, err := exec.Execute(&executors.ExecParams{
		W:                           recorder,
		R:                           httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
		BodyBytes:                   []byte(`{"model":"p03-model","messages":[{"role":"user","content":"count to ten"}],"stream":true}`),
		Model:                       "p03-model",
		ClientModel:                 "p03-model",
		ClientProtocol:              "openai-completions",
		IsStream:                    true,
		Capture:                     audit.NewStreamCapture(),
		ClientID:                    identity.ClientIdentity{IdentityHash: "p03-stream-continuation"},
		Candidates:                  []provider.Candidate{cand},
		Policy:                      &provider.Policy{TierFallbackMax: 1, RetryPerCredential: 0},
		DispatchAllowProviderChange: true,
	})
	return recorder, result, err
}

// p03TailAssistantContent returns the trailing assistant message content of
// the request body ("" when the tail is not an assistant message).
func p03TailAssistantContent(body string) (string, bool) {
	var parsed struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		return "", false
	}
	if len(parsed.Messages) == 0 {
		return "", false
	}
	tail := parsed.Messages[len(parsed.Messages)-1]
	if tail.Role != "assistant" {
		return "", false
	}
	return tail.Content, true
}

// ── 用例 ─────────────────────────────────────────────────────────────

const (
	p03Frame1a = "data: {\"id\":\"p03\",\"object\":\"chat.completion.chunk\",\"model\":\"p03-model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"\"},\"finish_reason\":null}]}\n\n"
	p03Frame1b = "data: {\"id\":\"p03\",\"object\":\"chat.completion.chunk\",\"model\":\"p03-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"one two \"},\"finish_reason\":null}]}\n\n"
	p03Frame1c = "data: {\"id\":\"p03\",\"object\":\"chat.completion.chunk\",\"model\":\"p03-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"three\"},\"finish_reason\":null}]}\n\n"
	// 续问腿首帧带 role:assistant + 新 content —— role 必须被抑制，content 必须透传。
	p03Frame2a = "data: {\"id\":\"p03\",\"object\":\"chat.completion.chunk\",\"model\":\"p03-model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"four\"},\"finish_reason\":null}]}\n\n"
	p03Frame2b = "data: {\"id\":\"p03\",\"object\":\"chat.completion.chunk\",\"model\":\"p03-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\" five\"},\"finish_reason\":null}]}\n\n"
	p03Frame2z = "data: {\"id\":\"p03\",\"object\":\"chat.completion.chunk\",\"model\":\"p03-model\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"
	p03Done    = "data: [DONE]\n\n"
)

// 主用例：断流续问拼接。第 1 腿发「one two three」后直接关连接（无
// [DONE]/finish_reason）；第 2 腿（续问）发「four five」+ finish + [DONE]。
func TestP03StreamContinuationSplicesSeamlessly(t *testing.T) {
	withP03ContinuationSetting(t, true)
	upstream, log := newP03Upstream(t,
		[]string{p03Frame1a, p03Frame1b, p03Frame1c},
		[]string{p03Frame2a, p03Frame2b, p03Frame2z, p03Done},
	)
	exec := newP03Executor(t, upstream.URL)
	recorder, result, err := p03Execute(t, exec, upstream.URL)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result == nil {
		t.Fatal("expected successful result")
	}

	got := parseP03ClientStream(recorder.Body.String())

	// 客户端拼出的完整文本 = 前半 + 后半，无重复前缀。
	if want := "one two threefour five"; got.text != want {
		t.Fatalf("client text = %q, want %q", got.text, want)
	}
	// 只有一个终态。
	if got.doneCount != 1 {
		t.Fatalf("client [DONE] count = %d, want 1 (raw=%q)", got.doneCount, got.raw)
	}
	// 无错误帧。
	if got.errFrames != 0 {
		t.Fatalf("client error frames = %d, want 0 (raw=%q)", got.errFrames, got.raw)
	}
	// 无 role 重复帧：第一腿首帧的 role 是唯一一次。
	if got.roleFrame != 1 {
		t.Fatalf("client role frames = %d, want 1 (raw=%q)", got.roleFrame, got.raw)
	}

	// 前提在场（判据纪律第 2 条）：必须真的发生了续问，且第 2 个请求的
	// messages 尾部带 assistant prefill（内容=前半段文本）。
	if n := log.count(); n != 2 {
		t.Fatalf("upstream request count = %d, want 2", n)
	}
	prefill, ok := p03TailAssistantContent(log.body(1))
	if !ok {
		t.Fatalf("continuation request tail message is not assistant prefill: %s", log.body(1))
	}
	if prefill != "one two three" {
		t.Fatalf("continuation prefill = %q, want %q", prefill, "one two three")
	}
	// 同候选：两次请求同一凭据头。
	if log.auth(0) == "" || log.auth(0) != log.auth(1) {
		t.Fatalf("authorization changed between legs: %q vs %q", log.auth(0), log.auth(1))
	}
}

// 回归用例：第 1 腿正常 [DONE] 结束 —— 行为与现状完全一致（请求总数=1，
// 不触发续问）。
func TestP03NormalStreamDoesNotContinue(t *testing.T) {
	withP03ContinuationSetting(t, true)
	upstream, log := newP03Upstream(t,
		[]string{p03Frame1a, p03Frame1b, p03Frame2z, p03Done},
		[]string{p03Frame2a, p03Done},
	)
	exec := newP03Executor(t, upstream.URL)
	recorder, result, err := p03Execute(t, exec, upstream.URL)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result == nil {
		t.Fatal("expected successful result")
	}
	if n := log.count(); n != 1 {
		t.Fatalf("upstream request count = %d, want 1", n)
	}
	got := parseP03ClientStream(recorder.Body.String())
	if got.text != "one two " || got.doneCount != 1 || got.errFrames != 0 {
		t.Fatalf("client stream regressed: %+v (raw=%q)", got, got.raw)
	}
}

// 续问腿也断流：整流终态化（现有 eof_without_done 语义）。直接驱动生产
// 读循环 streaming.StreamChatWithPendingCapture（与 E2E 用例同一入口），
// 续问 hook 用脚本化版本（返回静态 SSE reader）——预算是读循环的属性，
// 在这一层断言才不受 executor 候选循环 failover 噪音干扰（§11.6 之后
// 候选循环本就会继续走下一个候选，这是既有行为，与本特性无关）。
func TestP03ContinuationBudgetIsExactlyOne(t *testing.T) {
	var (
		hookMu    sync.Mutex
		hookCalls []string
	)
	leg1 := p03Frame1a + p03Frame1b + p03Frame1c // 无 [DONE]，断流
	leg2 := p03Frame2a + p03Frame2b              // 续问腿也断流（无 [DONE]）
	ctx := executors.WithStreamContinuation(context.Background(), func(prefixText string) (io.ReadCloser, error) {
		hookMu.Lock()
		hookCalls = append(hookCalls, prefixText)
		hookMu.Unlock()
		return io.NopCloser(strings.NewReader(leg2)), nil
	})

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(leg1)),
		Request:    httptest.NewRequest(http.MethodPost, "http://upstream.test/v1/chat/completions", nil),
	}
	recorder := httptest.NewRecorder()
	outcome := streaming.StreamChatWithPendingCapture(ctx, recorder, resp, "p03-model", "p03-model", nil, nil, false, nil, nil)

	hookMu.Lock()
	defer hookMu.Unlock()
	if len(hookCalls) != 1 {
		t.Fatalf("continuation hook calls = %d, want exactly 1 (budget; calls=%v)", len(hookCalls), hookCalls)
	}
	if hookCalls[0] != "one two three" {
		t.Fatalf("continuation prefill = %q, want %q", hookCalls[0], "one two three")
	}
	if !outcome.Interrupted || outcome.Reason != "eof_without_done" {
		t.Fatalf("double-cut outcome = %+v, want Interrupted eof_without_done", outcome)
	}
	if !outcome.TerminalRendered {
		t.Fatal("double-cut outcome must be TerminalRendered (legacy §11.6 semantics)")
	}
	got := parseP03ClientStream(recorder.Body.String())
	if want := "one two threefour five"; got.text != want {
		t.Fatalf("client text = %q, want truncated %q", got.text, want)
	}
	if got.doneCount != 1 {
		t.Fatalf("client [DONE] count = %d, want exactly 1 (no double terminal; raw=%q)", got.doneCount, got.raw)
	}
	if got.errFrames != 1 {
		t.Fatalf("client error frames = %d, want exactly 1 eof_without_done envelope (raw=%q)", got.errFrames, got.raw)
	}
	if !strings.Contains(got.raw, "eof_without_done") {
		t.Fatalf("terminal envelope missing eof_without_done code (raw=%q)", got.raw)
	}
}

// 开关关闭（读循环侧形态 = ctx 无 hook）：完全回到现有行为——hook 零调用、
// 截断 + 单终态化。executor 侧的开关读取由 TestP03ExecutorGateInstallsHook
// 钉住（两层拼起来才是完整的 kill switch）。
func TestP03KillSwitchRestoresLegacyBehavior(t *testing.T) {
	leg1 := p03Frame1a + p03Frame1b + p03Frame1c
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(leg1)),
		Request:    httptest.NewRequest(http.MethodPost, "http://upstream.test/v1/chat/completions", nil),
	}
	recorder := httptest.NewRecorder()
	outcome := streaming.StreamChatWithPendingCapture(context.Background(), recorder, resp, "p03-model", "p03-model", nil, nil, false, nil, nil)

	if !outcome.Interrupted || outcome.Reason != "eof_without_done" {
		t.Fatalf("legacy outcome = %+v, want Interrupted eof_without_done", outcome)
	}
	got := parseP03ClientStream(recorder.Body.String())
	if got.text != "one two three" {
		t.Fatalf("client text = %q, want legacy truncated %q", got.text, "one two three")
	}
	if got.doneCount != 1 || got.errFrames != 1 {
		t.Fatalf("legacy terminal shape regressed: done=%d errFrames=%d (raw=%q)", got.doneCount, got.errFrames, got.raw)
	}
}

// executor 开关门：开关开 → 生产读循环收到的 ctx 里有续问 hook；开关关 →
// 没有。stub StreamChat 在真实调用位（streamReaderContext → resp ctx）检查，
// 钉的是 executeOpenAI 的安装条件（含 settings 读取——r1027 教训：键不进
// 注册表/不读取会静默回退默认值）。
func TestP03ExecutorGateInstallsHook(t *testing.T) {
	for _, tc := range []struct {
		name    string
		enabled bool
		want    bool
	}{
		{"enabled installs hook", true, true},
		{"kill switch withholds hook", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withP03ContinuationSetting(t, tc.enabled)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w, p03Frame1a+p03Frame1b+p03Frame2z+p03Done)
			}))
			defer upstream.Close()

			var hookSeen bool
			limiter := credential.NewLimiter()
			defer limiter.Stop()
			exec := executors.NewExecutor(
				executors.NewRouter(executors.NewStickyCache(), limiter),
				credential.NewManager(),
				limiter,
				pool.NewPoolManager(nil),
				nil,
				func(chunk []byte, _ bool) []byte { return chunk },
				func(ctx context.Context, w http.ResponseWriter, resp *http.Response, clientModel, outboundModel, _ string, _ executors.NormalizerFunc, capture *audit.StreamCapture, toolsRequested bool) executors.StreamOutcome {
					// 生产接线里读循环拿到的 ctx 就是这里的 ctx
					// （streamReaderContext(params, resp)）。同包检查 hook
					// 在场性即等价于「读循环能发现它」。
					hookSeen = executors.StreamContinuationFromContext(ctx) != nil
					_, _ = io.Copy(w, resp.Body)
					return executors.StreamOutcome{}
				},
				nil,
			)
			pipeline := exec.NewDispatchPipeline(nil)
			pipeline.Start()
			defer pipeline.Stop()
			exec.SetDispatchPipeline(pipeline)

			_, _, err := p03Execute(t, exec, upstream.URL)
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if hookSeen != tc.want {
				t.Fatalf("hook present in stream ctx = %v, want %v", hookSeen, tc.want)
			}
		})
	}
}

func TestP1ContinuationRequiresKnownPrefillCapability(t *testing.T) {
	for _, tc := range []struct{ name, base, model string }{
		{"generic chat", "https://relay.example/v1", "deepseek-chat"},
		{"host suffix spoof", "https://api.deepseek.com.evil.example/v1", "deepseek-chat"},
		{"unknown model on official host", "https://api.deepseek.com/v1", "gpt-4.1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withP03ContinuationSetting(t, true)
			upstream, log := newP03Upstream(t, []string{p03Frame1a, p03Frame1b, p03Frame1c}, []string{p03Frame2a, p03Frame2z, p03Done})
			exec := newP03Executor(t, upstream.URL)
			recorder, _, err := p03ExecuteCandidate(t, exec, upstream.URL, tc.base, tc.model)
			if log.count() != 1 {
				t.Fatalf("unproven capability must not continue; upstream calls=%d err=%v bodies=%v wire=%s", log.count(), err, log.bodies, recorder.Body.String())
			}
			got := parseP03ClientStream(recorder.Body.String())
			if got.text != "one two three" || got.errFrames != 1 || got.doneCount != 1 {
				t.Fatalf("must expose original cut terminal: %+v", got)
			}
		})
	}
}

func TestP1ContinuationSendsVendorPrefillMode(t *testing.T) {
	for _, tc := range []struct{ name, base, model, field, path string }{
		{"DeepSeek", "https://api.deepseek.com/v1", "deepseek-chat", "prefix", "/beta/chat/completions"},
		{"Kimi", "https://api.moonshot.cn/v1", "moonshot-v1-8k", "partial", "/v1/chat/completions"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withP03ContinuationSetting(t, true)
			upstream, log := newP03Upstream(t, []string{p03Frame1a, p03Frame1b, p03Frame1c}, []string{p03Frame2a, p03Frame2z, p03Done})
			exec := newP03Executor(t, upstream.URL)
			_, _, err := p03ExecuteCandidate(t, exec, upstream.URL, tc.base, tc.model)
			if err != nil {
				t.Fatal(err)
			}
			if log.count() != 2 {
				t.Fatalf("supported upstream must continue once; calls=%d", log.count())
			}
			var body struct {
				Messages []map[string]any `json:"messages"`
			}
			if err := json.Unmarshal([]byte(log.body(1)), &body); err != nil {
				t.Fatal(err)
			}
			tail := body.Messages[len(body.Messages)-1]
			if tail[tc.field] != true {
				t.Fatalf("missing %s=true on assistant prefill: %s", tc.field, log.body(1))
			}
			if log.paths[1] != tc.path {
				t.Fatalf("continuation path=%s, want %s", log.paths[1], tc.path)
			}
		})
	}
}

func TestP1ContinuationNeverResumesStartedToolCall(t *testing.T) {
	for _, tool := range []string{
		`data:{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_no_space"}]}}]}` + "\n\n",
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"run","arguments":"{"}}]}}]}` + "\n\n",
		`data: {"choices":[{"index":0,"delta":{}},{"index":1,"delta":{"tool_calls":[{"index":0,"id":"call_2"}]}}]}` + "\n\n",
		`data: {"choices":[{"index":0,"delta":{"function_call":{"name":"run","arguments":"{"}}}]}` + "\n\n",
	} {
		t.Run(tool, func(t *testing.T) {
			calls := 0
			ctx := executors.WithStreamContinuation(context.Background(), func(string) (io.ReadCloser, error) {
				calls++
				return io.NopCloser(strings.NewReader(p03Frame2z + p03Done)), nil
			})
			resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(p03Frame1a + p03Frame1b + tool)), Request: httptest.NewRequest(http.MethodPost, "http://upstream.test/v1/chat/completions", nil).WithContext(ctx)}
			recorder := httptest.NewRecorder()
			out := streaming.StreamChatWithPendingCapture(ctx, recorder, resp, "p03-model", "p03-model", nil, nil, true, nil, nil)
			if calls != 0 {
				t.Fatalf("a tool call cannot be reconstructed from text prefill; hook calls=%d", calls)
			}
			if !out.Interrupted || out.Reason != "eof_without_done" {
				t.Fatalf("cut tool call must retain original terminal: %+v", out)
			}
			if !strings.Contains(recorder.Body.String(), "call_") && !strings.Contains(recorder.Body.String(), "function_call") {
				t.Fatalf("tool-call precondition missing from client wire: %s", recorder.Body.String())
			}
		})
	}
}

func TestP1ContinuationDisabledWithoutSettingsRegistry(t *testing.T) {
	previous := settings.Global
	settings.Global = nil
	t.Cleanup(func() { settings.Global = previous })
	upstream, log := newP03Upstream(t, []string{p03Frame1a, p03Frame1b, p03Frame1c}, []string{p03Frame2a, p03Frame2z, p03Done})
	recorder, _, _ := p03Execute(t, newP03Executor(t, upstream.URL), upstream.URL)
	if log.count() != 1 {
		t.Fatalf("missing settings must keep continuation disabled: %d calls", log.count())
	}
	got := parseP03ClientStream(recorder.Body.String())
	if got.errFrames != 1 || got.doneCount != 1 {
		t.Fatalf("original cut terminal missing: %+v", got)
	}
}
