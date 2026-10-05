// Package compressor - threetier_hook_test.go (2026-09-30, 修订审计三十二轮
// §四A A-G1)
//
// Proves the three-tier provenance check (threetier_hook.go) is live at its
// production call site — SessionCache.Set, reached through the real
// Prepare → updateCache path — and not dead code. The registered verifier
// here is a stub (the real one, threetier.VerifySessionState, has its own
// registration test in the threetier package).
package compression

import (
	"context"
	"sync"
	"testing"
)

// stubThreeTierCheck records every state passed to the hook and returns a
// fixed reason list so the fail path (log + counter) is exercised.
type stubThreeTierCheck struct {
	mu     sync.Mutex
	states []*SessionState
}

func (s *stubThreeTierCheck) fn(st *SessionState) []string {
	s.mu.Lock()
	s.states = append(s.states, st)
	s.mu.Unlock()
	return []string{"stub: deliberate misalignment for test"}
}

func (s *stubThreeTierCheck) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.states)
}

// TestSet_InvokesThreeTierCheckOnRealPreparePath is the A-G1 load-bearing
// test: a normal (non-V2) Prepare must reach SessionCache.Set, which must
// invoke the registered three-tier verifier, and a misalignment verdict must
// increment compression_threetier_check_total{result="fail"} WITHOUT failing
// the request — Prepare still returns normally.
func TestSet_InvokesThreeTierCheckOnRealPreparePath(t *testing.T) {
	withPlatformFlag(t, "sessions_v2_compression_read", false) // force V1 path

	stub := &stubThreeTierCheck{}
	SetThreeTierCheck(stub.fn)
	t.Cleanup(func() { SetThreeTierCheck(nil) })

	rec := &recordingBackend{}
	sc := &SessionCompressor{deps: SessionCompressorDeps{
		Cache: NewSessionCache(rec, nil),
	}}
	clientBody := []byte(`{"messages":[{"role":"user","content":"first"}]}`)
	failBefore := ThreeTierCheckCount(ThreeTierResultFail)

	res := sc.Prepare(context.Background(), clientBody, "t1", "gw_threetier01", "openai", 0, false)
	if res == nil {
		t.Fatal("Prepare returned nil — request must not be blocked by the check")
	}
	if stub.count() == 0 {
		t.Fatal("three-tier verifier never invoked: call site is dead code")
	}
	if got := ThreeTierCheckCount(ThreeTierResultFail) - failBefore; got < 1 {
		t.Fatalf("fail counter did not advance on misaligned verdict, delta=%v", got)
	}
}

// TestSet_ThreeTierCheckSeesMergedTiers verifies the verifier receives the
// fully merged state at the san_msg_refs/algn serialisation chokepoint: all
// three tiers (raw snapshot, compressed counters + AlignmentMap, sanitize
// refs) must be visible to the validator.
func TestSet_ThreeTierCheckSeesMergedTiers(t *testing.T) {
	var got *SessionState
	SetThreeTierCheck(func(s *SessionState) []string {
		got = s
		return nil
	})
	t.Cleanup(func() { SetThreeTierCheck(nil) })

	cache := NewSessionCache(&recordingBackend{}, nil)
	state := &SessionState{
		RawTokenEstimate:    1000,
		RawMsgCount:         10,
		CompressedTokens:    400,
		CompressedMsgs:      4,
		AlignmentMap:        []AlignmentInfo{{OriginalIndex: 1, CompressedIndex: 0, Hash: "h1"}},
		SanitizeMapRef:      "session:san:ref",
		SanitizeMessageRefs: []SanitizedMessageRef{{RawIndex: 2, SanitizedIndex: 2}},
	}
	passBefore := ThreeTierCheckCount(ThreeTierResultPass)
	if err := cache.Set(context.Background(), "t1", "gw_threetier02", state, []byte(`[...]`)); err != nil {
		t.Fatalf("Set failed: %v", err)
	}
	if got == nil {
		t.Fatal("verifier not invoked on direct Set")
	}
	if got.RawTokenEstimate != 1000 || got.CompressedTokens != 400 || got.SanitizeMapRef == "" ||
		len(got.AlignmentMap) != 1 || len(got.SanitizeMessageRefs) != 1 {
		t.Fatalf("merged state missing tier data: %+v", got)
	}
	if d := ThreeTierCheckCount(ThreeTierResultPass) - passBefore; d != 1 {
		t.Fatalf("pass counter delta = %v, want 1", d)
	}
}

// TestSet_ThreeTierCheckUnregisteredCounted guards the observability story:
// when no verifier is linked (e.g. a binary without threetier), Set must
// still succeed and count "unregistered" so dashboards can tell "check off"
// apart from "all pass".
func TestSet_ThreeTierCheckUnregisteredCounted(t *testing.T) {
	SetThreeTierCheck(nil)
	unregBefore := ThreeTierCheckCount(ThreeTierResultUnregistered)

	cache := NewSessionCache(&recordingBackend{}, nil)
	if err := cache.Set(context.Background(), "t1", "gw_threetier03", &SessionState{}, nil); err != nil {
		t.Fatalf("Set failed without verifier: %v", err)
	}
	if d := ThreeTierCheckCount(ThreeTierResultUnregistered) - unregBefore; d != 1 {
		t.Fatalf("unregistered counter delta = %v, want 1", d)
	}
}

// TestRunThreeTierCheck_NilStateIsNoop pins the guard: nil states (defensive
// callers) neither panic nor emit metrics.
func TestRunThreeTierCheck_NilStateIsNoop(t *testing.T) {
	called := false
	SetThreeTierCheck(func(*SessionState) []string { called = true; return nil })
	t.Cleanup(func() { SetThreeTierCheck(nil) })

	runThreeTierCheck(context.Background(), "t1", "gw_nil", nil)
	if called {
		t.Fatal("verifier must not be called for nil state")
	}
}
