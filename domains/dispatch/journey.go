package dispatch

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

type observationEmitter func(Observation)

type dispatchAttempt struct {
	ref         AttemptRef
	vendor      string
	startedAt   time.Time
	firstByteAt time.Time
	endedAt     time.Time
	outcome     Outcome
	errorKind   string
}

func (qr *QueuedRequest) setObservationEmitter(emit observationEmitter) {
	qr.journeyMu.Lock()
	qr.observationEmit = emit
	qr.journeyMu.Unlock()
}

// emitObservation serializes sequence allocation and sink delivery. This
// preserves strict per-request ordering when streaming reports first byte
// concurrently with the forward goroutine finishing the attempt.
func (qr *QueuedRequest) emitObservation(observation Observation) {
	if qr == nil {
		return
	}
	qr.journeyMu.Lock()
	defer qr.journeyMu.Unlock()
	qr.emitObservationLocked(observation)
}

func (qr *QueuedRequest) emitObservationLocked(observation Observation) {
	if qr.observationEmit == nil || qr.TenantID == "" || qr.GatewayInstanceID == "" || qr.ID == "" {
		return
	}
	if observation.Stage == StageTerminal && qr.JourneyTerminal != nil &&
		!qr.JourneyTerminal.CompareAndSwap(false, true) {
		return
	}
	observation.TenantID = qr.TenantID
	observation.GatewayInstanceID = qr.GatewayInstanceID
	observation.RequestID = qr.ID
	if qr.JourneySharedSeq != nil {
		observation.Seq = qr.JourneySharedSeq.Add(1)
	} else {
		observation.Seq = qr.JourneySeq.Add(1)
	}
	observation.RequestedModel = qr.RequestedModel
	if observation.OccurredAt.IsZero() {
		observation.OccurredAt = time.Now()
	}
	if err := observation.Validate(); err != nil {
		return
	}
	func() {
		defer func() { _ = recover() }()
		qr.observationEmit(observation)
	}()
}

func (qr *QueuedRequest) reserveAttempt(cred CredentialRef) AttemptRef {
	qr.attemptMu.Lock()
	defer qr.attemptMu.Unlock()
	ref := AttemptRef{
		AttemptID:    uuid.NewString(),
		AttemptNo:    qr.AttemptCount + 1,
		Model:        qr.ResolvedModel,
		ProviderID:   int64(cred.ProviderID),
		Provider:     cred.Vendor,
		CredentialID: int64(cred.CredentialID),
	}
	qr.currentAttempt = &dispatchAttempt{ref: ref, vendor: cred.Vendor}
	return ref
}

func (qr *QueuedRequest) abandonReservedAttempt(attemptID string) {
	qr.attemptMu.Lock()
	defer qr.attemptMu.Unlock()
	if qr.currentAttempt != nil && qr.currentAttempt.ref.AttemptID == attemptID && qr.currentAttempt.startedAt.IsZero() {
		qr.currentAttempt = nil
	}
}

func (qr *QueuedRequest) commitReservedAttempt(attemptID string) (AttemptRef, bool) {
	qr.attemptMu.Lock()
	defer qr.attemptMu.Unlock()
	cur := qr.currentAttempt
	if cur == nil || cur.ref.AttemptID != attemptID || cur.ref.AttemptNo != qr.AttemptCount+1 {
		return AttemptRef{}, false
	}
	qr.AttemptCount++
	return cur.ref, true
}

func (qr *QueuedRequest) startAllocatedAttempt() (AttemptRef, time.Time, bool) {
	qr.attemptMu.Lock()
	defer qr.attemptMu.Unlock()
	cur := qr.currentAttempt
	if cur == nil || !cur.startedAt.IsZero() {
		return AttemptRef{}, time.Time{}, false
	}
	cur.startedAt = time.Now()
	startedAt := cur.startedAt
	qr.T7_ForwardStartAt = &startedAt
	qr.attempts = append(qr.attempts, cur)
	return cur.ref, startedAt, true
}

// ActiveAttemptRef returns a read-only snapshot of the currently allocated
// attempt. The returned value is detached from QueuedRequest's mutable state.
func (qr *QueuedRequest) ActiveAttemptRef() (AttemptRef, bool) {
	if qr == nil {
		return AttemptRef{}, false
	}
	qr.attemptMu.Lock()
	defer qr.attemptMu.Unlock()
	if qr.currentAttempt == nil || !qr.currentAttempt.endedAt.IsZero() {
		return AttemptRef{}, false
	}
	return qr.currentAttempt.ref, true
}

// FirstSemanticByteCallback returns an idempotent callback bound to the active
// attempt. Streaming adapters should capture it during ForwardFunc so a delayed
// callback from an old stream can never mark a later retry.
func (qr *QueuedRequest) FirstSemanticByteCallback() func() {
	if qr == nil {
		return func() {}
	}
	qr.attemptMu.Lock()
	attemptID := ""
	if qr.currentAttempt != nil {
		attemptID = qr.currentAttempt.ref.AttemptID
	}
	qr.attemptMu.Unlock()
	return func() { qr.markFirstSemanticByte(attemptID) }
}

// MarkFirstSemanticByte records the first response byte semantically visible to
// the current attempt. It is convenient for synchronous ForwardFunc adapters;
// asynchronous streaming adapters should use FirstSemanticByteCallback.
func (qr *QueuedRequest) MarkFirstSemanticByte() {
	qr.markFirstSemanticByte("")
}

func (qr *QueuedRequest) markFirstSemanticByte(attemptID string) {
	if qr == nil {
		return
	}
	qr.attemptMu.Lock()
	defer qr.attemptMu.Unlock()
	cur := qr.currentAttempt
	if cur == nil || (attemptID != "" && cur.ref.AttemptID != attemptID) || cur.startedAt.IsZero() || !cur.endedAt.IsZero() || !cur.firstByteAt.IsZero() {
		return
	}
	cur.firstByteAt = time.Now()
	firstByteAt := cur.firstByteAt
	qr.T8_ResponseStartAt = &firstByteAt
	ref := cur.ref
	qr.emitObservation(Observation{
		Type:         ObservationFirstByte,
		Stage:        StageStreaming,
		Model:        ref.Model,
		ProviderID:   ref.ProviderID,
		Provider:     ref.Provider,
		CredentialID: ref.CredentialID,
		Attempt:      copyAttemptRef(ref),
		OccurredAt:   firstByteAt,
	})
}

func (qr *QueuedRequest) finishAttempt(attemptID string, out ForwardOutcome) (AttemptRef, Outcome, string, time.Time, bool) {
	qr.attemptMu.Lock()
	defer qr.attemptMu.Unlock()
	cur := qr.currentAttempt
	if cur == nil || cur.ref.AttemptID != attemptID || cur.startedAt.IsZero() || !cur.endedAt.IsZero() {
		return AttemptRef{}, "", "", time.Time{}, false
	}
	cur.endedAt = time.Now()
	cur.outcome = OutcomeSuccess
	if out.Err != nil {
		cur.outcome = OutcomeFailure
		if errors.Is(out.Err, context.Canceled) || errors.Is(out.Err, context.DeadlineExceeded) {
			cur.outcome = OutcomeCanceled
		}
	}
	cur.errorKind = out.ErrorKind
	if cur.errorKind == "" {
		cur.errorKind = classifyError(out.Err)
	}
	qr.currentAttempt = nil
	return cur.ref, cur.outcome, cur.errorKind, cur.endedAt, true
}

func (qr *QueuedRequest) lastAttemptRef() *AttemptRef {
	qr.attemptMu.Lock()
	defer qr.attemptMu.Unlock()
	if len(qr.attempts) == 0 {
		return nil
	}
	return copyAttemptRef(qr.attempts[len(qr.attempts)-1].ref)
}

// exhaustionAttempts builds the aggregate model×node×reason summary carried
// by ExhaustedError (R2.4 / UT-FO-05: the terminal error body lists every
// tried combination). Attempts with no classified error default to the
// coarse "upstream_error" bucket.
func (qr *QueuedRequest) exhaustionAttempts() []ExhaustionAttempt {
	qr.attemptMu.Lock()
	defer qr.attemptMu.Unlock()
	out := make([]ExhaustionAttempt, 0, len(qr.attempts))
	for _, attempt := range qr.attempts {
		reason := attempt.errorKind
		if reason == "" {
			reason = "upstream_error"
		}
		out = append(out, ExhaustionAttempt{
			Model:        attempt.ref.Model,
			ProviderID:   attempt.ref.ProviderID,
			CredentialID: attempt.ref.CredentialID,
			Reason:       reason,
		})
	}
	return out
}

func (qr *QueuedRequest) waterfallAttempts() []WaterfallAttempt {
	qr.attemptMu.Lock()
	defer qr.attemptMu.Unlock()
	out := make([]WaterfallAttempt, 0, len(qr.attempts))
	for _, attempt := range qr.attempts {
		out = append(out, WaterfallAttempt{
			AttemptID:    attempt.ref.AttemptID,
			AttemptNo:    attempt.ref.AttemptNo,
			Model:        attempt.ref.Model,
			ProviderID:   attempt.ref.ProviderID,
			CredentialID: attempt.ref.CredentialID,
			Vendor:       attempt.vendor,
			StartedAt:    formatTS(attempt.startedAt),
			FirstByteAt:  formatTS(attempt.firstByteAt),
			EndedAt:      formatTS(attempt.endedAt),
			Outcome:      string(attempt.outcome),
			ErrorKind:    attempt.errorKind,
		})
	}
	return out
}

func copyAttemptRef(ref AttemptRef) *AttemptRef {
	copy := ref
	return &copy
}

func classifyError(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case IsShutdown(err):
		return "shutdown"
	case IsPaceTimeout(err):
		return "pace_timeout"
	case errors.Is(err, ErrNoRoute):
		return "no_route"
	case errors.Is(err, ErrOverflow):
		return "overflow"
	default:
		return "upstream_error"
	}
}
