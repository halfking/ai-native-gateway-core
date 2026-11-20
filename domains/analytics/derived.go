package analytics

import (
	"fmt"
	"sort"
)

// DerivedMetricsCalculator 派生指标计算器
// 提供统一的派生指标计算方法，防除零、处理边界情况
type DerivedMetricsCalculator struct{}

// NewDerivedMetricsCalculator 创建派生指标计算器
func NewDerivedMetricsCalculator() *DerivedMetricsCalculator {
	return &DerivedMetricsCalculator{}
}

// SuccessRate 计算成功率
// 输入：总数、成功数
// 输出：成功率（0.0~1.0），防除零
func (d *DerivedMetricsCalculator) SuccessRate(total, success int64) float64 {
	if total == 0 {
		return 0.0
	}
	return float64(success) / float64(total)
}

// SuccessRatePercent 计算成功率（百分比形式）
// 输出：0~100
func (d *DerivedMetricsCalculator) SuccessRatePercent(total, success int64) float64 {
	return d.SuccessRate(total, success) * 100.0
}

// ErrorCount 计算失败数
// 输入：总数、成功数
// 输出：失败数（total - success）
func (d *DerivedMetricsCalculator) ErrorCount(total, success int64) int64 {
	if success > total {
		// 数据异常：成功数不应大于总数
		return 0
	}
	return total - success
}

// ErrorRate 计算失败率
// 输出：失败率（0.0~1.0）
func (d *DerivedMetricsCalculator) ErrorRate(total, success int64) float64 {
	return 1.0 - d.SuccessRate(total, success)
}

// Average 计算平均值
// 输入：总和、计数
// 输出：平均值，防除零
func (d *DerivedMetricsCalculator) Average(sum, count int64) float64 {
	if count == 0 {
		return 0.0
	}
	return float64(sum) / float64(count)
}

// AverageFloat 计算浮点数平均值
func (d *DerivedMetricsCalculator) AverageFloat(sum float64, count int64) float64 {
	if count == 0 {
		return 0.0
	}
	return sum / float64(count)
}

// WeightedAverage 计算加权平均值
// 输入：值列表、权重列表
// 输出：加权平均值
func (d *DerivedMetricsCalculator) WeightedAverage(values []float64, weights []int64) float64 {
	if len(values) != len(weights) || len(values) == 0 {
		return 0.0
	}

	var weightedSum float64
	var totalWeight int64

	for i, value := range values {
		weightedSum += value * float64(weights[i])
		totalWeight += weights[i]
	}

	if totalWeight == 0 {
		return 0.0
	}

	return weightedSum / float64(totalWeight)
}

// Percentile 计算百分位数
// 输入：数值列表、百分位（0.0~1.0）
// 输出：对应百分位的值
// 注意：会对输入列表排序
func (d *DerivedMetricsCalculator) Percentile(values []float64, p float64) float64 {
	if len(values) == 0 {
		return 0.0
	}

	if p < 0.0 {
		p = 0.0
	}
	if p > 1.0 {
		p = 1.0
	}

	// 复制并排序
	sorted := make([]float64, len(values))
	copy(sorted, values)
	sort.Float64s(sorted)

	// 计算索引
	index := p * float64(len(sorted)-1)
	lower := int(index)
	upper := lower + 1

	if upper >= len(sorted) {
		return sorted[len(sorted)-1]
	}

	// 线性插值
	weight := index - float64(lower)
	return sorted[lower]*(1-weight) + sorted[upper]*weight
}

// P50 计算中位数（P50）
func (d *DerivedMetricsCalculator) P50(values []float64) float64 {
	return d.Percentile(values, 0.50)
}

// P95 计算 P95 百分位数
func (d *DerivedMetricsCalculator) P95(values []float64) float64 {
	return d.Percentile(values, 0.95)
}

// P99 计算 P99 百分位数
func (d *DerivedMetricsCalculator) P99(values []float64) float64 {
	return d.Percentile(values, 0.99)
}

// GrowthRate 计算增长率
// 输入：旧值、新值
// 输出：增长率（如 0.15 表示增长15%）
func (d *DerivedMetricsCalculator) GrowthRate(oldValue, newValue float64) float64 {
	if oldValue == 0 {
		if newValue == 0 {
			return 0.0
		}
		// 从0增长到非0，返回100%增长
		return 1.0
	}
	return (newValue - oldValue) / oldValue
}

// GrowthRatePercent 计算增长率（百分比形式）
func (d *DerivedMetricsCalculator) GrowthRatePercent(oldValue, newValue float64) float64 {
	return d.GrowthRate(oldValue, newValue) * 100.0
}

// FormatRate 格式化比率为百分比字符串
func (d *DerivedMetricsCalculator) FormatRate(rate float64) string {
	return fmt.Sprintf("%.2f%%", rate*100.0)
}

// FormatAverage 格式化平均值
func (d *DerivedMetricsCalculator) FormatAverage(avg float64, precision int) string {
	format := fmt.Sprintf("%%.%df", precision)
	return fmt.Sprintf(format, avg)
}

// Min 返回最小值
func (d *DerivedMetricsCalculator) Min(values []float64) float64 {
	if len(values) == 0 {
		return 0.0
	}
	min := values[0]
	for _, v := range values[1:] {
		if v < min {
			min = v
		}
	}
	return min
}

// Max 返回最大值
func (d *DerivedMetricsCalculator) Max(values []float64) float64 {
	if len(values) == 0 {
		return 0.0
	}
	max := values[0]
	for _, v := range values[1:] {
		if v > max {
			max = v
		}
	}
	return max
}

// Sum 计算总和
func (d *DerivedMetricsCalculator) Sum(values []float64) float64 {
	var sum float64
	for _, v := range values {
		sum += v
	}
	return sum
}

// StandardDeviation 计算标准差
func (d *DerivedMetricsCalculator) StandardDeviation(values []float64) float64 {
	if len(values) == 0 {
		return 0.0
	}

	// 计算平均值
	mean := d.Sum(values) / float64(len(values))

	// 计算方差
	var variance float64
	for _, v := range values {
		diff := v - mean
		variance += diff * diff
	}
	variance /= float64(len(values))

	// 返回标准差
	return sqrt(variance)
}

// 简单的平方根实现（避免依赖math包）
func sqrt(x float64) float64 {
	if x == 0 {
		return 0
	}
	z := x
	for i := 0; i < 10; i++ {
		z = (z + x/z) / 2
	}
	return z
}
