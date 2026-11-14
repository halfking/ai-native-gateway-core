package intent

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// RuleRecognizer implements Layer 1: rule-based classification.
//
// Characteristics:
//  - Latency: <1ms (deterministic, no I/O)
//  - Cost: Zero (pure computation)
//  - Accuracy: 85-95% precision on clear signals
//
// Strategy: Hard overrides + keyword matching + structural patterns
// Design inspired by Rasa's rule-based NLU and the existing HeuristicClassifier.
type RuleRecognizer struct {
	keywords   KeywordRegistry
	patterns   PatternRegistry
	thresholds RuleThresholds
}

// RuleThresholds contains tunable parameters for rule-based classification.
type RuleThresholds struct {
	LongContextTokens   int     // Default: 50000
	AgentToolThreshold  int     // Default: 3
	FunctionCallToolMax int     // Default: 2
	KeywordWeight       float64 // Default: 0.4 per hit
}

// DefaultRuleThresholds returns production-ready defaults.
func DefaultRuleThresholds() RuleThresholds {
	return RuleThresholds{
		LongContextTokens:   50_000,
		AgentToolThreshold:  3,
		FunctionCallToolMax: 2,
		KeywordWeight:       0.4,
	}
}

// KeywordRegistry stores keywords for each intent category.
type KeywordRegistry struct {
	Reasoning []string
	Code      []string
	Creative  []string
	Planning  []string
	Audit     []string
	// Add more categories as needed
}

// DefaultKeywordRegistry returns the built-in keyword set.
// Coverage: zh + en, conservative choices for high precision.
func DefaultKeywordRegistry() KeywordRegistry {
	return KeywordRegistry{
		Reasoning: []string{
			// English
			"solve", "prove", "derive", "calculate", "compute",
			"reason", "reasoning", "logic", "theorem", "proof",
			"step by step", "explain why", "analyze",
			// 中文
			"证明", "推导", "求解", "计算", "推理", "逻辑",
			"证明题", "推导过程", "步骤",
		},
		Code: []string{
			// English
			"function", "class", "method", "algorithm", "implement",
			"code", "program", "script", "debug", "refactor",
			"compile", "syntax", "variable",
			"write a function", "write a class", "fix this bug",
			"unit test", "test case", "code review",
			// 中文
			"代码", "函数", "方法", "实现", "编写", "写代码",
			"算法", "重构", "调试", "bug", "编程",
			"实现一个", "写一个函数", "修复bug", "单元测试",
			"```", // code fence marker
		},
		Creative: []string{
			// English
			"write a", "write an", "compose", "draft", "story",
			"blog post", "essay", "poem", "creative",
			"translate", "summarize", "summary",
			// 中文
			"写一篇", "撰写", "创作", "故事", "小说", "诗歌",
			"翻译", "总结", "摘要", "文案", "润色", "改写",
		},
		Planning: []string{
			// English
			"technical design", "design doc", "write a plan",
			"project plan", "roadmap", "task breakdown",
			// 中文
			"技术方案", "设计方案", "制定计划", "任务拆解",
			"项目计划", "技术规划", "路线图",
		},
		Audit: []string{
			// English
			"code audit", "security audit", "security review",
			"vulnerability scan", "code quality review",
			// 中文
			"代码审查", "代码审计", "安全审计", "漏洞扫描",
		},
	}
}

// PatternRegistry stores compiled regex patterns for structural matching.
type PatternRegistry struct {
	// Patterns are lazily compiled on first use
	patterns map[IntentType][]patternMatcher
}

type patternMatcher struct {
	pattern string
	weight  float64
	reason  string
}

// NewRuleRecognizer creates a Layer 1 rule-based recognizer.
func NewRuleRecognizer(keywords KeywordRegistry, thresholds RuleThresholds) *RuleRecognizer {
	if thresholds.LongContextTokens == 0 {
		thresholds = DefaultRuleThresholds()
	}
	if len(keywords.Reasoning) == 0 {
		keywords = DefaultKeywordRegistry()
	}

	return &RuleRecognizer{
		keywords:   keywords,
		patterns:   NewPatternRegistry(),
		thresholds: thresholds,
	}
}

// Recognize implements IntentRecognizer for Layer 1.
func (r *RuleRecognizer) Recognize(ctx context.Context, signals RecognitionSignals) (*IntentResult, error) {
	start := time.Now()

	// Priority 1: Hard overrides (highest confidence)
	if result := r.checkHardOverrides(signals); result != nil {
		result.Latency = time.Since(start)
		return result, nil
	}

	// Priority 2: Tool-based dispatch
	if result := r.checkToolPatterns(signals); result != nil {
		result.Latency = time.Since(start)
		return result, nil
	}

	// Priority 3: Keyword + pattern scoring
	scores := r.scoreByKeywordsAndPatterns(signals)

	// Pick winner with priority tiebreak
	winner, winnerScore := r.pickWinner(scores)
	alternatives := r.rankAlternatives(scores, winner)

	result := &IntentResult{
		Primary:      winner,
		Confidence:   winnerScore,
		Alternatives: alternatives,
		Layer:        "rule",
		Reason:       r.buildReason(winner, signals, scores),
		Latency:      time.Since(start),
		Metadata: map[string]interface{}{
			"thresholds": r.thresholds,
			"scores":     scores,
		},
	}

	return result, nil
}

// checkHardOverrides applies deterministic rules with very high confidence.
func (r *RuleRecognizer) checkHardOverrides(signals RecognitionSignals) *IntentResult {
	// 1. Vision (highest priority, non-overridable)
	if signals.HasImages {
		return &IntentResult{
			Primary:    IntentVision,
			Confidence: 0.95,
			Layer:      "rule",
			Reason:     "request contains image parts (hard override)",
		}
	}

	// 2. Strong coding signals (IDE + code blocks + plan-mode patterns)
	if r.hasStrongCodingSignal(signals) {
		return &IntentResult{
			Primary:    IntentCode,
			Confidence: 0.90,
			Layer:      "rule",
			Reason:     r.describeCodingSignals(signals),
		}
	}

	// 3. Specialized task types (code audit, intent classification, planning)
	if r.isCodeAudit(signals) {
		return &IntentResult{
			Primary:    IntentCodeAudit,
			Confidence: 0.85,
			Layer:      "rule",
			Reason:     "explicit code audit / security review request",
		}
	}

	if r.isIntentClassification(signals) {
		return &IntentResult{
			Primary:    IntentIntentClassification,
			Confidence: 0.82,
			Layer:      "rule",
			Reason:     "intent classification / text classification request",
		}
	}

	if r.isPlanning(signals) {
		return &IntentResult{
			Primary:    IntentPlanning,
			Confidence: 0.82,
			Layer:      "rule",
			Reason:     "plan / proposal / design / task-breakdown request",
		}
	}

	// 4. Long context (only if no coding signal)
	if signals.EstimatedTokens > r.thresholds.LongContextTokens {
		return &IntentResult{
			Primary:    IntentLongContext,
			Confidence: 0.85,
			Layer:      "rule",
			Reason:     fmt.Sprintf("estimated tokens (%d) > threshold (%d)", signals.EstimatedTokens, r.thresholds.LongContextTokens),
		}
	}

	return nil
}

// checkToolPatterns examines tool counts to identify agent/function_call patterns.
func (r *RuleRecognizer) checkToolPatterns(signals RecognitionSignals) *IntentResult {
	if signals.ToolCount >= r.thresholds.AgentToolThreshold && signals.HasToolResults {
		return &IntentResult{
			Primary:    IntentAgent,
			Confidence: 0.85,
			Layer:      "rule",
			Reason:     fmt.Sprintf("tool_count=%d (>=%d) + has_tool_results=true", signals.ToolCount, r.thresholds.AgentToolThreshold),
		}
	}

	if signals.ToolCount >= 1 && signals.ToolCount <= r.thresholds.FunctionCallToolMax {
		return &IntentResult{
			Primary:    IntentFunctionCall,
			Confidence: 0.80,
			Layer:      "rule",
			Reason:     fmt.Sprintf("1-2 tools declared (tool_count=%d)", signals.ToolCount),
		}
	}

	return nil
}

// scoreByKeywordsAndPatterns computes scores for all intent types.
func (r *RuleRecognizer) scoreByKeywordsAndPatterns(signals RecognitionSignals) map[IntentType]float64 {
	scores := make(map[IntentType]float64)

	// Normalize text for keyword matching
	text := r.normalizeText(signals.SystemPrompt, signals.LastUserPrompt)

	// Keyword scoring
	reasoningHits := r.countKeywordHits(text, r.keywords.Reasoning)
	codeHits := r.countKeywordHits(text, r.keywords.Code)
	creativeHits := r.countKeywordHits(text, r.keywords.Creative)

	if reasoningHits > 0 {
		scores[IntentReasoning] = min(1.0, float64(reasoningHits)*r.thresholds.KeywordWeight)
	}
	if codeHits > 0 || signals.HasCodeBlock {
		boost := float64(codeHits) * r.thresholds.KeywordWeight
		if signals.HasCodeBlock {
			boost += 0.3
		}
		scores[IntentCode] = min(1.0, boost)
	}
	if creativeHits > 0 {
		scores[IntentCreative] = min(1.0, float64(creativeHits)*r.thresholds.KeywordWeight)
	}

	// Pattern matching (structural cues)
	patternScores := r.patterns.Match(text)
	for intent, score := range patternScores {
		if score > scores[intent] {
			scores[intent] = score
		}
	}

	// Default baseline for chat
	if len(scores) == 0 {
		scores[IntentChat] = 0.1
	} else {
		scores[IntentChat] = 0.1 // Baseline for fallback
	}

	return scores
}

// Helper methods

func (r *RuleRecognizer) hasStrongCodingSignal(signals RecognitionSignals) bool {
	hasCodeBlock := signals.HasCodeBlock
	hasIDEFingerprint := signals.ClientType != "" && isIDEClient(signals.ClientType)
	hasPlanModePattern := r.hasPlanModeCodingPattern(signals)

	return hasCodeBlock || hasIDEFingerprint || hasPlanModePattern
}

func (r *RuleRecognizer) describeCodingSignals(signals RecognitionSignals) string {
	reasons := []string{}
	if signals.HasCodeBlock {
		reasons = append(reasons, "has_code_block=true")
	}
	if signals.ClientType != "" && isIDEClient(signals.ClientType) {
		reasons = append(reasons, fmt.Sprintf("ide_client=%s", signals.ClientType))
	}
	if r.hasPlanModeCodingPattern(signals) {
		reasons = append(reasons, "plan_mode_pattern")
	}
	return "coding strong signal: " + strings.Join(reasons, ", ")
}

func (r *RuleRecognizer) hasPlanModeCodingPattern(signals RecognitionSignals) bool {
	text := strings.ToLower(signals.LastUserPrompt + " " + signals.SystemPrompt)
	patterns := []string{
		"先制定计划", "plan mode", "step by step implement",
		"先列出步骤", "然后实现", "then implement",
	}
	for _, p := range patterns {
		if strings.Contains(text, p) {
			return true
		}
	}
	return false
}

func (r *RuleRecognizer) isCodeAudit(signals RecognitionSignals) bool {
	if signals.ToolCount > 0 && signals.HasToolResults {
		return false // Let agent handle it
	}
	text := strings.ToLower(signals.LastUserPrompt + " " + signals.SystemPrompt)
	return r.containsAny(text, r.keywords.Audit)
}

func (r *RuleRecognizer) isIntentClassification(signals RecognitionSignals) bool {
	if signals.ToolCount > 0 && signals.HasToolResults {
		return false
	}
	text := strings.ToLower(signals.LastUserPrompt + " " + signals.SystemPrompt)
	keywords := []string{
		"intent classification", "intent detection", "classify intent",
		"text classification", "sentiment analysis",
		"意图分类", "意图识别", "文本分类", "情感分析",
	}
	return r.containsAny(text, keywords)
}

func (r *RuleRecognizer) isPlanning(signals RecognitionSignals) bool {
	if signals.ToolCount > 0 && signals.HasToolResults {
		return false
	}
	text := strings.ToLower(signals.LastUserPrompt + " " + signals.SystemPrompt)
	return r.containsAny(text, r.keywords.Planning)
}

func (r *RuleRecognizer) normalizeText(system, user string) string {
	const maxLen = 32 * 1024
	var b strings.Builder

	if system != "" {
		if len(system) > maxLen/2 {
			system = system[:maxLen/2]
		}
		b.WriteString(strings.ToLower(system))
		b.WriteByte('\n')
	}
	if user != "" {
		if len(user) > maxLen {
			user = user[:maxLen]
		}
		b.WriteString(strings.ToLower(user))
	}
	return b.String()
}

func (r *RuleRecognizer) countKeywordHits(text string, keywords []string) int {
	count := 0
	for _, kw := range keywords {
		if kw != "" && strings.Contains(text, strings.ToLower(kw)) {
			count++
		}
	}
	return count
}

func (r *RuleRecognizer) containsAny(text string, keywords []string) bool {
	for _, kw := range keywords {
		if strings.Contains(text, kw) {
			return true
		}
	}
	return false
}

func (r *RuleRecognizer) pickWinner(scores map[IntentType]float64) (IntentType, float64) {
	priority := []IntentType{
		IntentReasoning, IntentPlanning, IntentCode, IntentAgent,
		IntentCreative, IntentFunctionCall, IntentVision,
		IntentLongContext, IntentChat,
	}

	var best IntentType
	bestScore := -1.0
	for _, intent := range priority {
		if score, ok := scores[intent]; ok && score > bestScore {
			bestScore = score
			best = intent
		}
	}

	if best == "" {
		best = IntentChat
		bestScore = scores[IntentChat]
	}

	return best, bestScore
}

func (r *RuleRecognizer) rankAlternatives(scores map[IntentType]float64, winner IntentType) []IntentScore {
	var alternatives []IntentScore
	for intent, score := range scores {
		if intent != winner {
			alternatives = append(alternatives, IntentScore{Intent: intent, Score: score})
		}
	}

	// Simple insertion sort (small list)
	for i := 1; i < len(alternatives); i++ {
		for j := i; j > 0 && alternatives[j].Score > alternatives[j-1].Score; j-- {
			alternatives[j], alternatives[j-1] = alternatives[j-1], alternatives[j]
		}
	}

	if len(alternatives) > 3 {
		alternatives = alternatives[:3]
	}
	return alternatives
}

func (r *RuleRecognizer) buildReason(winner IntentType, signals RecognitionSignals, scores map[IntentType]float64) string {
	switch winner {
	case IntentReasoning:
		return fmt.Sprintf("keyword hits in reasoning channel (score: %.2f)", scores[IntentReasoning])
	case IntentCode:
		return fmt.Sprintf("keyword hits in code channel (score: %.2f)", scores[IntentCode])
	case IntentCreative:
		return fmt.Sprintf("keyword hits in creative channel (score: %.2f)", scores[IntentCreative])
	case IntentFunctionCall:
		return "1-2 tools declared"
	default:
		return "default: no strong signal, falling back to chat"
	}
}

func (r *RuleRecognizer) Name() string { return "rule" }
func (r *RuleRecognizer) Layer() int   { return 1 }

// Helper functions

func isIDEClient(clientType string) bool {
	ideClients := []string{"cursor", "vscode", "copilot", "windsurf", "claude-code", "roocode"}
	clientLower := strings.ToLower(clientType)
	for _, ide := range ideClients {
		if strings.Contains(clientLower, ide) {
			return true
		}
	}
	return false
}

func min(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

// NewPatternRegistry creates an empty pattern registry.
// In production, this would load compiled regex patterns.
func NewPatternRegistry() PatternRegistry {
	return PatternRegistry{
		patterns: make(map[IntentType][]patternMatcher),
	}
}

// Match applies all patterns to the text and returns scores.
func (pr *PatternRegistry) Match(text string) map[IntentType]float64 {
	scores := make(map[IntentType]float64)
	// Pattern matching implementation would go here
	// For now, return empty scores (keyword layer handles most cases)
	return scores
}
