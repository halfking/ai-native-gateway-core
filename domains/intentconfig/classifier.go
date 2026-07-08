package intentconfig

import (
	"regexp"
	"strings"
)

// EnhancedClassifier 增强分类器（支持可配置规则）
type EnhancedClassifier struct {
	config *ClassifierConfig
}

// NewEnhancedClassifier 创建分类器实例
func NewEnhancedClassifier(config *ClassifierConfig) *EnhancedClassifier {
	return &EnhancedClassifier{
		config: config,
	}
}

// ClassifyWithCandidates 分类并返回多方向候选
func (c *EnhancedClassifier) ClassifyWithCandidates(content string, contextLength int, hasImages bool, toolCount int) []IntentCandidate {
	candidates := make(map[IntentKind]*IntentCandidate)

	// 初始化所有意图类型
	for _, kind := range []IntentKind{
		IntentCode, IntentReasoning, IntentChat, IntentSummary,
		IntentTranslation, IntentExtraction, IntentToolUse, IntentUnclassified,
	} {
		candidates[kind] = &IntentCandidate{
			Kind:       kind,
			Confidence: 0.0,
			Signals:    make(map[string]float64),
		}
	}

	// Layer 1: 硬规则（优先级最高，命中后直接返回）
	hardRuleHit := false
	if c.config.EnabledLayers.HardRules {
		hardRuleHit = c.applyHardRules(content, hasImages, toolCount, contextLength, candidates)
	}

	// 如果硬规则命中，跳过后续层（硬规则置信度足够高）
	if !hardRuleHit {
		// Layer 2: 模式匹配
		if c.config.EnabledLayers.PatternMatch {
			c.applyPatternMatch(content, candidates)
		}

		// Layer 3: 关键词评分
		if c.config.EnabledLayers.KeywordScore {
			c.applyKeywordScore(content, candidates)
		}

		// 归一化候选置信度（仅在无硬规则时归一化）
		c.normalizeCandidates(candidates)
	}

	// 转换为切片并排序
	result := make([]IntentCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.Confidence > 0.01 { // 过滤极低置信度
			result = append(result, *candidate)
		}
	}

	// 按置信度降序排序
	for i := 0; i < len(result); i++ {
		for j := i + 1; j < len(result); j++ {
			if result[j].Confidence > result[i].Confidence {
				result[i], result[j] = result[j], result[i]
			}
		}
	}

	// 如果没有任何候选，返回 unclassified
	if len(result) == 0 {
		result = append(result, IntentCandidate{
			Kind:       IntentUnclassified,
			Confidence: 0.5,
			Signals:    map[string]float64{"default": 0.5},
		})
	}

	return result
}

// applyHardRules 应用硬规则（最高优先级，直接设置不参与归一化）
func (c *EnhancedClassifier) applyHardRules(content string, hasImages bool, toolCount int, contextLength int, candidates map[IntentKind]*IntentCandidate) bool {
	lc := strings.ToLower(content)

	// 规则1: 图像检测（最高优先级）
	if hasImages {
		candidates[IntentCode].Confidence = 0.95
		candidates[IntentCode].Signals["has_images"] = 0.95
		return true // 硬规则命中，不再执行其他层
	}

	// 规则2: 工具调用判断
	if toolCount >= 3 {
		// 多工具 = agent
		candidates[IntentToolUse].Confidence = 0.90
		candidates[IntentToolUse].Signals["tool_count_high"] = 0.90
		return true
	} else if toolCount >= 1 {
		// 少量工具 = function_call
		candidates[IntentToolUse].Confidence = 0.75
		candidates[IntentToolUse].Signals["tool_count_medium"] = 0.75
		return true
	}

	// 规则3: 长上下文检测
	if contextLength > 50000 {
		candidates[IntentSummary].Confidence = 0.85
		candidates[IntentSummary].Signals["long_context"] = 0.85
		return true
	}

	// 规则4: 代码块检测（强信号）
	if strings.Contains(content, "```") {
		candidates[IntentCode].Confidence = 0.95
		candidates[IntentCode].Signals["code_block"] = 0.95
		return true
	}

	// 规则5: IDE客户端指纹
	ideKeywords := []string{"cursor", "vscode", "pycharm", "intellij", "vim", "emacs"}
	for _, kw := range ideKeywords {
		if strings.Contains(lc, kw) {
			candidates[IntentCode].Confidence = 0.80
			candidates[IntentCode].Signals["ide_fingerprint"] = 0.80
			return true
		}
	}

	// 规则6: 函数/类定义检测
	funcDefPatterns := []string{
		`def\s+\w+\s*\(`,
		`function\s+\w+\s*\(`,
		`class\s+\w+`,
		`public\s+class`,
		`func\s+\w+\s*\(`,
	}
	for _, pattern := range funcDefPatterns {
		if matched, _ := regexp.MatchString(pattern, content); matched {
			candidates[IntentCode].Confidence = 0.85
			candidates[IntentCode].Signals["function_definition"] = 0.85
			return true
		}
	}

	return false // 无硬规则命中
}

// applyPatternMatch 应用正则模式匹配
func (c *EnhancedClassifier) applyPatternMatch(content string, candidates map[IntentKind]*IntentCandidate) {
	for intentKind, patterns := range c.config.PatternsConfig {
		for _, pattern := range patterns {
			re, err := regexp.Compile(pattern.Pattern)
			if err != nil {
				continue // 跳过无效正则
			}

			if re.MatchString(content) {
				// 累加权重（多个模式可叠加）
				currentConf := candidates[intentKind].Confidence
				newConf := currentConf + pattern.Weight*(1-currentConf) // 递减叠加
				candidates[intentKind].Confidence = newConf
				candidates[intentKind].Signals["pattern_"+pattern.Description] = pattern.Weight
			}
		}
	}
}

// applyKeywordScore 应用关键词评分
func (c *EnhancedClassifier) applyKeywordScore(content string, candidates map[IntentKind]*IntentCandidate) {
	lc := strings.ToLower(content)

	for intentKind, keywordSet := range c.config.KeywordsConfig {
		hitCount := 0
		totalKeywords := len(keywordSet.EN) + len(keywordSet.ZH)

		if totalKeywords == 0 {
			continue
		}

		// 英文关键词
		for _, keyword := range keywordSet.EN {
			if strings.Contains(lc, strings.ToLower(keyword)) {
				hitCount++
				candidates[intentKind].Signals["keyword_en_"+keyword] = 0.4
			}
		}

		// 中文关键词
		for _, keyword := range keywordSet.ZH {
			if strings.Contains(content, keyword) { // 中文不转小写
				hitCount++
				candidates[intentKind].Signals["keyword_zh_"+keyword] = 0.4
			}
		}

		// 计算关键词得分（每命中一个+0.4，累计到1.0）
		if hitCount > 0 {
			keywordScore := float64(hitCount) * 0.4
			if keywordScore > 1.0 {
				keywordScore = 1.0
			}

			// 与现有置信度合并（取最大值）
			currentConf := candidates[intentKind].Confidence
			if keywordScore > currentConf {
				candidates[intentKind].Confidence = keywordScore
			} else {
				// 叠加但不超过1.0
				newConf := currentConf + keywordScore*(1-currentConf)*0.5
				candidates[intentKind].Confidence = newConf
			}
		}
	}
}

// normalizeCandidates 归一化候选置信度（总和不超过1.0）
func (c *EnhancedClassifier) normalizeCandidates(candidates map[IntentKind]*IntentCandidate) {
	// 计算总置信度
	total := 0.0
	for _, candidate := range candidates {
		total += candidate.Confidence
	}

	// 如果总和超过1.0，按比例缩放
	if total > 1.0 {
		for _, candidate := range candidates {
			candidate.Confidence = candidate.Confidence / total
		}
	}

	// 如果总和为0（无任何匹配），给 unclassified 一个默认值
	if total == 0.0 {
		candidates[IntentUnclassified].Confidence = 0.5
		candidates[IntentUnclassified].Signals["default"] = 0.5
	}
}

// GetPrimaryIntent 从候选中选择主意图
func (c *EnhancedClassifier) GetPrimaryIntent(candidates []IntentCandidate) IntentCandidate {
	if len(candidates) == 0 {
		return IntentCandidate{
			Kind:       IntentUnclassified,
			Confidence: 0.5,
			Signals:    map[string]float64{"default": 0.5},
		}
	}

	// 已经按置信度降序排序，直接返回第一个
	return candidates[0]
}

// GetConfidenceLevel 获取置信度等级
func (c *EnhancedClassifier) GetConfidenceLevel(confidence float64) string {
	thresholds := c.config.ConfidenceThresholds
	switch {
	case confidence >= thresholds.High:
		return "high"
	case confidence >= thresholds.Medium:
		return "medium"
	case confidence >= thresholds.Low:
		return "low"
	default:
		return "very_low"
	}
}
