package autoroute

import (
	"context"
	"testing"
	"time"
)

// stubClassifier returns the configured classification (or error).
type stubClassifier struct {
	name string
	out  *Classification
	err  error
}

func (s *stubClassifier) Classify(_ context.Context, _ ClassificationSignals) (*Classification, error) {
	return s.out, s.err
}
func (s *stubClassifier) Name() string { return s.name }

// stubIndex returns preconfigured candidates from Recommend and reports
// a fixed lastRefresh time.
type stubIndex struct {
	cands []ScoredCandidate
}

func (s *stubIndex) Recommend(_ TaskType, _ ClassificationSignals, _ Profile, topN int) []ScoredCandidate {
	if topN > 0 && len(s.cands) > topN {
		return s.cands[:topN]
	}
	return s.cands
}
func (s *stubIndex) Snapshot() []Candidate  { return nil }
func (s *stubIndex) LastRefresh() time.Time { return time.Now() }

func TestDecide_HeuristicOnly_NoFallback(t *testing.T) {
	cls := &stubClassifier{name: "heuristic", out: &Classification{
		Primary: TaskCode, Confidence: 0.9, Classifier: "heuristic", Reason: "code keyword",
	}}
	idx := &stubIndex{cands: []ScoredCandidate{{
		Candidate: Candidate{CanonicalName: "claude-sonnet-4.5", CredentialID: 7, RawModel: "claude-sonnet-4.5"},
		Breakdown: ScoringBreakdown{Composite: 75},
	}}}
	d := NewDecider(cls, nil, idx, NewMemoryProfileStore())
	d.TopN = 3

	dec, err := d.Decide(context.Background(), ClassificationSignals{}, 42, "", "", "")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if dec.ChosenModel != "claude-sonnet-4.5" {
		t.Fatalf("model: got %s", dec.ChosenModel)
	}
	if dec.TaskType != TaskCode {
		t.Fatalf("task: got %s", dec.TaskType)
	}
	if dec.Classifier != "heuristic" {
		t.Fatalf("classifier: got %s", dec.Classifier)
	}
	if dec.Profile != ProfileSmart {
		t.Fatalf("profile default should be smart, got %s", dec.Profile)
	}
}

func TestDecide_HeaderOverridesSticky(t *testing.T) {
	cls := &stubClassifier{name: "heuristic", out: &Classification{Primary: TaskChat, Confidence: 0.9}}
	idx := &stubIndex{cands: []ScoredCandidate{{Candidate: Candidate{CanonicalName: "m"}}}}
	store := NewMemoryProfileStore()
	_ = store.Put(context.Background(), 42, ProfileCostFirst, 30*time.Minute)

	d := NewDecider(cls, nil, idx, store)
	dec, err := d.Decide(context.Background(), ClassificationSignals{}, 42, "speed_first", "", "")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if dec.Profile != ProfileSpeedFirst {
		t.Fatalf("profile: got %s", dec.Profile)
	}
	// Verify sticky was updated
	got, _ := store.Get(context.Background(), 42)
	if got != ProfileSpeedFirst {
		t.Fatalf("sticky should be updated: got %s", got)
	}
}

func TestDecide_StickyUsedWhenNoHeader(t *testing.T) {
	cls := &stubClassifier{name: "heuristic", out: &Classification{Primary: TaskChat, Confidence: 0.9}}
	idx := &stubIndex{cands: []ScoredCandidate{{Candidate: Candidate{CanonicalName: "m"}}}}
	store := NewMemoryProfileStore()
	_ = store.Put(context.Background(), 7, ProfileCostFirst, 30*time.Minute)

	d := NewDecider(cls, nil, idx, store)
	dec, _ := d.Decide(context.Background(), ClassificationSignals{}, 7, "", "", "")
	if dec.Profile != ProfileCostFirst {
		t.Fatalf("sticky should be used: got %s", dec.Profile)
	}
}

func TestDecide_LLMFallback_TriggersOnLowConfidence(t *testing.T) {
	heuristic := &stubClassifier{name: "heuristic", out: &Classification{Primary: TaskChat, Confidence: 0.5}}
	llm := &stubClassifier{name: "llm", out: &Classification{Primary: TaskCode, Confidence: 0.85, Classifier: "llm"}}
	idx := &stubIndex{cands: []ScoredCandidate{{Candidate: Candidate{CanonicalName: "m"}}}}
	d := NewDecider(heuristic, llm, idx, nil)
	d.LLMConfidenceThreshold = 0.7

	dec, err := d.Decide(context.Background(), ClassificationSignals{}, 0, "", "", "")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if dec.Classifier != "llm" {
		t.Fatalf("expected llm fallback, got %s", dec.Classifier)
	}
	if dec.TaskType != TaskCode {
		t.Fatalf("task: got %s", dec.TaskType)
	}
}

func TestDecide_NoCandidates_Error(t *testing.T) {
	cls := &stubClassifier{name: "heuristic", out: &Classification{Primary: TaskChat, Confidence: 0.9}}
	idx := &stubIndex{cands: nil}
	d := NewDecider(cls, nil, idx, nil)

	_, err := d.Decide(context.Background(), ClassificationSignals{}, 0, "", "", "")
	if err == nil {
		t.Fatal("expected error for empty candidate set")
	}
}

func TestDecide_HeuristicError_UsesDefault(t *testing.T) {
	cls := &stubClassifier{name: "heuristic", err: errTest}
	idx := &stubIndex{cands: []ScoredCandidate{{Candidate: Candidate{CanonicalName: "m"}}}}
	d := NewDecider(cls, nil, idx, nil)

	dec, err := d.Decide(context.Background(), ClassificationSignals{}, 0, "", "", "")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if dec.Classifier != "default" {
		t.Fatalf("expected default fallback, got %s", dec.Classifier)
	}
	if dec.TaskType != TaskChat {
		t.Fatalf("expected chat default, got %s", dec.TaskType)
	}
}

func TestDecide_TaskHintOverridesHeuristic(t *testing.T) {
	cls := &stubClassifier{name: "heuristic", out: &Classification{Primary: TaskCode, Confidence: 0.9}}
	idx := &stubIndex{cands: []ScoredCandidate{{Candidate: Candidate{CanonicalName: "m"}}}}
	d := NewDecider(cls, nil, idx, nil)

	dec, _ := d.Decide(context.Background(), ClassificationSignals{}, 0, "", TaskReasoning, "")
	if dec.TaskType != TaskReasoning {
		t.Fatalf("task hint should override heuristic, got %s", dec.TaskType)
	}
	if dec.Classifier != "heuristic" {
		t.Fatalf("task hint path uses heuristic name, got %s", dec.Classifier)
	}
}

func TestDecide_InvalidTaskHint_Ignored(t *testing.T) {
	cls := &stubClassifier{name: "heuristic", out: &Classification{Primary: TaskCode, Confidence: 0.9}}
	idx := &stubIndex{cands: []ScoredCandidate{{Candidate: Candidate{CanonicalName: "m"}}}}
	d := NewDecider(cls, nil, idx, nil)

	dec, _ := d.Decide(context.Background(), ClassificationSignals{}, 0, "", TaskType("bogus"), "")
	if dec.TaskType != TaskCode {
		t.Fatalf("invalid hint should fall through to heuristic, got %s", dec.TaskType)
	}
}

func TestNormaliseProfile(t *testing.T) {
	cases := map[string]Profile{
		"":              "",
		"smart":         ProfileSmart,
		"SMART":         ProfileSmart,
		" speed_first ": ProfileSpeedFirst,
		"cost_first":    ProfileCostFirst,
		"unknown":       "",
	}
	for in, want := range cases {
		if got := normaliseProfile(in); got != want {
			t.Fatalf("normaliseProfile(%q)=%q, want %q", in, got, want)
		}
	}
}

// errTest is a sentinel error used by stub-based tests.
var errTest = &testErr{}

type testErr struct{}

func (e *testErr) Error() string { return "test err" }