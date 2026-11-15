package streaming

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/middleware"
	"github.com/kaixuan/llm-gateway-go/ratelimit"
	dto "github.com/prometheus/client_model/go"
)

type budgetExceededLimiter struct {
	estimatedWaitSec int
}

func (l budgetExceededLimiter) CheckRPM(int, int) bool { return false }

func (l budgetExceededLimiter) RPMStatus(_ int, limit int) (int, int) {
	return limit, 0
}

func (l budgetExceededLimiter) AdmitRPM(context.Context, int, int) (ratelimit.AdmissionResult, error) {
	return ratelimit.AdmissionResult{}, nil
}

func (l budgetExceededLimiter) AdmitRPMWithBudget(_ context.Context, _ int, limit int, _ time.Duration, _ func(ratelimit.AdmissionResult)) (ratelimit.AdmissionResult, error) {
	return ratelimit.AdmissionResult{
		Limit:            limit,
		Position:         1,
		EstimatedWaitSec: l.estimatedWaitSec,
	}, ratelimit.ErrQueueBudgetExceeded
}

func TestCheckGatewayRateLimit_StaticDataPlaneKeySkipsSharedRPM(t *testing.T) {
	ratelimit.EnableRateLimit()
	limiter := ratelimit.NewSlidingWindowLimiter()
	t.Cleanup(limiter.Stop)
	limit := 2
	keyInfo := &authentication.KeyInfo{ID: 105, RateLimitRPM: &limit}

	for i := 0; i < limit; i++ {
		if outcome := checkGatewayRateLimit(context.Background(), keyInfo, limiter, nil, 0); outcome.Blocked {
			t.Fatalf("database key request %d should be allowed", i+1)
		}
	}
	ctx1, cancel1 := context.WithCancel(context.Background())
	ctx2, cancel2 := context.WithCancel(context.Background())
	waiting := make(chan rateLimitOutcome, 2)
	go func() { waiting <- checkGatewayRateLimit(ctx1, keyInfo, limiter, nil, 0) }()
	go func() { waiting <- checkGatewayRateLimit(ctx2, keyInfo, limiter, nil, 0) }()
	time.Sleep(time.Millisecond)
	if outcome := checkGatewayRateLimit(context.Background(), keyInfo, limiter, nil, 0); !outcome.Blocked {
		t.Fatal("request beyond 2L capacity must remain blocked")
	}
	cancel1()
	cancel2()
	for i := 0; i < 2; i++ {
		select {
		case <-waiting:
		case <-time.After(time.Second):
			t.Fatal("waiting request did not observe cancellation")
		}
	}

	ctx := middleware.RegisterAuthOwnerUser(context.Background(), "global-auth-passed")
	if outcome := checkGatewayRateLimit(ctx, keyInfo, limiter, nil, 0); !outcome.Skipped {
		t.Fatal("verified static data-plane key should bypass the shared RPM limit")
	}
}

func TestCheckGatewayRateLimit_QueuedBeyondBudgetFailsFast(t *testing.T) {
	// 2026-08-26 kimi-k3 incident: queued requests burned the whole 60s
	// upstream budget inside the RPM queue and surfaced as bare 502s with
	// no upstream attempt logged. Now: a request whose remaining deadline
	// cannot cover the queue wait is rejected immediately (429/Blocked).
	ratelimit.EnableRateLimit()
	limit := 1
	keyInfo := &authentication.KeyInfo{ID: 777, RateLimitRPM: &limit}
	limiter := budgetExceededLimiter{estimatedWaitSec: 42}

	// The minute bucket has its own clock-sensitive tests. This test locks
	// the gateway contract: a budget-exceeded admission must map to a fast
	// blocked outcome and preserve the limiter's Retry-After estimate.
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(30*time.Second))
	defer cancel()
	start := time.Now()
	outcome := checkGatewayRateLimit(ctx, keyInfo, limiter, nil, 0)
	if !outcome.Blocked {
		t.Fatalf("expected fast block on over-budget queue wait, got %+v", outcome)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("over-budget queue must reject fast, took %v", elapsed)
	}
	if outcome.ResetSec != 42 {
		t.Fatalf("ResetSec=%d, want limiter estimate 42", outcome.ResetSec)
	}
	if outcome.Reason != "queue_budget_exceeded" {
		t.Fatalf("Reason=%q, want queue_budget_exceeded", outcome.Reason)
	}
}

func TestNonChatHandlers_RecordQueueBudgetRejection(t *testing.T) {
	ratelimit.EnableRateLimit()
	limit := 1
	keyInfo := &authentication.KeyInfo{ID: 778, TenantID: "tenant-rate-limit", RateLimitRPM: &limit}

	tests := []struct {
		name           string
		path           string
		body           string
		wantStatus     int
		wrap           func(*ChatHandler) http.Handler
		enableExecutor bool
		// wantRequestLog 断言被限流时是否**要**写一条请求日志。
		//
		// ★ 2026-10-30：count_tokens 的 RPM 闸不写请求日志 —— 它不是推理面
		//   （不产生上游调用、不计费），仓内既有的 count_tokens 面本来就没有
		//   请求日志路径 ⇒ 这里显式区分，而不是把断言整体放宽。
		//   ⇒ 默认零值 false 会让既有三面失效，故用指针显式声明。
		wantRequestLog *bool
	}{
		{
			name:       "chat completions",
			path:       "/v1/chat/completions",
			body:       `{"model":"kimi-k3","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`,
			wantStatus: http.StatusTooManyRequests,
			wrap:       func(h *ChatHandler) http.Handler { return h },
			// chat 路径的限流检查在 serveWithExecutor 内，executor/provider
			// 为 nil 时 serveHTTPInner 会在限流前以 503 提前返回。
			enableExecutor: true,
			wantRequestLog: boolPtr(true),
		},
		{
			name:           "responses",
			path:           "/v1/responses",
			body:           `{"model":"kimi-k3","input":"hi"}`,
			wantStatus:     http.StatusTooManyRequests,
			wrap:           func(h *ChatHandler) http.Handler { return NewResponsesHandler(h) },
			wantRequestLog: boolPtr(true),
		},
		{
			name: "messages",
			path: "/v1/messages",
			body: `{"model":"kimi-k3","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`,
			// 2026-11-15 改判：529 → 429（限流统一按 key 轮——Anthropic 协议
			// 529=overloaded_error，限流是 429 rate_limit_error）。
			wantStatus:     http.StatusTooManyRequests,
			wrap:           func(h *ChatHandler) http.Handler { return NewMessagesHandler(h) },
			wantRequestLog: boolPtr(true),
		},
		{
			// ★ 2026-10-30（ingressguard P2 缺口收口轮）：count_tokens 补上了
			//   RPM 闸 ⇒ 它也在这张表里。
			//   ★ 2026-11-15 改判：429 ⇒ 与全仓标准对齐（原与 /v1/messages 的
			//     旧 529 同口径，两处一并改）。
			//   ★ 不写请求日志 ⇒ 它不是推理面（不产生上游调用、不计费）。
			name:       "messages count_tokens",
			path:       "/v1/messages/count_tokens",
			body:       `{"model":"kimi-k3","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`,
			wantStatus: http.StatusTooManyRequests,
			wrap:       func(h *ChatHandler) http.Handler { return NewCountTokensHandler(h) },
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := NewChatHandler(nil, nil, nil, nil, nil, nil)
			if tc.enableExecutor {
				// 复用 survival e2e 的空 Executor + noNodesResolver 组合，
				// 让请求越过 serveHTTPInner 的 executor 闸门进入限流检查。
				h.executor = &executors.Executor{}
				h.provider = noNodesResolver{}
			}
			h.setRequestKeyVerifierForTest(&stubKeyVerifier{info: keyInfo})
			h.rateLimiter = budgetExceededLimiter{estimatedWaitSec: 42}
			var entries []*telemetry.RequestLogEntry
			h.requestLogHook = func(entry *telemetry.RequestLogEntry) {
				entries = append(entries, entry)
			}

			ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(30*time.Second))
			defer cancel()
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body)).WithContext(ctx)
			req.Header.Set("Authorization", "Bearer sk-test")
			res := httptest.NewRecorder()
			tc.wrap(h).ServeHTTP(res, req)

			if res.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", res.Code, tc.wantStatus, res.Body.String())
			}
			if got := res.Header().Get(ratelimit.GatewayRateLimitScopeHeader); got != ratelimit.GatewayRateLimitScopeSharedKey {
				t.Errorf("gateway scope header = %q, want %q", got, ratelimit.GatewayRateLimitScopeSharedKey)
			}
			// ★ 请求日志：期望写则必须恰好 1 条且状态为 rate_limited；
			//   不期望写则必须恰好 0 条（count_tokens 不是推理面）。
			wantLog := tc.wantRequestLog != nil && *tc.wantRequestLog
			if !wantLog {
				if len(entries) != 0 {
					t.Fatalf("request log entries = %d, want 0 (non-inference plane)", len(entries))
				}
				return
			}
			if len(entries) != 1 {
				t.Fatalf("request log entries = %d, want 1", len(entries))
			}
			entry := entries[0]
			if entry.RequestStatus == nil || *entry.RequestStatus != telemetry.RequestStatusRateLimited {
				t.Fatalf("request status = %v, want %q", entry.RequestStatus, telemetry.RequestStatusRateLimited)
			}
			if entry.ErrorKind == nil || *entry.ErrorKind != "rate_limit_exceeded" {
				t.Fatalf("error kind = %v, want rate_limit_exceeded", entry.ErrorKind)
			}
		})
	}
}

func TestChatHandlerMarksThrottledSharedKeyResponse(t *testing.T) {
	h := NewChatHandler(nil, nil, nil, nil, nil, nil)
	// 同 TestNonChatHandlers_RecordQueueBudgetRejection/chat completions：
	// 限流检查位于 serveWithExecutor 内，需要 executor+provider 越闸。
	h.executor = &executors.Executor{}
	h.provider = noNodesResolver{}
	h.setRequestKeyVerifierForTest(&stubKeyVerifier{info: &authentication.KeyInfo{ID: 779, Status: "throttled"}})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"kimi-k3","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer sk-test")
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429; body=%s", res.Code, res.Body.String())
	}
	if got := res.Header().Get(ratelimit.GatewayRateLimitScopeHeader); got != ratelimit.GatewayRateLimitScopeSharedKey {
		t.Fatalf("gateway scope header = %q, want %q", got, ratelimit.GatewayRateLimitScopeSharedKey)
	}
}

func TestRecordGatewayRateLimitRejection_UsesAdmissionReason(t *testing.T) {
	before := readRateLimitRejectionCounter(t, "queue_budget_exceeded")
	recordGatewayRateLimitRejection(rateLimitOutcome{Blocked: true, Reason: "queue_budget_exceeded"})
	if got := readRateLimitRejectionCounter(t, "queue_budget_exceeded"); got != before+1 {
		t.Fatalf("counter = %v, want %v", got, before+1)
	}
}

func readRateLimitRejectionCounter(t *testing.T, reason string) float64 {
	t.Helper()
	var metric dto.Metric
	if err := gatewayRateLimitRejectionsTotal.WithLabelValues(reason).Write(&metric); err != nil {
		t.Fatalf("read rate-limit rejection counter: %v", err)
	}
	return metric.GetCounter().GetValue()
}

func TestNotifyRateLimitWaitWritesSSEEventOnlyForStreamingClient(t *testing.T) {
	streamRequest := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	streamRequest.Header.Set("Accept", "text/event-stream")
	streamRecorder := httptest.NewRecorder()
	notifyRateLimitWait(streamRecorder, true)(ratelimit.AdmissionResult{Waiting: true, Position: 2})
	if got := streamRecorder.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", got)
	}
	if body := streamRecorder.Body.String(); !strings.Contains(body, "event: rate_limit_waiting") || !strings.Contains(body, `"position":2`) {
		t.Fatalf("waiting event = %q", body)
	}

	nonStreamRecorder := httptest.NewRecorder()
	if notify := notifyRateLimitWait(nonStreamRecorder, false); notify != nil {
		t.Fatal("non-streaming request must not receive a pre-response waiting event")
	}
}

// tpmRecordingLimiter 是可编程的 TPM 检查桩：记录最后一次记账参数，
// RPM 恒放行（隔离 TPM 语义），reject 臂可编程。
type tpmRecordingLimiter struct {
	gotKey    int
	gotTokens int
	gotLimit  int
	calls     int
	reject    bool
}

func (l *tpmRecordingLimiter) CheckRPM(int, int) bool { return true }
func (l *tpmRecordingLimiter) RPMStatus(_ int, limit int) (int, int) {
	return 0, limit
}
func (l *tpmRecordingLimiter) CheckTPM(keyID int, estimatedTokens int, limit int) bool {
	l.calls++
	l.gotKey, l.gotTokens, l.gotLimit = keyID, estimatedTokens, limit
	return !l.reject
}

// TestCheckGatewayRateLimit_TPMLimitBlocks 钉 2026-11-15 TPM 接线（死字段
// 复活）：EffectiveTPM>0 且限流器支持 TPM ⇒ RPM 通过后按入口估计预检；
// 超限 Reason="tpm_limit"（走各面现有 Blocked 渲染路径）。estimate=0 的面
// （count_tokens 等读 body 前挡住的面）不预记账——「没法估」不得变成「无限」
// 的反面：skip 是登记的口径边界，不是豁免。
func TestCheckGatewayRateLimit_TPMLimitBlocks(t *testing.T) {
	ratelimit.EnableRateLimit()
	rpm := 1000
	tpm := 5000
	keyInfo := &authentication.KeyInfo{ID: 321, RateLimitRPM: &rpm, RateLimitTPM: &tpm}
	lim := &tpmRecordingLimiter{}

	outcome := checkGatewayRateLimit(context.Background(), keyInfo, lim, nil, 1000)
	if outcome.Blocked {
		t.Fatalf("5000 TPM 槽内首笔（估计 1000）应放行: %+v", outcome)
	}
	if lim.calls != 1 || lim.gotKey != 321 || lim.gotTokens != 1000 || lim.gotLimit != 5000 {
		t.Fatalf("TPM 预检参数不符: calls=%d key=%d tokens=%d limit=%d", lim.calls, lim.gotKey, lim.gotTokens, lim.gotLimit)
	}

	// 超限臂：后续调用拒绝。
	lim.reject = true
	outcome = checkGatewayRateLimit(context.Background(), keyInfo, lim, nil, 6000)
	if !outcome.Blocked || outcome.Reason != "tpm_limit" || outcome.Limit != 5000 {
		t.Fatalf("TPM 超限必须 Blocked/tpm_limit: %+v", outcome)
	}

	// 无估计值 ⇒ 跳过 TPM（口径边界：count_tokens 类面）。
	lim2 := &tpmRecordingLimiter{}
	if outcome := checkGatewayRateLimit(context.Background(), keyInfo, lim2, nil, 0); outcome.Blocked || lim2.calls != 0 {
		t.Fatalf("estimate=0 不得触发 TPM: outcome=%+v calls=%d", outcome, lim2.calls)
	}

	// 无 TPM 列 ⇒ 不执法（NULL/0 语义）。
	keyNoTPM := &authentication.KeyInfo{ID: 322, RateLimitRPM: &rpm}
	lim3 := &tpmRecordingLimiter{}
	if outcome := checkGatewayRateLimit(context.Background(), keyNoTPM, lim3, nil, 1000); outcome.Blocked || lim3.calls != 0 {
		t.Fatalf("无 TPM 配置不得执法: outcome=%+v calls=%d", outcome, lim3.calls)
	}
}
