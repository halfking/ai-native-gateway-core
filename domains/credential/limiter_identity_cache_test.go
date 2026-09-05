package credential

import (
	"testing"
	"time"
)

func TestLimiterIdentityCacheEvictsIdleButRetainsBusyEntry(t *testing.T) {
	l := NewWithLimits(10, 10, 10, 1)
	defer l.Stop()
	l.identityMaxEntries = 2
	l.identityIdleTTL = time.Minute

	busy := l.Identity(1, 1, "busy")
	if !busy.TryAcquire() {
		t.Fatal("acquire busy identity")
	}
	l.Identity(1, 1, "idle")

	l.mu.Lock()
	l.idents["1/1/idle"].lastUsed = time.Now().Add(-2 * time.Minute)
	l.cleanupIdentitiesLocked(time.Now())
	_, busyPresent := l.idents["1/1/busy"]
	_, idlePresent := l.idents["1/1/idle"]
	l.mu.Unlock()
	if !busyPresent {
		t.Fatal("busy identity was evicted")
	}
	if idlePresent {
		t.Fatal("idle identity was not evicted")
	}

	busy.Release()
	l.Identity(1, 1, "first")
	l.Identity(1, 1, "second")
	l.Identity(1, 1, "third")
	l.mu.RLock()
	count := len(l.idents)
	evictions := l.identityEvictions.Load()
	l.mu.RUnlock()
	if count > l.identityMaxEntries {
		t.Fatalf("identity cache size = %d, max = %d", count, l.identityMaxEntries)
	}
	if evictions == 0 {
		t.Fatal("expected an identity cache eviction")
	}
}

func TestLimiterStopIsIdempotent(t *testing.T) {
	l := NewLimiter()
	l.Stop()
	l.Stop()
}
