package dispatch

// Stage B.3 — Pipeline.SetGovernorBackend injection seam tests.
//
// The seam is intentionally minimal: store / get / swap / nil-safety /
// race-cleanliness. Stage D/E is responsible for consuming the stored
// backend inside credForwarder.gov construction; this commit proves
// the container-level contract only.

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestSetGovernorBackendNilReceiverIsNoOp(t *testing.T) {
	var p *Pipeline
	// Must not panic.
	p.SetGovernorBackend(NewLocalBackend("nil-receiver"))
	if v := p.GovernorBackend(); v != nil {
		t.Fatalf("nil receiver must report nil backend, got %v", v)
	}
}

func TestSetGovernorBackendNilArgumentDisablesBackend(t *testing.T) {
	f := &fakeDeps{refsByModel: map[string][]CredentialRef{}}
	p := f.pipeline()
	defer p.Stop()

	p.SetGovernorBackend(nil)
	if v := p.GovernorBackend(); v != nil {
		t.Fatalf("passing nil must clear the backend, got %v", v)
	}
}

func TestSetGovernorBackendStoresAndReturns(t *testing.T) {
	f := &fakeDeps{refsByModel: map[string][]CredentialRef{}}
	p := f.pipeline()
	defer p.Stop()

	want := NewLocalBackend("stage-b-test")
	p.SetGovernorBackend(want)
	if got := p.GovernorBackend(); got != want {
		t.Fatalf("GovernorBackend() = %v, want %v", got, want)
	}
}

func TestSetGovernorBackendSwapsInPlace(t *testing.T) {
	f := &fakeDeps{refsByModel: map[string][]CredentialRef{}}
	p := f.pipeline()
	defer p.Stop()

	first := NewLocalBackend("first")
	second := NewLocalBackend("second")

	p.SetGovernorBackend(first)
	if got := p.GovernorBackend(); got != first {
		t.Fatalf("after first set: got %v want %v", got, first)
	}
	p.SetGovernorBackend(second)
	if got := p.GovernorBackend(); got != second {
		t.Fatalf("after swap: got %v want %v", got, second)
	}
}

func TestSetGovernorBackendConcurrentRaceClean(t *testing.T) {
	f := &fakeDeps{refsByModel: map[string][]CredentialRef{}}
	p := f.pipeline()
	defer p.Stop()

	// 8 goroutines × 200 set/get iterations under -race. The backendMu
	// guarantees a clean swap; without it the race detector would flag
	// a write/read racy access on p.governorBackend.
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func(id int) {
			defer wg.Done()
			b := NewLocalBackend("g-" + strconv.Itoa(id))
			for j := 0; j < 200; j++ {
				p.SetGovernorBackend(b)
			}
		}(i)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				_ = p.GovernorBackend()
			}
		}()
	}
	wg.Wait()
	if got := p.GovernorBackend(); got == nil {
		t.Fatalf("GovernorBackend() returned nil after concurrent sets")
	}
}

func TestPipelineWiresGovernorBackend(t *testing.T) {
	// Mirror the queue_mirror_test.go pattern: build a real Pipeline
	// with controllable callbacks, wire a backend via SetGovernorBackend,
	// and verify the getter returns it across Start.
	f := &fakeDeps{
		refsByModel: map[string][]CredentialRef{
			"m": {cred(1, ModeConcurrency, 4)},
		},
	}
	p := f.pipeline()
	defer p.Stop()

	backend := NewLocalBackend("wired-test")
	p.SetGovernorBackend(backend)
	p.Start()
	if got := p.GovernorBackend(); got != backend {
		t.Fatalf("GovernorBackend() after Start: got %v want %v", got, backend)
	}
}

func TestPipelineUsesRedisEnforceBackendForConcurrencyForwarder(t *testing.T) {
	ref := cred(7, ModeConcurrency, 3)
	backendGovernor := newNoopGovernor()
	backend := &fakeBackend{kind: BackendRedisEnforce, name: "redis", newGov: backendGovernor}
	p := NewPipeline(Deps{})
	defer p.Stop()
	p.SetGovernorBackend(backend)

	forwarder := p.getOrCreateForwarder(ref)
	if forwarder.gov != backendGovernor {
		t.Fatalf("forwarder governor = %T, want backend governor", forwarder.gov)
	}
	if backend.newCalls != 1 {
		t.Fatalf("backend New calls = %d, want 1", backend.newCalls)
	}
}

func TestPipelineUsesRedisEnforceForDefaultConcurrencyMode(t *testing.T) {
	backendGovernor := newNoopGovernor()
	backend := &fakeBackend{kind: BackendRedisEnforce, name: "redis", newGov: backendGovernor}
	p := NewPipeline(Deps{})
	defer p.Stop()
	p.SetGovernorBackend(backend)

	forwarder := p.getOrCreateForwarder(CredentialRef{CredentialID: 10, ConcurrencyLimit: 2})
	if forwarder.gov != backendGovernor {
		t.Fatalf("forwarder governor = %T, want backend governor for default concurrency mode", forwarder.gov)
	}
}

func TestPipelineKeepsLocalGovernorForRateModes(t *testing.T) {
	backend := &fakeBackend{kind: BackendRedisEnforce, name: "redis", newGov: newNoopGovernor()}
	p := NewPipeline(Deps{})
	defer p.Stop()
	p.SetGovernorBackend(backend)

	forwarder := p.getOrCreateForwarder(CredentialRef{CredentialID: 8, ConcurrencyMode: ModeRPM, RPMLimit: 10})
	if forwarder.gov.Mode() != ModeRPM {
		t.Fatalf("rate governor mode = %q, want %q", forwarder.gov.Mode(), ModeRPM)
	}
	if backend.newCalls != 0 {
		t.Fatalf("backend New calls = %d, want 0 for RPM", backend.newCalls)
	}
}

func TestPipelineFailsClosedWhenRedisGovernorConstructionFails(t *testing.T) {
	backend := &fakeBackend{kind: BackendRedisEnforce, name: "redis", newErr: errors.New("redis unavailable")}
	p := NewPipeline(Deps{})
	defer p.Stop()
	p.SetGovernorBackend(backend)

	forwarder := p.getOrCreateForwarder(cred(9, ModeConcurrency, 1))
	err := forwarder.gov.Acquire(context.Background(), NewQueuedRequest("r", "t", "m", context.Background(), nil), time.Now())
	if !IsGovernorUnavailable(err) {
		t.Fatalf("Acquire error = %v, want ErrGovernorUnavailable", err)
	}
}
