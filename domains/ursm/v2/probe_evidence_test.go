package v2

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/store"
	"github.com/redis/go-redis/v9"
)

func TestManagerProbeHealthEvidenceUsesConfiguredPrefixAndIdentity(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()
	cfg := DefaultConfig()
	cfg.RedisKeyPrefix = "custom:evidence:"
	mgr := New(Dependencies{Redis: rdb, Config: cfg})
	key := store.NodeKeyForTenant(cfg.RedisKeyPrefix, "tenant-a", 42, "model-a")
	mr.HSet(key, "available", "1")
	got, err := mgr.ProbeHealthEvidence(context.Background(), "tenant-a", 42, []string{"model-a"})
	if err != nil || len(got) != 1 || !got[0].Known || !got[0].Healthy {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	wrong, err := mgr.ProbeHealthEvidence(context.Background(), "tenant-b", 42, []string{"model-a"})
	if err != nil || wrong[0].Known {
		t.Fatalf("identity leak got=%+v err=%v", wrong, err)
	}
}
