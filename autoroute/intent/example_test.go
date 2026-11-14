package intent_test

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/kaixuan/llm-gateway-go/autoroute"
	"github.com/kaixuan/llm-gateway-go/autoroute/intent"
)

// Example_basicUsage demonstrates the simplest way to use the intent system.
func Example_basicUsage() {
	// Create a default recognizer (rule + context layers)
	recognizer := intent.NewDefaultIntentRecognizer()

	// Prepare signals from request
	signals := intent.RecognitionSignals{
		LastUserPrompt:  "write a function to calculate fibonacci numbers",
		MessageCount:    1,
		EstimatedTokens: 50,
		HasCodeBlock:    false,
	}

	// Recognize intent
	ctx := context.Background()
	result, err := recognizer.Recognize(ctx, signals)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("Intent: %s\n", result.Primary)
	fmt.Printf("Confidence: %.2f\n", result.Confidence)
	fmt.Printf("Layer: %s\n", result.Layer)
	// Output:
	// Intent: code
	// Confidence: 0.80
	// Layer: chained(rule)
}

// Example_withLLMLayer demonstrates adding an LLM layer for high accuracy.
func Example_withLLMLayer() {
	// Create base recognizer
	recognizer := intent.NewDefaultIntentRecognizer()

	// Add LLM provider for Layer 3 (optional, for edge cases)
	// In production, use GLMProvider or OpenAIProvider
	provider := intent.NewMockLLMProvider()
	recognizerWithLLM := intent.WithLLMProvider(recognizer, provider)

	signals := intent.RecognitionSignals{
		LastUserPrompt:  "help me with this",
		MessageCount:    1,
		EstimatedTokens: 20,
	}

	ctx := context.Background()
	result, _ := recognizerWithLLM.Recognize(ctx, signals)

	fmt.Printf("Intent: %s\n", result.Primary)
	fmt.Printf("Layer: %s\n", result.Layer)
}

// Example_backwardCompatibility shows drop-in replacement for HeuristicClassifier.
func Example_backwardCompatibility() {
	// Old code:
	// classifier := autoroute.NewHeuristicClassifier(thresholds, keywords)

	// New code (backward compatible):
	classifier := intent.NewIntentClassifier()

	// Use exactly the same API
	signals := autoroute.ClassificationSignals{
		LastUserPrompt:  "prove that sqrt(2) is irrational",
		MessageCount:    1,
		EstimatedTokens: 100,
	}

	ctx := context.Background()
	result, err := classifier.Classify(ctx, signals)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("Task: %s\n", result.Primary)
	fmt.Printf("Confidence: %.2f\n", result.Confidence)
	// Output:
	// Task: reasoning
	// Confidence: 0.40
}

// Example_sessionContext demonstrates context-aware classification.
func Example_sessionContext() {
	ruleLayer := intent.NewRuleRecognizer(
		intent.DefaultKeywordRegistry(),
		intent.DefaultRuleThresholds(),
	)

	history := intent.NewInMemorySessionHistory()
	contextLayer := intent.NewContextRecognizer(ruleLayer, history)

	config := intent.DefaultChainConfig()
	recognizer := intent.NewChainedRecognizer(ruleLayer, contextLayer, nil, config)

	ctx := context.Background()
	sessionID := "user-session-123"

	// First request: clearly code-related
	signals1 := intent.RecognitionSignals{
		SessionID:      sessionID,
		LastUserPrompt: "implement quicksort in Python",
	}
	result1, _ := recognizer.Recognize(ctx, signals1)
	history.Record(sessionID, result1.Primary)

	// Second request: ambiguous, but context suggests code
	signals2 := intent.RecognitionSignals{
		SessionID:       sessionID,
		PreviousIntent:  result1.Primary,
		LastUserPrompt:  "now add unit tests",
		MessageCount:    2,
	}
	result2, _ := recognizer.Recognize(ctx, signals2)

	fmt.Printf("First: %s\n", result1.Primary)
	fmt.Printf("Second: %s (boosted by context)\n", result2.Primary)
	// Output:
	// First: code
	// Second: code (boosted by context)
}

// Example_customKeywords demonstrates extending the keyword registry.
func Example_customKeywords() {
	// Start with defaults
	keywords := intent.DefaultKeywordRegistry()

	// Add domain-specific keywords
	keywords.Code = append(keywords.Code,
		"microservice", "kubernetes", "docker",
		"微服务", "容器编排",
	)

	keywords.Reasoning = append(keywords.Reasoning,
		"optimize", "trade-off", "complexity analysis",
		"优化分析", "权衡",
	)

	// Create recognizer with custom keywords
	recognizer := intent.NewRuleRecognizer(keywords, intent.DefaultRuleThresholds())

	signals := intent.RecognitionSignals{
		LastUserPrompt: "help me design a microservice architecture",
	}

	ctx := context.Background()
	result, _ := recognizer.Recognize(ctx, signals)

	fmt.Printf("Intent: %s\n", result.Primary)
	// Output:
	// Intent: code
}

// Example_performanceMetrics demonstrates monitoring the system.
func Example_performanceMetrics() {
	ruleLayer := intent.NewRuleRecognizer(
		intent.DefaultKeywordRegistry(),
		intent.DefaultRuleThresholds(),
	)
	contextLayer := intent.NewContextRecognizer(ruleLayer, nil)

	config := intent.ChainConfig{
		ConfidenceThreshold: 0.7,
		EnableCaching:       true,
		CacheTTL:            5 * time.Minute,
	}

	chained := intent.NewChainedRecognizer(ruleLayer, contextLayer, nil, config)

	// Process some requests...
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		signals := intent.RecognitionSignals{
			SessionID:      fmt.Sprintf("session-%d", i%3), // 3 sessions
			LastUserPrompt: "write a function",
		}
		_, _ = chained.Recognize(ctx, signals)
	}

	// Get statistics
	stats := chained.GetStats()
	cacheHitRate := float64(stats.CacheHits) / float64(stats.TotalRequests) * 100

	fmt.Printf("Total requests: %d\n", stats.TotalRequests)
	fmt.Printf("Cache hit rate: %.1f%%\n", cacheHitRate)
	fmt.Printf("Avg latency: %v\n", stats.AvgLatency)
	fmt.Printf("Rule layer: %d\n", stats.RuleLayerUsed)
	fmt.Printf("Context layer: %d\n", stats.ContextLayerUsed)
}

// Example_costOptimization shows a zero-cost configuration.
func Example_costOptimization() {
	// Rule + Context only (no LLM calls = zero external cost)
	ruleLayer := intent.NewRuleRecognizer(
		intent.DefaultKeywordRegistry(),
		intent.DefaultRuleThresholds(),
	)
	contextLayer := intent.NewContextRecognizer(ruleLayer, nil)

	config := intent.ChainConfig{
		ConfidenceThreshold: 0.65, // Accept lower confidence to avoid LLM
		EnableCaching:       true,
		CacheTTL:            5 * time.Minute, // Fixed: must be positive duration
		MaxLatency:          100 * time.Millisecond,
	}

	// No tool layer = no external costs
	recognizer := intent.NewChainedRecognizer(ruleLayer, contextLayer, nil, config)

	signals := intent.RecognitionSignals{
		LastUserPrompt: "implement a sorting algorithm",
	}

	ctx := context.Background()
	result, _ := recognizer.Recognize(ctx, signals)

	fmt.Printf("Intent: %s (cost: $0)\n", result.Primary)
	// Output:
	// Intent: code (cost: $0)
}
