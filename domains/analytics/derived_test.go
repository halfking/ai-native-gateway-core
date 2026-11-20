package analytics

import (
	"testing"
)

func TestSuccessRate(t *testing.T) {
	calc := NewDerivedMetricsCalculator()

	tests := []struct {
		name    string
		total   int64
		success int64
		want    float64
	}{
		{"zero total", 0, 0, 0.0},
		{"perfect success", 100, 100, 1.0},
		{"half success", 100, 50, 0.5},
		{"realistic case", 123367, 78185, 0.633759},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := calc.SuccessRate(tt.total, tt.success)
			if !floatEqual(got, tt.want, 0.0001) {
				t.Errorf("SuccessRate(%d, %d) = %.6f, want %.6f", tt.total, tt.success, got, tt.want)
			}
		})
	}
}

func TestErrorCount(t *testing.T) {
	calc := NewDerivedMetricsCalculator()

	tests := []struct {
		name    string
		total   int64
		success int64
		want    int64
	}{
		{"no errors", 100, 100, 0},
		{"some errors", 123367, 78185, 45182},
		{"all errors", 100, 0, 100},
		{"anomaly - success > total", 100, 150, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := calc.ErrorCount(tt.total, tt.success)
			if got != tt.want {
				t.Errorf("ErrorCount(%d, %d) = %d, want %d", tt.total, tt.success, got, tt.want)
			}
		})
	}
}

func TestAverage(t *testing.T) {
	calc := NewDerivedMetricsCalculator()

	tests := []struct {
		name  string
		sum   int64
		count int64
		want  float64
	}{
		{"zero count", 100, 0, 0.0},
		{"simple average", 100, 10, 10.0},
		{"with decimals", 123, 10, 12.3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := calc.Average(tt.sum, tt.count)
			if !floatEqual(got, tt.want, 0.0001) {
				t.Errorf("Average(%d, %d) = %.2f, want %.2f", tt.sum, tt.count, got, tt.want)
			}
		})
	}
}

func TestWeightedAverage(t *testing.T) {
	calc := NewDerivedMetricsCalculator()

	t.Run("simple weighted average", func(t *testing.T) {
		values := []float64{100.0, 200.0, 300.0}
		weights := []int64{1, 2, 3}
		// (100*1 + 200*2 + 300*3) / (1+2+3) = 1400/6 = 233.33
		want := 233.33333
		got := calc.WeightedAverage(values, weights)
		if !floatEqual(got, want, 0.001) {
			t.Errorf("WeightedAverage() = %.5f, want %.5f", got, want)
		}
	})

	t.Run("empty inputs", func(t *testing.T) {
		got := calc.WeightedAverage([]float64{}, []int64{})
		if got != 0.0 {
			t.Errorf("WeightedAverage(empty) = %.2f, want 0.0", got)
		}
	})

	t.Run("mismatched lengths", func(t *testing.T) {
		values := []float64{100.0, 200.0}
		weights := []int64{1}
		got := calc.WeightedAverage(values, weights)
		if got != 0.0 {
			t.Errorf("WeightedAverage(mismatched) = %.2f, want 0.0", got)
		}
	})
}

func TestPercentile(t *testing.T) {
	calc := NewDerivedMetricsCalculator()

	values := []float64{1.0, 2.0, 3.0, 4.0, 5.0, 6.0, 7.0, 8.0, 9.0, 10.0}

	tests := []struct {
		name string
		p    float64
		want float64
	}{
		{"P0 (min)", 0.0, 1.0},
		{"P50 (median)", 0.50, 5.5},
		{"P95", 0.95, 9.55},
		{"P99", 0.99, 9.91},
		{"P100 (max)", 1.0, 10.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := calc.Percentile(values, tt.p)
			if !floatEqual(got, tt.want, 0.01) {
				t.Errorf("Percentile(%.2f) = %.2f, want %.2f", tt.p, got, tt.want)
			}
		})
	}
}

func TestP95(t *testing.T) {
	calc := NewDerivedMetricsCalculator()

	// 模拟延迟数据
	latencies := []float64{10, 20, 30, 40, 50, 100, 200, 500, 1000, 2000}
	p95 := calc.P95(latencies)

	// P95应该接近1900（在1000和2000之间）
	if p95 < 1000 || p95 > 2000 {
		t.Errorf("P95(latencies) = %.2f, expected between 1000 and 2000", p95)
	}
}

func TestGrowthRate(t *testing.T) {
	calc := NewDerivedMetricsCalculator()

	tests := []struct {
		name     string
		oldValue float64
		newValue float64
		want     float64
	}{
		{"no growth", 100.0, 100.0, 0.0},
		{"50% growth", 100.0, 150.0, 0.5},
		{"negative growth", 100.0, 80.0, -0.2},
		{"from zero", 0.0, 100.0, 1.0},
		{"to zero", 100.0, 0.0, -1.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := calc.GrowthRate(tt.oldValue, tt.newValue)
			if !floatEqual(got, tt.want, 0.0001) {
				t.Errorf("GrowthRate(%.2f, %.2f) = %.4f, want %.4f",
					tt.oldValue, tt.newValue, got, tt.want)
			}
		})
	}
}

func TestMinMaxSum(t *testing.T) {
	calc := NewDerivedMetricsCalculator()

	values := []float64{5.0, 2.0, 8.0, 1.0, 9.0, 3.0}

	t.Run("Min", func(t *testing.T) {
		got := calc.Min(values)
		if got != 1.0 {
			t.Errorf("Min() = %.2f, want 1.0", got)
		}
	})

	t.Run("Max", func(t *testing.T) {
		got := calc.Max(values)
		if got != 9.0 {
			t.Errorf("Max() = %.2f, want 9.0", got)
		}
	})

	t.Run("Sum", func(t *testing.T) {
		got := calc.Sum(values)
		if got != 28.0 {
			t.Errorf("Sum() = %.2f, want 28.0", got)
		}
	})

	t.Run("empty slice", func(t *testing.T) {
		empty := []float64{}
		if calc.Min(empty) != 0.0 {
			t.Error("Min(empty) should be 0.0")
		}
		if calc.Max(empty) != 0.0 {
			t.Error("Max(empty) should be 0.0")
		}
		if calc.Sum(empty) != 0.0 {
			t.Error("Sum(empty) should be 0.0")
		}
	})
}

func TestStandardDeviation(t *testing.T) {
	calc := NewDerivedMetricsCalculator()

	t.Run("uniform values", func(t *testing.T) {
		values := []float64{5.0, 5.0, 5.0, 5.0}
		got := calc.StandardDeviation(values)
		if got != 0.0 {
			t.Errorf("StandardDeviation(uniform) = %.2f, want 0.0", got)
		}
	})

	t.Run("varying values", func(t *testing.T) {
		values := []float64{2.0, 4.0, 4.0, 4.0, 5.0, 5.0, 7.0, 9.0}
		// Mean = 5, expected std dev ≈ 2.0
		got := calc.StandardDeviation(values)
		if got < 1.5 || got > 2.5 {
			t.Errorf("StandardDeviation() = %.2f, expected around 2.0", got)
		}
	})

	t.Run("empty slice", func(t *testing.T) {
		got := calc.StandardDeviation([]float64{})
		if got != 0.0 {
			t.Errorf("StandardDeviation(empty) = %.2f, want 0.0", got)
		}
	})
}

func TestFormatRate(t *testing.T) {
	calc := NewDerivedMetricsCalculator()

	tests := []struct {
		rate float64
		want string
	}{
		{0.0, "0.00%"},
		{0.5, "50.00%"},
		{0.9956, "99.56%"},
		{1.0, "100.00%"},
	}

	for _, tt := range tests {
		got := calc.FormatRate(tt.rate)
		if got != tt.want {
			t.Errorf("FormatRate(%.4f) = %s, want %s", tt.rate, got, tt.want)
		}
	}
}

// Helper function for float comparison
func floatEqual(a, b, epsilon float64) bool {
	diff := a - b
	if diff < 0 {
		diff = -diff
	}
	return diff < epsilon
}
