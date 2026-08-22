package autocombo

import (
	"encoding/json"
	"testing"
)

func TestEngine_ScoreAll(t *testing.T) {
	weights := ScoringWeights{
		HealthScore:    0.3,
		LatencyP95:     0.2,
		QuotaRemaining: 0.25,
		Cost:           0.0,
		TaskFit:        0.15,
		TierAffinity:   0.1,
	}

	weightsJSON, _ := json.Marshal(weights)
	engine, err := NewEngine(weightsJSON)
	if err != nil {
		t.Fatalf("NewEngine failed: %v", err)
	}

	pool := []Candidate{
		{
			ProviderCode: "openrouter",
			ModelID:      "gpt-3.5-turbo:free",
			HealthScore:  0.95,
			LatencyP95:   150,
			CostPer1M:    0,
			QuotaRemain:  0.8,
			IsFree:       true,
		},
		{
			ProviderCode: "groq",
			ModelID:      "llama-3.3-70b",
			HealthScore:  0.9,
			LatencyP95:   80,
			CostPer1M:    0,
			QuotaRemain:  0.9,
			IsFree:       true,
		},
		{
			ProviderCode: "mistral",
			ModelID:      "mistral-large-latest",
			HealthScore:  0.85,
			LatencyP95:   200,
			CostPer1M:    0,
			QuotaRemain:  0.95,
			IsFree:       true,
		},
	}

	scored := engine.scoreAll(pool)

	if len(scored) != 3 {
		t.Errorf("expected 3 scored candidates, got %d", len(scored))
	}

	// 验证排序（按评分降序）
	for i := 0; i < len(scored)-1; i++ {
		if scored[i].Score < scored[i+1].Score {
			t.Errorf("scored candidates not sorted: %f < %f", scored[i].Score, scored[i+1].Score)
		}
	}

	// 验证评分范围 [0, 1]
	for _, sc := range scored {
		if sc.Score < 0 || sc.Score > 1 {
			t.Errorf("score out of range: %f", sc.Score)
		}
	}
}

func TestEngine_SplitTiers(t *testing.T) {
	engine := &Engine{}

	scored := []ScoredCandidate{
		{Score: 0.95},
		{Score: 0.82},
		{Score: 0.75},
		{Score: 0.6},
		{Score: 0.45},
		{Score: 0.3},
	}

	tiers := engine.splitTiers(scored)

	if len(tiers.Top) != 2 {
		t.Errorf("expected 2 in Top tier, got %d", len(tiers.Top))
	}
	if len(tiers.Mid) != 2 {
		t.Errorf("expected 2 in Mid tier, got %d", len(tiers.Mid))
	}
	if len(tiers.Rest) != 2 {
		t.Errorf("expected 2 in Rest tier, got %d", len(tiers.Rest))
	}
}

func TestEngine_SelectCandidate(t *testing.T) {
	weights := ScoringWeights{
		HealthScore:    0.4,
		LatencyP95:     0.3,
		QuotaRemaining: 0.3,
	}

	weightsJSON, _ := json.Marshal(weights)
	engine, _ := NewEngine(weightsJSON)

	pool := []Candidate{
		{
			ProviderCode: "provider1",
			ModelID:      "model1",
			HealthScore:  0.9,
			LatencyP95:   100,
			QuotaRemain:  0.8,
			IsFree:       true,
		},
		{
			ProviderCode: "provider2",
			ModelID:      "model2",
			HealthScore:  0.7,
			LatencyP95:   200,
			QuotaRemain:  0.5,
			IsFree:       true,
		},
	}

	candidate, err := engine.SelectCandidate(pool)
	if err != nil {
		t.Fatalf("SelectCandidate failed: %v", err)
	}

	if candidate == nil {
		t.Fatal("expected non-nil candidate")
	}

	// 应该选择评分更高的 provider1
	if candidate.ProviderCode != "provider1" {
		t.Errorf("expected provider1, got %s", candidate.ProviderCode)
	}
}

func TestEngine_EmptyPool(t *testing.T) {
	engine := &Engine{}

	_, err := engine.SelectCandidate([]Candidate{})
	if err == nil {
		t.Error("expected error for empty pool")
	}
}

func TestResolver_BuiltinTemplates(t *testing.T) {
	resolver := &Resolver{}

	testCases := []struct {
		modelID  string
		variant  Variant
		wantTier string // expected TierFilter's first item ("" if any)
	}{
		// round 2 已有的 free tier 家族.
		{"auto/free", VariantCheap, "free"},
		{"auto/best-free", VariantCheap, "free"},
		{"auto/coding:free", VariantCoding, "free"},
		{"auto/reasoning:free", VariantReasoning, "free"},
		{"auto/fast:free", VariantFast, "free"},
		{"auto/creative:free", VariantCreative, "free"},

		// round 3 M8 新增.
		{"auto/coding", VariantCoding, ""}, // 任意 tier
		{"auto/coding:cheap", VariantCoding, "cheap"},
		{"auto/coding:pro", VariantCoding, ""}, // pro 表示允许 paid
		{"auto/reasoning", VariantReasoning, ""},
		{"auto/reasoning:pro", VariantReasoning, ""},
		{"auto/fast", VariantFast, ""},
		{"auto/vision", VariantSmart, ""},
		{"auto/multimodal", VariantSmart, ""},
	}

	for _, tc := range testCases {
		t.Run(tc.modelID, func(t *testing.T) {
			spec, err := resolver.getBuiltinTemplate(tc.modelID, "default")
			if err != nil {
				t.Fatalf("getBuiltinTemplate failed: %v", err)
			}

			if spec.Variant != tc.variant {
				t.Errorf("expected variant %s, got %s", tc.variant, spec.Variant)
			}

			if spec.ComboName != tc.modelID {
				t.Errorf("expected combo_name %s, got %s", tc.modelID, spec.ComboName)
			}

			// 验证权重和约为 1.0
			var weights ScoringWeights
			if err := json.Unmarshal(spec.ScoringWeightsJSON, &weights); err != nil {
				t.Fatalf("unmarshal weights failed: %v", err)
			}

			total := weights.HealthScore + weights.LatencyP95 +
				weights.QuotaRemaining + weights.Cost +
				weights.TaskFit + weights.TierAffinity

			if total < 0.99 || total > 1.01 {
				t.Errorf("weights sum to %.2f, expected ~1.0", total)
			}

			// 校验 tier 过滤 (只取 TierFilter 的第一个非空项作为代表)
			firstTier := ""
			for _, t := range spec.TierFilter {
				if t != "" {
					firstTier = t
					break
				}
			}
			if firstTier != tc.wantTier {
				t.Errorf("expected first tier %q, got %q", tc.wantTier, firstTier)
			}
		})
	}
}

func TestResolver_UnknownCombo(t *testing.T) {
	resolver := &Resolver{}

	_, err := resolver.getBuiltinTemplate("auto/unknown", "default")
	if err == nil {
		t.Error("expected error for unknown combo")
	}
}

// TestNewEngine_RejectsBadWeights 验证 round 3 M2 权重和校验.
func TestNewEngine_RejectsBadWeights(t *testing.T) {
	badCases := []ScoringWeights{
		{HealthScore: 0.5, LatencyP95: 0.3},                      // 0.8 < 0.99
		{HealthScore: 0.5, LatencyP95: 0.7},                      // 1.2 > 1.01
		{HealthScore: 0.0, LatencyP95: 0.0, QuotaRemaining: 0.0}, // 0
	}
	for _, w := range badCases {
		j, _ := json.Marshal(w)
		if _, err := NewEngine(j); err == nil {
			t.Errorf("NewEngine should reject weights %+v", w)
		}
	}

	// 合法权重.
	good := ScoringWeights{
		HealthScore: 0.3, LatencyP95: 0.2, QuotaRemaining: 0.25,
		Cost: 0.0, TaskFit: 0.15, TierAffinity: 0.1,
	}
	j, _ := json.Marshal(good)
	if _, err := NewEngine(j); err != nil {
		t.Errorf("NewEngine should accept valid weights: %v", err)
	}
}

// TestEstimateTaskFit_Keyword 验证 round 4 L7 estimateTaskFit 的 keyword
// 启发式. 不依赖 DB, 纯函数测试.
func TestEstimateTaskFit_Keyword(t *testing.T) {
	tests := []struct {
		variant Variant
		modelID string
		wantMin float64 // 期望 task fit 至少这个值
	}{
		// coding 变体: 模型名包含 code/coder 给 1.0, 否则 0.6.
		{VariantCoding, "openai/gpt-code", 1.0},
		{VariantCoding, "starcoder2", 1.0},
		{VariantCoding, "openai/gpt-4o", 0.6},

		// reasoning 变体.
		{VariantReasoning, "openai/o1-preview", 1.0},
		{VariantReasoning, "deepseek-r1", 1.0},
		{VariantReasoning, "openai/gpt-4o", 0.6},

		// fast 变体.
		{VariantFast, "claude-haiku-3", 1.0},
		{VariantFast, "gpt-4o-mini", 1.0},
		{VariantFast, "gpt-4o", 0.7},

		// creative 变体.
		{VariantCreative, "storyteller-llm", 1.0},
		{VariantCreative, "gpt-4o", 0.7},

		// 空 variant: 通用 0.85.
		{"", "gpt-4o", 0.85},
	}
	for _, tc := range tests {
		score := taskFitFromKeywords(tc.modelID, tc.variant)
		if score < tc.wantMin-0.01 {
			t.Errorf("variant=%q modelID=%q: got %.3f, want >= %.3f",
				tc.variant, tc.modelID, score, tc.wantMin)
		}
	}
}
