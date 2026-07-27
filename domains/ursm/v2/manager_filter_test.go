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
	// 2026-07-27 (M2): set Mode=authoritative so FilterAndScore actually
	// exercises the read path. DefaultConfig() is ModeOff, which short-
	// circuits before the Redis read — so the test only asserted the error
	// by accident of the old statement ordering. With the LRU fail-open
	// refactor, ModeOff returns (nil,nil) directly; use a real mode + an
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
