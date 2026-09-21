package dispatch

import (
	"context"
	"sync/atomic"
)

// Pipeline-side queue-backend integration (V6-W1.7 U3,
// docs/架构优化v6/10-dual-backend-queue.md §2.3). All helpers are no-ops
// without a backend (or with the local pass-through backend), so the
// pre-W1.7 behavior is bit-identical — pinned by the full dispatch suite
// passing unmodified.

// SetQueueBackend wires the cluster admission plane. nil keeps the pure
// in-process behavior. The composition root (wireDispatchQueueBackend) calls
// Open before wiring; Pipeline.Stop closes the backend.
func (p *Pipeline) SetQueueBackend(b QueueBackend) {
	p.queueBackend = b
}

// clusterActive reports whether cluster admission accounting applies.
func (p *Pipeline) clusterActive() bool {
	b := p.queueBackend
	return b != nil && b.Kind() != QueueBackendLocal
}

// admitTotal is the cluster-side Tier-0 gate (Submit + due re-admission).
// false → the existing OverflowError path.
func (p *Pipeline) admitTotal(ctx context.Context, qr *QueuedRequest) bool {
	if !p.clusterActive() {
		return true
	}
	b := p.queueBackend
	adm, ok := b.TryAdmitTotal(ctx, qr, p.config().TotalQueueCapacity)
	if !ok {
		metricQueueBackendRejected.WithLabelValues(string(b.Kind()), string(LaneTotal)).Inc()
		return false
	}
	if adm.Kind != "" {
		qr.clusterTotal.Store(&adm)
		metricQueueBackendAdmit.WithLabelValues(string(b.Kind()), string(adm.Kind)).Inc()
	}
	return true
}

// reserveLane reserves one cluster lane slot for the request. slot receives
// the token (take-once release). cap <= 0 or no backend → free pass.
func (p *Pipeline) reserveLane(ctx context.Context, kind LaneKind, id string, cap int, slot *atomic.Pointer[Admission]) bool {
	if !p.clusterActive() || cap <= 0 {
		return true
	}
	b := p.queueBackend
	adm, ok := b.TryReserveLane(ctx, kind, id, cap)
	if !ok {
		metricQueueBackendRejected.WithLabelValues(string(b.Kind()), string(kind)).Inc()
		return false
	}
	if adm.Kind != "" {
		slot.Store(&adm)
		metricQueueBackendAdmit.WithLabelValues(string(b.Kind()), string(adm.Kind)).Inc()
	}
	return true
}

// releaseClusterTotal returns the request's Tier-0 cluster admission.
// Take-once: racing releasers (drainer, cancel path, complete sweep) release
// exactly one token (invariant 2 symmetry).
func (p *Pipeline) releaseClusterTotal(qr *QueuedRequest) {
	if p.queueBackend == nil {
		return
	}
	if a := qr.clusterTotal.Swap(nil); a != nil {
		p.queueBackend.Release(*a)
		metricQueueBackendRelease.WithLabelValues(string(p.queueBackend.Kind()), string(a.Kind)).Inc()
	}
}

// releaseTotal mirrors the existing totalQueue.release sites: local slot +
// cluster admission return together.
func (p *Pipeline) releaseTotal(qr *QueuedRequest) {
	p.totalQueue.release(qr)
	p.releaseClusterTotal(qr)
}

// releaseLaneAdmission returns a lane token (model/credential) at the lane
// dequeue points.
func (p *Pipeline) releaseLaneAdmission(slot *atomic.Pointer[Admission]) {
	if p.queueBackend == nil {
		return
	}
	if a := slot.Swap(nil); a != nil {
		p.queueBackend.Release(*a)
		metricQueueBackendRelease.WithLabelValues(string(p.queueBackend.Kind()), string(a.Kind)).Inc()
	}
}

// releaseAllClusterAdmissions is the defensive complete()-time sweep: the
// leave-point releases normally already ran; take-once makes this a no-op
// then, and a safety net when a request dies while parked in a lane.
func (p *Pipeline) releaseAllClusterAdmissions(qr *QueuedRequest) {
	if p.queueBackend == nil {
		return
	}
	p.releaseClusterTotal(qr)
	p.releaseLaneAdmission(&qr.clusterModel)
	p.releaseLaneAdmission(&qr.clusterCred)
}
