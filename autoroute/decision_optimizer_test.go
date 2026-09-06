package autoroute

import (
	"context"
	"errors"
	"testing"

	"github.com/kaixuan/llm-gateway-go/routingopt"
)

// TestDecider_WithOptimizer_NoOp verifies that injecting a no-op optimizer
// does not change routing behavior (transparency test).
func TestDecider_WithOptimizer_NoOp(t *testing.T) {
	cls := &stubClassifier{
		name: "heuristic",
		out: &Classification{
			Primary:    TaskCode,
			Confidence: 0.9,
			Classifier: "heuristic",
			Reason:     "code keyword detected",
		},
	}
	idx := &stubIndex{
		cands: []ScoredCandidate{{
			Candidate: Candidate{
				CanonicalName: "claude-sonnet-4",
				CredentialID:  7,
				RawModel:      "claude-sonnet-4",
			},
			Breakdown: ScoringBreakdown{Composite: 85},
		}},
	}

	d := NewDecider(cls, nil, idx, NewMemoryProfileStore())
	d.TopN = 3
	d.SetOptimizer(routingopt.NewDefaultOptimizer()) // inject no-op optimizer

	sigs := ClassificationSignals{
		MessageCount:    10,
		EstimatedTokens: 500,
		ToolCount:       0,
		HasImages:       false,
	}

	dec, err := d.Decide(context.Background(), sigs, 42, "", "", "")
	if err != nil {
		t.Fatalf("Decide with optimizer should not fail: %v", err)
	}

	// Verify routing result unchanged (no-op optimizer is transparent)
	if dec.ChosenModel != "claude-sonnet-4" {
		t.Fatalf("optimizer should not change model selection in no-op mode: got %s", dec.ChosenModel)
	}
	if dec.TaskType != TaskCode {
		t.Fatalf("optimizer should not change task type in no-op mode: got %s", dec.TaskType)
	}
	if dec.Confidence != 0.9 {
		t.Fatalf("optimizer should not change confidence in no-op mode: got %f", dec.Confidence)
	}
	if dec.Classifier != "heuristic" {
		t.Fatalf("optimizer should not change classifier in no-op mode: got %s", dec.Classifier)
	}
}

// TestDecider_WithoutOptimizer_NoChange verifies that Decider without an
// optimizer (optimizer=nil) behaves identically to pre-P2.2 baseline.
func TestDecider_WithoutOptimizer_NoChange(t *testing.T) {
	cls := &stubClassifier{
		name: "heuristic",
		out: &Classification{
			Primary:    TaskChat,
			Confidence: 0.85,
		},
	}
	idx := &stubIndex{
		cands: []ScoredCandidate{{
			Candidate: Candidate{
				CanonicalName: "gpt-4o",
				CredentialID:  3,
				RawModel:      "gpt-4o-2024-11-20",
			},
		}},
	}

	d := NewDecider(cls, nil, idx, NewMemoryProfileStore())
	// Do NOT inject optimizer (optimizer field remains nil)

	dec, err := d.Decide(context.Background(), ClassificationSignals{}, 42, "", "", "")
	if err != nil {
		t.Fatalf("Decide without optimizer should not fail: %v", err)
	}

	// Verify baseline behavior
	if dec.ChosenModel != "gpt-4o" {
		t.Fatalf("without optimizer, model selection should be unchanged: got %s", dec.ChosenModel)
	}
	if dec.TaskType != TaskChat {
		t.Fatalf("without optimizer, task type should be unchanged: got %s", dec.TaskType)
	}
	if dec.Confidence != 0.85 {
		t.Fatalf("without optimizer, confidence should be unchanged: got %f", dec.Confidence)
	}
}

// TestDecider_OptimizerError_FallsBackToBaseline verifies that plugin errors
// (PreClassify, PostClassify failures) are logged but do not block routing.
func TestDecider_OptimizerError_FallsBackToBaseline(t *testing.T) {
	cls := &stubClassifier{
		name: "heuristic",
		out: &Classification{
			Primary:    TaskReasoning,
			Confidence: 0.75,
		},
	}
	idx := &stubIndex{
		cands: []ScoredCandidate{{
			Candidate: Candidate{CanonicalName: "test-model"},
		}},
	}

	// Create a failing optimizer (returns errors from all methods)
	failingOpt := &failingOptimizer{}

	d := NewDecider(cls, nil, idx, NewMemoryProfileStore())
	d.SetOptimizer(failingOpt)

	dec, err := d.Decide(context.Background(), ClassificationSignals{}, 42, "", "", "")
	if err != nil {
		t.Fatalf("Decide should not fail when optimizer fails (fallback to baseline): %v", err)
	}

	// Verify routing succeeded with baseline behavior (plugin errors ignored)
	if dec.TaskType != TaskReasoning {
		t.Fatalf("on optimizer failure, baseline task type should be used: got %s", dec.TaskType)
	}
	if dec.Confidence != 0.75 {
		t.Fatalf("on optimizer failure, baseline confidence should be used: got %f", dec.Confidence)
	}
	if dec.ChosenModel != "test-model" {
		t.Fatalf("on optimizer failure, baseline model should be chosen: got %s", dec.ChosenModel)
	}
}

// TestDecider_SessionCache_BypassesOptimizer verifies that cached session
// intents skip classification and optimizer hooks (performance optimization).
func TestDecider_SessionCache_BypassesOptimizer(t *testing.T) {
	cls := &stubClassifier{
		name: "heuristic",
		out:  &Classification{Primary: TaskCode, Confidence: 0.9},
	}
	idx := &stubIndex{
		cands: []ScoredCandidate{{
			Candidate: Candidate{CanonicalName: "cached-model"},
		}},
	}

	d := NewDecider(cls, nil, idx, NewMemoryProfileStore())
	d.SetOptimizer(routingopt.NewDefaultOptimizer())

	// First request: cache miss, goes through optimizer
	sessionID := "test-session-123"
	dec1, err := d.Decide(context.Background(), ClassificationSignals{}, 42, "", "", sessionID)
	if err != nil {
		t.Fatalf("first request should succeed: %v", err)
	}

	// Second request: cache hit, bypasses optimizer
	dec2, err := d.Decide(context.Background(), ClassificationSignals{}, 42, "", "", sessionID)
	if err != nil {
		t.Fatalf("cached request should succeed: %v", err)
	}

	// Verify session cache was used
	if dec2.Classifier != "session_cache" {
		t.Fatalf("cached request should have Classifier=session_cache: got %s", dec2.Classifier)
	}
	if dec2.ChosenModel != dec1.ChosenModel {
		t.Fatalf("cached request should reuse first decision's model: got %s, want %s",
			dec2.ChosenModel, dec1.ChosenModel)
	}
}

// TestDecider_OptimizerNil_ZeroOverhead verifies that when optimizer=nil,
// the plugin hooks have zero overhead (single nil check, no method calls).
func TestDecider_OptimizerNil_ZeroOverhead(t *testing.T) {
	cls := &stubClassifier{
		name: "heuristic",
		out:  &Classification{Primary: TaskChat, Confidence: 0.8},
	}
	idx := &stubIndex{
		cands: []ScoredCandidate{{Candidate: Candidate{CanonicalName: "baseline-model"}}},
	}

	d := NewDecider(cls, nil, idx, NewMemoryProfileStore())
	// optimizer field is nil (default)

	// Decide should succeed without calling any optimizer methods
	dec, err := d.Decide(context.Background(), ClassificationSignals{}, 42, "", "", "")
	if err != nil {
		t.Fatalf("Decide with nil optimizer should succeed: %v", err)
	}
	if dec.ChosenModel != "baseline-model" {
		t.Fatalf("nil optimizer should not affect routing: got %s", dec.ChosenModel)
	}
}

// failingOptimizer is a test stub that returns errors from all methods.
type failingOptimizer struct{}

func (f *failingOptimizer) PreClassify(ctx context.Context, signals interface{}) (interface{}, error) {
	return nil, errors.New("simulated timeout")
}

func (f *failingOptimizer) PostClassify(ctx context.Context, taskType string, confidence float64) (float64, error) {
	return 0, errors.New("simulated cancellation")
}

func (f *failingOptimizer) RecommendModel(ctx context.Context, candidates interface{}, context interface{}) (interface{}, error) {
	return nil, errors.New("simulated timeout")
}

func (f *failingOptimizer) RecordFeedback(ctx context.Context, feedback interface{}) error {
	return errors.New("simulated cancellation")
}

func (f *failingOptimizer) GetStats(ctx context.Context) (interface{}, error) {
	return nil, errors.New("simulated timeout")
}
