package dispatch

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/kaixuan/llm-gateway-go/domains/requestjourney"
)

// EventSink is the dispatch-to-RequestJourney observation seam. Implementations
// must return quickly or arrange their own buffering. Observation must never
// change the dispatch result.
type EventSink interface {
	EmitJourneyEvent(context.Context, requestjourney.JourneyEvent)
}

// EventSinkFunc adapts a function to EventSink.
type EventSinkFunc func(context.Context, requestjourney.JourneyEvent)

func (f EventSinkFunc) EmitJourneyEvent(ctx context.Context, event requestjourney.JourneyEvent) {
	if f != nil {
		f(ctx, event)
	}
}

type journeyEmitter func(requestjourney.JourneyEvent)

type dispatchAttempt struct {
	ref         requestjourney.AttemptRef
	vendor      string
	startedAt   time.Time
	firstByteAt time.Time
	endedAt     time.Time
	outcome     requestjourney.Outcome
	errorKind   string
}

func (qr *QueuedRequest) setJourneyEmitter(emit journeyEmitter) {
	qr.journeyMu.Lock()
	qr.journeyEmit = emit
	qr.journeyMu.Unlock()
}

// emitJourney serializes sequence allocation and sink delivery. This preserves
// strict per-request ordering when streaming reports first byte concurrently
// with the forward goroutine finishing the attempt.
func (qr *QueuedRequest) emitJourney(event requestjourney.JourneyEvent) {
	if qr == nil {
		return
	}
	qr.journeyMu.Lock()
	defer qr.journeyMu.Unlock()
	qr.emitJourneyLocked(event)
}

func (qr *QueuedRequest) emitJourneyLocked(event requestjourney.JourneyEvent) {
	if qr.journeyEmit == nil || qr.TenantID == "" || qr.GatewayInstanceID == "" || qr.ID == "" {
		return
	}
	if event.Stage == requestjourney.StageTerminal && qr.JourneyTerminal != nil &&
		!qr.JourneyTerminal.CompareAndSwap(false, true) {
		return
	}
	event.TenantID = qr.TenantID
	event.GatewayInstanceID = qr.GatewayInstanceID
	event.RequestID = qr.ID
	if qr.JourneySharedSeq != nil {
		event.Seq = qr.JourneySharedSeq.Add(1)
	} else {
		event.Seq = qr.JourneySeq.Add(1)
	}
	event.RequestedModel = qr.RequestedModel
	event.ObservationStatus = requestjourney.ObservationComplete
	if event.OccurredAt.IsZero() {
		event.OccurredAt = time.Now()
	}
	if err := event.Validate(); err != nil {
		return
	}
	func() {
		defer func() { _ = recover() }()
		qr.journeyEmit(event)
	}()
}

func (qr *QueuedRequest) reserveAttempt(cred CredentialRef) requestjourney.AttemptRef {
	qr.attemptMu.Lock()
	defer qr.attemptMu.Unlock()
	ref := requestjourney.AttemptRef{
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

func (qr *QueuedRequest) commitReservedAttempt(attemptID string) (requestjourney.AttemptRef, bool) {
	qr.attemptMu.Lock()
	defer qr.attemptMu.Unlock()
	cur := qr.currentAttempt
	if cur == nil || cur.ref.AttemptID != attemptID || cur.ref.AttemptNo != qr.AttemptCount+1 {
		return requestjourney.AttemptRef{}, false
	}
	qr.AttemptCount++
	return cur.ref, true
}

func (qr *QueuedRequest) startAllocatedAttempt() (requestjourney.AttemptRef, time.Time, bool) {
	qr.attemptMu.Lock()
	defer qr.attemptMu.Unlock()
	cur := qr.currentAttempt
	if cur == nil || !cur.startedAt.IsZero() {
		return requestjourney.AttemptRef{}, time.Time{}, false
	}
	cur.startedAt = time.Now()
	startedAt := cur.startedAt
	qr.T7_ForwardStartAt = &startedAt
	qr.attempts = append(qr.attempts, cur)
	return cur.ref, startedAt, true
}

// ActiveAttemptRef returns a read-only snapshot of the currently allocated
// attempt. The returned value is detached from QueuedRequest's mutable state.
func (qr *QueuedRequest) ActiveAttemptRef() (requestjourney.AttemptRef, bool) {
	if qr == nil {
		return requestjourney.AttemptRef{}, false
	}
	qr.attemptMu.Lock()
	defer qr.attemptMu.Unlock()
	if qr.currentAttempt == nil || !qr.currentAttempt.endedAt.IsZero() {
		return requestjourney.AttemptRef{}, false
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
	qr.emitJourney(requestjourney.JourneyEvent{
		Type:         requestjourney.EventFirstByte,
		Stage:        requestjourney.StageStreaming,
		Model:        ref.Model,
		ProviderID:   ref.ProviderID,
		Provider:     ref.Provider,
		CredentialID: ref.CredentialID,
		Attempt:      copyAttemptRef(ref),
		OccurredAt:   firstByteAt,
	})
}

func (qr *QueuedRequest) finishAttempt(attemptID string, out ForwardOutcome) (requestjourney.AttemptRef, requestjourney.Outcome, string, time.Time, bool) {
	qr.attemptMu.Lock()
	defer qr.attemptMu.Unlock()
	cur := qr.currentAttempt
	if cur == nil || cur.ref.AttemptID != attemptID || cur.startedAt.IsZero() || !cur.endedAt.IsZero() {
		return requestjourney.AttemptRef{}, "", "", time.Time{}, false
	}
	cur.endedAt = time.Now()
	cur.outcome = requestjourney.OutcomeSuccess
	if out.Err != nil {
		cur.outcome = requestjourney.OutcomeFailure
		if errors.Is(out.Err, context.Canceled) || errors.Is(out.Err, context.DeadlineExceeded) {
			cur.outcome = requestjourney.OutcomeCanceled
		}
	}
	cur.errorKind = out.ErrorKind
	if cur.errorKind == "" {
		cur.errorKind = classifyError(out.Err)
	}
	qr.currentAttempt = nil
	return cur.ref, cur.outcome, cur.errorKind, cur.endedAt, true
}

func (qr *QueuedRequest) lastAttemptRef() *requestjourney.AttemptRef {
	qr.attemptMu.Lock()
	defer qr.attemptMu.Unlock()
	if len(qr.attempts) == 0 {
		return nil
	}
	return copyAttemptRef(qr.attempts[len(qr.attempts)-1].ref)
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

func copyAttemptRef(ref requestjourney.AttemptRef) *requestjourney.AttemptRef {
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
