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

	// 编译 PII 正则表达式（大小写不敏感，优化性能）
	piiPatterns := []*regexp.Regexp{
		// SSN: 支持多种表达方式 - "ssn", "social security", "社保号"等
		// 模式1: 带关键词的SSN
		regexp.MustCompile(`(?i)(ssn|social\s*security\s*(number)?|社保号?)(?:\s|[:：=]|is|是|为)*\d{3}[-\s]?\d{2}[-\s]?\d{4}`),
		// 模式2: 纯SSN格式（更严格，避免误报普通数字序列）
		regexp.MustCompile(`\b\d{3}[-]\d{2}[-]\d{4}\b`),
		
		// Passport: 支持有无冒号、空格，6-12位字母数字
		regexp.MustCompile(`(?i)(passport|护照号?)(?:\s|[:：=]|is|是|为)*[A-Z0-9]{6,12}`),
		
		// 信用卡：支持中英文、空格、连字符、无分隔符，更宽松匹配
		regexp.MustCompile(`(?i)(信用卡号?|credit\s*card\s*number?|card\s*number?)(?:\s|[:：=]|is|是|为)*\d{4}[\s\-]?\d{4}[\s\-]?\d{4}[\s\-]?\d{4}`),
		
		// 身份证号（中国）：18位或15位
		// 模式1: 带关键词
		regexp.MustCompile(`(?i)(身份证号?|id\s*card\s*number?|身份证|居民身份证)(?:\s|[:：=]|is|是|为)*\d{15}(\d{2}[0-9Xx])?`),
		// 模式2: 纯18位身份证格式（中国身份证特征：地区码6位+出生日期8位+顺序码3位+校验码1位）
		regexp.MustCompile(`\b[1-9]\d{5}(19|20)\d{2}(0[1-9]|1[0-2])(0[1-9]|[12]\d|3[01])\d{3}[0-9Xx]\b`),
		
		// 电话号码：常见格式
		regexp.MustCompile(`(?i)(电话|phone|mobile|tel|手机号?)(?:\s|[:：=]|is|是|为)*[\+\d][\d\s\-\(\)]{8,20}`),
		
		// 银行卡号：通常16-19位数字
		regexp.MustCompile(`(?i)(银行卡号?|bank\s*card|account\s*number)(?:\s|[:：=]|is|是|为)*\d{16,19}`),
		
		// 邮箱地址：标准email格式
		regexp.MustCompile(`(?i)(邮箱|email|e-mail)(?:\s|[:：=]|is|是|为)*[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}`),
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

	// 提示注入检测（扩展规则，覆盖更多变体）
	injectionKeywords := []string{
		"ignore instructions", "ignore all instructions", "ignore previous", "ignore all",
		"system:", "<|im_start|>", "disregard all", "disregard previous", "disregard instructions",
		"forget previous", "forget instructions", "override instructions", "new instructions",
	}
	if containsAnyLower(contentLower, injectionKeywords) {
		threats = append(threats, &Threat{
			Type:     "injection",
			Severity: 9,
			Evidence: "prompt injection pattern",
		})
	}

	// 越狱检测（扩展英文+中文规则，覆盖更多攻击模式）
	jailbreakKeywordsEnglish := []string{
		"dan", "jailbreak", "no restrictions", "please jailbreak",
		"bypass all", "ignore all", "unrestricted mode", "do anything now",
		"remove limitations", "disable safety", "no limits", "without restrictions",
		"you are now", "you must now", "act as if", "pretend you are",
	}
	jailbreakKeywordsChinese := []string{
		"越狱", "不受限制", "没有任何限制", "可以做任何事情",
		"解除限制", "忽略", "无视", "绕过", "假装你是", "扮演",
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
	return isCriticalAt(threats, d.severityThreshold)
}

func isCriticalAt(threats []*Threat, threshold int) bool {
	for _, t := range threats {
		if t.Severity >= threshold {
			return true
		}
	}
	return false
}
