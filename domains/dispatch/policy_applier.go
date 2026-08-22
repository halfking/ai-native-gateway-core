package dispatch

import (
	"context"
	"sync"
)

// ApplyPolicySnapshot is the abstract target of a policy publication.
// It is a stable proxy so the management layer does not need to know
// whether the live runtime is the local Pipeline (single-process test)
// or a remote cluster proxy (Stage E production).
//
// Implementations MUST be safe to call ApplyPolicy concurrently with
// Acquire/Release on the underlying Governor set — Stage E pins this via
// shadow-load tests. ActiveRevision MUST be safe to call concurrently
// with ApplyPolicy.
type ApplyPolicySnapshot interface {
	// ApplyPolicy atomically replaces the active GovernorPolicy at the
	// proxy. A revision that is not strictly greater than ActiveRevision()
	// MUST be rejected — Stage E "no-op publication" uses an unchanged
	// revision to convey "no change" without bumping the counter.
	//
	// On backend communication failure (Stage E: Redis unreachable),
	// implementations return ErrGovernorUnavailable so the caller can
	// decide between surface / retry / log.
	ApplyPolicy(ctx context.Context, pol GovernorPolicy) error
	// ActiveRevision returns the currently applied revision; 0 means no
	// policy has been applied yet. Used for diagnostics and for the Stage
	// E "is this process synced?" check.
	ActiveRevision(ctx context.Context) uint64
}

// recordingApplier is an in-test stub that records every ApplyPolicy call
// and serializes concurrent calls. It is exported via NewRecordingApplier
// so tests across the dispatch package can share it without duplicating
// the locking / history logic. Stage A keeps it in this file; if Stage B
// wants a Redis-backed impl, that lands in policy_applier_redis.go and
// uses the same interface.
type recordingApplier struct {
	mu       sync.Mutex
	active   uint64
	last     GovernorPolicy
	calls    int
	applyErr error
}

// NewRecordingApplier builds a stub ApplyPolicySnapshot for tests.
// applyErr, when non-nil, is returned from every ApplyPolicy call.
func NewRecordingApplier(applyErr error) ApplyPolicySnapshot {
	return &recordingApplier{applyErr: applyErr}
}

func (r *recordingApplier) ApplyPolicy(ctx context.Context, pol GovernorPolicy) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.applyErr != nil {
		return r.applyErr
	}
	r.last = pol
	r.active = pol.Revision
	r.calls++
	return nil
}

func (r *recordingApplier) ActiveRevision(ctx context.Context) uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.active
}

// AsRecordingApplier returns the underlying *recordingApplier if impl was
// built via NewRecordingApplier; ok=false otherwise. Used by tests to
// assert on call counts without forcing every assertion through a
// separate query API.
func AsRecordingApplier(impl ApplyPolicySnapshot) (*recordingApplier, bool) {
	r, ok := impl.(*recordingApplier)
	return r, ok
}
