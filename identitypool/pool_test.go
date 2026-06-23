package identitypool

import (
	"context"
	"testing"
	"time"
)

func TestPool_Disabled(t *testing.T) {
	p := New(Config{Enabled: false, MaxIdentities: 5}, nil)
	ctx := context.Background()

	// When disabled, every identity passes through unchanged.
	got, acquired, err := p.Acquire(ctx, "user-a")
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	if acquired {
		t.Errorf("disabled pool should not mark anything acquired")
	}
	if got != "user-a" {
		t.Errorf("disabled pool should pass identity through, got %q", got)
	}
}

func TestPool_AcquireMemory_BelowCap(t *testing.T) {
	p := New(Config{Enabled: true, MaxIdentities: 3}, nil)
	ctx := context.Background()

	for _, user := range []string{"a", "b", "c"} {
		got, acquired, err := p.Acquire(ctx, Identity(user))
		if err != nil {
			t.Fatalf("Acquire(%q) failed: %v", user, err)
		}
		if !acquired {
			t.Errorf("user %q should be fresh-acquired below cap", user)
		}
		if got != Identity(user) {
			t.Errorf("below cap, got %q want %q", got, user)
		}
	}

	stats := p.Stats(ctx)
	if stats.UsedIdentities != 3 {
		t.Errorf("expected UsedIdentities=3, got %d", stats.UsedIdentities)
	}
}

func TestPool_AcquireMemory_CapReached_Recycle(t *testing.T) {
	p := New(Config{Enabled: true, MaxIdentities: 3}, nil)
	ctx := context.Background()

	// Fill the pool with a, b, c.
	_, _, _ = p.Acquire(ctx, "a")
	_, _, _ = p.Acquire(ctx, "b")
	_, _, _ = p.Acquire(ctx, "c")

	// 4th user should be recycled to the oldest (a).
	got, acquired, err := p.Acquire(ctx, "d")
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	if acquired {
		t.Errorf("cap-reached acquire should not count toward counter")
	}
	if got != "a" {
		t.Errorf("expected recycled identity 'a', got %q", got)
	}
	t.Logf("4th user 'd' was recycled to identity %q", got)
}

func TestPool_AcquireMemory_RepeatUser(t *testing.T) {
	p := New(Config{Enabled: true, MaxIdentities: 3}, nil)
	ctx := context.Background()

	// User a visits twice.
	got1, acquired1, _ := p.Acquire(ctx, "a")
	if !acquired1 {
		t.Errorf("first visit should be acquired")
	}
	if got1 != "a" {
		t.Errorf("first visit should pass through, got %q", got1)
	}

	// Second visit should return the same identity, NOT acquire a new slot.
	got2, acquired2, _ := p.Acquire(ctx, "a")
	if acquired2 {
		t.Errorf("repeat visit should not be re-acquired")
	}
	if got2 != "a" {
		t.Errorf("repeat visit should return same identity, got %q", got2)
	}
}

func TestPool_AcquireMemory_Stats(t *testing.T) {
	p := New(Config{Enabled: true, MaxIdentities: 100, LRUWindow: time.Hour}, nil)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		_, _, _ = p.Acquire(ctx, Identity(string(rune('a'+i))))
	}

	stats := p.Stats(ctx)
	if stats.MaxIdentities != 100 {
		t.Errorf("MaxIdentities mismatch: %d", stats.MaxIdentities)
	}
	if stats.UsedIdentities != 5 {
		t.Errorf("UsedIdentities mismatch: %d", stats.UsedIdentities)
	}
	if stats.WindowSeconds != 3600 {
		t.Errorf("WindowSeconds mismatch: %d", stats.WindowSeconds)
	}
	if stats.BackendMode != "memory" {
		t.Errorf("BackendMode should be memory: %s", stats.BackendMode)
	}
}

func TestPool_AcquireMemory_LRUEviction(t *testing.T) {
	p := New(Config{Enabled: true, MaxIdentities: 3, LRUWindow: 100 * time.Millisecond}, nil)
	ctx := context.Background()

	// Fill the pool.
	_, _, _ = p.Acquire(ctx, "a")
	_, _, _ = p.Acquire(ctx, "b")
	_, _, _ = p.Acquire(ctx, "c")

	// Wait for LRU window to expire.
	time.Sleep(200 * time.Millisecond)

	// After eviction, pool is empty so new identity should be acquired fresh.
	got, acquired, err := p.Acquire(ctx, "d")
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	if !acquired {
		t.Errorf("post-eviction should acquire fresh slot")
	}
	if got != "d" {
		t.Errorf("post-eviction should pass through, got %q", got)
	}
}

func TestHashIdentity_Stable(t *testing.T) {
	h1 := hashIdentity("user-1")
	h2 := hashIdentity("user-1")
	if h1 != h2 {
		t.Errorf("hash should be stable for same input")
	}
	h3 := hashIdentity("user-2")
	if h1 == h3 {
		t.Errorf("different inputs should hash differently")
	}
}
