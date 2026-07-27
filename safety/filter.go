package safety

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kaixuan/llm-gateway-go/pkg/logger"
)

// ContentFilter 是内容安全过滤器实现
type ContentFilter struct {
	keywordMatcher *KeywordMatcher
	regexMatcher   *RegexMatcher
	rules          []Rule
	mu             sync.RWMutex

	// 统计
	totalChecks       int64
	totalBlocked      int64
	totalWarnings     int64
	totalSanitized    int64
	totalLatencyNanos int64
	hitsByRule        map[string]int64
	blockedByRule     map[string]int64
	blockedBySeverity map[Severity]int64
	statsMu           sync.Mutex

	// 日志
	logger logger.Logger
}

// NewContentFilter 创建内容过滤器
func NewContentFilter(rules []Rule) *ContentFilter {
	cf := &ContentFilter{
		keywordMatcher:    NewKeywordMatcher(),
		regexMatcher:      NewRegexMatcher(),
		rules:             make([]Rule, 0),
		hitsByRule:        make(map[string]int64),
		blockedByRule:     make(map[string]int64),
		blockedBySeverity: make(map[Severity]int64),
		logger:            logger.New("safety"),
	}

	cf.UpdateRules(rules)

	cf.logger.Info("content filter created",
		"rules_count", len(rules),
	)

	return cf
}

// CheckRequest 检查请求内容
func (cf *ContentFilter) CheckRequest(ctx context.Context, req *CheckRequest) (*CheckResult, error) {
	start := time.Now()
	defer func() {
		atomic.AddInt64(&cf.totalChecks, 1)
		atomic.AddInt64(&cf.totalLatencyNanos, time.Since(start).Nanoseconds())
	}()

	// 2026-07-27 concurrency fix: read len(cf.rules) under RLock. The rules
	// slice header is written by UpdateRules (which takes the write lock);
	// reading it unlocked here was an unsynchronised slice-header read/write.
	cf.mu.RLock()
	rulesCount := len(cf.rules)
	cf.mu.RUnlock()

	log := logger.WithContext(ctx, "safety")
	log.Debug("checking request content",
		"content_len", len(req.Content),
		"rules_count", rulesCount,
	)

	result, err := cf.check(req.Content)

	if err == nil && result != nil {
		log.Info("request check completed",
			"safe", result.Safe,
			"action", result.Action,
			"hits", len(result.Hits),
			"duration_us", time.Since(start).Microseconds(),
		)
	}

	return result, err
}

// CheckResponse 检查响应内容
func (cf *ContentFilter) CheckResponse(ctx context.Context, resp *CheckResponse) (*CheckResult, error) {
	start := time.Now()
	defer func() {
		atomic.AddInt64(&cf.totalChecks, 1)
		atomic.AddInt64(&cf.totalLatencyNanos, time.Since(start).Nanoseconds())
	}()

	return cf.check(resp.Content)
}

// check 执行检查
func (cf *ContentFilter) check(content string) (*CheckResult, error) {
	// 2026-07-27 concurrency fix: snapshot the matchers and rules under RLock,
	// then release before running the CPU-bound keyword/regex scans and
	// wg.Wait(). Previously the RLock was held across the whole match phase,
	// so UpdateRules (which takes the write lock) starved behind every
	// in-flight check. The matchers carry their own internal locks, so it is
	// safe to call Match on the snapshotted pointers after releasing cf.mu.
	cf.mu.RLock()
	keywordMatcher := cf.keywordMatcher
	regexMatcher := cf.regexMatcher
	rules := cf.rules
	cf.mu.RUnlock()

	// 并行检测
	var (
		keywordHits []Hit
		regexHits   []Hit
		wg          sync.WaitGroup
	)

	wg.Add(2)

	// Keyword 检测
	go func() {
		defer wg.Done()
		keywordHits = keywordMatcher.Match(content)
	}()

	// Regex 检测
	go func() {
		defer wg.Done()
		regexHits = regexMatcher.Match(content)
	}()

	wg.Wait()

	// 合并结果
	allHits := append(keywordHits, regexHits...)

	// 如果没有匹配，直接放行
	if len(allHits) == 0 {
		return &CheckResult{
			Safe:   true,
			Action: ActionAllow,
		}, nil
	}

	// 应用规则决策（使用快照后的 rules）
	result := cf.applyRules(content, allHits, rules)

	// 更新统计
	cf.updateStats(result)

	return result, nil
}

// applyRules 应用规则决策
func (cf *ContentFilter) applyRules(content string, hits []Hit, rules []Rule) *CheckResult {
	// 按严重程度排序，取最高级别
	highestSeverity := SeverityLow
	highestAction := ActionAllow
	var matchedRules []string
	matchedRulesSet := make(map[string]bool)

	for _, hit := range hits {
		if !matchedRulesSet[hit.RuleID] {
			matchedRules = append(matchedRules, hit.RuleID)
			matchedRulesSet[hit.RuleID] = true
		}

		// 更新最高严重程度
		if severityLevel(hit.Severity) > severityLevel(highestSeverity) {
			highestSeverity = hit.Severity
		}

		// 找到对应规则
		for _, rule := range rules {
			if rule.ID == hit.RuleID && rule.Enabled {
				// 检查白名单
				if cf.inWhiteList(content, rule.WhiteList) {
					cf.logger.Debug("content matched whitelist",
						"rule_id", rule.ID,
						"rule_name", rule.Name,
					)
					continue
				}

				// 更新最高动作
				if actionLevel(rule.Action) > actionLevel(highestAction) {
					highestAction = rule.Action
				}
			}
		}
	}

	result := &CheckResult{
		Safe:         highestAction == ActionAllow,
		Action:       highestAction,
		MatchedRules: matchedRules,
		Hits:         hits,
	}

	// 根据动作类型处理
	switch highestAction {
	case ActionBlock:
		result.Reason = "内容包含敏感信息"
		cf.logger.Warn("content blocked",
			"action", highestAction,
			"severity", highestSeverity,
			"hits", len(hits),
			"matched_rules", len(matchedRules),
		)
	case ActionWarn:
		result.Reason = "内容可能包含敏感信息"
		cf.logger.Info("content warning",
			"action", highestAction,
			"severity", highestSeverity,
			"hits", len(hits),
		)
	case ActionSanitize:
		result.SanitizedContent = cf.sanitize(content, hits)
		result.Reason = "内容已脱敏"
		cf.logger.Info("content sanitized",
			"action", highestAction,
			"original_len", len(content),
			"sanitized_len", len(result.SanitizedContent),
			"hits", len(hits),
		)
	}

	return result
}

// inWhiteList 检查是否在白名单
func (cf *ContentFilter) inWhiteList(content string, whiteList []string) bool {
	for _, pattern := range whiteList {
		if strings.Contains(content, pattern) {
			return true
		}
	}
	return false
}

// sanitize 脱敏处理
func (cf *ContentFilter) sanitize(content string, hits []Hit) string {
	// 按位置倒序排序，避免索引偏移
	sortedHits := make([]Hit, len(hits))
	copy(sortedHits, hits)

	// 简单冒泡排序（按位置倒序）
	for i := 0; i < len(sortedHits)-1; i++ {
		for j := 0; j < len(sortedHits)-i-1; j++ {
			if sortedHits[j].Position < sortedHits[j+1].Position {
				sortedHits[j], sortedHits[j+1] = sortedHits[j+1], sortedHits[j]
			}
		}
	}

	result := content
	for _, hit := range sortedHits {
		// 只脱敏中高严重程度
		if hit.Severity == SeverityMedium || hit.Severity == SeverityHigh || hit.Severity == SeverityCritical {
			// 替换为 ***
			mask := strings.Repeat("*", hit.Length)
			result = result[:hit.Position] + mask + result[hit.Position+hit.Length:]
		}
	}

	return result
}

// UpdateRules 更新规则
func (cf *ContentFilter) UpdateRules(rules []Rule) error {
	cf.mu.Lock()
	defer cf.mu.Unlock()

	cf.rules = rules

	// 更新 KeywordMatcher
	keywordRules := make([]Rule, 0)
	regexRules := make([]Rule, 0)

	for _, rule := range rules {
		if !rule.Enabled {
			continue
		}

		switch rule.Type {
		case RuleTypeKeyword:
			keywordRules = append(keywordRules, rule)
		case RuleTypeRegex:
			regexRules = append(regexRules, rule)
		}
	}

	cf.keywordMatcher.UpdatePatterns(keywordRules)
	cf.regexMatcher.UpdatePatterns(regexRules)

	return nil
}

// Metrics 返回统计指标
func (cf *ContentFilter) Metrics() FilterMetrics {
	cf.statsMu.Lock()
	defer cf.statsMu.Unlock()

	totalChecks := atomic.LoadInt64(&cf.totalChecks)
	totalLatency := atomic.LoadInt64(&cf.totalLatencyNanos)

	avgLatency := time.Duration(0)
	if totalChecks > 0 {
		avgLatency = time.Duration(totalLatency / totalChecks)
	}

	// 复制 map
	hitsByRule := make(map[string]int64)
	blockedByRule := make(map[string]int64)
	blockedBySeverity := make(map[Severity]int64)

	for k, v := range cf.hitsByRule {
		hitsByRule[k] = v
	}
	for k, v := range cf.blockedByRule {
		blockedByRule[k] = v
	}
	for k, v := range cf.blockedBySeverity {
		blockedBySeverity[k] = v
	}

	return FilterMetrics{
		TotalChecks:       totalChecks,
		TotalBlocked:      atomic.LoadInt64(&cf.totalBlocked),
		TotalWarnings:     atomic.LoadInt64(&cf.totalWarnings),
		TotalSanitized:    atomic.LoadInt64(&cf.totalSanitized),
		AverageLatency:    avgLatency,
		HitsByRule:        hitsByRule,
		BlockedByRule:     blockedByRule,
		BlockedBySeverity: blockedBySeverity,
	}
}

// updateStats 更新统计
func (cf *ContentFilter) updateStats(result *CheckResult) {
	cf.statsMu.Lock()
	defer cf.statsMu.Unlock()

	// 更新命中统计
	for _, ruleID := range result.MatchedRules {
		cf.hitsByRule[ruleID]++
	}

	// 更新动作统计
	switch result.Action {
	case ActionBlock:
		atomic.AddInt64(&cf.totalBlocked, 1)
		for _, ruleID := range result.MatchedRules {
			cf.blockedByRule[ruleID]++
		}
		for _, hit := range result.Hits {
			cf.blockedBySeverity[hit.Severity]++
		}
	case ActionWarn:
		atomic.AddInt64(&cf.totalWarnings, 1)
	case ActionSanitize:
		atomic.AddInt64(&cf.totalSanitized, 1)
	}
}

// Helper functions

func severityLevel(s Severity) int {
	switch s {
	case SeverityLow:
		return 1
	case SeverityMedium:
		return 2
	case SeverityHigh:
		return 3
	case SeverityCritical:
		return 4
	default:
		return 0
	}
}

func actionLevel(a Action) int {
	switch a {
	case ActionAllow:
		return 0
	case ActionWarn:
		return 1
	case ActionSanitize:
		return 2
	case ActionBlock:
		return 3
	default:
		return 0
	}
}
