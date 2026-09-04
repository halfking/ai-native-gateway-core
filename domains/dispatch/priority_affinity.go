package dispatch

import (
	"context"
	"log/slog"
	"sort"
	"time"
)

// SessionAffinitySink persists routing affinity outside dispatch. Calls are
// best-effort so Redis/session-store outages cannot change request delivery.
type SessionAffinitySink interface {
	RecordSuccess(ctx context.Context, sessionID string, credential CredentialRef, model string) error
	Invalidate(ctx context.Context, sessionID string, credentialID int) error
}

func sortPriorityClusters(refs []CredentialRef) []CredentialRef {
	if len(refs) < 2 {
		return refs
	}
	sorted := append([]CredentialRef(nil), refs...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].PriorityCluster < sorted[j].PriorityCluster
	})
	return sorted
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
		if err := sink.RecordSuccess(context.WithoutCancel(ctxOf(qr)), qr.SessionID, qr.selectedCredential(), qr.resolvedModel()); err != nil {
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
	if err := sink.Invalidate(context.WithoutCancel(ctxOf(qr)), qr.SessionID, credentialID); err != nil {
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
