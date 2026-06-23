package credentialfpslot

// Tests for the 2026-06-24 active-gate preemption logic. See
// executor.go and the BuildClientStickyKey refactor rationale —
// one client = one fingerprint, the active gate (5 min default)
// is the only thing that says "an idle slot is fair game for
// the next arrival".
//
// All tests run in the in-memory mode (client == nil) so they
// don't depend on Redis being available in the test environment.
// The Redis Lua path is exercised by the same in-memory logic
// (we test the manager's state machine; the Lua is a thin
// atomicity wrapper around it).

import (
	"context"
	"testing"
	"time"
)

// TestActiveGate_HolderRenewsOwnSlot is the happy path: as long
// as the holder keeps acquiring, their slot TTL is refreshed and
// no preemption happens. This is what makes "same client = same
// slot" work in steady state.
func TestActiveGate_HolderRenewsOwnSlot(t *testing.T) {
	m := New(Config{
		DefaultLimit:      1, // tight pool
		Enabled:           true,
		ActiveGateSeconds: 5,
	}, nil)
	ctx := context.Background()

	first, ok := m.Acquire(ctx, 1, nil, "alice", "default")
	if !ok {
		t.Fatal("first acquire should succeed")
	}
	originalSlot := first.SlotIndex

	// 10 successive re-acquires. Each refresh extends the slot's
	// exp by slotTTLSeconds (30 min) from "now", so the entry is
	// always fresh and the active gate never fires.
	for i := 0; i < 10; i++ {
		m.Release(ctx, first)
		next, ok := m.Acquire(ctx, 1, nil, "alice", "default")
		if !ok {
			t.Fatalf("iteration %d: alice must keep her slot", i)
		}
		if next.SlotIndex != originalSlot {
			t.Fatalf("iteration %d: slot drifted from %d to %d",
				i, originalSlot, next.SlotIndex)
		}
		first = next
	}
	m.Release(ctx, first)
}

// TestActiveGate_DoesNotPreemptActiveHolder is the core safety
// rule: an ACTIVE holder (idle < gate) must NOT be preempted by
// a new arrival. We simulate "active" by inserting a fresh entry
// directly (TTL = 30 min from now), then trying to acquire as a
// different holder.
func TestActiveGate_DoesNotPreemptActiveHolder(t *testing.T) {
	m := New(Config{
		DefaultLimit:      1,
		Enabled:           true,
		ActiveGateSeconds: 5,
	}, nil)
	ctx := context.Background()

	// alice takes the only slot.
	first, ok := m.Acquire(ctx, 1, nil, "alice", "default")
	if !ok {
		t.Fatal("alice should take the slot")
	}
	// alice releases, but long-term-occupancy keeps the entry
	// (just refreshes TTL — see releaseMemory).
	m.Release(ctx, first)

	// bob tries to take it while alice is still inside the
	// active-gate window (TTL = 30 min, well above 5 min gate).
	_, ok = m.Acquire(ctx, 1, nil, "bob", "default")
	if ok {
		t.Fatal("bob must NOT preempt an active alice — active gate is " +
			"the whole point of the '5 min 内不允许抢的' rule")
	}

	// alice can still get her slot back.
	aliceAgain, ok := m.Acquire(ctx, 1, nil, "alice", "default")
	if !ok || aliceAgain.SlotIndex != first.SlotIndex {
		t.Fatalf("alice must reacquire her own slot %d, got %+v",
			first.SlotIndex, aliceAgain)
	}
	m.Release(ctx, aliceAgain)
}

// TestActiveGate_PreemptsIdleHolderAfterGateWindow covers the
// "后来者抢空闲 slot" path. We make the holder's slot artificially
// idle by injecting an old `exp`, then a new arrival should be
// able to take it.
//
// We can't easily wait 5 min in a unit test, so we directly poke
// memSlots to backdate the entry's exp.
func TestActiveGate_PreemptsIdleHolderAfterGateWindow(t *testing.T) {
	m := New(Config{
		DefaultLimit:      1,
		Enabled:           true,
		ActiveGateSeconds: 5,
	}, nil)
	ctx := context.Background()

	first, ok := m.Acquire(ctx, 1, nil, "alice", "default")
	if !ok {
		t.Fatal("alice should take the slot")
	}
	m.Release(ctx, first)

	// Backdate alice's slot entry so it looks like it has been
	// idle for 6 minutes (TTL remaining ≈ 24 min out of 30 min).
	// memSlots is package-internal, so we use the same key
	// constructor the manager uses.
	m.mu.Lock()
	oldExp := time.Now().Add(24 * time.Minute) // remaining = 24 min, idle ≈ 6 min
	m.memSlots[slotKey{credentialID: 1, slotIndex: first.SlotIndex}] = memEntry{
		holder: "alice",
		exp:    oldExp,
	}
	m.mu.Unlock()

	// bob should now be able to acquire it: the active gate
	// fires when (slotTTL - remaining) > gate, i.e. 30 - 24 = 6
	// min idle > 5 min gate.
	bob, ok := m.Acquire(ctx, 1, nil, "bob", "default")
	if !ok {
		t.Fatal("bob must preempt alice's idle slot (active gate fired)")
	}
	if bob.SlotIndex != first.SlotIndex {
		t.Fatalf("bob should reuse the same slot %d, got %d",
			first.SlotIndex, bob.SlotIndex)
	}
	if bob.Holder != "bob" {
		t.Fatalf("bob must own the slot now, got holder %q", bob.Holder)
	}
	m.Release(ctx, bob)
}

// TestActiveGate_PreemptResetsPinToNewHolder verifies the side
// effect of preemption: the old holder's pin must NOT survive —
// otherwise the old holder would still think they own the slot.
func TestActiveGate_PreemptResetsPinToNewHolder(t *testing.T) {
	m := New(Config{
		DefaultLimit:      1,
		Enabled:           true,
		ActiveGateSeconds: 5,
	}, nil)
	ctx := context.Background()

	first, _ := m.Acquire(ctx, 1, nil, "alice", "default")
	m.Release(ctx, first)

	// Idle the slot.
	m.mu.Lock()
	m.memSlots[slotKey{credentialID: 1, slotIndex: first.SlotIndex}] = memEntry{
		holder: "alice",
		exp:    time.Now().Add(20 * time.Minute), // idle ≈ 10 min > gate
	}
	m.mu.Unlock()

	// bob preempts.
	bob, _ := m.Acquire(ctx, 1, nil, "bob", "default")
	if bob == nil {
		t.Fatal("bob must preempt")
	}
	m.Release(ctx, bob)

	// alice's pin must be gone (so she doesn't try to refresh
	// the slot she no longer owns).
	if m.hasPin(ctx, "alice", 1) {
		t.Fatal("alice's pin should be cleared after bob preempts her slot")
	}
	// bob's pin must be present and point at the slot he just took.
	if !m.hasPin(ctx, "bob", 1) {
		t.Fatal("bob's pin should be set after preemption")
	}
	pinned, _ := m.memPins[pinRedisKey("bob", 1)]
	if pinned.slot != first.SlotIndex {
		t.Fatalf("bob's pin should point at slot %d, got %d",
			first.SlotIndex, pinned.slot)
	}
}

// TestActiveGate_DifferentPoolSize_LRURoundRobin covers the
// "no-pin holder + 2 slots + idle" case: with limit=2 and
// no incoming pin, an idle slot should be preempted in scan
// order (slot 0 before slot 1). This is the closest in-memory
// proxy for the Redis Lua LRU path.
func TestActiveGate_DifferentPoolSize_LRURoundRobin(t *testing.T) {
	m := New(Config{
		DefaultLimit:      2,
		Enabled:           true,
		ActiveGateSeconds: 5,
	}, nil)
	ctx := context.Background()

	// alice and bob take both slots.
	alice, _ := m.Acquire(ctx, 1, nil, "alice", "default")
	bob, _ := m.Acquire(ctx, 1, nil, "bob", "default")
	if alice == nil || bob == nil || alice.SlotIndex == bob.SlotIndex {
		t.Fatalf("alice and bob must take different slots: alice=%+v bob=%+v", alice, bob)
	}
	// Release → long-term-occupancy refresh.
	m.Release(ctx, alice)
	m.Release(ctx, bob)

	// Idle both slots.
	idleExp := time.Now().Add(20 * time.Minute)
	m.mu.Lock()
	m.memSlots[slotKey{credentialID: 1, slotIndex: alice.SlotIndex}] = memEntry{
		holder: "alice", exp: idleExp,
	}
	m.memSlots[slotKey{credentialID: 1, slotIndex: bob.SlotIndex}] = memEntry{
		holder: "bob", exp: idleExp,
	}
	m.mu.Unlock()

	// charlie arrives with no pin. The scan should find the
	// first idle slot (slot 0 if alice is there) and preempt.
	charlie, ok := m.Acquire(ctx, 1, nil, "charlie", "default")
	if !ok {
		t.Fatal("charlie must preempt an idle slot")
	}
	if charlie.SlotIndex != alice.SlotIndex {
		t.Fatalf("scan-order preemption: expected slot %d (alice's), got %d",
			alice.SlotIndex, charlie.SlotIndex)
	}
	m.Release(ctx, charlie)
}

// TestActiveGate_AllActive_NoPreempt is the last-resort safety
// net: every slot has a fresh, active holder. A new arrival
// must NOT acquire any of them.
func TestActiveGate_AllActive_NoPreempt(t *testing.T) {
	m := New(Config{
		DefaultLimit:      2,
		Enabled:           true,
		ActiveGateSeconds: 5,
	}, nil)
	ctx := context.Background()

	a, _ := m.Acquire(ctx, 1, nil, "alice", "default")
	b, _ := m.Acquire(ctx, 1, nil, "bob", "default")
	m.Release(ctx, a)
	m.Release(ctx, b)

	// Both entries are still active (just-released, full 30 min TTL).
	charlie, ok := m.Acquire(ctx, 1, nil, "charlie", "default")
	if ok {
		t.Fatal("charlie must fail when all slots are held by active holders")
	}
	if charlie != nil {
		t.Logf("got charlie=%+v (should be nil)", charlie)
	}
}

// TestActiveGate_DefaultConfig_5Min verifies the default fallback
// when Config.ActiveGateSeconds is left at 0 — the manager must
// pick up DefaultActiveGateSeconds (5 min) and apply it.
func TestActiveGate_DefaultConfig_5Min(t *testing.T) {
	m := New(Config{
		DefaultLimit: 1,
		Enabled:      true,
		// ActiveGateSeconds intentionally 0 → falls back to default.
	}, nil)
	if got := m.cfg.resolveActiveGateSeconds(); got != DefaultActiveGateSeconds {
		t.Fatalf("resolveActiveGateSeconds = %d, want %d",
			got, DefaultActiveGateSeconds)
	}
	if DefaultActiveGateSeconds != 300 {
		t.Fatalf("DefaultActiveGateSeconds = %d, want 300 (5 min)", DefaultActiveGateSeconds)
	}
}

// TestReclaimConfigFromManager_PropagatesActiveGate ensures the
// reclaim goroutine reads the same active gate as the Acquire
// path, so the two cannot drift apart via a configuration mistake.
func TestReclaimConfigFromManager_PropagatesActiveGate(t *testing.T) {
	m := New(Config{
		DefaultLimit:      5,
		Enabled:           true,
		ActiveGateSeconds: 180, // 3 min, custom
	}, nil)
	cfg := m.reclaimConfigFromManager()
	if cfg.idleAfter != 3*time.Minute {
		t.Fatalf("reclaim idleAfter = %v, want 3m", cfg.idleAfter)
	}
}

// TestNewManager_AppliesActiveGateDefault verifies that New()
// fills in DefaultActiveGateSeconds when the operator leaves
// the field at 0.
func TestNewManager_AppliesActiveGateDefault(t *testing.T) {
	m := New(Config{DefaultLimit: 1, Enabled: true}, nil)
	if m.cfg.ActiveGateSeconds != DefaultActiveGateSeconds {
		t.Fatalf("ActiveGateSeconds = %d, want default %d",
			m.cfg.ActiveGateSeconds, DefaultActiveGateSeconds)
	}
}
