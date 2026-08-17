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

	sourceVersion atomic.Int64
	overflowUntil atomic.Int64
}

type projectedCredential struct {
	mode  string
	depth int64
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
	p.mu.Unlock()
}

// ObserveQueue applies one immutable dispatch transition.
func (p *QueueProjection) ObserveQueue(observation QueueObservation) {
	if p == nil || !p.wired.Load() {
		return
	}
	switch observation.Kind {
	case QueueOverflow:
		p.overflowUntil.Store(time.Now().Add(5 * time.Second).UnixNano())
		return
	case QueueRequestCompleted:
		if observation.Completed != nil {
			p.waterfall.push(cloneWaterfallRequest(*observation.Completed))
		}
		return
	}

	p.mu.Lock()
	if !p.wired.Load() {
		p.mu.Unlock()
		return
	}
	switch observation.Kind {
	case QueueModelDepth:
		if observation.Model != "" {
			depth := observation.Depth
			if observation.Delta != 0 {
				depth = p.models[observation.Model] + observation.Delta
			}
			p.models[observation.Model] = depth
		}
	case QueueCredentialDepth:
		if observation.CredentialID > 0 {
			lane := p.credentials[observation.CredentialID]
			mode := observation.Mode
			if mode == "" {
				mode = lane.mode
			}
			depth := observation.Depth
			if observation.Delta != 0 {
				depth = lane.depth + observation.Delta
			}
			p.credentials[observation.CredentialID] = projectedCredential{mode: mode, depth: depth}
		}
	case QueueInFlight:
		if observation.Delta != 0 {
			observation.InFlight = p.inFlight + observation.Delta
		}
		p.inFlight = nonNegative(observation.InFlight)
	case QueueGovernorDegraded:
		if observation.CredentialID > 0 {
			p.degraded[observation.CredentialID] = observation.Degraded
		}
	}
	p.mu.Unlock()
}

func nonNegative(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

// Snapshot returns a sorted, detached queue read model.
func (p *QueueProjection) Snapshot() *SnapshotView {
	if p == nil || !p.wired.Load() {
		return &SnapshotView{Enabled: IsDispatchEnabled(), Wired: false, Models: []LaneView{}, Credentials: []LaneView{}}
	}
	enabled := IsDispatchEnabled()
	p.mu.RLock()
	if !p.wired.Load() {
		p.mu.RUnlock()
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
		credentials = append(credentials, LaneView{Credential: credentialID, Mode: lane.mode, Depth: laneDepth})
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
	p.mu.RUnlock()

	sort.Slice(models, func(i, j int) bool { return models[i].Model < models[j].Model })
	sort.Slice(credentials, func(i, j int) bool { return credentials[i].Credential < credentials[j].Credential })
	view := &SnapshotView{Enabled: enabled, Wired: true, Models: models, Credentials: credentials}
	if !enabled {
		return view
	}

	p50, p95 := waitingPercentilesFromRequests(p.waterfall.snapshot(waterfallRingCap, "", 0))
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
func (p *QueueProjection) SnapshotWaterfall(limit int, model string, credentialID int) WaterfallSnapshot {
	snapshot := WaterfallSnapshot{
		Requests: []WaterfallRequest{},
		Enabled:  IsDispatchEnabled(),
		Wired:    p != nil && p.wired.Load(),
		BottleneckDiagnosis: BottleneckDiagnosis{
			Bottleneck: "none",
			Message:    "队列正常",
		},
	}
	if p == nil || !p.wired.Load() {
		snapshot.BottleneckDiagnosis.Message = "dispatch queue projection not wired"
		return snapshot
	}
	requests := p.waterfall.snapshot(limit, model, credentialID)
	snapshot.Requests = requests
	if len(requests) > 0 {
		end := requests[0].ResponseEndAt
		if end == "" {
			end = requests[0].ArrivedAt
		}
		snapshot.TimeRange = &WaterfallTimeRange{Start: requests[len(requests)-1].ArrivedAt, End: end}
	}
	view := p.Snapshot()
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

func waitingPercentilesFromRequests(requests []WaterfallRequest) (p50, p95 *int64) {
	waits := make([]int, 0, len(requests))
	for _, request := range requests {
		if request.ArrivedAt == "" || request.CredDequeuedAt == "" || request.QueueWaitMS <= 0 {
			continue
		}
		waits = append(waits, request.QueueWaitMS)
	}
	if len(waits) == 0 {
		return nil, nil
	}
	sort.Ints(waits)
	p50Value := int64(nearestRank(waits, 50))
	p95Value := int64(nearestRank(waits, 95))
	return &p50Value, &p95Value
}
