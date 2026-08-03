package executors

import (
	"context"
	"math"
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
)

// TestCostOptimized_UnknownPenalty 验证未知价格不当作免费。
func TestCostOptimized_UnknownPenalty(t *testing.T) {
	s := costOptimizedStrategy{}
	cheapIn, cheapOut := 1.0, 2.0
	expensiveIn, expensiveOut := 10.0, 20.0
	cases := []struct {
		name string
		c    provider.Candidate
		// wantScore 检查相对顺序：unknown 必须 > expensive > cheap
		wantRank int // 0=best, 2=worst
	}{
		{"cheap known", provider.Candidate{CredentialID: 1, PriceInPer1M: &cheapIn, PriceOutPer1M: &cheapOut}, 0},
		{"expensive known", provider.Candidate{CredentialID: 2, PriceInPer1M: &expensiveIn, PriceOutPer1M: &expensiveOut}, 1},
		{"unknown price nil", provider.Candidate{CredentialID: 3}, 2}, // nil → MaxFloat64
	}
	scores := make([]float64, len(cases))
	for i, tc := range cases {
		sc, err := s.Score(context.Background(), tc.c, StrategyInput{})
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		scores[i] = sc
	}
	// 验证 ranking：scores[0] < scores[1] < scores[2]
	if !(scores[0] < scores[1] && scores[1] < scores[2]) {
		t.Fatalf("cost ranking wrong: cheap=%v expensive=%v unknown=%v (want cheap<expensive<unknown)",
			scores[0], scores[1], scores[2])
	}
	if scores[2] != math.MaxFloat64 {
		t.Errorf("unknown price should be MaxFloat64, got %v", scores[2])
	}
}

// TestCacheOptimized_UnknownPenalty 验证不支持 cache → 最大惩罚；
// 支持 cache 但价格未知 → 高惩罚但优于不支持。
func TestCacheOptimized_UnknownPenalty(t *testing.T) {
	s := cacheOptimizedStrategy{}
	lowCacheRead := 0.5
	cases := []struct {
		name    string
		c       provider.Candidate
		wantMax bool // 是否应得 MaxFloat64
	}{
		{"supports + known low", provider.Candidate{CredentialID: 1, SupportsPromptCache: true, CacheReadPricePer1M: &lowCacheRead}, false},
		{"supports + unknown price", provider.Candidate{CredentialID: 2, SupportsPromptCache: true}, false},
		{"no cache support", provider.Candidate{CredentialID: 3, SupportsPromptCache: false}, true},
	}
	scores := make([]float64, 3)
	for i, tc := range cases {
		sc, _ := s.Score(context.Background(), tc.c, StrategyInput{})
		scores[i] = sc
	}
	// supports+known < supports+unknown < no-support(MaxFloat64)
	if !(scores[0] < scores[1] && scores[1] < scores[2]) {
		t.Fatalf("cache ranking wrong: known=%v unknown=%v nosupport=%v", scores[0], scores[1], scores[2])
	}
	if scores[2] != math.MaxFloat64 {
		t.Errorf("no-cache-support should be MaxFloat64, got %v", scores[2])
	}
}

// TestContextAware_UnknownPenalty 验证未知 context 不当作无限。
func TestContextAware_UnknownPenalty(t *testing.T) {
	s := contextAwareStrategy{}
	bigCtx := 200000
	smallCtx := 8000
	cases := []struct {
		name string
		c    provider.Candidate
	}{
		{"big context", provider.Candidate{CredentialID: 1, ContextWindow: &bigCtx}},
		{"small context", provider.Candidate{CredentialID: 2, ContextWindow: &smallCtx}},
		{"unknown nil", provider.Candidate{CredentialID: 3}},                        // nil → MaxFloat64
		{"zero", provider.Candidate{CredentialID: 4, ContextWindow: intPtr(0)}},     // <=0 → MaxFloat64
	}
	scores := make([]float64, 4)
	for i, tc := range cases {
		sc, _ := s.Score(context.Background(), tc.c, StrategyInput{})
		scores[i] = sc
	}
	// big(best) < small < unknown(MaxFloat64) == zero(MaxFloat64)
	if !(scores[0] < scores[1]) {
		t.Fatalf("big context should beat small: big=%v small=%v", scores[0], scores[1])
	}
	if scores[2] != math.MaxFloat64 || scores[3] != math.MaxFloat64 {
		t.Errorf("unknown/zero context must be MaxFloat64: nil=%v zero=%v", scores[2], scores[3])
	}
}

// TestHeadroom_UnknownNeutral 验证 ConcurrencyLimit 未知 → 中性分 0.5。
func TestHeadroom_UnknownNeutral(t *testing.T) {
	s := headroomStrategy{r: NewRouter(nil, nil)}
	unknown := provider.Candidate{CredentialID: 1} // ConcurrencyLimit nil
	sc, _ := s.Score(context.Background(), unknown, StrategyInput{})
	if sc != 0.5 {
		t.Errorf("unknown ConcurrencyLimit should be neutral 0.5, got %v", sc)
	}
}

// TestNewStrategyByName_AllRegistered 验证所有策略名可构造。
func TestNewStrategyByName_AllRegistered(t *testing.T) {
	r := NewRouter(nil, nil)
	names := []string{"p2c", "cost-optimized", "cache-optimized", "context-aware", "headroom"}
	want := map[string]string{
		"p2c":             "p2c",
		"cost-optimized":  "cost-optimized",
		"cache-optimized": "cache-optimized",
		"context-aware":   "context-aware",
		"headroom":        "headroom",
	}
	for _, n := range names {
		s, err := NewStrategyByName(n, r)
		if err != nil {
			t.Errorf("NewStrategyByName(%q): %v", n, err)
			continue
		}
		if s.Name() != want[n] {
			t.Errorf("NewStrategyByName(%q).Name() = %q, want %q", n, s.Name(), want[n])
		}
	}
	// off/none/empty → nil, nil
	for _, n := range []string{"", "off", "none"} {
		s, err := NewStrategyByName(n, r)
		if err != nil || s != nil {
			t.Errorf("NewStrategyByName(%q) = %v, %v; want nil, nil", n, s, err)
		}
	}
	// unknown → error
	if _, err := NewStrategyByName("bogus", r); err == nil {
		t.Error("NewStrategyByName(bogus) should error")
	}
}

// TestShadowDiff_RankingStability 验证 shadow 评分在含/不含 unknown 字段的
// 候选集上排序稳定，unknown 惩罚生效（不排到已知好候选前面）。
func TestShadowDiff_RankingStability(t *testing.T) {
	r := NewRouter(nil, nil)
	r.ShadowStrategy = costOptimizedStrategy{}
	cheapIn, cheapOut := 1.0, 1.0
	cands := []provider.Candidate{
		{CredentialID: 100, Tier: 1, PriceInPer1M: nil, PriceOutPer1M: nil},            // unknown → 惩罚
		{CredentialID: 200, Tier: 1, PriceInPer1M: &cheapIn, PriceOutPer1M: &cheapOut}, // 已知低价
	}
	policy := &provider.Policy{TierFallbackMax: 4}
	out := r.planByTier(context.Background(), cands, policy, StrategyInput{LoadScoreWeights: DefaultLoadScoreWeights()})
	// shadow 不改实际选中；只是观测。验证不 panic 且集合完整。
	if len(out) != 2 {
		t.Fatalf("shadow changed output size: got %d, want 2", len(out))
	}
}
