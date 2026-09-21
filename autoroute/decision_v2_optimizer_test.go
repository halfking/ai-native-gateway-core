package autoroute

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/routingopt"
)

// TestDecideV2_WithOptimizer_NoOp verifies that injecting a no-op optimizer
// into V2 path does not change routing behavior (transparency test).
func TestDecideV2_WithOptimizer_NoOp(t *testing.T) {
	cls := &stubClassifier{
		name: "heuristic",
		out: &Classification{
			Primary:    TaskCode,
			Confidence: 0.9,
			Classifier: "heuristic",
			Reason:     "code keyword detected",
		},
	}

	// Create real Index for V2 path
	idx := NewIndex()
	idx.entries = []Candidate{{
		CanonicalName: "claude-sonnet-4",
		CredentialID:  7,
		CanonicalID:   1,
		RawModel:      "claude-sonnet-4",
		Tags:          []string{"code"},
	}}

	d := NewDecider(cls, nil, idx, NewMemoryProfileStore())
	d.TopN = 3
	d.SetOptimizer(routingopt.NewDefaultOptimizer()) // inject no-op optimizer

	sigs := ClassificationSignals{
		MessageCount:    10,
		EstimatedTokens: 500,
		ToolCount:       0,
		HasImages:       false,
	}

	// Force V2 path via UseChannelQualityRouting
	oldFlags := GetFeatureFlags()
	defer SetGlobalFeatureFlagsForTest(oldFlags)
	SetGlobalFeatureFlagsForTest(&FeatureFlags{
		UseChannelQualityRouting: true,
	})

	dec, err := d.DecideV2(context.Background(), sigs, 42, "", "", "")
	if err != nil {
		t.Fatalf("DecideV2 with optimizer should not fail: %v", err)
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
}

// TestDecideV2_PreClassifyHook_SignalEnhancement verifies PreClassify hook
// is called in V2 path and signal enhancements are properly extracted.
func TestDecideV2_PreClassifyHook_SignalEnhancement(t *testing.T) {
	cls := &stubClassifier{
		name: "heuristic",
		out: &Classification{
			Primary:    TaskChat,
			Confidence: 0.8,
			Classifier: "heuristic",
		},
	}

	idx := NewIndex()
	idx.entries = []Candidate{{
		CanonicalName: "gpt-4o",
		CredentialID:  3,
		CanonicalID:   1,
		RawModel:      "gpt-4o",
		Tags:          []string{"chat"},
	}}

	d := NewDecider(cls, nil, idx, NewMemoryProfileStore())
	enhancer := &trackingOptimizer{}
	d.SetOptimizer(enhancer)

	oldFlags := GetFeatureFlags()
	defer SetGlobalFeatureFlagsForTest(oldFlags)
	SetGlobalFeatureFlagsForTest(&FeatureFlags{
		UseChannelQualityRouting: true,
	})

	_, err := d.DecideV2(context.Background(), ClassificationSignals{}, 42, "", "", "")
	if err != nil {
		t.Fatalf("DecideV2 should not fail: %v", err)
	}

	enhancer.mu.Lock()
	preClassifyCalled := enhancer.preClassifyCalls
	enhancer.mu.Unlock()

	if preClassifyCalled != 1 {
		t.Fatalf("PreClassify should be called once in V2 path, got %d", preClassifyCalled)
	}
}

// TestDecideV2_PostClassifyHook_ConfidenceAdjustment verifies PostClassify
// hook adjusts confidence in V2 path.
func TestDecideV2_PostClassifyHook_ConfidenceAdjustment(t *testing.T) {
	cls := &stubClassifier{
		name: "heuristic",
		out: &Classification{
			Primary:    TaskReasoning,
			Confidence: 0.7,
			Classifier: "heuristic",
		},
	}

	idx := NewIndex()
	idx.entries = []Candidate{{
		CanonicalName: "test-model",
		CredentialID:  1,
		CanonicalID:   1,
		RawModel:      "test-model",
		Tags:          []string{"reasoning"},
	}}

	d := NewDecider(cls, nil, idx, NewMemoryProfileStore())
	adjuster := &confidenceAdjustingOptimizer{boost: 0.2}
	d.SetOptimizer(adjuster)

	oldFlags := GetFeatureFlags()
	defer SetGlobalFeatureFlagsForTest(oldFlags)
	SetGlobalFeatureFlagsForTest(&FeatureFlags{
		UseChannelQualityRouting: true,
	})

	dec, err := d.DecideV2(context.Background(), ClassificationSignals{}, 42, "", "", "")
	if err != nil {
		t.Fatalf("DecideV2 should not fail: %v", err)
	}

	// Confidence should be boosted from 0.7 to 0.9
	expected := 0.9
	if diff := dec.Confidence - expected; diff < -0.0001 || diff > 0.0001 {
		t.Fatalf("PostClassify should adjust confidence to 0.9, got %f", dec.Confidence)
	}
}

// TestDecideV2_RecommendModelHook_ReordersWinner proves the RecommendModel
// hook is wired into DecideV2: reversing candidate list must flip the winner.
func TestDecideV2_RecommendModelHook_ReordersWinner(t *testing.T) {
	cls := &stubClassifier{
		name: "heuristic",
		out: &Classification{
			Primary:    TaskCode,
			Confidence: 0.9,
			Classifier: "heuristic",
		},
	}

	idx := NewIndex()
	idx.entries = []Candidate{
		{
			CanonicalName: "model-a",
			CredentialID:  1,
			CanonicalID:   1,
			RawModel:      "model-a",
			Tags:          []string{"code"},
		},
		{
			CanonicalName: "model-b",
			CredentialID:  2,
			CanonicalID:   2,
			RawModel:      "model-b",
			Tags:          []string{"code"},
		},
	}

	d := NewDecider(cls, nil, idx, NewMemoryProfileStore())
	ranker := &rankingOptimizer{}
	d.SetOptimizer(ranker)

	oldFlags := GetFeatureFlags()
	defer SetGlobalFeatureFlagsForTest(oldFlags)
	SetGlobalFeatureFlagsForTest(&FeatureFlags{
		UseChannelQualityRouting: true,
	})

	dec, err := d.DecideV2(context.Background(), ClassificationSignals{}, 42, "", "", "")
	if err != nil {
		t.Fatalf("DecideV2 failed: %v", err)
	}

	ranker.mu.Lock()
	calls := ranker.calls
	ranker.mu.Unlock()

	if calls != 1 {
		t.Fatalf("RecommendModel should be called exactly once in V2 path, got %d", calls)
	}
	if dec.ChosenModel != "model-b" {
		t.Fatalf("reversed ranking should pick model-b, got %s", dec.ChosenModel)
	}
}

// TestDecideV2_RecordFeedbackHook_CarriesRequestIdentity proves the feedback
// hook fires on fresh V2 decisions with proper identity metadata.
func TestDecideV2_RecordFeedbackHook_CarriesRequestIdentity(t *testing.T) {
	cls := &stubClassifier{
		name: "heuristic",
		out: &Classification{
			Primary:    TaskChat,
			Confidence: 0.8,
			Classifier: "heuristic",
		},
	}

	idx := NewIndex()
	idx.entries = []Candidate{{
		CanonicalName: "model-a",
		CredentialID:  1,
		CanonicalID:   1,
		RawModel:      "model-a",
		Tags:          []string{"chat"},
	}}

	d := NewDecider(cls, nil, idx, NewMemoryProfileStore())
	ranker := &rankingOptimizer{}
	d.SetOptimizer(ranker)

	oldFlags := GetFeatureFlags()
	defer SetGlobalFeatureFlagsForTest(oldFlags)
	SetGlobalFeatureFlagsForTest(&FeatureFlags{
		UseChannelQualityRouting: true,
	})

	if _, err := d.DecideV2(context.Background(), ClassificationSignals{}, 42, "", "", "sess-v2"); err != nil {
		t.Fatalf("DecideV2 failed: %v", err)
	}

	// The feedback write is async — poll briefly for it to land
	deadline := time.Now().Add(2 * time.Second)
	for {
		ranker.mu.Lock()
		n := len(ranker.feedbacks)
		var fb routingopt.RoutingFeedback
		if n > 0 {
			fb = ranker.feedbacks[0]
		}
		ranker.mu.Unlock()
		if n > 0 {
			if fb.UserID != 42 {
				t.Fatalf("V2 feedback should carry UserID=42, got %d", fb.UserID)
			}
			if fb.SessionID != "sess-v2" {
				t.Fatalf("V2 feedback should carry SessionID=sess-v2, got %q", fb.SessionID)
			}
			if fb.TaskType != string(TaskChat) || fb.PredictedProvider != "model-a" {
				t.Fatalf("unexpected V2 feedback: %+v", fb)
			}
			if fb.RequestID == "" {
				t.Fatal("V2 feedback RequestID must not be empty")
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("RecordFeedback was not called within 2s in V2 path")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestDecideV2_OptimizerError_FallsBackToBaseline verifies plugin errors
// in V2 path are logged but do not block routing.
func TestDecideV2_OptimizerError_FallsBackToBaseline(t *testing.T) {
	cls := &stubClassifier{
		name: "heuristic",
		out: &Classification{
			Primary:    TaskReasoning,
			Confidence: 0.75,
			Classifier: "heuristic",
		},
	}

	idx := NewIndex()
	idx.entries = []Candidate{{
		CanonicalName: "test-model",
		CredentialID:  1,
		CanonicalID:   1,
		RawModel:      "test-model",
		Tags:          []string{"reasoning"},
	}}

	d := NewDecider(cls, nil, idx, NewMemoryProfileStore())
	failingOpt := &failingOptimizer{}
	d.SetOptimizer(failingOpt)

	oldFlags := GetFeatureFlags()
	defer SetGlobalFeatureFlagsForTest(oldFlags)
	SetGlobalFeatureFlagsForTest(&FeatureFlags{
		UseChannelQualityRouting: true,
	})

	dec, err := d.DecideV2(context.Background(), ClassificationSignals{}, 42, "", "", "")
	if err != nil {
		t.Fatalf("DecideV2 should not fail when optimizer fails (fallback to baseline): %v", err)
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

// trackingOptimizer records hook call counts for verification.
type trackingOptimizer struct {
	mu                sync.Mutex
	preClassifyCalls  int
	postClassifyCalls int
	recommendCalls    int
	feedbackCalls     int
}

func (t *trackingOptimizer) PreClassify(ctx context.Context, signals interface{}) (interface{}, error) {
	t.mu.Lock()
	t.preClassifyCalls++
	t.mu.Unlock()
	return nil, nil
}

func (t *trackingOptimizer) PostClassify(ctx context.Context, taskType string, confidence float64) (float64, error) {
	t.mu.Lock()
	t.postClassifyCalls++
	t.mu.Unlock()
	return confidence, nil
}

func (t *trackingOptimizer) RecommendModel(ctx context.Context, candidates interface{}, context interface{}) (interface{}, error) {
	t.mu.Lock()
	t.recommendCalls++
	t.mu.Unlock()
	return candidates, nil
}

func (t *trackingOptimizer) RecordFeedback(ctx context.Context, feedback interface{}) error {
	t.mu.Lock()
	t.feedbackCalls++
	t.mu.Unlock()
	return nil
}

func (t *trackingOptimizer) GetStats(ctx context.Context) (interface{}, error) {
	return nil, nil
}

// confidenceAdjustingOptimizer boosts confidence by a fixed amount.
type confidenceAdjustingOptimizer struct {
	boost float64
}

func (c *confidenceAdjustingOptimizer) PreClassify(ctx context.Context, signals interface{}) (interface{}, error) {
	return nil, nil
}

func (c *confidenceAdjustingOptimizer) PostClassify(ctx context.Context, taskType string, confidence float64) (float64, error) {
	adjusted := confidence + c.boost
	if adjusted > 1.0 {
		adjusted = 1.0
	}
	return adjusted, nil
}

func (c *confidenceAdjustingOptimizer) RecommendModel(ctx context.Context, candidates interface{}, context interface{}) (interface{}, error) {
	return candidates, nil
}

func (c *confidenceAdjustingOptimizer) RecordFeedback(ctx context.Context, feedback interface{}) error {
	return nil
}

func (c *confidenceAdjustingOptimizer) GetStats(ctx context.Context) (interface{}, error) {
	return nil, nil
}
