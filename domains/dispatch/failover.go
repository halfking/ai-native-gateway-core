package dispatch

import (
	"log/slog"
	"strconv"

	"github.com/kaixuan/llm-gateway-go/internal/liveactions"
)

// failoverItem pairs a request with the pre-firstbyte error that caused the
// forwarder to hand it to the ③ mover.
type failoverItem struct {
	qr              *QueuedRequest
	err             error
	fatalCredential bool
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
			p.move(it.qr, it.err, it.fatalCredential)
		case <-p.stopCh:
			return
		}
	}
}

// move implements the failover ladder:
//  1. same-credential retry while under the request retry budget;
//  2. switch to another available credential under the same provider/model;
//  3. cross-provider credential switch only when the request allows it;
//  4. model-change only when both global and request-level switches allow it;
//  5. terminal → complete with the real upstream error when present.
func (p *Pipeline) move(qr *QueuedRequest, err error, fatalCredential bool) {
	// Global safety net: cap total forward attempts so a request can never
	// churn an unbounded candidate set. Under normal operation this is well
	// above candidate count × retry and never trips.
	if qr.AttemptCount >= maxAttempts {
		metricOverflow.WithLabelValues("attempt_cap").Inc()
		slog.Warn("dispatch: attempt cap reached, giving up",
			"request_id", qr.ID, "attempts", qr.AttemptCount,
			"tried_creds", len(qr.TriedCredentials))
		p.complete(qr, ForwardOutcome{Err: terminalErr(err)})
		return
	}
	// (1) Same-credential retry — skipped for credential-fatal errors.
	// Retrying a quota-exhausted / auth-revoked credential deterministically
	// re-yields the same upstream rejection, wasting a concurrency slot and
	// delaying the switch to a healthy sibling. Mirrors the legacy executor
	// loop's errorsx.IsCredentialFatal → continue path (executor.go:3510).
	if !fatalCredential && qr.CredRetryCount < p.retryBudget(qr) {
		qr.CredRetryCount++
		metricFailover.WithLabelValues("cred_retry").Inc()
		// V3.3-OBS OBS-B1 (2026-08-15): node_switch 动作事件，retry=true、
		// retry_seq 递增时前端打特别标（24 号 §1）。
		p.emitNodeSwitch(qr, qr.SelectedCred.CredentialID, qr.SelectedCred.CredentialID, "cred_retry", true, qr.CredRetryCount)
		if p.tryEnqueueCred(qr.SelectedCred, qr) {
			return
		}
		// Re-enqueue failed (queue full) → fall through to credential switch.
	}

	// (2/3) Switch credential under the current model, honoring provider scope.
	qr.markTriedCredential(qr.SelectedCred.CredentialID)
	qr.CredRetryCount = 0
	refs, _ := p.routeFunc(ctxOf(qr), qr)
	for _, ref := range refs {
		if qr.hasTriedCredential(ref.CredentialID) {
			continue
		}
		if !p.providerSwitchAllowed(qr, ref.ProviderID) {
			qr.markTriedCredential(ref.CredentialID)
			continue
		}
		fromCred := qr.SelectedCred.CredentialID
		p.selectCredential(qr, ref)
		metricFailover.WithLabelValues("cred_switch").Inc()
		// V3.3-OBS OBS-B1 (2026-08-15): node_switch 动作事件（跨凭据切换）。
		p.emitNodeSwitch(qr, fromCred, ref.CredentialID, "cred_switch", false, 0)
		if p.tryEnqueueCred(ref, qr) {
			return
		}
		qr.markTriedCredential(ref.CredentialID)
	}

	// (4) All credentials under this model exhausted → model-change.
	slog.Info("dispatch: all credentials exhausted under model, trying model-change",
		"request_id", qr.ID, "model", qr.ResolvedModel,
		"tried_creds", len(qr.TriedCredentials))
	p.tryModelChange(qr, err)
}

func (p *Pipeline) retryBudget(qr *QueuedRequest) int {
	if qr != nil && qr.RetryPerCredential >= 0 {
		return qr.RetryPerCredential
	}
	return p.config().RetryPerCredential
}

func (p *Pipeline) providerSwitchAllowed(qr *QueuedRequest, providerID int) bool {
	if qr == nil || qr.InitialProviderID == 0 || providerID == 0 {
		return true
	}
	if providerID == qr.InitialProviderID {
		return true
	}
	return qr.AllowProviderChange
}

func terminalErr(err error) error {
	if err != nil {
		return err
	}
	return ErrNoRoute
}

// emitNodeSwitch emits the node_switch action event (V3.3-OBS OBS-B1,
// 24 号 §2: from/to/reason/retry/retry_seq)。
func (p *Pipeline) emitNodeSwitch(qr *QueuedRequest, fromCred, toCred int, reason string, retry bool, retrySeq int) {
	if p == nil || qr == nil {
		return
	}
	p.liveActions.Emit(ctxOf(qr), liveactions.ActionEvent{
		RequestID:    qr.ID,
		Action:       liveactions.ActionNodeSwitch,
		Model:        qr.ResolvedModel,
		CredentialID: toCred,
		Retry:        retry,
		RetrySeq:     retrySeq,
		Detail: map[string]string{
			"from_credential_id": strconv.Itoa(fromCred),
			"to_credential_id":   strconv.Itoa(toCred),
			"reason":             reason,
		},
	})
}
