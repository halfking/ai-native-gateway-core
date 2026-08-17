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
type credForwarder struct {
	cred   CredentialRef
	queue  chan *QueuedRequest
	depth  atomic.Int64
	limit  int64
	gov    Governor
	pipe   *Pipeline
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func newCredForwarder(cred CredentialRef, queueDepth int, pipe *Pipeline) *credForwarder {
	ctx, cancel := context.WithCancel(context.Background())
	cf := &credForwarder{
		cred:   cred,
		queue:  make(chan *QueuedRequest, queueDepth),
		limit:  int64(queueDepth),
		gov:    newGovernor(cred),
		pipe:   pipe,
		ctx:    ctx,
		cancel: cancel,
	}
	// Track the loop goroutine in the pipeline-wide WaitGroup so Stop() waits
	// for in-flight forwards to finish (concurrency audit 2026-08-13 D3).
	// Safe against Stop's wg.Wait: newCredForwarder is only reached via
	// getOrCreateForwarder under p.credMu with a shutdown guard, so the Add
	// happens-before Stop's credMu section and therefore before wg.Wait.
	pipe.wg.Add(1)
	go cf.loop()
	return cf
}

func (cf *credForwarder) tryReserve() bool {
	for {
		cur := cf.depth.Load()
		if cf.limit > 0 && cur >= cf.limit {
			return false
		}
		if cf.depth.CompareAndSwap(cur, cur+1) {
			return true
		}
	}
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
		select {
		case qr, ok := <-cf.queue:
			if !ok {
				return
			}
			if !cf.acquire(qr) {
				continue
			}
			depth := cf.depth.Add(-1)
			metricCredQueueDepth.WithLabelValues(itoa(cf.cred.CredentialID), cf.cred.ConcurrencyMode).Dec()
			cf.pipe.observeQueue(QueueObservation{Kind: QueueCredentialDepth, CredentialID: cf.cred.CredentialID, Mode: cf.cred.ConcurrencyMode, Depth: depth, Delta: -1})
			cf.wg.Add(1)
			go cf.attempt(qr)
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
	for {
		select {
		case qr, ok := <-cf.queue:
			if !ok {
				return
			}
			depth := cf.depth.Add(-1)
			metricCredQueueDepth.WithLabelValues(itoa(cf.cred.CredentialID), cf.cred.ConcurrencyMode).Dec()
			cf.pipe.observeQueue(QueueObservation{Kind: QueueCredentialDepth, CredentialID: cf.cred.CredentialID, Mode: cf.cred.ConcurrencyMode, Depth: depth, Delta: -1})
			cf.pipe.complete(qr, ForwardOutcome{Err: ErrShutdown})
		default:
			return
		}
	}
}

func (cf *credForwarder) acquire(qr *QueuedRequest) bool {
	attempt := qr.reserveAttempt(cf.cred)
	giveUp := time.Now().Add(cf.pipe.queueWaitBudget(qr))
	ctx, cancel := context.WithCancel(ctxOf(qr))
	defer cancel()
	go func() {
		select {
		case <-cf.ctx.Done():
			cancel()
		case <-ctx.Done():
		}
	}()

	if err := cf.gov.Acquire(ctx, qr, giveUp); err != nil {
		qr.abandonReservedAttempt(attempt.AttemptID)
		depth := cf.depth.Add(-1)
		metricCredQueueDepth.WithLabelValues(itoa(cf.cred.CredentialID), cf.cred.ConcurrencyMode).Dec()
		cf.pipe.observeQueue(QueueObservation{Kind: QueueCredentialDepth, CredentialID: cf.cred.CredentialID, Mode: cf.cred.ConcurrencyMode, Depth: depth, Delta: -1})
		if ctxOf(qr).Err() != nil {
			cf.pipe.complete(qr, ForwardOutcome{Err: ctxOf(qr).Err()})
			return false
		}
		if cf.ctx.Err() != nil {
			cf.pipe.complete(qr, ForwardOutcome{Err: ErrShutdown})
			return false
		}
		if IsPaceTimeout(err) {
			qr.CredRetryCount = maxRetryBudget
		}
		metricOverflow.WithLabelValues("pace_timeout").Inc()
		cf.pipe.observeOverflow("pace_timeout")
		cf.pipe.routeFailover(qr, ForwardOutcome{Err: err})
		return false
	}

	// V3.1: Record T6 timestamp (credential queue dequeue, governor acquired)
	qr.SetT6_CredDequeued()
	attempt, committed := qr.commitReservedAttempt(attempt.AttemptID)
	if !committed {
		cf.gov.Release(qr)
		depth := cf.depth.Add(-1)
		metricCredQueueDepth.WithLabelValues(itoa(cf.cred.CredentialID), cf.cred.ConcurrencyMode).Dec()
		cf.pipe.observeQueue(QueueObservation{Kind: QueueCredentialDepth, CredentialID: cf.cred.CredentialID, Mode: cf.cred.ConcurrencyMode, Depth: depth, Delta: -1})
		cf.pipe.complete(qr, ForwardOutcome{Err: errors.New("dispatch: missing reserved attempt")})
		return false
	}

	// V3.3-OBS OBS-B1 (2026-08-15): node_selected 动作事件（S7 前，最终选定
	// 节点——通过 governor 准入，即将开始转发）。
	cf.pipe.observeQueue(QueueObservation{Kind: QueueGovernorDegraded, CredentialID: cf.cred.CredentialID, Degraded: cf.gov.Mode() == ModeDisabled})
	cf.pipe.liveActions.Emit(ctxOf(qr), liveactions.ActionEvent{
		RequestID:    qr.ID,
		Action:       liveactions.ActionNodeSelected,
		Model:        qr.ResolvedModel,
		CredentialID: cf.cred.CredentialID,
		Detail: map[string]string{
			"attempt": itoa(attempt.AttemptNo),
		},
	})
	qr.emitObservation(Observation{
		Type:          ObservationNodeSelected,
		Stage:         StageNodeSelection,
		ResolvedModel: qr.ResolvedModel,
		Model:         attempt.Model,
		ProviderID:    attempt.ProviderID,
		Provider:      attempt.Provider,
		CredentialID:  attempt.CredentialID,
		Attempt:       copyAttemptRef(attempt),
	})

	if !qr.CredEnqueuedAt.IsZero() {
		metricCredQueueWait.WithLabelValues(itoa(cf.cred.CredentialID)).Observe(qr.DequeuedAt.Sub(qr.CredEnqueuedAt).Seconds())
	}
	return true
}

// attempt forwards one request after governor admission.
func (cf *credForwarder) attempt(qr *QueuedRequest) {
	defer cf.wg.Done()
	mode := cf.cred.ConcurrencyMode
	attempt, startedAt, started := qr.startAllocatedAttempt()
	if !started {
		cf.pipe.complete(qr, ForwardOutcome{Err: errors.New("dispatch: missing allocated attempt")})
		return
	}
	qr.emitObservation(Observation{
		Type:          ObservationAttemptStarted,
		Stage:         StageUpstream,
		ResolvedModel: qr.ResolvedModel,
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
	// cf.gov.Mode(): a concurrency credential with limit 0 degrades to a
	// noopGovernor whose Mode() returns "disabled", which would mismatch the
	// "concurrency" label used on the queue-depth gauge for the same credential.
	metricDequeued.WithLabelValues(itoa(cf.cred.CredentialID), mode).Inc()
	inFlight := cf.pipe.inFlight.Add(1)
	metricInFlight.WithLabelValues(itoa(cf.cred.CredentialID), mode).Inc()
	cf.pipe.observeQueue(QueueObservation{Kind: QueueInFlight, InFlight: inFlight, Delta: 1})
	var releaseOnce sync.Once
	releaseResources := func() {
		releaseOnce.Do(func() {
			cf.gov.Release(qr)
			inFlight := cf.pipe.inFlight.Add(-1)
			metricInFlight.WithLabelValues(itoa(cf.cred.CredentialID), mode).Dec()
			cf.pipe.observeQueue(QueueObservation{Kind: QueueInFlight, InFlight: inFlight, Delta: -1})
		})
	}
	defer releaseResources()

	out := cf.pipe.forwardFunc(ctxOf(qr), qr, cf.cred)
	releaseResources()
	cf.pipe.emitAttemptFinished(qr, attempt.AttemptID, out)

	if out.Err == nil {
		metricForwarded.WithLabelValues(itoa(cf.cred.CredentialID), "success").Inc()
		cf.pipe.complete(qr, out)
		return
	}
	if out.BytesSent {
		// Bytes already left the client: do NOT switch nodes (ADR-Disp-003).
		metricForwarded.WithLabelValues(itoa(cf.cred.CredentialID), "fail_postfirstbyte").Inc()
		cf.pipe.complete(qr, out)
		return
	}
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
		ResolvedModel: qr.ResolvedModel,
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
