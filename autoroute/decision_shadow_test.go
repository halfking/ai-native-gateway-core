package autoroute

import (
	"context"
	"errors"
	"testing"
	"time"
)

type shadowTestClassifier struct {
	calls int
	out   *Classification
	err   error
}

func (s *shadowTestClassifier) Classify(_ context.Context, _ ClassificationSignals) (*Classification, error) {
	s.calls++
	return s.out, s.err
}

func (s *shadowTestClassifier) Name() string { return "embedding" }

func shadowTestDecider(shadow Classifier) *Decider {
	primary := &stubClassifier{name: "heuristic", out: &Classification{
		Primary: TaskCode, Confidence: 0.9, Classifier: "heuristic",
	}}
	idx := &stubIndex{cands: []ScoredCandidate{{Candidate: Candidate{CanonicalName: "m"}}}}
	d := NewDecider(primary, nil, idx, nil)
	d.SetShadowClassifier(shadow)
	return d
}

func TestDecider_Shadow_OffNoOp(t *testing.T) {
	shadow := &shadowTestClassifier{out: &Classification{Primary: TaskReasoning, Confidence: 0.8}}
	d := shadowTestDecider(shadow)
	d.SetShadowSampleRate(0)

	decision, err := d.Decide(context.Background(), ClassificationSignals{LastUserPrompt: "solve"}, 0, "", "", "")
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if shadow.calls != 0 {
		t.Fatalf("shadow calls = %d, want 0", shadow.calls)
	}
	if decision.EmbeddingShadowTask != "" {
		t.Fatalf("shadow task = %q, want empty", decision.EmbeddingShadowTask)
	}
}

func TestDecider_Shadow_SampleRate0(t *testing.T) {
	shadow := &shadowTestClassifier{out: &Classification{Primary: TaskReasoning, Confidence: 0.8}}
	d := shadowTestDecider(shadow)
	d.SetShadowSampleRate(0)

	_, err := d.Decide(context.Background(), ClassificationSignals{}, 0, "", "", "")
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if shadow.calls != 0 {
		t.Fatalf("shadow calls = %d, want 0", shadow.calls)
	}
}

func TestDecider_Shadow_ClassifyErrorNoImpact(t *testing.T) {
	shadow := &shadowTestClassifier{err: errors.New("embedding unavailable")}
	d := shadowTestDecider(shadow)
	d.SetShadowSampleRate(1)

	decision, err := d.Decide(context.Background(), ClassificationSignals{LastUserPrompt: "hello"}, 0, "", "", "")
	if err != nil {
		t.Fatalf("Decide should ignore shadow error: %v", err)
	}
	if decision.ChosenModel != "m" || decision.TaskType != TaskCode {
		t.Fatalf("primary decision changed: %+v", decision)
	}
	if decision.EmbeddingShadowTask != "" {
		t.Fatalf("shadow task = %q, want empty", decision.EmbeddingShadowTask)
	}
}

func TestDecider_Shadow_SuccessPopulates(t *testing.T) {
	shadow := &shadowTestClassifier{out: &Classification{Primary: TaskReasoning, Confidence: 0.83}}
	d := shadowTestDecider(shadow)
	d.SetShadowSampleRate(1)

	decision, err := d.Decide(context.Background(), ClassificationSignals{LastUserPrompt: "prove"}, 0, "", "", "")
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if shadow.calls != 1 {
		t.Fatalf("shadow calls = %d, want 1", shadow.calls)
	}
	if decision.EmbeddingShadowTask != string(TaskReasoning) {
		t.Fatalf("shadow task = %q, want %q", decision.EmbeddingShadowTask, TaskReasoning)
	}
	if decision.EmbeddingShadowSimilarity != 0.83 {
		t.Fatalf("shadow similarity = %v, want 0.83", decision.EmbeddingShadowSimilarity)
	}
}

func TestDecider_Shadow_TimeoutNoImpact(t *testing.T) {
	shadow := &blockingShadowClassifier{}
	d := shadowTestDecider(shadow)
	d.SetShadowSampleRate(1)

	start := time.Now()
	decision, err := d.Decide(context.Background(), ClassificationSignals{LastUserPrompt: "hello"}, 0, "", "", "")
	if err != nil {
		t.Fatalf("Decide should ignore shadow timeout: %v", err)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Fatal("shadow timeout exceeded expected bound")
	}
	if decision.ChosenModel != "m" {
		t.Fatalf("primary decision changed: %+v", decision)
	}
}

type blockingShadowClassifier struct{}

func (*blockingShadowClassifier) Classify(ctx context.Context, _ ClassificationSignals) (*Classification, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (*blockingShadowClassifier) Name() string { return "embedding" }
