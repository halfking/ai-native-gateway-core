package autoroute

import "sync"

// classification_feedback.go — V3 in-process aggregation of classification
// feedback and routing-cost outcomes (plan §3.1). Every event is mirrored
// into Prometheus immediately; the in-memory aggregates exist so a later
// async-persistence worker (plan "后续可接入异步持久化") can drain
// summaries via Snapshot/Flush without touching the hot request path again.
//
// Cardinality note: taskType and tier come from bounded enums (classifier
// task types, scoring tiers); they are safe as map keys and metric labels.

// ClassificationFeedbackSummary aggregates feedback verdicts for one task type.
type ClassificationFeedbackSummary struct {
	TaskType  string
	Correct   uint64
	Incorrect uint64
}

// CorrectRate returns correct/(correct+incorrect); 0 when no feedback yet.
func (s ClassificationFeedbackSummary) CorrectRate() float64 {
	total := s.Correct + s.Incorrect
	if total == 0 {
		return 0
	}
	return float64(s.Correct) / float64(total)
}

// CostTierSummary aggregates routing cost outcomes for one tier.
type CostTierSummary struct {
	Tier          string
	Requests      uint64
	ActualTotal   float64
	BaselineTotal float64
}

// SavedTotal is baseline minus actual; negative spend-vs-baseline deltas
// count as zero saving here (they still show up in the raw totals).
func (s CostTierSummary) SavedTotal() float64 {
	if saved := s.BaselineTotal - s.ActualTotal; saved > 0 {
		return saved
	}
	return 0
}

// ClassificationFeedbackSnapshot is a point-in-time deep copy of all
// aggregates. Safe for callers to retain and mutate.
type ClassificationFeedbackSnapshot struct {
	FeedbackByTask map[string]ClassificationFeedbackSummary
	CostsByTier    map[string]CostTierSummary
}

// ClassificationFeedbackSink receives flushed snapshots. Implementations
// must be safe for concurrent use and must not call back into the
// aggregator (Flush holds no lock while dispatching, but re-entrancy would
// be a design smell).
type ClassificationFeedbackSink interface {
	PersistClassificationFeedback(snapshot ClassificationFeedbackSnapshot)
}

type classificationFeedbackAccumulator struct {
	summary ClassificationFeedbackSummary
}

type costTierAccumulator struct {
	summary CostTierSummary
}

// ClassificationFeedbackAggregator is the in-process feedback/cost recorder.
// All methods are nil-receiver safe, so a disabled aggregator is a drop-in
// no-op.
type ClassificationFeedbackAggregator struct {
	mu       sync.Mutex
	feedback map[string]*classificationFeedbackAccumulator
	costs    map[string]*costTierAccumulator
	sink     ClassificationFeedbackSink
}

// NewClassificationFeedbackAggregator creates an empty aggregator.
func NewClassificationFeedbackAggregator() *ClassificationFeedbackAggregator {
	return &ClassificationFeedbackAggregator{
		feedback: make(map[string]*classificationFeedbackAccumulator),
		costs:    make(map[string]*costTierAccumulator),
	}
}

// SetSink attaches an optional persistence sink. May be called at any time;
// nil disables persistence.
func (a *ClassificationFeedbackAggregator) SetSink(sink ClassificationFeedbackSink) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sink = sink
}

// RecordFeedback records one feedback verdict for a task type.
func (a *ClassificationFeedbackAggregator) RecordFeedback(taskType string, correct bool) {
	if a == nil {
		return
	}
	recordClassificationFeedback(taskType, correct)

	a.mu.Lock()
	defer a.mu.Unlock()
	acc, ok := a.feedback[taskType]
	if !ok {
		acc = &classificationFeedbackAccumulator{summary: ClassificationFeedbackSummary{TaskType: taskType}}
		a.feedback[taskType] = acc
	}
	if correct {
		acc.summary.Correct++
	} else {
		acc.summary.Incorrect++
	}
}

// RecordCost records one routing outcome. actualCost < 0 means "unknown" and
// is skipped; baseline below actual (routing got more expensive) is kept in
// the raw totals but contributes no saving.
func (a *ClassificationFeedbackAggregator) RecordCost(tier string, actualCost, baselineCost float64) {
	if a == nil {
		return
	}
	recordCostMetrics(tier, actualCost, baselineCost)
	if actualCost < 0 {
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	acc, ok := a.costs[tier]
	if !ok {
		acc = &costTierAccumulator{summary: CostTierSummary{Tier: tier}}
		a.costs[tier] = acc
	}
	acc.summary.Requests++
	acc.summary.ActualTotal += actualCost
	if baselineCost > 0 {
		acc.summary.BaselineTotal += baselineCost
	}
}

// Snapshot returns a deep copy of the current aggregates.
func (a *ClassificationFeedbackAggregator) Snapshot() ClassificationFeedbackSnapshot {
	if a == nil {
		return ClassificationFeedbackSnapshot{
			FeedbackByTask: map[string]ClassificationFeedbackSummary{},
			CostsByTier:    map[string]CostTierSummary{},
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	snapshot := ClassificationFeedbackSnapshot{
		FeedbackByTask: make(map[string]ClassificationFeedbackSummary, len(a.feedback)),
		CostsByTier:    make(map[string]CostTierSummary, len(a.costs)),
	}
	for taskType, acc := range a.feedback {
		snapshot.FeedbackByTask[taskType] = acc.summary
	}
	for tier, acc := range a.costs {
		snapshot.CostsByTier[tier] = acc.summary
	}
	return snapshot
}

// Flush hands the current snapshot to the attached sink, if any. It is the
// hook a future background worker will call on a cadence; today it is a
// no-op unless a sink was set.
func (a *ClassificationFeedbackAggregator) Flush() {
	if a == nil {
		return
	}
	a.mu.Lock()
	sink := a.sink
	a.mu.Unlock()
	if sink == nil {
		return
	}
	sink.PersistClassificationFeedback(a.Snapshot())
}
