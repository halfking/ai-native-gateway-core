package autoroute

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestScoreSimplified_IntentAndPriceWeighting 验证简化评分公式的权重
func TestScoreSimplified_IntentAndPriceWeighting(t *testing.T) {
	// 构造候选：高意图匹配 + 高价格
	highIntentHighPrice := Candidate{
		CanonicalID:       1,
		CanonicalName:     "model-a",
		TaskMatchScore:    0.9, // 90% 意图匹配
		UnitPriceInPer1M:  500,
		UnitPriceOutPer1M: 500, // 总价 1000
	}

	// 构造候选：低意图匹配 + 低价格
	lowIntentLowPrice := Candidate{
		CanonicalID:       2,
		CanonicalName:     "model-b",
		TaskMatchScore:    0.3, // 30% 意图匹配
		UnitPriceInPer1M:  50,
		UnitPriceOutPer1M: 50, // 总价 100
	}

	avgPrices := map[int]float64{
		1: 1000, // model-a 平均价格
		2: 100,  // model-b 平均价格
	}

	// 评分
	scoreA := ScoreSimplified(highIntentHighPrice, TaskCode, avgPrices, 0)
	scoreB := ScoreSimplified(lowIntentLowPrice, TaskCode, avgPrices, 0)

	// 验证意图匹配分
	if scoreA.MatchScore != 90 {
		t.Errorf("model-a MatchScore: got %.2f, want 90", scoreA.MatchScore)
	}
	if scoreB.MatchScore != 30 {
		t.Errorf("model-b MatchScore: got %.2f, want 30", scoreB.MatchScore)
	}

	// 验证价格分
	if scoreA.PriceScore != 0 {
		t.Errorf("model-a PriceScore: got %.2f, want 0", scoreA.PriceScore)
	}
	if scoreB.PriceScore != 90 {
		t.Errorf("model-b PriceScore: got %.2f, want 90", scoreB.PriceScore)
	}

	expectedA := 90*0.6 + 0*0.4
	expectedB := 30*0.6 + 90*0.4

	if abs(scoreA.Composite-expectedA) > 0.01 {
		t.Errorf("model-a Composite: got %.2f, want %.2f", scoreA.Composite, expectedA)
	}
	if abs(scoreB.Composite-expectedB) > 0.01 {
		t.Errorf("model-b Composite: got %.2f, want %.2f", scoreB.Composite, expectedB)
	}

	if abs(scoreA.Composite-scoreB.Composite) > 0.01 {
		t.Errorf("scores should be equal: model-a %.2f vs model-b %.2f", scoreA.Composite, scoreB.Composite)
	}
}

// TestComputeCorrectionScore 验证会话校正分计算
func TestComputeCorrectionScore(t *testing.T) {
	tests := []struct {
		name          string
		lastTask      TaskType
		lastModel     string
		lastSuccess   bool
		lastLatencyMs int
		currentTask   TaskType
		currentModel  string
		expectedScore float64
	}{
		{
			name:          "上次成功且快速 → +5",
			lastTask:      TaskCode,
			lastModel:     "model-a",
			lastSuccess:   true,
			lastLatencyMs: 1500,
			currentTask:   TaskCode,
			currentModel:  "model-a",
			expectedScore: 5,
		},
		{
			name:          "上次失败 → -10",
			lastTask:      TaskCode,
			lastModel:     "model-a",
			lastSuccess:   false,
			lastLatencyMs: 2000,
			currentTask:   TaskCode,
			currentModel:  "model-a",
			expectedScore: -10,
		},
		{
			name:          "任务类型变化 → 0",
			lastTask:      TaskCode,
			lastModel:     "model-a",
			lastSuccess:   true,
			lastLatencyMs: 1000,
			currentTask:   TaskReasoning,
			currentModel:  "model-a",
			expectedScore: 0,
		},
		{
			name:          "模型不同 → 0",
			lastTask:      TaskCode,
			lastModel:     "model-a",
			lastSuccess:   true,
			lastLatencyMs: 1000,
			currentTask:   TaskCode,
			currentModel:  "model-b",
			expectedScore: 0,
		},
		{
			name:          "上次成功但较慢 → 0",
			lastTask:      TaskCode,
			lastModel:     "model-a",
			lastSuccess:   true,
			lastLatencyMs: 3000,
			currentTask:   TaskCode,
			currentModel:  "model-a",
			expectedScore: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score := ComputeCorrectionScore(
				tt.lastTask,
				tt.lastModel,
				tt.lastSuccess,
				tt.lastLatencyMs,
				tt.currentTask,
				tt.currentModel,
			)
			if score != tt.expectedScore {
				t.Errorf("got %.2f, want %.2f", score, tt.expectedScore)
			}
		})
	}
}

// TestComputeAvgPriceByCanonical 验证按 canonical 计算平均价格
func TestComputeAvgPriceByCanonical(t *testing.T) {
	candidates := []Candidate{
		{CanonicalID: 1, UnitPriceInPer1M: 100, UnitPriceOutPer1M: 100, UnavailableReason: ""},
		{CanonicalID: 1, UnitPriceInPer1M: 200, UnitPriceOutPer1M: 200, UnavailableReason: ""},
		{CanonicalID: 2, UnitPriceInPer1M: 50, UnitPriceOutPer1M: 50, UnavailableReason: ""},
		{CanonicalID: 1, UnitPriceInPer1M: 300, UnitPriceOutPer1M: 300, UnavailableReason: "manual"},
	}

	avgPrices := ComputeAvgPriceByCanonical(candidates)

	if avgPrices[1] != 300 {
		t.Errorf("canonical 1 avg price: got %.2f, want 300", avgPrices[1])
	}
	if avgPrices[2] != 100 {
		t.Errorf("canonical 2 avg price: got %.2f, want 100", avgPrices[2])
	}
}

// TestRecommendV2_AvailabilityGate 验证快照可用性硬门禁
func TestRecommendV2_AvailabilityGate(t *testing.T) {
	now := time.Now()
	candidates := []Candidate{
		{CredentialID: 1, CanonicalID: 1, CanonicalName: "available", UnavailableReason: "", Tags: []string{"code"}, SuccessRate: 0.95, UnitPriceInPer1M: 100, UnitPriceOutPer1M: 100},
		{CredentialID: 2, CanonicalID: 2, CanonicalName: "unavailable", UnavailableReason: "manual", Tags: []string{"code"}, SuccessRate: 0.98, UnitPriceInPer1M: 50, UnitPriceOutPer1M: 50},
	}

	idx := &Index{entries: candidates, lastRefresh: now}

	results := idx.RecommendV2(context.Background(), TaskCode, ClassificationSignals{}, "", 3)

	if len(results) != 1 {
		t.Fatalf("expected 1 available candidate, got %d", len(results))
	}
	if results[0].Candidate.CanonicalName != "available" {
		t.Errorf("expected 'available', got %s", results[0].Candidate.CanonicalName)
	}
}

// TestRecommendV2_LiveAvailabilityFilter 验证 V2 在快照字段不可用时仍可通过实时过滤排除候选。
func TestRecommendV2_LiveAvailabilityFilter(t *testing.T) {
	candidates := []Candidate{
		{CredentialID: 1, CanonicalID: 1, CanonicalName: "blocked", UnavailableReason: "", Tags: []string{"code"}, SuccessRate: 0.99, UnitPriceInPer1M: 10, UnitPriceOutPer1M: 10},
		{CredentialID: 2, CanonicalID: 2, CanonicalName: "allowed", UnavailableReason: "", Tags: []string{"code"}, SuccessRate: 0.90, UnitPriceInPer1M: 100, UnitPriceOutPer1M: 100},
	}

	idx := &Index{
		entries:     candidates,
		lastRefresh: time.Now(),
		availabilityFilter: func(_ context.Context, _ *pgxpool.Pool, all []Candidate) ([]Candidate, error) {
			return []Candidate{all[1]}, nil
		},
	}

	results := idx.RecommendV2(context.Background(), TaskCode, ClassificationSignals{}, "", 3)
	if len(results) != 1 {
		t.Fatalf("expected 1 candidate after live availability filter, got %d", len(results))
	}
	if results[0].Candidate.CanonicalName != "allowed" {
		t.Fatalf("expected allowed candidate, got %s", results[0].Candidate.CanonicalName)
	}
}

// TestRecommendV2_CorrectionScoreApplied 验证上次任务结果校正会提升同模型得分。
func TestRecommendV2_CorrectionScoreApplied(t *testing.T) {
	candidates := []Candidate{
		{CredentialID: 1, CanonicalID: 1, CanonicalName: "model-a", UnavailableReason: "", Tags: []string{"code"}, SuccessRate: 0.95, UnitPriceInPer1M: 100, UnitPriceOutPer1M: 100},
		{CredentialID: 2, CanonicalID: 2, CanonicalName: "model-b", UnavailableReason: "", Tags: []string{"code"}, SuccessRate: 0.95, UnitPriceInPer1M: 100, UnitPriceOutPer1M: 100},
	}

	idx := &Index{
		entries:     candidates,
		lastRefresh: time.Now(),
		correctionLoader: func(_ context.Context, _ *pgxpool.Pool, sessionID string, _ int, task TaskType) (map[string]float64, error) {
			if sessionID != "sess-1" {
				t.Fatalf("unexpected sessionID: %s", sessionID)
			}
			if task != TaskCode {
				t.Fatalf("unexpected task: %s", task)
			}
			return map[string]float64{"model-b": 5}, nil
		},
	}

	results := idx.RecommendV2(context.Background(), TaskCode, ClassificationSignals{}, "sess-1", 3)
	if len(results) < 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Candidate.CanonicalName != "model-b" {
		t.Fatalf("expected corrected model-b to rank first, got %s", results[0].Candidate.CanonicalName)
	}
}

// TestGetHotTop3Canonicals_CacheHit 验证缓存命中时不查 DB。
func TestGetHotTop3Canonicals_CacheHit(t *testing.T) {
	idx := &Index{
		hotCanonicals:    []int{10, 20, 30},
		hotCanonicalsTS:  time.Now(),
		hotCanonicalsTTL: 2 * time.Minute,
	}

	result := idx.getHotTop3Canonicals(context.Background())
	if len(result) != 3 {
		t.Fatalf("expected 3 cached canonicals, got %d", len(result))
	}
	if result[0] != 10 || result[1] != 20 || result[2] != 30 {
		t.Fatalf("cached result mismatch: %v", result)
	}
}

// TestGetHotTop3Canonicals_CacheStale 验证缓存过期时重新查询。
func TestGetHotTop3Canonicals_CacheStale(t *testing.T) {
	idx := &Index{
		hotCanonicals:    []int{10, 20, 30},
		hotCanonicalsTS:  time.Now().Add(-5 * time.Minute),
		hotCanonicalsTTL: 2 * time.Minute,
		pool:             nil,
	}

	result := idx.getHotTop3Canonicals(context.Background())
	if len(result) != 0 {
		t.Fatalf("expected empty result when cache stale and no pool, got %d", len(result))
	}
}

// TestRecommendV2_EmptyPool_Triggers48hFallback 验证空池触发 48h 回退
func TestRecommendV2_EmptyPool_Triggers48hFallback(t *testing.T) {
	candidates := []Candidate{
		{CredentialID: 1, CanonicalID: 1, CanonicalName: "model-a", UnavailableReason: "manual", Tags: []string{"code"}},
		{CredentialID: 2, CanonicalID: 2, CanonicalName: "model-b", UnavailableReason: "timeout", Tags: []string{"code"}},
	}

	idx := &Index{entries: candidates, lastRefresh: time.Now()}
	results := idx.RecommendV2(context.Background(), TaskCode, ClassificationSignals{}, "", 3)
	if results != nil {
		t.Errorf("expected nil when all candidates unavailable and no pool, got %d results", len(results))
	}
}

// TestValidateCachedChoice 验证缓存可用性校验
func TestValidateCachedChoice(t *testing.T) {
	t.Skip("需要数据库连接，集成测试中验证")
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
