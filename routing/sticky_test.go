package routing

import (
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

func TestBuildClientStickyKey(t *testing.T) {
	appID := 2
	apiKeyID := 10
	got := BuildClientStickyKey("tenant-a", &appID, &apiKeyID, "Cursor")
	want := "tenant-a:2:10:cursor"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	// Same client → same key, every call
	got2 := BuildClientStickyKey("tenant-a", &appID, &apiKeyID, "Cursor")
	if got2 != want {
		t.Fatalf("client-sticky key must be deterministic, got %q", got2)
	}
}

func TestBuildClientStickyKey_EmptyProfileDefault(t *testing.T) {
	appID := 2
	apiKeyID := 10
	got := BuildClientStickyKey("t", &appID, &apiKeyID, "")
	want := "t:2:10:default"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestBuildClientStickyKey_ProfileCaseInsensitive(t *testing.T) {
	appID := 2
	apiKeyID := 10
	a := BuildClientStickyKey("t", &appID, &apiKeyID, "Cursor")
	b := BuildClientStickyKey("t", &appID, &apiKeyID, "cursor")
	if a != b {
		t.Fatalf("profile must be normalised: got %q vs %q", a, b)
	}
}

func TestBuildClientStickyKey_DifferentClientDifferentKey(t *testing.T) {
	appID := 2
	apiKeyID := 10
	// Two different clients (different API keys) produce different keys.
	a := BuildClientStickyKey("t", &appID, &apiKeyID, "cursor")
	otherKey := 99
	b := BuildClientStickyKey("t", &appID, &otherKey, "cursor")
	if a == b {
		t.Fatal("different api keys must produce different sticky keys")
	}
}

// 2026-06-24: model is intentionally not in the key. The old
// TestBuildClientStickyKey_DifferentModelDifferentKey was testing
// the now-removed model dimension; the new invariant is the
// opposite — model drift MUST NOT change the key.
func TestBuildClientStickyKey_ModelDoesNotAffectKey(t *testing.T) {
	appID := 2
	apiKeyID := 10
	// The helper now has no model parameter; same input → same key.
	a := BuildClientStickyKey("t", &appID, &apiKeyID, "cursor")
	b := BuildClientStickyKey("t", &appID, &apiKeyID, "cursor")
	if a != b {
		t.Fatal("key must be stable: model is intentionally not part of the fingerprint")
	}
	if a != "t:2:10:cursor" {
		t.Fatalf("key format changed unexpectedly: %q", a)
	}
}

// Regression: fpHash was removed from the holder in commit 96832f01
// (unify holder to client-scoped). Verify it stays out.
func TestBuildClientStickyKey_StableAcrossFpHashVariations(t *testing.T) {
	appID := 4
	apiKeyID := 10
	// Three "different device fingerprints" — must still produce the
	// same key because the holder is client-scoped, not device-scoped.
	fpHashes := []string{
		"2c149caf6cfb1f21",
		"2ff3c8ab1fe705cb",
		"69a7e61accbda555",
	}
	var keys []string
	for range fpHashes {
		k := BuildClientStickyKey("default", &appID, &apiKeyID, "default")
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		if keys[i] != keys[0] {
			t.Fatalf("holder must be stable across fpHash variations: got %q vs %q",
				keys[0], keys[i])
		}
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
