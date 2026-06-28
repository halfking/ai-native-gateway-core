package provider

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestMaybeExitSuspiciousCacheNoopWithoutRedis(t *testing.T) {
	c := NewClient()
	c.maybeExitSuspicious(1, "glm-5.2")
}

func TestMaybeExitSuspiciousNonBlockingAndDispatchesAsyncHook(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ctx := context.Background()
	if err := client.HSet(ctx, "llmgw:avail:7:glm-5.2", map[string]any{
		"state": "suspicious",
	}).Err(); err != nil {
		t.Fatalf("seed redis: %v", err)
	}

	c := NewClient()
	c.redis = client
	called := int32(0)
	c.asyncExitSuspicious = func(credentialID int, rawModel string) {
		atomic.StoreInt32(&called, 1)
	}

	start := time.Now()
	c.maybeExitSuspicious(7, "glm-5.2")
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("maybeExitSuspicious blocked hot path")
	}
	if atomic.LoadInt32(&called) != 1 {
		t.Fatal("expected asyncExitSuspicious to be dispatched")
	}
}
