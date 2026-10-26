package admin

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	ursmv2 "github.com/kaixuan/llm-gateway-go/domains/ursm/v2"
	v2api "github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/redis/go-redis/v9"
)

// 2026-10-16 regression for the reported grok-4.6 resolve/dispatch divergence.
// The initial 211 DB snapshot had credential 63 healthy/ready and circuit=open.
// The available URSM view below is a controlled fixture, not a captured live
// view; the supplied gateway key returned invalid_key, so no authenticated
// failing request was reproduced. These tests prove that this DB shape cannot
// be called routable by resolve when production Candidate.IsAvailable blocks it.

func TestLivePlanOrderFiltersCredentialCircuitAndCompactsRanks(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	cfg := ursmv2.DefaultConfig()
	cfg.Mode = v2api.ModeAuthoritative
	mgr := ursmv2.New(ursmv2.Dependencies{Redis: rdb, Config: cfg})
	if err := mgr.SetReady(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	blocked := livePlanOrderTestCandidate(101, 1, 100)
	// Post-reconciliation dispatch admission gates on availability_state,
	// quota, lifecycle and balance — NOT the lossy circuit mirror. Block this
	// candidate through a real admission field so the panel filter + rank
	// compaction are exercised against what production actually drops.
	blocked.AvailabilityState = "suspended"
	allowed := livePlanOrderTestCandidate(202, 2, 100)
	mirrorOpen := livePlanOrderTestCandidate(303, 3, 100)
	mirrorOpen.CircuitState = "open" // DB mirror: display-only, must survive
	for _, key := range []string{"ursm:v2:node:t:101:glm-5.2", "ursm:v2:node:t:202:glm-5.2", "ursm:v2:node:t:303:glm-5.2"} {
		mr.HSet(key, "available", "1", "generation", "1", "source_priority", "10", "fail_streak", "0")
	}
	router := executors.NewRouter(nil, nil)
	router.URSMv2 = mgr
	cands := []provider.Candidate{blocked, allowed, mirrorOpen}
	planned := router.PlanCandidatesPinned(context.Background(), cands, nil, nil, provider.DefaultPolicy(), nil, "t", "glm-5.2", "precondition")
	if len(planned) != 3 || planned[0].CredentialID != 101 {
		t.Fatalf("precondition: authoritative router must retain all three candidates in order: %v", planned)
	}
	h := &Handler{}
	h.SetLiveRoutingSource(&LiveRoutingSource{Router: router, Resolver: &fakeLiveResolver{cands: cands}})
	entries, source := h.livePlanOrder(context.Background(), "glm-5.2", "t")
	if source != "live-router" || len(entries) != 2 {
		t.Fatalf("dispatch-aligned compact plan: source=%s entries=%v", source, entries)
	}
	if entries[0]["credential_id"] != 202 || entries[0]["rank"] != 1 {
		t.Fatalf("suspended candidate must be dropped and rank compacted: %v", entries)
	}
	if entries[1]["credential_id"] != 303 || entries[1]["rank"] != 2 {
		t.Fatalf("circuit-mirror-open candidate must survive (send-time breaker decides): %v", entries)
	}
}

// TestApplyCredentialCircuitGateSurfacesMirrorWithoutBlocking is the primary
// post-reconciliation assertion: the DB breaker mirror must surface for the
// node drawer (circuit_state / cooling_until) but must NOT flip the resolve
// verdict, because dispatch admission (provider.Candidate.IsAvailable) no
// longer gates on that mirror — the in-memory Circuit.Allow at the send point
// owns breaker enforcement and needs its half-open probe to reach the
// candidate. Blocking resolve on a stale 'open' mirror is how the Vapeur
// stranding incident presented.
func TestApplyCredentialCircuitGateSurfacesMirrorWithoutBlocking(t *testing.T) {
	cooling := "2026-10-16T00:39:32Z"
	candidates := []resolveCandidate{
		{
			CredentialID: 63, ModelName: "grok-4.6",
			Routable: true, RuntimeRoutable: true, DBEligible: true,
			CircuitState:   "closed", // URSM said closed
			dbCircuitState: "open", dbCoolingUntil: &cooling,
		},
	}

	applyCredentialCircuitGate(candidates)

	got := candidates[0]
	if !got.Routable || !got.RuntimeRoutable {
		t.Fatalf("cred 63: routable=%v runtime_routable=%v, want both true — dispatch admission does not gate on the lossy mirror",
			got.Routable, got.RuntimeRoutable)
	}
	if got.BlockReason != "" {
		t.Fatalf("cred 63 block_reason = %q, want empty (display-only mirror)", got.BlockReason)
	}
	if got.CircuitState != "open" {
		t.Fatalf("cred 63 circuit_state = %q, want open (must mirror the DB mirror, not the URSM default)", got.CircuitState)
	}
	if got.CoolingUntil == nil || *got.CoolingUntil != cooling {
		t.Fatalf("cred 63 cooling_until = %v, want %q (the node drawer renders this next to 熔断)", got.CoolingUntil, cooling)
	}
}

// TestApplyCredentialCircuitGateAgreesWithDispatch is the anti-divergence
// assertion: the resolve verdict and the production dispatch verdict must be
// the SAME function of the same fields. It builds the exact production shape
// (circuit_state='open', everything else healthy/ready) and requires both
// sides to agree — post-2026-10-16 reconciliation, admission keeps the
// candidate available (breaker enforcement moved to Circuit.Allow at send).
func TestApplyCredentialCircuitGateAgreesWithDispatch(t *testing.T) {
	bal := 12.5
	// This is the production shape read on 211 for credential 63.
	slice := []resolveCandidate{{
		CredentialID: 63, ProviderID: 36, ModelName: "grok-4.6",
		Routable: true, RuntimeRoutable: true, DBEligible: true,
		LifecycleStatus: "active", AvailabilityState: "ready",
		QuotaState: "ok", BalanceUSD: &bal,
		CircuitState:   "closed",
		dbCircuitState: "open",
	}}

	// Production's own gate, built from the identical field values.
	prod := provider.Candidate{
		CredentialID:      63,
		ProviderID:        36,
		LifecycleStatus:   "active",
		AvailabilityState: "ready",
		QuotaState:        "ok",
		BalanceUSD:        &bal,
		CircuitState:      "open",
		Routable:          true,
	}

	applyCredentialCircuitGate(slice)

	if !prod.IsAvailable() {
		t.Fatalf("precondition: production admission must keep a circuit-mirror-open candidate (send-time Circuit.Allow enforces the breaker): %q", prod.UnavailableReason())
	}
	if strings.Contains(prod.UnavailableReason(), "circuit:open") {
		t.Fatalf("precondition: production reason = %q, must not contain circuit:open (lossy mirror is out of admission)", prod.UnavailableReason())
	}
	if !slice[0].Routable {
		t.Fatalf("resolve says unroutable while production says available — divergence reborn in the opposite direction")
	}
}

// TestApplyCredentialCircuitGateLeavesClosedRowsAlone is the negative control
// for over-blocking: a healthy credential must NOT be touched. Without this,
// a gate that unconditionally sets Routable=false would still pass the test
// above and the diagnostic would be useless in the other direction.
func TestApplyCredentialCircuitGateLeavesClosedRowsAlone(t *testing.T) {
	candidates := []resolveCandidate{
		{CredentialID: 63, Routable: true, RuntimeRoutable: true, dbCircuitState: "closed"},
		{CredentialID: 64, Routable: true, RuntimeRoutable: true, dbCircuitState: "half_open"},
		{CredentialID: 65, Routable: true, RuntimeRoutable: true, dbCircuitState: ""},
	}
	before := make([]string, len(candidates))
	for i, c := range candidates {
		before[i] = c.BlockReason
	}

	applyCredentialCircuitGate(candidates)

	for i, c := range candidates {
		if !c.Routable || !c.RuntimeRoutable {
			t.Fatalf("cred %d: routable=%v runtime_routable=%v, want untouched (dbCircuitState=%q must not block)",
				c.CredentialID, c.Routable, c.RuntimeRoutable, c.dbCircuitState)
		}
		if c.BlockReason != before[i] {
			t.Fatalf("cred %d: block_reason mutated %q → %q", c.CredentialID, before[i], c.BlockReason)
		}
	}
}

// TestApplyCredentialCircuitGatePreservesStrongerReason ensures the gate does
// not overwrite a more specific reason already established by the SQL view or
// the URSM overlay (e.g. an auth_failed credential that ALSO has a stale
// circuit_state='open').
func TestApplyCredentialCircuitGatePreservesStrongerReason(t *testing.T) {
	candidates := []resolveCandidate{
		{
			CredentialID: 44, Routable: false, RuntimeRoutable: false,
			BlockReason:    "availability_auth_failed",
			dbCircuitState: "open",
		},
	}

	applyCredentialCircuitGate(candidates)

	if got := candidates[0].BlockReason; got != "availability_auth_failed" {
		t.Fatalf("block_reason = %q, want the pre-existing availability_auth_failed preserved", got)
	}
	// The circuit state itself must still be surfaced — the node drawer reads it.
	if candidates[0].CircuitState != "open" {
		t.Fatalf("circuit_state = %q, want open even when a stronger block reason wins", candidates[0].CircuitState)
	}
}

// TestApplyURSMOverlayAppliesCredentialGate pins the ORDERING as a property of
// applyURSMOverlay rather than of a call site.
//
// A previous revision applied the credential gate as a separate statement in
// handleRoutingResolve. Moving that one line above applyURSMOverlay kept every
// other test green while silently reintroducing the incident, because
// resolveRuntimeDefaults() resets CircuitState to "closed" on every row and
// would overwrite whatever the gate had just set. The gate is therefore folded
// into applyURSMOverlay, and this test is the regression that bites if anyone
// separates them again.
func TestApplyURSMOverlayAppliesCredentialGate(t *testing.T) {
	candidates := []resolveCandidate{
		{
			CredentialID: 63, ModelName: "grok-4.6",
			Routable: true, RuntimeRoutable: true, DBEligible: true,
			dbCircuitState: "open",
		},
	}
	// URSM v2 reports the node healthy and out of cooldown — the disagreement
	// that produced "no available candidate nodes" pre-reconciliation.
	views := []v2api.NodeView{
		{CredentialID: 63, RawModel: "grok-4.6", Available: true},
	}

	applyURSMOverlay(candidates, views, time.Now())

	if !candidates[0].Routable {
		t.Fatalf("applyURSMOverlay must apply the credential gate's admission truth; circuit mirror must not block (lossy mirror, send-time breaker decides)")
	}
	if candidates[0].CircuitState != "open" {
		t.Fatalf("circuit_state = %q, want open — the DB mirror must survive the overlay", candidates[0].CircuitState)
	}
}

func TestResolveCredentialBalanceGateAgreesWithDispatch(t *testing.T) {
	zero, negative, positive := 0.0, -1.0, 1.0
	for _, tc := range []struct {
		name    string
		balance *float64
		blocked bool
	}{
		{"zero", &zero, true}, {"negative", &negative, true}, {"positive", &positive, false}, {"unknown", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prod := provider.Candidate{Routable: true, LifecycleStatus: "active", CircuitState: "closed", AvailabilityState: "ready", QuotaState: "ok", BalanceUSD: tc.balance}
			if prod.IsAvailable() == tc.blocked {
				t.Fatalf("dispatch precondition: %s", prod.UnavailableReason())
			}
			candidates := []resolveCandidate{{CredentialID: 63, ModelName: "grok-4.6", DBEligible: true, Routable: true, RuntimeRoutable: true, dbCircuitState: "closed", BalanceUSD: tc.balance}}
			applyURSMOverlay(candidates, []v2api.NodeView{{CredentialID: 63, RawModel: "grok-4.6", Available: true}}, time.Now())
			if candidates[0].Routable == tc.blocked || candidates[0].RuntimeRoutable == tc.blocked {
				t.Fatalf("resolve differs from dispatch: %+v", candidates[0])
			}
			if tc.blocked && candidates[0].BlockReason != "balance_zero" {
				t.Fatalf("reason=%s", candidates[0].BlockReason)
			}
		})
	}
}

// TestApplyCredentialCircuitGateIsIdempotent guards the safety-net double call
// (applyURSMOverlay applies it, and handleRoutingResolve calls it again for the
// non-overlay branches). A second application must not overwrite a more
// specific block reason or flip any verdict.
func TestApplyCredentialCircuitGateIsIdempotent(t *testing.T) {
	cooling := "2026-10-16T00:39:32Z"
	candidates := []resolveCandidate{
		{
			CredentialID: 63, Routable: true, RuntimeRoutable: true,
			dbCircuitState: "open", dbCoolingUntil: &cooling,
		},
	}
	applyCredentialCircuitGate(candidates)
	after1 := candidates[0]

	applyCredentialCircuitGate(candidates)
	after2 := candidates[0]

	if after2.Routable != after1.Routable || after2.RuntimeRoutable != after1.RuntimeRoutable {
		t.Fatalf("second application flipped routable: %v/%v → %v/%v",
			after1.Routable, after1.RuntimeRoutable, after2.Routable, after2.RuntimeRoutable)
	}
	if after2.BlockReason != after1.BlockReason {
		t.Fatalf("block_reason mutated on second application: %q → %q", after1.BlockReason, after2.BlockReason)
	}
	if after2.CircuitState != "open" {
		t.Fatalf("circuit_state = %q, want open", after2.CircuitState)
	}
}
