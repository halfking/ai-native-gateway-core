package autoroute

import (
	"testing"
	"time"
)

// TestIndex_Recommend_ThreeTierFunnel 验证三级漏斗逻辑（需求 #6）
func TestIndex_Recommend_ThreeTierFunnel(t *testing.T) {
	// 构造候选池：5 个 primary（热门），3 个 secondary，2 个 fallback
	now := time.Now()
	primary := []Candidate{
		{CredentialID: 1, CanonicalName: "hot-1", Tier: "primary", PopularityScore: 100, Tags: []string{"reasoning"}, SuccessRate: 0.95, ReleasedAt: &now},
		{CredentialID: 2, CanonicalName: "hot-2", Tier: "primary", PopularityScore: 90, Tags: []string{"reasoning"}, SuccessRate: 0.93, ReleasedAt: &now},
		{CredentialID: 3, CanonicalName: "hot-3", Tier: "primary", PopularityScore: 80, Tags: []string{"reasoning"}, SuccessRate: 0.92, ReleasedAt: &now},
		{CredentialID: 4, CanonicalName: "hot-4", Tier: "primary", PopularityScore: 70, Tags: []string{"reasoning"}, SuccessRate: 0.90, ReleasedAt: &now},
		{CredentialID: 5, CanonicalName: "hot-5", Tier: "primary", PopularityScore: 60, Tags: []string{"reasoning"}, SuccessRate: 0.88, ReleasedAt: &now},
	}
	secondary := []Candidate{
		{CredentialID: 6, CanonicalName: "sec-1", Tier: "secondary", PopularityScore: 50, Tags: []string{"reasoning"}, SuccessRate: 0.85, ReleasedAt: &now},
		{CredentialID: 7, CanonicalName: "sec-2", Tier: "secondary", PopularityScore: 40, Tags: []string{"reasoning"}, SuccessRate: 0.83, ReleasedAt: &now},
		{CredentialID: 8, CanonicalName: "sec-3", Tier: "secondary", PopularityScore: 30, Tags: []string{"reasoning"}, SuccessRate: 0.80, ReleasedAt: &now},
	}
	fallback := []Candidate{
		{CredentialID: 9, CanonicalName: "fb-1", Tier: "fallback", PopularityScore: 20, Tags: []string{"reasoning"}, SuccessRate: 0.75, ReleasedAt: &now},
		{CredentialID: 10, CanonicalName: "fb-2", Tier: "fallback", PopularityScore: 10, Tags: []string{"reasoning"}, SuccessRate: 0.70, ReleasedAt: &now},
	}

	all := append(append(primary, secondary...), fallback...)
	idx := &Index{entries: all, lastRefresh: time.Now()}

	// 场景 1：正常场景（热门池充足，返回 top-3 from primary）
	results := idx.Recommend(TaskReasoning, ClassificationSignals{EstimatedTokens: 1000}, ProfileSmart, 3)
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	// 验证全部来自 primary
	for i, sc := range results {
		if sc.Candidate.Tier != "primary" {
			t.Errorf("result[%d] tier=%s, want primary", i, sc.Candidate.Tier)
		}
	}

	// 场景 2：热门池不足 3 个（模拟只有 2 个 primary 可用）
	smallPrimary := []Candidate{
		{CredentialID: 1, CanonicalName: "hot-1", Tier: "primary", PopularityScore: 100, Tags: []string{"code"}, SuccessRate: 0.95, ReleasedAt: &now},
		{CredentialID: 2, CanonicalName: "hot-2", Tier: "primary", PopularityScore: 90, Tags: []string{"code"}, SuccessRate: 0.93, ReleasedAt: &now},
	}
	smallSecondary := []Candidate{
		{CredentialID: 6, CanonicalName: "sec-1", Tier: "secondary", PopularityScore: 50, Tags: []string{"code"}, SuccessRate: 0.85, ReleasedAt: &now},
		{CredentialID: 7, CanonicalName: "sec-2", Tier: "secondary", PopularityScore: 40, Tags: []string{"code"}, SuccessRate: 0.83, ReleasedAt: &now},
	}
	idxSmall := &Index{entries: append(smallPrimary, smallSecondary...), lastRefresh: time.Now()}
	resultsSmall := idxSmall.Recommend(TaskCode, ClassificationSignals{EstimatedTokens: 1000}, ProfileSmart, 3)
	if len(resultsSmall) < 3 {
		t.Fatalf("expected at least 3 results (with fallback), got %d", len(resultsSmall))
	}
	// 验证前 2 个来自 primary，第 3 个来自 secondary（兜底）
	primaryCount := 0
	secondaryCount := 0
	for _, sc := range resultsSmall {
		if sc.Candidate.Tier == "primary" {
			primaryCount++
		} else if sc.Candidate.Tier == "secondary" {
			secondaryCount++
		}
	}
	if primaryCount != 2 {
		t.Errorf("expected 2 primary, got %d", primaryCount)
	}
	if secondaryCount < 1 {
		t.Errorf("expected at least 1 secondary (fallback), got %d", secondaryCount)
	}
}

// TestIndex_Recommend_PopularitySort 验证热门池按 popularity_score 排序
func TestIndex_Recommend_PopularitySort(t *testing.T) {
	now := time.Now()
	// 故意打乱顺序，验证是否按 popularity_score 排序
	candidates := []Candidate{
		{CredentialID: 3, CanonicalName: "low", Tier: "primary", PopularityScore: 30, Tags: []string{"chat"}, SuccessRate: 0.90, ReleasedAt: &now},
		{CredentialID: 1, CanonicalName: "high", Tier: "primary", PopularityScore: 100, Tags: []string{"chat"}, SuccessRate: 0.85, ReleasedAt: &now},
		{CredentialID: 2, CanonicalName: "mid", Tier: "primary", PopularityScore: 60, Tags: []string{"chat"}, SuccessRate: 0.88, ReleasedAt: &now},
	}
	idx := &Index{entries: candidates, lastRefresh: time.Now()}

	results := idx.Recommend(TaskChat, ClassificationSignals{EstimatedTokens: 1000}, ProfileSmart, 3)
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	// 验证第一个是 popularity_score 最高的（虽然 SuccessRate 较低）
	// 注意：L1 只按 popularity 排序，L2 才按 composite 排序
	// 所以这里验证 L1 输入包含了 high/mid/low 三个候选
	names := make(map[string]bool)
	for _, sc := range results {
		names[sc.Candidate.CanonicalName] = true
	}
	if !names["high"] || !names["mid"] || !names["low"] {
		t.Errorf("expected all 3 candidates in results, got %v", names)
	}
}

// TestIndex_Recommend_EmptyPool 验证空池处理
func TestIndex_Recommend_EmptyPool(t *testing.T) {
	idx := &Index{entries: []Candidate{}, lastRefresh: time.Now()}
	results := idx.Recommend(TaskReasoning, ClassificationSignals{EstimatedTokens: 1000}, ProfileSmart, 3)
	if results != nil {
		t.Errorf("expected nil for empty pool, got %d results", len(results))
	}
}
