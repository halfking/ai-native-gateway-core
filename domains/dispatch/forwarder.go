package dispatch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/liveactions"
)

// credForwarder is the ② Credential Forwarder for one credential. It owns the
// Tier-2 FIFO queue and the per-credential governor that paces forwarding.
//
// The Governor reference is held under govMu (RWMutex) so Stage F
// ApplyPolicy can replace it atomically without rebuilding the
// forwarder's loop goroutine or invalidating the in-flight attempts
// path. Reads from acquire/release paths take the RLock; the
// snapshot observer holds credMu + govMu.RLock as an ordered pair.
// An admitted request carries the Governor selected for Acquire into its
// execution attempt, so its matching Release is never redirected to a
// replacement Governor.
type credForwarder struct {
	cred      CredentialRef
	queue     atomic.Pointer[chan *QueuedRequest]
	handoffMu sync.Mutex
	depth     atomic.Int64
	limit     atomic.Int64
	gov       Governor
	govMu     sync.RWMutex // protects cf.gov (Stage F: hot-swap by ApplyPolicy)
	pipe      *Pipeline
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	// wakeCh unparks the loop goroutine when the channel is swapped by
	// replaceDepth; the loop re-reads *cf.queue on its next select entry.
	wakeCh chan struct{}
	// pendingOld holds the channel displaced by the most recent grow, so a
	// request that raced the swap and landed in it can be reclaimed.
	pendingOld atomic.Pointer[chan *QueuedRequest]
}

func newCredForwarder(cred CredentialRef, queueDepth int, pipe *Pipeline) *credForwarder {
	ctx, cancel := context.WithCancel(context.Background())
	gov := buildForwarderGovernor(pipe, cred)
	cf := &credForwarder{
		cred:   cred,
		gov:    gov,
		pipe:   pipe,
		ctx:    ctx,
		cancel: cancel,
		wakeCh: make(chan struct{}),
	}
	cf.limit.Store(int64(queueDepth))
	q := make(chan *QueuedRequest, queueDepth)
	cf.queue.Store(&q)
	// Track the loop goroutine in the pipeline-wide WaitGroup so Stop() waits
	// for in-flight forwards to finish (concurrency audit 2026-08-13 D3).
	// Safe against Stop's wg.Wait: newCredForwarder is only reached via
	// getOrCreateForwarder under p.credMu with a shutdown guard, so the Add
	// happens-before Stop's credMu section and therefore before wg.Wait.
	pipe.wg.Add(1)
	go cf.loop()
	return cf
}

// buildForwarderGovernor wraps the policy-aware governor constructor with a
// fail-open fallback for the cold-start path. ApplyPolicy itself must
// fail-closed when the backend rejects a spec; but a cold-start forwarder
// cannot block dispatch when the Redis backend is transiently unavailable —
// we degrade to the in-process governor and rely on the publisher's retry
// to install the Redis governor once the cluster recovers.
func buildForwarderGovernor(pipe *Pipeline, cred CredentialRef) Governor {
	gov, err := pipe.governorForCredential(cred, pipe.ActiveRevision())
	if err == nil {
		return gov
	}
	if backend := pipe.GovernorBackend(); backend != nil && backend.Kind() == BackendRedisEnforce {
		slog.Error("dispatch: cold-start governor unavailable; strict backend remains fail-closed",
			"credential_id", cred.CredentialID,
			"provider_id", cred.ProviderID,
			"mode", cred.ConcurrencyMode,
			"error", err)
		return unavailableGovernor{}
	}
	slog.Warn("dispatch: cold-start governor fell back to in-process impl",
		"credential_id", cred.CredentialID,
		"provider_id", cred.ProviderID,
		"mode", cred.ConcurrencyMode,
		"error", err)
	return newGovernor(cred)
}

// govLocked returns the current Governor under govMu.RLock. The returned
// pointer is stable for the duration of the caller's critical section;
// ApplyPolicy swaps it under govMu.Lock so concurrent callers either see
// the old Governor or the new one but never a torn pointer.
func (cf *credForwarder) govLocked() Governor {
	cf.govMu.RLock()
	defer cf.govMu.RUnlock()
	return cf.gov
}

// replaceGov atomically swaps the Governor. Stage F: ApplyPolicy calls
// this once per spec while holding credMu.
func (cf *credForwarder) replaceGov(g Governor) {
	cf.govMu.Lock()
	cf.gov = g
	cf.govMu.Unlock()
}

func (cf *credForwarder) tryReserve() bool {
	for {
		cur := cf.depth.Load()
		if cf.limit.Load() > 0 && cur >= cf.limit.Load() {
			return false
		}
		if cf.depth.CompareAndSwap(cur, cur+1) {
			return true
		}
	}
}

func (cf *credForwarder) CurrentDepth() int64 {
	return cf.depth.Load()
}

// Limit returns the configured bounded queue capacity.
func (cf *credForwarder) Limit() int64 {
	return cf.limit.Load()
}

// HasCapacity reports whether another item can be reserved. tryReserve remains
// the atomic admission gate; this helper is only an early routing hint.
func (cf *credForwarder) HasCapacity() bool {
	return cf.limit.Load() <= 0 || cf.depth.Load() < cf.limit.Load()
}

// loop drains the Tier-2 queue. Governor admission happens in this owner
// goroutine, so requests waiting for a credential slot remain visible in the
// bounded queue instead of escaping into unbounded goroutines.
func (cf *credForwarder) loop() {
	// LIFO: cf.wg.Wait (registered second) runs first, so the pipeline-wide
	// Done only fires after every in-flight attempt goroutine has exited.
	defer cf.pipe.wg.Done()
	defer cf.wg.Wait()
	for {
		// 2026-09-01 fix (queue/concurrency audit P1): reclaim before every
		// select. replaceDepth's wakeCh send is non-blocking, so the wake can
		// be lost when the loop is not parked on select at that instant (e.g.
		// it is busy in the queue-receive branch) — previously nothing would
		// ever trigger reclaimPendingOld again and a request that raced the
		// channel swap into pendingOld was stranded forever. Checking the
		// atomic pointer at the top of each iteration guarantees that ANY
		// later event (queue send, ctx done, another grow's wake) collects
		// the displaced channel. reclaimPendingOld is a cheap no-op when the
		// pointer is nil.
		cf.reclaimPendingOld()
		select {
		case qr, ok := <-*cf.queue.Load():
			if !ok {
				return
			}
			// The producer holds handoffMu while publishing node_enqueued after
			// the channel send. Wait for that publication before the forwarder
			// can emit node_selected, preserving lifecycle order without holding
			// the lock during governor waits or upstream I/O.
			cf.handoffMu.Lock()
			cf.handoffMu.Unlock()
			// V6-W1.7: the request left the cred lane — return the cluster
			// slot (no-op without a redis backend).
			cf.pipe.releaseLaneAdmission(&qr.clusterCred)
			gov, acquired := cf.acquire(qr)
			if !acquired {
				continue
			}
			depth := cf.depth.Add(-1)
			metricCredQueueDepth.WithLabelValues(itoa(cf.cred.CredentialID), cf.cred.ConcurrencyMode).Dec()
			cf.pipe.observeQueue(QueueObservation{Kind: QueueCredentialDepth, CredentialID: cf.cred.CredentialID, Mode: cf.cred.ConcurrencyMode, Depth: depth, Delta: -1, AbsoluteDepth: true})
			cf.wg.Add(1)
			go cf.attempt(qr, gov)
		case <-cf.wakeCh:
			// replaceDepth swapped the channel while we were parked on the old
			// one. Reclaim any request that raced the swap into the displaced
			// channel, then re-enter select to read the new channel.
			cf.reclaimPendingOld()
			continue
		case <-cf.ctx.Done():
			// Drain remaining queued requests and complete them with
			// ErrShutdown so their Submit callers don't block forever on
			// qr.ResultCh. This mirrors runModelDrainer's stopCh handling
			// (pipeline.go runModelDrainer) — the same bug class that
			// Tier-1 was fixed against must not live at Tier-2.
			cf.drainAndComplete()
			return
		}
	}
}

// drainAndComplete non-blockingly drains cf.queue, completing every
// still-buffered request with ErrShutdown. Called once on shutdown.
func (cf *credForwarder) drainAndComplete() {
	cf.reclaimPendingOld()
	for {
		select {
		case qr, ok := <-*cf.queue.Load():
			if !ok {
				return
			}
			// V6-W1.7: shutdown drain is also a lane leave.
			cf.pipe.releaseLaneAdmission(&qr.clusterCred)
			depth := cf.depth.Add(-1)
			metricCredQueueDepth.WithLabelValues(itoa(cf.cred.CredentialID), cf.cred.ConcurrencyMode).Dec()
			cf.pipe.observeQueue(QueueObservation{Kind: QueueCredentialDepth, CredentialID: cf.cred.CredentialID, Mode: cf.cred.ConcurrencyMode, Depth: depth, Delta: -1, AbsoluteDepth: true})
			cf.pipe.complete(qr, ForwardOutcome{Err: ErrShutdown})
		default:
			return
		}
	}
}

// replaceDepth hot-reloads the forwarder's bounded queue depth without
// rebuilding the loop goroutine. The publisher's catch-up query already fires
// a policy revision when max_queue_depth changes; this is the missing consumer
// side (Stage F residual item).
//
// Shrink (newDepth <= current buffer capacity): the channel is left in place
// and only the admission limit (cf.limit, consulted by tryReserve) moves. An
// oversized buffer is harmless because reservations are already capped at
// cf.limit, so no in-flight request is bounced.
//
// Grow (newDepth > current buffer capacity): a larger channel is allocated and
// any still-buffered requests are moved into it so none are dropped. The loop
// goroutine re-reads *cf.queue on every select entry, so after the swap it
// drains the new channel; pendingOld reclaims the micro-race where a producer
// landed in the old channel after the swap.
func (cf *credForwarder) replaceDepth(newDepth int) {
	if newDepth <= 0 {
		newDepth = 1
	}
	if newDepth <= cap(*cf.queue.Load()) {
		// Shrink / no-op: keep the existing channel; only the admission limit
		// moves. An oversized buffer is harmless — reservations are capped at
		// cf.limit, so no in-flight request is bounced.
		cf.limit.Store(int64(newDepth))
		return
	}
	old := cf.queue.Load()
	nq := make(chan *QueuedRequest, newDepth)
	// Move buffered (not-yet-forwarded) requests into the larger channel so a
	// grow never drops an in-flight request. All fit because newDepth > cap(old).
	for {
		select {
		case qr := <-*old:
			nq <- qr
		default:
			goto swapped
		}
	}
swapped:
	cf.queue.Store(&nq)
	cf.limit.Store(int64(newDepth))
	cf.pendingOld.Store(old)
	// Wake the loop if it is parked on the old channel; the loop re-reads
	// *cf.queue on its next select entry regardless, so a dropped send here
	// only adds latency, not a correctness gap.
	select {
	case cf.wakeCh <- struct{}{}:
	default:
	}
}

// reclaimPendingOld moves any request that raced a grow and landed in the
// displaced channel into the live channel. Called at the top of every loop
// iteration (see loop) and from drainAndComplete (on shutdown) so a raced
// request is never stranded even if the non-blocking wakeCh send was lost.
func (cf *credForwarder) reclaimPendingOld() {
	po := cf.pendingOld.Load()
	if po == nil {
		return
	}
	cf.pendingOld.Store(nil)
	live := cf.queue.Load()
	for {
		select {
		case qr := <-*po:
			select {
			case *live <- qr:
			default:
				// Live channel unexpectedly full; route the request to failover
				// instead of stranding it.
				cf.pipe.releaseLaneAdmission(&qr.clusterCred)
				depth := cf.depth.Add(-1)
				metricCredQueueDepth.WithLabelValues(itoa(cf.cred.CredentialID), cf.cred.ConcurrencyMode).Dec()
				cf.pipe.observeQueue(QueueObservation{Kind: QueueCredentialDepth, CredentialID: cf.cred.CredentialID, Mode: cf.cred.ConcurrencyMode, Depth: depth, Delta: -1, AbsoluteDepth: true})
				cf.pipe.complete(qr, ForwardOutcome{Err: ErrShutdown})
			}
		default:
			return
		}
	}
}

// acquireGiveUp computes the governor pace deadline for a request already in
// this credential's Tier-2 queue.
//
//   - budget > 0: hard deadline = now + budget (RPM/TPM and concurrency).
//   - budget == 0 + concurrency mode: zero time.Time → "wait until ctx done"
//     so queued requests park for an in-flight slot instead of instantly
//     pace_timeout → failover (plan A / v4 wait-room semantics).
//   - budget == 0 + RPM/TPM: giveUp = now → immediate pace timeout if saturated
//     (keeps v4 zero-wait rate-bucket behaviour).
func (cf *credForwarder) acquireGiveUp(qr *QueuedRequest, gov Governor) time.Time {
	budget := cf.pipe.queueWaitBudget(qr)
	if budget > 0 {
		return time.Now().Add(budget)
	}
	if gov.Mode() == ModeConcurrency {
		return time.Time{}
	}
	return time.Now()
}

func (cf *credForwarder) acquire(qr *QueuedRequest) (Governor, bool) {
	// Keep this Governor for the entire admission/attempt lifetime. ApplyPolicy
	// may swap cf.gov after admission, but only this instance owns the acquired
	// capacity or Redis lease.
	gov := cf.govLocked()
	attempt := qr.reserveAttempt(cf.cred)
	giveUp := cf.acquireGiveUp(qr, gov)
	ctx, cancel := context.WithCancel(ctxOf(qr))
	defer cancel()
	go func() {
		select {
		case <-cf.ctx.Done():
			cancel()
		case <-ctx.Done():
		}
	}()

	if err := gov.Acquire(ctx, qr, giveUp); err != nil {
		qr.abandonReservedAttempt(attempt.AttemptID)
		depth := cf.depth.Add(-1)
		metricCredQueueDepth.WithLabelValues(itoa(cf.cred.CredentialID), cf.cred.ConcurrencyMode).Dec()
		cf.pipe.observeQueue(QueueObservation{Kind: QueueCredentialDepth, CredentialID: cf.cred.CredentialID, Mode: cf.cred.ConcurrencyMode, Depth: depth, Delta: -1, AbsoluteDepth: true})
		if ctxOf(qr).Err() != nil {
			cf.pipe.complete(qr, ForwardOutcome{Err: ctxOf(qr).Err()})
			return nil, false
		}
		if cf.ctx.Err() != nil {
			cf.pipe.complete(qr, ForwardOutcome{Err: ErrShutdown})
			return nil, false
		}
		if IsPaceTimeout(err) {
			// Mark credential as tried to prevent retry on same credential (P1 fix)
			qr.markTriedCredential(cf.cred.CredentialID)
			qr.CredRetryCount = maxRetryBudget
		}
		metricOverflow.WithLabelValues("pace_timeout").Inc()
		cf.pipe.observeOverflow("pace_timeout")
		cf.pipe.routeFailover(qr, ForwardOutcome{Err: err})
		return nil, false
	}

	// V3.1: Record T6 timestamp (credential queue dequeue, governor acquired)
	qr.SetT6_CredDequeued()
	attempt, committed := qr.commitReservedAttempt(attempt.AttemptID)
	if !committed {
		gov.Release(qr)
		depth := cf.depth.Add(-1)
		metricCredQueueDepth.WithLabelValues(itoa(cf.cred.CredentialID), cf.cred.ConcurrencyMode).Dec()
		cf.pipe.observeQueue(QueueObservation{Kind: QueueCredentialDepth, CredentialID: cf.cred.CredentialID, Mode: cf.cred.ConcurrencyMode, Depth: depth, Delta: -1, AbsoluteDepth: true})
		cf.pipe.complete(qr, ForwardOutcome{Err: errors.New("dispatch: missing reserved attempt")})
		return nil, false
	}

	// V3.3-OBS OBS-B1 (2026-08-15): node_selected 动作事件（S7 前，最终选定
	// 节点——通过 governor 准入，即将开始转发）。
	cf.pipe.observeQueue(QueueObservation{Kind: QueueGovernorDegraded, CredentialID: cf.cred.CredentialID, Degraded: gov.Mode() == ModeDisabled})
	cf.pipe.liveActions.Emit(ctxOf(qr), liveactions.ActionEvent{
		RequestID:    qr.ID,
		Action:       liveactions.ActionNodeSelected,
		Model:        qr.resolvedModel(),
		CredentialID: cf.cred.CredentialID,
		Detail: map[string]string{
			"attempt": itoa(attempt.AttemptNo),
		},
	})
	qr.emitObservation(Observation{
		Type:          ObservationNodeSelected,
		Stage:         StageNodeSelection,
		ResolvedModel: qr.resolvedModel(),
		Model:         attempt.Model,
		ProviderID:    attempt.ProviderID,
		Provider:      attempt.Provider,
		CredentialID:  attempt.CredentialID,
		Attempt:       copyAttemptRef(attempt),
	})

	if !qr.CredEnqueuedAt.IsZero() {
		metricCredQueueWait.WithLabelValues(itoa(cf.cred.CredentialID)).Observe(qr.DequeuedAt.Sub(qr.CredEnqueuedAt).Seconds())
	}
	return gov, true
}

// attempt forwards one request after governor admission.
func (cf *credForwarder) attempt(qr *QueuedRequest, gov Governor) {
	defer cf.wg.Done()
	mode := cf.cred.ConcurrencyMode
	attempt, startedAt, started := qr.startAllocatedAttempt()
	if !started {
		gov.Release(qr)
		cf.pipe.complete(qr, ForwardOutcome{Err: errors.New("dispatch: missing allocated attempt")})
		return
	}
	// V4 R1.1: registry pending → in-flight at the dequeue-for-execution
	// boundary (spec §6: upstream attempts are in-flight). Bookkeeping
	// bypass — never gates the forward.
	cf.pipe.registry.MarkInFlight(qr.ID, startedAt)
	qr.emitObservation(Observation{
		Type:          ObservationAttemptStarted,
		Stage:         StageUpstream,
		ResolvedModel: qr.resolvedModel(),
		Model:         attempt.Model,
		ProviderID:    attempt.ProviderID,
		Provider:      attempt.Provider,
		CredentialID:  attempt.CredentialID,
		Attempt:       copyAttemptRef(attempt),
		OccurredAt:    startedAt,
	})
	defer func() {
		if recovered := recover(); recovered != nil {
			slog.Error("dispatch forward panic recovered",
				"request_id", qr.ID,
				"credential_id", cf.cred.CredentialID,
				"panic", recovered)
			out := ForwardOutcome{Err: fmt.Errorf("forward panic: %v", recovered), ErrorKind: "forward_panic"}
			cf.pipe.emitAttemptFinished(qr, attempt.AttemptID, out)
			cf.pipe.complete(qr, out)
		}
	}()
	// Use the credential's CONFIGURED mode for all metric labels so labels are
	// consistent across queue-depth / dequeued / in-flight. Do NOT use
	// gov.Mode(): a concurrency credential with limit 0 degrades to a
	// noopGovernor whose Mode() returns "disabled", which would mismatch the
	// "concurrency" label used on the queue-depth gauge for the same credential.
	metricDequeued.WithLabelValues(itoa(cf.cred.CredentialID), mode).Inc()
	inFlight := cf.pipe.inFlight.Add(1)
	metricInFlight.WithLabelValues(itoa(cf.cred.CredentialID), mode).Inc()
	cf.pipe.observeQueue(QueueObservation{Kind: QueueInFlight, InFlight: inFlight, Delta: 1})
	var releaseOnce sync.Once
	releaseResources := func() {
		releaseOnce.Do(func() {
			gov.Release(qr)
			inFlight := cf.pipe.inFlight.Add(-1)
			metricInFlight.WithLabelValues(itoa(cf.cred.CredentialID), mode).Dec()
			cf.pipe.observeQueue(QueueObservation{Kind: QueueInFlight, InFlight: inFlight, Delta: -1})
		})
	}
	defer releaseResources()

	out := cf.pipe.forwardFunc(ctxOf(qr), qr, cf.cred)

	// P1 fix: Release capacity based on first-byte boundary
	// - Pre-first-byte failure → release immediately (allow fast retry)
	// - Post-first-byte or success → delay release until after complete
	if !out.BytesSent && out.Err != nil {
		releaseResources() // Early release for pre-first-byte failures
	}

	cf.pipe.emitAttemptFinished(qr, attempt.AttemptID, out)

	if out.Err == nil {
		metricForwarded.WithLabelValues(itoa(cf.cred.CredentialID), "success").Inc()
		releaseResources() // Publish in-flight zero before waking Submit.
		cf.pipe.complete(qr, out)
		return
	}
	if out.BytesSent {
		// Bytes already left the client: do NOT switch nodes (ADR-Disp-003).
		metricForwarded.WithLabelValues(itoa(cf.cred.CredentialID), "fail_postfirstbyte").Inc()
		releaseResources() // Publish in-flight zero before waking Submit.
		cf.pipe.complete(qr, out)
		return
	}
	// Pre-first-byte failure: capacity already released above
	metricForwarded.WithLabelValues(itoa(cf.cred.CredentialID), "fail_prefirstbyte").Inc()
	cf.pipe.routeFailover(qr, out)
}

func (p *Pipeline) emitAttemptFinished(qr *QueuedRequest, attemptID string, out ForwardOutcome) {
	attempt, outcome, errorKind, endedAt, ok := qr.finishAttempt(attemptID, out)
	if !ok {
		return
	}
	event := Observation{
		Type:          ObservationAttemptSucceeded,
		Stage:         StageUpstream,
		ResolvedModel: qr.resolvedModel(),
		Model:         attempt.Model,
		ProviderID:    attempt.ProviderID,
		Provider:      attempt.Provider,
		CredentialID:  attempt.CredentialID,
		Attempt:       copyAttemptRef(attempt),
		Outcome:       OutcomeSuccess,
		OccurredAt:    endedAt,
	}
	if out.Err != nil {
		event.Type = ObservationAttemptFailed
		event.Outcome = outcome
		event.ErrorKind = errorKind
		event.HTTPStatus = out.HTTPStatus
	}
	qr.emitObservation(event)
}
