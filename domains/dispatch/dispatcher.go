package dispatch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/liveactions"
)

// runDispatcher is a ① Model Dispatcher worker. It consumes from dispatchIn,
// resolves the model, picks a credential (RouteFunc), and enqueues into the
// credential's Tier-2 queue. On exhaustion it triggers model-change.
//
// Shutdown (audit 2026-09-05 C-#2): every request still buffered in dispatchIn
// when stopCh fires is completed with ErrShutdown instead of being orphaned —
// a dropped qr means its Submit caller blocks on qr.ResultCh until ctx expiry
// (2h for survival streams). Mirrors runModelDrainer's stopCh handling and
// runTotalDrainer's stopCh drain. See TestStopDrainsDispatchInResidue.
func (p *Pipeline) runDispatcher() {
	defer p.wg.Done()
	for {
		select {
		case qr, ok := <-p.dispatchIn:
			if !ok {
				return
			}
			if p.shutdown.Load() {
				// stopCh is already closed and the select randomly picked the
				// receive branch: this is buffered residue. Complete it
				// directly — do NOT run executor callbacks (model resolve /
				// route) on a stopped pipeline.
				p.complete(qr, ForwardOutcome{Err: ErrShutdown})
				continue
			}
			p.dispatch(qr)
		case <-p.stopCh:
			p.shutdownDrainDispatchIn()
			return
		}
	}
}

// shutdownDrainDispatchIn completes every request still buffered in
// dispatchIn with ErrShutdown (audit 2026-09-05 C-#2).
//
// Ordering guarantee (why the drain is lossless): the sole producers of
// dispatchIn are the per-model runModelDrainer goroutines. Waiting for them
// FIRST — after a modelMu barrier that makes the producer set final
// (getOrCreateModelQueue spawns drainers only under modelMu with a shutdown
// guard, so after the barrier no new drainer can join) — guarantees nothing
// is sent into dispatchIn after the drain loop observes it empty. Without
// this wait, a drainer whose inner select randomly picked the send branch
// (both branches ready once stopCh closed) could land a qr AFTER the drain
// exited, with no consumer left.
func (p *Pipeline) shutdownDrainDispatchIn() {
	p.modelMu.Lock()
	p.modelMu.Unlock() // barrier: producer set is now final (no new drainers)
	p.drainerWg.Wait() // every runModelDrainer has exited
	for {
		select {
		case qr := <-p.dispatchIn:
			p.complete(qr, ForwardOutcome{Err: ErrShutdown})
		default:
			return
		}
	}
}

// dispatch handles one request through model-resolution + credential selection.
// Bounded retry on "credential queue full" to avoid spinning.
func (p *Pipeline) dispatch(qr *QueuedRequest) {
	// V3.1: Record T4 timestamp (model queue dequeued - start model resolution)
	// Note: In current architecture, dispatchIn acts as the model queue.
	// T3 (model enqueue) is set in runModelDrainer when feeding dispatchIn.
	qr.SetT4_ModelDequeued()

	// Resolve model (auto / empty → concrete).
	if qr.resolvedModel() == "" {
		resolved, alts, err := p.modelResolveFunc(qr.Ctx, qr.RequestedModel, nil)
		if err != nil || resolved == "" {
			slog.Debug("dispatch: model resolve failed",
				"requested", qr.RequestedModel, "request_id", qr.ID, "error", err)
			// V3.3-OBS OBS-B1 (2026-08-15): no_route 动作事件（终态）。
			p.emitNoRoute(qr, qr.RequestedModel, "model_resolve_failed")
			p.complete(qr, ForwardOutcome{Err: ErrNoRoute})
			return
		}
		qr.setResolvedModel(resolved)
		if len(qr.ModelAlternatives) == 0 && len(alts) > 0 {
			qr.ModelAlternatives = append([]string(nil), alts...)
		}
	}

	// Pick an available credential (routeFunc excludes tried creds).
	refs, hasCredentials := p.selectAndEnqueue(qr)
	if !hasCredentials {
		// No routable credential under this model → model-change or reject.
		p.tryModelChange(qr, ErrNoRoute)
	} else if len(refs) > 0 {
		// Has credentials but all queues full → capacity wait
		p.scheduleCapacityRetry(qr)
	}
	// else: successfully enqueued (refs == nil && hasCredentials == true)
}

// selectAndEnqueue routes, picks the first non-tried credential whose Tier-2
// queue can accept the request, and enqueues it. Returns (refs, hasCredentials):
// - (nil, true): successfully enqueued
// - (refs, true): has credentials but all queues full
// - (nil/refs, false): no routable credentials (error or empty)
func (p *Pipeline) selectAndEnqueue(qr *QueuedRequest) ([]CredentialRef, bool) {
	refs, err := p.routeFunc(qr.Ctx, qr)
	if err != nil || len(refs) == 0 {
		return refs, false
	}
	refs = sortPriorityClusters(refs)
	for _, ref := range refs {
		if qr.hasTriedCredential(ref.CredentialID) {
			continue
		}
		if !providerSwitchAllowed(qr, ref.ProviderID) {
			qr.markTriedCredential(ref.CredentialID)
			continue
		}
		if forwarder := p.getForwarderIfExists(ref.CredentialID); forwarder != nil && !forwarder.HasCapacity() {
			metricCredentialFull.WithLabelValues(itoa(ref.CredentialID), ref.ConcurrencyMode).Inc()
			p.observeQueue(QueueObservation{
				Kind:         QueueCredentialFull,
				CredentialID: ref.CredentialID,
				Mode:         ref.ConcurrencyMode,
				Depth:        forwarder.CurrentDepth(),
				Limit:        forwarder.Limit(),
			})
			// Queue full is temporary, don't mark as tried - continue polling
			continue
		}

		p.selectCredential(qr, ref)
		// Clear the capacity flag BEFORE the cred-queue hand-off (the write
		// must happen while this goroutine still owns qr — after the hand-off
		// the failover chain can re-dispatch on another worker and race a
		// late write). A stale CapacityRetryCount makes onRetryDue treat
		// every later same-credential error retry as a capacity re-dispatch
		// (full re-route) instead of re-enqueueing the selected credential.
		// If the enqueue below still fails, scheduleCapacityRetry re-counts
		// from zero — benign: HasCapacity was just checked.
		qr.CapacityRetryCount = 0
		if p.tryEnqueueCred(ref, qr) {
			return nil, true // Successfully enqueued
		}
		// Queue full after selection → don't mark tried, continue polling
	}
	// All candidate queues full, but credentials are not marked as tried
	return refs, true // Has credentials but all queues full
}

// tryModelChange is the Tier-1 escape hatch: when all credentials under the
// current model are exhausted, switch to an alternative model. If model-change
// is disabled or no alternative exists, complete with the original cause.
func (p *Pipeline) tryModelChange(qr *QueuedRequest, cause error) {
	p.tryModelChangeOutcome(qr, ForwardOutcome{Err: cause})
}

func (p *Pipeline) tryModelChangeOutcome(qr *QueuedRequest, outcome ForwardOutcome) {
	cause := outcome.Err
	// completeCause terminates the request. When the request actually made
	// attempts, the cause is wrapped in the aggregate ExhaustedError (R2.4 /
	// UT-FO-05): combination exhaustion fires here — BEFORE the attempt
	// budget can matter — with the tried model/node/reason summary attached.
	completeCause := func() {
		outcome.Err = exhaustedTerminal(qr, terminalErr(cause))
		p.complete(qr, outcome)
	}
	if d := PlanNoRoute(qr, p.modelChangeEnabled()); d.Action == NextActionFailed {
		p.emitNoRouteIfCause(qr, cause)
		completeCause()
		return
	}
	qr.markTriedModel(qr.resolvedModel())
	d := PlanModelChange(qr, p.modelChangeCandidates(qr))
	if d.Action != NextActionSwitchModel {
		p.emitNoRouteIfCause(qr, cause)
		completeCause()
		return
	}
	chosen := d.NextModel
	// An alternative model remains: continuation, so the attempt budget
	// guards it. (Exhaustion terminals above already returned by now —
	// this is the R2.4 priority: 组合穷尽 > 预算. )
	if AttemptBudgetLeft(qr) <= 0 {
		p.terminateOnAttemptCap(qr, outcome)
		return
	}
	// V3.3-OBS OBS-B1 (2026-08-15): model_switch 动作事件（24 号 §2：
	// from/to/reason；不产生新 request_id）。
	fromModel := qr.resolvedModel()
	p.liveActions.Emit(ctxOf(qr), liveactions.ActionEvent{
		RequestID: qr.ID,
		Action:    liveactions.ActionModelSwitch,
		Model:     chosen,
		Detail: map[string]string{
			"from_model": fromModel,
			"to_model":   chosen,
			"reason":     "no_node",
		},
	})
	qr.emitObservation(Observation{
		Type:          ObservationModelSwitched,
		Stage:         StageRetrying,
		ResolvedModel: fromModel,
		FromModel:     fromModel,
		ToModel:       chosen,
		SwitchReason:  "no_node",
	})
	// v6 G-Ⅴ (回队打标) + G-Ⅲ (换模型 think 通知)：请求带着上一轮的失败
	// 标志换道重进 Tier-1，客户端在同一连接上看到模型切换进度。
	// W1.6 R9: journal 继承上一条的 ErrorKind/HTTPStatus/Vendor ——
	// entry 构造先于 recordDecision（后者会重写 LastFailover 投影）。
	// 2026-09-01 P0 fix: populate FromModel/ToModel so the journal→journey
	// bridge can emit valid EventModelSwitched events.
	now := time.Now()
	qr.recordDecision(JournalEntry{
		Model:      fromModel,
		Vendor:     qr.LastFailover.Vendor,
		Action:     NextActionSwitchModel,
		ErrorKind:  qr.LastFailover.ErrorKind,
		HTTPStatus: qr.LastFailover.HTTPStatus,
		Attempt:    qr.AttemptCount,
		At:         now,
		FromModel:  fromModel,
		ToModel:    chosen,
	})
	qr.notifyDispatch(DispatchNotice{
		Kind:      NoticeKindModelSwitch,
		Message:   fmt.Sprintf("模型 %s 无可用节点，切换到 %s 继续执行…", fromModel, chosen),
		FromModel: fromModel,
		ToModel:   chosen,
		Attempt:   qr.AttemptCount,
	})
	qr.setResolvedModel(chosen)
	qr.TriedCredentials = make(map[int]struct{})
	qr.CredRetryCount = 0
	qr.InitialProviderID = 0
	metricFailover.WithLabelValues("model_change").Inc()
	slog.Info("dispatch: model change",
		"request_id", qr.ID, "from", qr.RequestedModel, "to", chosen,
		"tried_models", len(qr.TriedModels))
	// Re-enter Tier-1 for the new model (re-routes its credentials).
	key := queueKeyFor(chosen)
	if !p.enqueueModel(key, qr) {
		if p.shutdown.Load() {
			p.complete(qr, ForwardOutcome{Err: ErrShutdown})
			return
		}
		metricOverflow.WithLabelValues("model_queue_full").Inc()
		p.observeOverflow("model_queue_full")
		// R1.3: immediate, retryable overflow with a Retry-After hint.
		p.complete(qr, ForwardOutcome{Err: &OverflowError{Reason: "model_queue_full", RetryAfter: DefaultOverflowRetryAfter}})
	}
}

// modelChangeCandidates fetches the alternative models for the ladder — the
// only I/O step of the model-change decision, kept executor-side so the
// planner stays pure. nil on failure: the planner then decides the
// exhaustion terminal.
func (p *Pipeline) modelChangeCandidates(qr *QueuedRequest) []string {
	var (
		alts []string
		err  error
	)
	if p.modelRecommendFunc != nil {
		alts, err = p.modelRecommendFunc(qr.Ctx, qr, triedList(qr.TriedModels))
	} else {
		alts = qr.ModelAlternatives
		if len(alts) == 0 {
			_, alts, err = p.modelResolveFunc(qr.Ctx, qr.RequestedModel, triedList(qr.TriedModels))
		}
	}
	if err != nil {
		return nil
	}
	return alts
}

func (p *Pipeline) selectCredential(qr *QueuedRequest, ref CredentialRef) {
	qr.setSelectedCredential(ref)
	qr.setVendor(ref.Vendor)
	if qr.InitialProviderID == 0 {
		qr.InitialProviderID = ref.ProviderID
	}
	resolvedModel := qr.resolvedModel()
	qr.emitObservation(Observation{
		Type:          ObservationCredentialSelected,
		Stage:         StageNodeSelection,
		ResolvedModel: resolvedModel,
		Model:         resolvedModel,
		ProviderID:    int64(ref.ProviderID),
		Provider:      ref.Vendor,
		CredentialID:  int64(ref.CredentialID),
	})
}

// abandoned-aware ctx helper: returns the request ctx or background.
func ctxOf(qr *QueuedRequest) context.Context {
	if qr.Ctx != nil {
		return qr.Ctx
	}
	return context.Background()
}

// emitNoRoute emits the no_route terminal action event (V3.3-OBS OBS-B1).
func (p *Pipeline) emitNoRoute(qr *QueuedRequest, model, reason string) {
	if p == nil || qr == nil {
		return
	}
	p.liveActions.Emit(ctxOf(qr), liveactions.ActionEvent{
		RequestID: qr.ID,
		Action:    liveactions.ActionNoRoute,
		Model:     model,
		Detail: map[string]string{
			"blocked_reason": reason,
		},
	})
}

// emitNoRouteIfCause emits no_route only when the terminal cause is a
// no-route condition (模型换完仍无可用路由 → rejected(503))。
func (p *Pipeline) emitNoRouteIfCause(qr *QueuedRequest, cause error) {
	if p == nil || qr == nil || cause == nil {
		return
	}
	if cause == ErrNoRoute || errors.Is(cause, ErrNoRoute) {
		p.emitNoRoute(qr, qr.resolvedModel(), "all_credentials_exhausted")
	}
}

// scheduleCapacityRetry schedules a request for capacity retry after the
// planner pacing (capacityRetryDelay, capacityRetryDelay × maxCapacityRetries
// total). Called when all credentials under the current model have full queues.
func (p *Pipeline) scheduleCapacityRetry(qr *QueuedRequest) {
	now := time.Now()
	d := PlanCapacityWait(qr, now)
	if d.Action != NextActionCapacityWait {
		// Exceeded max capacity wait time → escalate to model-change
		p.tryModelChange(qr, errCapacitySaturated)
		return
	}

	qr.CapacityRetryCount++
	retryAt := d.RetryAt

	// Use existing HeapRetryScheduler infrastructure
	if p.retryScheduler != nil && ctxOf(qr).Err() == nil {
		observation := Observation{
			Type:          ObservationRetryScheduled,
			Stage:         StageRetrying,
			ResolvedModel: qr.resolvedModel(),
			Model:         qr.resolvedModel(),
			RetryReason:   "capacity_saturated",
		}
		at := retryAt
		observation.RetryAt = &at
		qr.emitObservation(observation)

		p.registry.MarkRetryScheduled(qr.ID, retryAt)
		p.queueMirror.MirrorRetryAt(qr.ID, retryAt)

		if p.retryScheduler.Schedule(qr, retryAt) {
			// v6 G-Ⅲ/G-Ⅴ: 供应商并发/限流到达时的等待通知（think 通道，
			// 不影响会话）+ 回队打标。凭据不标记 tried（队列满是暂态）。
			now := time.Now()
			qr.recordDecision(JournalEntry{
				Model:   qr.resolvedModel(),
				Action:  NextActionCapacityWait,
				Attempt: qr.AttemptCount,
				At:      now,
			})
			p.dimensionIndex.UpdateWait(qr, retryAt, NextActionCapacityWait, now)
			qr.notifyDispatch(DispatchNotice{
				Kind:     NoticeKindQueued,
				Message:  fmt.Sprintf("所有节点并发/限流已满，排队等待中（第 %d/%d 轮，%s 后重试）…", qr.CapacityRetryCount, maxCapacityRetries, waitHint(capacityRetryDelay)),
				RetryAt:  retryAt,
				WaitHint: waitHint(capacityRetryDelay),
				ToModel:  qr.resolvedModel(),
				Attempt:  qr.AttemptCount,
			})
			return
		}
	}

	// Scheduling failed → escalate to model-change (avoid infinite loop)
	p.tryModelChange(qr, errCapacitySaturated)
}
