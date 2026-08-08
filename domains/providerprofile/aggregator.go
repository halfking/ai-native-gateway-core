package providerprofile

import (
	"context"
	"fmt"
	"math"
	"time"
)

// Aggregator 聚合器接口
type Aggregator interface {
	// AggregateDailyProfiles 聚合每日画像
	AggregateDailyProfiles(ctx context.Context, date time.Time) error
}

// DailyAggregator 每日聚合器
// 职责：从小时级指标计算天级画像和评分
type DailyAggregator struct {
	metricsStore     MetricsStore
	profileStore     ProfileStore
	scorer           Scorer
	weights          ProfileWeights
	credentialLister CredentialLister
}

// NewDailyAggregator 创建每日聚合器
func NewDailyAggregator(
	metricsStore MetricsStore,
	profileStore ProfileStore,
	scorer Scorer,
	weights ProfileWeights,
	credentialLister CredentialLister,
) *DailyAggregator {
	return &DailyAggregator{
		metricsStore:     metricsStore,
		profileStore:     profileStore,
		scorer:           scorer,
		weights:          weights,
		credentialLister: credentialLister,
	}
}

// AggregateDailyProfiles 聚合指定日期的每日画像
func (a *DailyAggregator) AggregateDailyProfiles(ctx context.Context, date time.Time) error {
	// 1. 获取所有活跃凭证
	credentialIDs, err := a.credentialLister.ListActiveCredentials(ctx)
	if err != nil {
		return fmt.Errorf("list active credentials: %w", err)
	}

	// 2. 对每个凭证聚合画像
	var aggregateErrors []error
	for _, credID := range credentialIDs {
		if err := a.aggregateForCredential(ctx, credID, date); err != nil {
			aggregateErrors = append(aggregateErrors, fmt.Errorf("credential %d: %w", credID, err))
		}
	}

	if len(aggregateErrors) > 0 {
		return fmt.Errorf("aggregated with %d errors: first error: %w", len(aggregateErrors), aggregateErrors[0])
	}

	return nil
}

// aggregateForCredential 聚合单个凭证的每日画像
func (a *DailyAggregator) aggregateForCredential(ctx context.Context, credentialID int64, date time.Time) error {
	// 1. 获取指定日期的所有快照（00:00 到 23:59:59）
	startOfDay := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, date.Location())
	endOfDay := startOfDay.Add(24 * time.Hour)

	snapshots, err := a.metricsStore.GetSnapshotsByDateRange(ctx, credentialID, startOfDay, endOfDay)
	if err != nil {
		return fmt.Errorf("get snapshots: %w", err)
	}

	if len(snapshots) == 0 {
		return nil // 当天无数据，跳过
	}

	// 2026-08-07: 依据当日 snapshot 序列反推"综合分序列"，计算当日 CV 并
	// 写回到最后一个 snapshot，供后续 scorer 在 calculateQualityStabilityScore
	// 中读取。这个过程无需 schema 迁移（信号留在内存对象里），每日聚合时
	// 覆写一次。
	a.stampQualityStability(snapshots)

	// 2. 计算各维度分数
	dimensionScores := a.scorer.CalculateDimensionScores(snapshots, a.weights)

	// 3. 计算总分
	totalScore := a.scorer.CalculateTotalScore(dimensionScores, a.weights)

	// 4. 计算时段分析
	timeslotScores := a.calculateTimeslotScores(snapshots)
	scoreStddev := a.calculateScoreStddev(timeslotScores)
	bestTimeslot, worstTimeslot := a.findBestWorstTimeslots(timeslotScores)

	// 5. 构建原始统计数据
	rawStats := a.buildRawStats(snapshots)

	// 6. 获取 providerID（从第一个快照）
	providerID := snapshots[0].ProviderID

	// 7. 创建画像
	profile := &DailyProfile{
		CredentialID:      credentialID,
		ProviderID:        providerID,
		ProfileDate:       startOfDay,
		NetworkScore:      dimensionScores.NetworkScore,
		CredibilityScore:  dimensionScores.CredibilityScore,
		AvailabilityScore: dimensionScores.AvailabilityScore,
		StabilityScore:    dimensionScores.StabilityScore,
		ScaleScore:        dimensionScores.ScaleScore,
		CostAccuracyScore: dimensionScores.CostAccuracyScore,
		PriceScore:        dimensionScores.PriceScore,
		TotalScore:        totalScore,
		TimeslotScores:    timeslotScores,
		ScoreStddev:       scoreStddev,
		BestTimeslot:      bestTimeslot,
		WorstTimeslot:     worstTimeslot,
		RawStats:          rawStats,
		CreatedAt:         time.Now(),
	}

	// 8. 保存画像
	if err := a.profileStore.SaveDailyProfile(ctx, profile); err != nil {
		return fmt.Errorf("save daily profile: %w", err)
	}

	return nil
}

// calculateTimeslotScores 计算各时段的总分
func (a *DailyAggregator) calculateTimeslotScores(snapshots []*MetricSnapshot) map[TimeSlot]float64 {
	// 按时段分组
	timeslotSnapshots := make(map[TimeSlot][]*MetricSnapshot)
	for _, snap := range snapshots {
		timeslotSnapshots[snap.TimeSlot] = append(timeslotSnapshots[snap.TimeSlot], snap)
	}

	// 计算每个时段的分数
	timeslotScores := make(map[TimeSlot]float64)
	for slot, snaps := range timeslotSnapshots {
		scores := a.scorer.CalculateDimensionScores(snaps, a.weights)
		timeslotScores[slot] = a.scorer.CalculateTotalScore(scores, a.weights)
	}

	return timeslotScores
}

// calculateScoreStddev 计算分数标准差
func (a *DailyAggregator) calculateScoreStddev(timeslotScores map[TimeSlot]float64) float64 {
	if len(timeslotScores) == 0 {
		return 0
	}

	// 计算平均值
	var sum float64
	for _, score := range timeslotScores {
		sum += score
	}
	mean := sum / float64(len(timeslotScores))

	// 计算方差
	var variance float64
	for _, score := range timeslotScores {
		diff := score - mean
		variance += diff * diff
	}
	variance /= float64(len(timeslotScores))

	// 返回标准差
	return math.Sqrt(variance)
}

// findBestWorstTimeslots 找出最好和最差的时段
func (a *DailyAggregator) findBestWorstTimeslots(timeslotScores map[TimeSlot]float64) (TimeSlot, TimeSlot) {
	if len(timeslotScores) == 0 {
		return "", ""
	}

	var bestSlot, worstSlot TimeSlot
	bestScore := -1.0
	worstScore := 101.0

	for slot, score := range timeslotScores {
		if score > bestScore {
			bestScore = score
			bestSlot = slot
		}
		if score < worstScore {
			worstScore = score
			worstSlot = slot
		}
	}

	return bestSlot, worstSlot
}

// buildRawStats 构建原始统计数据
func (a *DailyAggregator) buildRawStats(snapshots []*MetricSnapshot) map[string]interface{} {
	totalRequests := 0
	successRequests := 0
	totalErrors := 0

	for _, snap := range snapshots {
		if snap.AvailabilityMetrics != nil {
			totalRequests += snap.AvailabilityMetrics.TotalRequests
			successRequests += snap.AvailabilityMetrics.SuccessRequests
		}
		if snap.StabilityMetrics != nil {
			totalErrors += snap.StabilityMetrics.ErrorCount
		}
	}

	var successRate float64
	if totalRequests > 0 {
		successRate = float64(successRequests) / float64(totalRequests) * 100
	}

	stats := map[string]interface{}{
		"snapshot_count":   len(snapshots),
		"total_requests":   totalRequests,
		"success_requests": successRequests,
		"total_errors":     totalErrors,
		"success_rate":     successRate,
	}

	// 2026-08-07: 把当日 QualityStabilitySignal 也写入 raw_stats，便于
	// 排查和历史回溯；前端不直接消费，但 debug 接口能用到。
	for _, snap := range snapshots {
		if snap != nil && snap.QualityStabilitySignal != nil {
			s := snap.QualityStabilitySignal
			stats["quality_stability"] = map[string]interface{}{
				"mean":        s.Mean,
				"stddev":      s.Stddev,
				"cv":          s.CV,
				"is_volatile": s.IsVolatile,
				"sample_n":    s.SampleN,
			}
			break
		}
	}

	return stats
}

// stampQualityStability 在聚合时计算当日综合分序列的均值/标准差/CV，
// 写到最后一个 snapshot.QualityStabilitySignal。
//
// 算法：复用 DefaultScorer 的 4 维计算（不引入新维度依赖，避免循环调用），
// 对每个 snapshot 算一次当日总评分估计，得到一个 len(snapshots) 的小序列。
// CV>0.10 标记 IsVolatile，scorer 据此扣分。
func (a *DailyAggregator) stampQualityStability(snapshots []*MetricSnapshot) {
	if len(snapshots) == 0 {
		return
	}
	scores := calculateDailySnapshotScores(snapshots)
	mean, stddev, cv := meanStddevCV(scores)
	sig := &QualityStabilitySignal{
		Mean:       mean,
		Stddev:     stddev,
		CV:         cv,
		IsVolatile: cv > 0.10,
		SampleN:    len(scores),
	}
	// 只在最后一个 snapshot 上写，前面的不必携带（scorer 用最新一条）。
	snapshots[len(snapshots)-1].QualityStabilitySignal = sig
}
