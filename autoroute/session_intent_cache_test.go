package autoroute

import (
	"context"
	"testing"
	"time"
)

func TestSessionIntentCache_GetPut(t *testing.T) {
	c := NewSessionIntentCache(1 * time.Minute)
	c.Put("sess-1", CachedIntent{
		TaskType:     TaskCode,
		ChosenModel:  "claude-sonnet-4.5",
		CredentialID: 12,
		Profile:      ProfileSmart,
		Confidence:   0.92,
	})

	got, ok := c.Get("sess-1")
	if !ok {
		t.Fatal("expected cache hit")
	}
	if got.TaskType != TaskCode {
		t.Fatalf("task: got %s want %s", got.TaskType, TaskCode)
	}
	if got.ChosenModel != "claude-sonnet-4.5" {
		t.Fatalf("model: got %s", got.ChosenModel)
	}
}

func TestSessionIntentCache_Expiry(t *testing.T) {
	c := NewSessionIntentCache(50 * time.Millisecond)
	c.Put("sess-1", CachedIntent{TaskType: TaskChat, ChosenModel: "m"})

	time.Sleep(60 * time.Millisecond)
	_, ok := c.Get("sess-1")
	if ok {
		t.Fatal("expected cache miss after expiry")
	}
}

func TestSessionIntentCache_EmptySessionID(t *testing.T) {
	c := NewSessionIntentCache(1 * time.Minute)
	c.Put("", CachedIntent{TaskType: TaskChat}) // no-op
	_, ok := c.Get("")
	if ok {
		t.Fatal("empty sessionID should not cache")
	}
}

func TestSessionIntentCache_Invalidate(t *testing.T) {
	c := NewSessionIntentCache(1 * time.Minute)
	c.Put("sess-1", CachedIntent{TaskType: TaskChat})
	c.Invalidate("sess-1")
	_, ok := c.Get("sess-1")
	if ok {
		t.Fatal("expected miss after invalidate")
	}
}

func TestSessionIntentCache_Len(t *testing.T) {
	c := NewSessionIntentCache(1 * time.Minute)
	c.Put("a", CachedIntent{TaskType: TaskChat})
	c.Put("b", CachedIntent{TaskType: TaskCode})
	if c.Len() != 2 {
		t.Fatalf("Len: got %d want 2", c.Len())
	}
}

func TestShouldReclassify_VisionOverride(t *testing.T) {
	// Images present but cached was chat → reclassify
	if !shouldReclassify(TaskChat, ClassificationSignals{HasImages: true}) {
		t.Fatal("vision override should trigger reclassify")
	}
	// Images present and cached was vision → no reclassify
	if shouldReclassify(TaskVision, ClassificationSignals{HasImages: true}) {
		t.Fatal("cached=vision + images should NOT reclassify")
	}
}

func TestShouldReclassify_LongContextOverride(t *testing.T) {
	if !shouldReclassify(TaskChat, ClassificationSignals{EstimatedTokens: 80_000}) {
		t.Fatal("long_context override should trigger reclassify")
	}
	if shouldReclassify(TaskLongContext, ClassificationSignals{EstimatedTokens: 80_000}) {
		t.Fatal("cached=long_context + long should NOT reclassify")
	}
}

func TestShouldReclassify_AgentOverride(t *testing.T) {
	sigs := ClassificationSignals{ToolCount: 5, HasToolResults: true}
	if !shouldReclassify(TaskChat, sigs) {
		t.Fatal("agent override should trigger reclassify")
	}
	if shouldReclassify(TaskAgent, sigs) {
		t.Fatal("cached=agent + agent signals should NOT reclassify")
	}
}

func TestShouldReclassify_NoDrift(t *testing.T) {
	// Same task type, soft signal change → no reclassify
	sigs := ClassificationSignals{LastUserPrompt: "write a function"}
	if shouldReclassify(TaskCode, sigs) {
		t.Fatal("soft signal change should NOT reclassify")
	}
}

func TestDecider_SessionCacheHit(t *testing.T) {
	cls := &stubClassifier{name: "heuristic", out: &Classification{Primary: TaskCode, Confidence: 0.9, Classifier: "heuristic"}}
	idx := &stubIndex{cands: []ScoredCandidate{{
		Candidate: Candidate{CanonicalName: "claude-sonnet-4.5", CredentialID: 7, RawModel: "claude-sonnet-4.5"},
		Breakdown: ScoringBreakdown{Composite: 85},
	}}}
	d := NewDecider(cls, nil, idx, NewMemoryProfileStore())

	// First call: classify + cache
	dec1, err := d.Decide(context.Background(), ClassificationSignals{
		LastUserPrompt: "write a function",
	}, 42, "", "", "sess-test")
	if err != nil {
		t.Fatalf("first decide: %v", err)
	}
	if dec1.Classifier != "heuristic" {
		t.Fatalf("first call classifier: got %s", dec1.Classifier)
	}

	// Second call: should hit cache
	dec2, err := d.Decide(context.Background(), ClassificationSignals{
		LastUserPrompt: "write another function",
	}, 42, "", "", "sess-test")
	if err != nil {
		t.Fatalf("second decide: %v", err)
	}
	if dec2.Classifier != "session_cache" {
		t.Fatalf("second call should hit cache: got classifier=%s", dec2.Classifier)
	}
	if dec2.ChosenModel != dec1.ChosenModel {
		t.Fatalf("cached model mismatch: %s vs %s", dec2.ChosenModel, dec1.ChosenModel)
	}
}

func TestDecider_SessionCacheDrift(t *testing.T) {
	cls := &stubClassifier{name: "heuristic", out: &Classification{Primary: TaskChat, Confidence: 0.9, Classifier: "heuristic"}}
	idx := &stubIndex{cands: []ScoredCandidate{{
		Candidate: Candidate{CanonicalName: "m", CredentialID: 1},
	}}}
	d := NewDecider(cls, nil, idx, nil)

	// First call: chat
	//nolint:errcheck // best-effort touch, non-critical
	d.Decide(context.Background(), ClassificationSignals{}, 0, "", "", "sess-drift")

	// Second call: vision override → should reclassify (cache miss)
	dec2, _ := d.Decide(context.Background(), ClassificationSignals{HasImages: true}, 0, "", "", "sess-drift")
	// classify will return TaskChat (stub), but shouldReclassify returns true
	// so the cache should NOT be hit. The classifier still runs.
	if dec2.Classifier == "session_cache" {
		t.Fatal("vision drift should NOT use cache")
	}
}

func TestDecider_NoSessionID_AlwaysReclassify(t *testing.T) {
	cls := &stubClassifier{name: "heuristic", out: &Classification{Primary: TaskChat, Confidence: 0.9, Classifier: "heuristic"}}
	idx := &stubIndex{cands: []ScoredCandidate{{Candidate: Candidate{CanonicalName: "m"}}}}
	d := NewDecider(cls, nil, idx, nil)

	// Two calls with empty sessionID → both should classify
	//nolint:errcheck // best-effort touch, non-critical
	d.Decide(context.Background(), ClassificationSignals{}, 0, "", "", "")
	dec2, _ := d.Decide(context.Background(), ClassificationSignals{}, 0, "", "", "")
	if dec2.Classifier == "session_cache" {
		t.Fatal("empty sessionID should never hit cache")
	}
}