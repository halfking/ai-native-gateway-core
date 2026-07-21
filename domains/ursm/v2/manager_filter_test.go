package v2

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestFilterAndScoreRedisErrorProtectsRejection(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	mgr := New(Dependencies{Redis: rdb, Config: DefaultConfig()})
	_ = mgr.SetReady(context.Background(), true)
	mr.Close() // 强制 redis 错误
	_, err := mgr.FilterAndScore(context.Background(), []CandidateSeed{{
		ProviderID: 1, CredentialID: 1, RawModel: "m", TenantID: "t",
	}})
	if err == nil {
		t.Fatalf("redis error must surface")
	}
}
