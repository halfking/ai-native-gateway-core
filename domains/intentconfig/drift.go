package intentconfig

import (
	"math"
)

// calculateIntentDrift 计算意图漂移分数（基于KL散度）
func calculateIntentDrift(history []IntentEvolution, currentCandidates []IntentCandidate) float64 {
	if len(history) == 0 {
		return 0.0 // 没有历史，无漂移
	}

	// 构建历史意图分布（最近3轮）
	historyDist := buildHistoryDistribution(history, 3)

	// 构建当前意图分布
	currentDist := buildCurrentDistribution(currentCandidates)

	// 计算KL散度
	return klDivergence(historyDist, currentDist)
}

// buildHistoryDistribution 构建历史意图分布（最近N轮加权平均）
func buildHistoryDistribution(history []IntentEvolution, windowSize int) map[string]float64 {
	dist := make(map[string]float64)

	// 限制窗口大小
	if windowSize > len(history) {
		windowSize = len(history)
	}

	// 最近的轮次权重更高（指数衰减）
	totalWeight := 0.0
	for i := 0; i < windowSize; i++ {
		weight := math.Exp(-float64(i) * 0.3) // 衰减系数0.3
		totalWeight += weight

		evo := history[i]
		// 累加主意图的加权置信度
		dist[evo.PrimaryIntent] += evo.PrimaryConfidence * weight

		// 也考虑候选意图（权重减半）
		for _, candidate := range evo.IntentCandidates {
			if string(candidate.Kind) != evo.PrimaryIntent {
				dist[string(candidate.Kind)] += candidate.Confidence * weight * 0.5
			}
		}
	}

	// 归一化
	if totalWeight > 0 {
		for k := range dist {
			dist[k] = dist[k] / totalWeight
		}
	}

	return dist
}

// buildCurrentDistribution 构建当前意图分布
func buildCurrentDistribution(candidates []IntentCandidate) map[string]float64 {
	dist := make(map[string]float64)

	// 直接使用候选置信度（已归一化）
	for _, candidate := range candidates {
		dist[string(candidate.Kind)] = candidate.Confidence
	}

	return dist
}

// klDivergence 计算KL散度 D(P||Q) = Σ P(x) * log(P(x)/Q(x))
// P: 历史分布, Q: 当前分布
// 返回值范围：0（完全相同）到正无穷（完全不同），实际中通常<2
func klDivergence(p, q map[string]float64) float64 {
	// 合并所有出现过的意图类型
	allIntents := make(map[string]bool)
	for k := range p {
		allIntents[k] = true
	}
	for k := range q {
		allIntents[k] = true
	}

	kl := 0.0
	epsilon := 1e-10 // 平滑因子，避免除零或log(0)

	for intent := range allIntents {
		pVal := p[intent]
		qVal := q[intent]

		// 平滑处理
		if pVal < epsilon {
			pVal = epsilon
		}
		if qVal < epsilon {
			qVal = epsilon
		}

		// KL散度公式
		kl += pVal * math.Log(pVal/qVal)
	}

	// 归一化到0-1范围（使用sigmoid函数）
	// KL散度通常<2，映射到[0,1]
	normalized := 1.0 - math.Exp(-kl)

	return math.Max(0, math.Min(1, normalized))
}

// detectIntentShift 检测意图切换（突变 vs 渐变）
func detectIntentShift(history []IntentEvolution, currentIntent string) (isShift bool, shiftType string) {
	if len(history) == 0 {
		return false, "no_history"
	}

	// 检查最近一轮是否切换
	lastIntent := history[0].PrimaryIntent
	if lastIntent != currentIntent {
		// 检查是否持续切换（突变）还是来回摇摆（渐变）
		if len(history) >= 3 {
			// 统计最近3轮的意图分布
			intentCounts := make(map[string]int)
			for i := 0; i < 3 && i < len(history); i++ {
				intentCounts[history[i].PrimaryIntent]++
			}

			if len(intentCounts) >= 3 {
				return true, "oscillating" // 来回摇摆
			}
			return true, "sudden" // 突然切换
		}

		return true, "sudden"
	}

	return false, "stable"
}

// calculateIntentStability 计算意图稳定性（0-1，越高越稳定）
func calculateIntentStability(history []IntentEvolution, windowSize int) float64 {
	if len(history) < 2 {
		return 1.0 // 没有足够历史，假设稳定
	}

	if windowSize > len(history) {
		windowSize = len(history)
	}

	// 统计窗口内意图切换次数
	switchCount := 0
	for i := 1; i < windowSize; i++ {
		if history[i-1].PrimaryIntent != history[i].PrimaryIntent {
			switchCount++
		}
	}

	// 稳定性 = 1 - (切换次数 / 最大可能切换次数)
	maxSwitches := windowSize - 1
	if maxSwitches == 0 {
		return 1.0
	}

	stability := 1.0 - float64(switchCount)/float64(maxSwitches)
	return math.Max(0, math.Min(1, stability))
}

// predictNextIntent 预测下一轮最可能的意图（基于历史趋势）
func predictNextIntent(history []IntentEvolution) (string, float64) {
	if len(history) == 0 {
		return string(IntentUnclassified), 0.0
	}

	// 简单实现：使用最近的主意图作为预测
	// 未来可以使用马尔可夫链或LSTM等高级方法
	mostRecent := history[0].PrimaryIntent
	confidence := history[0].PrimaryConfidence

	// 如果最近3轮意图一致，提高预测置信度
	if len(history) >= 3 {
		if history[0].PrimaryIntent == history[1].PrimaryIntent &&
			history[1].PrimaryIntent == history[2].PrimaryIntent {
			confidence = math.Min(1.0, confidence*1.2)
		}
	}

	return mostRecent, confidence
}

// calculateIntentTrend 计算意图趋势（上升/下降/稳定）
func calculateIntentTrend(history []IntentEvolution, targetIntent string) string {
	if len(history) < 3 {
		return "insufficient_data"
	}

	// 统计最近5轮中目标意图的出现频率
	windowSize := 5
	if windowSize > len(history) {
		windowSize = len(history)
	}

	// 分别统计前半段和后半段
	mid := windowSize / 2
	earlyCount := 0
	lateCount := 0

	for i := 0; i < mid; i++ {
		if history[windowSize-1-i].PrimaryIntent == targetIntent {
			earlyCount++
		}
	}

	for i := 0; i < windowSize-mid; i++ {
		if history[i].PrimaryIntent == targetIntent {
			lateCount++
		}
	}

	// 判断趋势
	if lateCount > earlyCount+1 {
		return "rising"
	} else if lateCount < earlyCount-1 {
		return "declining"
	} else {
		return "stable"
	}
}

// smoothIntentDistribution 平滑意图分布（移动平均）
func smoothIntentDistribution(history []IntentEvolution, windowSize int) map[string]float64 {
	if len(history) == 0 {
		return make(map[string]float64)
	}

	if windowSize > len(history) {
		windowSize = len(history)
	}

	smoothed := make(map[string]float64)
	totalWeight := 0.0

	// 加权移动平均（越近权重越高）
	for i := 0; i < windowSize; i++ {
		weight := float64(windowSize - i) // 线性衰减
		totalWeight += weight

		evo := history[i]
		smoothed[evo.PrimaryIntent] += evo.PrimaryConfidence * weight
	}

	// 归一化
	if totalWeight > 0 {
		for k := range smoothed {
			smoothed[k] = smoothed[k] / totalWeight
		}
	}

	return smoothed
}
