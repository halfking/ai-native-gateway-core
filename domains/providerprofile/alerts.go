package providerprofile

import "time"

// AlertType 告警类型（对应设计文档 §8.2）
type AlertType string

const (
	AlertTypeScoreDrop    AlertType = "score_drop"    // 24h下降≥20 或 7d下降≥30
	AlertTypeTrendDrop    AlertType = "trend_drop"    // 连续3天下降累计≥15
	AlertTypeDimensionLow AlertType = "dimension_low" // 可用性/稳定性<60
	AlertTypeAutoDisabled AlertType = "auto_disabled" // 已自动禁用
	AlertTypeAutoEnabled  AlertType = "auto_enabled"  // 已自动恢复
)

// AlertLevel 告警级别。值的大小关系用于取最严重级别（critical>warning>info），
// 因此字符串值使用严重程度前缀（1/2/3），使其字典序与严重程度序一致。
type AlertLevel string

const (
	AlertLevelInfo     AlertLevel = "1_info"
	AlertLevelWarning  AlertLevel = "2_warning"
	AlertLevelCritical AlertLevel = "3_critical"
)

// AlertConfig 告警与自动处理阈值（设计文档 §8.1）
type AlertConfig struct {
	// 相对变化阈值
	ScoreDropThreshold24h float64 // 默认20
	ScoreDropThreshold7d  float64 // 默认30

	// 趋势告警阈值
	ContinuousDropDays int     // 默认3
	TrendDropTotal     float64 // 默认15

	// 自动禁用阈值
	AutoDisableThreshold      float64 // 默认40
	AutoDisableAvailThreshold float64 // 默认50（可用性立即触发）
	AutoDisableContinuousDays int     // 默认3

	// 自动恢复阈值
	AutoEnableThreshold      float64 // 默认70
	AutoEnableContinuousDays int     // 默认3
}

// DefaultAlertConfig 返回设计文档默认阈值
func DefaultAlertConfig() AlertConfig {
	return AlertConfig{
		ScoreDropThreshold24h:     20,
		ScoreDropThreshold7d:      30,
		ContinuousDropDays:        3,
		TrendDropTotal:            15,
		AutoDisableThreshold:      40,
		AutoDisableAvailThreshold: 50,
		AutoDisableContinuousDays: 3,
		AutoEnableThreshold:       70,
		AutoEnableContinuousDays:  3,
	}
}

// Alert 告警实体（映射到 provider_profile_alerts 表）
type Alert struct {
	ID            int64
	CredentialID  int64
	ProviderID    int64
	Type          AlertType
	Level         AlertLevel
	TriggerDate   time.Time
	CurrentScore  float64
	PreviousScore float64
	ScoreChange   float64
	Dimension     string
	Message       string
	Details       map[string]interface{}
	ActionTaken   string // disabled/enabled/degraded/none
}

// highestLevel 返回给定级别中最严重的一个
func highestLevel(levels ...AlertLevel) AlertLevel {
	best := AlertLevelInfo
	for _, l := range levels {
		if l > best {
			best = l
		}
	}
	return best
}

// isContiguousRecentDays 判断 profiles（按 ProfileDate 倒序，最新在前）是否从最新一天起
// 连续 N 天都满足 predicate。日期必须逐日递减（无空洞）。
func isContiguousRecentDays(profiles []DailyProfile, n int, pred func(DailyProfile) bool) bool {
	if len(profiles) < n {
		return false
	}
	for i := 0; i < n; i++ {
		// 相邻两条日期差应为 1 天（profiles[0]最新，profiles[1]应是前一天）
		if i > 0 {
			expected := profiles[i-1].ProfileDate.AddDate(0, 0, -1)
			if !profiles[i].ProfileDate.Equal(expected) {
				return false
			}
		}
		if !pred(profiles[i]) {
			return false
		}
	}
	return true
}
