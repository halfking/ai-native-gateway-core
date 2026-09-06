package routingopt

import (
	"context"
	"testing"
	"time"
)

// mockClassificationSignals mimics autoroute.ClassificationSignals without importing autoroute
type mockClassificationSignals struct {
	MessageCount    int
	EstimatedTokens int
	ToolCount       int
	HasImages       bool
	Language        string
}

// TestDefaultOptimizer_NoOp verifies that DefaultOptimizer passes through
// all inputs unchanged (no-op behavior).
func TestDefaultOptimizer_NoOp(t *testing.T) {
	opt := NewDefaultOptimizer()

	// Test PreClassify: should wrap original signals without enhancements
	t.Run("PreClassify", func(t *testing.T) {
		sigs := &mockClassificationSignals{
			MessageCount:    5,
			EstimatedTokens: 1000,
			ToolCount:       2,
			HasImages:       false,
			Language:        "en",
		}
		enhanced, err := opt.PreClassify(context.Background(), sigs)
		if err != nil {
			t.Fatalf("PreClassify should not fail: %v", err)
		}
		if enhanced == nil {
			t.Fatal("PreClassify should return non-nil EnhancedSignals")
		}
		e, ok := enhanced.(*EnhancedSignals)
		if !ok {
			t.Fatal("PreClassify should return *EnhancedSignals")
		}
		if e.Original != sigs {
			t.Fatalf("PreClassify should preserve original signals pointer")
		}
		if e.UserAffinities != nil {
			t.Fatalf("PreClassify should not add affinities in no-op mode")
		}
		if e.SessionMode != "unknown" {
			t.Fatalf("PreClassify should set SessionMode=unknown, got %s", e.SessionMode)
		}
	})

	// Test PostClassify: should return original confidence unchanged
	t.Run("PostClassify", func(t *testing.T) {
		testCases := []struct {
			name       string
			taskType   string
			confidence float64
		}{
			{"code-high", "code", 0.95},
			{"chat-low", "chat", 0.45},
			{"reasoning-mid", "reasoning", 0.72},
			{"boundary-zero", "agent", 0.0},
			{"boundary-one", "creative", 1.0},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				conf, err := opt.PostClassify(context.Background(), tc.taskType, tc.confidence)
				if err != nil {
					t.Fatalf("PostClassify should not fail: %v", err)
				}
				if conf != tc.confidence {
					t.Fatalf("PostClassify should return original confidence: got %f, want %f", conf, tc.confidence)
				}
			})
		}
	})

	// Test RecommendModel: should return original candidates unchanged
	t.Run("RecommendModel", func(t *testing.T) {
		candidates := []ModelCandidate{
			{CanonicalName: "claude-sonnet-4", Score: 0.95, Provider: "anthropic"},
			{CanonicalName: "gpt-4o", Score: 0.88, Provider: "openai"},
			{CanonicalName: "gemini-2.0-flash", Score: 0.82, Provider: "google"},
		}
		ctx := RoutingContext{
			TaskType: "code",
			Profile:  "smart",
			UserID:   123,
		}

		result, err := opt.RecommendModel(context.Background(), candidates, ctx)
		if err != nil {
			t.Fatalf("RecommendModel should not fail: %v", err)
		}
		resultCands, ok := result.([]ModelCandidate)
		if !ok {
			t.Fatalf("RecommendModel should return []ModelCandidate")
		}
		if len(resultCands) != len(candidates) {
			t.Fatalf("RecommendModel should preserve candidate count: got %d, want %d", len(resultCands), len(candidates))
		}

		// Verify order and content unchanged
		for i, cand := range resultCands {
			if cand.CanonicalName != candidates[i].CanonicalName {
				t.Fatalf("RecommendModel should preserve order: position %d got %s, want %s",
					i, cand.CanonicalName, candidates[i].CanonicalName)
			}
			if cand.Score != candidates[i].Score {
				t.Fatalf("RecommendModel should preserve scores: %s got %f, want %f",
					cand.CanonicalName, cand.Score, candidates[i].Score)
			}
		}
	})

	// Test RecordFeedback: should succeed but discard feedback (no persistence)
	t.Run("RecordFeedback", func(t *testing.T) {
		feedback := RoutingFeedback{
			RequestID:         "test-req-123",
			TaskType:          "code",
			PredictedProvider: "anthropic",
			ActualProvider:    "anthropic",
			IsSuccess:         true,
			Latency:           150 * time.Millisecond,
			Cost:              0.003,
			HumanCorrection:   nil,
		}

		err := opt.RecordFeedback(context.Background(), feedback)
		if err != nil {
			t.Fatalf("RecordFeedback should not fail in no-op mode: %v", err)
		}
	})

	// Test GetStats: should return empty statistics
	t.Run("GetStats", func(t *testing.T) {
		result, err := opt.GetStats(context.Background())
		if err != nil {
			t.Fatalf("GetStats should not fail: %v", err)
		}
		stats, ok := result.(*OptimizerStats)
		if !ok {
			t.Fatal("GetStats should return *OptimizerStats")
		}
		if stats.OverallAccuracy != 0.0 {
			t.Fatalf("GetStats should return zero accuracy in no-op mode: got %f", stats.OverallAccuracy)
		}
		if stats.ParameterVersion != 0 {
			t.Fatalf("GetStats should return zero version in no-op mode: got %d", stats.ParameterVersion)
		}
		if !stats.LastUpdated.IsZero() {
			t.Fatalf("GetStats should return zero time in no-op mode: got %v", stats.LastUpdated)
		}
		if stats.HumanAnnotationsUsed != 0 {
			t.Fatalf("GetStats should return zero annotations in no-op mode: got %d", stats.HumanAnnotationsUsed)
		}
	})
}

// TestEnhancedSignals_GetOriginal verifies that EnhancedSignals implements
// the GetOriginal() interface expected by autoroute.Decider.
func TestEnhancedSignals_GetOriginal(t *testing.T) {
	original := &mockClassificationSignals{
		MessageCount: 10,
		ToolCount:    3,
	}

	enhanced := &EnhancedSignals{
		Original:       original,
		UserAffinities: map[string]float64{"code": 0.6},
		SessionMode:    "ide",
		TimeContext:    TimeContext{IsPeakHour: true, Hour: 14},
	}

	// Verify GetOriginal() returns the original signals
	got := enhanced.GetOriginal()
	if got != original {
		t.Fatalf("GetOriginal() should return original signals pointer")
	}
	gotSigs, ok := got.(*mockClassificationSignals)
	if !ok {
		t.Fatal("GetOriginal() should return original type")
	}
	if gotSigs.MessageCount != 10 {
		t.Fatalf("GetOriginal() returned wrong signals: got MessageCount=%d", gotSigs.MessageCount)
	}
}

// TestDefaultOptimizer_ContextCancellation verifies that the optimizer respects
// context cancellation (best-effort, does not block).
func TestDefaultOptimizer_ContextCancellation(t *testing.T) {
	opt := NewDefaultOptimizer()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // immediately cancel

	// All methods should still succeed (no-op has no I/O to cancel)
	sigs := &mockClassificationSignals{}
	_, err := opt.PreClassify(ctx, sigs)
	if err != nil {
		t.Fatalf("PreClassify should not fail on cancelled context in no-op mode: %v", err)
	}

	_, err = opt.PostClassify(ctx, "chat", 0.8)
	if err != nil {
		t.Fatalf("PostClassify should not fail on cancelled context in no-op mode: %v", err)
	}

	_, err = opt.RecommendModel(ctx, []ModelCandidate{}, RoutingContext{})
	if err != nil {
		t.Fatalf("RecommendModel should not fail on cancelled context in no-op mode: %v", err)
	}

	err = opt.RecordFeedback(ctx, RoutingFeedback{})
	if err != nil {
		t.Fatalf("RecordFeedback should not fail on cancelled context in no-op mode: %v", err)
	}

	_, err = opt.GetStats(ctx)
	if err != nil {
		t.Fatalf("GetStats should not fail on cancelled context in no-op mode: %v", err)
	}
}
