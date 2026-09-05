package sync

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestUpsertSeedNumericTenantUsesTaggedKey(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	s := NewSyncer(rdb, "ursm:v2:")
	if err := s.UpsertNodeSeed(context.Background(), Seed{CredentialID: 2, RawModel: "gpt", TenantID: "123", Available: true}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if !mr.Exists("ursm:v2:node:t:123:2:gpt") {
		t.Fatal("numeric tenant sync must use tagged node key")
	}
}

func TestUpsertSeed(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	s := NewSyncer(rdb, "ursm:v2:")
	if err := s.UpsertNodeSeed(context.Background(), Seed{
		ProviderID: 1, CredentialID: 2, RawModel: "gpt", Available: true,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
}
