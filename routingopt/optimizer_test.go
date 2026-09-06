package routingopt

import (
	"context"
	"testing"
	"time"
)

// =============================================================================
// ClassificationEnhancer 单元测试
// =============================================================================

func TestNormalizeDistribution(t *testing.T) {
	if got := normalizeDistribution(map[string]int{"code": 60, "chat": 30, "reasoning": 10}); got == nil ||
		got["code"] != 0.6 || got["chat"] != 0.3 || got["reasoning"] != 0.1 {
		t.Fatalf("unexpected distribution: %+v", got)
	}
	if got := normalizeDistribution(nil); got != nil {
		t.Fatalf("nil input should return nil, got %+v", got)
	}
	if got := normalizeDistribution(map[string]int{"code": 0}); got != nil {
		t.Fatalf("zero-total input should return nil, got %+v", got)
	}
}

func TestDetectSessionMode_ClientType(t *testing.T) {
	cases := []struct {
		clientType string
		want       string
	}{
		{"cursor", "ide"},
		{"Claude Code", "ide"}, // spaces normalized to dash
		{"claude-code", "ide"},
		{"vscode", "ide"},
		{"curl", "cli"},
		{"python-requests", "cli"},
		{"web", "web"},
		{"unknown-client", "api"},
		{"", "api"}, // empty falls through to UA, also empty → api
	}
	for _, tc := range cases {
		if got := detectSessionMode(tc.clientType, ""); got != tc.want {
			t.Errorf("detectSessionMode(%q) = %q, want %q", tc.clientType, got, tc.want)
		}
	}
	// UA fallback when clientType is empty
	if got := detectSessionMode("", "Mozilla/5.0 Chrome/120"); got != "web" {
		t.Errorf("UA fallback for browser = %q, want web", got)
	}
	if got := detectSessionMode("", "curl/8.1"); got != "cli" {
		t.Errorf("UA fallback for curl = %q, want cli", got)
	}
}

func TestIsPeakHour(t *testing.T) {
	// Mon 2026-09-07 10:00 UTC — peak
	mon := time.Date(2026, 9, 7, 10, 0, 0, 0, time.UTC)
	if !isPeakHour(mon) {
		t.Fatal("Mon 10:00 UTC should be peak")
	}
	// Mon 20:00 UTC — off-peak
	monNight := time.Date(2026, 9, 7, 20, 0, 0, 0, time.UTC)
	if isPeakHour(monNight) {
		t.Fatal("Mon 20:00 UTC should be off-peak")
	}
	// Sat 2026-09-12 10:00 UTC — weekend off-peak
	sat := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	if isPeakHour(sat) {
		t.Fatal("Sat 10:00 UTC should be off-peak")
	}
}

// =============================================================================
// AdaptiveLearner: WeightedAccuracy 纯函数（人工标注 ×2）
// =============================================================================

func TestWeightedAccuracy(t *testing.T) {
	cases := []struct {
		name                                             string
		autoCorrect, autoTotal, humanCorrect, humanTotal int
		want                                             float64
	}{
		{"no data", 0, 0, 0, 0, 0},
		{"auto only 80%", 80, 100, 0, 0, 0.8},
		{"human doubles weight", 80, 100, 10, 10, 100.0 / 120.0}, // (80+20)/(100+20)
		{"human all wrong drags down", 90, 100, 0, 10, 90.0 / 120.0},
		{"human all right lifts up", 80, 100, 10, 10, 100.0 / 120.0},
		{"clamped when over", 100, 0, 5, 5, 1}, // correct>total clamps to 1
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := WeightedAccuracy(tc.autoCorrect, tc.autoTotal, tc.humanCorrect, tc.humanTotal)
			if diff := got - tc.want; diff > 1e-9 || diff < -1e-9 {
				t.Fatalf("WeightedAccuracy = %v, want %v", got, tc.want)
			}
		})
	}
}

// =============================================================================
// ModelRecommender: 多目标排序（注入 state，无 DB）
// =============================================================================

func testOptimizationState(exploration float64) *OptimizationState {
	return &OptimizationState{
		Version:              7,
		ClassifierWeights:    map[string]float64{},
		ConfidenceThresholds: map[string]float64{},
		RecommenderWeights: map[string]float64{
			"quality": 0.4, "cost": 0.3, "latency": 0.2, "availability": 0.1,
		},
		ExplorationRate: exploration,
	}
}

func TestModelRecommender_WeightedOrdering(t *testing.T) {
	r := NewModelRecommender(nil)
	r.SetActiveStateForTest(testOptimizationState(0)) // no exploration

	// model-a: higher quality. model-b: much cheaper+faster, slight quality gap.
	// score-a = .4*90 + .3*(1-.5) + .2*(1-.5) + .1*.9 = 36+15+10+9 = 70
	// score-b = .4*80 + .3*(1-.1) + .2*(1-.1) + .1*.9 = 32+27+18+9 = 86 → wins
	cands := []ModelCandidate{
		{CanonicalName: "model-a", Score: 90, Cost: 0.5, Latency: 0.5, Availability: 0.9},
		{CanonicalName: "model-b", Score: 80, Cost: 0.1, Latency: 0.1, Availability: 0.9},
	}
	out, err := r.Recommend(context.Background(), cands, RoutingContext{TaskType: "code", Profile: "smart"})
	if err != nil {
		t.Fatalf("Recommend failed: %v", err)
	}
	if out[0].CanonicalName != "model-b" {
		t.Fatalf("multi-objective scoring should prefer model-b, got %s", out[0].CanonicalName)
	}
	if len(out) != 2 {
		t.Fatalf("all candidates must be returned, got %d", len(out))
	}
}

func TestModelRecommender_SingleCandidateShortCircuitsInDecider(t *testing.T) {
	// Decider skips the plugin for <2 candidates; the recommender itself must
	// still pass single-candidate lists through untouched.
	r := NewModelRecommender(nil)
	r.SetActiveStateForTest(testOptimizationState(0))
	out, err := r.Recommend(context.Background(),
		[]ModelCandidate{{CanonicalName: "only", Score: 50}}, RoutingContext{})
	if err != nil || len(out) != 1 || out[0].CanonicalName != "only" {
		t.Fatalf("single candidate pass-through failed: out=%+v err=%v", out, err)
	}
}

func TestModelRecommender_ConcurrentGetActiveState(t *testing.T) {
	// Run with -race to prove the cache mutex works.
	r := NewModelRecommender(nil)
	r.SetActiveStateForTest(testOptimizationState(0)) // prime cache: reads stay off the DB path
	done := make(chan struct{})
	go func() {
		for i := 0; i < 50; i++ {
			r.SetActiveStateForTest(testOptimizationState(0))
			time.Sleep(time.Millisecond)
		}
		close(done)
	}()
	for i := 0; i < 50; i++ {
		_, _ = r.getActiveState(context.Background())
	}
	<-done
}

func TestComputeFallbackChain(t *testing.T) {
	r := NewModelRecommender(nil)
	cands := []ModelCandidate{{CanonicalName: "a"}, {CanonicalName: "b"}, {CanonicalName: "c"}, {CanonicalName: "d"}}
	chain := r.ComputeFallbackChain(cands)
	if len(chain) != 3 || chain[0].CanonicalName != "a" {
		t.Fatalf("fallback chain should be top-3, got %+v", chain)
	}
}

// =============================================================================
// FeedbackIntegrator: stub 注入，验证插入→亲和力→人工标注闭环
// =============================================================================

type stubFeedbackWriter struct {
	inserted []*FeedbackLog
	marked   []string
}

func (s *stubFeedbackWriter) Insert(ctx context.Context, log *FeedbackLog) (int64, error) {
	s.inserted = append(s.inserted, log)
	return int64(len(s.inserted)), nil
}

func (s *stubFeedbackWriter) MarkHumanCorrection(ctx context.Context, requestID string, p markHumanCorrectionParams) error {
	s.marked = append(s.marked, requestID)
	return nil
}

type stubAffinity struct {
	updated []string
}

func (s *stubAffinity) UpdateUserAffinity(ctx context.Context, userID string, taskType string, provider string) error {
	s.updated = append(s.updated, userID+"|"+taskType+"|"+provider)
	return nil
}

func TestFeedbackIntegrator_PersistsIdentityAndUpdatesAffinity(t *testing.T) {
	writer := &stubFeedbackWriter{}
	affinity := &stubAffinity{}
	i := &FeedbackIntegrator{feedback: writer, enhancer: affinity, pool: nil} // pool nil → no annotation lookup

	err := i.RecordFeedback(context.Background(), RoutingFeedback{
		RequestID: "req-1", TaskType: "code",
		PredictedProvider: "model-a", ActualProvider: "model-a",
		IsSuccess: true, UserID: 42, SessionID: "sess-9",
	})
	if err != nil {
		t.Fatalf("RecordFeedback failed: %v", err)
	}
	if len(writer.inserted) != 1 {
		t.Fatalf("expected 1 insert, got %d", len(writer.inserted))
	}
	logRow := writer.inserted[0]
	if logRow.UserID == nil || *logRow.UserID != "42" {
		t.Fatalf("feedback row should carry user_id=42, got %+v", logRow.UserID)
	}
	if logRow.SessionID == nil || *logRow.SessionID != "sess-9" {
		t.Fatalf("feedback row should carry session_id=sess-9, got %+v", logRow.SessionID)
	}
	if len(affinity.updated) != 1 || affinity.updated[0] != "42|code|model-a" {
		t.Fatalf("affinity should be updated, got %+v", affinity.updated)
	}
}

func TestFeedbackIntegrator_AnonymousSkipsAffinity(t *testing.T) {
	writer := &stubFeedbackWriter{}
	affinity := &stubAffinity{}
	i := &FeedbackIntegrator{feedback: writer, enhancer: affinity, pool: nil}

	if err := i.RecordFeedback(context.Background(), RoutingFeedback{
		RequestID: "req-2", TaskType: "chat",
		PredictedProvider: "model-a", IsSuccess: true, UserID: 0,
	}); err != nil {
		t.Fatalf("RecordFeedback failed: %v", err)
	}
	if len(affinity.updated) != 0 {
		t.Fatalf("anonymous feedback must not update affinity, got %+v", affinity.updated)
	}
	if writer.inserted[0].UserID != nil {
		t.Fatalf("anonymous user_id should be NULL, got %q", *writer.inserted[0].UserID)
	}
}

// =============================================================================
// RequestMeta context 往返
// =============================================================================

func TestRequestMetaRoundTrip(t *testing.T) {
	if got := RequestMetaFrom(context.Background()); got != (RequestMeta{}) {
		t.Fatalf("empty context should return zero meta, got %+v", got)
	}
	ctx := WithRequestMeta(context.Background(), RequestMeta{UserID: 7, SessionID: "s", ClientType: "cursor"})
	got := RequestMetaFrom(ctx)
	if got.UserID != 7 || got.SessionID != "s" || got.ClientType != "cursor" {
		t.Fatalf("meta round-trip failed: %+v", got)
	}
}
