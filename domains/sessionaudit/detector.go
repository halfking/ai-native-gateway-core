package sessionaudit

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// FastDetector 实时检测器（同步，目标 ≤5ms）
//
// 使用 Trie 树 + 预编译正则实现快速扫描，不调用 LLM。
type FastDetector struct {
	sensitiveTrie  *SensitiveWordTrie
	injectionRules []*regexp.Regexp
	piiRules       []*regexp.Regexp
	jailbreakRules []*regexp.Regexp
	maxContentLen  int // 超长内容截断（防止 DoS）
}

// NewFastDetector 创建检测器
func NewFastDetector(cfg *DetectorConfig) *FastDetector {
	if cfg == nil {
		cfg = DefaultDetectorConfig()
	}

	return &FastDetector{
		sensitiveTrie:  NewSensitiveWordTrie(cfg.SensitiveWords),
		injectionRules: compileRegexList(cfg.InjectionPatterns),
		piiRules:       compileRegexList(cfg.PIIPatterns),
		jailbreakRules: compileRegexList(cfg.JailbreakPatterns),
		maxContentLen:  cfg.MaxContentLen,
	}
}

// DetectorConfig 检测器配置
type DetectorConfig struct {
	SensitiveWords    []string
	InjectionPatterns []string
	PIIPatterns       []string
	JailbreakPatterns []string
	MaxContentLen     int
}

// DefaultDetectorConfig 返回默认配置
func DefaultDetectorConfig() *DetectorConfig {
	return &DetectorConfig{
		SensitiveWords: []string{
			// 政治敏感词（示例，生产需从配置加载）
			"政变", "六四", "法轮功",
			// 色情暴力
			"色情", "暴力", "血腥",
			// 违禁品
			"毒品", "枪支", "炸药",
		},
		InjectionPatterns: []string{
			`(?i)ignore\s+(previous|all|above)\s+instructions?`,
			`(?i)disregard\s+(previous|all)\s+(instructions?|prompts?)`,
			`(?i)you\s+are\s+now\s+a\s+different`,
			`(?i)system:\s*`,
			`(?i)<\|im_start\|>`,
			`(?i)__SYSTEM__`,
		},
		PIIPatterns: []string{
			// 信用卡号（简化）
			`\b\d{4}[\s-]?\d{4}[\s-]?\d{4}[\s-]?\d{4}\b`,
			// 身份证号（简化）
			`\b\d{17}[\dXx]\b`,
			// 手机号
			`\b1[3-9]\d{9}\b`,
			// 邮箱
			`\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Z|a-z]{2,}\b`,
		},
		JailbreakPatterns: []string{
			`(?i)\bDAN\b`,
			`(?i)jailbreak`,
			`(?i)no\s+restrictions?`,
			`(?i)pretend\s+you\s+(are|can)`,
			`(?i)developer\s+mode`,
		},
		MaxContentLen: 50000, // 50KB
	}
}

// Detect 执行快速检测
func (d *FastDetector) Detect(ctx context.Context, content string) (*DetectResult, error) {
	start := time.Now()

	// 1. 长度检查
	if len(content) > d.maxContentLen {
		content = content[:d.maxContentLen]
	}

	result := &DetectResult{
		SensitiveWords: []string{},
		Threats:        []Threat{},
		Score:          0,
	}

	// 2. 敏感词扫描
	words := d.sensitiveTrie.Scan(content)
	result.SensitiveWords = words
	result.Score += len(words) * 2 // 每个敏感词 +2 分

	// 3. Prompt Injection 检测
	for _, rule := range d.injectionRules {
		if matches := rule.FindStringSubmatch(content); matches != nil {
			result.Threats = append(result.Threats, Threat{
				Type:       "prompt_inject",
				Severity:   8,
				Evidence:   truncate(matches[0], 50),
				DetectedAt: time.Now(),
			})
			result.Score += 5 // 提升单次 injection 命中权重（2026-06-27 audit fix）
		}
	}

	// 4. PII 检测
	for _, rule := range d.piiRules {
		if matches := rule.FindStringSubmatch(content); matches != nil {
			result.Threats = append(result.Threats, Threat{
				Type:       "pii_leak",
				Severity:   9,
				Evidence:   "[REDACTED]", // 不记录实际 PII
				DetectedAt: time.Now(),
			})
			result.Score += 6
		}
	}

	// 5. Jailbreak 检测
	for _, rule := range d.jailbreakRules {
		if matches := rule.FindStringSubmatch(content); matches != nil {
			result.Threats = append(result.Threats, Threat{
				Type:       "jailbreak",
				Severity:   10,
				Evidence:   truncate(matches[0], 50),
				DetectedAt: time.Now(),
			})
			result.Score += 7
		}
	}

	// 6. 限制最高分
	if result.Score > 10 {
		result.Score = 10
	}

	// 7. 决策逻辑：
	//   - 任一 Threat.Severity >= 8 → NeedApproval（防御性升级，覆盖任
	//     何单次命中都需要人工介入的高危类型）
	//   - 否则按 Score 阈值：>=8 Approval / >=5 Warn / <5 Pass
	maxSeverity := 0
	for _, th := range result.Threats {
		if th.Severity > maxSeverity {
			maxSeverity = th.Severity
		}
	}
	switch {
	case maxSeverity >= 8 || result.Score >= 8:
		result.Decision = DecisionNeedApproval
		result.Reason = "high risk score or high-severity threat, manual review required"
	case result.Score >= 5:
		result.Decision = DecisionWarn
		result.Reason = "medium risk, logged for review"
	default:
		result.Decision = DecisionPass
		result.Reason = "no significant risk detected"
	}

	result.LatencyMs = int(time.Since(start) / time.Millisecond)
	return result, nil
}

// SensitiveWordTrie Trie 树实现
type SensitiveWordTrie struct {
	root *trieNode
}

type trieNode struct {
	children map[rune]*trieNode
	isEnd    bool
	word     string
}

// NewSensitiveWordTrie 构建 Trie 树
func NewSensitiveWordTrie(words []string) *SensitiveWordTrie {
	t := &SensitiveWordTrie{root: &trieNode{children: make(map[rune]*trieNode)}}
	for _, word := range words {
		if word != "" {
			t.Insert(word)
		}
	}
	return t
}

// Insert 插入敏感词
func (t *SensitiveWordTrie) Insert(word string) {
	node := t.root
	for _, ch := range strings.ToLower(word) {
		if node.children[ch] == nil {
			node.children[ch] = &trieNode{children: make(map[rune]*trieNode)}
		}
		node = node.children[ch]
	}
	node.isEnd = true
	node.word = word
}

// Scan 扫描文本中的敏感词
func (t *SensitiveWordTrie) Scan(text string) []string {
	var found []string
	seen := make(map[string]bool)
	text = strings.ToLower(text)
	runes := []rune(text)

	for i := 0; i < len(runes); i++ {
		node := t.root
		j := i
		for j < len(runes) && node.children[runes[j]] != nil {
			node = node.children[runes[j]]
			if node.isEnd && !seen[node.word] {
				found = append(found, node.word)
				seen[node.word] = true
				break
			}
			j++
		}
	}
	return found
}

// 辅助函数
func compileRegexList(patterns []string) []*regexp.Regexp {
	var rules []*regexp.Regexp
	for _, p := range patterns {
		if r, err := regexp.Compile(p); err == nil {
			rules = append(rules, r)
		}
	}
	return rules
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// CalculateMultiDimensionScore 计算多维度评分
func CalculateMultiDimensionScore(result *DetectResult) MultiDimensionScore {
	score := MultiDimensionScore{
		Security:  10, // 默认最高
		Danger:    0,
		Trust:     10,
		Sensitive: 0,
	}

	// 根据检测结果调整分数
	if result.Score > 0 {
		score.Security = 10 - result.Score
		score.Danger = result.Score
		score.Trust = 10 - result.Score
		score.Sensitive = result.Score
	}

	// 确保范围 0-10
	score.Security = clamp(score.Security, 0, 10)
	score.Danger = clamp(score.Danger, 0, 10)
	score.Trust = clamp(score.Trust, 0, 10)
	score.Sensitive = clamp(score.Sensitive, 0, 10)

	return score
}

func clamp(val, min, max int) int {
	if val < min {
		return min
	}
	if val > max {
		return max
	}
	return val
}

// ExtractUserContent 从请求体提取用户内容（工具函数）
func ExtractUserContent(bodyBytes []byte) (string, error) {
	// 简化实现：提取 JSON 中的 messages 数组最后一条 user 消息
	// 生产环境需要处理 OpenAI/Anthropic 协议差异
	type Message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	type Request struct {
		Messages []Message `json:"messages"`
	}

	var req Request
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		return "", fmt.Errorf("parse request body: %w", err)
	}

	// 提取最后一条 user 消息
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == "user" {
			return req.Messages[i].Content, nil
		}
	}

	return "", fmt.Errorf("no user message found")
}
