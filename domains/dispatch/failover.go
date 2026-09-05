package dispatch

import (
	"fmt"
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
//
// Shutdown (audit 2026-09-05 C-#2): every item still buffered in failoverCh
// when stopCh fires is completed with ErrShutdown instead of being orphaned —
// a dropped qr means its Submit caller blocks on qr.ResultCh until ctx
// expiry. Mirrors runDispatcher's shutdownDrainDispatchIn. See
// TestStopDrainsFailoverChResidue.
func (p *Pipeline) runFailover() {
	defer p.wg.Done()
	for {
		select {
		case it, ok := <-p.failoverCh:
			if !ok {
				return
			}
			if p.shutdown.Load() {
				// stopCh is already closed and the select randomly picked the
				// receive branch: buffered residue. Complete directly instead
				// of walking the failover ladder on a stopped pipeline.
				p.complete(it.qr, ForwardOutcome{Err: ErrShutdown})
				continue
			}
			p.move(it.qr, it.out)
		case <-p.stopCh:
			p.shutdownDrainFailover()
			return
		}
	}
}

// shutdownDrainFailover completes every item still buffered in failoverCh
// with ErrShutdown (audit 2026-09-05 C-#2).
//
// Ordering guarantee: the sole producers of failoverCh are the
// credForwarder loop goroutines (routeFailover from acquire/attempt).
// Waiting for them FIRST — after a credMu barrier that makes the producer
// set final (newCredForwarder is only reached via getOrCreateForwarder under
// credMu with a shutdown guard) — guarantees nothing is handed to failoverCh
// after the drain observes it empty. forwarderWg.Done fires after the loop's
// cf.wg.Wait, so in-flight attempt goroutines (which may also call
// routeFailover) are included.
func (p *Pipeline) shutdownDrainFailover() {
	p.credMu.Lock()
	p.credMu.Unlock() // barrier: producer set is now final (no new forwarders)
	p.forwarderWg.Wait()
	for {
		select {
		case it := <-p.failoverCh:
			p.complete(it.qr, ForwardOutcome{Err: ErrShutdown})
		default:
			return
		}
	}
}

// move implements the failover ladder (decisions in planner.go, V6-W1.6 R11):
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
	// Cancellation race guard (audit 2026-09-05 C-#1): Submit's ctx.Done path
	// may have already CAS-won complete() and stamped the terminal journal
	// entry in the caller's goroutine while this item sat in failoverCh. The
	// journal/counts/attempt fields recordDecision mutates are unsynchronized,
	// so a mover that lost the terminal race must not touch them (it also must
	// not burn another attempt/slot for a caller that already left).
	if qr == nil || qr.completed.Load() || qr.abandoned.Load() {
		return
	}
	err := out.Err
	cred := qr.selectedCredential()
	model := qr.resolvedModel()
	// (1) Same-credential retry — planner decision: skipped for
	// credential-fatal errors (retrying a quota-exhausted / auth-revoked
	// credential deterministically re-yields the same upstream rejection,
	// wasting a concurrency slot and delaying the switch to a healthy
	// sibling; mirrors the legacy executor loop's errorsx.IsCredentialFatal
	// → continue path, executor.go:3510).
	switch d := PlanAfterFailure(qr, out, p.config()); d.Action {
	case NextActionRetrySameCred:
		qr.CredRetryCount++
		metricFailover.WithLabelValues("cred_retry").Inc()
		// v6 G-Ⅴ (回队打标) + W1.6 R9 (执行轨迹): journal the previous
		// round's error kind, the model/node that failed, and the decided
		// next action before the request parks back into the pending set.
		// recordDecision refreshes LastFailover as the journal-tail projection.
		qr.recordDecision(JournalEntry{
			Model:        model,
			CredentialID: cred.CredentialID,
			ProviderID:   cred.ProviderID,
			Vendor:       cred.Vendor,
			Action:       NextActionRetrySameCred,
			ErrorKind:    firstNonEmpty(out.ErrorKind, classifyError(err)),
			HTTPStatus:   out.HTTPStatus,
			Attempt:      qr.AttemptCount,
		})
		if qr.OnNodeSwitchSummary != nil {
			qr.OnNodeSwitchSummary(failoverSummary(out.ErrorKind, out.HTTPStatus, "retry"))
		}
		// V3.3-OBS OBS-B1 (2026-08-15): node_switch 动作事件，retry=true、
		// retry_seq 递增时前端打特别标（24 号 §1）。
		p.emitNodeSwitch(qr, cred.CredentialID, cred.CredentialID, "cred_retry", true, qr.CredRetryCount)
		if p.scheduleSameCredRetry(qr, out, err) {
			return
		}
		// Re-enqueue failed (queue full) → fall through to credential switch.
	case NextActionFailed:
		// R12 attempt_cap: the planner refused the continuation.
		p.terminateOnAttemptCap(qr, out)
		return
	}

	// (2/3) Switch credential under the current model, honoring provider scope.
	fromCredID := cred.CredentialID
	fromProviderID := cred.ProviderID
	qr.markTriedCredential(fromCredID)
	p.invalidateSessionAffinity(qr, fromCredID)
	qr.CredRetryCount = 0
	
	// 2026-09-01 P0 fix: plan the switch BEFORE journaling so we can record
	// complete from/to endpoints in one entry. This preserves the journal
	// contract that recordDecision updates LastFailover to the tail entry.
	refs, _ := p.routeFunc(ctxOf(qr), qr)
	next, scoped := PlanSwitchCred(qr, refs)
	for _, id := range scoped {
		qr.markTriedCredential(id)
	}
	
	// v6 G-Ⅴ + W1.6 R9: the credential-exhaustion round is journaled before
	// hunting for the sibling so the tail always reflects the LAST executed node.
	// 2026-09-01 P0 fix: populate FromCredentialID/ToCredentialID so the
	// journal→journey bridge can emit valid EventNodeSwitched events.
	journalEntry := JournalEntry{
		Model:            model,
		CredentialID:     fromCredID,
		ProviderID:       fromProviderID,
		Vendor:           cred.Vendor,
		Action:           NextActionSwitchCred,
		ErrorKind:        firstNonEmpty(out.ErrorKind, classifyError(err)),
		HTTPStatus:       out.HTTPStatus,
		Attempt:          qr.AttemptCount,
		FromCredentialID: fromCredID,
		FromProviderID:   fromProviderID,
	}
	if next != nil {
		// Found a switch target: fill To fields
		journalEntry.ToCredentialID = next.CredentialID
		journalEntry.ToProviderID = next.ProviderID
	}
	qr.recordDecision(journalEntry)
	
	if next == nil {
		// No sibling credential available under this model → model change.
		p.tryModelChangeOutcome(qr, out)
		return
	}
	
	for {
		// Already consumed next from the first plan above
		if next == nil {
			next, scoped = PlanSwitchCred(qr, refs)
			for _, id := range scoped {
				qr.markTriedCredential(id)
			}
			if next == nil {
				break
			}
		}
		ref := *next
		next = nil // consume the planned switch
		
		// Continuation step: the attempt budget guards it (R12).
		if AttemptBudgetLeft(qr) <= 0 {
			p.terminateOnAttemptCap(qr, out)
			return
		}
		fromCred := qr.selectedCredential().CredentialID
		fromModel := qr.resolvedModel()
		p.selectCredential(qr, ref)
		metricFailover.WithLabelValues("cred_switch").Inc()
		// Prepare the switch notice while this goroutine still owns qr; it
		// is delivered only after the enqueue succeeds (see below).
		switchNotice := qr.prepareNotice(DispatchNotice{
			Kind:             NoticeKindNodeSwitch,
			Message:          failoverSummary(out.ErrorKind, out.HTTPStatus, "switch"),
			ErrorKind:        firstNonEmpty(out.ErrorKind, classifyError(err)),
			FromCredentialID: fromCred,
			ToCredentialID:   ref.CredentialID,
			FromModel:        fromModel,
			ToModel:          qr.resolvedModel(),
			Vendor:           ref.Vendor,
			Attempt:          qr.AttemptCount,
		})
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
			// v6 G-Ⅲ: route-switch notice rides the thinking channel so the
			// client sees the failover without it entering the conversation.
			// The notice was prepared (seq-stamped) BEFORE the Tier-2 handoff
			// so this goroutine no longer reads mutable qr state after the
			// forwarder took ownership.
			qr.deliverNotice(switchNotice)
			return
		}
		qr.markTriedCredential(ref.CredentialID)
	}

	// (4) All credentials under this model exhausted → model-change.
	slog.Info("dispatch: all credentials exhausted under model, trying model-change",
		"request_id", qr.ID, "model", qr.resolvedModel(),
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
	cred := qr.selectedCredential()
	observation := Observation{
		Type:          ObservationRetryScheduled,
		Stage:         StageRetrying,
		ResolvedModel: qr.resolvedModel(),
		Model:         qr.resolvedModel(),
		ProviderID:    int64(cred.ProviderID),
		Provider:      cred.Vendor,
		CredentialID:  int64(cred.CredentialID),
		Attempt:       qr.lastAttemptRef(),
		RetryReason:   firstNonEmpty(out.ErrorKind, classifyError(err)),
	}
	errorKind := observation.RetryReason
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
			// v6 G-Ⅲ/G-Ⅴ: waiting notice + pending projection.
			p.dimensionIndex.UpdateWait(qr, retryAt, NextActionRetrySameCred, time.Now())
			qr.notifyDispatch(p.retryNotice(qr, out, err, retryAt, errorKind))
			return true
		}
		// Scheduler refused (closed) → fall back to the immediate enqueue
		// without re-emitting the event above.
		qr.notifyDispatch(p.retryNotice(qr, out, err, time.Time{}, errorKind))
		return p.tryEnqueueCred(cred, qr)
	}
	qr.emitObservation(observation)
	qr.notifyDispatch(p.retryNotice(qr, out, err, time.Time{}, errorKind))
	return p.tryEnqueueCred(cred, qr)
}

// retryNotice builds the same-credential retry notice (v6 G-Ⅲ).
func (p *Pipeline) retryNotice(qr *QueuedRequest, out ForwardOutcome, err error, retryAt time.Time, errorKind string) DispatchNotice {
	cred := qr.selectedCredential()
	model := qr.resolvedModel()
	notice := DispatchNotice{
		Kind:             NoticeKindRetry,
		Message:          failoverSummary(out.ErrorKind, out.HTTPStatus, "retry"),
		ErrorKind:        errorKind,
		FromCredentialID: cred.CredentialID,
		ToCredentialID:   cred.CredentialID,
		FromModel:        model,
		ToModel:          model,
		Vendor:           cred.Vendor,
		Attempt:          qr.CredRetryCount,
	}
	if !retryAt.IsZero() {
		notice.RetryAt = retryAt
		notice.WaitHint = waitHint(time.Until(retryAt))
		notice.Message = fmt.Sprintf("%s（等待 %s 后第 %d 次重试）",
			notice.Message, notice.WaitHint, qr.CredRetryCount)
	}
	return notice
}

// onRetryDue is the RetryScheduler pickup: the parked request re-enters
// in-flight (UT-DQ-04) and is re-enqueued on its scheduled credential.
func (p *Pipeline) onRetryDue(qr *QueuedRequest, retryAt time.Time) {
	if qr == nil || qr.completed.Load() || qr.abandoned.Load() {
		return // already terminal or caller left (client cancel raced the timer)
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
		if !p.tryEnqueueCred(qr.selectedCredential(), qr) {
			// R1.3: admission refused at re-enqueue → immediate overflow.
			p.complete(qr, ForwardOutcome{Err: &OverflowError{Reason: "cred_queue_full", RetryAfter: DefaultOverflowRetryAfter}})
		}
	}
}

// terminateOnAttemptCap is the global safety net: cap total forward attempts
// so a request can never churn an unbounded candidate set. The terminal
// outcome is built by the planner (attemptCapOutcome, R12): the aggregate
// exhaustion envelope (ADR-Disp-006 mapping) stamped with the attempt_cap
// reason so the journal terminal entry reads failed(attempt_cap).
func (p *Pipeline) terminateOnAttemptCap(qr *QueuedRequest, out ForwardOutcome) {
	metricOverflow.WithLabelValues("attempt_cap").Inc()
	p.observeOverflow("attempt_cap")
	slog.Warn("dispatch: attempt cap reached, giving up",
		"request_id", qr.ID, "attempts", qr.AttemptCount,
		"tried_creds", len(qr.TriedCredentials))
	p.complete(qr, attemptCapOutcome(qr, out))
}

func (p *Pipeline) retryBudget(qr *QueuedRequest) int {
	return retryBudgetOf(qr, p.config())
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
		Model:        qr.resolvedModel(),
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
