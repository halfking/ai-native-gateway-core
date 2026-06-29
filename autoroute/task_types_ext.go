// Package autoroute extensions for goal mode task types.
package autoroute

import "strings"

// Extended task types for goal mode operations.
const (
	// TaskCodeAudit covers code review, security analysis, and quality checks.
	TaskCodeAudit TaskType = "code_audit"
	// TaskIntentClassification covers intent detection and classification tasks.
	TaskIntentClassification TaskType = "intent_classification"
)

// IsCodeAuditRequest checks if a request is asking for code audit.
func IsCodeAuditRequest(signals ClassificationSignals) bool {
	auditKeywords := []string{
		"audit", "review", "审计", "审查", "检查",
		"security analysis", "安全分析",
		"code quality", "代码质量",
		"best practices", "最佳实践",
		"vulnerability", "漏洞",
	}
	content := signals.SystemPrompt + " " + signals.LastUserPrompt
	contentLower := strings.ToLower(content)
	for _, kw := range auditKeywords {
		if strings.Contains(contentLower, kw) {
			return true
		}
	}
	return false
}

// IsIntentClassificationRequest checks if a request is for intent classification.
func IsIntentClassificationRequest(signals ClassificationSignals) bool {
	intentKeywords := []string{
		"classify", "分类", "判断", "intent", "意图",
		"detect", "检测", "is this", "是否",
		"determine if", "判断是否",
	}
	content := signals.SystemPrompt + " " + signals.LastUserPrompt
	contentLower := strings.ToLower(content)
	for _, kw := range intentKeywords {
		if strings.Contains(contentLower, kw) {
			return true
		}
	}
	return false
}
