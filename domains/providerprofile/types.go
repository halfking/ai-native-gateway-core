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
