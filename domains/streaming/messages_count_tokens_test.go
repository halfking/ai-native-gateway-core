package streaming

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/ratelimit"
)

// ── /v1/messages/count_tokens ──────────────────────────────────────────────

func newCountTokensTestHandler() *CountTokensHandler {
	ch := &ChatHandler{}
	return NewCountTokensHandler(ch)
}

func TestCountTokens_ReturnsEstimate(t *testing.T) {
	h := newCountTokensTestHandler()
	body := `{"model":"minimax-m3","max_tokens":4096,"system":[{"type":"text","text":"You are helpful."}],"messages":[{"role":"user","content":[{"type":"text","text":"hello world this is a counting test"}]}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		InputTokens int `json:"input_tokens"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v; body=%s", err, rec.Body.String())
	}
	if resp.InputTokens <= 0 {
		t.Fatalf("input_tokens = %d, want > 0", resp.InputTokens)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type = %q, want application/json", ct)
	}
}

func TestCountTokens_MethodNotAllowed(t *testing.T) {
	h := newCountTokensTestHandler()
	req := httptest.NewRequest(http.MethodGet, "/v1/messages/count_tokens", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "type") {
		t.Errorf("error body not anthropic-shaped: %s", rec.Body.String())
	}
}

func TestCountTokens_UnknownFieldsTolerated(t *testing.T) {
	// Claude Code sends anthropic-beta payloads with context_management and
	// other fields the gateway does not model; estimation must not 4xx.
	h := newCountTokensTestHandler()
	body := `{"model":"claude-x","context_management":{"edits":[{"type":"clear_tool_uses_20250919"}]},"messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":1024}}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", strings.NewReader(body))
	req.Header.Set("anthropic-beta", "context-management-2025-06-27")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
}

// ── RPM 闸（2026-10-30 补上，218 号登记的 P2 缺口）─────────────────────────

// countingBody 记录自己有没有被读过。
type countingBody struct {
	reader io.Reader
	read   bool
}

func (b *countingBody) Read(p []byte) (int, error) {
	b.read = true
	return b.reader.Read(p)
}

func (b *countingBody) Close() error { return nil }

// TestCountTokens_RateLimitedDoesNotReadBody 钉住**闸位**：
// 被限流的请求**不得**再去读那个最多 128MB 的 body。
//
// ★ 为什么这条要单独钉：补这道闸的**全部意义**就是「限流挡住网关自己的
//   算力与内存开销」⇒ 若闸被放到读 body 之后，被拒的请求已经付掉了全部
//   读取成本 ⇒ 限流就只剩一个「拒绝」语义、挡不住消耗。
// ⇒ 这是一条**位置断言**，不是行为断言：把闸挪到 body 读取之后，
//   状态码仍然是 529、测试若只断言状态码就会照样绿 ⇒ 必须显式断言 body 未被读。
func TestCountTokens_RateLimitedDoesNotReadBody(t *testing.T) {
	ratelimit.EnableRateLimit()
	limit := 1
	keyInfo := &authentication.KeyInfo{ID: 9901, TenantID: "tenant-ct", RateLimitRPM: &limit}

	h := NewChatHandler(nil, nil, nil, nil, nil, nil)
	h.setRequestKeyVerifierForTest(&stubKeyVerifier{info: keyInfo})
	// budgetExceededLimiter ⇒ 闸必然判定为超预算 ⇒ 走 529 分支。
	h.rateLimiter = budgetExceededLimiter{estimatedWaitSec: 7}

	body := &countingBody{reader: strings.NewReader(
		`{"model":"claude-x","messages":[{"role":"user","content":"hi"}]}`)}
	req := httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", nil)
	req.Body = body
	req.Header.Set("Authorization", "Bearer sk-test")
	// ★ 必须带 deadline：checkGatewayRateLimit 只在 ctx 有 deadline 时才走
	//   AdmitRPMWithBudget（预算化准入）这条路径；无 deadline 时走的是
	//   AdmitRPMWithWait，而 budgetExceededLimiter 只实现了前者
	//   ⇒ 不带 deadline 这个桩不会被触发，测试会假绿。
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(30*time.Second))
	defer cancel()
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()

	NewCountTokensHandler(h).ServeHTTP(rec, req)

	if rec.Code != 529 {
		t.Fatalf("status = %d, want 529 (same family as /v1/messages); body=%s", rec.Code, rec.Body.String())
	}
	if body.read {
		t.Fatal("rate-limited request must NOT read the request body — " +
			"the RPM gate is what stops the gateway from paying that read cost")
	}
	if got := rec.Header().Get(ratelimit.GatewayRateLimitScopeHeader); got != ratelimit.GatewayRateLimitScopeSharedKey {
		t.Errorf("gateway scope header = %q, want %q", got, ratelimit.GatewayRateLimitScopeSharedKey)
	}
	// Anthropic 形状的错误信封。
	var env struct {
		Type  string `json:"type"`
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v; body=%s", err, rec.Body.String())
	}
	if env.Type != "error" || env.Error.Type != "rate_limit_error" {
		t.Errorf("error envelope = %+v, want {type:error, error.type:rate_limit_error}", env)
	}
}

// TestCountTokens_WithinLimitStillCountsTokens 钉住反向面：
// 没被限流时**必须**照常返回 token 估算（补闸不能把正常路径打死）。
func TestCountTokens_WithinLimitStillCountsTokens(t *testing.T) {
	ratelimit.EnableRateLimit()
	limit := 10
	keyInfo := &authentication.KeyInfo{ID: 9902, TenantID: "tenant-ct", RateLimitRPM: &limit}

	h := NewChatHandler(nil, nil, nil, nil, nil, nil)
	h.setRequestKeyVerifierForTest(&stubKeyVerifier{info: keyInfo})
	limiter := ratelimit.NewSlidingWindowLimiter()
	t.Cleanup(limiter.Stop)
	h.rateLimiter = limiter

	body := &countingBody{reader: strings.NewReader(
		`{"model":"claude-x","messages":[{"role":"user","content":"hello world"}]}`)}
	req := httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", nil)
	req.Body = body
	req.Header.Set("Authorization", "Bearer sk-test")
	rec := httptest.NewRecorder()

	NewCountTokensHandler(h).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !body.read {
		t.Fatal("allowed request must read the body (the estimate needs it)")
	}
	var resp struct {
		InputTokens int `json:"input_tokens"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v; body=%s", err, rec.Body.String())
	}
	if resp.InputTokens <= 0 {
		t.Fatalf("input_tokens = %d, want > 0", resp.InputTokens)
	}
}

// ── usage-zero 兜底 ────────────────────────────────────────────────────────

func TestPatchAnthropicUsageInput_FillsZeroUsage(t *testing.T) {
	body := []byte(`{"content":[{"type":"text","text":"ok"}],"id":"msg_x","model":"m","role":"assistant","stop_reason":"end_turn","stop_sequence":null,"type":"message","usage":{"input_tokens":0,"output_tokens":0}}`)
	out := patchAnthropicUsageInput(body, 178)
	var resp struct {
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Usage.InputTokens != 178 {
		t.Errorf("input_tokens = %d, want 178", resp.Usage.InputTokens)
	}
	if resp.Usage.OutputTokens != 0 {
		t.Errorf("output_tokens = %d, want 0 (untouched)", resp.Usage.OutputTokens)
	}
}

func TestPatchAnthropicUsageInput_NonZeroUntouched(t *testing.T) {
	body := []byte(`{"usage":{"input_tokens":42,"output_tokens":7},"content":[]}`)
	out := patchAnthropicUsageInput(body, 999)
	if string(out) != string(body) {
		t.Errorf("non-zero usage must be returned unchanged:\n got %s\nwant %s", out, body)
	}
}

func TestPatchAnthropicUsageInput_MalformedSafe(t *testing.T) {
	body := []byte(`not-json`)
	out := patchAnthropicUsageInput(body, 5)
	if string(out) != string(body) {
		t.Errorf("malformed body must pass through unchanged")
	}
}

// 2026-09-21 R51: upstream response with NO usage key at all — the function
// used to bail out and the client kept input_tokens=0; it must now construct
// a usage object carrying the estimate (observed live on Anthropic-format
// bodies whose pipeline dropped usage).
func TestPatchAnthropicUsageInput_MissingUsageKeyConstructs(t *testing.T) {
	body := []byte(`{"id":"msg_x","model":"m","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","type":"message"}`)
	out := patchAnthropicUsageInput(body, 178)
	var resp struct {
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("unmarshal: %v; body=%s", err, out)
	}
	if resp.Usage.InputTokens != 178 {
		t.Errorf("input_tokens = %d, want 178 (constructed usage)", resp.Usage.InputTokens)
	}
	if resp.Usage.OutputTokens != 0 {
		t.Errorf("output_tokens = %d, want 0 (constructed shape)", resp.Usage.OutputTokens)
	}
	// Non-usage fields must survive the reconstruction.
	if !strings.Contains(string(out), `"msg_x"`) {
		t.Errorf("response id lost: %s", out)
	}
}

// R51: input missing/zero while output is real — the old both-zero gate
// refused to backfill; real output must be preserved and only input filled.
func TestPatchAnthropicUsageInput_ZeroInputNonZeroOutput(t *testing.T) {
	body := []byte(`{"usage":{"output_tokens":37},"content":[]}`)
	out := patchAnthropicUsageInput(body, 178)
	var resp struct {
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Usage.InputTokens != 178 {
		t.Errorf("input_tokens = %d, want 178 (backfilled despite output>0)", resp.Usage.InputTokens)
	}
	if resp.Usage.OutputTokens != 37 {
		t.Errorf("output_tokens = %d, want 37 (untouched)", resp.Usage.OutputTokens)
	}
}

// R51: "usage": null must behave like a missing key, not panic on a nil map.
func TestPatchAnthropicUsageInput_NullUsageConstructs(t *testing.T) {
	body := []byte(`{"usage":null,"content":[]}`)
	out := patchAnthropicUsageInput(body, 178)
	var resp struct {
		Usage struct {
			InputTokens int `json:"input_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("unmarshal: %v; body=%s", err, out)
	}
	if resp.Usage.InputTokens != 178 {
		t.Errorf("input_tokens = %d, want 178 (null usage constructed)", resp.Usage.InputTokens)
	}
}

// ── writeNonStreamResponse usage 兜底集成 ──────────────────────────────────

func TestWriteNonStreamResponse_PatchesZeroUsageWithEstimate(t *testing.T) {
	h := &MessagesHandler{chatHandler: &ChatHandler{}}
	// Upstream OpenAI body WITHOUT usage (the shape observed live when the
	// pipeline drops it): converter yields usage 0/0, guard fills estimate.
	upstream := []byte(`{"id":"x1","choices":[{"index":0,"message":{"role":"assistant","content":"<think>hmm</think>\n\nok"},"finish_reason":"stop"}]}`)
	rec := httptest.NewRecorder()
	out := h.writeNonStreamResponse(rec, upstream, "minimax-m3", "req123", 210)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var resp struct {
		Content []map[string]any `json:"content"`
		Usage   struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("unmarshal: %v; body=%s", err, out)
	}
	if resp.Usage.InputTokens != 210 {
		t.Errorf("input_tokens = %d, want 210 (estimate)", resp.Usage.InputTokens)
	}
	// <think> split must survive the patch pass.
	if len(resp.Content) < 2 || resp.Content[0]["type"] != "thinking" {
		t.Errorf("thinking block lost: %+v", resp.Content)
	}
}

func TestWriteNonStreamResponse_RealUsageBeatsEstimate(t *testing.T) {
	h := &MessagesHandler{chatHandler: &ChatHandler{}}
	upstream := []byte(`{"id":"x2","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":178,"completion_tokens":21,"total_tokens":199}}`)
	rec := httptest.NewRecorder()
	out := h.writeNonStreamResponse(rec, upstream, "minimax-m3", "req124", 999)
	var resp struct {
		Usage struct {
			InputTokens int `json:"input_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Usage.InputTokens != 178 {
		t.Errorf("input_tokens = %d, want 178 (real usage wins over estimate)", resp.Usage.InputTokens)
	}
}
