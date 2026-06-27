//go:build legacy_identity_pool

package identitypool

import (
	"context"
	"testing"
	"time"
)

func TestPool_Disabled(t *testing.T) {
	p := New(Config{Enabled: false, MaxIdentities: 5}, nil)
	ctx := context.Background()
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
	_, _, _ = p.Acquire(ctx, "a")
	_, _, _ = p.Acquire(ctx, "b")
	_, _, _ = p.Acquire(ctx, "c")
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
}

func TestPool_AcquireMemory_RepeatUser(t *testing.T) {
	p := New(Config{Enabled: true, MaxIdentities: 3}, nil)
	ctx := context.Background()
	got1, acquired1, _ := p.Acquire(ctx, "a")
	if !acquired1 || got1 != "a" {
		t.Fatalf("first visit mismatch: got=%q acquired=%v", got1, acquired1)
	}
	got2, acquired2, _ := p.Acquire(ctx, "a")
	if acquired2 || got2 != "a" {
		t.Fatalf("repeat visit mismatch: got=%q acquired=%v", got2, acquired2)
	}
}

func TestPool_AcquireMemory_Stats(t *testing.T) {
	p := New(Config{Enabled: true, MaxIdentities: 100, LRUWindow: time.Hour}, nil)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		_, _, _ = p.Acquire(ctx, Identity(string(rune('a'+i))))
	}
	stats := p.Stats(ctx)
	if stats.MaxIdentities != 100 || stats.UsedIdentities != 5 || stats.WindowSeconds != 3600 || stats.BackendMode != "memory" {
		t.Fatalf("unexpected stats: %+v", stats)
	}
}

func TestPool_AcquireMemory_LRUEviction(t *testing.T) {
	p := New(Config{Enabled: true, MaxIdentities: 3, LRUWindow: 100 * time.Millisecond}, nil)
	ctx := context.Background()
	_, _, _ = p.Acquire(ctx, "a")
	_, _, _ = p.Acquire(ctx, "b")
	_, _, _ = p.Acquire(ctx, "c")
	time.Sleep(200 * time.Millisecond)
	got, acquired, err := p.Acquire(ctx, "d")
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	if !acquired || got != "d" {
		t.Fatalf("post-eviction mismatch: got=%q acquired=%v", got, acquired)
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
