package dispatch

import (
	"context"
	"fmt"
	"time"
)

// GovernorBackendKind names the concrete implementation of GovernorBackend.
// It is the closed-enum identifier used by Stage C metrics labels and the
// management-layer registry. Extend by adding a new constant here and
// wiring the corresponding backend; do NOT produce runtime values from
// user input (high-cardinality blast radius).
type GovernorBackendKind string

const (
	// BackendLocal is the in-process token-bucket / semaphore backend —
	// the four implementations in governor.go today. Always available,
	// no external dependency; default when no backend is configured.
	BackendLocal GovernorBackendKind = "local"
	// BackendRedisEnforce is the strict shared-cluster backend (Lua
	// sliding window + lease token). Lands in Stage B; fail-closed on
	// Redis outage (ErrGovernorUnavailable, no memory fallback).
	BackendRedisEnforce GovernorBackendKind = "redis_enforce"
	// BackendRedisShadow observes Redis state without enforcing it; lets
	// operators soak-test the enforcement path before flipping it on.
	// Lands in Stage B. Never gates admission.
	BackendRedisShadow GovernorBackendKind = "redis_shadow"
)

// GovernorBackend produces a per-spec Governor and participates in the
// policy-snapshot lifecycle. It is the MANAGEMENT interface, NOT the
// hot-path admit/release contract — that remains the existing Governor
// interface (Mode / Acquire / Release). Stage A only requires the
// lifecycle methods declared here; per-spec New(spec) lands in Stage B.
//
// Implementations are expected to be safe for concurrent Open / Close /
// NotifyRevisions calls. The backend registry (Stage E) holds a single
// instance per kind per process.
type GovernorBackend interface {
	// Kind is the closed-enum identifier used in metrics + logs.
	Kind() GovernorBackendKind
	// Name is a free-form instance identifier (e.g. "redis-eu-west-1");
	// intended for human/operator diagnostics, never a metric label key.
	Name() string
	// Open initializes the backend (dials, warms caches). Idempotent; a
	// second call without an intervening Close returns nil.
	Open(ctx context.Context) error
	// Close releases any held resources. Idempotent; safe to call on an
	// unopened backend.
	Close(ctx context.Context) error
	// NotifyRevisions is called by ApplyPolicySnapshot whenever a new
	// GovernorPolicy revision has been published on the spec version the
	// backend observes. Backends that do not own their own spec state
	// (e.g. LocalBackend) return nil. Stage B backends use it to warm
	// their in-memory lease pools. Stage A: signature only — no backend
	// is wired into the production runtime yet.
	NotifyRevisions(ctx context.Context, rev uint64) error
	// New constructs a per-spec Governor. Stage B extension: the local
	// factory wraps newGovernor(CredentialRef); the Redis factories
	// wrap a Lua-backed sliding window. Errors returned here are
	// wrapped as ErrGovernorUnavailable so the management layer can
	// distinguish "backend misconfigured" from "backend up but the
	// admission call returned capacity-saturated" (the latter is
	// returned from the Governor's own Acquire method, not from New).
	New(ctx context.Context, spec GovernorSpec) (Governor, error)
}

// unavailableGovernorBackend preserves strict backend selection when Redis is
// unavailable. It deliberately never falls back to an in-process governor.
type unavailableGovernorBackend struct {
	name  string
	cause error
}

func NewUnavailableGovernorBackend(name string, cause error) GovernorBackend {
	if name == "" {
		name = "redis-enforce-unavailable"
	}
	if cause == nil {
		cause = ErrGovernorUnavailable
	}
	return &unavailableGovernorBackend{name: name, cause: cause}
}

func (b *unavailableGovernorBackend) Kind() GovernorBackendKind   { return BackendRedisEnforce }
func (b *unavailableGovernorBackend) Name() string                { return b.name }
func (b *unavailableGovernorBackend) Open(context.Context) error  { return nil }
func (b *unavailableGovernorBackend) Close(context.Context) error { return nil }
func (b *unavailableGovernorBackend) NotifyRevisions(context.Context, uint64) error {
	return fmt.Errorf("%w: %v", ErrGovernorUnavailable, b.cause)
}
func (b *unavailableGovernorBackend) New(context.Context, GovernorSpec) (Governor, error) {
	return nil, fmt.Errorf("%w: %v", ErrGovernorUnavailable, b.cause)
}

type unavailableGovernor struct{}

func (unavailableGovernor) Mode() string { return ModeConcurrency }
func (unavailableGovernor) Acquire(context.Context, *QueuedRequest, time.Time) error {
	return ErrGovernorUnavailable
}
func (unavailableGovernor) Release(*QueuedRequest) {}
