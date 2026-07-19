package pluginruntime

import (
	"testing"
	"time"
)

func TestNonceCache_FirstSeenThenReplay(t *testing.T) {
	c := NewNonceCache(2 * time.Minute)
	key := "ai-session-manager|t-1|1700000000|n7"
	if !c.SeenFirst(key) {
		t.Fatal("first seen should be true")
	}
	if c.SeenFirst(key) {
		t.Fatal("replay should be rejected (false)")
	}
}

func TestNonceCache_Expiry(t *testing.T) {
	c := NewNonceCache(30 * time.Millisecond)
	key := "k1"
	if !c.SeenFirst(key) {
		t.Fatal("first should be true")
	}
	time.Sleep(60 * time.Millisecond)
	if !c.SeenFirst(key) {
		t.Fatal("after TTL expiry, key should be seen as first again")
	}
}

func TestNonceCache_ConcurrentSameKey(t *testing.T) {
	c := NewNonceCache(time.Minute)
	key := "shared"
	results := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func() { results <- c.SeenFirst(key) }()
	}
	trues := 0
	for i := 0; i < 10; i++ {
		if <-results {
			trues++
		}
	}
	if trues != 1 {
		t.Fatalf("expected exactly 1 first-seen, got %d", trues)
	}
}
