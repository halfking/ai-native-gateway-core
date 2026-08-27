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
		if outcome := checkGatewayRateLimit(context.Background(), keyInfo, limiter, nil); outcome.Blocked {
			t.Fatalf("database key request %d should be allowed", i+1)
		}
	}
	ctx1, cancel1 := context.WithCancel(context.Background())
	ctx2, cancel2 := context.WithCancel(context.Background())
	waiting := make(chan rateLimitOutcome, 2)
	go func() { waiting <- checkGatewayRateLimit(ctx1, keyInfo, limiter, nil) }()
	go func() { waiting <- checkGatewayRateLimit(ctx2, keyInfo, limiter, nil) }()
	time.Sleep(time.Millisecond)
	if outcome := checkGatewayRateLimit(context.Background(), keyInfo, limiter, nil); !outcome.Blocked {
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
	if outcome := checkGatewayRateLimit(ctx, keyInfo, limiter, nil); !outcome.Skipped {
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
	outcome := checkGatewayRateLimit(ctx, keyInfo, limiter, nil)
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
		name       string
		path       string
		body       string
		wantStatus int
		wrap       func(*ChatHandler) http.Handler
	}{
		{
			name:       "responses",
			path:       "/v1/responses",
			body:       `{"model":"kimi-k3","input":"hi"}`,
			wantStatus: http.StatusTooManyRequests,
			wrap:       func(h *ChatHandler) http.Handler { return NewResponsesHandler(h) },
		},
		{
			name:       "messages",
			path:       "/v1/messages",
			body:       `{"model":"kimi-k3","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`,
			wantStatus: 529,
			wrap:       func(h *ChatHandler) http.Handler { return NewMessagesHandler(h) },
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := NewChatHandler(nil, nil, nil, nil, nil, nil)
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
