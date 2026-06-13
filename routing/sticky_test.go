package routing

import (
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/errorsx"
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

// 2026-06-13: recordStickyFailure must NOT count network/timeout/upstream-down/
// client-bug kinds toward the failure threshold. Three TCP resets in an
// hour should not silently unbind the sticky session.
func TestRecordStickyFailure_IgnoresTransientKinds(t *testing.T) {
	e := &Executor{Router: &Router{Sticky: NewStickyCache()}}
	const stickyKey = "tenant:1:1:default:sess-abc"
	const credID = 12

	e.Router.Sticky.Set(stickyKey, credID, 10*time.Minute)
	// 5 rounds of transient / client-bug kinds. None of them should
	// count toward the sticky-failure threshold.
	transientKinds := []errorsx.ErrorKind{
		errorsx.KindNetwork,
		errorsx.KindTimeout,
		errorsx.KindUpstreamDown,
		errorsx.KindModelNotFound,
		errorsx.KindToolCallIdMismatch,
		errorsx.KindUnsupportedFeature,
		errorsx.KindContextLength,
		errorsx.KindCanceled,
	}
	for i := 0; i < 5; i++ {
		for _, kind := range transientKinds {
			e.recordStickyFailure(&ExecParams{StickyKey: stickyKey}, credID, kind)
		}
	}
	// Sticky entry must still be present and still bound to the original cred.
	bound, _, ok := e.Router.Sticky.GetEntry(stickyKey)
	if !ok {
		t.Fatal("sticky entry must survive transient / client-bug failures")
	}
	if bound != credID {
		t.Fatalf("sticky credential changed: got %d, want %d", bound, credID)
	}
}
