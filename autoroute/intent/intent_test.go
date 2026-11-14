package intent

import (
	"context"
	"testing"
	"time"
)

func TestRuleRecognizer_HardOverrides(t *testing.T) {
	recognizer := NewRuleRecognizer(DefaultKeywordRegistry(), DefaultRuleThresholds())

	tests := []struct {
		name           string
		signals        RecognitionSignals
		expectedIntent IntentType
		minConfidence  float64
	}{
		{
			name: "vision_override",
			signals: RecognitionSignals{
				HasImages:      true,
				LastUserPrompt: "analyze this image",
			},
			expectedIntent: IntentVision,
			minConfidence:  0.90,
		},
		{
			name: "code_block_override",
			signals: RecognitionSignals{
				HasCodeBlock:   true,
				LastUserPrompt: "here's my code: ```python\ndef foo(): pass\n```",
			},
			expectedIntent: IntentCode,
			minConfidence:  0.85,
		},
		{
			name: "ide_client_override",
			signals: RecognitionSignals{
				ClientType:     "cursor",
				LastUserPrompt: "help me refactor this function",
			},
			expectedIntent: IntentCode,
			minConfidence:  0.85,
		},
		{
			name: "long_context",
			signals: RecognitionSignals{
				EstimatedTokens: 60000,
				LastUserPrompt:  "summarize this document",
			},
			expectedIntent: IntentLongContext,
			minConfidence:  0.80,
		},
		{
			name: "agent_pattern",
			signals: RecognitionSignals{
				ToolCount:      5,
				HasToolResults: true,
				LastUserPrompt: "search the web and analyze results",
			},
			expectedIntent: IntentAgent,
			minConfidence:  0.80,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			result, err := recognizer.Recognize(ctx, tt.signals)

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if result.Primary != tt.expectedIntent {
				t.Errorf("expected intent %v, got %v", tt.expectedIntent, result.Primary)
			}

			if result.Confidence < tt.minConfidence {
				t.Errorf("expected confidence >= %.2f, got %.2f", tt.minConfidence, result.Confidence)
			}

			if result.Layer != "rule" {
				t.Errorf("expected layer 'rule', got %v", result.Layer)
			}
		})
	}
}

func TestRuleRecognizer_KeywordMatching(t *testing.T) {
	recognizer := NewRuleRecognizer(DefaultKeywordRegistry(), DefaultRuleThresholds())

	tests := []struct {
		name           string
		prompt         string
		expectedIntent IntentType
	}{
		{
			name:           "reasoning_keywords",
			prompt:         "prove that the square root of 2 is irrational",
			expectedIntent: IntentReasoning,
		},
		{
			name:           "code_keywords",
			prompt:         "write a function to calculate fibonacci numbers",
			expectedIntent: IntentCode,
		},
		{
			name:           "creative_keywords",
			prompt:         "write a blog post about machine learning",
			expectedIntent: IntentCreative,
		},
		{
			name:           "chinese_reasoning",
			prompt:         "证明勾股定理",
			expectedIntent: IntentReasoning,
		},
		{
			name:           "chinese_code",
			prompt:         "实现一个快速排序算法",
			expectedIntent: IntentCode,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			signals := RecognitionSignals{
				LastUserPrompt: tt.prompt,
			}

			result, err := recognizer.Recognize(ctx, signals)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if result.Primary != tt.expectedIntent {
				t.Errorf("expected intent %v, got %v (reason: %s)",
					tt.expectedIntent, result.Primary, result.Reason)
			}
		})
	}
}

func TestContextRecognizer_SessionContinuity(t *testing.T) {
	ruleLayer := NewRuleRecognizer(DefaultKeywordRegistry(), DefaultRuleThresholds())
	history := NewInMemorySessionHistory()
	contextLayer := NewContextRecognizer(ruleLayer, history)

	// Simulate a session with multiple coding requests
	sessionID := "test-session-123"
	history.Record(sessionID, IntentCode)
	history.Record(sessionID, IntentCode)
	history.Record(sessionID, IntentCode)

	// Ambiguous request that could be code or chat
	signals := RecognitionSignals{
		SessionID:       sessionID,
		PreviousIntent:  IntentCode,
		LastUserPrompt:  "now do the same for the other function",
		MessageCount:    4,
	}

	ctx := context.Background()
	result, err := contextLayer.Recognize(ctx, signals)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should favor code intent due to session continuity
	if result.Primary != IntentCode {
		t.Errorf("expected IntentCode due to session continuity, got %v", result.Primary)
	}

	if result.Layer != "context" {
		t.Errorf("expected layer 'context', got %v", result.Layer)
	}
}

func TestChainedRecognizer_ThresholdEscalation(t *testing.T) {
	ruleLayer := NewRuleRecognizer(DefaultKeywordRegistry(), DefaultRuleThresholds())
	contextLayer := NewContextRecognizer(ruleLayer, nil)
	toolLayer := NewToolRecognizer(NewMockLLMProvider())

	config := ChainConfig{
		ConfidenceThreshold: 0.85, // High threshold to force escalation
		MaxLatency:          1 * time.Second,
		EnableCaching:       false,
	}

	chained := NewChainedRecognizer(ruleLayer, contextLayer, toolLayer, config)

	// Ambiguous request with low confidence
	signals := RecognitionSignals{
		LastUserPrompt: "help me with this",
		MessageCount:   1,
	}

	ctx := context.Background()
	result, err := chained.Recognize(ctx, signals)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should escalate through all layers
	stats := chained.GetStats()
	if stats.RuleLayerUsed == 0 {
		t.Error("expected rule layer to be used")
	}

	// Check that result came from a layer
	if result.Layer == "" {
		t.Error("expected result to have a layer identifier")
	}
}

func TestChainedRecognizer_Caching(t *testing.T) {
	ruleLayer := NewRuleRecognizer(DefaultKeywordRegistry(), DefaultRuleThresholds())
	contextLayer := NewContextRecognizer(ruleLayer, nil)

	config := ChainConfig{
		ConfidenceThreshold: 0.7,
		EnableCaching:       true,
		CacheTTL:            1 * time.Second,
	}

	chained := NewChainedRecognizer(ruleLayer, contextLayer, nil, config)

	sessionID := "cached-session"
	signals := RecognitionSignals{
		SessionID:      sessionID,
		LastUserPrompt: "write a function to sort an array",
	}

	ctx := context.Background()

	// First request
	result1, err := chained.Recognize(ctx, signals)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	stats1 := chained.GetStats()
	if stats1.CacheHits != 0 {
		t.Error("expected no cache hits on first request")
	}

	// Second request (should hit cache)
	result2, err := chained.Recognize(ctx, signals)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	stats2 := chained.GetStats()
	if stats2.CacheHits != 1 {
		t.Errorf("expected 1 cache hit, got %d", stats2.CacheHits)
	}

	if result1.Primary != result2.Primary {
		t.Error("cached result should match original")
	}

	// Wait for cache to expire
	time.Sleep(1100 * time.Millisecond)

	// Third request (cache expired)
	result3, err := chained.Recognize(ctx, signals)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	stats3 := chained.GetStats()
	if stats3.CacheHits != 1 {
		t.Error("expected cache miss after TTL expiration")
	}

	_ = result3
}

func TestInMemoryCache_TTL(t *testing.T) {
	cache := NewInMemoryCache(100 * time.Millisecond)
	defer cache.Stop()

	sessionID := "test-session"
	result := &IntentResult{
		Primary:    IntentCode,
		Confidence: 0.90,
	}

	// Store in cache
	cache.Set(sessionID, result)

	// Immediate retrieval should work
	cached, found := cache.Get(sessionID)
	if !found {
		t.Fatal("expected to find cached result")
	}
	if cached.Primary != IntentCode {
		t.Error("cached result doesn't match")
	}

	// Wait for TTL to expire
	time.Sleep(150 * time.Millisecond)

	// Should not find expired entry
	_, found = cache.Get(sessionID)
	if found {
		t.Error("expected cache entry to be expired")
	}
}

func TestSessionHistoryProvider(t *testing.T) {
	history := NewInMemorySessionHistory()
	sessionID := "test-session"

	// Record some intents
	history.Record(sessionID, IntentCode)
	history.Record(sessionID, IntentCode)
	history.Record(sessionID, IntentReasoning)
	history.Record(sessionID, IntentCode)

	// Get recent intents
	recent := history.GetRecentIntents(sessionID, 3)
	if len(recent) != 3 {
		t.Errorf("expected 3 recent intents, got %d", len(recent))
	}

	// Get frequency
	freq := history.GetIntentFrequency(sessionID)
	if freq[IntentCode] != 3 {
		t.Errorf("expected code frequency 3, got %d", freq[IntentCode])
	}
	if freq[IntentReasoning] != 1 {
		t.Errorf("expected reasoning frequency 1, got %d", freq[IntentReasoning])
	}

	// Clear history
	history.Clear(sessionID)
	recent = history.GetRecentIntents(sessionID, 10)
	if len(recent) != 0 {
		t.Error("expected empty history after clear")
	}
}

func BenchmarkRuleRecognizer(b *testing.B) {
	recognizer := NewRuleRecognizer(DefaultKeywordRegistry(), DefaultRuleThresholds())
	ctx := context.Background()
	signals := RecognitionSignals{
		LastUserPrompt:  "write a function to calculate fibonacci numbers",
		MessageCount:    1,
		EstimatedTokens: 100,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = recognizer.Recognize(ctx, signals)
	}
}

func BenchmarkContextRecognizer(b *testing.B) {
	ruleLayer := NewRuleRecognizer(DefaultKeywordRegistry(), DefaultRuleThresholds())
	history := NewInMemorySessionHistory()
	contextLayer := NewContextRecognizer(ruleLayer, history)

	ctx := context.Background()
	signals := RecognitionSignals{
		SessionID:       "bench-session",
		LastUserPrompt:  "help me debug this code",
		MessageCount:    5,
		EstimatedTokens: 200,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = contextLayer.Recognize(ctx, signals)
	}
}

func BenchmarkChainedRecognizer_WithCache(b *testing.B) {
	ruleLayer := NewRuleRecognizer(DefaultKeywordRegistry(), DefaultRuleThresholds())
	contextLayer := NewContextRecognizer(ruleLayer, nil)
	config := DefaultChainConfig()
	config.EnableCaching = true

	chained := NewChainedRecognizer(ruleLayer, contextLayer, nil, config)

	ctx := context.Background()
	signals := RecognitionSignals{
		SessionID:       "bench-session",
		LastUserPrompt:  "implement quicksort algorithm",
		MessageCount:    1,
		EstimatedTokens: 50,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = chained.Recognize(ctx, signals)
	}
}
