package autoroute

import (
	"context"
	"fmt"
	"strings"
)

// classifier_v3.go implements the enhanced 10-category classification system
// for AUTO_MODEL V3 optimization.
//
// Related: docs/03-design/02-feature-design/design/AUTO_MODEL_OPTIMIZATION_V3_PLAN.md Section 2.2
//
// Key differences from legacy classifier:
//   1. 10 task categories instead of 8 (finer-grained)
//   2. Keyword matching tuned for new categories
//   3. Confidence thresholds per task type
//   4. Sub-agent depth detection support (via signals)
//   5. Better separation of overlapping categories (e.g., documentation vs summary)

// V3Classifier implements the 10-category classification system.
// It extends HeuristicClassifier with V3-specific logic while maintaining
// backward compatibility through feature flags.
type V3Classifier struct {
	keywords   V3KeywordSet
	thresholds HeuristicThresholds
	
	// enableV3 controls whether to use V3 classification or fall back to legacy.
	// Controlled by feature flag: auto_v3_enhanced_classification
	enableV3 bool
	
	// legacyClassifier is used as fallback when enableV3 is false
	legacyClassifier *HeuristicClassifier
}

// NewV3Classifier constructs a V3 classifier with the given keyword set.
// When enableV3 is false, it delegates to the legacy HeuristicClassifier.
func NewV3Classifier(keywords V3KeywordSet, thresholds HeuristicThresholds, enableV3 bool) *V3Classifier {
	if thresholds.LongContextTokens == 0 {
		thresholds = DefaultHeuristicThresholds()
	}
	
	clf := &V3Classifier{
		keywords:   keywords,
		thresholds: thresholds,
		enableV3:   enableV3,
	}
	
	// Create legacy classifier for fallback
	legacyKW := convertV3ToLegacyKeywords(keywords)
	clf.legacyClassifier = NewHeuristicClassifier(thresholds, legacyKW)
	
	return clf
}

// Classify implements Classifier interface.
// Routes to V3 or legacy classification based on enableV3 flag.
func (c *V3Classifier) Classify(ctx context.Context, sigs ClassificationSignals) (*Classification, error) {
	if !c.enableV3 {
		// Feature flag disabled, use legacy classifier
		return c.legacyClassifier.Classify(ctx, sigs)
	}
	
	return c.classifyV3(ctx, sigs)
}

// classifyV3 implements the V3 classification algorithm.
//
// Algorithm (priority order):
//  1. Hard overrides (vision, strong coding signals, long context)
//  2. Specialized task detection (architecture, audit, debugging)
//  3. Tool-based dispatch (agent, function_call)
//  4. Keyword-based scoring for remaining categories
//  5. Confidence threshold check (trigger LLM fallback if needed)
func (c *V3Classifier) classifyV3(_ context.Context, sigs ClassificationSignals) (*Classification, error) {
	scores := make(map[TaskType]float64, len(AllTaskTypesV3))
	
	// Normalize text for keyword matching
	text := normaliseForKeyword(sigs.LastUserPrompt, sigs.SystemPrompt)
	
	// ========================================================================
	// Phase 1: Hard Overrides (confidence 0.90-0.95)
	// ========================================================================
	
	// 1.1 Vision (highest priority, cannot be overridden)
	if sigs.HasImages {
		return &Classification{
			Primary:    TaskVision,
			Confidence: 0.95,
			Secondary:  []TaskScore{{Task: TaskVision, Score: 0.95}},
			Signals:    sigs,
			Classifier: "v3_heuristic",
			Reason:     "request contains image parts (hard override)",
		}, nil
	}
	
	// 1.2 Strong coding signals (code block, IDE fingerprint, plan mode pattern)
	hasCodeBlock := sigs.HasCodeBlock
	hasIDEFingerprint := sigs.ClientType != "" && isIDEClient(sigs.ClientType)
	hasPlanModePattern := containsFold(text, "先制定计划") || containsFold(text, "plan mode") ||
		containsFold(text, "step by step implement") || containsFold(text, "先列出步骤") ||
		containsFold(text, "然后实现") || containsFold(text, "then implement")
	
	strongCodingSignal := hasCodeBlock || hasIDEFingerprint || hasPlanModePattern
	
	if strongCodingSignal {
		conf := 0.90
		reason := "coding strong signal: "
		reasons := []string{}
		if hasCodeBlock {
			reasons = append(reasons, "has_code_block=true")
		}
		if hasIDEFingerprint {
			reasons = append(reasons, fmt.Sprintf("ide_client=%s", sigs.ClientType))
		}
		if hasPlanModePattern {
			reasons = append(reasons, "plan_mode_pattern")
		}
		return &Classification{
			Primary:    TaskCoding,
			Confidence: conf,
			Secondary:  []TaskScore{{Task: TaskCoding, Score: conf}},
			Signals:    sigs,
			Classifier: "v3_heuristic",
			Reason:     reason + strings.Join(reasons, ", "),
		}, nil
	}
	
	// 1.3 Long context (only if no strong coding signal)
	if sigs.EstimatedTokens > c.thresholds.LongContextTokens {
		conf := 0.85
		return &Classification{
			Primary:    TaskLongContext,
			Confidence: conf,
			Secondary:  []TaskScore{{Task: TaskLongContext, Score: conf}},
			Signals:    sigs,
			Classifier: "v3_heuristic",
			Reason:     fmtTokens(sigs.EstimatedTokens, c.thresholds.LongContextTokens),
		}, nil
	}
	
	// ========================================================================
	// Phase 2: Specialized Task Detection (confidence 0.80-0.90)
	// ========================================================================
	
	// 2.1 Architecture (system design, API design)
	archScore := c.scoreTaskType(text, c.keywords.Architecture)
	if archScore >= 0.60 {
		conf := min(0.90, archScore+0.15) // Boost confidence
		return &Classification{
			Primary:    TaskArchitecture,
			Confidence: conf,
			Secondary:  []TaskScore{{Task: TaskArchitecture, Score: conf}},
			Signals:    sigs,
			Classifier: "v3_heuristic",
			Reason:     fmt.Sprintf("architecture keywords (score: %.2f)", archScore),
		}, nil
	}
	scores[TaskArchitecture] = archScore
	
	// 2.2 Audit (code review, security audit)
	auditScore := c.scoreTaskType(text, c.keywords.Audit)
	if auditScore >= 0.60 {
		conf := min(0.90, auditScore+0.15)
		return &Classification{
			Primary:    TaskAudit,
			Confidence: conf,
			Secondary:  []TaskScore{{Task: TaskAudit, Score: conf}},
			Signals:    sigs,
			Classifier: "v3_heuristic",
			Reason:     fmt.Sprintf("audit keywords (score: %.2f)", auditScore),
		}, nil
	}
	scores[TaskAudit] = auditScore
	
	// 2.3 Debugging (bug investigation, stack trace analysis)
	debugScore := c.scoreTaskType(text, c.keywords.Debugging)
	// Check for error patterns (strong debugging signals)
	hasErrorPattern := containsFold(text, "error") || containsFold(text, "exception") ||
		containsFold(text, "stack trace") || containsFold(text, "traceback") ||
		containsFold(text, "doesn't work") || containsFold(text, "not working") ||
		containsFold(text, "why") || containsFold(text, "为什么")
	
	if hasErrorPattern {
		debugScore = min(1.0, debugScore+0.25) // Boost for error patterns
	}
	
	if debugScore >= 0.55 { // Lower threshold for debugging (often urgent)
		conf := min(0.88, debugScore+0.15)
		return &Classification{
			Primary:    TaskDebugging,
			Confidence: conf,
			Secondary:  []TaskScore{{Task: TaskDebugging, Score: conf}},
			Signals:    sigs,
			Classifier: "v3_heuristic",
			Reason:     fmt.Sprintf("debugging keywords (score: %.2f, error_pattern: %v)", debugScore, hasErrorPattern),
		}, nil
	}
	scores[TaskDebugging] = debugScore
	
	// ========================================================================
	// Phase 3: Tool-Based Dispatch
	// ========================================================================
	
	// 3.1 Agent (multi-tool workflows)
	if sigs.ToolCount >= c.thresholds.AgentToolThreshold && sigs.HasToolResults {
		conf := 0.85
		return &Classification{
			Primary:    TaskAgent,
			Confidence: conf,
			Secondary:  []TaskScore{{Task: TaskAgent, Score: conf}},
			Signals:    sigs,
			Classifier: "v3_heuristic",
			Reason: fmt.Sprintf("tool_count=%d (>= %d) + has_tool_results=true",
				sigs.ToolCount, c.thresholds.AgentToolThreshold),
		}, nil
	}
	
	// 3.2 Function Call (1-2 tools)
	if sigs.ToolCount >= 1 && sigs.ToolCount <= c.thresholds.FunctionCallToolMax {
		scores[TaskFunctionCall] = 0.80
	}
	
	// ========================================================================
	// Phase 4: Keyword-Based Scoring (all remaining categories)
	// ========================================================================
	
	// Score all V3 task types
	scores[TaskCoding] = c.scoreTaskType(text, c.keywords.Coding)
	if sigs.HasCodeBlock {
		scores[TaskCoding] = min(1.0, scores[TaskCoding]+0.30)
	}
	
	scores[TaskRefactoring] = c.scoreTaskType(text, c.keywords.Refactoring)
	scores[TaskTesting] = c.scoreTaskType(text, c.keywords.Testing)
	scores[TaskDevOps] = c.scoreTaskType(text, c.keywords.DevOps)
	scores[TaskDocumentation] = c.scoreTaskType(text, c.keywords.Documentation)
	scores[TaskSummary] = c.scoreTaskType(text, c.keywords.Summary)
	scores[TaskDependency] = c.scoreTaskType(text, c.keywords.Dependency)
	
	// Default baseline for chat
	scores[TaskChat] = 0.10
	
	// ========================================================================
	// Phase 5: Winner Selection & Confidence Check
	// ========================================================================
	
	winner, winnerScore := c.pickWinnerV3(scores)
	secondary := rankSecondary(scores, winner)
	
	// Build reason
	reason := c.buildReasonV3(winner, scores)
	
	// Check minimum confidence threshold for the task type
	minConf := MinConfidenceThresholds[winner]
	if minConf == 0 {
		minConf = 0.70 // Default threshold
	}
	
	// Apply confidence boost for strong signals
	if winnerScore >= 0.75 {
		winnerScore = min(0.95, winnerScore+0.08)
	}
	
	return &Classification{
		Primary:    winner,
		Confidence: winnerScore,
		Secondary:  secondary,
		Signals:    sigs,
		Classifier: "v3_heuristic",
		Reason:     reason,
	}, nil
}

// scoreTaskType computes a 0.0-1.0 score for a task type based on keyword hits.
// Uses weighted scoring: each keyword contributes 0.35 (up to max 1.0).
func (c *V3Classifier) scoreTaskType(text string, keywords []string) float64 {
	if len(keywords) == 0 {
		return 0.0
	}
	
	hits := countKeywordHits(text, keywords)
	if hits == 0 {
		return 0.0
	}
	
	// Weight per hit: 0.35 (so 3 hits = 1.0)
	// This is higher than legacy (0.2) to make decisions more decisive
	const perHitWeight = 0.35
	score := min(1.0, float64(hits)*perHitWeight)
	
	return score
}

// pickWinnerV3 selects the highest-scoring task type with priority tiebreaker.
// Priority order for V3:
//   architecture > audit > debugging > coding > refactoring > testing >
//   devops > agent > function_call > documentation > summary > dependency >
//   vision > long_context > chat
func (c *V3Classifier) pickWinnerV3(scores map[TaskType]float64) (TaskType, float64) {
	priority := []TaskType{
		// Tier-A tasks (highest priority - most valuable)
		TaskArchitecture,
		TaskAudit,
		TaskDebugging,
		
		// Tier-B tasks (standard development)
		TaskCoding,
		TaskRefactoring,
		TaskTesting,
		
		// Tier-C tasks (economy, operational)
		TaskDevOps,
		
		// Other specialized tasks
		TaskAgent,
		TaskFunctionCall,
		
		// Tier-C documentation/summary
		TaskDocumentation,
		TaskSummary,
		TaskDependency,
		
		// Fallback categories
		TaskVision,
		TaskLongContext,
		TaskChat,
	}
	
	var best TaskType
	bestScore := -1.0
	
	for _, t := range priority {
		if s, ok := scores[t]; ok && s > bestScore {
			bestScore = s
			best = t
		}
	}
	
	if best == "" {
		best = TaskChat
		bestScore = scores[TaskChat]
	}
	
	return best, bestScore
}

// buildReasonV3 constructs a human-readable explanation for the classification.
func (c *V3Classifier) buildReasonV3(winner TaskType, scores map[TaskType]float64) string {
	winnerScore := scores[winner]
	
	switch winner {
	case TaskArchitecture:
		return fmt.Sprintf("architecture keywords (score: %.2f)", winnerScore)
	case TaskAudit:
		return fmt.Sprintf("audit keywords (score: %.2f)", winnerScore)
	case TaskDebugging:
		return fmt.Sprintf("debugging keywords (score: %.2f)", winnerScore)
	case TaskCoding:
		return fmt.Sprintf("coding keywords (score: %.2f)", winnerScore)
	case TaskRefactoring:
		return fmt.Sprintf("refactoring keywords (score: %.2f)", winnerScore)
	case TaskTesting:
		return fmt.Sprintf("testing keywords (score: %.2f)", winnerScore)
	case TaskDevOps:
		return fmt.Sprintf("devops keywords (score: %.2f)", winnerScore)
	case TaskDocumentation:
		return fmt.Sprintf("documentation keywords (score: %.2f)", winnerScore)
	case TaskSummary:
		return fmt.Sprintf("summary keywords (score: %.2f)", winnerScore)
	case TaskDependency:
		return fmt.Sprintf("dependency keywords (score: %.2f)", winnerScore)
	case TaskAgent:
		return "multi-tool agent workflow detected"
	case TaskFunctionCall:
		return "1-2 tools declared (function_call)"
	default:
		return fmt.Sprintf("default: %s (score: %.2f)", winner, winnerScore)
	}
}

// Name implements Classifier interface.
func (c *V3Classifier) Name() string {
	if c.enableV3 {
		return "v3_heuristic"
	}
	return "heuristic"
}

// convertV3ToLegacyKeywords converts V3KeywordSet to legacy KeywordSet
// for fallback compatibility.
func convertV3ToLegacyKeywords(v3kw V3KeywordSet) KeywordSet {
	// Merge V3 keywords into legacy categories
	legacy := KeywordSet{
		Reasoning: append([]string{}, v3kw.Architecture...),
		Code: append([]string{},
			append(
				append(
					append(
						append(v3kw.Coding, v3kw.Refactoring...),
						v3kw.Testing...,
					),
					v3kw.DevOps...,
				),
				v3kw.Debugging...,
			)...,
		),
		Creative: append([]string{},
			append(
				append(v3kw.Documentation, v3kw.Summary...),
				v3kw.Dependency...,
			)...,
		),
	}
	
	// Add audit keywords to reasoning (analysis task)
	legacy.Reasoning = append(legacy.Reasoning, v3kw.Audit...)
	
	return legacy
}

// IsV3Enabled returns whether V3 classification is enabled.
func (c *V3Classifier) IsV3Enabled() bool {
	return c.enableV3
}

// SetV3Enabled dynamically enables or disables V3 classification.
// Used for feature flag control and A/B testing.
func (c *V3Classifier) SetV3Enabled(enabled bool) {
	c.enableV3 = enabled
}
