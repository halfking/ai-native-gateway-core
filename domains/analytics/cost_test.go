package analytics

import (
	"testing"
)

func TestCalculateInternalCost(t *testing.T) {
	calc := NewCostCalculator()

	tests := []struct {
		name      string
		costCents int64
		wantUSD   float64
	}{
		{"zero", 0, 0.0},
		{"one dollar", 100, 1.0},
		{"cents", 123, 1.23},
		{"large amount", 20127900, 201279.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := calc.CalculateInternalCost(tt.costCents)
			if got != tt.wantUSD {
				t.Errorf("CalculateInternalCost(%d) = %.2f, want %.2f", tt.costCents, got, tt.wantUSD)
			}
		})
	}
}

func TestSplitCost(t *testing.T) {
	calc := NewCostCalculator()

	tests := []struct {
		name            string
		internalCents   int64
		externalCredits int64
		wantInternal    float64
		wantExternal    int64
		wantHasInternal bool
		wantHasExternal bool
	}{
		{"both zero", 0, 0, 0.0, 0, false, false},
		{"only internal", 10050, 0, 100.50, 0, true, false},
		{"only external", 0, 5000, 0.0, 5000, false, true},
		{"both present", 20127900, 5000, 201279.0, 5000, true, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			breakdown := calc.SplitCost(tt.internalCents, tt.externalCredits)

			if breakdown.InternalCostUSD != tt.wantInternal {
				t.Errorf("InternalCostUSD = %.2f, want %.2f", breakdown.InternalCostUSD, tt.wantInternal)
			}
			if breakdown.ExternalCredits != tt.wantExternal {
				t.Errorf("ExternalCredits = %d, want %d", breakdown.ExternalCredits, tt.wantExternal)
			}
			if breakdown.HasInternalCost != tt.wantHasInternal {
				t.Errorf("HasInternalCost = %v, want %v", breakdown.HasInternalCost, tt.wantHasInternal)
			}
			if breakdown.HasExternalCost != tt.wantHasExternal {
				t.Errorf("HasExternalCost = %v, want %v", breakdown.HasExternalCost, tt.wantHasExternal)
			}
		})
	}
}

func TestDistributeTailDifference(t *testing.T) {
	calc := NewCostCalculator()

	t.Run("simple distribution", func(t *testing.T) {
		groups := map[string]int64{
			"A": 50,
			"B": 30,
			"C": 20,
		}
		result := calc.DistributeTailDifference(100, groups)

		// 验证总和等于 100
		var sum int64
		for _, cost := range result {
			sum += cost
		}
		if sum != 100 {
			t.Errorf("sum = %d, want 100", sum)
		}

		// A组最大，应该得到尾差
		if result["A"] < 50 {
			t.Errorf("group A should get at least 50, got %d", result["A"])
		}
	})

	t.Run("empty groups", func(t *testing.T) {
		groups := map[string]int64{}
		result := calc.DistributeTailDifference(100, groups)
		if len(result) != 0 {
			t.Errorf("expected empty result for empty groups")
		}
	})

	t.Run("zero total", func(t *testing.T) {
		groups := map[string]int64{"A": 10, "B": 20}
		result := calc.DistributeTailDifference(0, groups)

		var sum int64
		for _, cost := range result {
			sum += cost
		}
		if sum != 0 {
			t.Errorf("sum should be 0 for zero total, got %d", sum)
		}
	})

	t.Run("realistic scenario from reconciliation", func(t *testing.T) {
		// 模拟对账模块 F4 场景：6天成本差4分的情况
		groups := map[string]int64{
			"provider_1": 50000,
			"provider_2": 30000,
			"provider_3": 20000,
		}

		total := int64(20127900) // 201279.00 分
		result := calc.DistributeTailDifference(total, groups)

		var sum int64
		for _, cost := range result {
			sum += cost
		}

		if sum != total {
			t.Errorf("sum = %d, want %d (diff: %d)", sum, total, total-sum)
		}
	})
}

func TestValidateCostUnit(t *testing.T) {
	calc := NewCostCalculator()

	tests := []struct {
		name        string
		hasInternal bool
		hasExternal bool
		wantErr     bool
	}{
		{"only internal", true, false, false},
		{"only external", false, true, false},
		{"neither", false, false, false},
		{"both - prohibited", true, true, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := calc.ValidateCostUnit(tt.hasInternal, tt.hasExternal)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateCostUnit() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestCalculateTotalCost_Deprecated(t *testing.T) {
	calc := NewCostCalculator()

	// 验证 deprecated 方法确实返回错误
	_, err := calc.CalculateTotalCost(100, 200)
	if err == nil {
		t.Error("CalculateTotalCost should return error (deprecated)")
	}
}

func TestFormatCost(t *testing.T) {
	calc := NewCostCalculator()

	t.Run("format internal cost", func(t *testing.T) {
		tests := []struct {
			usd  float64
			want string
		}{
			{0.0, "$0.00"},
			{1.23, "$1.23"},
			{201279.0, "$201279.00"},
		}

		for _, tt := range tests {
			got := calc.FormatInternalCost(tt.usd)
			if got != tt.want {
				t.Errorf("FormatInternalCost(%.2f) = %s, want %s", tt.usd, got, tt.want)
			}
		}
	})

	t.Run("format external cost", func(t *testing.T) {
		tests := []struct {
			credits int64
			want    string
		}{
			{0, "0 积分"},
			{5000, "5000 积分"},
			{123456, "123456 积分"},
		}

		for _, tt := range tests {
			got := calc.FormatExternalCost(tt.credits)
			if got != tt.want {
				t.Errorf("FormatExternalCost(%d) = %s, want %s", tt.credits, got, tt.want)
			}
		}
	})
}
