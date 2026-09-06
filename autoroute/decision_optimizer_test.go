package autoroute

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

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

// rankingOptimizer reverses the candidate order and records hook calls.
// It proves the RecommendModel hook is actually wired into Decide and that
// the re-ranking changes the chosen model.
type rankingOptimizer struct {
	mu         sync.Mutex
	calls      int
	feedbacks  []routingopt.RoutingFeedback
	exploreAll bool // when true, RecommendModel returns an error to test fallback
}

func (r *rankingOptimizer) PreClassify(ctx context.Context, signals interface{}) (interface{}, error) {
	return nil, nil
}

func (r *rankingOptimizer) PostClassify(ctx context.Context, taskType string, confidence float64) (float64, error) {
	return confidence, nil
}

func (r *rankingOptimizer) RecommendModel(ctx context.Context, candidates interface{}, context interface{}) (interface{}, error) {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	if r.exploreAll {
		return nil, errors.New("ranker down")
	}
	cands := candidates.([]routingopt.ModelCandidate)
	out := make([]routingopt.ModelCandidate, len(cands))
	for i, c := range cands {
		out[len(cands)-1-i] = c
	}
	return out, nil
}

func (r *rankingOptimizer) RecordFeedback(ctx context.Context, feedback interface{}) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.feedbacks = append(r.feedbacks, feedback.(routingopt.RoutingFeedback))
	return nil
}

func (r *rankingOptimizer) GetStats(ctx context.Context) (interface{}, error) {
	return nil, nil
}

// TestDecide_RecommendModelHook_ReordersWinner proves the P2.2 RecommendModel
// hook is wired into Decide: reversing the candidate list must flip the winner.
func TestDecide_RecommendModelHook_ReordersWinner(t *testing.T) {
	cls := &stubClassifier{name: "heuristic", out: &Classification{
		Primary: TaskCode, Confidence: 0.9, Classifier: "heuristic",
	}}
	idx := &stubIndex{cands: []ScoredCandidate{
		{Candidate: Candidate{CanonicalName: "model-a", CredentialID: 1, RawModel: "model-a"},
			Breakdown: ScoringBreakdown{Composite: 90, PriceScore: 80, SpeedScore: 70, Reliability: 95}},
		{Candidate: Candidate{CanonicalName: "model-b", CredentialID: 2, RawModel: "model-b"},
			Breakdown: ScoringBreakdown{Composite: 85, PriceScore: 60, SpeedScore: 90, Reliability: 80}},
	}}

	d := NewDecider(cls, nil, idx, NewMemoryProfileStore())
	ranker := &rankingOptimizer{}
	d.SetOptimizer(ranker)

	dec, err := d.Decide(context.Background(), ClassificationSignals{}, 42, "", "", "")
	if err != nil {
		t.Fatalf("Decide failed: %v", err)
	}

	ranker.mu.Lock()
	calls := ranker.calls
	ranker.mu.Unlock()

	if calls != 1 {
		t.Fatalf("RecommendModel should be called exactly once, got %d", calls)
	}
	if dec.ChosenModel != "model-b" {
		t.Fatalf("reversed ranking should pick model-b, got %s", dec.ChosenModel)
	}
	if len(dec.CandidatesTopN) != 2 || dec.CandidatesTopN[0].Candidate.CanonicalName != "model-b" {
		t.Fatalf("CandidatesTopN should follow the optimizer order, got %+v", dec.CandidatesTopN)
	}
	// Breakdown must survive the round-trip (winner is original model-b row).
	if got := dec.CandidatesTopN[0].Breakdown.Composite; got != 85 {
		t.Fatalf("Breakdown should be preserved through the bridge, got %v", got)
	}
}

// TestDecide_RecommendModelHook_ErrorKeepsIndexOrder proves a plugin failure
// degrades to the index order instead of breaking routing.
func TestDecide_RecommendModelHook_ErrorKeepsIndexOrder(t *testing.T) {
	cls := &stubClassifier{name: "heuristic", out: &Classification{
		Primary: TaskCode, Confidence: 0.9, Classifier: "heuristic",
	}}
	idx := &stubIndex{cands: []ScoredCandidate{
		{Candidate: Candidate{CanonicalName: "model-a", CredentialID: 1, RawModel: "model-a"}},
		{Candidate: Candidate{CanonicalName: "model-b", CredentialID: 2, RawModel: "model-b"}},
	}}

	d := NewDecider(cls, nil, idx, NewMemoryProfileStore())
	ranker := &rankingOptimizer{exploreAll: true}
	d.SetOptimizer(ranker)

	dec, err := d.Decide(context.Background(), ClassificationSignals{}, 42, "", "", "")
	if err != nil {
		t.Fatalf("plugin error must not fail Decide: %v", err)
	}
	if dec.ChosenModel != "model-a" {
		t.Fatalf("on plugin error the index winner must be kept, got %s", dec.ChosenModel)
	}
}

// TestDecide_RecordFeedbackHook_CarriesRequestIdentity proves the feedback
// hook fires on fresh decisions with the API-key/session identity attached.
func TestDecide_RecordFeedbackHook_CarriesRequestIdentity(t *testing.T) {
	cls := &stubClassifier{name: "heuristic", out: &Classification{
		Primary: TaskChat, Confidence: 0.8, Classifier: "heuristic",
	}}
	idx := &stubIndex{cands: []ScoredCandidate{
		{Candidate: Candidate{CanonicalName: "model-a", CredentialID: 1, RawModel: "model-a"}},
	}}

	d := NewDecider(cls, nil, idx, NewMemoryProfileStore())
	ranker := &rankingOptimizer{}
	d.SetOptimizer(ranker)

	if _, err := d.Decide(context.Background(), ClassificationSignals{}, 42, "", "", "sess-1"); err != nil {
		t.Fatalf("Decide failed: %v", err)
	}

	// The feedback write is async — poll briefly for it to land.
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
				t.Fatalf("feedback should carry UserID=42, got %d", fb.UserID)
			}
			if fb.SessionID != "sess-1" {
				t.Fatalf("feedback should carry SessionID=sess-1, got %q", fb.SessionID)
			}
			if fb.TaskType != string(TaskChat) || fb.PredictedProvider != "model-a" {
				t.Fatalf("unexpected feedback: %+v", fb)
			}
			if fb.RequestID == "" {
				t.Fatal("feedback RequestID must not be empty")
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("RecordFeedback was not called within 2s")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestDecide_SessionCacheHit_SkipsFeedback proves cache hits do not
// double-count feedback for a reused decision.
func TestDecide_SessionCacheHit_SkipsFeedback(t *testing.T) {
	cls := &stubClassifier{name: "heuristic", out: &Classification{
		Primary: TaskCode, Confidence: 0.9, Classifier: "heuristic",
	}}
	idx := &stubIndex{cands: []ScoredCandidate{
		{Candidate: Candidate{CanonicalName: "model-a", CredentialID: 1, RawModel: "model-a"}},
	}}

	d := NewDecider(cls, nil, idx, NewMemoryProfileStore())
	ranker := &rankingOptimizer{}
	d.SetOptimizer(ranker)

	if _, err := d.Decide(context.Background(), ClassificationSignals{}, 7, "", "", "sess-2"); err != nil {
		t.Fatalf("first Decide failed: %v", err)
	}
	if _, err := d.Decide(context.Background(), ClassificationSignals{}, 7, "", "", "sess-2"); err != nil {
		t.Fatalf("cached Decide failed: %v", err)
	}

	time.Sleep(50 * time.Millisecond) // allow async feedback to land
	ranker.mu.Lock()
	n := len(ranker.feedbacks)
	ranker.mu.Unlock()
	if n != 1 {
		t.Fatalf("cache hit must not record feedback, got %d records", n)
	}
}
