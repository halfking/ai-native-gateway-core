package routing

import (
	"testing"
	"time"
)

func TestBuildSessionStickyKey(t *testing.T) {
	appID := 2
	apiKeyID := 10
	got := BuildSessionStickyKey("tenant-a", &appID, &apiKeyID, "Cursor", "sess-123")
	want := "tenant-a:2:10:cursor:sess-123"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestStickyCacheRecordFailureThreshold(t *testing.T) {
	s := NewStickyCache()
	s.Set("k", 11, 10*time.Minute)
	if evicted := s.RecordFailure("k", 3); evicted {
		t.Fatal("should not evict on first failure")
	}
	if evicted := s.RecordFailure("k", 3); evicted {
		t.Fatal("should not evict on second failure")
	}
	if evicted := s.RecordFailure("k", 3); !evicted {
		t.Fatal("should evict on third failure")
	}
	if _, ok := s.Get("k"); ok {
		t.Fatal("sticky entry should be removed after threshold")
	}
}
