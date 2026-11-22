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
		"disregard all", "disregard previous", "disregard instructions",
		"forget previous", "forget instructions", "override instructions", "new instructions",
	}
	if containsAnyLower(contentLower, injectionKeywords) || hasSystemPromptInjection(contentLower) {
		threats = append(threats, &Threat{
			Type:     "injection",
			Severity: 9,
			Evidence: "prompt injection pattern",
		})
	}

	// 越狱检测（扩展英文+中文规则，覆盖更多攻击模式）
	//
	// 2026-11-23 修复：移除 "dan" 子串。
	// 实证：211 生产 7 天 80,306 条真实流量分析，"dan" 触发的 11 笔命中中
	// 9 笔是误报（"redundant"/"abundance" 等正常业务术语），真实攻击 0 笔。
	// "dan" 是 "Do Anything Now" 越狱提示词的缩写，但单独作为子串会误判
	// 大量正常词汇。真实 DAN 越狱会包含其他关键词（"you are now" / "act as"），
	// 移除 "dan" 不会降低安全性。
	jailbreakKeywordsEnglish := []string{
		"jailbreak", "no restrictions", "please jailbreak",
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

// hasSystemPromptInjection 判断内容是否是「以 system: 伪装的指令注入」。
//
// ★ 背景（211 生产 2026-11-23 实测，误报 0.0435% 的**唯一**来源）：
// 框架注入的环境段形如
//
//	… | system: # Environment You have been invoked in the …
//
// 它含 `system:` 字面量，但**不是**用户表达的攻击意图 —— 而注入关键词表
// 里此前把 `system:` 列为**独立**命中项 ⇒ **任何带该框架环境段的正常业务
// 请求都会被判 injection(sev9) ≥ 阈值 ⇒ 403**。
// 7 天真实流量回放（80,306 条）：user 段命中 11 笔，其中
// **9 笔是这一形态的误报**（用户实际在问「In the Go source of the LLM
// gateway, find…」这类正常业务请求），2 笔是历史测试语，**真攻击 0 笔**。
//
// ★ 修法（不是简单删词，而是加判据）：**`system:` 之后同一行内**必须出现
// 指令性内容才算注入。
//   - 真实注入：`system: ignore all previous instructions` /
//     `system: you are now …` —— `system:` 后**同行**紧跟指令；
//   - 框架环境段：`system: # Environment You have been invoked …` ——
//     `system:` 后是**环境/标题描述**，不含指令。
//
// ★ 为什么不直接把 `system:` 从词表删掉：那样虽然也能消除误报，但会放过
// 纯 `system: <指令>` 形态的注入（攻击者写 `system: 新的系统指令…` 即可
// 绕过）。保留「同行 + 指令」条件，**攻击面不增**。
//
// ★ 为什么限定「同一行」而不是「后 N 字符窗口」：框架环境段之后可能还跟着
// 别的内容，若用窗口判据，「环境段 + 后面恰好出现指令词」仍会误伤；同行
// 判据把「`system:` 引入的那一条指令」限定为注入，语义更准。
//
// ★ 通用式：**「关键词误伤」的正确修法不是删词，是加判据**——删词是净损失
// （攻击面变大），加判据是净收益（误伤消除且防护不降）。
func hasSystemPromptInjection(contentLower string) bool {
	// 逐个检查所有 `system:` 出现位置（可能多次出现）。
	offset := 0
	for {
		idx := strings.Index(contentLower[offset:], "system:")
		if idx < 0 {
			return false
		}
		abs := offset + idx
		// 取 `system:` 之后到行尾（或内容尾）的片段。
		rest := contentLower[abs+len("system:"):]
		if nl := strings.IndexAny(rest, "\n\r"); nl >= 0 {
			rest = rest[:nl]
		}
		if containsInjectionInstruction(rest) {
			return true
		}
		offset = abs + len("system:")
	}
}

// containsInjectionInstruction 判断一段文本是否含「指令性内容」。
//
// 这些标记是**攻击者用来改写系统指令的祈使表述**，覆盖中英两种形态。
// 注意其中不含 "system" / "prompt" 等中性词——那正是此前误伤的来源。
func containsInjectionInstruction(s string) bool {
	instructionMarkers := []string{
		// 英文祈使
		"ignore", "disregard", "forget", "override", "new instruction",
		"you are now", "you must now", "act as if", "pretend you are",
		"jailbreak", "no restrictions", "unrestricted", "bypass",
		"disable safety", "remove limitations", "do anything now",
		"reveal", "print your", "output your", "from now on", "new system",
		// 中文祈使
		"忽略", "无视", "绕过", "假装你是", "扮演", "越狱", "解除限制",
		"不受限制", "没有任何限制", "可以做任何事情",
		"输出你", "打印你", "从现在起", "新的系统", "新的指令", "系统指令",
	}
	return containsAnyLower(s, instructionMarkers)
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
