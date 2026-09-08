package dispatch

import (
	"context"
	"log/slog"
	"time"
)

// SessionAffinitySink persists routing affinity outside dispatch. Calls are
// best-effort so Redis/session-store outages cannot change request delivery.
type SessionAffinitySink interface {
	RecordSuccess(ctx context.Context, sessionID string, credential CredentialRef, model string) error
	Invalidate(ctx context.Context, sessionID string, credentialID int) error
}

func (p *Pipeline) recordSessionAffinity(qr *QueuedRequest, out ForwardOutcome) {
	if p == nil || qr == nil || qr.SessionID == "" {
		return
	}
	p.queueObservationMu.RLock()
	sink := p.affinitySink
	p.queueObservationMu.RUnlock()
	if sink == nil {
		return
	}
	if out.Err == nil {
		// 2026-09-09 audit round 3: bound the Redis write — WithoutCancel
		// alone let a Redis blip stall every request completion for the
		// go-redis default ReadTimeout (3s), the same head-of-line class the
		// journal sink fixed (pipeline.go untimed DB writes).
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctxOf(qr)), 200*time.Millisecond)
		defer cancel()
		if err := sink.RecordSuccess(ctx, qr.SessionID, qr.selectedCredential(), qr.resolvedModel()); err != nil {
			slog.Warn("dispatch: record session affinity failed", "request_id", qr.ID, "error", err)
		}
	}
}

func (p *Pipeline) invalidateSessionAffinity(qr *QueuedRequest, credentialID int) {
	if p == nil || qr == nil || qr.SessionID == "" || credentialID <= 0 {
		return
	}
	p.queueObservationMu.RLock()
	sink := p.affinitySink
	p.queueObservationMu.RUnlock()
	if sink == nil {
		return
	}
	// Same bounded-write treatment as RecordSuccess above (audit round 3).
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctxOf(qr)), 200*time.Millisecond)
	defer cancel()
	if err := sink.Invalidate(ctx, qr.SessionID, credentialID); err != nil {
		slog.Warn("dispatch: invalidate session affinity failed", "request_id", qr.ID, "credential_id", credentialID, "error", err)
	}
}

func (p *Pipeline) recordMinuteStats(qr *QueuedRequest, out ForwardOutcome) {
	if p == nil || qr == nil {
		return
	}
	p.queueObservationMu.RLock()
	sink := p.minuteStatsSink
	p.queueObservationMu.RUnlock()
	if sink == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctxOf(qr)), 100*time.Millisecond)
	defer cancel()
	if err := sink.Record(ctx, qr.selectedCredential(), qr.resolvedModel(), qr.EstimatedTokens, out); err != nil {
		slog.Warn("dispatch: record minute stats failed", "request_id", qr.ID, "error", err)
	}
}
