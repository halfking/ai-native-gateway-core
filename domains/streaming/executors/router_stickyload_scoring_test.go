package executors

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/provider"
)

func newScoringTracker(t *testing.T) *StickyLoadTracker {
	t.Helper()
	tr := NewStickyLoadTracker()
	t.Cleanup(tr.Close)
	return tr
}

// TestStickySessionPenaltyCapacityNormalized: sticky 会话数按并发容量
// 归一——2 会话/容量 4 = 0.5；超容量夹紧到 1.0；无会话不惩罚。
func TestStickySessionPenaltyCapacityNormalized(t *testing.T) {
	tr := newScoringTracker(t)
	r := &Router{StickyLoad: tr}

	limit := 4
	c := provider.Candidate{CredentialID: 1, ConcurrencyLimit: &limit}

	if p := stickySessionPenalty(c, r, nil); p != 0 {
		t.Fatalf("no sessions → 0, got %v", p)
	}

	tr.ObserveSession(1, "s1")
	tr.ObserveSession(1, "s2")
	if p := stickySessionPenalty(c, r, nil); p != 0.5 {
		t.Fatalf("2/4 sessions → 0.5, got %v", p)
	}

	tr.ObserveSession(1, "s3")
	tr.ObserveSession(1, "s4")
	tr.ObserveSession(1, "s5")
	if p := stickySessionPenalty(c, r, nil); p != 1.0 {
		t.Fatalf("5/4 sessions → clamped 1.0, got %v", p)
	}
}

// TestStickySessionPenaltyCapacityDefaultFallback: 容量未知（无
// ConcurrencyLimit、无 Limiter/LiveLoad）时用保守默认容量，不视为无限。
func TestStickySessionPenaltyCapacityDefaultFallback(t *testing.T) {
	tr := newScoringTracker(t)
	r := &Router{StickyLoad: tr}
	c := provider.Candidate{CredentialID: 2}

	tr.ObserveSession(2, "s1")
	p := stickySessionPenalty(c, r, nil)
	if p <= 0 || p >= 1 {
		t.Fatalf("expected a bounded default-capacity ratio in (0,1), got %v", p)
	}
}

// TestRecentRequestPenaltyDecay: 最近请求时间惩罚——无信号 0；刚活跃 ~1；
// 半个地平线 ~0.5；超地平线 0。
func TestRecentRequestPenaltyDecay(t *testing.T) {
	tr := newScoringTracker(t)
	r := &Router{StickyLoad: tr}
	c := provider.Candidate{CredentialID: 9}

	if p := recentRequestPenalty(c, r, nil); p != 0 {
		t.Fatalf("no activity → 0, got %v", p)
	}

	tr.ObserveActivity(9)
	if p := recentRequestPenalty(c, r, nil); p < 0.99 {
		t.Fatalf("fresh activity → ~1, got %v", p)
	}

	tr.mu.Lock()
	tr.activity[9] = time.Now().Add(-15 * time.Second).UnixMilli()
	tr.mu.Unlock()
	if p := recentRequestPenalty(c, r, nil); p < 0.45 || p > 0.55 {
		t.Fatalf("15s of 30s horizon → ~0.5, got %v", p)
	}

	tr.mu.Lock()
	tr.activity[9] = time.Now().Add(-60 * time.Second).UnixMilli()
	tr.mu.Unlock()
	if p := recentRequestPenalty(c, r, nil); p != 0 {
		t.Fatalf("beyond horizon → 0, got %v", p)
	}
}

// TestBalancePenaltyMatrix: 余额惩罚——nil 余额 fail-open 0；订阅/免费
// （round 1）不适用；按量低余额线性加重；0 余额=1。
func TestBalancePenaltyMatrix(t *testing.T) {
	bal := func(v float64) *float64 { return &v }

	if p := balancePenaltyForCandidate(provider.Candidate{BillingMode: "per_token"}); p != 0 {
		t.Fatalf("nil balance → 0, got %v", p)
	}
	if p := balancePenaltyForCandidate(provider.Candidate{BillingMode: "free", BalanceUSD: bal(0.1)}); p != 0 {
		t.Fatalf("billing round 1 → 0, got %v", p)
	}
	if p := balancePenaltyForCandidate(provider.Candidate{BillingMode: "token_plan", BalanceUSD: bal(0.1)}); p != 0 {
		t.Fatalf("token_plan → 0, got %v", p)
	}
	if p := balancePenaltyForCandidate(provider.Candidate{BillingMode: "per_token", BalanceUSD: bal(10)}); p != 0 {
		t.Fatalf("balance ≥ watermark → 0, got %v", p)
	}
	if p := balancePenaltyForCandidate(provider.Candidate{BillingMode: "per_token", BalanceUSD: bal(0)}); p != 1.0 {
		t.Fatalf("zero balance → 1.0, got %v", p)
	}
	p := balancePenaltyForCandidate(provider.Candidate{BillingMode: "per_token", BalanceUSD: bal(1)})
	if p < 0.75 || p > 0.85 {
		t.Fatalf("1/5 watermark → ~0.8, got %v", p)
	}
}

// TestLoadScoreStickyLoadWiredZeroObservationIdentical: 接线了 tracker 但
// 零观察（无会话、无活跃）时，sticky/recency 惩罚为 0；BalanceUSD 未设
// 时 balance 惩罚也为 0——composite 与未接线完全一致。
func TestLoadScoreStickyLoadWiredZeroObservationIdentical(t *testing.T) {
	tr := newScoringTracker(t)

	c := provider.Candidate{CredentialID: 1, SuccessRate: 1.0}
	rNil := &Router{LoadScoreWeights: DefaultLoadScoreWeights()}
	rWire := &Router{LoadScoreWeights: DefaultLoadScoreWeights(), StickyLoad: tr}

	base := calculateLoadScore(c, rNil, context.Background(), rNil.LoadScoreWeights, StrategyInput{})
	wired := calculateLoadScore(c, rWire, context.Background(), rWire.LoadScoreWeights, StrategyInput{})
	if base != wired {
		t.Fatalf("zero-observation tracker must not change composite: base=%v wired=%v", base, wired)
	}
}

// TestLoadScoreStickyLoadPenalizesLoadedNode: 会话数多的节点 composite
// 更高（P2C 中更不易胜出）。
func TestLoadScoreStickyLoadPenalizesLoadedNode(t *testing.T) {
	tr := newScoringTracker(t)
	r := &Router{LoadScoreWeights: DefaultLoadScoreWeights(), StickyLoad: tr}

	limit := 4
	idle := provider.Candidate{CredentialID: 1, SuccessRate: 1.0, ConcurrencyLimit: &limit}
	loaded := provider.Candidate{CredentialID: 2, SuccessRate: 1.0, ConcurrencyLimit: &limit}
	tr.ObserveSession(2, "s1")
	tr.ObserveSession(2, "s2")
	tr.ObserveSession(2, "s3")
	tr.ObserveSession(2, "s4")

	scoreIdle := calculateLoadScore(idle, r, context.Background(), r.LoadScoreWeights, StrategyInput{})
	scoreLoaded := calculateLoadScore(loaded, r, context.Background(), r.LoadScoreWeights, StrategyInput{})
	if scoreLoaded <= scoreIdle {
		t.Fatalf("loaded node must score higher (worse): idle=%v loaded=%v", scoreIdle, scoreLoaded)
	}
}

// TestFirstHopLotteryAvoidsStickyLoadedNode: 端到端证据——首跳由
// promoteWeightedCandidate 加权轮询决定（评分只影响 failover 顺序），
// 折算份额后满载节点的首跳占比必须显著低于 1/3 基线。
func TestFirstHopLotteryAvoidsStickyLoadedNode(t *testing.T) {
	tr := newScoringTracker(t)
	r := &Router{LoadScoreWeights: DefaultLoadScoreWeights(), StickyLoad: tr}

	limit := 4
	mk := func(id int) provider.Candidate {
		return provider.Candidate{
			CredentialID: id, ProviderID: 1, RawModel: "m", Tier: 1,
			SuccessRate: 1.0, ConcurrencyLimit: &limit, Weight: 100,
		}
	}
	cands := []provider.Candidate{mk(11), mk(12), mk(13)}
	// 节点 13 满载：4 会话/容量 4 → sticky 惩罚 1.0，且刚活跃 → recency ~1。
	for i := 0; i < 4; i++ {
		tr.ObserveSession(13, fmt.Sprintf("s%d", i))
	}

	policy := &provider.Policy{}
	const rounds = 300
	loadedFirst := 0
	for i := 0; i < rounds; i++ {
		ordered := r.planByTier(context.Background(),
			append([]provider.Candidate(nil), cands...), policy, StrategyInput{})
		if len(ordered) == 0 {
			t.Fatal("planByTier returned no candidates")
		}
		if ordered[0].CredentialID == 13 {
			loadedFirst++
		}
	}
	share := float64(loadedFirst) / float64(rounds)
	if share > 0.26 {
		t.Fatalf("loaded node first-hop share %.3f (%d/%d) must sit well below the 1/3 baseline", share, loadedFirst, rounds)
	}

	// 对照组：未接线 tracker 时三节点等权，首跳应回到 ~1/3。
	plain := &Router{LoadScoreWeights: DefaultLoadScoreWeights()}
	loadedFirst = 0
	for i := 0; i < rounds; i++ {
		ordered := plain.planByTier(context.Background(),
			append([]provider.Candidate(nil), cands...), policy, StrategyInput{})
		if ordered[0].CredentialID == 13 {
			loadedFirst++
		}
	}
	share = float64(loadedFirst) / float64(rounds)
	if share < 0.25 || share > 0.42 {
		t.Fatalf("unwired control should stay near 1/3, got %.3f", share)
	}
}
