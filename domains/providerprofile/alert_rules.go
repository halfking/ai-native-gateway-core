package providerprofile

import (
	"fmt"
	"time"
)

// EvaluateAlerts 在纯函数层评估所有告警规则，返回触发的告警列表（不含已采取动作）。
// profiles 必须按 ProfileDate 倒序排列（最新在前）。credentialID/providerID 用于填充告警实体。
//
// 规则（设计文档 §8.2 / §8.3）：
//  1. score_drop:   24h下降≥20 (warning) 或 7d下降≥30 (critical)
//  2. trend_drop:   连续3天下降，累计≥15 (critical)
//  3. dimension_low: 可用性或稳定性<60 (critical)
//  4. auto_disabled: 总分<40连续3天 或 可用性<50立即 (critical)
//  5. auto_enabled:  总分≥70连续3天 (info)
//
// NOTE: auto_enabled 由动作层（后续任务）根据 credential 当前是否 disabled 决定是否真正执行。
// 此函数在连续高分时也返回 auto_enabled 告警，由动作层过滤。
func EvaluateAlerts(profiles []DailyProfile, credentialID, providerID int64, cfg AlertConfig) []Alert {
	if len(profiles) == 0 {
		return nil
	}
	today := profiles[0]
	var alerts []Alert
	triggerDate := truncateDate(today.ProfileDate)

	// 1. score_drop
	if len(profiles) >= 2 {
		drop24 := profiles[1].TotalScore - today.TotalScore
		if drop24 >= cfg.ScoreDropThreshold7d {
			alerts = append(alerts, makeAlert(credentialID, providerID, AlertTypeScoreDrop,
				AlertLevelCritical, triggerDate, today.TotalScore, profiles[1].TotalScore, drop24,
				"", fmt.Sprintf("总分24小时内下降 %.1f 分", drop24), nil))
		} else if drop24 >= cfg.ScoreDropThreshold24h {
			alerts = append(alerts, makeAlert(credentialID, providerID, AlertTypeScoreDrop,
				AlertLevelWarning, triggerDate, today.TotalScore, profiles[1].TotalScore, drop24,
				"", fmt.Sprintf("总分24小时内下降 %.1f 分", drop24), nil))
		}
	}
	if len(profiles) >= 8 {
		drop7d := profiles[7].TotalScore - today.TotalScore
		if drop7d >= cfg.ScoreDropThreshold7d {
			alerts = append(alerts, makeAlert(credentialID, providerID, AlertTypeScoreDrop,
				AlertLevelCritical, triggerDate, today.TotalScore, profiles[7].TotalScore, drop7d,
				"", fmt.Sprintf("总分7天内下降 %.1f 分", drop7d), nil))
		}
	}

	// 2. trend_drop: 连续 N 天单调下降，累计≥TrendDropTotal
	if drops, total, ok := detectContinuousDrop(profiles, cfg.ContinuousDropDays); ok && total >= cfg.TrendDropTotal {
		alerts = append(alerts, makeAlert(credentialID, providerID, AlertTypeTrendDrop,
			AlertLevelCritical, triggerDate, today.TotalScore, today.TotalScore+total, -total,
			"", fmt.Sprintf("连续 %d 天下降，累计 %.1f 分", len(drops), total), map[string]interface{}{"daily_drops": drops}))
	}

	// 3. dimension_low
	if today.AvailabilityScore < 60 {
		alerts = append(alerts, makeAlert(credentialID, providerID, AlertTypeDimensionLow,
			AlertLevelCritical, triggerDate, today.TotalScore, 0, 0,
			"availability", fmt.Sprintf("可用性评分 %.1f 低于60", today.AvailabilityScore), nil))
	}
	if today.StabilityScore < 60 {
		alerts = append(alerts, makeAlert(credentialID, providerID, AlertTypeDimensionLow,
			AlertLevelCritical, triggerDate, today.TotalScore, 0, 0,
			"stability", fmt.Sprintf("稳定性评分 %.1f 低于60", today.StabilityScore), nil))
	}

	// 4. auto_disabled: 总分<40连续N天 或 可用性<50立即
	lowScoreDays := isContiguousRecentDays(profiles, cfg.AutoDisableContinuousDays, func(p DailyProfile) bool {
		return p.TotalScore < cfg.AutoDisableThreshold
	})
	if lowScoreDays {
		alerts = append(alerts, makeAlert(credentialID, providerID, AlertTypeAutoDisabled,
			AlertLevelCritical, triggerDate, today.TotalScore, 0, 0,
			"total_score", fmt.Sprintf("总分连续 %d 天低于 %.0f", cfg.AutoDisableContinuousDays, cfg.AutoDisableThreshold), nil))
	}
	if today.AvailabilityScore > 0 && today.AvailabilityScore < cfg.AutoDisableAvailThreshold {
		alerts = append(alerts, makeAlert(credentialID, providerID, AlertTypeAutoDisabled,
			AlertLevelCritical, triggerDate, today.TotalScore, 0, 0,
			"availability", fmt.Sprintf("可用性 %.1f 低于 %.0f，立即禁用", today.AvailabilityScore, cfg.AutoDisableAvailThreshold), nil))
	}

	// 5. auto_enabled: 总分>=70连续N天
	highScoreDays := isContiguousRecentDays(profiles, cfg.AutoEnableContinuousDays, func(p DailyProfile) bool {
		return p.TotalScore >= cfg.AutoEnableThreshold
	})
	if highScoreDays {
		alerts = append(alerts, makeAlert(credentialID, providerID, AlertTypeAutoEnabled,
			AlertLevelInfo, triggerDate, today.TotalScore, 0, 0,
			"total_score", fmt.Sprintf("总分连续 %d 天达到 %.0f，可恢复", cfg.AutoEnableContinuousDays, cfg.AutoEnableThreshold), nil))
	}

	return alerts
}

// makeAlert 减少重复字段构造样板
func makeAlert(credID, provID int64, typ AlertType, level AlertLevel, date time.Time,
	current, previous, change float64, dimension, msg string, details map[string]interface{}) Alert {
	return Alert{
		CredentialID:  credID,
		ProviderID:    provID,
		Type:          typ,
		Level:         level,
		TriggerDate:   date,
		CurrentScore:  current,
		PreviousScore: previous,
		ScoreChange:   change,
		Dimension:     dimension,
		Message:       msg,
		Details:       details,
	}
}

// detectContinuousDrop 检查从最新一天起是否连续 N 天都在下降，返回每日下降量与累计下降。
// profiles[0] 最新。下降 = profiles[i+1].TotalScore - profiles[i].TotalScore（更早一天的分数减今天的）。
// 必须连续 N 段，每段都是严格正下降，且日期逐日相连无空洞。
func detectContinuousDrop(profiles []DailyProfile, n int) (drops []float64, total float64, ok bool) {
	if len(profiles) < n+1 {
		return nil, 0, false
	}
	for i := 0; i < n; i++ {
		if i > 0 {
			expected := profiles[i-1].ProfileDate.AddDate(0, 0, -1)
			if !profiles[i].ProfileDate.Equal(expected) {
				return nil, 0, false
			}
		}
		if i+1 >= len(profiles) {
			return nil, 0, false
		}
		// profiles[i+1] 必须是 profiles[i] 的前一天
		if !profiles[i+1].ProfileDate.Equal(profiles[i].ProfileDate.AddDate(0, 0, -1)) {
			return nil, 0, false
		}
		d := profiles[i+1].TotalScore - profiles[i].TotalScore
		if d <= 0 {
			return nil, 0, false // 非下降即中断
		}
		drops = append(drops, d)
		total += d
	}
	return drops, total, true
}

// truncateDate 截断到当天 00:00:00
func truncateDate(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}
