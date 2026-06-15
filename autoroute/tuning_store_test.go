package autoroute

import (
	"context"
	"testing"
)

// TestTuningStore_Defaults verifies that a fresh store (never reloaded)
// returns compiled defaults.
func TestTuningStore_Defaults(t *testing.T) {
	store := NewTuningStore(nil) // no pool → defaults forever

	kw := store.Keywords()
	if len(kw.Reasoning) == 0 {
		t.Error("default Keywords().Reasoning is empty")
	}

	if store.LLMConfidenceThreshold() != 0.7 {
		t.Errorf("LLMConfidenceThreshold = %f, want 0.7", store.LLMConfidenceThreshold())
	}

	if store.LongContextTokens() != 50000 {
		t.Errorf("LongContextTokens = %d, want 50000", store.LongContextTokens())
	}

	w := store.WeightsFor(ProfileSmart)
	if w.Price != 25 || w.Speed != 25 {
		t.Errorf("smart weights Price=%f Speed=%f, want 25/25", w.Price, w.Speed)
	}
}

// TestTuningStore_SetForKeywords verifies the test-injection method.
func TestTuningStore_SetForKeywords(t *testing.T) {
	store := NewTuningStore(nil)

	custom := KeywordSet{
		Reasoning: []string{"custom_reason"},
		Code:      []string{"custom_code"},
		Creative:  []string{"custom_creative"},
	}
	store.SetForKeywords(custom)

	kw := store.Keywords()
	if len(kw.Reasoning) != 1 || kw.Reasoning[0] != "custom_reason" {
		t.Errorf("Keywords().Reasoning = %v, want [custom_reason]", kw.Reasoning)
	}
}

// TestTuningStore_IntegrationWithClassifier verifies that when a
// TuningStore is wired into a HeuristicClassifier, keyword changes in
// the store are reflected in classification output.
func TestTuningStore_IntegrationWithClassifier(t *testing.T) {
	store := NewTuningStore(nil)

	// Start with defaults — "求解" is a reasoning keyword
	clf := NewHeuristicClassifierWithTuning(
		DefaultHeuristicThresholds(),
		DefaultKeywords(),
		store,
	)

	// Verify "求解" triggers reasoning
	cls, err := clf.Classify(context.Background(), ClassificationSignals{
		LastUserPrompt: "求解这个方程",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cls.Primary != TaskReasoning {
		t.Errorf("expected reasoning, got %s", cls.Primary)
	}

	// Now override keywords: remove all reasoning keywords
	store.SetForKeywords(KeywordSet{
		Reasoning: []string{}, // empty
		Code:      DefaultKeywords().Code,
		Creative:  DefaultKeywords().Creative,
	})

	// "求解" should no longer trigger reasoning (keyword removed)
	cls2, err := clf.Classify(context.Background(), ClassificationSignals{
		LastUserPrompt: "求解这个方程",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cls2.Primary == TaskReasoning {
		t.Error("after removing reasoning keywords, '求解' should not classify as reasoning")
	}
}

// TestTuningStore_LongContextThresholdOverride verifies that changing
// the long-context token threshold via the store affects classification.
func TestTuningStore_LongContextThresholdOverride(t *testing.T) {
	store := NewTuningStore(nil)
	store.SetForKeywords(DefaultKeywords())

	clf := NewHeuristicClassifierWithTuning(
		DefaultHeuristicThresholds(),
		DefaultKeywords(),
		store,
	)

	// At default threshold (50k), 40k tokens → not long_context
	cls, _ := clf.Classify(context.Background(), ClassificationSignals{
		LastUserPrompt:  "hello",
		EstimatedTokens: 40000,
	})
	if cls.Primary == TaskLongContext {
		t.Error("40k tokens should not trigger long_context at 50k threshold")
	}

	// Override threshold to 30k via SetForKeywords path (simulate reload)
	// We need a direct snapshot injection for the threshold — use the
	// internal approach by reloading with a mock. Since we don't have
	// a pool, test via a manual snapshot.
	snap := defaultSnapshot()
	snap.LongContextTokens = 30000
	store.snapshot.Store(snap)

	// Now 40k tokens → long_context (threshold lowered to 30k)
	cls2, _ := clf.Classify(context.Background(), ClassificationSignals{
		LastUserPrompt:  "hello",
		EstimatedTokens: 40000,
	})
	if cls2.Primary != TaskLongContext {
		t.Errorf("40k tokens should trigger long_context at 30k threshold, got %s", cls2.Primary)
	}
}

// TestTuningStore_WeightsForDynamic verifies that SetTuningStore makes
// Score() use dynamic weights.
func TestTuningStore_WeightsForDynamic(t *testing.T) {
	// Reset global state
	SetTuningStore(nil)

	// Without store → compiled defaults
	w := WeightsForDynamic(ProfileSmart)
	if w.Price != 25 {
		t.Fatalf("default Price = %f, want 25", w.Price)
	}

	// With store → override
	store := NewTuningStore(nil)
	snap := defaultSnapshot()
	snap.Weights[ProfileSmart] = ProfileWeights{
		Price: 99, Speed: 1, Stability: 1, Match: 1, Pressure: 1, ContextFit: 1,
	}
	store.snapshot.Store(snap)
	SetTuningStore(store)
	defer SetTuningStore(nil) // reset after test

	w2 := WeightsForDynamic(ProfileSmart)
	if w2.Price != 99 {
		t.Errorf("dynamic Price = %f, want 99", w2.Price)
	}
}

// TestTuningStore_SourceCount verifies observability metadata.
func TestTuningStore_SourceCount(t *testing.T) {
	store := NewTuningStore(nil)
	src := store.SourceCount()
	if len(src) == 0 {
		t.Error("SourceCount() is empty on default snapshot")
	}
	if src["default"] != 1 {
		t.Errorf("SourceCount[default] = %d, want 1", src["default"])
	}
}

// TestApplyTuningParam_UnknownKey verifies unknown keys are silently ignored.
func TestApplyTuningParam_UnknownKey(t *testing.T) {
	snap := defaultSnapshot()
	err := applyTuningParam(snap, "nonexistent.key", []byte(`{}`))
	if err != nil {
		t.Errorf("unknown key should not error, got: %v", err)
	}
}

// TestApplyTuningParam_BadJSON verifies malformed JSON returns an error.
func TestApplyTuningParam_BadJSON(t *testing.T) {
	snap := defaultSnapshot()
	err := applyTuningParam(snap, "keywords.reasoning", []byte(`not json`))
	if err == nil {
		t.Error("malformed JSON should return error")
	}
}
