package dispatch

// Stage F — Pipeline.ApplyPolicy live-path tests.
//
// These tests pin the contract declared in policy_applier.go:17:
//   - Strict-monotonic revision (no-op replay is allowed and returns nil)
//   - Atomic per-cred Governor swap on a live credForwarder
//   - Spec-staged Governor consumed by the FIRST getOrCreateForwarder
//     after a publish-before-traffic (cold start order)
//   - Backend NotifyRevisions runs BEFORE any local swap; a backend
//     error leaves the active revision pinned
//   - ApplyPolicy on an empty Pipeline is a safe no-op apart from
//     stamping the revision

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestApplyPolicyRejectsZeroRevision(t *testing.T) {
	p := NewPipeline(Deps{})
	defer p.Stop()

	if err := p.ApplyPolicy(context.Background(), GovernorPolicy{Revision: 0}); err == nil {
		t.Fatal("ApplyPolicy with revision=0 must return error")
	}
	if got := p.ActiveRevision(); got != 0 {
		t.Fatalf("active revision after rejected zero-rev: got %d want 0", got)
	}
}

func TestApplyPolicyOnEmptyPipelineOnlyStampsRevision(t *testing.T) {
	p := NewPipeline(Deps{})
	defer p.Stop()

	if err := p.ApplyPolicy(context.Background(), GovernorPolicy{
		Revision:    3,
		GeneratedAt: time.Now(),
	}); err != nil {
		t.Fatalf("ApplyPolicy: %v", err)
	}
	if got := p.ActiveRevision(); got != 3 {
		t.Fatalf("active revision: got %d want 3", got)
	}
}

func TestApplyPolicyRejectsNonMonotonicRevision(t *testing.T) {
	p := NewPipeline(Deps{})
	defer p.Stop()

	if err := p.ApplyPolicy(context.Background(), GovernorPolicy{Revision: 5}); err != nil {
		t.Fatalf("first ApplyPolicy: %v", err)
	}
	// Same revision is a no-op (publisher replay).
	if err := p.ApplyPolicy(context.Background(), GovernorPolicy{Revision: 5}); err != nil {
		t.Fatalf("replay ApplyPolicy: %v", err)
	}
	// Older revision is also a no-op (out-of-order NOTIFY).
	if err := p.ApplyPolicy(context.Background(), GovernorPolicy{Revision: 3}); err != nil {
		t.Fatalf("older ApplyPolicy: %v", err)
	}
	if got := p.ActiveRevision(); got != 5 {
		t.Fatalf("active revision: got %d want 5 (must not regress)", got)
	}
}

func TestApplyPolicySwapsGovernorOnExistingForwarder(t *testing.T) {
	p := NewPipeline(Deps{})
	defer p.Stop()

	ref := cred(7, ModeConcurrency, 2)
	cf := p.getOrCreateForwarder(ref)
	if cf == nil {
		t.Fatal("getOrCreateForwarder returned nil")
	}
	original, ok := cf.govLocked().(*concurrencyGovernor)
	if !ok {
		t.Fatalf("initial governor type = %T, want *concurrencyGovernor", cf.govLocked())
	}
	if original.cap != 2 {
		t.Fatalf("initial governor cap = %d, want 2", original.cap)
	}

	// Stage F: publish a new spec that doubles the cap on the same
	// credential. The live forwarder's governor must be swapped.
	newPolicy := GovernorPolicy{
		Revision:    1,
		GeneratedAt: time.Now(),
		Specs: []GovernorSpec{
			{CredentialID: 7, Mode: ModeConcurrency, Limit: 8},
		},
	}
	if err := p.ApplyPolicy(context.Background(), newPolicy); err != nil {
		t.Fatalf("ApplyPolicy: %v", err)
	}
	if got := p.ActiveRevision(); got != 1 {
		t.Fatalf("active revision: got %d want 1", got)
	}
	swapped, ok := cf.govLocked().(*concurrencyGovernor)
	if !ok {
		t.Fatalf("swapped governor type = %T, want *concurrencyGovernor", cf.govLocked())
	}
	if swapped == original {
		t.Fatal("ApplyPolicy did not replace the Governor pointer")
	}
	if swapped.cap != 8 {
		t.Fatalf("swapped governor cap = %d, want 8", swapped.cap)
	}
}

func TestApplyPolicyStagePreStartGovernor(t *testing.T) {
	// Stage F: a policy published BEFORE the first request creates a
	// forwarder. The spec-derived Governor must be the one the freshly
	// constructed forwarder ends up with, not the CredentialRef default.
	p := NewPipeline(Deps{})
	defer p.Stop()

	if err := p.ApplyPolicy(context.Background(), GovernorPolicy{
		Revision:    1,
		GeneratedAt: time.Now(),
		Specs: []GovernorSpec{
			{CredentialID: 11, Mode: ModeConcurrency, Limit: 12},
		},
	}); err != nil {
		t.Fatalf("ApplyPolicy (pre-traffic): %v", err)
	}

	cf := p.getOrCreateForwarder(cred(11, ModeConcurrency, 99))
	if cf == nil {
		t.Fatal("getOrCreateForwarder returned nil")
	}
	got, ok := cf.govLocked().(*concurrencyGovernor)
	if !ok {
		t.Fatalf("staged governor type = %T, want *concurrencyGovernor", cf.govLocked())
	}
	if got.cap != 12 {
		t.Fatalf("staged governor cap = %d, want 12 (spec-driven, not 99 from CredentialRef)", got.cap)
	}
	if pending := len(p.pendingGov); pending != 0 {
		t.Fatalf("pendingGov not drained after claim: %d entries remain", pending)
	}
}

func TestApplyPolicyCallsBackendNotifyRevisions(t *testing.T) {
	p := NewPipeline(Deps{})
	defer p.Stop()

	backend := &fakeBackend{kind: BackendRedisEnforce, name: "redis"}
	p.SetGovernorBackend(backend)

	if err := p.ApplyPolicy(context.Background(), GovernorPolicy{
		Revision:    7,
		GeneratedAt: time.Now(),
		Specs: []GovernorSpec{
			{CredentialID: 13, Mode: ModeConcurrency, Limit: 3},
		},
	}); err != nil {
		t.Fatalf("ApplyPolicy: %v", err)
	}
	if got, want := len(backend.notifs), 1; got != want {
		t.Fatalf("NotifyRevisions calls: got %d want %d", got, want)
	}
	if backend.notifs[0] != 7 {
		t.Fatalf("NotifyRevisions rev: got %d want 7", backend.notifs[0])
	}
}

func TestApplyPolicyBackendNotifyFailureLeavesRevisionPinned(t *testing.T) {
	p := NewPipeline(Deps{})
	defer p.Stop()

	ref := cred(15, ModeConcurrency, 1)
	cf := p.getOrCreateForwarder(ref)
	if cf == nil {
		t.Fatal("getOrCreateForwarder returned nil")
	}
	original, _ := cf.govLocked().(*concurrencyGovernor)

	want := errors.New("redis: cluster down")
	backend := &fakeBackend{kind: BackendRedisEnforce, name: "redis", notifs: nil}
	backend.notifyErr = want
	p.SetGovernorBackend(backend)

	err := p.ApplyPolicy(context.Background(), GovernorPolicy{
		Revision:    1,
		GeneratedAt: time.Now(),
		Specs: []GovernorSpec{
			{CredentialID: 15, Mode: ModeConcurrency, Limit: 99},
		},
	})
	if !errors.Is(err, want) {
		t.Fatalf("ApplyPolicy error: got %v want %v", err, want)
	}
	if got := p.ActiveRevision(); got != 0 {
		t.Fatalf("active revision after backend failure: got %d want 0", got)
	}
	current, _ := cf.govLocked().(*concurrencyGovernor)
	if current != original {
		t.Fatalf("Governor pointer swapped despite backend failure: was %p now %p", original, current)
	}
}

func TestApplyPolicyLocalBackendSkipsNotifyRevisions(t *testing.T) {
	// Stage F: with a non-Redis backend (Local/Shadow) ApplyPolicy must
	// still swap the governor but MUST NOT call NotifyRevisions (only
	// RedisEnforce uses the round-trip; local/Shadow are passive
	// observers).
	p := NewPipeline(Deps{})
	defer p.Stop()

	backend := &fakeBackend{kind: BackendLocal, name: "local"}
	p.SetGovernorBackend(backend)

	ref := cred(20, ModeConcurrency, 2)
	cf := p.getOrCreateForwarder(ref)
	if cf == nil {
		t.Fatal("getOrCreateForwarder returned nil")
	}

	if err := p.ApplyPolicy(context.Background(), GovernorPolicy{
		Revision:    2,
		GeneratedAt: time.Now(),
		Specs: []GovernorSpec{
			{CredentialID: 20, Mode: ModeConcurrency, Limit: 16},
		},
	}); err != nil {
		t.Fatalf("ApplyPolicy: %v", err)
	}
	if got, want := len(backend.notifs), 0; got != want {
		t.Fatalf("NotifyRevisions calls on local backend: got %d want %d", got, want)
	}
	got, _ := cf.govLocked().(*concurrencyGovernor)
	if got.cap != 16 {
		t.Fatalf("local swap cap = %d, want 16", got.cap)
	}
}
