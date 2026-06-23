package routing

import (
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

func TestBuildClientStickyKey(t *testing.T) {
	appID := 2
	apiKeyID := 10
	got := BuildClientStickyKey("tenant-a", &appID, &apiKeyID, "Cursor", "minimax-m3")
	want := "tenant-a:2:10:cursor:minimax-m3"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	// Same client, same model — same key regardless of session
	got2 := BuildClientStickyKey("tenant-a", &appID, &apiKeyID, "Cursor", "minimax-m3")
	if got2 != want {
		t.Fatalf("client-sticky key must be deterministic, got %q", got2)
	}
}

func TestBuildClientStickyKey_EmptyProfileDefault(t *testing.T) {
	appID := 2
	apiKeyID := 10
	got := BuildClientStickyKey("t", &appID, &apiKeyID, "", "gpt-4")
	want := "t:2:10:default:gpt-4"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestBuildClientStickyKey_EmptyModelAsterisk(t *testing.T) {
	appID := 2
	apiKeyID := 10
	got := BuildClientStickyKey("t", &appID, &apiKeyID, "cursor", "")
	want := "t:2:10:cursor:*"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestBuildClientStickyKey_DifferentModelDifferentKey(t *testing.T) {
	appID := 2
	apiKeyID := 10
	a := BuildClientStickyKey("t", &appID, &apiKeyID, "cursor", "gpt-4")
	b := BuildClientStickyKey("t", &appID, &apiKeyID, "cursor", "claude-3")
	if a == b {
		t.Fatal("different models should produce different sticky keys")
	}
}

func TestBuildClientStickyKey_SameClientAcrossSessions(t *testing.T) {
	appID := 2
	apiKeyID := 10
	// Two different "sessions" (we don't pass sessionID at all) produce
	// the same key — which is the whole point.
	k1 := BuildClientStickyKey("t", &appID, &apiKeyID, "cursor", "gpt-4")
	k2 := BuildClientStickyKey("t", &appID, &apiKeyID, "cursor", "gpt-4")
	if k1 != k2 {
		t.Fatal("same client+model must produce identical keys across calls")
	}
}

// Regression test for the fpHash fragmentation bug:
// before the client-scoped refactor, a request without a sessionID
// would build a holder that included SHA256(fpSeed)[:8]. Different
// device headers → different fpHash → different holder → different
// fingerprint slot. Now fpSeed is no longer part of the holder, so
// the same client always lands on the same slot.
func TestBuildClientStickyKey_StableAcrossFpHashVariations(t *testing.T) {
	appID := 4
	apiKeyID := 10
	// Simulate three requests from the same client with three different
	// device fingerprints (e.g. X-Device-Seed rotates, or no X-Device-Seed
	// at all and the gateway falls back to IP+UA hash).
	fpHashes := []string{
		"2c149caf6cfb1f21",
		"2ff3c8ab1fe705cb",
		"69a7e61accbda555",
	}
	var keys []string
	for _, fp := range fpHashes {
		_ = fp // not used in BuildClientStickyKey anymore
		k := BuildClientStickyKey("default", &appID, &apiKeyID, "default", "minimax-m3")
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
