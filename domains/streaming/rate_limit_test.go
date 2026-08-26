package streaming

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/middleware"
	"github.com/kaixuan/llm-gateway-go/ratelimit"
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
