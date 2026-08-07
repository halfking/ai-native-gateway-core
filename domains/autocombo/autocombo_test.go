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
		modelID string
		variant Variant
	}{
		{"auto/free", VariantCheap},
		{"auto/best-free", VariantCheap},
		{"auto/coding:free", VariantCoding},
		{"auto/reasoning:free", VariantReasoning},
		{"auto/fast:free", VariantFast},
		{"auto/creative:free", VariantCreative},
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

			// 验证权重
			var weights ScoringWeights
			if err := json.Unmarshal(spec.ScoringWeightsJSON, &weights); err != nil {
				t.Fatalf("unmarshal weights failed: %v", err)
			}

			// 验证权重和约为 1.0
			total := weights.HealthScore + weights.LatencyP95 +
				weights.QuotaRemaining + weights.Cost +
				weights.TaskFit + weights.TierAffinity

			if total < 0.99 || total > 1.01 {
				t.Errorf("weights sum to %.2f, expected ~1.0", total)
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
