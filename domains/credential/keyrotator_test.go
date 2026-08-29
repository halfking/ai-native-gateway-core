package credential

import (
	"context"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

func TestKeyRotator_SingleKeyUnregistered(t *testing.T) {
	kr := NewKeyRotator()
	// credential not registered → always primary (0)
	if idx := kr.ResolveKey(1, -1); idx != 0 {
		t.Fatalf("unregistered credential: ResolveKey = %d, want 0", idx)
	}
	if kr.AllKeysInvalid(1) {
		t.Fatal("unregistered credential should not be AllKeysInvalid")
	}
}

func TestKeyRotator_RoundRobin(t *testing.T) {
	kr := NewKeyRotator()
	kr.EnsureCred(10, 3)
	// first three resolves should cycle 0,1,2 (or a rotation thereof)
	seen := map[int]int{}
	for i := 0; i < 6; i++ {
		idx := kr.ResolveKey(10, -1)
		seen[idx]++
	}
	if len(seen) != 3 {
		t.Fatalf("round-robin over 3 keys hit only %d distinct keys: %v", len(seen), seen)
	}
	for idx, c := range seen {
		if c != 2 {
			t.Errorf("key %d resolved %d times, want 2 (even distribution)", idx, c)
		}
	}
}

func TestKeyRotator_SkipInvalid(t *testing.T) {
	kr := NewKeyRotator()
	kr.EnsureCred(20, 3)
	// mark key 0 invalid via 2 consecutive failures
	kr.RecordKeyFailure(20, 0, errorsx.KindTransient)
	kr.RecordKeyFailure(20, 0, errorsx.KindTransient)
	// key 0 should never be returned now
	for i := 0; i < 10; i++ {
		if idx := kr.ResolveKey(20, -1); idx == 0 {
			t.Fatal("invalid key 0 was returned by ResolveKey")
		}
	}
	if kr.AllKeysInvalid(20) {
		t.Fatal("not all keys invalid yet (1,2 still active)")
	}
}

func TestKeyRotator_TerminalImmediate(t *testing.T) {
	kr := NewKeyRotator()
	kr.EnsureCred(30, 2)
	// 402 balance → terminal immediately, no threshold
	changed := kr.RecordKeyFailure(30, 0, errorsx.KindQuotaPermanent)
	if !changed {
		t.Fatal("terminal failure should report status change")
	}
	// key 0 skipped
	for i := 0; i < 5; i++ {
		if idx := kr.ResolveKey(30, -1); idx == 0 {
			t.Fatal("terminal key 0 was returned")
		}
	}
}

func TestKeyRotator_AllInvalid(t *testing.T) {
	kr := NewKeyRotator()
	kr.EnsureCred(40, 3)
	for i := 0; i < 3; i++ {
		kr.RecordKeyFailure(40, i, errorsx.KindQuotaPermanent) // terminal
	}
	if !kr.AllKeysInvalid(40) {
		t.Fatal("expected AllKeysInvalid after all 3 keys terminal")
	}
	// ResolveKey returns -1
	if idx := kr.ResolveKey(40, -1); idx != -1 {
		t.Fatalf("ResolveKey when all invalid = %d, want -1", idx)
	}
}

func TestKeyRotator_StickyReused(t *testing.T) {
	kr := NewKeyRotator()
	kr.EnsureCred(50, 3)
	// sticky key 1 should be reused
	if idx := kr.ResolveKey(50, 1); idx != 1 {
		t.Fatalf("sticky key 1: ResolveKey = %d, want 1", idx)
	}
	if idx := kr.ResolveKey(50, 1); idx != 1 {
		t.Fatalf("sticky key 1 second call: ResolveKey = %d, want 1", idx)
	}
	// sticky key that became invalid → fallback to round-robin
	kr.RecordKeyFailure(50, 1, errorsx.KindQuotaPermanent)
	idx := kr.ResolveKey(50, 1)
	if idx == 1 {
		t.Fatal("sticky key 1 is terminal but was still returned")
	}
}

func TestKeyRotator_SuccessClears(t *testing.T) {
	kr := NewKeyRotator()
	kr.EnsureCred(60, 2)
	kr.RecordKeyFailure(60, 0, errorsx.KindTransient) // 1 failure → warning
	kr.RecordKeySuccess(60, 0)
	// key 0 should be active again and eligible
	found := false
	for i := 0; i < 10; i++ {
		if kr.ResolveKey(60, -1) == 0 {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("key 0 not eligible after RecordKeySuccess")
	}
}

func TestKeyRotator_ResetKey(t *testing.T) {
	kr := NewKeyRotator()
	kr.EnsureCred(70, 2)
	kr.RecordKeyFailure(70, 0, errorsx.KindQuotaPermanent) // terminal
	kr.ResetKey(70, 0)
	found := false
	for i := 0; i < 10; i++ {
		if kr.ResolveKey(70, -1) == 0 {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("key 0 not eligible after ResetKey")
	}
}

// TestKeyRotator_ResetKey_SurgicalSemantics locks the surgical-reset contract
// for admin PATCH /keys/{kid} (2026-08-15 audit round-2 fix): ResetKey must
// flip ONLY the targeted key from terminal/invalid → Active, and must leave
// sibling keys' status and the round-robin cursor untouched. Without this,
// admin key activation would wipe out the rotator state for healthy sibling
// keys, dropping the credential back into single-key rotation until the next
// EnsureCred rebuild — observable as a request-rate cliff on sibling keys.
//
// Setup:
//   - 3 keys registered for credential 71
//   - keys 0 and 2 marked terminal (terminal kind: immediate, no threshold)
//   - advance the cursor past key 0 so it sits at index 1
// Then:
//   - ResetKey(71, 1) — note key 1 was never marked, it's already Active
//     (this also exercises the "no-op when already Active" path; the more
//     important property is that keys 0 and 2 stay terminal afterwards)
// Verify:
//   - keys 0 and 2 are STILL terminal (sibling isolation)
//   - ResolveKey(71, -1) returns 1 (the only eligible key, cursor advanced
//     from 1 → 2 means a subsequent call would land on 2 — still terminal
//     so it would scan to 1 again)
func TestKeyRotator_ResetKey_SurgicalSemantics(t *testing.T) {
	kr := NewKeyRotator()
	const credID = 71
	kr.EnsureCred(credID, 3)

	// Mark keys 0 and 2 terminal (sibling isolation target).
	kr.RecordKeyFailure(credID, 0, errorsx.KindQuotaPermanent)
	kr.RecordKeyFailure(credID, 2, errorsx.KindQuotaPermanent)

	// Drive the round-robin cursor so it sits somewhere stable. ResolveKey
	// with stickyIdx=-1 advances the cursor; after one call starting from
	// cursor=0 it picks the first eligible (key 1) and advances to cursor=2.
	if idx := kr.ResolveKey(credID, -1); idx != 1 {
		t.Fatalf("precondition: expected first eligible to be key 1, got %d", idx)
	}

	// Sanity: at this point AllKeysInvalid must be false (key 1 still active).
	if kr.AllKeysInvalid(credID) {
		t.Fatal("precondition: AllKeysInvalid should be false (key 1 active)")
	}

	// Surgical reset on key 1 (already active — this exercises the no-op
	// path while still proving the cursor and sibling state survive).
	kr.ResetKey(credID, 1)

	// (a) Targeted key 1 remains eligible.
	seen := map[int]bool{}
	for i := 0; i < 10; i++ {
		idx := kr.ResolveKey(credID, -1)
		seen[idx] = true
		if idx < 0 {
			t.Fatalf("expected a healthy key to remain eligible, got %d", idx)
		}
		if idx != 1 {
			t.Fatalf("key %d became eligible after surgical reset of key 1", idx)
		}
	}
	if !seen[1] {
		t.Fatal("key 1 was not returned by ResolveKey after ResetKey")
	}

	// (b) Sibling keys 0 and 2 stay terminal (sibling isolation).
	// Probe by exhausting ResolveKey from cursor=0 — the cursor wraps to 0
	// after one full cycle. If keys 0/2 became eligible they'd appear here.
	// We can't directly inspect state, so we use the AllKeysInvalid test:
	// after making key 1 invalid too, AllKeysInvalid must be true. But key
	// 1 is supposed to be active, so we mark it terminal and check.
	kr.RecordKeyFailure(credID, 1, errorsx.KindQuotaPermanent)
	if !kr.AllKeysInvalid(credID) {
		t.Fatal("sibling isolation broken: after also marking key 1 terminal, " +
			"AllKeysInvalid should be true")
	}
	// Reverse: clear key 1 again, verify only it is eligible.
	kr.ResetKey(credID, 1)
	if kr.AllKeysInvalid(credID) {
		t.Fatal("after ResetKey(71,1), AllKeysInvalid should be false (key 1 active)")
	}
	// Confirm only key 1 is returned across many rotations.
	for i := 0; i < 20; i++ {
		if idx := kr.ResolveKey(credID, -1); idx != 1 {
			t.Fatalf("only key 1 should be eligible, got %d on iter %d", idx, i)
		}
	}
}

func TestKeyRotator_ResetCredential(t *testing.T) {
	kr := NewKeyRotator()
	kr.EnsureCred(80, 3)
	for i := 0; i < 3; i++ {
		kr.RecordKeyFailure(80, i, errorsx.KindQuotaPermanent)
	}
	if !kr.AllKeysInvalid(80) {
		t.Fatal("expected all keys invalid before reset")
	}

	kr.ResetCredential(80)
	if got := kr.KeyCount(80); got != 0 {
		t.Fatalf("KeyCount after ResetCredential = %d, want 0", got)
	}
	if kr.AllKeysInvalid(80) {
		t.Fatal("ResetCredential should clear all-invalid state")
	}
	if idx := kr.ResolveKey(80, -1); idx != 0 {
		t.Fatalf("unregistered after ResetCredential should resolve primary 0, got %d", idx)
	}
}

// TestKeyRotator_SweepInvalid_BeforeCooldown asserts that an invalid key whose
// invalidSince is still within the cooldown window is left alone by the
// sweeper. This protects against sweeping too aggressively and re-burning the
// key inside its cooldown window.
func TestKeyRotator_SweepInvalid_BeforeCooldown(t *testing.T) {
	kr := NewKeyRotator()
	const credID = 90
	kr.EnsureCred(credID, 2)
	kr.RecordKeyFailure(credID, 0, errorsx.KindTransient)
	kr.RecordKeyFailure(credID, 0, errorsx.KindTransient) // → invalid

	now := time.Now()
	// 1 second after invalidSince — well inside a 15-minute cooldown.
	if n := kr.SweepInvalid(now.Add(1*time.Second), 15*time.Minute); n != 0 {
		t.Fatalf("SweepInvalid before cooldown recovered %d keys, want 0", n)
	}
	if idx := kr.ResolveKey(credID, -1); idx == 0 {
		t.Fatal("key 0 became eligible before its cooldown elapsed")
	}
	// Sibling key 1 was never touched — it must still be active.
	if kr.AllKeysInvalid(credID) {
		t.Fatal("SweepInvalid must not affect non-KeyStatusInvalid entries")
	}
}

// TestKeyRotator_SweepInvalid_AfterCooldown locks the self-heal contract for
// the handoff's MEDIUM-severity finding: a stale invalid key auto-recovers to
// active after DefaultInvalidCooldown elapses, so a transient 401 during a
// key-rotation window doesn't permanently eject the key from rotation.
func TestKeyRotator_SweepInvalid_AfterCooldown(t *testing.T) {
	kr := NewKeyRotator()
	const credID = 91
	kr.EnsureCred(credID, 2)
	kr.RecordKeyFailure(credID, 0, errorsx.KindTransient)
	kr.RecordKeyFailure(credID, 0, errorsx.KindTransient) // → invalid

	// Drift the synthetic clock past DefaultInvalidCooldown.
	now := time.Now().Add(DefaultInvalidCooldown + time.Second)
	if n := kr.SweepInvalid(now, DefaultInvalidCooldown); n != 1 {
		t.Fatalf("SweepInvalid after cooldown recovered %d keys, want 1", n)
	}
	// Key 0 must now be eligible again.
	found := false
	for i := 0; i < 10; i++ {
		if kr.ResolveKey(credID, -1) == 0 {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("key 0 not eligible after sweeper recovery")
	}
	// Sibling key 1 was never touched.
	if kr.AllKeysInvalid(credID) {
		t.Fatal("key 1 was erroneously affected by the sweeper")
	}
}

// TestKeyRotator_SweepInvalid_SkipsTerminal asserts that terminal keys (402
// balance, revoked) are NEVER recovered by the sweeper — they're non-
// recoverable without operator action by design.
func TestKeyRotator_SweepInvalid_SkipsTerminal(t *testing.T) {
	kr := NewKeyRotator()
	const credID = 92
	kr.EnsureCred(credID, 2)
	kr.RecordKeyFailure(credID, 0, errorsx.KindQuotaPermanent) // → terminal

	now := time.Now().Add(24 * time.Hour)
	if n := kr.SweepInvalid(now, DefaultInvalidCooldown); n != 0 {
		t.Fatalf("SweepInvalid recovered %d terminal keys, want 0", n)
	}
	if idx := kr.ResolveKey(credID, -1); idx == 0 {
		t.Fatal("terminal key 0 became eligible after sweep — must never recover")
	}
}

// TestKeyRotator_ResetKey_ClearsInvalidSince asserts that an admin ResetKey
// clears the invalidSince stamp so a subsequent sweep won't treat the
// operator-reset key as stale.
func TestKeyRotator_ResetKey_ClearsInvalidSince(t *testing.T) {
	kr := NewKeyRotator()
	const credID = 93
	kr.EnsureCred(credID, 1)
	kr.RecordKeyFailure(credID, 0, errorsx.KindTransient)
	kr.RecordKeyFailure(credID, 0, errorsx.KindTransient) // → invalid

	// Admin resets while still inside cooldown.
	kr.ResetKey(credID, 0)

	// Sweep well past the original invalidSince — must NOT touch anything
	// because the operator already cleared the state.
	now := time.Now().Add(DefaultInvalidCooldown + time.Hour)
	if n := kr.SweepInvalid(now, DefaultInvalidCooldown); n != 0 {
		t.Fatalf("SweepInvalid recovered %d keys after admin ResetKey, want 0", n)
	}
	if idx := kr.ResolveKey(credID, -1); idx != 0 {
		t.Fatalf("ResolveKey after ResetKey = %d, want 0", idx)
	}
}

// TestKeyRotator_StartSweeper_StopIdempotent asserts that the background
// sweeper lifecycle is safe to call from multiple goroutines and that
// repeated stop calls don't panic. We cancel the parent context before any
// tick fires (DefaultSweepInterval = 1 minute), then exercise a sequence of
// start/stop calls to verify idempotency under -race.
func TestKeyRotator_StartSweeper_StopIdempotent(t *testing.T) {
	kr := NewKeyRotator()
	kr.EnsureCred(100, 2)
	kr.RecordKeyFailure(100, 0, errorsx.KindTransient)
	kr.RecordKeyFailure(100, 0, errorsx.KindTransient) // → invalid

	ctx, cancel := context.WithCancel(context.Background())
	kr.StartSweeper(ctx)
	kr.StartSweeper(ctx) // second call must be idempotent — no extra goroutine
	kr.StartSweeper(ctx)

	cancel()
	kr.StopSweeper()
	kr.StopSweeper() // must not panic
	kr.StopSweeper()
}

