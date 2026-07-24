package executors

import (
	"math"
)

// calculatePressurePenalty 计算压力惩罚系数（0-1 之间）
// 压力越高，惩罚越重，返回值越大
//
// 参数:
//   - fpPressure: FpSlots 压力（0-1）
//   - limiterPressure: Limiter 压力（0-1）
//
// 返回:
//   - 惩罚系数（0-1），0 表示无惩罚，1 表示最大惩罚
//
// 惩罚策略:
//   - 压力 < 0.5: 无惩罚（0%）
//   - 压力 0.5-0.8: 线性惩罚（0-30%）
//   - 压力 > 0.8: 指数惩罚（30-70%）
//
// 设计理念:
//   - 低压力时不干预，保持 URSM v2 原始评分
//   - 中等压力时轻度惩罚，引导流量到其他节点
//   - 高压力时重度惩罚，避免资源耗尽
func calculatePressurePenalty(fpPressure, limiterPressure float64) float64 {
	// 取两者最大值（最严重的压力）
	maxPressure := math.Max(fpPressure, limiterPressure)

	// 压力上限为 1.0
	if maxPressure > 1.0 {
		maxPressure = 1.0
	}

	if maxPressure < 0.5 {
		// 低压力：无惩罚
		return 0
	} else if maxPressure < 0.8 {
		// 中压力：线性惩罚 0-30%
		// (maxPressure - 0.5) / 0.3 = 压力在 [0.5, 0.8] 区间的归一化位置
		// * 0.3 = 映射到 [0, 0.3] 的惩罚范围
		return (maxPressure - 0.5) / 0.3 * 0.3
	} else {
		// 高压力：指数惩罚 30-70%
		// excess = 超过 0.8 的部分（0-0.2）
		// excess / 0.2 = 归一化到 [0, 1]
		// * 0.4 = 映射到 [0, 0.4] 的额外惩罚
		// 0.3 + ... = 基础 30% + 额外惩罚，总计 30-70%
		excess := maxPressure - 0.8
		return 0.3 + excess/0.2*0.4
	}
}

// Pressure penalty lookup table (for documentation and testing)
//
// | Pressure | Penalty | Description                    |
// |----------|---------|--------------------------------|
// | 0.0      | 0%      | No pressure                    |
// | 0.3      | 0%      | Low pressure                   |
// | 0.5      | 0%      | Threshold, start penalty       |
// | 0.65     | 15%     | Medium pressure, light penalty |
// | 0.8      | 30%     | High pressure threshold        |
// | 0.9      | 50%     | High pressure, heavy penalty   |
// | 1.0      | 70%     | Saturated, max penalty         |
