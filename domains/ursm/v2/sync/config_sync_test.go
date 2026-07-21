package sync

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

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
