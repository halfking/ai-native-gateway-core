package kv

import (
	"context"
	"testing"
	"time"
)

// TestStore_LookupHit: store → lookup → hit.
func TestStore_LookupHit(t *testing.T) {
	s := NewInMemoryStore()
	ctx := context.Background()
	if err := s.Store(ctx, "key-A", []byte("payload-A"), time.Minute); err != nil {
		t.Fatalf("Store: %v", err)
	}
	got, ok, err := s.Lookup(ctx, "key-A")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !ok {
		t.Fatal("expected hit")
	}
	if string(got) != "payload-A" {
		t.Errorf("got %q, want %q", got, "payload-A")
	}
}

// TestStore_LookupMiss: no prior store → ok=false.
func TestStore_LookupMiss(t *testing.T) {
	s := NewInMemoryStore()
	_, ok, err := s.Lookup(context.Background(), "nope")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if ok {
		t.Error("expected miss for never-stored key")
	}
}

// TestStore_TTLExpiry: after TTL elapses, entry MUST be reported as miss
// (and not panicking on expired read).
func TestStore_TTLExpiry(t *testing.T) {
	s := NewInMemoryStore()
	ctx := context.Background()
	if err := s.Store(ctx, "k", []byte("v"), 20*time.Millisecond); err != nil {
		t.Fatalf("Store: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	_, ok, _ := s.Lookup(ctx, "k")
	if ok {
		t.Error("expected miss after TTL")
	}
}

// TestStore_OverwriteSameKey: storing same key again overwrites.
func TestStore_OverwriteSameKey(t *testing.T) {
	s := NewInMemoryStore()
	ctx := context.Background()
	_ = s.Store(ctx, "k", []byte("v1"), time.Minute)
	_ = s.Store(ctx, "k", []byte("v2"), time.Minute)
	got, ok, _ := s.Lookup(ctx, "k")
	if !ok || string(got) != "v2" {
		t.Errorf("got %q ok=%v, want v2 ok=true", got, ok)
	}
	if s.Size() != 1 {
		t.Errorf("Size = %d, want 1 (overwrite)", s.Size())
	}
}

// TestStore_Invalidate: removing a key returns it to miss.
func TestStore_Invalidate(t *testing.T) {
	s := NewInMemoryStore()
	ctx := context.Background()
	_ = s.Store(ctx, "k", []byte("v"), time.Minute)
	if err := s.Invalidate(ctx, "k"); err != nil {
		t.Fatalf("Invalidate: %v", err)
	}
	_, ok, _ := s.Lookup(ctx, "k")
	if ok {
		t.Error("expected miss after invalidate")
	}
}

// TestStore_InvalidateMissing: invalidating a non-existent key is a no-op
// (no error).
func TestStore_InvalidateMissing(t *testing.T) {
	s := NewInMemoryStore()
	if err := s.Invalidate(context.Background(), "ghost"); err != nil {
		t.Errorf("Invalidate(ghost) returned error: %v", err)
	}
}

// TestStore_StatsHitsMisses: Stats tracks Lookups / Hits / Misses / Stores.
func TestStore_StatsHitsMisses(t *testing.T) {
	s := NewInMemoryStore()
	ctx := context.Background()
	_ = s.Store(ctx, "k", []byte("v"), time.Minute)

	_, _, _ = s.Lookup(ctx, "k")       // hit
	_, _, _ = s.Lookup(ctx, "k")       // hit
	_, _, _ = s.Lookup(ctx, "missing") // miss

	stats := s.Stats()
	if stats.Lookups != 3 {
		t.Errorf("Lookups = %d, want 3", stats.Lookups)
	}
	if stats.Hits != 2 {
		t.Errorf("Hits = %d, want 2", stats.Hits)
	}
	if stats.Misses != 1 {
		t.Errorf("Misses = %d, want 1", stats.Misses)
	}
	if stats.Stores != 1 {
		t.Errorf("Stores = %d, want 1", stats.Stores)
	}
	if stats.HitRate() < 0.66 || stats.HitRate() > 0.67 {
		t.Errorf("HitRate = %f, want ~0.667", stats.HitRate())
	}
}

// TestStore_HitRateZero: HitRate with zero lookups returns 0 (no divide-by-zero).
func TestStore_HitRateZero(t *testing.T) {
	s := NewInMemoryStore()
	if r := s.Stats().HitRate(); r != 0 {
		t.Errorf("HitRate on empty store = %f, want 0", r)
	}
}

// TestStore_DefensiveCopyOnLookup: caller MUST NOT be able to mutate stored
// bytes via the returned slice (same contract as cache/semantic).
func TestStore_DefensiveCopyOnLookup(t *testing.T) {
	s := NewInMemoryStore()
	ctx := context.Background()
	original := []byte("secret")
	_ = s.Store(ctx, "k", original, time.Minute)

	got, _, _ := s.Lookup(ctx, "k")
	got[0] = 'X' // attempt mutation

	got2, _, _ := s.Lookup(ctx, "k")
	if string(got2) != "secret" {
		t.Errorf("returned slice aliases storage: got %q after mutation", got2)
	}
}

// TestStore_DefensiveCopyOnStore: mutating the input after Store MUST NOT
// change the stored entry.
func TestStore_DefensiveCopyOnStore(t *testing.T) {
	s := NewInMemoryStore()
	ctx := context.Background()
	in := []byte("hello")
	_ = s.Store(ctx, "k", in, time.Minute)
	in[0] = 'X'

	got, _, _ := s.Lookup(ctx, "k")
	if string(got) != "hello" {
		t.Errorf("input aliased storage: got %q", got)
	}
}

// TestStore_EmptyKeyRejected: empty key MUST be rejected with an error
// (defensive — prevents accidentally keying into a single global bucket).
func TestStore_EmptyKeyRejected(t *testing.T) {
	s := NewInMemoryStore()
	ctx := context.Background()
	if err := s.Store(ctx, "", []byte("v"), time.Minute); err == nil {
		t.Error("Store with empty key should return error")
	}
	if _, _, err := s.Lookup(ctx, ""); err == nil {
		t.Error("Lookup with empty key should return error")
	}
	if err := s.Invalidate(ctx, ""); err == nil {
		t.Error("Invalidate with empty key should return error")
	}
}

// TestStore_SweepExpired: periodic sweep removes only expired entries.
func TestStore_SweepExpired(t *testing.T) {
	s := NewInMemoryStore()
	ctx := context.Background()
	_ = s.Store(ctx, "live", []byte("a"), time.Hour)
	_ = s.Store(ctx, "dead", []byte("b"), 10*time.Millisecond)
	time.Sleep(30 * time.Millisecond)

	removed := s.SweepExpired()
	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}
	if s.Size() != 1 {
		t.Errorf("Size = %d, want 1", s.Size())
	}
}

// TestStore_PayloadTooLarge: per-entry size cap is enforced (matches
// cache/semantic.MaxPayloadBytes convention).
func TestStore_PayloadTooLarge(t *testing.T) {
	s := NewInMemoryStore()
	tooBig := make([]byte, MaxPayloadBytes+1)
	if err := s.Store(context.Background(), "k", tooBig, time.Minute); err == nil {
		t.Error("expected ErrTooLarge for oversized payload")
	}
}

// TestStore_InterfaceAssertion: the InMemoryStore MUST satisfy the Store
// interface. This is a compile-time guard.
func TestStore_InterfaceAssertion(t *testing.T) {
	var _ Store = (*InMemoryStore)(nil)
	var _ Store = NewInMemoryStore()
}
