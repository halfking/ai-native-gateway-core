package cache

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestIntentStoreSetGet(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	s := NewIntentStore(rdb, 100, time.Minute)

	ctx := context.Background()
	in := Intent{TaskType: "chat", ChosenModel: "gpt-5", CredentialID: 7, HitCount: 0}
	if err := s.Set(ctx, "sess1", in, time.Minute); err != nil {
		t.Fatal(err)
	}
	got, ok := s.Get(ctx, "sess1")
	if !ok || got.ChosenModel != "gpt-5" || got.CredentialID != 7 {
		t.Fatalf("expected gpt-5/7, got %+v ok=%v", got, ok)
	}
	// LRU 命中第二次
	got, ok = s.Get(ctx, "sess1")
	if !ok || got.ChosenModel != "gpt-5" {
		t.Fatalf("LRU hit failed: %+v ok=%v", got, ok)
	}
}

func TestIntentStoreMissReturnsFalse(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	s := NewIntentStore(rdb, 100, time.Minute)
	_, ok := s.Get(context.Background(), "nonexistent")
	if ok {
		t.Fatal("expected miss for nonexistent session")
	}
}

func TestIntentStoreLastSeenStamped(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	s := NewIntentStore(rdb, 100, time.Minute)
	ctx := context.Background()
	in := Intent{TaskType: "chat", ChosenModel: "m"}
	before := time.Now().Unix()
	if err := s.Set(ctx, "s", in, time.Minute); err != nil {
		t.Fatal(err)
	}
	got, ok := s.Get(ctx, "s")
	if !ok {
		t.Fatal("miss after set")
	}
	if got.LastSeen < before {
		t.Fatalf("LastSeen %d < set time %d", got.LastSeen, before)
	}
}
