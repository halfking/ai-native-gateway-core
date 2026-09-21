package autoroute

import (
	"sync"
	"testing"
)

func TestClassificationFeedbackAggregatorFeedback(t *testing.T) {
	agg := NewClassificationFeedbackAggregator()

	agg.RecordFeedback("reasoning", true)
	agg.RecordFeedback("reasoning", true)
	agg.RecordFeedback("reasoning", false)
	agg.RecordFeedback("code", true)

	snap := agg.Snapshot()
	if len(snap.FeedbackByTask) != 2 {
		t.Fatalf("task types = %d, want 2", len(snap.FeedbackByTask))
	}
	got := snap.FeedbackByTask["reasoning"]
	if got.Correct != 2 || got.Incorrect != 1 {
		t.Errorf("reasoning summary = %+v, want correct=2 incorrect=1", got)
	}
	if rate := got.CorrectRate(); rate < 0.666 || rate > 0.667 {
		t.Errorf("reasoning correct rate = %v, want ~0.6667", rate)
	}
	if rate := (ClassificationFeedbackSummary{}).CorrectRate(); rate != 0 {
		t.Errorf("empty summary correct rate = %v, want 0", rate)
	}
}

func TestClassificationFeedbackAggregatorCost(t *testing.T) {
	agg := NewClassificationFeedbackAggregator()

	agg.RecordCost("standard", 0.02, 0.05)
	agg.RecordCost("standard", 0.01, 0.00) // zero baseline still counts spend
	agg.RecordCost("lite", 0.03, 0.01)     // more expensive than baseline
	agg.RecordCost("standard", -1, 0.10)   // unknown actual, skipped

	snap := agg.Snapshot()
	std := snap.CostsByTier["standard"]
	if std.Requests != 2 {
		t.Errorf("standard requests = %d, want 2 (negative actual skipped)", std.Requests)
	}
	if std.ActualTotal < 0.029 || std.ActualTotal > 0.031 {
		t.Errorf("standard actual total = %v, want ~0.03", std.ActualTotal)
	}
	if std.BaselineTotal != 0.05 {
		t.Errorf("standard baseline total = %v, want 0.05", std.BaselineTotal)
	}
	if saved := std.SavedTotal(); saved < 0.019 || saved > 0.021 {
		t.Errorf("standard saved total = %v, want ~0.02", saved)
	}
	lite := snap.CostsByTier["lite"]
	if saved := lite.SavedTotal(); saved != 0 {
		t.Errorf("lite saved total = %v, want 0 (actual exceeded baseline)", saved)
	}
	if _, ok := snap.CostsByTier["unknown-tier-from-negative"]; ok {
		t.Error("negative-only tier should not exist")
	}
}

func TestClassificationFeedbackAggregatorSnapshotIsDeepCopy(t *testing.T) {
	agg := NewClassificationFeedbackAggregator()
	agg.RecordFeedback("reasoning", true)
	agg.RecordCost("standard", 0.01, 0.02)

	snap := agg.Snapshot()
	fb := snap.FeedbackByTask["reasoning"]
	fb.Correct = 999
	snap.FeedbackByTask["reasoning"] = fb
	cost := snap.CostsByTier["standard"]
	cost.Requests = 999
	snap.CostsByTier["standard"] = cost
	delete(snap.FeedbackByTask, "reasoning")

	fresh := agg.Snapshot()
	if got := fresh.FeedbackByTask["reasoning"].Correct; got != 1 {
		t.Errorf("mutating snapshot leaked into aggregator: correct = %d, want 1", got)
	}
	if got := fresh.CostsByTier["standard"].Requests; got != 1 {
		t.Errorf("mutating snapshot leaked into aggregator: requests = %d, want 1", got)
	}
}

type recordingSink struct {
	mu        sync.Mutex
	snapshots []ClassificationFeedbackSnapshot
}

func (s *recordingSink) PersistClassificationFeedback(snapshot ClassificationFeedbackSnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshots = append(s.snapshots, snapshot)
}

func TestClassificationFeedbackAggregatorFlush(t *testing.T) {
	agg := NewClassificationFeedbackAggregator()

	// No sink: Flush is a no-op.
	agg.RecordFeedback("reasoning", true)
	agg.Flush()

	sink := &recordingSink{}
	agg.SetSink(sink)
	agg.Flush()

	if len(sink.snapshots) != 1 {
		t.Fatalf("sink snapshots = %d, want 1", len(sink.snapshots))
	}
	if got := sink.snapshots[0].FeedbackByTask["reasoning"].Correct; got != 1 {
		t.Errorf("flushed correct = %d, want 1", got)
	}

	// Setting nil sink disables persistence again.
	agg.SetSink(nil)
	agg.Flush()
	if len(sink.snapshots) != 1 {
		t.Errorf("sink snapshots after detach = %d, want unchanged 1", len(sink.snapshots))
	}
}

func TestClassificationFeedbackAggregatorNilSafe(t *testing.T) {
	var agg *ClassificationFeedbackAggregator
	agg.RecordFeedback("reasoning", true)
	agg.RecordCost("standard", 0.01, 0.02)
	agg.SetSink(nil)
	agg.Flush()
	snap := agg.Snapshot()
	if len(snap.FeedbackByTask) != 0 || len(snap.CostsByTier) != 0 {
		t.Errorf("nil aggregator snapshot = %+v, want empty maps", snap)
	}
}

func TestClassificationFeedbackAggregatorConcurrent(t *testing.T) {
	agg := NewClassificationFeedbackAggregator()
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				agg.RecordFeedback("reasoning", j%2 == 0)
				agg.RecordCost("standard", 0.001, 0.002)
				if j%10 == 0 {
					agg.Snapshot()
				}
			}
		}(i)
	}
	wg.Wait()

	snap := agg.Snapshot()
	if got := snap.FeedbackByTask["reasoning"].Correct + snap.FeedbackByTask["reasoning"].Incorrect; got != 16*50 {
		t.Errorf("total feedback = %d, want %d", got, 16*50)
	}
	if got := snap.CostsByTier["standard"].Requests; got != 16*50 {
		t.Errorf("standard requests = %d, want %d", got, 16*50)
	}
}
