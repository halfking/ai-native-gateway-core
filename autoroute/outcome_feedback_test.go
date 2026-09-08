package autoroute

// outcome_feedback_test.go — 2026-09-08 audit (round 2, Track A): proves the
// routingopt feedback loop carries the REAL upstream outcome instead of the
// decision-time placeholder, plus the non-blocking guarantees that must not
// regress (bounded registry, bounded write semaphore with drop-on-full).

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/routingopt"
)

// resetOutcomeRegistry isolates package-level registry state between tests.
// Counters are left running; assertions use deltas via outcomeStatsSnapshot.
func resetOutcomeRegistry(t *testing.T) {
	t.Helper()
	pendingFeedbackMu.Lock()
	pendingFeedback = map[string]*pendingFeedbackEntry{}
	pendingFeedbackOrder.Init()
	pendingFeedbackMu.Unlock()
}

// outcomeStatsSnapshot reads the registry counters.
func outcomeStatsSnapshot() (stashed, matched, orphan, expired, dropped int64) {
	return RoutingOutcomeStats()
}

// waitForFeedback polls until the stub optimizer has the wanted number of
// feedback rows, or fails after a short deadline.
func waitForFeedback(t *testing.T, ranker *rankingOptimizer, want int) []routingopt.RoutingFeedback {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		ranker.mu.Lock()
		got := append([]routingopt.RoutingFeedback(nil), ranker.feedbacks...)
		ranker.mu.Unlock()
		if len(got) >= want {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected %d feedback rows, got %d within deadline", want, len(got))
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// newFeedbackTestDecider builds a decider whose classifier confidence is
// observable (0.42) so Confidence propagation can be asserted end to end.
func newFeedbackTestDecider(t *testing.T) (*Decider, *rankingOptimizer) {
	t.Helper()
	cls := &stubClassifier{name: "heuristic", out: &Classification{
		Primary: TaskChat, Confidence: 0.42, Classifier: "heuristic",
	}}
	idx := &stubIndex{cands: []ScoredCandidate{
		{Candidate: Candidate{CanonicalName: "model-a", CredentialID: 1, RawModel: "model-a"}},
	}}
	d := NewDecider(cls, nil, idx, NewMemoryProfileStore())
	ranker := &rankingOptimizer{}
	d.SetOptimizer(ranker)
	return d, ranker
}

// TestRecordFeedback_RealOutcomeBackfill proves the core Track A closure: a
// decision made with a request id writes NOTHING at decision time; the real
// outcome reported by the completion path produces exactly one feedback row
// with the true success flag, latency, cost and decision confidence.
func TestRecordFeedback_RealOutcomeBackfill(t *testing.T) {
	resetOutcomeRegistry(t)
	d, ranker := newFeedbackTestDecider(t)

	ctx := WithRequestID(context.Background(), "req-backfill-1")
	if _, err := d.Decide(ctx, ClassificationSignals{}, 42, "", "", "sess-1"); err != nil {
		t.Fatalf("Decide failed: %v", err)
	}

	// Decision time: feedback must be parked, not written (the old code wrote
	// IsSuccess=true here — assert that placeholder is gone).
	ranker.mu.Lock()
	if n := len(ranker.feedbacks); n != 0 {
		t.Fatalf("decision-time placeholder must not be written, got %d rows", n)
	}
	ranker.mu.Unlock()
	if stashed, _, _, _, _ := outcomeStatsSnapshot(); stashed < 1 {
		t.Fatalf("decision should have been stashed, stats=%v", outcomeStatsTuple())
	}

	// Request completes with a failure — the signal the old loop could never see.
	ReportRoutingOutcome(RoutingOutcome{
		RequestID: "req-backfill-1",
		Success:   false,
		LatencyMs: 1234,
		CostUSD:   0.25,
	})

	fbs := waitForFeedback(t, ranker, 1)
	fb := fbs[0]
	if fb.RequestID != "req-backfill-1" {
		t.Fatalf("feedback should carry the real request id, got %q", fb.RequestID)
	}
	if fb.IsSuccess {
		t.Fatal("feedback must carry the real (failed) outcome, got IsSuccess=true")
	}
	if fb.Latency != 1234*time.Millisecond {
		t.Fatalf("feedback should carry settled latency, got %s", fb.Latency)
	}
	if fb.Cost != 0.25 {
		t.Fatalf("feedback should carry settled cost, got %f", fb.Cost)
	}
	if fb.Confidence != 0.42 {
		t.Fatalf("feedback should carry decision confidence, got %f", fb.Confidence)
	}
	if _, matched, _, _, _ := outcomeStatsSnapshot(); matched < 1 {
		t.Fatalf("outcome should have matched a stashed decision, stats=%v", outcomeStatsTuple())
	}

	// A replayed outcome for the same request must not double-count.
	ReportRoutingOutcome(RoutingOutcome{RequestID: "req-backfill-1", Success: true})
	time.Sleep(50 * time.Millisecond)
	ranker.mu.Lock()
	n := len(ranker.feedbacks)
	ranker.mu.Unlock()
	if n != 1 {
		t.Fatalf("replayed outcome must be ignored (orphan), got %d rows", n)
	}
}

// TestRecordFeedback_WithoutRequestID_KeepsLegacyPlaceholder proves paths
// without a correlation id (internal callers, tests) behave exactly as
// before: immediate decision-time write, synthetic id, IsSuccess=true.
func TestRecordFeedback_WithoutRequestID_KeepsLegacyPlaceholder(t *testing.T) {
	resetOutcomeRegistry(t)
	d, ranker := newFeedbackTestDecider(t)

	if _, err := d.Decide(context.Background(), ClassificationSignals{}, 7, "", "", "sess-2"); err != nil {
		t.Fatalf("Decide failed: %v", err)
	}

	fbs := waitForFeedback(t, ranker, 1)
	fb := fbs[0]
	if len(fb.RequestID) == 0 || fb.RequestID[:5] != "auto-" {
		t.Fatalf("expected synthetic auto- request id, got %q", fb.RequestID)
	}
	if !fb.IsSuccess {
		t.Fatal("legacy placeholder path keeps IsSuccess=true")
	}
	if fb.Confidence != 0.42 {
		t.Fatalf("confidence must be populated on the legacy path too, got %f", fb.Confidence)
	}
}

// TestRecordFeedback_OutcomeNeverArrivesIsDropped tests the TTL janitor: a
// stashed decision whose request never reports is dropped — counted, never
// written. A placeholder IsSuccess=true row would label rate-limited/crashed
// requests as routing successes (2026-09-09 audit round 3).
func TestRecordFeedback_OutcomeNeverArrivesIsDropped(t *testing.T) {
	resetOutcomeRegistry(t)
	oldTTL := pendingFeedbackTTL
	pendingFeedbackTTL = 5 * time.Millisecond
	defer func() { pendingFeedbackTTL = oldTTL }()

	d, ranker := newFeedbackTestDecider(t)
	if _, err := d.Decide(WithRequestID(context.Background(), "req-late"), ClassificationSignals{}, 9, "", "", ""); err != nil {
		t.Fatalf("Decide failed: %v", err)
	}
	time.Sleep(10 * time.Millisecond)
	evictExpiredPendingFeedback()

	time.Sleep(50 * time.Millisecond)
	ranker.mu.Lock()
	n := len(ranker.feedbacks)
	ranker.mu.Unlock()
	if n != 0 {
		t.Fatalf("TTL-expired decision must not be written as placeholder, got %d rows", n)
	}
	if _, _, _, expired, _ := outcomeStatsSnapshot(); expired < 1 {
		t.Fatalf("expiry should be counted, stats=%v", outcomeStatsTuple())
	}

	// A late outcome for an already-expired decision is an orphan, not a row.
	ReportRoutingOutcome(RoutingOutcome{RequestID: "req-late", Success: true})
	time.Sleep(50 * time.Millisecond)
	ranker.mu.Lock()
	n = len(ranker.feedbacks)
	ranker.mu.Unlock()
	if n != 0 {
		t.Fatalf("late outcome after expiry must be orphaned, got %d rows", n)
	}
}

// TestRecordFeedback_RegistryBoundEvictsOldest proves the registry never
// grows unbounded: overflowing it evicts the oldest stashed decision (dropped
// and counted, since its outcome never arrived) rather than blocking or
// dropping the new one.
func TestRecordFeedback_RegistryBoundEvictsOldest(t *testing.T) {
	resetOutcomeRegistry(t)
	oldMax := maxPendingFeedback
	maxPendingFeedback = 2
	defer func() { maxPendingFeedback = oldMax }()

	d, ranker := newFeedbackTestDecider(t)
	for i, id := range []string{"req-a", "req-b", "req-c"} {
		if _, err := d.Decide(WithRequestID(context.Background(), id), ClassificationSignals{}, 11, "", "", ""); err != nil {
			t.Fatalf("Decide %d failed: %v", i, err)
		}
	}
	// req-a (oldest) was evicted without a write; only the two newest stay
	// pending, both settle for real.
	time.Sleep(50 * time.Millisecond)
	ranker.mu.Lock()
	n := len(ranker.feedbacks)
	ranker.mu.Unlock()
	if n != 0 {
		t.Fatalf("evicted entry must not be written as placeholder, got %d rows", n)
	}
	if _, _, _, expired, _ := outcomeStatsSnapshot(); expired < 1 {
		t.Fatalf("eviction should be counted, stats=%v", outcomeStatsTuple())
	}
	ReportRoutingOutcome(RoutingOutcome{RequestID: "req-b", Success: false})
	ReportRoutingOutcome(RoutingOutcome{RequestID: "req-c", Success: true})
	fbs := waitForFeedback(t, ranker, 2)
	byID := map[string]bool{}
	for _, fb := range fbs {
		byID[fb.RequestID] = fb.IsSuccess
	}
	if byID["req-b"] {
		t.Fatal("req-b settled as failure, feedback said success")
	}
	if !byID["req-c"] {
		t.Fatal("req-c settled as success, feedback said failure")
	}
}

// TestReportRoutingOutcome_NoPendingIsCheapNoop proves outcomes without a
// stashed decision (non-auto requests, session-cache reuse) are counted and
// dropped without touching the optimizer.
func TestReportRoutingOutcome_NoPendingIsCheapNoop(t *testing.T) {
	resetOutcomeRegistry(t)
	_, ranker := newFeedbackTestDecider(t)

	ReportRoutingOutcome(RoutingOutcome{RequestID: "req-unknown", Success: false})
	ReportRoutingOutcome(RoutingOutcome{RequestID: "", Success: false}) // empty id: fully ignored

	ranker.mu.Lock()
	n := len(ranker.feedbacks)
	ranker.mu.Unlock()
	if n != 0 {
		t.Fatalf("orphan outcome must not reach the optimizer, got %d rows", n)
	}
	if _, _, orphan, _, _ := outcomeStatsSnapshot(); orphan < 1 {
		t.Fatalf("orphan should be counted, stats=%v", outcomeStatsTuple())
	}
}

// TestOutcomeBackfill_RespectsWriteBound proves the Track A dispatch keeps
// the bounded-semaphore drop semantics: with all 32 slots held, the backfill
// returns immediately (never blocks the completion path) and sheds the write.
func TestOutcomeBackfill_RespectsWriteBound(t *testing.T) {
	resetOutcomeRegistry(t)
	d, ranker := newFeedbackTestDecider(t)

	if _, err := d.Decide(WithRequestID(context.Background(), "req-bound"), ClassificationSignals{}, 13, "", "", ""); err != nil {
		t.Fatalf("Decide failed: %v", err)
	}

	// Hold every slot so the dispatch cannot proceed.
	release := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < maxConcurrentFeedbackWrites; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case feedbackWriteSlots <- struct{}{}:
				<-release
			case <-time.After(2 * time.Second):
			}
		}()
	}
	// Wait until the slots are actually held (poling the drop counter would
	// race the fillers).
	time.Sleep(50 * time.Millisecond)

	done := make(chan struct{})
	go func() {
		ReportRoutingOutcome(RoutingOutcome{RequestID: "req-bound", Success: false})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("ReportRoutingOutcome blocked on the write bound — drop semantics regressed")
	}

	close(release)
	wg.Wait()

	// The shed write must not arrive after slots free up.
	time.Sleep(50 * time.Millisecond)
	ranker.mu.Lock()
	n := len(ranker.feedbacks)
	ranker.mu.Unlock()
	if n != 0 {
		t.Fatalf("dropped outcome must stay dropped, got %d rows", n)
	}
	if _, _, _, _, dropped := outcomeStatsSnapshot(); dropped < 1 {
		t.Fatalf("drop should be counted, stats=%v", outcomeStatsTuple())
	}
}

// outcomeStatsTuple formats the counters for assertion messages.
func outcomeStatsTuple() string {
	stashed, matched, orphan, expired, dropped := RoutingOutcomeStats()
	return fmt.Sprintf("stashed=%d matched=%d orphan=%d expired=%d dropped=%d",
		stashed, matched, orphan, expired, dropped)
}
