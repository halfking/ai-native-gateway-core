package dispatch

// capacity_aware_sort.go — Stage D soft-rank pure function.
//
// ADR-0003 §"Stage D — Capacity-aware soft sort": after dispatchRoute
// returns the ranked candidate list from the Router, consult the
// latest GovernorSnapshot per candidate and re-rank so candidates
// reporting SnapshotStateQueueFull or SnapshotStateGovernorSaturated
// move to the tail while preserving their relative order. NO
// candidate is dropped — this is a soft penalty, not a hard filter.
// The forwarder still has the chance to drain a saturated queue and
// the executor's failover ladder handles terminal failure.
//
// The function is pure: it does not mutate the input slice, does not
// call into Pipeline, and does no I/O. The snapshot lookup is
// supplied by the caller as a closure so the same code paths can be
// exercised against a stub SnapshotProvider in tests.

// ApplySoftPenalty re-ranks a candidate list using the supplied
// per-cred snapshot lookup.
//
// Behavior:
//   - Candidates whose snapFn returns a non-Ready, non-Unknown state
//     (QueueFull or GovernorSaturated) move to the end of the result.
//   - Relative order is preserved inside each bucket (Ready vs. demoted).
//   - snapFn == nil is treated as "all Ready" — the function returns a
//     defensive copy of the input.
//   - snapFn returning (_, false) for a given cred is treated as Ready
//     (fail-open: a missing snapshot must not penalize a healthy
//     candidate).
//   - The input slice is never mutated.
//
// Returns nil when input is nil; returns an empty (non-nil) slice when
// input is empty so callers can range over the result unconditionally.
func ApplySoftPenalty(candidates []CredentialRef, snapFn func(credID int) (SnapshotState, bool)) []CredentialRef {
	if len(candidates) == 0 {
		if candidates == nil {
			return nil
		}
		// Preserve the empty-but-non-nil contract callers rely on.
		out := make([]CredentialRef, 0, len(candidates))
		return out
	}
	if snapFn == nil {
		// No snapshot info → no-op (defensive copy).
		out := make([]CredentialRef, len(candidates))
		copy(out, candidates)
		return out
	}

	// Single-pass partition into two slices. The Ready bucket keeps the
	// original index order; the demoted bucket accumulates tail entries
	// in the order they were first encountered. Concatenating yields a
	// stable sort without pulling in sort.SliceStable (which would
	// require a closure + reflection on int).
	keep := make([]CredentialRef, 0, len(candidates))
	demote := make([]CredentialRef, 0, len(candidates))
	for _, c := range candidates {
		state, ok := snapFn(c.CredentialID)
		if !ok || shouldKeep(state) {
			keep = append(keep, c)
		} else {
			demote = append(demote, c)
		}
	}
	if len(demote) == 0 {
		return keep
	}
	out := make([]CredentialRef, 0, len(candidates))
	out = append(out, keep...)
	out = append(out, demote...)
	return out
}

// shouldKeep reports whether a state should remain in the head bucket.
// SnapshotStateUnknown (BackendErr != nil path) is treated as keep
// because the existing failover ladder in dispatch already handles
// backend faults via errCapacitySaturated — we must not compound the
// penalty by demoting an unknown-state candidate that might still
// succeed at the forwarder layer.
func shouldKeep(state SnapshotState) bool {
	switch state {
	case SnapshotStateQueueFull, SnapshotStateGovernorSaturated:
		return false
	default:
		// Ready, Unknown (fail-open), and any future state land in keep.
		return true
	}
}

// SnapshotFnFromProvider adapts a SnapshotProvider to the snapFn
// signature consumed by ApplySoftPenalty. The returned closure holds
// only the interface value; it is safe to share across goroutines
// (Pipeline.SnapshotForCred is internally serialized via
// credStateCacheMu.RLock).
//
// Returns nil when p is nil so the caller can pass the result
// directly to ApplySoftPenalty without an extra guard.
func SnapshotFnFromProvider(p SnapshotProvider) func(int) (SnapshotState, bool) {
	if p == nil {
		return nil
	}
	return func(credID int) (SnapshotState, bool) {
		return p.SnapshotForCred(credID)
	}
}
