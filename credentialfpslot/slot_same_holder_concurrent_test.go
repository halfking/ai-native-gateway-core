package credentialfpslot

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

// TestSameHolderConcurrentSharesOneSlot (2026-06-29) is the regression test
// for the "minimax-prod-1 52% failure rate" incident. The scenario:
//
//   - Pool size = 5 slots (the production default, see DefaultDefaultLimit).
//   - One user (holder A) issues 100 concurrent requests (e.g. an agent loop
//     firing many parallel tool-call continuations).
//   - Without the fix: each concurrent request from holder A takes a DIFFERENT
//     slot, because acquireLRUScript's per-slot scan had no `current == holder`
//     branch. After 5 of holder A's requests, the pool is fully consumed by
//     holder A alone. The next 95 requests see no free slot and no idle slot,
//     so acquireRedis returns false → executor rejects the request →
//     "cred_fp_slot saturated for credential N".
//   - With the fix: holder A's first request takes slot 0 (via Phase 2 LRU
//     scan). All subsequent holder A requests, whether they hit Phase 1
//     (pin-reuse) or Phase 2 (LRU scan), refresh slot 0's TTL and return it.
//     The pool still has 4 free slots for other users (holder B, holder C…).
//
// Expected outcome with the fix: 100/100 of holder A's concurrent requests
// succeed, AND they all observe the SAME SlotIndex (slot 0). All 5 slots are
// NOT consumed by holder A.
func TestSameHolderConcurrentSharesOneSlot(t *testing.T) {
	m, _ := newTestManager(t, Config{DefaultLimit: 5, Enabled: true})
	ctx := context.Background()

	const goroutines = 100
	const credentialID = 6 // minimax-prod-1 in production

	var wg sync.WaitGroup
	var acquired atomic.Int32
	var saturated atomic.Int32

	// Track distinct slot indices observed by holder A.
	var slotMu sync.Mutex
	seenSlots := make(map[int]int) // slotIndex -> count

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lease, ok := m.Acquire(ctx, credentialID, nil, "holder-A", "default")
			if !ok {
				saturated.Add(1)
				return
			}
			acquired.Add(1)
			slotMu.Lock()
			seenSlots[lease.SlotIndex]++
			slotMu.Unlock()
		}()
	}
	wg.Wait()

	if acquired.Load() != goroutines {
		t.Fatalf("expected all %d concurrent acquires to succeed, got %d acquired / %d saturated",
			goroutines, acquired.Load(), saturated.Load())
	}
	if saturated.Load() != 0 {
		t.Fatalf("expected zero saturation for same-holder concurrent, got %d", saturated.Load())
	}

	// All goroutines must observe the SAME slot (design intent: one slot
	// per holder, regardless of concurrency).
	if len(seenSlots) != 1 {
		slotList := make([]string, 0, len(seenSlots))
		for s := range seenSlots {
			slotList = append(slotList, fmt.Sprintf("slot%d×%d", s, seenSlots[s]))
		}
		t.Fatalf("expected holder A to occupy exactly 1 slot, but observed %d distinct slots: %v",
			len(seenSlots), slotList)
	}
}

// TestSameHolderConcurrentAlongsideOtherHolders (2026-06-29) verifies that
// after holder A's 100 concurrent requests share one slot, there are still
// slots available for OTHER holders. This is the "other users can still use
// the credential" half of the regression — the production incident's second
// symptom was that OTHER users (2-5 concurrent) saw "no candidates" while
// the heavy user was active.
func TestSameHolderConcurrentAlongsideOtherHolders(t *testing.T) {
	m, _ := newTestManager(t, Config{DefaultLimit: 5, Enabled: true})
	ctx := context.Background()
	const credentialID = 6

	// Holder A fires 100 concurrent requests; all should succeed sharing 1 slot.
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lease, ok := m.Acquire(ctx, credentialID, nil, "holder-A", "default")
			if !ok {
				t.Errorf("holder A should not be saturated")
				return
			}
			if lease.SlotIndex != 0 {
				t.Errorf("holder A should land on slot 0 (first free), got %d", lease.SlotIndex)
			}
		}()
	}
	wg.Wait()

	// After holder A's storm, slots 1..4 must still be free for other holders.
	// 4 OTHER holders should each get a distinct slot.
	for i := 1; i <= 4; i++ {
		holder := fmt.Sprintf("holder-B%d", i)
		lease := acquireSuccess(t, m, ctx, credentialID, nil, holder, "default")
		if lease.SlotIndex < 1 || lease.SlotIndex > 4 {
			t.Errorf("%s expected slot 1..4, got %d", holder, lease.SlotIndex)
		}
	}

	// 5th other holder should be saturated (pool full: slot0=A, slot1..4=B1..B4).
	acquireExpectSaturated(t, m, ctx, credentialID, nil, "holder-C", "default")
}