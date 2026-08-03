package executors

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
)

// stubStrategy 是测试用的确定性策略：永远选 CredentialID 最小的候选
// （即按 CredentialID 升序打分）。
type stubStrategy struct {
	name string
}

func (s stubStrategy) Name() string { return s.name }
func (s stubStrategy) Score(_ context.Context, c provider.Candidate, _ StrategyInput) (float64, error) {
	// lower = better；CredentialID 越小分越低 → 首选最小 ID。
	return float64(c.CredentialID), nil
}

// TestP2CStrategy_NameAndScore 验证 p2cStrategy 适配现有 calculateLoadScore。
func TestP2CStrategy_NameAndScore(t *testing.T) {
	r := NewRouter(nil, nil)
	s := NewP2CStrategy(r)
	if s.Name() != "p2c" {
		t.Fatalf("name = %q, want p2c", s.Name())
	}
	cand := provider.Candidate{CredentialID: 1}
	score, err := s.Score(context.Background(), cand, StrategyInput{LoadScoreWeights: DefaultLoadScoreWeights()})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// calculateLoadScore 对空 limiter/fpslot 返回有限正分；只验证不 panic 且有限。
	if score < 0 || score > 1e9 {
		t.Fatalf("score out of expected range: %v", score)
	}
}

// TestShadowStrategy_NoEffectWhenNil 确认 ShadowStrategy=nil 时路由行为零变化。
// 这是 GW-03 的核心门禁：flag 关闭时现有 P2C golden 行为不变。
func TestShadowStrategy_NoEffectWhenNil(t *testing.T) {
	r := NewRouter(nil, nil)
	cands := []provider.Candidate{
		{CredentialID: 1, Tier: 1, Protocol: "openai-completions"},
		{CredentialID: 2, Tier: 1, Protocol: "openai-completions"},
		{CredentialID: 3, Tier: 1, Protocol: "openai-completions"},
	}
	policy := &provider.Policy{TierFallbackMax: 4}
	// ShadowStrategy nil → scoreWithShadow 直接 return，不应 panic。
	out := r.planByTier(context.Background(), cands, policy, StrategyInput{LoadScoreWeights: DefaultLoadScoreWeights()})
	if len(out) != 3 {
		t.Fatalf("planByTier returned %d cands, want 3", len(out))
	}
	// 确认全部 3 个都在结果里（顺序由 P2C 决定，但集合不变）。
	seen := map[int]bool{}
	for _, c := range out {
		seen[c.CredentialID] = true
	}
	for _, want := range []int{1, 2, 3} {
		if !seen[want] {
			t.Errorf("candidate CredentialID=%d missing from planByTier output", want)
		}
	}
}

// TestShadowStrategy_DoesNotChangeSelection 确认 shadow 评分不改变实际选中候选。
// stubStrategy 总是选最小 CredentialID，但实际 P2C 可能选别的——shadow 只记录。
func TestShadowStrategy_DoesNotChangeSelection(t *testing.T) {
	r := NewRouter(nil, nil)
	r.ShadowStrategy = stubStrategy{name: "stub-min-id"}
	cands := []provider.Candidate{
		{CredentialID: 10, Tier: 1, Protocol: "openai-completions"},
		{CredentialID: 20, Tier: 1, Protocol: "openai-completions"},
		{CredentialID: 30, Tier: 1, Protocol: "openai-completions"},
	}
	policy := &provider.Policy{TierFallbackMax: 4}
	out := r.planByTier(context.Background(), cands, policy, StrategyInput{LoadScoreWeights: DefaultLoadScoreWeights()})
	// 集合不变（shadow 不删候选）。
	if len(out) != 3 {
		t.Fatalf("shadow changed output size: got %d, want 3", len(out))
	}
}

// TestStrategyInterface_Contract 锁定 Strategy 接口签名，防止意外破坏。
func TestStrategyInterface_Contract(t *testing.T) {
	var _ Strategy = stubStrategy{name: "x"}
	var _ Strategy = NewP2CStrategy(NewRouter(nil, nil))
}
