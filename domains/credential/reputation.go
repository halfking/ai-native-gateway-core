// Package credential - Provider reputation time-series analysis.
//
// Tracks reliability, latency, and error-rate trends per (provider, model),
// detects anomalies (error-rate / latency spikes), and persists incidents
// for post-mortem analysis. The data flow is:
//
//	BanditScorer (per-request) → ReputationWorker (daily aggregate)
//	                          → ReputationStore (Postgres)
//	                          → ReputationAnalyzer (on-demand)
//	                          → IncidentTracker (anomaly → incident)
package credential

import "time"

// IncidentType 事件类型
type IncidentType string

const (
	IncidentRateLimitSpike      IncidentType = "rate_limit_spike"
	IncidentOutage              IncidentType = "outage"
	IncidentAuthFailure         IncidentType = "auth_failure"
	IncidentDegradedPerformance IncidentType = "degraded_performance"
	IncidentLatencySpike        IncidentType = "latency_spike"
	IncidentErrorRateSpike      IncidentType = "error_rate_spike"
)

// ImpactLevel 影响级别
type ImpactLevel string

const (
	ImpactLow      ImpactLevel = "low"
	ImpactMedium   ImpactLevel = "medium"
	ImpactHigh     ImpactLevel = "high"
	ImpactCritical ImpactLevel = "critical"
)

// AnomalyType 异常类型（与 IncidentType 部分重叠，但更聚焦于检测信号）
type AnomalyType string

const (
	AnomalyErrorRateSpike AnomalyType = "error_rate_spike"
	AnomalyLatencySpike   AnomalyType = "latency_spike"
	AnomalySuccessDrop    AnomalyType = "success_drop"
)

// DailyMetric 每日指标
type DailyMetric struct {
	Date  time.Time
	Value float64
}

// Anomaly 检测到的异常
type Anomaly struct {
	Type      AnomalyType
	Date      time.Time
	Severity  ImpactLevel
	Value     float64
	Threshold float64
	Message   string
}

// Incident 供应商事件（故障、降级等）
type Incident struct {
	ID               int64
	ProviderID       string
	Model            string // 空字符串表示影响所有模型
	Type             IncidentType
	Impact           ImpactLevel
	Description      string
	StartedAt        time.Time
	EndedAt          *time.Time
	Duration         time.Duration // 计算字段：EndedAt - StartedAt
	DurationSeconds  int
	AffectedRequests int64
	AffectedTenants  int
	Resolved         bool
	ResolutionNotes  string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// ResolvedDuration 返回事件持续时间；若未结束则返回至今的时长
func (i *Incident) ResolvedDuration() time.Duration {
	if i == nil {
		return 0
	}
	if i.EndedAt != nil {
		return i.EndedAt.Sub(i.StartedAt)
	}
	return time.Since(i.StartedAt)
}

// ProviderReputation 供应商信誉（时序聚合视图）
type ProviderReputation struct {
	ProviderID string
	Model      string

	// 时序指标（默认 7 天滑动窗口，按需扩展到 30 天）
	ReliabilityTrend []DailyMetric
	LatencyTrend     []DailyMetric
	ErrorRateTrend   []DailyMetric

	// 长期指标
	LongTermReliability float64
	StabilityScore      float64 // [0, 1]；标准差越大分越低
	AverageLatencyMs    float64
	AverageErrorRate    float64
	TotalRequests       int64
	TotalSuccesses      int64

	// 事件追踪
	RecentIncidents  []Incident
	UptimePercentage float64 // [0, 1]
	UnresolvedCount  int

	LastUpdated time.Time
}

// IsEmpty 判断信誉数据是否为空
func (r *ProviderReputation) IsEmpty() bool {
	return r == nil || (len(r.ReliabilityTrend) == 0 && len(r.RecentIncidents) == 0)
}

// AnomalyThresholds 异常检测阈值（可由调用方覆盖）
type AnomalyThresholds struct {
	ErrorRateJumpAbove float64 // 当前错误率超过此值视为飙升
	ErrorRatePrevBelow float64 // 前一天错误率低于此值视为基线正常
	LatencyMinMs       float64 // 仅当延迟绝对值 > 此值才视为飙升
	LatencyMultiplier  float64 // 延迟超过前一天的 N 倍视为飙升
	SuccessDropBelow   float64 // 成功率跌至此值以下视为骤降
}

// DefaultAnomalyThresholds 返回默认阈值（基于任务文档）
func DefaultAnomalyThresholds() AnomalyThresholds {
	return AnomalyThresholds{
		ErrorRateJumpAbove: 0.30,
		ErrorRatePrevBelow: 0.10,
		LatencyMinMs:       1000,
		LatencyMultiplier:  2.0,
		SuccessDropBelow:   0.80,
	}
}
