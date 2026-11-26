package maas

import (
	"context"
	"testing"
)

func TestCalculateCredits_Disabled(t *testing.T) {
	svc := &Service{} // pool == nil means MaaS disabled

	result, err := svc.CalculateCredits(context.Background(), CalculateCreditsInput{
		TenantID:      "test-tenant",
		CanonicalName: "gpt-4",
		Usage: TokenUsage{
			PromptTokens:     1000,
			CompletionTokens: 500,
		},
		RateMultiplier: 1.0,
	})

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if result.Credits != 0 {
		t.Errorf("expected 0 credits when disabled, got %d", result.Credits)
	}
}

func TestCalculateCredits_ZeroUsage(t *testing.T) {
	t.Skip("需要数据库连接，跳过单元测试")
	// svc := &Service{pool: pool} // Need real pool

	result, err := (&Service{}).CalculateCredits(context.Background(), CalculateCreditsInput{
		TenantID:      "test-tenant",
		CanonicalName: "gpt-4",
		Usage:         TokenUsage{}, // 零用量
		RateMultiplier: 1.0,
	})

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if result.Credits != 0 {
		t.Errorf("expected 0 credits for zero usage, got %d", result.Credits)
	}
}

func TestCalculateCredits_MultiplierNormalization(t *testing.T) {
	tests := []struct {
		name           string
		inputMultiplier float64
		wantMultiplier float64
	}{
		{
			name:           "zero multiplier normalized to 1.0",
			inputMultiplier: 0,
			wantMultiplier: 1.0,
		},
		{
			name:           "negative multiplier normalized to 1.0",
			inputMultiplier: -0.5,
			wantMultiplier: 1.0,
		},
		{
			name:           "positive multiplier kept",
			inputMultiplier: 1.5,
			wantMultiplier: 1.5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &Service{} // pool == nil means MaaS disabled

			result, err := svc.CalculateCredits(context.Background(), CalculateCreditsInput{
				TenantID:      "test-tenant",
				CanonicalName: "gpt-4",
				Usage:         TokenUsage{}, // 零用量，只测试倍率规范化
				RateMultiplier: tt.inputMultiplier,
			})

			if err != nil {
				t.Fatalf("expected no error, got %v", err)
			}

			if result.RateMultiplier != tt.wantMultiplier {
				t.Errorf("expected multiplier %f, got %f", tt.wantMultiplier, result.RateMultiplier)
			}
		})
	}
}

func TestCalculateCredits_BasicCalculation(t *testing.T) {
	// 注意：此测试需要真实数据库连接或mock
	// 这里仅展示测试结构，实际运行需要集成测试环境

	t.Skip("需要数据库连接，跳过单元测试")

	// 示例测试逻辑：
	// svc := setupTestService(t)
	// 
	// result, err := svc.CalculateCredits(context.Background(), CalculateCreditsInput{
	//     TenantID:      "test-tenant",
	//     CanonicalName: "gpt-4",
	//     Usage: TokenUsage{
	//         PromptTokens:     1000,
	//         CompletionTokens: 500,
	//     },
	//     RateMultiplier: 1.0,
	// })
	// 
	// if err != nil {
	//     t.Fatalf("expected no error, got %v", err)
	// }
	// 
	// if result.Credits <= 0 {
	//     t.Errorf("expected positive credits, got %d", result.Credits)
	// }
}
