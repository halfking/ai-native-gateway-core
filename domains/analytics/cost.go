package analytics

import (
	"fmt"
	"math"
)

// CostCalculator 提供成本计算的统一接口
// 核心原则：禁止跨单位求和（USD + credits）
type CostCalculator struct{}

// NewCostCalculator 创建成本计算器实例
func NewCostCalculator() *CostCalculator {
	return &CostCalculator{}
}

// CostBreakdown 成本分解结构
type CostBreakdown struct {
	InternalCostUSD float64 // 内部成本（美元）
	ExternalCredits int64   // 外部成本（积分）
	HasInternalCost bool    // 是否有内部成本
	HasExternalCost bool    // 是否有外部成本
}

// CalculateInternalCost 计算内部成本（返回 USD）
// 输入：成本分值（cents）
// 输出：美元金额
func (c *CostCalculator) CalculateInternalCost(costCents int64) float64 {
	return float64(costCents) / 100.0
}

// CalculateExternalCost 返回外部成本（积分）
// 输入：积分数
// 输出：积分数（原样返回，用于类型安全）
func (c *CostCalculator) CalculateExternalCost(credits int64) int64 {
	return credits
}

// SplitCost 将成本分解为内部和外部
func (c *CostCalculator) SplitCost(internalCents int64, externalCredits int64) CostBreakdown {
	return CostBreakdown{
		InternalCostUSD: c.CalculateInternalCost(internalCents),
		ExternalCredits: externalCredits,
		HasInternalCost: internalCents > 0,
		HasExternalCost: externalCredits > 0,
	}
}

// DistributeTailDifference 尾差分配
// 将总成本按各组比例分配，确保总和等于 totalCents
// 尾差分配给最大的组
func (c *CostCalculator) DistributeTailDifference(totalCents int64, groups map[string]int64) map[string]int64 {
	if len(groups) == 0 {
		return make(map[string]int64)
	}

	// 计算总请求数
	var totalRequests int64
	for _, count := range groups {
		totalRequests += count
	}

	if totalRequests == 0 {
		return make(map[string]int64)
	}

	// 按比例分配（向下取整）
	result := make(map[string]int64)
	var allocated int64
	var maxGroup string
	var maxCount int64

	for group, count := range groups {
		// 按比例计算应分配的成本
		proportion := float64(count) / float64(totalRequests)
		groupCost := int64(math.Floor(proportion * float64(totalCents)))

		result[group] = groupCost
		allocated += groupCost

		// 记录最大的组
		if count > maxCount {
			maxCount = count
			maxGroup = group
		}
	}

	// 将尾差分配给最大的组
	if maxGroup != "" {
		tailDiff := totalCents - allocated
		result[maxGroup] += tailDiff
	}

	return result
}

// FormatInternalCost 格式化内部成本为货币字符串
func (c *CostCalculator) FormatInternalCost(costUSD float64) string {
	return fmt.Sprintf("$%.2f", costUSD)
}

// FormatExternalCost 格式化外部成本为积分字符串
func (c *CostCalculator) FormatExternalCost(credits int64) string {
	return fmt.Sprintf("%d 积分", credits)
}

// ValidateCostUnit 验证成本单位是否纯净
// 返回 error 如果检测到跨单位操作
func (c *CostCalculator) ValidateCostUnit(hasInternal, hasExternal bool) error {
	if hasInternal && hasExternal {
		return fmt.Errorf("prohibited cross-unit cost aggregation: cannot combine USD and credits")
	}
	return nil
}

// ⚠️ 以下方法被标记为 DEPRECATED，禁止使用

// CalculateTotalCost 计算总成本
// ⚠️ DEPRECATED: 此方法已废弃，禁止跨单位求和
// 使用 SplitCost 代替，分别处理内部和外部成本
// 此方法保留仅用于向后兼容，但会返回错误
func (c *CostCalculator) CalculateTotalCost(internalCents int64, externalCredits int64) (float64, error) {
	return 0, fmt.Errorf("CalculateTotalCost is deprecated: cross-unit aggregation (USD + credits) is prohibited. Use SplitCost instead")
}
