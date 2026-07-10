package security

import (
	"regexp"
	"strings"
)

// ThreatDetector 威胁检测器
type ThreatDetector struct {
	severityThreshold int
	piiPatterns       []*regexp.Regexp
}

// NewThreatDetector 创建威胁检测器。
// severityThreshold: 阻断阈值（severity >= threshold 视为 critical）
func NewThreatDetector(severityThreshold int) *ThreatDetector {
	if severityThreshold <= 0 {
		severityThreshold = 7
	}

	// 编译 PII 正则表达式（大小写不敏感）
	piiPatterns := []*regexp.Regexp{
		regexp.MustCompile(`(?i)ssn:?\s*\d{3}[-\s]?\d{2}[-\s]?\d{4}`),
		regexp.MustCompile(`(?i)passport:?\s*[A-Z0-9]{6,12}`),
		// 信用卡：支持空格、连字符或无分隔符
		regexp.MustCompile(`(?i)(信用卡|credit\s*card):?\s*\d{4}[\s\-]?\d{4}[\s\-]?\d{4}[\s\-]?\d{4}`),
	}

	return &ThreatDetector{
		severityThreshold: severityThreshold,
		piiPatterns:       piiPatterns,
	}
}

// Detect 检测威胁（增强版规则库）
func (d *ThreatDetector) Detect(content string) []*Threat {
	threats := []*Threat{}
	contentLower := strings.ToLower(content)

	// PII 检测（使用正则表达式）
	for _, pattern := range d.piiPatterns {
		if pattern.MatchString(content) {
			threats = append(threats, &Threat{
				Type:     "pii_leak",
				Severity: 8,
				Evidence: "PII pattern detected: " + pattern.String(),
			})
			break // 只报告一次 PII 威胁
		}
	}

	// 提示注入检测（扩展规则）
	injectionKeywords := []string{
		"ignore instructions", "ignore all instructions", "ignore previous",
		"system:", "<|im_start|>", "disregard all", "disregard previous",
	}
	if containsAnyLower(contentLower, injectionKeywords) {
		threats = append(threats, &Threat{
			Type:     "injection",
			Severity: 9,
			Evidence: "prompt injection pattern",
		})
	}

	// 越狱检测（扩展英文+中文规则）
	jailbreakKeywordsEnglish := []string{
		"dan", "jailbreak", "no restrictions", "please jailbreak",
		"bypass all", "ignore all", "unrestricted mode", "do anything now",
		"remove limitations", "disable safety", "no limits",
	}
	jailbreakKeywordsChinese := []string{
		"越狱", "不受限制", "没有任何限制", "可以做任何事情",
		"解除限制", "忽略", "无视", "绕过",
	}

	if containsAnyLower(contentLower, jailbreakKeywordsEnglish) || containsAny(content, jailbreakKeywordsChinese) {
		threats = append(threats, &Threat{
			Type:     "jailbreak",
			Severity: 10,
			Evidence: "jailbreak keywords",
		})
	}

	return threats
}

// containsAnyLower 检查小写内容是否包含任意关键词（关键词已是小写）
func containsAnyLower(contentLower string, keywords []string) bool {
	for _, k := range keywords {
		if strings.Contains(contentLower, k) {
			return true
		}
	}
	return false
}

// IsCritical 判断是否需要阻断
func (d *ThreatDetector) IsCritical(threats []*Threat) bool {
	for _, t := range threats {
		if t.Severity >= d.severityThreshold {
			return true
		}
	}
	return false
}
