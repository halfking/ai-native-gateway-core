package autoroute

// ScoreSimplified 实现简化的 2 维评分公式：
// FinalScore = IntentMatchScore * 0.6 + PriceScore * 0.4
//
// 这是新的评分逻辑，用于替代现有的 8 维评分。
// 通过 Feature Flag 控制是否启用。
//
// 评分维度：
//   1. IntentMatchScore (0-100): 任务类型与模型 Tags 的匹配度
//   2. PriceScore (0-100): 基于该 canonical 所有有效凭据的平均成本
//   3. CorrectionScore (-10 ~ +10): 基于上次任务结果的小幅校正
//
// 最终得分：IntentMatch * 0.6 + Price * 0.4 + Correction
func ScoreSimplified(c Candidate, task TaskType, avgPriceByCanonical map[int]float64, correctionScore float64) ScoringBreakdown {
	// 1. 意图匹配分（0-100）
	// 已在 Index.Recommend 中预计算 c.TaskMatchScore (0-1)
	intentMatch := c.TaskMatchScore * 100

	// 2. 价格分（0-100）
	// 使用该 canonical 的平均价格，而非单个 credential 的价格
	avgCost := avgPriceByCanonical[c.CanonicalID]
	if avgCost == 0 {
		// 如果没有价格数据，回退到单个 credential 的价格
		avgCost = c.UnitPriceInPer1M + c.UnitPriceOutPer1M
	}

	// 价格分公式：1000 - avgCost，然后归一化到 0-100
	// 假设 avgCost 范围在 0-1000，则：
	// - avgCost = 0 (免费) → priceScore = 100
	// - avgCost = 500 → priceScore = 50
	// - avgCost = 1000 → priceScore = 0
	// - avgCost > 1000 → priceScore = 0 (下限保护)
	priceScore := (1000 - avgCost) / 10.0
	if priceScore < 0 {
		priceScore = 0
	}
	if priceScore > 100 {
		priceScore = 100
	}

	// 3. 校正分（-10 ~ +10）
	// 由调用方传入，基于上次任务结果
	correction := correctionScore
	if correction < -10 {
		correction = -10
	}
	if correction > 10 {
		correction = 10
	}

	// 4. 最终得分
	composite := intentMatch*0.6 + priceScore*0.4 + correction

	return ScoringBreakdown{
		MatchScore: intentMatch,
		PriceScore: priceScore,
		Composite:  composite,
		// 其余维度保持为 0，保持结构兼容
		SpeedScore:     0,
		StabilityScore: 0,
		PressureScore:  0,
		ContextFit:     0,
		VersionRecency: 0,
		StrengthMatch:  0,
	}
}

// ComputeAvgPriceByCanonical 计算每个 canonical model 的平均价格
// 只统计可用的 credentials（UnavailableReason == ""）
//
// 返回 map[canonical_id]avg_price
func ComputeAvgPriceByCanonical(candidates []Candidate) map[int]float64 {
	type priceAcc struct {
		sum   float64
		count int
	}

	acc := make(map[int]*priceAcc)

	for _, c := range candidates {
		if c.UnavailableReason != "" {
			continue // 跳过不可用的
		}

		price := c.UnitPriceInPer1M + c.UnitPriceOutPer1M
		if _, ok := acc[c.CanonicalID]; !ok {
			acc[c.CanonicalID] = &priceAcc{}
		}
		acc[c.CanonicalID].sum += price
		acc[c.CanonicalID].count++
	}

	result := make(map[int]float64)
	for canonID, a := range acc {
		if a.count > 0 {
			result[canonID] = a.sum / float64(a.count)
		}
	}

	return result
}

// ComputeCorrectionScore 计算会话校正分
//
// 基于上次任务结果：
//   - 上次成功且快速 → +5
//   - 上次失败 → -10
//   - 任务类型变化 → 0
//   - 无历史记录 → 0
//
// 参数：
//   - lastTask: 上次任务类型
//   - lastModel: 上次选择的模型 (canonical_name)
//   - lastSuccess: 上次是否成功
//   - lastLatencyMs: 上次延迟（毫秒）
//   - currentTask: 当前任务类型
//   - currentModel: 当前候选模型 (canonical_name)
//
// 返回：-10 ~ +10 的校正分
func ComputeCorrectionScore(
	lastTask TaskType,
	lastModel string,
	lastSuccess bool,
	lastLatencyMs int,
	currentTask TaskType,
	currentModel string,
) float64 {
	// 只对同一模型应用校正
	if lastModel != currentModel {
		return 0
	}

	// 任务类型变化 → 校正归零
	if lastTask != currentTask {
		return 0
	}

	// 上次失败 → 降权
	if !lastSuccess {
		return -10
	}

	// 上次成功且快速（< 2 秒）→ 小幅加分
	if lastLatencyMs > 0 && lastLatencyMs < 2000 {
		return 5
	}

	// 上次成功但较慢 → 不加分
	return 0
}
