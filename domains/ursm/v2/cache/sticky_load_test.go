package cache

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newStickyLoadTestStore(t *testing.T) (*StickyLoadStore, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	return NewStickyLoadStore(rdb), mr
}

func TestStickyLoadObserveAndCount(t *testing.T) {
	store, _ := newStickyLoadTestStore(t)
	ctx := context.Background()
	window := 5 * time.Minute

	now := time.Now()
	for _, s := range []string{"sess1", "sess2", "sess3"} {
		if err := store.Observe(ctx, 7, s, now, window); err != nil {
			t.Fatal(err)
		}
	}
	sessions, lastSeen := store.LoadBatch(ctx, []int{7}, window)
	if sessions[7] != 3 {
		t.Fatalf("expected 3 sessions, got %d", sessions[7])
	}
	if lastSeen[7] == 0 {
		t.Fatal("expected lastSeen to be set")
	}

	// 同一会话重复观察不重复计数（member 去重）。
	if err := store.Observe(ctx, 7, "sess1", now.Add(time.Second), window); err != nil {
		t.Fatal(err)
	}
	sessions, _ = store.LoadBatch(ctx, []int{7}, window)
	if sessions[7] != 3 {
		t.Fatalf("expected dedup to 3, got %d", sessions[7])
	}
}

func TestStickyLoadWindowPrunesExpired(t *testing.T) {
	store, _ := newStickyLoadTestStore(t)
	ctx := context.Background()
	window := 5 * time.Minute

	now := time.Now()
	// sess-old 最后活跃在窗口外，sess-new 在窗口内。
	if err := store.Observe(ctx, 9, "sess-old", now.Add(-6*time.Minute), window); err != nil {
		t.Fatal(err)
	}
	if err := store.Observe(ctx, 9, "sess-new", now, window); err != nil {
		t.Fatal(err)
	}
	sessions, lastSeen := store.LoadBatch(ctx, []int{9}, window)
	if sessions[9] != 1 {
		t.Fatalf("expected window to prune to 1, got %d", sessions[9])
	}
	if lastSeen[9] < now.Add(-time.Minute).Unix() {
		t.Fatalf("lastSeen should reflect the surviving member, got %d", lastSeen[9])
	}
}

func TestStickyLoadMissingCredentialIsZero(t *testing.T) {
	store, _ := newStickyLoadTestStore(t)
	sessions, lastSeen := store.LoadBatch(context.Background(), []int{404}, 5*time.Minute)
	if sessions[404] != 0 || lastSeen[404] != 0 {
		t.Fatalf("expected zero view, got %d/%d", sessions[404], lastSeen[404])
	}
}

func TestStickyLoadRemove(t *testing.T) {
	store, _ := newStickyLoadTestStore(t)
	ctx := context.Background()
	window := 5 * time.Minute
	if err := store.Observe(ctx, 5, "sess1", time.Now(), window); err != nil {
		t.Fatal(err)
	}
	if err := store.Remove(ctx, 5, "sess1"); err != nil {
		t.Fatal(err)
	}
	sessions, _ := store.LoadBatch(ctx, []int{5}, window)
	if sessions[5] != 0 {
		t.Fatalf("expected 0 after Remove, got %d", sessions[5])
	}
}

func TestStickyLoadKeyTTLCleanup(t *testing.T) {
	store, mr := newStickyLoadTestStore(t)
	ctx := context.Background()
	window := 5 * time.Minute
	if err := store.Observe(ctx, 11, "sess1", time.Now(), window); err != nil {
		t.Fatal(err)
	}
	ttl := mr.TTL(StickyLoadKey(11))
	if ttl <= window {
		t.Fatalf("expected key TTL > window (idle self-cleanup), got %v", ttl)
	}
}
