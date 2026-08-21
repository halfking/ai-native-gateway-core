package dispatch

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/liveactions"
)

// runDispatcher is a ① Model Dispatcher worker. It consumes from dispatchIn,
// resolves the model, picks a credential (RouteFunc), and enqueues into the
// credential's Tier-2 queue. On exhaustion it triggers model-change.
func (p *Pipeline) runDispatcher() {
	defer p.wg.Done()
	for {
		select {
		case qr, ok := <-p.dispatchIn:
			if !ok {
				return
			}
			p.dispatch(qr)
		case <-p.stopCh:
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
	if qr.ResolvedModel == "" {
		resolved, alts, err := p.modelResolveFunc(qr.Ctx, qr.RequestedModel, nil)
		if err != nil || resolved == "" {
			slog.Debug("dispatch: model resolve failed",
				"requested", qr.RequestedModel, "request_id", qr.ID, "error", err)
			// V3.3-OBS OBS-B1 (2026-08-15): no_route 动作事件（终态）。
			p.emitNoRoute(qr, qr.RequestedModel, "model_resolve_failed")
			p.complete(qr, ForwardOutcome{Err: ErrNoRoute})
			return
		}
		qr.ResolvedModel = resolved
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
	for _, ref := range refs {
		if qr.hasTriedCredential(ref.CredentialID) {
			continue
		}
		if !p.providerSwitchAllowed(qr, ref.ProviderID) {
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
		outcome.Err = p.exhaustedTerminal(qr, terminalErr(cause))
		p.complete(qr, outcome)
	}
	if !p.modelChangeEnabled() || !qr.AllowModelChange {
		p.emitNoRouteIfCause(qr, cause)
		completeCause()
		return
	}
	qr.markTriedModel(qr.ResolvedModel)
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
		p.emitNoRouteIfCause(qr, cause)
		completeCause()
		return
	}
	if len(alts) == 0 {
		p.emitNoRouteIfCause(qr, cause)
		completeCause()
		return
	}
	// Take the first alternative not already tried.
	chosen := ""
	for _, a := range alts {
		if _, tried := qr.TriedModels[a]; !tried {
			chosen = a
			break
		}
	}
	if chosen == "" {
		p.emitNoRouteIfCause(qr, cause)
		completeCause()
		return
	}
	// An alternative model remains: continuation, so the attempt budget
	// guards it. (Exhaustion terminals above already returned by now —
	// this is the R2.4 priority: 组合穷尽 > 预算. )
	if qr.AttemptCount >= maxAttempts {
		p.terminateOnAttemptCap(qr, outcome)
		return
	}
	// V3.3-OBS OBS-B1 (2026-08-15): model_switch 动作事件（24 号 §2：
	// from/to/reason；不产生新 request_id）。
	fromModel := qr.ResolvedModel
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
	qr.ResolvedModel = chosen
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

func (p *Pipeline) selectCredential(qr *QueuedRequest, ref CredentialRef) {
	qr.SelectedCred = ref
	qr.vendor = ref.Vendor
	if qr.InitialProviderID == 0 {
		qr.InitialProviderID = ref.ProviderID
	}
	qr.emitObservation(Observation{
		Type:          ObservationCredentialSelected,
		Stage:         StageNodeSelection,
		ResolvedModel: qr.ResolvedModel,
		Model:         qr.ResolvedModel,
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
		p.emitNoRoute(qr, qr.ResolvedModel, "all_credentials_exhausted")
	}
}

// scheduleCapacityRetry schedules a request for capacity retry after 5 seconds.
// Called when all credentials under the current model have full queues.
func (p *Pipeline) scheduleCapacityRetry(qr *QueuedRequest) {
	const capacityRetryDelay = 5 * time.Second
	const maxCapacityRetries = 12 // 5s × 12 = 60s max wait

	// Check retry limit to prevent infinite loops
	if qr.CapacityRetryCount >= maxCapacityRetries {
		// Exceeded max capacity wait time → escalate to model-change
		p.tryModelChange(qr, errCapacitySaturated)
		return
	}

	qr.CapacityRetryCount++
	retryAt := time.Now().Add(capacityRetryDelay)

	// Use existing HeapRetryScheduler infrastructure
	if p.retryScheduler != nil && ctxOf(qr).Err() == nil {
		observation := Observation{
			Type:          ObservationRetryScheduled,
			Stage:         StageRetrying,
			ResolvedModel: qr.ResolvedModel,
			Model:         qr.ResolvedModel,
			RetryReason:   "capacity_saturated",
		}
		at := retryAt
		observation.RetryAt = &at
		qr.emitObservation(observation)

		p.registry.MarkRetryScheduled(qr.ID, retryAt)
		p.queueMirror.MirrorRetryAt(qr.ID, retryAt)

		if p.retryScheduler.Schedule(qr, retryAt) {
			return
		}
	}

	// Scheduling failed → escalate to model-change (avoid infinite loop)
	p.tryModelChange(qr, errCapacitySaturated)
}
