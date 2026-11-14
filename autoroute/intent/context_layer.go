package intent

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ContextRecognizer implements Layer 2: context-based classification.
//
// Characteristics:
//  - Latency: 1-10ms (pattern matching + context lookup)
//  - Cost: Low (computational only, no external calls)
//  - Accuracy: 70-85% precision, better than rules on ambiguous cases
//
// Strategy: Session history + message patterns + statistical features
// Design inspired by Haystack's context-aware routing and Rasa's featurization.
type ContextRecognizer struct {
	ruleLayer      *RuleRecognizer // Delegate to rules first
	sessionHistory SessionHistoryProvider
	featureScorer  FeatureScorer
}

// SessionHistoryProvider provides access to historical intent patterns.
type SessionHistoryProvider interface {
	// GetRecentIntents returns the last N intents for a session
	GetRecentIntents(sessionID string, n int) []IntentType

	// GetIntentFrequency returns how often each intent appears in session
	GetIntentFrequency(sessionID string) map[IntentType]int
}

// FeatureScorer computes additional scoring based on statistical features.
type FeatureScorer interface {
	// ScoreFeatures analyzes message patterns and returns score adjustments
	ScoreFeatures(signals RecognitionSignals) map[IntentType]float64
}

// NewContextRecognizer creates a Layer 2 context-aware recognizer.
func NewContextRecognizer(ruleLayer *RuleRecognizer, history SessionHistoryProvider) *ContextRecognizer {
	return &ContextRecognizer{
		ruleLayer:      ruleLayer,
		sessionHistory: history,
		featureScorer:  NewDefaultFeatureScorer(),
	}
}

// Recognize implements IntentRecognizer for Layer 2.
func (c *ContextRecognizer) Recognize(ctx context.Context, signals RecognitionSignals) (*IntentResult, error) {
	start := time.Now()

	// Start with rule-based classification
	ruleResult, err := c.ruleLayer.Recognize(ctx, signals)
	if err != nil {
		return nil, err
	}

	// If rule layer has high confidence, use it directly
	if ruleResult.Confidence >= 0.85 {
		ruleResult.Layer = "context"
		ruleResult.Reason = "high confidence from rule layer: " + ruleResult.Reason
		ruleResult.Latency = time.Since(start)
		return ruleResult, nil
	}

	// Apply context-based refinements
	contextScores := c.computeContextScores(signals, ruleResult)

	// Merge rule scores with context scores
	finalScores := c.mergeScores(ruleResult, contextScores)

	// Pick winner with context-aware tiebreak
	winner, winnerScore := c.pickWinnerWithContext(finalScores, signals)
	alternatives := c.rankAlternatives(finalScores, winner)

	result := &IntentResult{
		Primary:      winner,
		Confidence:   winnerScore,
		Alternatives: alternatives,
		Layer:        "context",
		Reason:       c.buildContextReason(winner, ruleResult, contextScores, signals),
		Latency:      time.Since(start),
		Metadata: map[string]interface{}{
			"rule_confidence":   ruleResult.Confidence,
			"rule_primary":      ruleResult.Primary,
			"context_scores":    contextScores,
			"session_id":        signals.SessionID,
			"previous_intent":   signals.PreviousIntent,
		},
	}

	return result, nil
}

// computeContextScores calculates score adjustments based on session context.
func (c *ContextRecognizer) computeContextScores(signals RecognitionSignals, ruleResult *IntentResult) map[IntentType]float64 {
	scores := make(map[IntentType]float64)

	// 1. Session continuity: boost if continuing same task type
	if signals.SessionID != "" && signals.PreviousIntent != "" {
		// Boost previous intent (sticky sessions pattern)
		scores[signals.PreviousIntent] += 0.15

		// Check for intent switches that make sense
		if c.isLikelyIntentTransition(signals.PreviousIntent, ruleResult.Primary) {
			scores[ruleResult.Primary] += 0.1
		}
	}

	// 2. Session history patterns
	if c.sessionHistory != nil && signals.SessionID != "" {
		recentIntents := c.sessionHistory.GetRecentIntents(signals.SessionID, 5)
		if len(recentIntents) > 0 {
			// Find dominant pattern in recent requests
			frequency := make(map[IntentType]int)
			for _, intent := range recentIntents {
				frequency[intent]++
			}

			// Boost frequently occurring intents
			for intent, count := range frequency {
				if count >= 3 {
					scores[intent] += 0.20 // Strong session pattern
				} else if count >= 2 {
					scores[intent] += 0.10 // Moderate pattern
				}
			}
		}
	}

	// 3. Feature-based scoring (message structure patterns)
	if c.featureScorer != nil {
		featureScores := c.featureScorer.ScoreFeatures(signals)
		for intent, score := range featureScores {
			scores[intent] += score
		}
	}

	// 4. Multi-turn conversation patterns
	if signals.MessageCount > 10 {
		// Long conversations tend to be agent or code-related
		if ruleResult.Primary == IntentAgent || ruleResult.Primary == IntentCode {
			scores[ruleResult.Primary] += 0.05
		}
	}

	return scores
}

// mergeScores combines rule-based scores with context adjustments.
func (c *ContextRecognizer) mergeScores(ruleResult *IntentResult, contextScores map[IntentType]float64) map[IntentType]float64 {
	merged := make(map[IntentType]float64)

	// Start with rule-based score for primary intent
	merged[ruleResult.Primary] = ruleResult.Confidence

	// Add alternative scores from rule layer
	for _, alt := range ruleResult.Alternatives {
		merged[alt.Intent] = alt.Score
	}

	// Apply context adjustments (additive)
	for intent, adjustment := range contextScores {
		merged[intent] += adjustment
		// Cap at 0.95 (leave room for tool layer to override)
		if merged[intent] > 0.95 {
			merged[intent] = 0.95
		}
	}

	return merged
}

// pickWinnerWithContext selects the highest-scoring intent with context-aware tiebreak.
func (c *ContextRecognizer) pickWinnerWithContext(scores map[IntentType]float64, signals RecognitionSignals) (IntentType, float64) {
	// Priority order with context considerations
	priority := []IntentType{
		IntentReasoning, IntentPlanning, IntentCode, IntentAgent,
		IntentCreative, IntentFunctionCall, IntentVision,
		IntentLongContext, IntentChat,
	}

	// If previous intent has high score, consider it first (sticky sessions)
	if signals.PreviousIntent != "" {
		if score, ok := scores[signals.PreviousIntent]; ok && score >= 0.75 {
			// Move previous intent to front of priority
			newPriority := []IntentType{signals.PreviousIntent}
			for _, intent := range priority {
				if intent != signals.PreviousIntent {
					newPriority = append(newPriority, intent)
				}
			}
			priority = newPriority
		}
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

// isLikelyIntentTransition checks if transitioning from prevIntent to newIntent makes sense.
func (c *ContextRecognizer) isLikelyIntentTransition(prevIntent, newIntent IntentType) bool {
	// Define logical transition patterns
	transitions := map[IntentType][]IntentType{
		IntentPlanning: {IntentCode, IntentAgent},              // Plan → Implement
		IntentCode:     {IntentCodeAudit, IntentAgent},         // Code → Review/Execute
		IntentReasoning: {IntentCode, IntentPlanning},          // Analyze → Plan/Code
		IntentCreative: {IntentCode},                           // Write docs → Code examples
	}

	allowedNext, ok := transitions[prevIntent]
	if !ok {
		return false
	}

	for _, allowed := range allowedNext {
		if allowed == newIntent {
			return true
		}
	}
	return false
}

func (c *ContextRecognizer) rankAlternatives(scores map[IntentType]float64, winner IntentType) []IntentScore {
	var alternatives []IntentScore
	for intent, score := range scores {
		if intent != winner {
			alternatives = append(alternatives, IntentScore{Intent: intent, Score: score})
		}
	}

	// Simple insertion sort
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

func (c *ContextRecognizer) buildContextReason(winner IntentType, ruleResult *IntentResult, contextScores map[IntentType]float64, signals RecognitionSignals) string {
	var parts []string

	// Start with rule reason if it contributed
	if ruleResult.Primary == winner {
		parts = append(parts, "rule: "+ruleResult.Reason)
	}

	// Add context contributions
	if adjustment, ok := contextScores[winner]; ok && adjustment > 0.05 {
		parts = append(parts, fmt.Sprintf("context boost: +%.2f", adjustment))
	}

	// Add session continuity info
	if signals.PreviousIntent == winner && signals.PreviousIntent != "" {
		parts = append(parts, "session continuity (sticky)")
	}

	if len(parts) == 0 {
		parts = append(parts, "context-based classification")
	}

	return strings.Join(parts, "; ")
}

func (c *ContextRecognizer) Name() string { return "context" }
func (c *ContextRecognizer) Layer() int   { return 2 }

// DefaultFeatureScorer implements basic statistical feature scoring.
type DefaultFeatureScorer struct{}

func NewDefaultFeatureScorer() *DefaultFeatureScorer {
	return &DefaultFeatureScorer{}
}

func (s *DefaultFeatureScorer) ScoreFeatures(signals RecognitionSignals) map[IntentType]float64 {
	scores := make(map[IntentType]float64)

	// Message structure analysis
	avgMessageLength := 0
	if signals.MessageCount > 0 {
		avgMessageLength = signals.EstimatedTokens / signals.MessageCount
	}

	// Very short messages tend to be chat/questions
	if avgMessageLength < 50 && signals.MessageCount < 3 {
		scores[IntentChat] += 0.05
	}

	// Very long messages tend to be creative or long_context
	if avgMessageLength > 500 {
		scores[IntentCreative] += 0.05
		scores[IntentLongContext] += 0.05
	}

	// Multiple short messages suggest agent workflow
	if signals.MessageCount > 5 && avgMessageLength < 200 {
		scores[IntentAgent] += 0.10
	}

	// Language-specific patterns
	if signals.Language == "zh" {
		// Chinese text often more concise, adjust thresholds
		if avgMessageLength > 300 {
			scores[IntentCreative] += 0.03
		}
	}

	return scores
}

// InMemorySessionHistory provides a simple in-memory session history store.
type InMemorySessionHistory struct {
	history map[string][]IntentType
	maxSize int
}

func NewInMemorySessionHistory() *InMemorySessionHistory {
	return &InMemorySessionHistory{
		history: make(map[string][]IntentType),
		maxSize: 20, // Keep last 20 intents per session
	}
}

func (h *InMemorySessionHistory) Record(sessionID string, intent IntentType) {
	intents := h.history[sessionID]
	intents = append(intents, intent)

	// Trim to max size
	if len(intents) > h.maxSize {
		intents = intents[len(intents)-h.maxSize:]
	}

	h.history[sessionID] = intents
}

func (h *InMemorySessionHistory) GetRecentIntents(sessionID string, n int) []IntentType {
	intents := h.history[sessionID]
	if len(intents) <= n {
		return intents
	}
	return intents[len(intents)-n:]
}

func (h *InMemorySessionHistory) GetIntentFrequency(sessionID string) map[IntentType]int {
	frequency := make(map[IntentType]int)
	for _, intent := range h.history[sessionID] {
		frequency[intent]++
	}
	return frequency
}

func (h *InMemorySessionHistory) Clear(sessionID string) {
	delete(h.history, sessionID)
}
