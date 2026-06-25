package security

// ThreatDetector 威胁检测器
type ThreatDetector struct {
	severityThreshold int
}

// NewThreatDetector 创建威胁检测器。
// severityThreshold: 阻断阈值（severity >= threshold 视为 critical）
func NewThreatDetector(severityThreshold int) *ThreatDetector {
	if severityThreshold <= 0 {
		severityThreshold = 7
	}
	return &ThreatDetector{severityThreshold: severityThreshold}
}

// Detect 检测威胁（简化规则）
func (d *ThreatDetector) Detect(content string) []*Threat {
	threats := []*Threat{}

	// PII 检测
	if containsAny(content, []string{"ssn:", "信用卡", "passport:"}) {
		threats = append(threats, &Threat{
			Type:     "pii_leak",
			Severity: 8,
			Evidence: "PII keyword detected",
		})
	}

	// 提示注入
	if containsAny(content, []string{"ignore instructions", "system:", "<|im_start|>"}) {
		threats = append(threats, &Threat{
			Type:     "injection",
			Severity: 9,
			Evidence: "prompt injection pattern",
		})
	}

	// 越狱
	if containsAny(content, []string{"DAN", "jailbreak", "no restrictions"}) {
		threats = append(threats, &Threat{
			Type:     "jailbreak",
			Severity: 10,
			Evidence: "jailbreak keywords",
		})
	}

	return threats
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
