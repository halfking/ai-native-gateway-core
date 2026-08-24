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
