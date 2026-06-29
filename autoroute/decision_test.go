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
		fatalf := t.Fatalf
		fatalf("invalid hint should fall through to heuristic, got %s", dec.TaskType)
	}
}

func TestDecideWithFeatureFlags_SubFeatureEnablesV2Path(t *testing.T) {
	old := globalFeatureFlags
	globalFeatureFlags = &FeatureFlags{UseCacheRevalidation: true}
	defer func() { globalFeatureFlags = old }()

	cls := &stubClassifier{name: "heuristic", out: &Classification{Primary: TaskChat, Confidence: 0.9, Classifier: "heuristic"}}
	idx := NewIndex()
	idx.entries = []Candidate{{CredentialID: 1, CanonicalID: 1, CanonicalName: "m", RawModel: "m", Tags: []string{"chat"}}}
	d := NewDecider(cls, nil, idx, nil)

	dec, err := d.DecideWithFeatureFlags(context.Background(), ClassificationSignals{}, 0, "", "", "")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if dec.Classifier != "heuristic_v2" {
		t.Fatalf("expected V2 path classifier suffix, got %s", dec.Classifier)
	}
}

func TestIsFallbackWinner(t *testing.T) {
	// Exact sentinel: single candidate, Composite=50, PriceScore=50, MatchScore<=30
	fallbackResult := []ScoredCandidate{{
		Candidate: Candidate{CanonicalName: "fallback-model"},
		Breakdown: ScoringBreakdown{Composite: 50, MatchScore: 30, PriceScore: 50},
	}}
	if !isFallbackWinner(fallbackResult) {
		t.Fatal("expected fallback sentinel to be detected")
	}

	// MatchScore = 0 (degenerate case in RecommendV2) should also be detected
	degenerate := []ScoredCandidate{{
		Candidate: Candidate{CanonicalName: "fallback-model"},
		Breakdown: ScoringBreakdown{Composite: 50, MatchScore: 0, PriceScore: 50},
	}}
	if !isFallbackWinner(degenerate) {
		t.Fatal("expected fallback with MatchScore=0 to be detected")
	}

	// Normal scoring result should NOT be flagged as fallback
	normal := []ScoredCandidate{
		{Candidate: Candidate{CanonicalName: "best"}, Breakdown: ScoringBreakdown{Composite: 80, MatchScore: 70, PriceScore: 60}},
		{Candidate: Candidate{CanonicalName: "second"}, Breakdown: ScoringBreakdown{Composite: 65, MatchScore: 50, PriceScore: 55}},
	}
	if isFallbackWinner(normal) {
		t.Fatal("normal multi-candidate result should not be flagged as fallback")
	}

	// Single candidate but with non-sentinel score should NOT be flagged
	nonSentinel := []ScoredCandidate{{
		Candidate: Candidate{CanonicalName: "lone"},
		Breakdown: ScoringBreakdown{Composite: 90, MatchScore: 80, PriceScore: 70},
	}}
	if isFallbackWinner(nonSentinel) {
		t.Fatal("single candidate with non-sentinel score should not be flagged")
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