package safety

import (
	"strings"
	"sync"
)

// KeywordMatcher 是关键词匹配器
type KeywordMatcher struct {
	patterns map[string]Rule // pattern -> rule
	mu       sync.RWMutex
}

// NewKeywordMatcher 创建关键词匹配器
func NewKeywordMatcher() *KeywordMatcher {
	return &KeywordMatcher{
		patterns: make(map[string]Rule),
	}
}

// UpdatePatterns 更新匹配模式
func (km *KeywordMatcher) UpdatePatterns(rules []Rule) {
	km.mu.Lock()
	defer km.mu.Unlock()

	km.patterns = make(map[string]Rule)
	for _, rule := range rules {
		km.patterns[strings.ToLower(rule.Pattern)] = rule
	}
}

// Match 匹配内容
func (km *KeywordMatcher) Match(content string) []Hit {
	km.mu.RLock()
	defer km.mu.RUnlock()

	var hits []Hit
	lowerContent := strings.ToLower(content)

	for pattern, rule := range km.patterns {
		// 查找所有出现位置
		pos := 0
		for {
			idx := strings.Index(lowerContent[pos:], pattern)
			if idx == -1 {
				break
			}

			actualPos := pos + idx
			hits = append(hits, Hit{
				RuleID:   rule.ID,
				RuleName: rule.Name,
				Pattern:  pattern,
				Position: actualPos,
				Length:   len(pattern),
				Severity: rule.Severity,
			})

			pos = actualPos + len(pattern)
		}
	}

	return hits
}
