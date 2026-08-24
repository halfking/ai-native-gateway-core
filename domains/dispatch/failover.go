package dispatch

import (
	"log/slog"
	"strconv"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/liveactions"
)

// failoverItem pairs a request with the pre-firstbyte error that caused the
// forwarder to hand it to the ③ mover.
type failoverItem struct {
	qr  *QueuedRequest
	out ForwardOutcome
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
			p.move(it.qr, it.out)
		case <-p.stopCh:
			return
		}
	}
}

// move implements the failover ladder:
//  1. same-credential retry while under the request retry budget (deferred
//     until retry_at when a RetryScheduler is wired, v4 T3-8);
//  2. switch to another available credential under the same provider/model;
//  3. cross-provider credential switch only when the request allows it;
//  4. model-change only when both global and request-level switches allow it;
//  5. terminal → complete with the aggregate exhaustion error when every
//     model×node combination was tried (R2.4: combination exhaustion takes
//     TERMINATION PRIORITY over the 100-attempt budget), otherwise with the
//     real upstream error.
//
// The attempt cap (maxAttempts) is enforced at each CONTINUATION step below
// rather than at the top of the ladder: when the combination is exhausted the
// ladder's terminal sites fire first, so exhaustion always wins over budget
// (UT-FO-05).
func (p *Pipeline) move(qr *QueuedRequest, out ForwardOutcome) {
	err := out.Err
	fatalCredential := out.FatalCredential
	// (1) Same-credential retry — skipped for credential-fatal errors.
	// Retrying a quota-exhausted / auth-revoked credential deterministically
	// re-yields the same upstream rejection, wasting a concurrency slot and
	// delaying the switch to a healthy sibling. Mirrors the legacy executor
	// loop's errorsx.IsCredentialFatal → continue path (executor.go:3510).
	if !fatalCredential && qr.CredRetryCount < p.retryBudget(qr) {
		if qr.AttemptCount >= maxAttempts {
			p.terminateOnAttemptCap(qr, out)
			return
		}
		qr.CredRetryCount++
		metricFailover.WithLabelValues("cred_retry").Inc()
		if qr.OnNodeSwitchSummary != nil {
			qr.OnNodeSwitchSummary(failoverSummary(out.ErrorKind, out.HTTPStatus, "retry"))
		}
		// V3.3-OBS OBS-B1 (2026-08-15): node_switch 动作事件，retry=true、
		// retry_seq 递增时前端打特别标（24 号 §1）。
		p.emitNodeSwitch(qr, qr.SelectedCred.CredentialID, qr.SelectedCred.CredentialID, "cred_retry", true, qr.CredRetryCount)
		if p.scheduleSameCredRetry(qr, out, err) {
			return
		}
		// Re-enqueue failed (queue full) → fall through to credential switch.
	}

	// (2/3) Switch credential under the current model, honoring provider scope.
	qr.markTriedCredential(qr.SelectedCred.CredentialID)
	p.invalidateSessionAffinity(qr, qr.SelectedCred.CredentialID)
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
		if qr.AttemptCount >= maxAttempts {
			p.terminateOnAttemptCap(qr, out)
			return
		}
		fromCred := qr.SelectedCred.CredentialID
		fromModel := qr.ResolvedModel
		p.selectCredential(qr, ref)
		metricFailover.WithLabelValues("cred_switch").Inc()
		// V3.3-OBS OBS-B1 (2026-08-15): node_switch 动作事件（跨凭据切换）。
		p.emitNodeSwitch(qr, fromCred, ref.CredentialID, "cred_switch", false, 0)
		qr.emitObservation(Observation{
			Type:             ObservationNodeSwitched,
			Stage:            StageRetrying,
			ResolvedModel:    fromModel,
			Model:            fromModel,
			ProviderID:       int64(ref.ProviderID),
			Provider:         ref.Vendor,
			CredentialID:     int64(ref.CredentialID),
			FromCredentialID: int64(fromCred),
			ToCredentialID:   int64(ref.CredentialID),
			Attempt:          qr.lastAttemptRef(),
			SwitchReason:     "cred_switch",
		})
		if p.tryEnqueueCred(ref, qr) {
			if qr.OnNodeSwitchSummary != nil {
				qr.OnNodeSwitchSummary(failoverSummary(out.ErrorKind, out.HTTPStatus, "switch"))
			}
			return
		}
		qr.markTriedCredential(ref.CredentialID)
	}

	// (4) All credentials under this model exhausted → model-change.
	slog.Info("dispatch: all credentials exhausted under model, trying model-change",
		"request_id", qr.ID, "model", qr.ResolvedModel,
		"tried_creds", len(qr.TriedCredentials))
	p.tryModelChangeOutcome(qr, out)
}

func failoverSummary(errorKind string, status int, action string) string {
	kind := firstNonEmpty(errorKind, "upstream_error")
	if action == "switch" && isQuotaErrorKind(kind) {
		return "upstream node quota exhausted; switching to the next available node"
	}
	statusText := ""
	if status > 0 {
		statusText = " (HTTP " + strconv.Itoa(status) + ")"
	}
	if action == "retry" {
		return "上游请求暂时失败（" + kind + statusText + "），正在重试..."
	}
	return "上游请求失败（" + kind + statusText + "），正在切换到备用节点..."
}

func isQuotaErrorKind(errorKind string) bool {
	switch errorKind {
	case "quota", "quota_balance", "quota_periodic", "quota_permanent", "quota_exhausted",
		"upstream_quota_balance", "upstream_quota_periodic", "upstream_quota_permanent":
		return true
	default:
		return false
	}
}

// scheduleSameCredRetry emits the retry_scheduled journey event and either
// parks the request until retry_at (RetryScheduler wired, v4 T3-8) or
// re-enqueues immediately (legacy behavior). Returns true when the request
// left the mover (scheduled or enqueued).
func (p *Pipeline) scheduleSameCredRetry(qr *QueuedRequest, out ForwardOutcome, err error) bool {
	observation := Observation{
		Type:          ObservationRetryScheduled,
		Stage:         StageRetrying,
		ResolvedModel: qr.ResolvedModel,
		Model:         qr.ResolvedModel,
		ProviderID:    int64(qr.SelectedCred.ProviderID),
		Provider:      qr.SelectedCred.Vendor,
		CredentialID:  int64(qr.SelectedCred.CredentialID),
		Attempt:       qr.lastAttemptRef(),
		RetryReason:   firstNonEmpty(out.ErrorKind, classifyError(err)),
	}
	if p.retryScheduler != nil && ctxOf(qr).Err() == nil {
		// Timed retry: registry parks back to pending with retry_at
		// (spec §6), the event carries retry_at, and the scheduler re-injects
		// the request at the due time.
		retryAt := time.Now().Add(NextRetryDelay(qr.CredRetryCount, out.RetryAfter))
		at := retryAt
		observation.RetryAt = &at
		qr.emitObservation(observation)
		p.registry.MarkRetryScheduled(qr.ID, retryAt)
		p.queueMirror.MirrorRetryAt(qr.ID, retryAt)
		if p.retryScheduler.Schedule(qr, retryAt) {
			return true
		}
		// Scheduler refused (closed) → fall back to the immediate enqueue
		// without re-emitting the event above.
		return p.tryEnqueueCred(qr.SelectedCred, qr)
	}
	qr.emitObservation(observation)
	return p.tryEnqueueCred(qr.SelectedCred, qr)
}

// onRetryDue is the RetryScheduler pickup: the parked request re-enters
// in-flight (UT-DQ-04) and is re-enqueued on its scheduled credential.
func (p *Pipeline) onRetryDue(qr *QueuedRequest, retryAt time.Time) {
	if qr == nil || qr.completed.Load() {
		return // already terminal (client cancel raced the timer)
	}
	if p.shutdown.Load() {
		p.complete(qr, ForwardOutcome{Err: ErrShutdown})
		return
	}
	if ctxOf(qr).Err() != nil {
		p.complete(qr, ForwardOutcome{Err: ctxOf(qr).Err()})
		return
	}
	p.registry.MarkInFlight(qr.ID, time.Now())
	p.queueMirror.ClearRetryAt(qr.ID)

	// Distinguish capacity retry from error retry
	if qr.CapacityRetryCount > 0 {
		// Capacity retry → re-route to all credentials (don't fix to one credential)
		p.dispatch(qr)
	} else {
		// Error retry → re-enqueue to the same credential
		if !p.tryEnqueueCred(qr.SelectedCred, qr) {
			// R1.3: admission refused at re-enqueue → immediate overflow.
			p.complete(qr, ForwardOutcome{Err: &OverflowError{Reason: "cred_queue_full", RetryAfter: DefaultOverflowRetryAfter}})
		}
	}
}

// terminateOnAttemptCap is the global safety net: cap total forward attempts
// so a request can never churn an unbounded candidate set. The terminal
// error reuses the aggregate exhaustion envelope (ADR-Disp-006 mapping) so
// the client still receives the tried-combination summary.
func (p *Pipeline) terminateOnAttemptCap(qr *QueuedRequest, out ForwardOutcome) {
	metricOverflow.WithLabelValues("attempt_cap").Inc()
	p.observeOverflow("attempt_cap")
	slog.Warn("dispatch: attempt cap reached, giving up",
		"request_id", qr.ID, "attempts", qr.AttemptCount,
		"tried_creds", len(qr.TriedCredentials))
	out.Err = p.exhaustedTerminal(qr, terminalErr(out.Err))
	p.complete(qr, out)
}

// exhaustedTerminal wraps a terminal cause with the aggregate combination
// summary (R2.4/UT-FO-05). Never-routed requests (zero attempts) keep the
// plain sentinel — ErrNoRoute stays distinguishable for the 503 mapping.
// The wrapper delegates Error()/Unwrap() to the cause so existing callers
// that pin the concrete upstream error keep working.
func (p *Pipeline) exhaustedTerminal(qr *QueuedRequest, cause error) error {
	if qr == nil || qr.AttemptCount == 0 {
		return cause
	}
	return &ExhaustedError{Cause: cause, Attempts: qr.exhaustionAttempts()}
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
