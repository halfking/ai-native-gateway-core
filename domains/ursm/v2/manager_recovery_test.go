package v2

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
)

func TestAuthoritativeRestoreRequiresCoverage(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cfg := DefaultConfig()
	cfg.Mode = api.ModeAuthoritative
	mgr := New(Dependencies{Redis: rdb, Config: cfg})
	ctx := context.Background()
	if err := mgr.SetReady(ctx, false); err != nil {
		t.Fatalf("close gate: %v", err)
	}
	if err := rdb.HSet(ctx, "ursm:v2:node:1:model", "generation", "1", "available", "1").Err(); err != nil {
		t.Fatalf("seed legacy node: %v", err)
	}
	if _, err := mgr.RestoreIfClosed(ctx); err == nil {
		t.Fatal("authoritative restore must reject arbitrary legacy node keys")
	}
	if mgr.Ready(ctx) {
		t.Fatal("gate must remain closed without coverage manifest")
	}
}
