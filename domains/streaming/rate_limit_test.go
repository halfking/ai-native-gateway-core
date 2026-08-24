package streaming

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/middleware"
	"github.com/kaixuan/llm-gateway-go/ratelimit"
)

func TestCheckGatewayRateLimit_StaticDataPlaneKeySkipsSharedRPM(t *testing.T) {
	ratelimit.EnableRateLimit()
	limiter := ratelimit.NewSlidingWindowLimiter()
	t.Cleanup(limiter.Stop)
	limit := 1
	keyInfo := &authentication.KeyInfo{ID: 105, RateLimitRPM: &limit}

	if outcome := checkGatewayRateLimit(context.Background(), keyInfo, limiter); outcome.Blocked {
		t.Fatal("first database key request should be allowed")
	}
	if outcome := checkGatewayRateLimit(context.Background(), keyInfo, limiter); !outcome.Blocked {
		t.Fatal("database key must remain subject to its RPM limit")
	}

	ctx := middleware.RegisterAuthOwnerUser(context.Background(), "global-auth-passed")
	if outcome := checkGatewayRateLimit(ctx, keyInfo, limiter); !outcome.Skipped {
		t.Fatal("verified static data-plane key should bypass the shared RPM limit")
	}
}
