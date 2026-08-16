package recovery

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestWarmupSeedsReady(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	m := New(rdb, "ursm:v2:")
	if err := m.WarmupFromSeed(context.Background(), []Seed{
		{ProviderID: 1, CredentialID: 1, RawModel: "m"},
	}); err != nil {
		t.Fatalf("warmup: %v", err)
	}
	if !m.Ready(context.Background()) {
		t.Fatalf("warmup must set ready=1")
	}
}

func TestWarmupNumericTenantUsesTaggedKey(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	m := New(rdb, "ursm:v2:")
	if err := m.WarmupFromSeed(context.Background(), []Seed{{CredentialID: 7, RawModel: "m", TenantID: "123"}}); err != nil {
		t.Fatalf("warmup: %v", err)
	}
	if !mr.Exists("ursm:v2:node:t:123:7:m") {
		t.Fatal("numeric tenant warmup must use tagged node key")
	}
}

func TestWarmupEmptyStaysClosed(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	m := New(rdb, "ursm:v2:")
	if err := m.WarmupFromSeed(context.Background(), nil); err == nil {
		t.Fatalf("empty warmup must error to avoid ready=1 with no data")
	}
	if m.Ready(context.Background()) {
		t.Fatalf("empty warmup must not set ready")
	}
}
