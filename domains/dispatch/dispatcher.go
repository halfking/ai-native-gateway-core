package dispatch

import (
	"context"
	"log/slog"
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
	// Resolve model (auto / empty → concrete).
	if qr.ResolvedModel == "" {
		resolved, _, err := p.modelResolveFunc(qr.Ctx, qr.RequestedModel, nil)
		if err != nil || resolved == "" {
			slog.Debug("dispatch: model resolve failed",
				"requested", qr.RequestedModel, "request_id", qr.ID, "error", err)
			p.complete(qr, ForwardOutcome{Err: ErrNoRoute})
			return
		}
		qr.ResolvedModel = resolved
	}

	// Pick an available credential (routeFunc excludes tried creds).
	if !p.selectAndEnqueue(qr) {
		// No routable credential under this model → model-change or reject.
		p.tryModelChange(qr, ErrNoRoute)
	}
}

// selectAndEnqueue routes, picks the first non-tried credential whose Tier-2
// queue can accept the request, and enqueues it. Returns false if no
// credential is available or all queues are full.
func (p *Pipeline) selectAndEnqueue(qr *QueuedRequest) bool {
	refs, err := p.routeFunc(qr.Ctx, qr)
	if err != nil || len(refs) == 0 {
		return false
	}
	for _, ref := range refs {
		if qr.hasTriedCredential(ref.CredentialID) {
			continue
		}
		// Set selected cred BEFORE enqueue so the forwarder knows the governor.
		qr.SelectedCred = ref
		qr.vendor = ref.Vendor
		if p.tryEnqueueCred(ref, qr) {
			return true
		}
		// Queue full → mark tried and try the next credential.
		qr.markTriedCredential(ref.CredentialID)
	}
	// All candidate queues full. Leave them as tried so the mover escalates.
	return false
}

// tryModelChange is the Tier-1 escape hatch: when all credentials under the
// current model are exhausted, switch to an alternative model. If model-change
// is disabled or no alternative exists, complete with ErrNoRoute.
func (p *Pipeline) tryModelChange(qr *QueuedRequest, cause error) {
	if !p.allowModelChange {
		// No route available and model-change disabled: the request has
		// exhausted every credential under its model.
		p.complete(qr, ForwardOutcome{Err: ErrNoRoute})
		return
	}
	qr.markTriedModel(qr.ResolvedModel)
	_, alts, err := p.modelResolveFunc(qr.Ctx, qr.RequestedModel, triedList(qr.TriedModels))
	if err != nil || len(alts) == 0 {
		p.complete(qr, ForwardOutcome{Err: ErrNoRoute})
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
		p.complete(qr, ForwardOutcome{Err: ErrNoRoute})
		return
	}
	qr.ResolvedModel = chosen
	metricFailover.WithLabelValues("model_switch").Inc()
	slog.Info("dispatch: model change",
		"request_id", qr.ID, "from", qr.RequestedModel, "to", chosen,
		"tried_models", len(qr.TriedModels), "tried_creds", len(qr.TriedCredentials))
	// Re-enter Tier-1 for the new model (re-routes its credentials).
	key := queueKeyFor(chosen)
	if !p.enqueueModel(key, qr) {
		metricOverflow.WithLabelValues("model_queue_full").Inc()
		p.complete(qr, ForwardOutcome{Err: ErrOverflow})
	}
}

// abandoned-aware ctx helper: returns the request ctx or background.
func ctxOf(qr *QueuedRequest) context.Context {
	if qr.Ctx != nil {
		return qr.Ctx
	}
	return context.Background()
}
