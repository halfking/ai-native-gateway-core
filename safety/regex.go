package safety

import (
	"regexp"
	"sync"
)

// RegexMatcher 是正则匹配器
type RegexMatcher struct {
	patterns map[string]*regexRule // rule ID -> compiled regex
	mu       sync.RWMutex
}

type regexRule struct {
	Rule  Rule
	Regex *regexp.Regexp
}

// NewRegexMatcher 创建正则匹配器
func NewRegexMatcher() *RegexMatcher {
	return &RegexMatcher{
		patterns: make(map[string]*regexRule),
	}
}

// UpdatePatterns 更新匹配模式
func (rm *RegexMatcher) UpdatePatterns(rules []Rule) error {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	rm.patterns = make(map[string]*regexRule)

	for _, rule := range rules {
		re, err := regexp.Compile(rule.Pattern)
		if err != nil {
			// 跳过无效正则
			continue
		}

		rm.patterns[rule.ID] = &regexRule{
			Rule:  rule,
			Regex: re,
		}
	}

	return nil
}

// Match 匹配内容
func (rm *RegexMatcher) Match(content string) []Hit {
	rm.mu.RLock()
	defer rm.mu.RUnlock()

	var hits []Hit

	for _, rr := range rm.patterns {
		// 查找所有匹配
		matches := rr.Regex.FindAllStringIndex(content, -1)
		for _, match := range matches {
			hits = append(hits, Hit{
				RuleID:   rr.Rule.ID,
				RuleName: rr.Rule.Name,
				Pattern:  rr.Rule.Pattern,
				Position: match[0],
				Length:   match[1] - match[0],
				Severity: rr.Rule.Severity,
			})
		}
	}

	return hits
}
