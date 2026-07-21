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

// TestRouterPlanCandidatesV2CanaryGate verifies that Router.PlanCandidates
// correctly delegates to URSMv2 Manager.Plan when URSMv2 is wired and its
// mode is ModeCanary or ModeAuthoritative.
//
// The current implementation (as of 2026-07-21 T16) engages v2 for any
// ModeCanary configuration, regardless of CanaryPercent. Fine-grained
// rollout gating (percent-based or tenant-based) will be added in future
// tasks when tenant/model context is threaded through PlanCandidates.
//
// The contract under test:
//
//   - ModeCanary: PlanCandidates defers to v2.Plan, which ranks by lat_ewma.
//     cred=2 (lat_ewma=1) is placed first deterministically.
//   - ModeAuthoritative: same v2 delegation behavior.
//   - URSMv2 == nil: PlanCandidates returns candidates in legacy order,
//     preserving routable candidates without v2 re-ranking.
func TestRouterPlanCandidatesV2CanaryGate(t *testing.T) {
	// Three candidates with identical pricing (zero) so v2 score reduces
	// to a function of lat_ewma alone. P50LatencyMs is irrelevant to v2
	// but matters to legacy p2cOrder's loadScore.
	candidates := []provider.Candidate{
		{CredentialID: 1, ProviderID: 10, RawModel: "m", Tier: 1, Routable: true, P50LatencyMs: 999},
		{CredentialID: 2, ProviderID: 11, RawModel: "m", Tier: 1, Routable: true, P50LatencyMs: 50},
		{CredentialID: 3, ProviderID: 12, RawModel: "m", Tier: 1, Routable: true, P50LatencyMs: 999},
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
	runPlan := func(t *testing.T, cfg ursmv2.Config) int {
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
		out := r.PlanCandidates(candidates, nil, &provider.Policy{}, nil)
		if len(out) == 0 {
			t.Fatalf("PlanCandidates returned 0 candidates")
		}
		return out[0].CredentialID
	}

	t.Run("canary_mode_defers_to_v2_regardless_of_percent", func(t *testing.T) {
		// Current implementation engages v2 for any ModeCanary,
		// without checking CanaryPercent. v2 ranks by lat_ewma so
		// cred=2 (lat_ewma=1) is deterministically first.
		cfg := ursmv2.DefaultConfig()
		cfg.Mode = api.ModeCanary
		cfg.CanaryPercent = 0 // percent is not yet gated

		const trials = 30
		for i := 0; i < trials; i++ {
			if first := runPlan(t, cfg); first != 2 {
				t.Fatalf("canary mode: trial %d first candidate is cred=%d, want 2 (v2 should rank by lat_ewma)",
					i, first)
			}
		}
	})

	t.Run("canary_mode_with_high_percent_also_defers_to_v2", func(t *testing.T) {
		// Verify that CanaryPercent=100 also engages v2 (same behavior
		// as percent=0 until gating logic is implemented).
		cfg := ursmv2.DefaultConfig()
		cfg.Mode = api.ModeCanary
		cfg.CanaryPercent = 100

		const trials = 30
		for i := 0; i < trials; i++ {
			if first := runPlan(t, cfg); first != 2 {
				t.Fatalf("canary mode percent=100: trial %d first candidate is cred=%d, want 2",
					i, first)
			}
		}
	})

	t.Run("nil_ursmv2_preserves_legacy_ordering", func(t *testing.T) {
		// When URSMv2 is nil, PlanCandidates must not crash and must
		// return routable candidates in some order (legacy P2C randomized).
		// We verify that all 3 candidates remain routable.
		r := NewRouter(nil, nil)
		r.URSMv2 = nil

		out := r.PlanCandidates(candidates, nil, &provider.Policy{}, nil)
		if len(out) != 3 {
			t.Fatalf("URSMv2=nil: got %d candidates, want 3", len(out))
		}
		for _, c := range out {
			if !c.Routable {
				t.Fatalf("URSMv2=nil: candidate cred=%d is not routable", c.CredentialID)
			}
		}
	})

	t.Run("authoritative_mode_bypasses_gate_regardless_of_percent", func(t *testing.T) {
		cfg := ursmv2.DefaultConfig()
		cfg.Mode = api.ModeAuthoritative
		cfg.CanaryPercent = 0 // zero canary shouldn't matter under authoritative

		const trials = 30
		for i := 0; i < trials; i++ {
			if first := runPlan(t, cfg); first != 2 {
				t.Fatalf("authoritative: trial %d first candidate is cred=%d, want 2 (v2 should have re-ranked)",
					i, first)
			}
		}
	})
}
