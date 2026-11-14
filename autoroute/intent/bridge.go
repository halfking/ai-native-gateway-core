package intent

import (
	"context"

	"github.com/kaixuan/llm-gateway-go/autoroute"
)

// Bridge provides integration between the new intent recognition system
// and the existing autoroute package.
//
// This allows gradual migration from HeuristicClassifier to the new
// modular intent system without breaking existing code.

// FromAutoRouteSignals converts autoroute.ClassificationSignals to RecognitionSignals.
func FromAutoRouteSignals(signals autoroute.ClassificationSignals) RecognitionSignals {
	return RecognitionSignals{
		SystemPrompt:    signals.SystemPrompt,
		LastUserPrompt:  signals.LastUserPrompt,
		Language:        signals.Language,
		MessageCount:    signals.MessageCount,
		EstimatedTokens: signals.EstimatedTokens,
		ToolCount:       signals.ToolCount,
		HasToolResults:  signals.HasToolResults,
		HasImages:       signals.HasImages,
		HasCodeBlock:    signals.HasCodeBlock,
		ClientType:      signals.ClientType,
		AgentRole:       string(signals.AgentRole),
		// Session context fields would need to be added to ClassificationSignals
		// or extracted from request context
	}
}

// ToAutoRouteClassification converts IntentResult to autoroute.Classification.
func ToAutoRouteClassification(result *IntentResult, signals autoroute.ClassificationSignals) *autoroute.Classification {
	// Convert IntentType to TaskType
	taskType := autoroute.TaskType(result.Primary)

	// Convert alternatives
	var secondary []autoroute.TaskScore
	for _, alt := range result.Alternatives {
		secondary = append(secondary, autoroute.TaskScore{
			Task:  autoroute.TaskType(alt.Intent),
			Score: alt.Score,
		})
	}

	return &autoroute.Classification{
		Primary:    taskType,
		Confidence: result.Confidence,
		Secondary:  secondary,
		Signals:    signals,
		Classifier: result.Layer,
		Reason:     result.Reason,
		// ExternalUsage would be populated from result.Metadata if tool layer was used
	}
}

// IntentClassifierAdapter wraps an IntentRecognizer to implement autoroute.Classifier.
// This allows the new intent system to be used as a drop-in replacement for
// HeuristicClassifier.
type IntentClassifierAdapter struct {
	recognizer IntentRecognizer
}

// NewIntentClassifierAdapter creates an adapter that wraps an IntentRecognizer.
func NewIntentClassifierAdapter(recognizer IntentRecognizer) *IntentClassifierAdapter {
	return &IntentClassifierAdapter{
		recognizer: recognizer,
	}
}

// Classify implements autoroute.Classifier.
func (a *IntentClassifierAdapter) Classify(ctx context.Context, signals autoroute.ClassificationSignals) (*autoroute.Classification, error) {
	// Convert to new signal format
	intentSignals := FromAutoRouteSignals(signals)

	// Recognize intent
	result, err := a.recognizer.Recognize(ctx, intentSignals)
	if err != nil {
		return nil, err
	}

	// Convert back to autoroute format
	return ToAutoRouteClassification(result, signals), nil
}

// Name implements autoroute.Classifier.
func (a *IntentClassifierAdapter) Name() string {
	return "intent-" + a.recognizer.Name()
}

// NewDefaultIntentRecognizer creates a complete 3-layer intent recognition pipeline
// with sensible defaults for production use.
//
// This is the recommended entry point for new deployments.
func NewDefaultIntentRecognizer() IntentRecognizer {
	// Layer 1: Rule-based
	ruleLayer := NewRuleRecognizer(
		DefaultKeywordRegistry(),
		DefaultRuleThresholds(),
	)

	// Layer 2: Context-based
	sessionHistory := NewInMemorySessionHistory()
	contextLayer := NewContextRecognizer(ruleLayer, sessionHistory)

	// Layer 3: Tool-based (optional, can be nil for cost-conscious deployments)
	// In production, replace MockLLMProvider with GLMProvider or OpenAIProvider
	var toolLayer *ToolRecognizer
	// toolLayer = NewToolRecognizer(NewMockLLMProvider())

	// Chained orchestrator
	chained := NewChainedRecognizer(
		ruleLayer,
		contextLayer,
		toolLayer,
		DefaultChainConfig(),
	)

	return chained
}

// NewIntentClassifier creates a drop-in replacement for autoroute.HeuristicClassifier
// using the new modular intent system.
//
// Usage in existing code:
//   // Old:
//   classifier := autoroute.NewHeuristicClassifier(thresholds, keywords)
//
//   // New:
//   classifier := intent.NewIntentClassifier()
func NewIntentClassifier() autoroute.Classifier {
	recognizer := NewDefaultIntentRecognizer()
	return NewIntentClassifierAdapter(recognizer)
}

// WithLLMProvider configures the chained recognizer with an LLM provider for Layer 3.
// This allows cost-conscious deployments to start with rule+context layers only,
// then add LLM capability when needed.
func WithLLMProvider(recognizer IntentRecognizer, provider LLMProvider) IntentRecognizer {
	if chained, ok := recognizer.(*ChainedRecognizer); ok {
		chained.toolLayer = NewToolRecognizer(provider)
		return chained
	}
	return recognizer
}
