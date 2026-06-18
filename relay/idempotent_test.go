package relay

import (
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestIdempotentCache_CheckAndMark(t *testing.T) {
	c := NewIdempotentCache(100, 5*time.Minute)
	// First call: miss, marks the entry.
	if c.CheckAndMark("sess-1", "req-1") {
		t.Fatal("first call should be a miss")
	}
	// Second call: hit (within TTL).
	if !c.CheckAndMark("sess-1", "req-1") {
		t.Fatal("second call should be a hit")
	}
}

func TestIdempotentCache_DifferentKeysAreMisses(t *testing.T) {
	c := NewIdempotentCache(100, 5*time.Minute)
	c.CheckAndMark("sess-1", "req-1")
	if c.CheckAndMark("sess-1", "req-2") {
		t.Fatal("different requestID should be a miss")
	}
	if c.CheckAndMark("sess-2", "req-1") {
		t.Fatal("different sessionID should be a miss")
	}
}

func TestIdempotentCache_TTLExpiry(t *testing.T) {
	c := NewIdempotentCache(100, 50*time.Millisecond)
	c.CheckAndMark("sess-1", "req-1")
	if !c.CheckAndMark("sess-1", "req-1") {
		t.Fatal("within TTL should be a hit")
	}
	time.Sleep(60 * time.Millisecond)
	if c.CheckAndMark("sess-1", "req-1") {
		t.Fatal("after TTL expiry should be a miss")
	}
}

func TestIdempotentCache_NilSafe(t *testing.T) {
	var c *IdempotentCache
	if c.CheckAndMark("s", "r") {
		t.Fatal("nil cache should return false")
	}
	if c.Len() != 0 {
		t.Fatalf("nil Len: got %d", c.Len())
	}
}

func TestIdempotentCache_EmptyKeyNoOp(t *testing.T) {
	c := NewIdempotentCache(0, 0)
	if c.CheckAndMark("", "r") {
		t.Fatal("empty sessionID should be a miss")
	}
	if c.CheckAndMark("s", "") {
		t.Fatal("empty requestID should be a miss")
	}
}

func TestIdempotentCache_CapEviction(t *testing.T) {
	c := NewIdempotentCache(3, 5*time.Minute)
	c.CheckAndMark("s", "1")
	c.CheckAndMark("s", "2")
	c.CheckAndMark("s", "3")
	if c.Len() != 3 {
		t.Fatalf("expected len=3, got %d", c.Len())
	}
	// Adding a 4th should evict the oldest.
	c.CheckAndMark("s", "4")
	if c.Len() > 3 {
		t.Fatalf("expected len<=3 after eviction, got %d", c.Len())
	}
}

func TestIdempotentCache_Defaults(t *testing.T) {
	c := NewIdempotentCache(0, 0)
	if c.cap != 100 {
		t.Errorf("default cap: got %d, want 100", c.cap)
	}
	if c.ttl != 5*time.Minute {
		t.Errorf("default ttl: got %v, want 5m", c.ttl)
	}
}

func TestIdempotentCache_Concurrent(t *testing.T) {
	c := NewIdempotentCache(1000, 5*time.Minute)
	var wg sync.WaitGroup
	for g := 0; g < 50; g++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				c.CheckAndMark("sess", "req-"+strconv.Itoa(gid%10))
			}
		}(g)
	}
	wg.Wait()
	// No assertion needed — the test is meaningful under -race.
}
