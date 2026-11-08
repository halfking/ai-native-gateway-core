package mcp

import (
	"regexp"
	"strings"

	"github.com/kaixuan/llm-gateway-go/domains/secretmask"
)

// Redactor 负责出参脱敏（DLP）。
//
// 两层：
//  1. 凭据层：复用 domains/secretmask（OpenAI/Anthropic/AWS/Google/Bearer/x-api-key）。
//  2. PII 层：邮箱、手机号、身份证号、银行卡号 —— 默认仅对 high 敏感度工具
//     生效，避免把正常业务返回搅乱（可通过配置放宽）。
type Redactor struct {
	// maskPII 是否启用 PII 掩码。
	maskPII bool
	// patterns PII 规则（占位符 → 正则）。
	patterns []piiPattern
}

type piiPattern struct {
	placeholder string
	re          *regexp.Regexp
}

// 默认 PII 规则。
var defaultPIIPatterns = []piiPattern{
	{"[REDACTED_EMAIL]", regexp.MustCompile(`[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}`)},
	{"[REDACTED_PHONE]", regexp.MustCompile(`\b(?:\+?86)?1[3-9]\d{9}\b`)},
	{"[REDACTED_IDCARD]", regexp.MustCompile(`\b[1-9]\d{5}(19|20)\d{2}(0[1-9]|1[0-2])(0[1-9]|[12]\d|3[01])\d{3}[\dXx]\b`)},
	{"[REDACTED_BANKCARD]", regexp.MustCompile(`\b\d{16,19}\b`)},
}

// NewRedactor 构造脱敏器。maskPII=false 时只做凭据掩码。
func NewRedactor(maskPII bool) *Redactor {
	return &Redactor{maskPII: maskPII, patterns: defaultPIIPatterns}
}

// Mask 对文本脱敏。
func (r *Redactor) Mask(text string) string {
	if text == "" {
		return text
	}
	out := secretmask.MaskSecrets(text)
	if !r.maskPII {
		return out
	}
	for _, p := range r.patterns {
		out = p.re.ReplaceAllString(out, p.placeholder)
	}
	return out
}

// MaskToolResult 对工具返回体脱敏。
//
// 只处理文本节点：JSON 结构（键名、嵌套）保持不变，避免破坏客户端解析。
func (r *Redactor) MaskToolResult(raw string) string {
	if raw == "" {
		return raw
	}
	// 快速判断：没有可脱敏特征则原样返回。
	if !strings.ContainsAny(raw, "@+") && !strings.Contains(raw, "sk-") && !strings.Contains(raw, "Bearer") {
		return raw
	}
	return r.Mask(raw)
}
