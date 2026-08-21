package dispatch

import (
	"context"
	"sync"
)

// LocalBackend is the in-process GovernorBackend. It owns no external
// resources and exists to keep the management-layer plumbing testable
// without a Redis dependency. Stage B will add RedisEnforceBackend /
// RedisShadowBackend alongside it; Stage E will register LocalBackend
// as the production default until the cluster-wide flag is flipped.
//
// Stage A: LocalBackend satisfies only the GovernorBackend LIFECYCLE
// interface. The per-spec New(GovernorSpec) method lands in Stage B
// (LocalBackend will wrap newGovernor(CredentialRef) — the existing
// in-process factory from governor.go).
//
// LocalBackend's contract for Stage B/C/D/E:
//
//   - It does NOT track per-spec revision state. NotifyRevisions is a
//     no-op; the backend always honors whatever spec the forwarder
//     hands it at New() time. The forwarder is the only thing that
//     ever holds a Governor instance under LocalBackend.
//   - It is always "available"; Open/Close return nil. The lifecycle
//     methods exist so the management-layer registry can wire it
//     uniformly alongside Stage B backends without per-kind branches.
type LocalBackend struct {
	name string

	mu        sync.Mutex
	openCount int
	opened    bool
	closed    bool
}

// NewLocalBackend constructs the in-process backend. The name is the
// free-form diagnostic identifier surfaced via Name() and never used as
// a metric label key.
func NewLocalBackend(name string) *LocalBackend {
	if name == "" {
		name = "local"
	}
	return &LocalBackend{name: name}
}

// Kind is BackendLocal — the closed-enum identifier Stage C metrics use.
func (b *LocalBackend) Kind() GovernorBackendKind { return BackendLocal }

// Name is the diagnostic identifier (e.g. "process-A", "process-B").
func (b *LocalBackend) Name() string { return b.name }

// Open is idempotent. A second call without an intervening Close is a
// no-op; the counter remains at one. Calling Open on an already-closed
// backend reopens it (fresh in-process state).
func (b *LocalBackend) Open(_ context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		b.closed = false
		b.openCount = 0
	}
	if b.opened {
		return nil
	}
	b.opened = true
	b.openCount++
	return nil
}

// Close is idempotent. Safe to call on an unopened backend.
func (b *LocalBackend) Close(_ context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.opened {
		return nil
	}
	b.opened = false
	b.closed = true
	return nil
}

// NotifyRevisions is a no-op for LocalBackend: per-spec revision state
// is owned by the forwarder, not the backend. Returns nil so the
// management-layer applier can wire it uniformly with Stage B backends.
func (b *LocalBackend) NotifyRevisions(_ context.Context, _ uint64) error {
	return nil
}

// New builds the per-spec in-process Governor by mapping GovernorSpec to
// the existing CredentialRef shape and calling newGovernor. Stage B: this
// is the load-bearing entry point that lets the management layer wire a
// LocalBackend uniformly alongside the Redis backends. Behaviour is
// byte-identical to the in-prod newGovernor factory because the wiring
// passes through it unchanged.
func (b *LocalBackend) New(_ context.Context, spec GovernorSpec) (Governor, error) {
	ref := CredentialRef{
		CredentialID:     spec.CredentialID,
		ProviderID:       spec.ProviderID,
		ConcurrencyMode:  spec.Mode,
		ConcurrencyLimit: spec.Limit,
		RPMLimit:         spec.RPMLimit,
		TPMLimit:         spec.TPMLimit,
	}
	return newGovernor(ref), nil
}