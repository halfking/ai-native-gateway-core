package semantic

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestLookup_ExactMatch_Hit verifies the basic cache hit path.
func TestLookup_ExactMatch_Hit(t *testing.T) {
	c := NewInMemoryCache()
	ctx := context.Background()
	_ = c.Store(ctx, "t1", "gpt-4o", "hash-A", "hello", []byte(`{"reply":"hi"}`), time.Minute)

	got, ok, err := c.Lookup(ctx, "t1", "gpt-4o", "hash-A", "hello")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !ok {
		t.Fatal("expected hit, got miss")
	}
	if string(got) != `{"reply":"hi"}` {
		t.Errorf("got %q, want %q", got, `{"reply":"hi"}`)
	}
}

// TestLookup_MissOnDifferentHash verifies the cache discriminates on hash.
func TestLookup_MissOnDifferentHash(t *testing.T) {
	c := NewInMemoryCache()
	ctx := context.Background()
	_ = c.Store(ctx, "t1", "gpt-4o", "hash-A", "x", []byte("payload"), time.Minute)

	_, ok, _ := c.Lookup(ctx, "t1", "gpt-4o", "hash-B", "x")
	if ok {
		t.Error("expected miss for different hash")
	}
}

// TestLookup_MissOnDifferentTenant is the CRITICAL multi-tenant test:
// tenant B MUST NOT see tenant A's cached response, even with the same
// hash and model (an attacker who knows a hash should not be able to read
// another tenant's data).
func TestLookup_MissOnDifferentTenant(t *testing.T) {
	c := NewInMemoryCache()
	ctx := context.Background()
	_ = c.Store(ctx, "tA", "gpt-4o", "hash-1", "prompt", []byte("secret-A"), time.Minute)

	_, ok, _ := c.Lookup(ctx, "tB", "gpt-4o", "hash-1", "prompt")
	if ok {
		t.Error("tenant B must not see tenant A's cache entry (RLS contract)")
	}
}

// TestLookup_MissOnExpiredEntry verifies TTL-based eviction.
func TestLookup_MissOnExpiredEntry(t *testing.T) {
	c := NewInMemoryCache()
	ctx := context.Background()
	_ = c.Store(ctx, "t1", "gpt-4o", "h", "p", []byte("x"), 10*time.Millisecond)
	time.Sleep(30 * time.Millisecond)

	_, ok, _ := c.Lookup(ctx, "t1", "gpt-4o", "h", "p")
	if ok {
		t.Error("expected miss on expired entry")
	}
}

// TestLookup_InvalidInput_Rejected verifies empty tenant/model/hash is rejected.
func TestLookup_InvalidInput_Rejected(t *testing.T) {
	c := NewInMemoryCache()
	ctx := context.Background()
	cases := []struct{ tenant, model, hash string }{
		{"", "m", "h"},
		{"t", "", "h"},
		{"t", "m", ""},
	}
	for _, tc := range cases {
		_, _, err := c.Lookup(ctx, tc.tenant, tc.model, tc.hash, "p")
		if err == nil {
			t.Errorf("expected error for (%q,%q,%q), got nil", tc.tenant, tc.model, tc.hash)
		}
		if !strings.Contains(err.Error(), "required") {
			t.Errorf("error should say 'required', got: %v", err)
		}
	}
}

// TestStore_OverwritesSameKey verifies idempotent storage.
func TestStore_OverwritesSameKey(t *testing.T) {
	c := NewInMemoryCache()
	ctx := context.Background()
	_ = c.Store(ctx, "t1", "m", "h", "p", []byte("v1"), time.Minute)
	_ = c.Store(ctx, "t1", "m", "h", "p", []byte("v2"), time.Minute)

	got, ok, _ := c.Lookup(ctx, "t1", "m", "h", "p")
	if !ok || string(got) != "v2" {
		t.Errorf("expected overwrite to v2, got %q ok=%v", got, ok)
	}
	if c.Size() != 1 {
		t.Errorf("expected 1 entry after overwrite, got %d", c.Size())
	}
}

// TestStore_TooLarge_Rejected enforces the per-entry size cap.
func TestStore_TooLarge_Rejected(t *testing.T) {
	c := NewInMemoryCache()
	ctx := context.Background()
	huge := make([]byte, MaxPayloadBytes+1)
	err := c.Store(ctx, "t1", "m", "h", "p", huge, time.Minute)
	if err == nil {
		t.Fatal("expected ErrTooLarge for payload > MaxPayloadBytes")
	}
	if !strings.Contains(err.Error(), "max entry size") {
		t.Errorf("error should mention size cap, got: %v", err)
	}
}

// TestInvalidate_RemovesAllModelEntries verifies invalidate clears everything.
func TestInvalidate_RemovesAllModelEntries(t *testing.T) {
	c := NewInMemoryCache()
	ctx := context.Background()
	_ = c.Store(ctx, "t1", "gpt-4o", "h1", "p", []byte("x"), time.Minute)
	_ = c.Store(ctx, "t1", "gpt-4o", "h2", "p", []byte("y"), time.Minute)
	_ = c.Store(ctx, "t1", "claude", "h1", "p", []byte("z"), time.Minute) // different model

	n, err := c.Invalidate(ctx, "t1", "gpt-4o")
	if err != nil {
		t.Fatalf("Invalidate: %v", err)
	}
	if n != 2 {
		t.Errorf("removed = %d, want 2", n)
	}
	if _, ok, _ := c.Lookup(ctx, "t1", "gpt-4o", "h1", "p"); ok {
		t.Error("expected gpt-4o entries to be gone")
	}
	if _, ok, _ := c.Lookup(ctx, "t1", "claude", "h1", "p"); !ok {
		t.Error("claude entry should remain (different model)")
	}
}

// TestStats_TracksLookups verifies the cumulative counters.
func TestStats_TracksLookups(t *testing.T) {
	c := NewInMemoryCache()
	ctx := context.Background()
	_ = c.Store(ctx, "t1", "m", "h1", "p", []byte("x"), time.Minute)

	// 2 hits + 1 miss
	_, _, _ = c.Lookup(ctx, "t1", "m", "h1", "p")
	_, _, _ = c.Lookup(ctx, "t1", "m", "h1", "p")
	_, _, _ = c.Lookup(ctx, "t1", "m", "h2", "p")

	s := c.Stats()
	if s.Lookups != 3 {
		t.Errorf("Lookups = %d, want 3", s.Lookups)
	}
	if s.ExactHits != 2 {
		t.Errorf("ExactHits = %d, want 2", s.ExactHits)
	}
	if s.Misses != 1 {
		t.Errorf("Misses = %d, want 1", s.Misses)
	}
	if got := s.HitRate(); got < 0.66 || got > 0.67 {
		t.Errorf("HitRate = %f, want ~0.667", got)
	}
	if s.Stores != 1 {
		t.Errorf("Stores = %d, want 1", s.Stores)
	}
}

// TestStats_HitRate_ZeroLookups verifies HitRate doesn't divide by zero.
func TestStats_HitRate_ZeroLookups(t *testing.T) {
	c := NewInMemoryCache()
	if got := c.Stats().HitRate(); got != 0 {
		t.Errorf("HitRate with zero lookups = %f, want 0", got)
	}
}

// TestSweepExpired_RemovesOnlyExpired verifies partial expiry.
func TestSweepExpired_RemovesOnlyExpired(t *testing.T) {
	c := NewInMemoryCache()
	ctx := context.Background()
	_ = c.Store(ctx, "t1", "m", "expired", "p", []byte("a"), 10*time.Millisecond)
	_ = c.Store(ctx, "t1", "m", "live", "p", []byte("b"), time.Hour)
	time.Sleep(30 * time.Millisecond)

	removed := c.SweepExpired()
	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}
	if c.Size() != 1 {
		t.Errorf("size after sweep = %d, want 1", c.Size())
	}
}

// TestPayload_DefensiveCopy verifies the caller cannot mutate stored bytes.
func TestPayload_DefensiveCopy(t *testing.T) {
	c := NewInMemoryCache()
	ctx := context.Background()
	original := []byte("secret-payload")
	_ = c.Store(ctx, "t1", "m", "h", "p", original, time.Minute)

	got, _, _ := c.Lookup(ctx, "t1", "m", "h", "p")
	if string(got) != "secret-payload" {
		t.Fatalf("unexpected payload: %q", got)
	}
	// Mutate the returned slice; the stored entry must NOT change.
	got[0] = 'X'
	got2, _, _ := c.Lookup(ctx, "t1", "m", "h", "p")
	if string(got2) != "secret-payload" {
		t.Errorf("payload aliasing: stored entry was mutated via returned slice")
	}
}
