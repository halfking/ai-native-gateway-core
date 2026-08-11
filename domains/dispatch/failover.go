package dispatch

import (
	"log/slog"
)

// failoverItem pairs a request with the pre-firstbyte error that caused the
// forwarder to hand it to the ③ mover.
type failoverItem struct {
	qr  *QueuedRequest
	err error
}

// runFailover is a ③ Failover Mover worker.
func (p *Pipeline) runFailover() {
	defer p.wg.Done()
	for {
		select {
		case it, ok := <-p.failoverCh:
			if !ok {
				return
			}
			p.move(it.qr, it.err)
		case <-p.stopCh:
			return
		}
	}
}

// move implements the failover ladder:
//  1. same-credential retry while under RetryPerCredential budget;
//  2. switch to another available credential under the same model;
//  3. model-change (tryModelChange);
//  4. terminal → complete with the error.
func (p *Pipeline) move(qr *QueuedRequest, err error) {
	// (1) Same-credential retry.
	if qr.CredRetryCount < p.config().RetryPerCredential {
		qr.CredRetryCount++
		metricFailover.WithLabelValues("cred_retry").Inc()
		if p.tryEnqueueCred(qr.SelectedCred, qr) {
			return
		}
		// Re-enqueue failed (queue full) → fall through to credential switch.
	}

	// (2) Switch credential under the same model.
	qr.markTriedCredential(qr.SelectedCred.CredentialID)
	qr.CredRetryCount = 0
	refs, _ := p.routeFunc(ctxOf(qr), qr)
	for _, ref := range refs {
		if qr.hasTriedCredential(ref.CredentialID) {
			continue
		}
		qr.SelectedCred = ref
		qr.vendor = ref.Vendor
		metricFailover.WithLabelValues("cred_switch").Inc()
		if p.tryEnqueueCred(ref, qr) {
			return
		}
		qr.markTriedCredential(ref.CredentialID)
	}

	// (3) All credentials under this model exhausted → model-change.
	slog.Info("dispatch: all credentials exhausted under model, trying model-change",
		"request_id", qr.ID, "model", qr.ResolvedModel,
		"tried_creds", len(qr.TriedCredentials))
	p.tryModelChange(qr, err)
}
