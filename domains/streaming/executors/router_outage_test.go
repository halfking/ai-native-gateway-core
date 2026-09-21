// Outage-mirror integration tests for the router's authoritative branches
// (2026-09-04 availability gear). Unlike the unit tests in
// manager_outage_test.go these drive the REAL router decision path with a
// real *ursmv2.Manager backed by miniredis, pinning the wiring in
// planCandidates: a dead Redis must serve from the node mirror and record
// StateSourceOutageMirror, while a deliberately closed gate on a REACHABLE
// Redis must keep failing closed.
package executors

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/statesource"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// TestPlanCandidates_AuthoritativeOutageMirror_ServesFromMirror pins the
// full Redis-crash scenario: mirror warmed while Redis is alive, then Redis
// dies (the Ready read itself fails) → the outage gear keeps routing and
// the outer source is outage_mirror, not fallback.
func TestPlanCandidates_AuthoritativeOutageMirror_ServesFromMirror(t *testing.T) {
	statesource.ResetForTest()
	mgr, mr := buildV2Manager(t, api.ModeAuthoritative)
	seedV2Node(t, mr, 1, "m", 1, true)
	seedV2Node(t, mr, 2, "m", 1, true)

	r := NewRouter(nil, nil)
	r.URSMv2 = mgr

	// Warm-up while Redis is alive: the pipeline read backfills the LRU
	// mirror (soft TTL 30s in buildV2Manager, outage window 30m default).
	if got := r.PlanCandidatesWithContext(
		context.Background(), candidateSet(), nil, &provider.Policy{}, nil, "t", "m", "req-warm",
	); len(got) == 0 {
		t.Fatal("warm-up call must succeed while Redis is alive")
	}

	// Kill Redis. The next call must NOT return empty (the pre-2026-09
	// behaviour was a guaranteed 503 here).
	mr.Close()
	got := r.PlanCandidatesWithContext(
		context.Background(), candidateSet(), nil, &provider.Policy{}, nil, "t", "m", "req-outage",
	)
	if len(got) == 0 {
		t.Fatal("outage gear must keep serving candidates from the node mirror while Redis is down")
	}

	snap := statesource.Snapshot()
	if n := snap[statesource.StateSourceOutageMirror]; n != 1 {
		t.Fatalf("outage_mirror count = %d, want 1; full snapshot = %+v", n, snap)
	}
	if n := snap[statesource.StateSourceFallback]; n != 0 {
		t.Fatalf("served requests must not be counted as fallback, got %d", n)
	}
}

// TestPlanCandidates_AuthoritativeClosedGateAliveRedis_StaysFailClosed pins
// the safety boundary: a deliberately closed recovery gate on a REACHABLE
// Redis is never bypassed by the outage gear (the manager's PING refuses),
// so the router keeps rejecting and records fallback.
func TestPlanCandidates_AuthoritativeClosedGateAliveRedis_StaysFailClosed(t *testing.T) {
	statesource.ResetForTest()
	mgr, mr := buildV2Manager(t, api.ModeAuthoritative)
	seedV2Node(t, mr, 1, "m", 1, true)
	seedV2Node(t, mr, 2, "m", 1, true)

	r := NewRouter(nil, nil)
	r.URSMv2 = mgr

	// Warm the mirror, then close the gate explicitly (Redis stays alive).
	if got := r.PlanCandidatesWithContext(
		context.Background(), candidateSet(), nil, &provider.Policy{}, nil, "t", "m", "req-warm",
	); len(got) == 0 {
		t.Fatal("warm-up call must succeed while the gate is open")
	}
	if err := mgr.SetReady(context.Background(), false); err != nil {
		t.Fatalf("close gate: %v", err)
	}

	got := r.PlanCandidatesWithContext(
		context.Background(), candidateSet(), nil, &provider.Policy{}, nil, "t", "m", "req-closed",
	)
	if len(got) != 0 {
		t.Fatalf("deliberate gate closure must keep failing closed, got %+v", got)
	}

	snap := statesource.Snapshot()
	if n := snap[statesource.StateSourceOutageMirror]; n != 0 {
		t.Fatalf("outage gear must not engage on a reachable Redis, got %d", n)
	}
	if n := snap[statesource.StateSourceFallback]; n != 1 {
		t.Fatalf("fallback count = %d, want 1; full snapshot = %+v", n, snap)
	}
}
