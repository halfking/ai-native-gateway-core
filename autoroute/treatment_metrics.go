package autoroute

import (
	"sort"
	"sync"
	"time"
)

// TreatmentOutcomeKey identifies one bounded evaluation cohort.
type TreatmentOutcomeKey struct {
	Experiment string
	Version    string
	Treatment  Treatment
	TaskType   TaskType
}

// TreatmentOutcome records aggregate request outcomes for a cohort.
type TreatmentOutcome struct {
	Key             TreatmentOutcomeKey
	Samples         uint64
	Successes       uint64
	Failures        uint64
	Unknown         uint64
	LatencySamples  []float64
	ActualCost      float64
	BaselineCost    float64
	FeedbackCorrect uint64
	FeedbackWrong   uint64
}

func (o TreatmentOutcome) FailureRate() float64 {
	known := o.Successes + o.Failures
	if known == 0 {
		return 0
	}
	return float64(o.Failures) / float64(known)
}

func (o TreatmentOutcome) FeedbackAccuracy() float64 {
	total := o.FeedbackCorrect + o.FeedbackWrong
	if total == 0 {
		return 0
	}
	return float64(o.FeedbackCorrect) / float64(total)
}

func (o TreatmentOutcome) P95Latency() float64 {
	if len(o.LatencySamples) == 0 {
		return 0
	}
	values := append([]float64(nil), o.LatencySamples...)
	sort.Float64s(values)
	idx := int(float64(len(values)) * 0.95)
	if idx >= len(values) {
		idx = len(values) - 1
	}
	return values[idx]
}

// TreatmentOutcomeSnapshot is a deep-copyable point-in-time view.
type TreatmentOutcomeSnapshot struct {
	StartedAt time.Time
	EndedAt   time.Time
	Outcomes  map[TreatmentOutcomeKey]TreatmentOutcome
}

type TreatmentOutcomeAggregator struct {
	mu       sync.Mutex
	started  time.Time
	outcomes map[TreatmentOutcomeKey]*TreatmentOutcome
	sink     func(TreatmentOutcomeSnapshot)
}

func NewTreatmentOutcomeAggregator() *TreatmentOutcomeAggregator {
	return &TreatmentOutcomeAggregator{started: time.Now(), outcomes: make(map[TreatmentOutcomeKey]*TreatmentOutcome)}
}

func (a *TreatmentOutcomeAggregator) SetSink(sink func(TreatmentOutcomeSnapshot)) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sink = sink
}

func (a *TreatmentOutcomeAggregator) ensure(key TreatmentOutcomeKey) *TreatmentOutcome {
	o := a.outcomes[key]
	if o == nil {
		o = &TreatmentOutcome{Key: key}
		a.outcomes[key] = o
	}
	return o
}

func (a *TreatmentOutcomeAggregator) Record(key TreatmentOutcomeKey, success *bool, latency time.Duration, actualCost, baselineCost float64) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	o := a.ensure(key)
	o.Samples++
	if success == nil {
		o.Unknown++
	} else if *success {
		o.Successes++
	} else {
		o.Failures++
	}
	if latency >= 0 {
		o.LatencySamples = append(o.LatencySamples, latency.Seconds())
	}
	if actualCost >= 0 {
		o.ActualCost += actualCost
	}
	if baselineCost >= 0 {
		o.BaselineCost += baselineCost
	}
}

func (a *TreatmentOutcomeAggregator) RecordFeedback(key TreatmentOutcomeKey, correct bool) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	o := a.ensure(key)
	if correct {
		o.FeedbackCorrect++
	} else {
		o.FeedbackWrong++
	}
}

func (a *TreatmentOutcomeAggregator) Snapshot() TreatmentOutcomeSnapshot {
	if a == nil {
		return TreatmentOutcomeSnapshot{Outcomes: map[TreatmentOutcomeKey]TreatmentOutcome{}}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	s := TreatmentOutcomeSnapshot{StartedAt: a.started, EndedAt: time.Now(), Outcomes: make(map[TreatmentOutcomeKey]TreatmentOutcome, len(a.outcomes))}
	for k, v := range a.outcomes {
		copy := *v
		copy.LatencySamples = append([]float64(nil), v.LatencySamples...)
		s.Outcomes[k] = copy
	}
	return s
}

func (a *TreatmentOutcomeAggregator) Flush() {
	if a == nil {
		return
	}
	a.mu.Lock()
	sink := a.sink
	a.mu.Unlock()
	if sink != nil {
		sink(a.Snapshot())
	}
}
