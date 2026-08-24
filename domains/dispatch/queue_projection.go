package dispatch

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// QueueObservationKind identifies one immutable queue-state transition.
type QueueObservationKind string

const (
	// QueueModelDepth changes one model lane depth.
	QueueModelDepth QueueObservationKind = "model_depth"
	// QueueCredentialDepth changes one credential lane depth.
	QueueCredentialDepth QueueObservationKind = "credential_depth"
	// QueueInFlight changes aggregate in-flight requests.
	QueueInFlight QueueObservationKind = "in_flight"
	// QueueOverflow marks a recent queue or observation overflow.
	QueueOverflow QueueObservationKind = "overflow"
	// QueueGovernorDegraded records an unpaced credential governor.
	QueueGovernorDegraded QueueObservationKind = "governor_degraded"
	// QueueRequestCompleted appends one immutable waterfall sample.
	QueueRequestCompleted QueueObservationKind = "request_completed"
	// QueueLifecycleEviction records lifecycle-registry completed-entry eviction.
	QueueLifecycleEviction QueueObservationKind = "lifecycle_eviction"
	// QueueCredentialFull records an early routing skip because the Tier-2 queue is full.
	QueueCredentialFull QueueObservationKind = "credential_full"
)

// QueueObservation carries one immutable queue transition. Delta is used for
// concurrent queue counters; Depth/InFlight remain absolute diagnostics.
type QueueObservation struct {
	// Kind names the transition kind.
	Kind QueueObservationKind
	// Model names the model lane for QueueModelDepth.
	Model string
	// CredentialID names the credential lane for QueueCredentialDepth.
	CredentialID int
	// Mode is the concurrency mode label for credential lane updates.
	Mode string
	// Depth is the absolute diagnostic depth (ignored when Delta != 0).
	Depth int64
	// AbsoluteDepth makes Depth authoritative even when Delta is also present.
	// Pipeline transition sites set it because independent goroutines can emit
	// enqueue/dequeue events out of order; read-model counters must not drift.
	AbsoluteDepth bool
	// Limit is the bounded Tier-2 capacity for QueueCredentialFull.
	Limit int64
	// Delta accumulates into the lane counter when non-zero.
	Delta int64
	// InFlight is the absolute in-flight diagnostic value.
	InFlight int64
	// Degraded records the latest governor-degraded observation for a credential.
	Degraded bool
	// OverflowReason is recorded on QueueOverflow.
	OverflowReason string
	// Completed carries the immutable waterfall sample on QueueRequestCompleted.
	Completed *WaterfallRequest
	// EvictedRequestIDs lists registry entries evicted on
	// QueueLifecycleEviction.
	EvictedRequestIDs []string
}

// QueueObservationSink receives queue-state transitions. Implementations must
// keep ObserveQueue bounded because it runs at the dispatch transition site.
type QueueObservationSink interface {
	// ObserveQueue applies one immutable queue transition to the sink.
	ObserveQueue(QueueObservation)
}

// QueueObservationSinkFunc adapts a function to QueueObservationSink.
type QueueObservationSinkFunc func(QueueObservation)

// ObserveQueue calls the wrapped queue observation function.
func (f QueueObservationSinkFunc) ObserveQueue(observation QueueObservation) {
	if f != nil {
		f(observation)
	}
}

// QueueProjection is the independent read model for admin queue and waterfall
// APIs. It never reads Pipeline maps, channels, gauges, or locks.
type QueueProjection struct {
	wired atomic.Bool

	mu          sync.RWMutex
	models      map[string]int64
	credentials map[int]projectedCredential
	degraded    map[int]bool
	inFlight    int64
	waterfall   *waterfallRing
	// waitsSorted mirrors the qualifying QueueWaitMS values of the
	// waterfall ring, ascending. Maintained incrementally at push time so
	// Snapshot computes percentiles without re-snapshotting the ring.
	// Bounded by the ring capacity; guarded by mu.
	waitsSorted []int

	sourceVersion atomic.Int64
	overflowUntil atomic.Int64
}

type projectedCredential struct {
	mode  string
	depth int64
	limit int64
	full  bool
}

// NewQueueProjection constructs a wired projection. A nil projection is the
// explicit degraded/unwired state used by composition code.
func NewQueueProjection() *QueueProjection {
	projection := &QueueProjection{
		models:      make(map[string]int64),
		credentials: make(map[int]projectedCredential),
		degraded:    make(map[int]bool),
		waterfall:   newWaterfallRing(waterfallRingCap),
	}
	projection.wired.Store(true)
	return projection
}

// Close makes subsequent observations no-ops and releases the projection's
// read model for garbage collection. Snapshots remain safe and report unwired.
func (p *QueueProjection) Close() {
	if p == nil || !p.wired.CompareAndSwap(true, false) {
		return
	}
	p.mu.Lock()
	p.models = nil
	p.credentials = nil
	p.degraded = nil
	p.waterfall = nil
	p.waitsSorted = nil
	p.mu.Unlock()
}

// ObserveQueue applies one immutable dispatch transition.
func (p *QueueProjection) ObserveQueue(observation QueueObservation) {
	if p == nil {
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.wired.Load() {
		return
	}

	switch observation.Kind {
	case QueueOverflow:
		p.overflowUntil.Store(time.Now().Add(5 * time.Second).UnixNano())
	case QueueRequestCompleted:
		if observation.Completed != nil && p.waterfall != nil {
			evicted, didEvict := p.waterfall.push(cloneWaterfallRequest(*observation.Completed))
			p.observeWaitSample(*observation.Completed, evicted, didEvict)
		}
	case QueueModelDepth:
		if observation.Model == "" {
			return
		}
		depth := observation.Depth
		if observation.Delta != 0 && !observation.AbsoluteDepth {
			depth = p.models[observation.Model] + observation.Delta
		}
		depth = nonNegative(depth)
		if depth == 0 {
			delete(p.models, observation.Model)
			return
		}
		p.models[observation.Model] = depth
	case QueueCredentialDepth:
		if observation.CredentialID <= 0 {
			return
		}
		lane := p.credentials[observation.CredentialID]
		mode := observation.Mode
		if mode == "" {
			mode = lane.mode
		}
		depth := observation.Depth
		if observation.Delta != 0 && !observation.AbsoluteDepth {
			depth = lane.depth + observation.Delta
		}
		depth = nonNegative(depth)
		if depth == 0 {
			delete(p.credentials, observation.CredentialID)
			return
		}
		full := lane.full && (lane.limit <= 0 || depth >= lane.limit)
		p.credentials[observation.CredentialID] = projectedCredential{mode: mode, depth: depth, limit: lane.limit, full: full}
	case QueueCredentialFull:
		if observation.CredentialID <= 0 {
			return
		}
		lane := p.credentials[observation.CredentialID]
		mode := observation.Mode
		if mode == "" {
			mode = lane.mode
		}
		limit := observation.Limit
		if limit <= 0 {
			limit = lane.limit
		}
		p.credentials[observation.CredentialID] = projectedCredential{
			mode: mode, depth: nonNegative(observation.Depth), limit: limit, full: true,
		}
	case QueueInFlight:
		if observation.Delta != 0 {
			observation.InFlight = p.inFlight + observation.Delta
		}
		p.inFlight = nonNegative(observation.InFlight)

	case QueueGovernorDegraded:
		if observation.CredentialID <= 0 {
			return
		}
		if observation.Degraded {
			p.degraded[observation.CredentialID] = true
			return
		}
		delete(p.degraded, observation.CredentialID)
	}
}

func nonNegative(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

// waitSampleQualifies mirrors the sample filter of the former
// per-snapshot percentile scan: both boundary timestamps must be present
// and the wait must be positive.
func waitSampleQualifies(r WaterfallRequest) bool {
	return r.ArrivedAt != "" && r.CredDequeuedAt != "" && r.QueueWaitMS > 0
}

// observeWaitSample keeps waitsSorted aligned with the ring window. It runs
// under the projection write lock at push time: one binary search plus one
// int memmove insert, and the symmetric removal for the evicted sample.
func (p *QueueProjection) observeWaitSample(item, evicted WaterfallRequest, didEvict bool) {
	if didEvict && waitSampleQualifies(evicted) {
		v := evicted.QueueWaitMS
		i := sort.SearchInts(p.waitsSorted, v)
		if i < len(p.waitsSorted) && p.waitsSorted[i] == v {
			p.waitsSorted = append(p.waitsSorted[:i], p.waitsSorted[i+1:]...)
		}
	}
	if waitSampleQualifies(item) {
		v := item.QueueWaitMS
		i := sort.SearchInts(p.waitsSorted, v)
		p.waitsSorted = append(p.waitsSorted, 0)
		copy(p.waitsSorted[i+1:], p.waitsSorted[i:])
		p.waitsSorted[i] = v
	}
}

// Snapshot returns a sorted, detached queue read model.
func (p *QueueProjection) Snapshot() *SnapshotView {
	if p == nil || !p.wired.Load() {
		return &SnapshotView{Enabled: true, Wired: false, Models: []LaneView{}, Credentials: []LaneView{}}
	}
	enabled := true // AUDIT_24H B2b: dispatch is the only path
	p.mu.RLock()
	defer p.mu.RUnlock()
	if !p.wired.Load() || p.waterfall == nil {
		return &SnapshotView{Enabled: enabled, Wired: false, Models: []LaneView{}, Credentials: []LaneView{}}
	}

	models := make([]LaneView, 0, len(p.models))
	credentials := make([]LaneView, 0, len(p.credentials))
	var depth int64
	for model, laneDepth := range p.models {
		laneDepth = nonNegative(laneDepth)
		models = append(models, LaneView{Model: model, Depth: laneDepth})
		depth += laneDepth
	}
	for credentialID, lane := range p.credentials {
		laneDepth := nonNegative(lane.depth)
		credentials = append(credentials, LaneView{Credential: credentialID, Mode: lane.mode, Depth: laneDepth, Limit: lane.limit, Full: lane.full})
		depth += laneDepth
	}
	inFlight := p.inFlight
	governorDegraded := false
	for _, degraded := range p.degraded {
		if degraded {
			governorDegraded = true
			break
		}
	}
	// Percentiles read the incrementally maintained window instead of
	// re-snapshotting the full ring. Fresh boxes per call keep the view
	// detached from projection state.
	var p50, p95 *int64
	if len(p.waitsSorted) > 0 {
		p50Value := int64(nearestRank(p.waitsSorted, 50))
		p95Value := int64(nearestRank(p.waitsSorted, 95))
		p50, p95 = &p50Value, &p95Value
	}
	sort.Slice(models, func(i, j int) bool { return models[i].Model < models[j].Model })
	sort.Slice(credentials, func(i, j int) bool { return credentials[i].Credential < credentials[j].Credential })
	view := &SnapshotView{Enabled: enabled, Wired: true, Models: models, Credentials: credentials}
	view.SourceVersion = p.sourceVersion.Add(1)
	view.Pipeline = &PipelineQueueStats{
		Depth:        depth,
		WaitingMsP50: p50,
		WaitingMsP95: p95,
		InFlight:     inFlight,
		Degraded:     governorDegraded || time.Now().UnixNano() <= p.overflowUntil.Load(),
	}
	return view
}

// SnapshotWaterfall returns the admin timeline from projection-owned state.
// tenantID empty = all tenants (platform ops).
func (p *QueueProjection) SnapshotWaterfall(limit int, model string, credentialID int, tenantID string) WaterfallSnapshot {
	snapshot := WaterfallSnapshot{
		Requests: []WaterfallRequest{}, Enabled: true,
		BottleneckDiagnosis: BottleneckDiagnosis{Bottleneck: "none", Message: "队列正常"},
	}
	if p == nil {
		snapshot.Wired = false
		snapshot.BottleneckDiagnosis.Message = "dispatch queue projection not wired"
		return snapshot
	}

	p.mu.RLock()
	if !p.wired.Load() || p.waterfall == nil {
		p.mu.RUnlock()
		snapshot.Wired = false
		snapshot.BottleneckDiagnosis.Message = "dispatch queue projection not wired"
		return snapshot
	}
	requests := p.waterfall.snapshot(limit, model, credentialID, tenantID)
	p.mu.RUnlock()

	snapshot.Wired = true
	snapshot.Requests = requests
	if len(requests) > 0 {
		snapshot.Source = "memory"
		end := requests[0].ResponseEndAt
		if end == "" {
			end = requests[0].ArrivedAt
		}
		snapshot.TimeRange = &WaterfallTimeRange{Start: requests[len(requests)-1].ArrivedAt, End: end}
	} else {
		snapshot.Source = "none"
	}

	view := p.Snapshot()
	if !view.Wired {
		snapshot.Wired = false
		snapshot.Requests = []WaterfallRequest{}
		snapshot.TimeRange = nil
		snapshot.BottleneckDiagnosis.Message = "dispatch queue projection not wired"
		return snapshot
	}
	models := make([]QueueSnapshot, 0, len(view.Models))
	credentials := make([]QueueSnapshot, 0, len(view.Credentials))
	for _, lane := range view.Models {
		models = append(models, QueueSnapshot{Model: lane.Model, Depth: lane.Depth})
	}
	for _, lane := range view.Credentials {
		credentials = append(credentials, QueueSnapshot{Credential: lane.Credential, Mode: lane.Mode, Depth: lane.Depth})
	}
	snapshot.BottleneckDiagnosis = diagnoseBottleneck(models, credentials)
	return snapshot
}

func cloneWaterfallRequest(request WaterfallRequest) WaterfallRequest {
	request.Attempts = append([]WaterfallAttempt(nil), request.Attempts...)
	return request
}
