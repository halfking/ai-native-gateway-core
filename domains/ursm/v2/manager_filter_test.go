package v2

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
)

func TestFilterAndScoreRedisErrorProtectsRejection(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	// Use authoritative mode so FilterAndScore exercises the Redis read path.
	// Explicit ModeOff returns (nil, nil) directly; use a real mode plus an
	// empty mirror (cold start) so the miss path requires Redis and the
	// protection-rejection invariant (miss + Redis down → error) is honored.
	cfg := DefaultConfig()
	cfg.Mode = api.ModeAuthoritative
	cfg.LRUMirrorSize = 0 // no mirror → every read must hit Redis
	mgr := New(Dependencies{Redis: rdb, Config: cfg})
	_ = mgr.SetReady(context.Background(), true)
	mr.Close() // 强制 redis 错误
	_, err := mgr.FilterAndScore(context.Background(), []CandidateSeed{{
		ProviderID: 1, CredentialID: 1, RawModel: "m", TenantID: "t",
	}})
	if err == nil {
		t.Fatalf("redis error must surface")
	}
}
