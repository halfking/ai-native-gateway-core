package routing

import (
	"strconv"
	"sync"
	"testing"
)

func TestMnfStreak_IncrementAndGet(t *testing.T) {
	m := NewMnfStreak(100)
	m.Increment("k:1")
	m.Increment("k:1")
	if got := m.Get("k:1"); got != 2 {
		t.Fatalf("expected count=2, got %d", got)
	}
}

func TestMnfStreak_Reset(t *testing.T) {
	m := NewMnfStreak(100)
	m.Increment("k:1")
	m.Increment("k:1")
	m.Reset("k:1")
	if got := m.Get("k:1"); got != 0 {
		t.Fatalf("expected count=0 after reset, got %d", got)
	}
	if m.Len() != 0 {
		t.Fatalf("expected len=0 after reset, got %d", m.Len())
	}
}

func TestMnfStreak_DifferentKeysIsolated(t *testing.T) {
	m := NewMnfStreak(100)
	m.Increment("k:1")
	m.Increment("k:1")
	m.Increment("k:2")
	if got := m.Get("k:1"); got != 2 {
		t.Fatalf("k:1 expected 2, got %d", got)
	}
	if got := m.Get("k:2"); got != 1 {
		t.Fatalf("k:2 expected 1, got %d", got)
	}
}

func TestMnfStreak_BuildKey(t *testing.T) {
	tests := []struct {
		stickyKey string
		credID    int
		want      string
	}{
		{"sk_abc", 1, "sk_abc:1"},
		{"tenant:1:1:profile:user", 42, "tenant:1:1:profile:user:42"},
		{"", 0, ":0"},
	}
	for _, tt := range tests {
		got := BuildMnfStreakKey(tt.stickyKey, tt.credID)
		if got != tt.want {
			t.Errorf("BuildMnfStreakKey(%q, %d) = %q, want %q",
				tt.stickyKey, tt.credID, got, tt.want)
		}
	}
}

func TestMnfStreak_BuildKey_StaysInSyncWithItoa(t *testing.T) {
	for _, id := range []int{0, 1, 12, 999, 12345678, -1} {
		want := "sk:" + strconv.Itoa(id)
		if got := BuildMnfStreakKey("sk", id); got != want {
			t.Errorf("BuildMnfStreakKey drift: cred=%d got=%q want=%q", id, got, want)
		}
	}
}

func TestMnfStreak_EmptyKeyNoOp(t *testing.T) {
	m := NewMnfStreak(100)
	if got := m.Increment(""); got != 0 {
		t.Fatalf("Increment(\"\") should return 0, got %d", got)
	}
	m.Reset("")
	if m.Len() != 0 {
		t.Fatalf("Reset(\"\") should not change map, len=%d", m.Len())
	}
}

func TestMnfStreak_LRUEviction(t *testing.T) {
	m := NewMnfStreak(3)
	m.Increment("a:1")
	m.Increment("b:1")
	m.Increment("c:1")
	if m.Len() != 3 {
		t.Fatalf("expected len=3, got %d", m.Len())
	}
	m.Increment("d:1")
	if m.Len() != 3 {
		t.Fatalf("expected len=3 after eviction, got %d", m.Len())
	}
	if m.Get("a:1") != 0 {
		t.Fatalf("a:1 should have been evicted")
	}
	if m.Get("d:1") != 1 {
		t.Fatalf("d:1 should be present")
	}
}

func TestMnfStreak_Concurrent(t *testing.T) {
	m := NewMnfStreak(1000)
	const goroutines = 50
	const increments = 200
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < increments; i++ {
				key := "k:" + strconv.Itoa(gid%5)
				m.Increment(key)
			}
		}(g)
	}
	wg.Wait()
	for k := 0; k < 5; k++ {
		key := "k:" + strconv.Itoa(k)
		got := m.Get(key)
		want := 2000
		if got != want {
			t.Errorf("key %s: got %d, want %d", key, got, want)
		}
	}
}

func TestMnfStreak_DefaultCap(t *testing.T) {
	m := NewMnfStreak(0)
	if m.cap != 10000 {
		t.Fatalf("expected default cap 10000, got %d", m.cap)
	}
	m = NewMnfStreak(-5)
	if m.cap != 10000 {
		t.Fatalf("expected default cap 10000 for negative input, got %d", m.cap)
	}
}
