package executors

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	ursmv2 "github.com/kaixuan/llm-gateway-go/domains/ursm/v2"
	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// TestRouterPlanCandidatesV2CanaryGate exercises the rollout-controller gate
// that was added in the T16 plumbing follow-up. Before the gate, both the
// authoritative filter block and the canary ordering block fired whenever
// URSMv2.Mode() matched, which means a freshly-deployed canary at percent=0
// would still re-rank every request through the v2 path.
//
// The contract under test:
//
//   - ModeCanary + CanaryPercent=0 + no tenant/model whitelist:
//     PlanCandidates must return the LEGACY ordered slice (no v2 re-rank).
//   - ModeCanary + CanaryPercent=100 (or matching tenant whitelist):
//     PlanCandidates must defer to v2.Plan — cred=2 (lowest lat_ewma) is
//     placed first, which is unreachable from legacy p2cOrder under our
//     seed because legacy uses P50LatencyMs not lat_ewma.
//   - ModeAuthoritative: the gate is bypassed (Authoritative always engages),
//     so v2 ordering is observable regardless of CanaryPercent.
//
// To make the gate-engaged vs gate-bypassed comparison deterministic
// independent of test-suite ordering noise (rrCounter state, randomized
// P2C draws, etc.) we use the SAME input across many trials. With the
// gate engaged, the ordering is fully deterministic (v2 is stable sort
// keyed on lat_ewma). With the gate bypassed, the legacy P2C is randomized
// so over 30 trials cred=2 will not be first 100% of the time. We assert
// the distribution: gate-engaged → cred=2 first in 100% of trials;
// gate-bypassed → cred=2 first in <100% of trials (with overwhelming
// probability given 3 candidates, this is <33%).
func TestRouterPlanCandidatesV2CanaryGate(t *testing.T) {
	// Three candidates with identical pricing (zero) so v2 score reduces
	// to a function of lat_ewma alone. P50LatencyMs is irrelevant to v2
	// but matters to legacy p2cOrder's loadScore.
	candidates := []provider.Candidate{
		{CredentialID: 1, ProviderID: 10, RawModel: "m", Tier: 1, Routable: true, P50LatencyMs: 999},
		{CredentialID: 2, ProviderID: 11, RawModel: "m", Tier: 1, Routable: true, P50LatencyMs: 50},
		{CredentialID: 3, ProviderID: 12, RawModel: "m", Tier: 1, Routable: true, P50LatencyMs: 999},
	}
	planCtx := PlanContext{
		TenantID:       "tenant-a",
		CanonicalModel: "m",
		RequestID:      "req-1",
	}

	// seedV2 writes the v2 store hash entries: cred=2 has the lowest
	// lat_ewma=1 so v2 ranks it first. cred=1 and cred=3 tie at 999.
	seedV2 := func(t *testing.T, rdb *redis.Client) {
		t.Helper()
		ctx := context.Background()
		if err := rdb.HSet(ctx,
			"ursm:v2:node:1:m", "available", "1", "lat_ewma", "999",
		).Err(); err != nil {
			t.Fatalf("seed cred=1: %v", err)
		}
		if err := rdb.HSet(ctx,
			"ursm:v2:node:2:m", "available", "1", "lat_ewma", "1",
		).Err(); err != nil {
			t.Fatalf("seed cred=2: %v", err)
		}
		if err := rdb.HSet(ctx,
			"ursm:v2:node:3:m", "available", "1", "lat_ewma", "999",
		).Err(); err != nil {
			t.Fatalf("seed cred=3: %v", err)
		}
	}

	// runPlan spins up a fresh router+manager and returns the first
	// candidate's CredentialID. The router's rrCounter is per-instance so
	// this is safe across trials.
	runPlan := func(t *testing.T, cfg ursmv2.Config, ctx PlanContext) int {
		t.Helper()
		mr := miniredis.RunT(t)
		rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		seedV2(t, rdb)
		mgr := ursmv2.New(ursmv2.Dependencies{Redis: rdb, Config: cfg})
		if err := mgr.SetReady(context.Background(), true); err != nil {
			t.Fatalf("ready: %v", err)
		}
		r := NewRouter(nil, nil)
		r.URSMv2 = mgr
		out := r.PlanCandidates(candidates, ctx, nil, &provider.Policy{}, nil)
		if len(out) == 0 {
			t.Fatalf("PlanCandidates returned 0 candidates")
		}
		return out[0].CredentialID
	}

	t.Run("canary_percent_zero_keeps_legacy_ordering_under_randomized_p2c", func(t *testing.T) {
		// Legacy p2cOrder is randomized via randomPair. With 3 candidates
		// cred=2 will be first in roughly 1/3 of trials. The crucial
		// property: NOT every trial produces cred=2 first (which would
		// only happen if v2 were silently engaging).
		cfg := ursmv2.DefaultConfig()
		cfg.Mode = api.ModeCanary
		cfg.CanaryPercent = 0

		const trials = 30
		var cred2FirstCount int
		for i := 0; i < trials; i++ {
			if runPlan(t, cfg, planCtx) == 2 {
				cred2FirstCount++
			}
		}
		if cred2FirstCount == trials {
			t.Fatalf("canary gate at CanaryPercent=0 is deterministic (cred=2 first in %d/%d trials); "+
				"v2 must not engage for zero-percent canary", trials, trials)
		}
	})

	t.Run("canary_percent_hundred_defers_to_v2", func(t *testing.T) {
		// With CanaryPercent=100 the rollout gate returns true for every
		// tuple, so v2 Manager.Plan is consulted. v2 ranks by ascending
		// Score = price + latency + stability. cred=2 has lat_ewma=1 so
		// it must be first in EVERY trial.
		cfg := ursmv2.DefaultConfig()
		cfg.Mode = api.ModeCanary
		cfg.CanaryPercent = 100

		const trials = 30
		var cred2FirstCount int
		for i := 0; i < trials; i++ {
			if runPlan(t, cfg, planCtx) == 2 {
				cred2FirstCount++
			}
		}
		if cred2FirstCount != trials {
			t.Fatalf("canary gate at CanaryPercent=100 did not engage v2 in %d/%d trials; "+
				"v2 ordering (cred=2 first) must be deterministic under full canary",
				trials-cred2FirstCount, trials)
		}
	})

	t.Run("canary_tenant_whitelist_defers_to_v2_for_listed_tenant_only", func(t *testing.T) {
		cfg := ursmv2.DefaultConfig()
		cfg.Mode = api.ModeCanary
		cfg.CanaryPercent = 0
		cfg.CanaryTenants = []string{"tenant-a"}

		// Whitelisted tenant: gate fires every time → cred=2 first in
		// every trial (v2 is deterministic).
		const trials = 30
		for i := 0; i < trials; i++ {
			if first := runPlan(t, cfg, planCtx); first != 2 {
				t.Fatalf("whitelisted tenant: trial %d first candidate is cred=%d, want 2 (v2 should have re-ranked)",
					i, first)
			}
		}

		// Non-whitelisted tenant: gate bypassed → legacy randomized P2C.
		// cred=2 will NOT be first in every trial.
		otherCtx := PlanContext{
			TenantID:       "tenant-other",
			CanonicalModel: "m",
			RequestID:      "req-1",
		}
		var cred2FirstCount int
		for i := 0; i < trials; i++ {
			if runPlan(t, cfg, otherCtx) == 2 {
				cred2FirstCount++
			}
		}
		if cred2FirstCount == trials {
			t.Fatalf("non-whitelisted tenant: gate leaked to v2 (cred=2 first in %d/%d trials); want legacy randomized ordering",
				trials, trials)
		}
	})

	t.Run("authoritative_mode_bypasses_gate_regardless_of_percent", func(t *testing.T) {
		cfg := ursmv2.DefaultConfig()
		cfg.Mode = api.ModeAuthoritative
		cfg.CanaryPercent = 0 // zero canary shouldn't matter under authoritative

		const trials = 30
		for i := 0; i < trials; i++ {
			if first := runPlan(t, cfg, planCtx); first != 2 {
				t.Fatalf("authoritative: trial %d first candidate is cred=%d, want 2 (v2 should have re-ranked)",
					i, first)
			}
		}
	})
}
