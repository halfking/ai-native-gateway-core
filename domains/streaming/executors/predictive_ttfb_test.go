package executors

import (
	"testing"
	"time"
)

type predictiveTTFBStub struct {
	stats map[int]*TTFBStats
}

func (s predictiveTTFBStub) Record(int, time.Duration) {}
func (s predictiveTTFBStub) Get(id int) *TTFBStats     { return s.stats[id] }

func TestPredictiveSkipper_DefaultOff(t *testing.T) {
	p := NewPredictiveSkipper(predictiveTTFBStub{stats: map[int]*TTFBStats{
		1: {AvgTTFB: 2 * time.Second, SampleCount: 5},
	}}, 1500*time.Millisecond, 3)
	p.Threshold = 0
	if _, skip := p.ShouldSkip(1); skip {
		t.Fatal("zero threshold must keep predictive skipping disabled")
	}
}

func TestPredictiveSkipper_RequiresSamplesAndStrictThreshold(t *testing.T) {
	stub := predictiveTTFBStub{stats: map[int]*TTFBStats{
		1: {AvgTTFB: 2 * time.Second, SampleCount: 2},
		2: {AvgTTFB: 1500 * time.Millisecond, SampleCount: 3},
		3: {AvgTTFB: 1501 * time.Millisecond, SampleCount: 3},
	}}
	p := NewPredictiveSkipper(stub, 1500*time.Millisecond, 3)

	if _, skip := p.ShouldSkip(1); skip {
		t.Fatal("candidate below the sample floor must not be skipped")
	}
	if _, skip := p.ShouldSkip(2); skip {
		t.Fatal("threshold equality must not be skipped")
	}
	decision, skip := p.ShouldSkip(3)
	if !skip {
		t.Fatal("candidate above threshold with enough samples must be skipped")
	}
	if decision.Reason != predictiveTTFBReason || decision.AvgTTFB != 1501*time.Millisecond || decision.SampleCount != 3 {
		t.Fatalf("unexpected decision: %+v", decision)
	}
}

func TestPredictiveSkipper_MissingCredential(t *testing.T) {
	p := NewPredictiveSkipper(predictiveTTFBStub{stats: map[int]*TTFBStats{}}, time.Second, 1)
	if _, skip := p.ShouldSkip(99); skip {
		t.Fatal("missing TTFB history must not be skipped")
	}
}

func TestPredictiveDecisionForCandidate_ConsumesBudget(t *testing.T) {
	calls := 0
	skipper := predictiveTTFBFunc(func(int) (PredictiveTTFBDecision, bool) {
		calls++
		return PredictiveTTFBDecision{Reason: predictiveTTFBReason}, true
	})
	decided := false

	decision, skip := predictiveDecisionForCandidate(skipper, &decided, 2, 1)
	if !skip || decision.Reason != predictiveTTFBReason || calls != 1 {
		t.Fatalf("first decision = (%+v, %v), calls=%d", decision, skip, calls)
	}
	if _, skip := predictiveDecisionForCandidate(skipper, &decided, 2, 2); skip || calls != 1 {
		t.Fatalf("decision budget should be consumed: skip=%v calls=%d", skip, calls)
	}
}

func TestPredictiveDecisionForCandidate_NeverSkipsLastCandidate(t *testing.T) {
	called := false
	skipper := predictiveTTFBFunc(func(int) (PredictiveTTFBDecision, bool) {
		called = true
		return PredictiveTTFBDecision{}, true
	})
	decided := false
	if _, skip := predictiveDecisionForCandidate(skipper, &decided, 1, 1); skip || called {
		t.Fatalf("single candidate must not be queried or skipped: skip=%v called=%v", skip, called)
	}
}

type predictiveTTFBFunc func(int) (PredictiveTTFBDecision, bool)

func (f predictiveTTFBFunc) ShouldSkip(id int) (PredictiveTTFBDecision, bool) {
	return f(id)
}
