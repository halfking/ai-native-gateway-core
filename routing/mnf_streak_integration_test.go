package routing

import (
	"testing"
	"time"
)

// TestMnfStreak_BreaksStickyAfter3 (Step 6, 2026-06-18) verifies the
// client hot-path breaker: 3 consecutive model_not_found on the same
// (stickyKey, credentialID) deletes the sticky binding so the next
// request re-picks a credential. This complements the original
// recordStickyFailure guard (which deliberately does NOT count
// client-bug kinds like model_not_found) by adding a separate
// client-facing escape hatch.
func TestMnfStreak_BreaksStickyAfter3(t *testing.T) {
	e := &Executor{
		Router:                   &Router{Sticky: NewStickyCache()},
		MnfStreak:                NewMnfStreak(100),
		MnfStickyBreakThreshold:  3,
		MnfStreakEnabled:         true,
	}
	const stickyKey = "tenant:1:1:default:sess-mnf"
	const credID = 42

	e.Router.Sticky.Set(stickyKey, credID, 10*time.Minute)
	params := &ExecParams{StickyKey: stickyKey}

	// 3 consecutive mnf on the same credential → sticky broken.
	for i := 0; i < 3; i++ {
		e.recordMnfStreak(params, credID)
	}
	if _, _, ok := e.Router.Sticky.GetEntry(stickyKey); ok {
		t.Fatal("sticky binding should be deleted after 3 consecutive mnf")
	}
}

// TestMnfStreak_ResetsOnSuccess verifies the success-side counterpart:
// one success on the credential clears its mnf counter so a future
// 1-off mnf does not push it over the threshold.
func TestMnfStreak_ResetsOnSuccess(t *testing.T) {
	e := &Executor{
		Router:                  &Router{Sticky: NewStickyCache()},
		MnfStreak:               NewMnfStreak(100),
		MnfStickyBreakThreshold: 3,
		MnfStreakEnabled:        true,
	}
	const stickyKey = "tenant:1:1:default:sess-reset"
	const credID = 7

	e.Router.Sticky.Set(stickyKey, credID, 10*time.Minute)
	params := &ExecParams{StickyKey: stickyKey}

	e.recordMnfStreak(params, credID) // 1
	e.recordMnfStreak(params, credID) // 2
	e.resetMnfStreak(params, credID)  // success on same credential

	// After reset, two more mnf should NOT break the binding (would
	// need a 3rd consecutive one).
	e.recordMnfStreak(params, credID) // 1 (fresh)
	e.recordMnfStreak(params, credID) // 2 (still below threshold)
	if _, _, ok := e.Router.Sticky.GetEntry(stickyKey); !ok {
		t.Fatal("sticky binding should survive 2 mnf after a success reset")
	}
}

// TestMnfStreak_PerCredentialIsolation verifies that counts for
// different credentials are independent. A failing 2x and B failing
// 1x are independent streaks — neither is at the threshold.
func TestMnfStreak_PerCredentialIsolation(t *testing.T) {
	e := &Executor{
		Router:                  &Router{Sticky: NewStickyCache()},
		MnfStreak:               NewMnfStreak(100),
		MnfStickyBreakThreshold: 3,
		MnfStreakEnabled:        true,
	}
	const stickyKey = "tenant:1:1:default:sess-multi"
	params := &ExecParams{StickyKey: stickyKey}

	// credential A: 2 mnf
	e.recordMnfStreak(params, 100)
	e.recordMnfStreak(params, 100)
	// credential B: 1 mnf
	e.recordMnfStreak(params, 200)

	// Neither should have broken (no single credential at threshold).
	if got := e.MnfStreak.Get(BuildMnfStreakKey(stickyKey, 100)); got != 2 {
		t.Errorf("cred 100: got count %d, want 2", got)
	}
	if got := e.MnfStreak.Get(BuildMnfStreakKey(stickyKey, 200)); got != 1 {
		t.Errorf("cred 200: got count %d, want 1", got)
	}
}

// TestMnfStreak_DisabledFlagNoOp verifies the env-gated feature flag
// fully disables the breaker (the background probe consensus in
// bg/model_probe.go remains the only path that affects credential
// health when this is off).
func TestMnfStreak_DisabledFlagNoOp(t *testing.T) {
	e := &Executor{
		Router:                  &Router{Sticky: NewStickyCache()},
		MnfStreak:               NewMnfStreak(100),
		MnfStickyBreakThreshold: 3,
		MnfStreakEnabled:        false, // <-- feature flag off
	}
	const stickyKey = "tenant:1:1:default:sess-disabled"
	const credID = 1
	e.Router.Sticky.Set(stickyKey, credID, 10*time.Minute)
	params := &ExecParams{StickyKey: stickyKey}

	for i := 0; i < 10; i++ {
		e.recordMnfStreak(params, credID)
	}
	if _, _, ok := e.Router.Sticky.GetEntry(stickyKey); !ok {
		t.Fatal("sticky binding must survive when MnfStreakEnabled=false")
	}
}

// TestMnfStreak_NilStickyKeyNoOp verifies that stateless requests
// (no X-Gw-Session-Id, no sticky binding) are unaffected by the
// streak counter.
func TestMnfStreak_NilStickyKeyNoOp(t *testing.T) {
	e := &Executor{
		Router:                  &Router{Sticky: NewStickyCache()},
		MnfStreak:               NewMnfStreak(100),
		MnfStickyBreakThreshold: 3,
		MnfStreakEnabled:        true,
	}
	// empty StickyKey → recordMnfStreak is a no-op
	for i := 0; i < 10; i++ {
		e.recordMnfStreak(&ExecParams{StickyKey: ""}, 1)
	}
	if e.MnfStreak.Len() != 0 {
		t.Fatalf("empty StickyKey should not populate map, got len=%d", e.MnfStreak.Len())
	}
}

// TestMnfStreak_DefaultThreshold verifies that threshold <= 0 falls
// back to 3 (the documented default).
func TestMnfStreak_DefaultThreshold(t *testing.T) {
	e := &Executor{
		Router:                  &Router{Sticky: NewStickyCache()},
		MnfStreak:               NewMnfStreak(100),
		MnfStickyBreakThreshold: 0, // <-- explicit default
		MnfStreakEnabled:        true,
	}
	const stickyKey = "tenant:1:1:default:sess-default-thresh"
	const credID = 5
	e.Router.Sticky.Set(stickyKey, credID, 10*time.Minute)
	params := &ExecParams{StickyKey: stickyKey}

	for i := 0; i < 3; i++ {
		e.recordMnfStreak(params, credID)
	}
	if _, _, ok := e.Router.Sticky.GetEntry(stickyKey); ok {
		t.Fatal("default threshold (3) should still break the binding")
	}
}
