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
	limiter := ratelimit.NewSlidingWindowLimiter()
	t.Cleanup(limiter.Stop)
	limit := 1
	keyInfo := &authentication.KeyInfo{ID: 777, RateLimitRPM: &limit}

	// Pre-fill the single bucket slot via the underlying minute-bucket
	// admission directly (bypasses checkGatewayRateLimit's budget logic so
	// the warm-up is deterministic regardless of where the test starts
	// within the minute). bucket.count=1 → next call is guaranteed to
	// reach the queueing path.
	sliding := limiter
	warmupCtx, warmupCancel := context.WithDeadline(context.Background(), time.Now().Add(60*time.Second))
	defer warmupCancel()
	if _, err := sliding.AdmitRPM(warmupCtx, keyInfo.ID, limit); err != nil {
		t.Fatalf("warm-up admit failed: %v", err)
	}

	// Simulate a request whose remaining budget (deadline 30s = 25s after
	// headroom) cannot cover the ~60s queue wait to the next minute bucket.
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
	if outcome.ResetSec < 30 {
		t.Fatalf("ResetSec=%d, want ~60 (estimated wait to next bucket)", outcome.ResetSec)
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
