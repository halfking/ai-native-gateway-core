package dispatch

import (
	"context"
	"sync/atomic"
	"time"
)

// credForwarder is the ② Credential Forwarder for one credential. It owns the
// Tier-2 FIFO queue and the per-credential governor that paces forwarding.
type credForwarder struct {
	cred   CredentialRef
	queue  chan *QueuedRequest
	depth  atomic.Int64
	gov    Governor
	pipe   *Pipeline
	ctx    context.Context
	cancel context.CancelFunc
}

func newCredForwarder(cred CredentialRef, queueDepth int, pipe *Pipeline) *credForwarder {
	ctx, cancel := context.WithCancel(context.Background())
	cf := &credForwarder{
		cred:   cred,
		queue:  make(chan *QueuedRequest, queueDepth),
		gov:    newGovernor(cred),
		pipe:   pipe,
		ctx:    ctx,
		cancel: cancel,
	}
	go cf.loop()
	return cf
}

// loop drains the Tier-2 queue. Each drained request is paced by the governor
// (which flattens peaks and never exceeds the concurrency/rate limit) in a
// transient goroutine, so a blocked governor does not stall other items in the
// queue.
func (cf *credForwarder) loop() {
	for {
		select {
		case qr, ok := <-cf.queue:
			if !ok {
				return
			}
			cf.depth.Add(-1)
			metricCredQueueDepth.WithLabelValues(itoa(cf.cred.CredentialID), cf.cred.ConcurrencyMode).Dec()
			go cf.attempt(qr)
		case <-cf.ctx.Done():
			return
		}
	}
}

// attempt paces then forwards one request.
func (cf *credForwarder) attempt(qr *QueuedRequest) {
	qr.AttemptCount++
	giveUp := time.Now().Add(cf.pipe.queueWaitBudget(qr))

	if err := cf.gov.Acquire(ctxOf(qr), qr, giveUp); err != nil {
		// Acquire failed (pacing timeout or ctx cancel): no slot is held, so
		// routeFailover is safe to call even if it blocks on a full channel.
		if ctxOf(qr).Err() != nil {
			// Client gave up; nothing to do (Submit already returned).
			cf.pipe.complete(qr, ForwardOutcome{Err: ctxOf(qr).Err()})
			return
		}
		// Pacing timeout means this credential's concurrency/rate budget is
		// saturated — an immediate same-credential retry would almost surely
		// time out again. Skip the retry budget so the mover switches to a
		// different credential right away.
		if IsPaceTimeout(err) {
			qr.CredRetryCount = maxRetryBudget
		}
		metricOverflow.WithLabelValues("pace_timeout").Inc()
		cf.pipe.routeFailover(qr, err)
		return
	}

	mode := cf.gov.Mode()
	metricDequeued.WithLabelValues(itoa(cf.cred.CredentialID), mode).Inc()
	metricInFlight.WithLabelValues(itoa(cf.cred.CredentialID), mode).Inc()

	out := cf.pipe.forwardFunc(ctxOf(qr), qr, cf.cred)

	// Release the governor slot + in-flight gauge BEFORE branching. A
	// blocking routeFailover (full failoverCh under a failure burst) must
	// NOT hold a concurrency slot — otherwise the credential's effective
	// concurrency collapses exactly when failures spike (concurrency mode:
	// DeepSeek/智谱/Kimi). Rate modes (rpm/tpm) Release is a no-op.
	cf.gov.Release(qr)
	metricInFlight.WithLabelValues(itoa(cf.cred.CredentialID), mode).Dec()

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
	cf.pipe.routeFailover(qr, out.Err)
}
