// Package providerprofile 实现供应商画像系统
// 通过7个维度评估供应商质量：网络延迟、模型可信度、可用性、稳定性、规模、费用准确性、价格
package providerprofile

import "time"

// ProfileWeights 维度权重配置
type ProfileWeights struct {
	Network      float64 // 0.10 - 网络延迟
	Credibility  float64 // 0.15 - 模型可信度
	Availability float64 // 0.20 - 可用性
	Stability    float64 // 0.20 - 稳定性
	Scale        float64 // 0.05 - 规模
	CostAccuracy float64 // 0.15 - 费用准确性
	Price        float64 // 0.15 - 价格
}

// DefaultWeights 返回默认权重配置
func DefaultWeights() ProfileWeights {
	return ProfileWeights{
		Network:      0.10,
		Credibility:  0.15,
		Availability: 0.20,
		Stability:    0.20,
		Scale:        0.05,
		CostAccuracy: 0.15,
		Price:        0.15,
	}
}

// TimeSlot 时段标识
type TimeSlot string

const (
	TimeSlotDawn      TimeSlot = "dawn"      // 00:00-06:00
	TimeSlotMorning   TimeSlot = "morning"   // 06:00-12:00
	TimeSlotAfternoon TimeSlot = "afternoon" // 12:00-18:00
	TimeSlotEvening   TimeSlot = "evening"   // 18:00-22:00
	TimeSlotNight     TimeSlot = "night"     // 22:00-24:00
)

// DetermineTimeSlot 根据时间确定时段
func DetermineTimeSlot(t time.Time) TimeSlot {
	hour := t.Hour()
	switch {
	case hour < 6:
		return TimeSlotDawn
	case hour < 12:
		return TimeSlotMorning
	case hour < 18:
		return TimeSlotAfternoon
	case hour < 22:
		return TimeSlotEvening
	default:
		return TimeSlotNight
	}
}

// MetricSnapshot 单次采集快照
type MetricSnapshot struct {
	CredentialID int64
	ProviderID   int64
	MetricTime   time.Time
	TimeSlot     TimeSlot

	NetworkMetrics      *NetworkMetrics
	AvailabilityMetrics *AvailabilityMetrics
	StabilityMetrics    *StabilityMetrics
	ScaleMetrics        *ScaleMetrics

	// 2026-08-07: extended quality signals. All optional; nil means "not
	// measured" and the scorer treats it as a missing dimension (excluded
	// from weighted total, per the existing if-score>0 contract in
	// scorer.go:CalculateTotalScore).
	RateLimitMetrics       *RateLimitMetrics       // 429/限流命中率
	ConcurrencyCapacity    *ConcurrencyCapacity    // 供应商并发承载能力
	AvailabilityWindow     *AvailabilityWindow     // 不可用窗口（连续低成功率段）
	QualityStabilitySignal *QualityStabilitySignal // 评分自身稳定性（综合分 CV）
	// 2026-08-11: 节点智商信号（来自 node_iq_latest）。nil = 未测量（冷启动），
	// scorer 跳过该维度。
	ModelIQSignal *ModelIQSignal
}

// ModelIQSignal 节点智商维度数据。
//
// 数据来源：node_iq_latest 按 credential 聚合其下所有节点的 overall_score
// 平均值（即该凭据所托管模型的平均实测智商，0-100）。SampleN=0 表示尚无任何
// 智商测试数据，应视为未测量。
type ModelIQSignal struct {
	AvgIQ   float64 // 节点 overall_score 平均值（0-100）
	SampleN int     // 参与聚合的节点数
}

// NetworkMetrics 网络延迟指标
type NetworkMetrics struct {
	P50 int // ms
	P95 int // ms
	P99 int // ms
}

// AvailabilityMetrics 可用性指标
type AvailabilityMetrics struct {
	TotalRequests   int
	SuccessRequests int
	AvgTTFTMs       int // 首字时间平均值
	AvgDurationMs   int // 完成时长平均值
}

// StabilityMetrics 稳定性指标
type StabilityMetrics struct {
	ErrorCount int
	ErrorTypes map[string]int // {"500": 3, "timeout": 2}
}

// ScaleMetrics 规模指标
type ScaleMetrics struct {
	TotalModels     int
	AvailableModels int
}

// DailyProfile 天级画像数据
type DailyProfile struct {
	ID           int64
	CredentialID int64
	ProviderID   int64
	ProfileDate  time.Time

	// 各维度分数
	NetworkScore      float64
	CredibilityScore  float64
	AvailabilityScore float64
	StabilityScore    float64
	ScaleScore        float64
	CostAccuracyScore float64
	PriceScore        float64
	TotalScore        float64

	// 时段分析
	TimeslotScores map[TimeSlot]float64
	ScoreStddev    float64
	BestTimeslot   TimeSlot
	WorstTimeslot  TimeSlot

	// 原始数据（JSONB）
	RawStats map[string]interface{}

	CreatedAt time.Time
}

// 2026-08-07: extended quality-signal structs. They live next to the
// original snapshot types so adapters, scorer, and aggregator can share
// the same vocabulary without an extra file hop.

// RateLimitMetrics 限流命中率（429）
//
// 数据来源：request_logs_hot.upstream_status_code，按 credential + 时间窗口
// 聚合得到。HitsRatio = RateLimitHits / TotalRequests（窗口内）。
type RateLimitMetrics struct {
	RateLimitHits int     // 窗口内 429 命中次数
	TotalRequests int     // 窗口内总请求（用于算 HitsRatio）
	HitsRatio     float64 // 命中率 0-1，TotalRequests=0 时为 0
}

// ConcurrencyCapacity 供应商并发承载能力
//
// 来自 credentials.concurrency_limit / concurrency_limit_auto：
//   - EffLimit  = auto 优先，但受 concurrency_limit 人工硬上限约束
//   - IsCapped  = (concurrency_limit_auto < concurrency_limit)
//     表示上游曾因 503 把自动上限压低过，是"被限流到降级"的关键证据。
//
// EffLimit=0 表示未配置并发上限，scorer 会返回中性分 60 并标记缺失维度。
type ConcurrencyCapacity struct {
	ConcurrencyLimit     int  // 用户配置的硬上限（credentials.concurrency_limit）
	ConcurrencyLimitAuto int  // 自动调优后的当前上限（credentials.concurrency_limit_auto）
	EffLimit             int  // 实际生效上限（auto 优先，且不超过 hard）
	IsCapped             bool // true=auto < hard，被自动降级过
}

// AvailabilityWindow 不可用窗口（连续低成功率段）
//
// 从最近 N 小时的 request_logs_hot 按 5 分钟桶聚合成功率得到。
// DowntimeBucket = 成功率 < 90% 的桶；LongestRun = 单次连续低成功率的最长桶数。
// 一个 5 分钟桶 ≈ 30 分钟连续不可用时 LongestRun=6。
type AvailabilityWindow struct {
	DowntimeBuckets int     // 窗口内成功率<90%的桶数
	TotalBuckets    int     // 窗口内总桶数（含成功+失败+空桶）
	DowntimeRatio   float64 // DowntimeBuckets/TotalBuckets（TotalBuckets=0 时为 0）
	LongestRun      int     // 单次最长连续低成功率桶数
}

// QualityStabilitySignal 评分自身的稳定性（综合分变异系数）
//
// 在聚合层用当日的 snapshot 序列反推综合分序列，再算 stddev/CV。CV<0.05
// 视为稳定，CV>0.20 视为剧烈抖动。样本不足时 CV=0，IsVolatile=false。
type QualityStabilitySignal struct {
	Mean       float64 // 综合分均值
	Stddev     float64 // 综合分标准差
	CV         float64 // 变异系数 Stddev/Mean（Mean=0 时为 0）
	IsVolatile bool    // CV > 0.10
	SampleN    int     // 参与计算的 snapshot 数（用于排除冷启动噪声）
}
