package security

import "strings"

// IntentAnalyzer 意图分析器
type IntentAnalyzer struct {
	minScore float64
}

// NewIntentAnalyzer 创建意图分析器。
// minScore: 接受阈值（score < minScore 视为 unknown）
func NewIntentAnalyzer(minScore float64) *IntentAnalyzer {
	if minScore <= 0 {
		minScore = 0.5
	}
	return &IntentAnalyzer{minScore: minScore}
}

// Analyze 简单关键字匹配（实际生产可替换为 ML 模型调用）
func (a *IntentAnalyzer) Analyze(content string) *Intent {
	score := 0.0
	intentType := "unknown"
	reason := "no specific pattern matched"
	contentLower := strings.ToLower(content)

	// 优先检测有害意图（优先级最高）
	harmfulKeywords := []string{"bypass", "ignore previous", "ignore all", "disregard", "jailbreak", "unrestricted"}
	if containsAnyInLower(contentLower, harmfulKeywords) {
		intentType = "harmful"
		score = 0.9
		reason = "jailbreak attempt keywords"
		return &Intent{Type: intentType, Score: score, Reason: reason}
	}

	// 代码相关意图
	if containsAny(content, []string{"code", "function", "var ", "class ", "def ", "import ", "package "}) {
		intentType = "code"
		score = 0.7
		reason = "code-related keywords detected"
	} else if containsAny(content, []string{"hello", "hi", "你好", "how are you", "帮我", "请问"}) {
		intentType = "chat"
		score = 0.8
		reason = "greeting detected"
	}

	return &Intent{
		Type:   intentType,
		Score:  score,
		Reason: reason,
	}
}

// containsAny 检查字符串是否包含任意关键词（大小写敏感）
func containsAny(s string, keywords []string) bool {
	for _, k := range keywords {
		if contains(s, k) {
			return true
		}
	}
	return false
}

// containsAnyInLower 检查小写字符串是否包含任意小写关键词
func containsAnyInLower(sLower string, keywords []string) bool {
	for _, k := range keywords {
		if strings.Contains(sLower, k) {
			return true
		}
	}
	return false
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
