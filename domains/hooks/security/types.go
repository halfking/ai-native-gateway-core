// Package security 实现请求安全检查领域 (Hook)。
// 阶段: PreRouting (intent) / PreUpstream (threat)
package security

import "time"

// Intent 意图
type Intent struct {
	Type       string  // "chat" / "code" / "tool_use" / "harmful" / "unknown"
	Score      float64 // 0.0-1.0
	Reason     string
	DetectedAt time.Time
}

// Threat 威胁
type Threat struct {
	Type       string // "jailbreak" / "pii_leak" / "injection" / "rate_abuse"
	Severity   int    // 0-10
	Evidence   string
	DetectedAt time.Time
}

// Verdict 检查结果
type Verdict struct {
	Allow   bool
	Intent  *Intent
	Threats []*Threat
	Reason  string
}
