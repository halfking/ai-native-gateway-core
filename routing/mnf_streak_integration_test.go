package routing

import (
	"testing"
	"time"
)

// TestMnfStreak_KeepsStickyAtThreshold (2026-06-24) verifies that
// hitting the MnfStreak threshold does NOT break the sticky binding.
//
// Why: model_not_found is a client-bug kind (errorsx.IsClientBug=true).
// The sticky-session rule is "same client + same model = same credential",
// and that rule is the operator-stated invariant for single-terminal
// sessions — switching credentials on transient client-bug kinds breaks
// the per-session identity (UA, TLS fingerprint slot, KV cache prefix).
//
// recordStickyFailure already exempts client-bug kinds from unpinning;
// recordMnfStreak must agree, otherwise the two paths disagree and a
// 3-streak silently re-picks a credential on the very next request —
// producing the "one normal / one failure" alternating pattern observed
// in production (2026-06-23 minimax-m3 incident, request
// 2cba16052f8b5d68175482e9383a0f0d).
//
// The counter is still incremented so observability (counters,
// /api/candidate-failures/stats, alert ring) can surface "this
// credential is returning model_not_found frequently" — that signal
// is now informational only and does NOT mutate routing state.
func TestMnfStreak_KeepsStickyAtThreshold(t *testing.T) {
	e := &Executor{
		Router:                  &Router{Sticky: NewStickyCache()},
		MnfStreak:               NewMnfStreak(100),
		MnfStickyBreakThreshold: 3,
		MnfStreakEnabled:        true,
	}
	const stickyKey = "tenant:1:1:default:sess-mnf"
	const credID = 42

	e.Router.Sticky.Set(stickyKey, credID, 10*time.Minute)
	params := &ExecParams{StickyKey: stickyKey}

	// 3 consecutive mnf on the same credential → sticky MUST survive.
	for i := 0; i < 3; i++ {
		e.recordMnfStreak(params, credID)
	}
	if _, _, ok := e.Router.Sticky.GetEntry(stickyKey); !ok {
		t.Fatal("sticky binding must survive 3 consecutive mnf (2026-06-24: do NOT break)")
	}
}

// TestMnfStreak_HighStreakStillKeepsSticky (2026-06-24) verifies the
// keep-sticky invariant holds even at very high streaks — there is no
// upper bound that silently re-picks. The counter keeps climbing so
// observability remains accurate.
func TestMnfStreak_HighStreakStillKeepsSticky(t *testing.T) {
	e := &Executor{
		Router:                  &Router{Sticky: NewStickyCache()},
		MnfStreak:               NewMnfStreak(100),
		MnfStickyBreakThreshold: 3,
		MnfStreakEnabled:        true,
	}
	const stickyKey = "tenant:1:1:default:sess-high"
	const credID = 42

	e.Router.Sticky.Set(stickyKey, credID, 10*time.Minute)
	params := &ExecParams{StickyKey: stickyKey}

	for i := 0; i < 50; i++ {
		e.recordMnfStreak(params, credID)
	}
	if _, _, ok := e.Router.Sticky.GetEntry(stickyKey); !ok {
		t.Fatal("sticky binding must survive any number of consecutive mnf")
	}
	want := 50
	if got := e.MnfStreak.Get(BuildMnfStreakKey(stickyKey, credID)); got != want {
		t.Errorf("counter should keep climbing for observability: got %d, want %d", got, want)
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
// back to 3 (the documented default). At default threshold, sticky
// must STILL survive (counter is informational only as of 2026-06-24).
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
	if _, _, ok := e.Router.Sticky.GetEntry(stickyKey); !ok {
		t.Fatal("default threshold (3) must keep sticky intact (2026-06-24)")
	}
	if got := e.MnfStreak.Get(BuildMnfStreakKey(stickyKey, credID)); got != 3 {
		t.Errorf("counter should still hit 3 at default threshold: got %d", got)
	}
}
