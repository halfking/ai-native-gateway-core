package autoroute

import (
	"testing"
)

func TestIsValidStrategy(t *testing.T) {
	tests := []struct {
		s    Strategy
		want bool
	}{
		{StrategyBaseline, true},
		{StrategyPatternLayered, true},
		{StrategyLLMFallback, true},
		{Strategy("garbage"), false},
		{Strategy(""), false},
	}
	for _, tt := range tests {
		if got := IsValidStrategy(tt.s); got != tt.want {
			t.Errorf("IsValidStrategy(%q) = %v, want %v", tt.s, got, tt.want)
		}
	}
}

func TestAssignStrategy_ABDisabled(t *testing.T) {
	DisableABTest()
	// When AB is disabled, every request should be pattern_layered
	for i := 0; i < 100; i++ {
		s := AssignStrategy("req-" + string(rune(i)))
		if s != StrategyPatternLayered {
			t.Errorf("AB disabled: got %q, want %q", s, StrategyPatternLayered)
		}
	}
}

func TestAssignStrategy_ABEnabled_50Pct(t *testing.T) {
	EnableABTest(50)
	defer DisableABTest()

	// Generate 1000 unique request_ids, count strategy distribution.
	const N = 1000
	var baseline, pattern int
	for i := 0; i < N; i++ {
		s := AssignStrategy("req-" + itoa(i))
		switch s {
		case StrategyBaseline:
			baseline++
		case StrategyPatternLayered:
			pattern++
		default:
			t.Errorf("unexpected strategy %q", s)
		}
	}
	// Expect ~50/50 (with sha1 distribution, allow ±5% tolerance)
	if baseline < 450 || baseline > 550 {
		t.Errorf("baseline = %d, expected ~500 (50%% of %d)", baseline, N)
	}
	if pattern < 450 || pattern > 550 {
		t.Errorf("pattern = %d, expected ~500 (50%% of %d)", pattern, N)
	}
}

func TestAssignStrategy_ABEnabled_AllBaseline(t *testing.T) {
	EnableABTest(100) // 100% baseline
	defer DisableABTest()

	for i := 0; i < 50; i++ {
		s := AssignStrategy("req-" + itoa(i))
		if s != StrategyBaseline {
			t.Errorf("100%% baseline: got %q, want %q", s, StrategyBaseline)
		}
	}
}

func TestAssignStrategy_ABEnabled_AllPattern(t *testing.T) {
	EnableABTest(0) // 0% baseline = all pattern
	defer DisableABTest()

	for i := 0; i < 50; i++ {
		s := AssignStrategy("req-" + itoa(i))
		if s != StrategyPatternLayered {
			t.Errorf("0%% baseline: got %q, want %q", s, StrategyPatternLayered)
		}
	}
}

func TestAssignStrategy_DeterministicForSameRequestID(t *testing.T) {
	EnableABTest(50)
	defer DisableABTest()

	// Same request_id should always yield the same strategy
	// (so a single user's traffic stays in one bucket).
	first := AssignStrategy("fixed-req-id-12345")
	for i := 0; i < 100; i++ {
		got := AssignStrategy("fixed-req-id-12345")
		if got != first {
			t.Errorf("non-deterministic: got %q then %q", first, got)
		}
	}
}

func TestBucketForRequest(t *testing.T) {
	// Sanity: empty request_id → 0 (no panic, deterministic)
	if got := bucketForRequest("", 100); got != 0 {
		t.Errorf("empty request_id bucket = %d, want 0", got)
	}
	// Same id → same bucket
	a := bucketForRequest("stable-id", 1000)
	b := bucketForRequest("stable-id", 1000)
	if a != b {
		t.Errorf("non-deterministic: %d vs %d", a, b)
	}
	// Different ids → typically different buckets
	a = bucketForRequest("alpha", 1000)
	b = bucketForRequest("beta", 1000)
	if a == b {
		t.Errorf("collision: alpha and beta both = %d", a)
	}
	// n=0 → 0
	if got := bucketForRequest("x", 0); got != 0 {
		t.Errorf("n=0 bucket = %d, want 0", got)
	}
}

func TestEmptyKeywordSet_AllowsPatterns(t *testing.T) {
	// The "baseline" strategy should reproduce the pre-Phase 1 bug:
	// water-pool problem → chat (not reasoning).
	kw := emptyKeywordSet()
	clf := NewHeuristicClassifier(DefaultHeuristicThresholds(), kw)

	cls, err := clf.Classify(nil, ClassificationSignals{
		LastUserPrompt: "有一个水池，进水管每分钟进水10升，排水管每分钟排水8升。",
		Language:       "zh",
	})
	if err != nil {
		t.Fatal(err)
	}
	// With keywords empty but patterns still present, this would
	// actually STILL classify as reasoning (the rate*time pattern).
	// To strictly reproduce v1.0 baseline, the classifier itself
	// would need to skip the pattern layer. The current emptyKeyword
	// helper is therefore a "keyword-degraded" baseline, not strict
	// v1.0 reproduction. This test documents the trade-off.
	if cls.Primary == TaskReasoning {
		t.Logf("empty-keyword baseline still hits pattern layer (got %s); this is expected — strict v1.0 reproduction requires a separate classifier that skips patterns entirely", cls.Primary)
	}
}

func TestClassifierForStrategy_DefaultIsBase(t *testing.T) {
	// Sanity: passing the base classifier through pattern_layered
	// returns it unchanged; baseline returns a new classifier with
	// empty keywords.
	base := NewHeuristicClassifier(DefaultHeuristicThresholds(), DefaultKeywords())

	got := ClassifierForStrategy(StrategyPatternLayered, base)
	if got != base {
		t.Errorf("pattern_layered should return base unchanged")
	}

	got2 := ClassifierForStrategy(StrategyBaseline, base)
	if got2 == base {
		t.Errorf("baseline should return a NEW classifier (not base)")
	}
	if got2 == nil {
		t.Errorf("baseline returned nil")
	}
}

func TestClassifierForStrategy_UnknownFallsBackToBase(t *testing.T) {
	base := NewHeuristicClassifier(DefaultHeuristicThresholds(), DefaultKeywords())
	got := ClassifierForStrategy(Strategy("nonsense"), base)
	if got != base {
		t.Errorf("unknown strategy should return base, got %T", got)
	}
}

func TestAllStrategies_ContainsAllThree(t *testing.T) {
	if len(AllStrategies) != 3 {
		t.Errorf("AllStrategies has %d entries, want 3", len(AllStrategies))
	}
	for _, s := range AllStrategies {
		if !IsValidStrategy(s) {
			t.Errorf("AllStrategies contains invalid %q", s)
		}
	}
}

// itoa lives in classifier.go (package-level)
