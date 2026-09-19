package executors

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
)

// twoLayerCreds seeds a tier bucket with one standard node and two priority
// nodes (manual_priority>0, quota ok). Each priority node gets an explicit
// ConcurrencyLimit so saturation is measurable through the fakeLiveLoad stub.
func twoLayerCreds() []provider.Candidate {
	lim := 2
	return []provider.Candidate{
		{CredentialID: 1, ProviderID: 1, RawModel: "m", Tier: 2, Weight: 100,
			ManualPriority: 1, Priority: true, QuotaState: "ok", ConcurrencyLimit: &lim},
		{CredentialID: 2, ProviderID: 1, RawModel: "m", Tier: 2, Weight: 100,
			ManualPriority: 2, Priority: true, QuotaState: "ok", ConcurrencyLimit: &lim},
		{CredentialID: 3, ProviderID: 1, RawModel: "m", Tier: 2, Weight: 100},
	}
}

func layerRouter(inflight map[int]int64) *Router {
	r := NewRouter(nil, nil)
	m := make(map[credModelKey]int64, len(inflight))
	for cred, n := range inflight {
		m[credModelKey{CredID: int64(cred), Model: "m"}] = n
	}
	r.LiveLoad = &fakeLiveLoad{concurrent: m}
	return r
}

// TestPriorityNodeSaturated pins the saturation reading used by the two-layer
// partition: capacity known + in-flight >= capacity → saturated; unknown
// capacity fails open; no LiveLoad signal → not saturated.
func TestPriorityNodeSaturated(t *testing.T) {
	cands := twoLayerCreds()
	// cred 1: capacity 2, in-flight 2 → saturated.
	r := layerRouter(map[int]int64{1: 2})
	if !priorityNodeSaturated(cands[0], r) {
		t.Errorf("cred1 (cap 2, used 2) must be saturated")
	}
	// cred 2: capacity 2, in-flight 1 → headroom.
	if priorityNodeSaturated(cands[1], r) {
		t.Errorf("cred2 (cap 2, used 0) must not be saturated")
	}
	// cred 3: no ConcurrencyLimit → fail open, never saturated.
	if priorityNodeSaturated(cands[2], r) {
		t.Errorf("cred3 (capacity unknown) must fail open as not saturated")
	}
	// nil router → not saturated.
	if priorityNodeSaturated(cands[0], nil) {
		t.Errorf("nil router must fail open as not saturated")
	}
}

// TestPartitionBySelectionLayer_TwoLayerSemantics pins the user contract:
// 优先节点之间平衡；只有优先节点全部满掉，才使用非优先节点；已满的优先
// 节点保留为最后的 failover 兜底（排在所有健康标准节点之后）。
func TestPartitionBySelectionLayer_TwoLayerSemantics(t *testing.T) {
	cands := twoLayerCreds()

	// Nothing saturated: both priority nodes form L1 (lottery segment), the
	// standard node trails.
	r := layerRouter(nil)
	got, lotto := partitionBySelectionLayer(cands, r)
	if got[2].CredentialID != 3 {
		t.Errorf("standard node must trail healthy priority nodes, got order %v",
			[]int{got[0].CredentialID, got[1].CredentialID, got[2].CredentialID})
	}
	if lotto != 2 {
		t.Errorf("lottery segment = %d, want 2 (the priority layer)", lotto)
	}

	// cred 1 saturated: L1 keeps only cred 2; cred 1 drops behind the
	// standard node into the last-resort segment.
	r = layerRouter(map[int]int64{1: 2})
	got, lotto = partitionBySelectionLayer(cands, r)
	if got[0].CredentialID != 2 {
		t.Errorf("the unsaturated priority node must lead, got cred %d", got[0].CredentialID)
	}
	if got[1].CredentialID != 3 || got[2].CredentialID != 1 {
		t.Errorf("saturated priority node must tail the standard node, got order %v",
			[]int{got[0].CredentialID, got[1].CredentialID, got[2].CredentialID})
	}
	if lotto != 1 {
		t.Errorf("lottery segment = %d, want 1 (remaining priority layer)", lotto)
	}

	// All priority nodes saturated: L1 empty, the standard node leads and the
	// lottery stays inside the standard segment.
	r = layerRouter(map[int]int64{1: 2, 2: 2})
	got, lotto = partitionBySelectionLayer(cands, r)
	if got[0].CredentialID != 3 {
		t.Errorf("standard node must lead when every priority node is full, got cred %d", got[0].CredentialID)
	}
	if lotto != 1 {
		t.Errorf("lottery segment = %d, want 1 (the standard segment)", lotto)
	}

	// Priority flag without ok quota_state never enters L1 regardless of
	// saturation (mirrors the SQL bucket predicate).
	exhausted := cands[0]
	exhausted.QuotaState = "periodic_exhausted"
	r = layerRouter(nil)
	// Neither candidate is priority-layer eligible: the lottery segment
	// covers both (no L1 prefix) and the partition only preserves the input
	// (P2C) order — positional ordering within L2 is P2C's job.
	got, lotto = partitionBySelectionLayer([]provider.Candidate{exhausted, cands[2]}, r)
	if lotto != len(got) {
		t.Errorf("periodic_exhausted priority node must not form a priority layer, got lotto=%d len=%d", lotto, len(got))
	}
}

// TestPlanByTier_SaturatedPriorityFallsBehindStandard runs the full tier
// planning path: with both priority nodes saturated, the ordered output must
// start with the standard candidate (the two-layer spill rule), while the
// saturated priority nodes remain in the list as failover targets.
func TestPlanByTier_SaturatedPriorityFallsBehindStandard(t *testing.T) {
	r := layerRouter(map[int]int64{1: 2, 2: 2})
	policy := &provider.Policy{TierFallbackMax: 4}
	ordered := r.planByTier(context.Background(), twoLayerCreds(), policy, StrategyInput{})
	if len(ordered) != 3 {
		t.Fatalf("ordered = %d candidates, want 3", len(ordered))
	}
	if ordered[0].CredentialID != 3 {
		t.Errorf("first attempt must be the standard node when priority layer is full, got cred %d", ordered[0].CredentialID)
	}
	// Failover ladder must still contain both priority nodes.
	seen := map[int]bool{}
	for _, c := range ordered {
		seen[c.CredentialID] = true
	}
	if !seen[1] || !seen[2] {
		t.Errorf("saturated priority nodes must remain failover targets, order %v", ordered)
	}
}

// TestCalculateCostPenalty_BillingAware pins the 2026-09-19 marginal-cost
// model: Round-1 plans cost 0 regardless of price fields (the plan fee is
// sunk); Round-2 PAYG prices linearly with the blended unit price and unknown
// stays max penalty.
func TestCalculateCostPenalty_BillingAware(t *testing.T) {
	t.Setenv("LLM_GATEWAY_ROUTING_COST_CAP", "30")
	cheapIn, cheapOut := 1.0, 2.0
	expIn, expOut := 10.0, 20.0

	cases := []struct {
		name string
		c    provider.Candidate
		want float64
	}{
		{"token_plan without unit prices is FREE marginal cost", provider.Candidate{BillingMode: "token_plan"}, 0},
		{"code_plan with stale prices still 0", provider.Candidate{BillingMode: "code_plan", PriceInPer1M: &expIn, PriceOutPer1M: &expOut}, 0},
		{"monthly 0", provider.Candidate{BillingMode: "monthly"}, 0},
		{"free 0", provider.Candidate{BillingMode: "free"}, 0},
		{"PAYG unknown price = max penalty", provider.Candidate{BillingMode: "per_token"}, 1.0},
		{"empty billing mode treated as PAYG unknown", provider.Candidate{}, 1.0},
		{"PAYG cheap blended 3 → 0.1", provider.Candidate{BillingMode: "per_token", PriceInPer1M: &cheapIn, PriceOutPer1M: &cheapOut}, 0.1},
		{"PAYG at cap → 1.0", provider.Candidate{BillingMode: "per_token", PriceInPer1M: &expIn, PriceOutPer1M: &expOut}, 1.0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := calculateCostPenalty(tc.c)
			if got < tc.want-1e-12 || got > tc.want+1e-12 {
				t.Errorf("calculateCostPenalty = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestCalculateLoadScore_PlanBillingNeverPunishedByPrice pins the composite
// integration: a token_plan credential without prices must not be pushed below
// an identical per_token credential by the cost dimension.
func TestCalculateLoadScore_PlanBillingNeverPunishedByPrice(t *testing.T) {
	r := NewRouter(nil, nil)
	ctx := context.Background()
	plan := provider.Candidate{CredentialID: 1, BillingMode: "token_plan"}
	payg := provider.Candidate{CredentialID: 2, BillingMode: "per_token"}
	if calculateLoadScore(plan, r, ctx, r.LoadScoreWeights) >= calculateLoadScore(payg, r, ctx, r.LoadScoreWeights) {
		t.Errorf("plan credential (marginal cost 0) must outrank unpriced PAYG (max cost penalty)")
	}
}

// TestPlanQuotaPenaltyForCandidate pins the plan-window utilization penalty:
// only Round-1 plan billings with probe data participate; free/PAYG/unknown
// stay neutral; values clamp to 0..1.
func TestPlanQuotaPenaltyForCandidate(t *testing.T) {
	used80, used120, used0 := 80.0, 120.0, 0.0
	cases := []struct {
		name string
		c    provider.Candidate
		want float64
	}{
		{"token_plan 80% used", provider.Candidate{BillingMode: "token_plan", PlanQuotaUsedPercent: &used80}, 0.8},
		{"code_plan 0% used", provider.Candidate{BillingMode: "code_plan", PlanQuotaUsedPercent: &used0}, 0},
		{"clamped >100%", provider.Candidate{BillingMode: "token_plan", PlanQuotaUsedPercent: &used120}, 1.0},
		{"no probe data → neutral", provider.Candidate{BillingMode: "token_plan"}, 0},
		{"free pool excluded", provider.Candidate{BillingMode: "free", PlanQuotaUsedPercent: &used80}, 0},
		{"PAYG excluded", provider.Candidate{BillingMode: "per_token", PlanQuotaUsedPercent: &used80}, 0},
		{"empty billing excluded", provider.Candidate{PlanQuotaUsedPercent: &used80}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := planQuotaPenaltyForCandidate(tc.c)
			if got < tc.want-1e-12 || got > tc.want+1e-12 {
				t.Errorf("planQuotaPenaltyForCandidate = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestCalculateLoadScore_PlanQuotaPrefersHeadroom pins the composite
// integration: between two token_plan candidates identical except window
// utilization, the one with more remaining quota scores lower (preferred) —
// spread plan usage so windows reset empty as rarely as possible.
func TestCalculateLoadScore_PlanQuotaPrefersHeadroom(t *testing.T) {
	r := NewRouter(nil, nil)
	ctx := context.Background()
	used90, used10 := 90.0, 10.0
	burned := provider.Candidate{CredentialID: 1, BillingMode: "token_plan", PlanQuotaUsedPercent: &used90}
	fresh := provider.Candidate{CredentialID: 2, BillingMode: "token_plan", PlanQuotaUsedPercent: &used10}
	if calculateLoadScore(fresh, r, ctx, r.LoadScoreWeights) >= calculateLoadScore(burned, r, ctx, r.LoadScoreWeights) {
		t.Errorf("fresh plan window (10%% used) must outrank burned window (90%% used)")
	}
}

// TestFirstHopLotteryWeights_PlanQuotaWithoutStickyLoad pins the 2026-09-19
// extension: the lottery fold no longer requires StickyLoad — balance and
// plan-quota penalties are candidate-field-only and adjust first-hop shares
// on deployments without the sticky tracker.
func TestFirstHopLotteryWeights_PlanQuotaWithoutStickyLoad(t *testing.T) {
	r := NewRouter(nil, nil) // StickyLoad deliberately nil
	used90, used0 := 90.0, 0.0
	cands := []provider.Candidate{
		{CredentialID: 1, ProviderID: 1, RawModel: "m", Weight: 100, BillingMode: "token_plan", PlanQuotaUsedPercent: &used90},
		{CredentialID: 2, ProviderID: 1, RawModel: "m", Weight: 100, BillingMode: "token_plan", PlanQuotaUsedPercent: &used0},
	}
	got := firstHopLotteryWeights(cands, r)
	if got == nil {
		t.Fatalf("plan-quota fold must apply without StickyLoad")
	}
	if got[0] >= got[1] {
		t.Errorf("burned window share (%d) must shrink below fresh window share (%d)", got[0], got[1])
	}
	if got[1] != 100 {
		t.Errorf("fresh window (zero penalty) must keep full weight, got %d", got[1])
	}
	// All dimensions off → nil (pure Weight path).
	t.Setenv("LLM_GATEWAY_ROUTING_W_STICKY", "0")
	t.Setenv("LLM_GATEWAY_ROUTING_W_RECENCY", "0")
	t.Setenv("LLM_GATEWAY_ROUTING_W_BALANCE", "0")
	t.Setenv("LLM_GATEWAY_ROUTING_W_PLANQUOTA", "0")
	if got := firstHopLotteryWeights(cands, r); got != nil {
		t.Errorf("all penalty weights off must return nil, got %v", got)
	}
}

// TestFirstHopLotteryWeights_QuotaFoldMath pins the exact share formula:
// p = planQuotaPenalty (only active dimension), w' = Weight·(1−0.9·p).
func TestFirstHopLotteryWeights_QuotaFoldMath(t *testing.T) {
	t.Setenv("LLM_GATEWAY_ROUTING_W_STICKY", "0")
	t.Setenv("LLM_GATEWAY_ROUTING_W_RECENCY", "0")
	t.Setenv("LLM_GATEWAY_ROUTING_W_BALANCE", "0")
	r := NewRouter(nil, nil)
	used50 := 50.0
	cands := []provider.Candidate{
		{CredentialID: 1, ProviderID: 1, RawModel: "m", Weight: 100, BillingMode: "token_plan", PlanQuotaUsedPercent: &used50},
	}
	got := firstHopLotteryWeights(cands, r)
	if got == nil || got[0] != 55 {
		t.Errorf("weight 100 with p=0.5 must fold to 55, got %v", got)
	}
}
