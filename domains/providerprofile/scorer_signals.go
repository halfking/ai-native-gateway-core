package providerprofile

// scorer_signals.go — extended quality signal calculators
//
// 2026-08-07 audit fix: the original DefaultScorer only considered four
// dimensions (network/availability/stability/scale) and never looked at
// rate-limit hits, concurrency ceilings, downtime windows, or score
// stability. The four helpers below add those signals. Each one reads
// only the corresponding field on MetricSnapshot, so an adapter that
// has not been wired yet will simply produce nil and the existing
// "missing dimension" path in CalculateTotalScore (scorer.go:54-94)
// will skip it.

import "math"

// ExtendedWeights 评分权重（含四个新增维度）。
//
// 总和必须为 1.0。各维度按"缺失不计权"策略（scorer.go 中 if score>0 才
// 计入加权），所以冷启动/旧版快照不会拉低总分。
//
// 权重分配原则：
//   - 不可用窗口 0.20：用户明确"数据不太正确，没有考虑并发限制和稳定性"，把
//     "不可用"作为头号权重。
//   - 质量稳定性 0.16：综合分 CV 高表示供应商表现抖动严重，是路由应该避开的
//     关键信号。
//   - 限流命中率 0.10：429 频繁的供应商会拖累路由，应该在评分中体现。
//   - 并发承载 0.10：限制过小或被自动降级（IsCapped）都说明容量不足。
//   - 原 4 维权重按比例下调到 0.44（0.08+0.16+0.16+0.04）；新 4 维占
//     0.56（0.20+0.16+0.10+0.10）；总和=1.0。
type ExtendedWeights struct {
	Network            float64
	Availability       float64
	Stability          float64
	Scale              float64
	RateLimit          float64
	Concurrency        float64
	AvailabilityWindow float64
	QualityStability   float64
	Credibility        float64
	CostAccuracy       float64
	Price              float64
}

// DefaultExtendedWeights 返回默认扩展权重（总和=1.0）。
func DefaultExtendedWeights() ExtendedWeights {
	return ExtendedWeights{
		Network:            0.08,
		Availability:       0.16,
		Stability:          0.16,
		Scale:              0.04,
		RateLimit:          0.10,
		Concurrency:        0.10,
		AvailabilityWindow: 0.20,
		QualityStability:   0.16,
		// 暂未实现维度，给 0 权重（如果未来填充值，权重需重新分配）
		Credibility:  0,
		CostAccuracy: 0,
		Price:        0,
	}
}

// BackwardCompatWeights 仅老 4 维有值时使用的兼容权重。
//
// 当所有新维度都是 nil（冷启动或旧版快照），回退到原 DefaultWeights 的归一化
// 版本，保证总分差异 < 0.5 分（与 scorer_test 的 BackwardsCompat 测试对应）。
func BackwardCompatWeights() ExtendedWeights {
	return ExtendedWeights{
		Network:            0.10 / 0.55,
		Availability:       0.20 / 0.55,
		Stability:          0.20 / 0.55,
		Scale:              0.05 / 0.55,
		RateLimit:          0,
		Concurrency:        0,
		AvailabilityWindow: 0,
		QualityStability:   0,
		Credibility:        0,
		CostAccuracy:       0,
		Price:              0,
	}
}

// calculateRateLimitScore 计算限流命中率得分（0-100）。
//
// 阈值（命中率 HitsRatio，0-1）：
//   - 0%      → 100 分（完全无 429）
//   - 0.5%    →  70 分
//   - 1%      →  40 分
//   - >5%     →   0 分（频繁被限流，应该被路由规避）
//
// 总请求为 0 时返回中性 100（不算坏）。
func calculateRateLimitScore(snapshots []*MetricSnapshot) float64 {
	if len(snapshots) == 0 {
		return 0
	}

	var totalHits, totalReq int
	anyPresent := false
	for _, snap := range snapshots {
		if snap == nil || snap.RateLimitMetrics == nil {
			continue
		}
		anyPresent = true
		totalHits += snap.RateLimitMetrics.RateLimitHits
		totalReq += snap.RateLimitMetrics.TotalRequests
	}
	if !anyPresent {
		return 0 // 标记缺失维度
	}
	if totalReq == 0 {
		return 100 // 没有流量就谈不上限流
	}

	ratio := float64(totalHits) / float64(totalReq)

	switch {
	case ratio <= 0:
		return 100
	case ratio <= 0.005: // 0-0.5% 线性 100→70
		return 100 - (ratio/0.005)*30
	case ratio <= 0.01: // 0.5%-1% 线性 70→40
		return 70 - ((ratio-0.005)/0.005)*30
	case ratio <= 0.05: // 1%-5% 线性 40→0
		return 40 - ((ratio-0.01)/0.04)*40
	default:
		return 0
	}
}

// calculateConcurrencyScore 计算并发承载能力得分（0-100）。
//
// 逻辑：
//   - EffLimit=0 → 中性 60 分（缺失维度，会被 CalculateTotalScore 跳过加权）
//   - EffLimit=1 →  10 分（容量过低）
//   - EffLimit=10 → 50 分
//   - EffLimit>=50 → 100 分
//   - IsCapped=true 时再扣 15 分（曾被 503 自动降级说明实际承载不稳定）
func calculateConcurrencyScore(snapshots []*MetricSnapshot) float64 {
	if len(snapshots) == 0 {
		return 0
	}

	// 取最新一条记录的并发数据（容量是相对静态的属性）
	var latest *ConcurrencyCapacity
	for i := len(snapshots) - 1; i >= 0; i-- {
		if snapshots[i] != nil && snapshots[i].ConcurrencyCapacity != nil {
			latest = snapshots[i].ConcurrencyCapacity
			break
		}
	}
	if latest == nil {
		return 0 // 缺失维度
	}
	if latest.EffLimit == 0 {
		return 60 // 未配置 → 中性分，scorer 会按"缺失"处理
	}

	// 1-50 的分段线性
	var score float64
	switch {
	case latest.EffLimit <= 1:
		score = 10
	case latest.EffLimit <= 10:
		// 1→10, 10→50
		score = 10 + (float64(latest.EffLimit)-1)/9*40
	case latest.EffLimit <= 50:
		// 10→50, 50→100
		score = 50 + (float64(latest.EffLimit)-10)/40*50
	default:
		score = 100
	}

	if latest.IsCapped {
		score -= 15 // 被自动降级说明实际承载不稳定
	}
	if score < 0 {
		score = 0
	}
	return score
}

// calculateAvailabilityWindowScore 计算不可用窗口得分（0-100）。
//
// 输入来自 AvailabilityWindow：
//   - DowntimeRatio = DowntimeBuckets / TotalBuckets
//   - LongestRun    = 单次最长连续低成功率桶数（5 分钟桶）
//
// 评分逻辑：
//   - DowntimeRatio < 1%  → 100 分
//   - DowntimeRatio >= 20% → 0 分
//   - 中间 1%-20% 线性 100→0
//   - LongestRun 超过 6 桶（即 30 分钟连续不可用）再线性扣分：每多 1 桶
//     扣 2 分，至多扣 30 分。
//
// 例子：5% 不可用 + 短段 → 78.9 分；10% 不可用 + LongestRun=10 → 52.6-8=44.6；
// 20% 不可用 + 长段 → 0 分。
func calculateAvailabilityWindowScore(snapshots []*MetricSnapshot) float64 {
	if len(snapshots) == 0 {
		return 0
	}

	// 聚合所有 snapshot 的窗口数据
	var totalDowntime, totalBuckets, maxLongest int
	anyPresent := false
	for _, snap := range snapshots {
		if snap == nil || snap.AvailabilityWindow == nil {
			continue
		}
		anyPresent = true
		totalDowntime += snap.AvailabilityWindow.DowntimeBuckets
		totalBuckets += snap.AvailabilityWindow.TotalBuckets
		if snap.AvailabilityWindow.LongestRun > maxLongest {
			maxLongest = snap.AvailabilityWindow.LongestRun
		}
	}
	if !anyPresent {
		return 0 // 缺失维度
	}
	if totalBuckets == 0 {
		return 100 // 没有时间桶可判断，按满分
	}

	ratio := float64(totalDowntime) / float64(totalBuckets)

	var score float64
	switch {
	case ratio <= 0.01:
		score = 100
	case ratio <= 0.20:
		score = 100 - (ratio-0.01)/0.19*100
	default:
		score = 0
	}

	// LongestRun > 6 桶（30 分钟连续不可用）开始扣分。
	// 最长连续段越长，扣分越重，至多 30 分。
	if maxLongest > 6 {
		penalty := float64(maxLongest-6) * 2.0 // 每多 1 桶扣 2 分
		if penalty > 30 {
			penalty = 30
		}
		score -= penalty
	}
	if score < 0 {
		score = 0
	}
	return score
}

// calculateQualityStabilityScore 计算评分自身稳定性得分（0-100）。
//
// 从 QualityStabilitySignal 读 CV：
//   - CV ≤ 0.05  → 100 分（稳定）
//   - CV ≥ 0.20  →   0 分（剧烈抖动）
//   - 0.05-0.20  → 线性 100→0
//   - 样本数 < 2 → 中性 100（无足够数据时不扣分，避免冷启动噪声）
func calculateQualityStabilityScore(snapshots []*MetricSnapshot) float64 {
	if len(snapshots) == 0 {
		return 0
	}

	// 取最新一条作为当日综合稳定性（聚合层每日覆写一次）
	var latest *QualityStabilitySignal
	for i := len(snapshots) - 1; i >= 0; i-- {
		if snapshots[i] != nil && snapshots[i].QualityStabilitySignal != nil {
			latest = snapshots[i].QualityStabilitySignal
			break
		}
	}
	if latest == nil {
		return 0 // 缺失维度
	}
	if latest.SampleN < 2 {
		return 100 // 样本不足不扣分
	}

	cv := latest.CV
	switch {
	case cv <= 0.05:
		return 100
	case cv <= 0.20:
		return 100 - (cv-0.05)/0.15*100
	default:
		return 0
	}
}

// calculateDailySnapshotScores 辅助函数：根据单个 snapshot 推算"该采集时点
// 的综合分估计"。聚合层在算 QualityStabilitySignal 时复用。
//
// 复用 DefaultScorer 的现有 4 维计算（不做扩展加权），目的是提供一个数值上
// 稳定的"每日分"序列用于 CV 计算。
func calculateDailySnapshotScores(snapshots []*MetricSnapshot) []float64 {
	if len(snapshots) == 0 {
		return nil
	}
	out := make([]float64, 0, len(snapshots))
	s := &DefaultScorer{}
	w := DefaultWeights()
	for _, snap := range snapshots {
		if snap == nil {
			continue
		}
		scores := s.CalculateDimensionScores([]*MetricSnapshot{snap}, w)
		total := s.CalculateTotalScore(scores, w)
		out = append(out, total)
	}
	return out
}

// meanStddevCV 辅助函数：算均值、标准差和变异系数。n<=1 时返回 0/0/0。
func meanStddevCV(values []float64) (mean, stddev, cv float64) {
	n := len(values)
	if n == 0 {
		return 0, 0, 0
	}
	var sum float64
	for _, v := range values {
		sum += v
	}
	mean = sum / float64(n)
	if n < 2 {
		return mean, 0, 0
	}
	var sq float64
	for _, v := range values {
		d := v - mean
		sq += d * d
	}
	stddev = math.Sqrt(sq / float64(n))
	if mean > 0 {
		cv = stddev / mean
	}
	return mean, stddev, cv
}
