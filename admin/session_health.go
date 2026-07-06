package admin

import (
	"fmt"
	"strings"
)

// HealthScoreConfig 健康评分配置（可热更新）
type HealthScoreConfig struct {
	ErrorEndedPenalty        int `json:"error_ended_penalty"`         // 30
	AbandonedPenalty         int `json:"abandoned_penalty"`           // 15
	PerErrorPenalty          int `json:"per_error_penalty"`           // 3
	PerErrorCap              int `json:"per_error_cap"`               // 30
	PerCompliancePenalty     int `json:"per_compliance_penalty"`      // 10
	PerComplianceCap         int `json:"per_compliance_cap"`          // 30
	HighLatencyThresholdMs   int `json:"high_latency_threshold_ms"`   // 5000
	HighLatencyPenalty       int `json:"high_latency_penalty"`        // 15
	ModelSwitchThreshold     int `json:"model_switch_threshold"`      // 3
	ModelSwitchPenalty       int `json:"model_switch_penalty"`        // 10
	PromptInjectionPenalty   int `json:"prompt_injection_penalty"`    // 20
	PIIPenalty               int `json:"pii_penalty"`                 // 15
	ToxicOutputPenalty       int `json:"toxic_output_penalty"`        // 15
	SensitivePenaltyCap      int `json:"sensitive_penalty_cap"`       // 30
}

// DefaultHealthScoreConfig 默认配置
func DefaultHealthScoreConfig() HealthScoreConfig {
	return HealthScoreConfig{
		ErrorEndedPenalty:      30,
		AbandonedPenalty:       15,
		PerErrorPenalty:        3,
		PerErrorCap:            30,
		PerCompliancePenalty:   10,
		PerComplianceCap:       30,
		HighLatencyThresholdMs: 5000,
		HighLatencyPenalty:     15,
		ModelSwitchThreshold:   3,
		ModelSwitchPenalty:     10,
		PromptInjectionPenalty: 20,
		PIIPenalty:             15,
		ToxicOutputPenalty:     15,
		SensitivePenaltyCap:    30,
	}
}

// PenaltyItem 扣分明细（可解释性）
type PenaltyItem struct {
	Reason    string `json:"reason"`    // high_latency / frequent_model_switch / ...
	Deduction int    `json:"deduction"` // 扣分值
	Detail    string `json:"detail"`    // 具体信息，如 "avg_latency_ms=6200 > 5000"
}

// SessionHealth 会话健康度
type SessionHealth struct {
	HealthScore   int           `json:"health_score"`    // 0-100
	HealthGrade   string        `json:"health_grade"`    // A-F
	Outcome       string        `json:"outcome"`         // completed/error/abandoned/unknown
	OutcomeReason string        `json:"outcome_reason"`  // 人类可读原因
	ErrorRate     float64       `json:"error_rate"`
	AvgLatencyMs  int           `json:"avg_latency_ms"`
	Penalties     []PenaltyItem `json:"penalties"`       // 扣分明细
}

// ComputeHealth 计算会话健康度（核心函数）
func ComputeHealth(summary AnalyticsSessionSummary, config HealthScoreConfig) SessionHealth {
	score := 100
	penalties := []PenaltyItem{}

	// 1. 会话以错误结束（最高优先级）
	errorRate := 0.0
	if summary.RequestCount > 0 {
		errorRate = float64(summary.ErrorCount) / float64(summary.RequestCount)
	}
	if summary.ErrorCount > 0 && errorRate > 0.5 {
		score -= config.ErrorEndedPenalty
		penalties = append(penalties, PenaltyItem{
			Reason:    "error_ended",
			Deduction: config.ErrorEndedPenalty,
			Detail:    fmt.Sprintf("error_rate=%.1f%% > 50%%", errorRate*100),
		})
	}

	// 2. 会话被放弃
	if summary.RequestCount <= 1 {
		score -= config.AbandonedPenalty
		penalties = append(penalties, PenaltyItem{
			Reason:    "abandoned",
			Deduction: config.AbandonedPenalty,
			Detail:    fmt.Sprintf("request_count=%d <= 1", summary.RequestCount),
		})
	}

	// 3. 错误请求扣分（封顶）
	errorDeduction := min(summary.ErrorCount*config.PerErrorPenalty, config.PerErrorCap)
	if errorDeduction > 0 {
		score -= errorDeduction
		penalties = append(penalties, PenaltyItem{
			Reason:    "per_error",
			Deduction: errorDeduction,
			Detail:    fmt.Sprintf("%d errors, capped at -%d", summary.ErrorCount, config.PerErrorCap),
		})
	}

	// 4. 合规问题扣分（封顶）
	complianceDeduction := min(summary.ComplianceIssuesCount*config.PerCompliancePenalty, config.PerComplianceCap)
	if complianceDeduction > 0 {
		score -= complianceDeduction
		penalties = append(penalties, PenaltyItem{
			Reason:    "compliance_issue",
			Deduction: complianceDeduction,
			Detail:    fmt.Sprintf("%d compliance issues, capped at -%d", summary.ComplianceIssuesCount, config.PerComplianceCap),
		})
	}

	// 5. 高延迟
	if summary.AvgLatencyMs > config.HighLatencyThresholdMs {
		score -= config.HighLatencyPenalty
		penalties = append(penalties, PenaltyItem{
			Reason:    "high_latency",
			Deduction: config.HighLatencyPenalty,
			Detail:    fmt.Sprintf("avg_latency_ms=%d > %d", summary.AvgLatencyMs, config.HighLatencyThresholdMs),
		})
	}

	// 6. 频繁模型切换
	if summary.ModelSwitchCount > config.ModelSwitchThreshold {
		score -= config.ModelSwitchPenalty
		penalties = append(penalties, PenaltyItem{
			Reason:    "frequent_model_switch",
			Deduction: config.ModelSwitchPenalty,
			Detail:    fmt.Sprintf("model_switch_count=%d > %d", summary.ModelSwitchCount, config.ModelSwitchThreshold),
		})
	}

	// 7. 提示注入检测
	if summary.PromptInjectionDetected {
		score -= config.PromptInjectionPenalty
		penalties = append(penalties, PenaltyItem{
			Reason:    "prompt_injection",
			Deduction: config.PromptInjectionPenalty,
			Detail:    "prompt injection detected",
		})
	}

	// 8. PII / 毒性输出（封顶）
	sensitiveDeduction := 0
	if summary.PIIDetected {
		sensitiveDeduction += config.PIIPenalty
	}
	if summary.ToxicOutputDetected {
		sensitiveDeduction += config.ToxicOutputPenalty
	}
	sensitiveDeduction = min(sensitiveDeduction, config.SensitivePenaltyCap)
	if sensitiveDeduction > 0 {
		details := []string{}
		if summary.PIIDetected {
			details = append(details, "PII")
		}
		if summary.ToxicOutputDetected {
			details = append(details, "toxic")
		}
		score -= sensitiveDeduction
		penalties = append(penalties, PenaltyItem{
			Reason:    "sensitive_content",
			Deduction: sensitiveDeduction,
			Detail:    fmt.Sprintf("%s detected, capped at -%d", strings.Join(details, " + "), config.SensitivePenaltyCap),
		})
	}

	// 下限 0
	if score < 0 {
		score = 0
	}

	// 等级映射
	grade := gradeFromScore(score)

	// 结果分类
	outcome, reason := classifyOutcome(summary)

	return SessionHealth{
		HealthScore:   score,
		HealthGrade:   grade,
		Outcome:       outcome,
		OutcomeReason: reason,
		ErrorRate:     errorRate,
		AvgLatencyMs:  summary.AvgLatencyMs,
		Penalties:     penalties,
	}
}

// gradeFromScore 分数→等级映射
func gradeFromScore(score int) string {
	switch {
	case score >= 90:
		return "A"
	case score >= 75:
		return "B"
	case score >= 60:
		return "C"
	case score >= 40:
		return "D"
	default:
		return "F"
	}
}

// classifyOutcome 结果分类
// 判定顺序（与文档 session-management-analytics-plan.md 4.4.3 一致）：
//  1. error_rate > 50%            → error（错误主导）
//  2. request_count ≤ 1           → abandoned（单轮即止）
//  3. error_rate ≤ 50% 且 ≥2 请求 → completed（含少量错误但未主导）
//  4. 其他                        → unknown
func classifyOutcome(summary AnalyticsSessionSummary) (string, string) {
	if summary.RequestCount == 0 {
		return "unknown", "no requests"
	}

	errorRate := float64(summary.ErrorCount) / float64(summary.RequestCount)

	if errorRate > 0.5 {
		return "error", fmt.Sprintf("error-dominated: %d errors / %d requests", summary.ErrorCount, summary.RequestCount)
	}

	if summary.RequestCount <= 1 {
		return "abandoned", "single request, user left"
	}

	// 错误率 ≤ 50% 且至少 2 个请求：视为完成（含部分错误）。
	// 注意 50% 边界：errorRate == 0.5 走到这里（> 0.5 才算 error），
	// 因此 10 请求 5 错误 = completed。
	return "completed", fmt.Sprintf("completed: %d requests, %d errors", summary.RequestCount, summary.ErrorCount)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
