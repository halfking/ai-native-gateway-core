package main

import (
	"context"
	"crypto/sha256"
	"log/slog"
	"sync"

	"github.com/kaixuan/llm-gateway-go/domains/dispatch"
	"github.com/kaixuan/llm-gateway-go/domains/requestjourney"
)

// dispatchJourneyAdapter is the composition adapter between dispatch's
// content-free observation contract and the persisted RequestJourney contract.
// The dispatch pipeline owns ordering and bounded asynchronous delivery; this
// adapter only translates immutable values and never changes execution.
type dispatchJourneyAdapter struct {
	recorder *requestjourney.Recorder
}

func newDispatchJourneyAdapter(recorder *requestjourney.Recorder) dispatch.ObservationSink {
	if recorder == nil {
		return nil
	}
	return &dispatchJourneyAdapter{recorder: recorder}
}

// ObserveDispatch translates one dispatch observation into a RequestJourney
// event and applies it to the recorder. Translation errors are logged and
// dropped; execution never depends on observation persistence.
func (a *dispatchJourneyAdapter) ObserveDispatch(ctx context.Context, observation dispatch.Observation) {
	if a == nil || a.recorder == nil {
		return
	}
	event, err := translateDispatchObservation(observation)
	if err != nil {
		slog.Warn("dispatch observation translation rejected", "request_id", observation.RequestID, "seq", observation.Seq, "error", err)
		return
	}
	if err := a.recorder.Apply(ctx, event); err != nil {
		slog.Warn("dispatch request journey observation rejected", "request_id", observation.RequestID, "seq", observation.Seq, "error", err)
	}
}

func translateDispatchObservation(observation dispatch.Observation) (requestjourney.JourneyEvent, error) {
	if err := observation.Validate(); err != nil {
		return requestjourney.JourneyEvent{}, err
	}
	event := requestjourney.JourneyEvent{
		TenantID: observation.TenantID, GatewayInstanceID: observation.GatewayInstanceID, RequestID: observation.RequestID,
		Seq: observation.Seq, Type: requestjourney.EventType(observation.Type), Stage: requestjourney.JourneyStage(observation.Stage),
		RequestedModel: observation.RequestedModel, ResolvedModel: observation.ResolvedModel, Model: observation.Model,
		ProviderID: observation.ProviderID, Provider: observation.Provider, CredentialID: observation.CredentialID,
		FromModel: observation.FromModel, ToModel: observation.ToModel, FromCredentialID: observation.FromCredentialID,
		ToCredentialID: observation.ToCredentialID, Outcome: requestjourney.Outcome(observation.Outcome), ErrorKind: observation.ErrorKind,
		HTTPStatus: observation.HTTPStatus, RetryReason: observation.RetryReason, SwitchReason: observation.SwitchReason,
		RetryAt:           observation.RetryAt,
		ObservationStatus: requestjourney.ObservationComplete, OccurredAt: observation.OccurredAt,
	}
	if observation.Attempt != nil {
		event.Attempt = &requestjourney.AttemptRef{
			AttemptID: observation.Attempt.AttemptID, AttemptNo: observation.Attempt.AttemptNo,
			Model: observation.Attempt.Model, ProviderID: observation.Attempt.ProviderID,
			Provider: observation.Attempt.Provider, CredentialID: observation.Attempt.CredentialID,
		}
	}
	if err := event.Validate(); err != nil {
		return requestjourney.JourneyEvent{}, err
	}
	return event, nil
}

// dispatchJourneyJournalAdapter (audit-24h-20260828-r3) bridges the
// dispatch.JournalSnapshot (per-request attempt trace) into the requestjourney
// Recorder at terminal time. Each JournalEntry is mapped to a single
// JourneyEvent so the trace flows through the same persistence pipeline as
// observations, sharing the existing seq-monotonic invariant by reading the
// recorder's current max seq and offsetting. Translation errors and Apply
// errors are logged and dropped; the trace is best-effort diagnostics, never
// gating execution. Kept independent of dispatchJourneyAdapter so a future
// change to one bridge cannot silently drop the other.
type journalSnapshotReceipt struct {
	hash [sha256.Size]byte
}

type journalSnapshotReceiptKey struct {
	tenantID, requestID string
	version             int64
}

type dispatchJourneyJournalAdapter struct {
	recorder *requestjourney.Recorder
	instance string
	store    dispatch.JournalSnapshotStore
	receipt  *requestjourney.JournalSnapshotReceiptStore
	mu       sync.Mutex
	receipts map[journalSnapshotReceiptKey]journalSnapshotReceipt
}

func newDispatchJourneyJournalAdapter(recorder *requestjourney.Recorder, instanceID string, stores ...dispatch.JournalSnapshotStore) dispatch.JournalSink {
	return newDispatchJourneyJournalAdapterWithDependencies(recorder, instanceID, nil, stores...)
}

func newDispatchJourneyJournalAdapterWithReceipt(recorder *requestjourney.Recorder, instanceID string, receipt *requestjourney.JournalSnapshotReceiptStore, stores ...dispatch.JournalSnapshotStore) dispatch.JournalSink {
	return newDispatchJourneyJournalAdapterWithDependencies(recorder, instanceID, receipt, stores...)
}

func newDispatchJourneyJournalAdapterWithDependencies(recorder *requestjourney.Recorder, instanceID string, receipt *requestjourney.JournalSnapshotReceiptStore, stores ...dispatch.JournalSnapshotStore) dispatch.JournalSink {
	if recorder == nil {
		return nil
	}
	var store dispatch.JournalSnapshotStore
	if len(stores) > 0 {
		store = stores[0]
	}
	return &dispatchJourneyJournalAdapter{recorder: recorder, instance: instanceID, store: store, receipt: receipt, receipts: make(map[journalSnapshotReceiptKey]journalSnapshotReceipt)}
}

// ApplyJournalSnapshot fans the per-request attempt journal out into the
// recorder as a sequence of JourneyEvents. Seq is offset from the recorder's
// current max for (tenant, request) so journal entries never collide with
// observation seqs (the projection rejects non-monotonic Seq).
//
// ADR §4 (2026-08-28-journal-snapshot): reject snapshots whose caller auth
// context disagrees with the snapshot's own tenant — a misconfigured or
// compromised caller must not be able to persist cross-tenant traces.
// ADR §6: idempotency by (tenant, request, snapshot_version). When the
// recorder's MaxSeq already covers the snapshot, the per-entry loop is
// short-circuited; otherwise projection.Apply's existing byte-equal dedup
// guarantees a second delivery of the same (tenant, request, seq) tuple is
// a silent no-op.
func (a *dispatchJourneyJournalAdapter) ApplyJournalSnapshot(ctx context.Context, snap dispatch.JournalSnapshot) {
	if a == nil || a.recorder == nil || !snap.CallerAuthorized {
		return
	}
	if snap.CallerTenantID != "" && snap.CallerTenantID != snap.TenantID {
		slog.Warn("dispatch journal sink rejected tenant mismatch", "request_id", snap.RequestID)
		return
	}
	if snap.TenantID == "" || snap.RequestID == "" || len(snap.Entries) == 0 || snap.SnapshotVersion <= 0 {
		return
	}

	// Serialize same-process delivery before claiming the durable receipt. The
	// durable store fences across processes; this lock also prevents two calls
	// from the same owner reclaiming their own live lease concurrently.
	a.mu.Lock()
	defer a.mu.Unlock()

	var claim requestjourney.JournalSnapshotReceiptClaim
	if a.receipt != nil {
		hash, err := requestjourney.SnapshotPayloadHash(snap)
		if err != nil {
			slog.Warn("dispatch journal snapshot hash failed", "request_id", snap.RequestID, "error", err)
			return
		}
		claim, err = a.receipt.Claim(ctx, snap.TenantID, snap.RequestID, snap.SnapshotVersion, hash)
		if err != nil || claim.AlreadyCompleted || !claim.Claimed {
			if err != nil {
				slog.Warn("dispatch journal snapshot receipt claim failed", "request_id", snap.RequestID, "snapshot_version", snap.SnapshotVersion, "error", err)
			}
			return
		}
	}

	hash := journalSnapshotHash(snap)
	key := journalSnapshotReceiptKey{tenantID: snap.TenantID, requestID: snap.RequestID, version: snap.SnapshotVersion}
	localClaimed := a.receipt == nil
	if localClaimed {
		if prior, ok := a.receipts[key]; ok {
			if prior.hash != hash {
				slog.Error("dispatch journal snapshot version conflict", "request_id", snap.RequestID)
			}
			return
		}
		a.receipts[key] = journalSnapshotReceipt{hash: hash}
	}
	failed := false
	defer func() {
		if a.receipt != nil {
			if !failed {
				if err := a.receipt.Complete(ctx, claim); err != nil {
					slog.Warn("dispatch journal snapshot receipt completion failed", "request_id", snap.RequestID, "snapshot_version", snap.SnapshotVersion, "error", err)
				}
			}
		} else if localClaimed && failed {
			delete(a.receipts, key)
		}
	}()

	if a.store != nil {
		stored := snap
		stored.Entries = append([]dispatch.JournalEntry(nil), snap.Entries...)
		a.store.Store(stored)
	}
	baseSeq := a.recorder.MaxSeq(snap.TenantID, snap.RequestID)
	for i, entry := range snap.Entries {
		event, ok := journalEntryToJourneyEvent(a.instance, snap.TenantID, snap.RequestID, baseSeq, i, entry)
		if !ok {
			continue
		}
		if err := a.recorder.Apply(ctx, event); err != nil {
			failed = true
			slog.Warn("dispatch request journey journal entry rejected", "request_id", snap.RequestID, "journal_seq", entry.Seq, "event_seq", event.Seq, "error", err)
		}
	}
}

func journalSnapshotHash(snap dispatch.JournalSnapshot) [sha256.Size]byte {
	payload, _ := json.Marshal(struct {
		TenantID       string                  `json:"tenant_id"`
		RequestID      string                  `json:"request_id"`
		Entries        []dispatch.JournalEntry `json:"entries"`
		Truncated      bool                    `json:"truncated"`
		TruncatedCount int                     `json:"truncated_count"`
		Version        int64                   `json:"version"`
	}{snap.TenantID, snap.RequestID, snap.Entries, snap.Truncated, snap.TruncatedCount, snap.SnapshotVersion})
	return sha256.Sum256(payload)
}

// journalEntryToJourneyEvent translates one dispatch.JournalEntry into a
// requestjourney.JourneyEvent. The closed EventType / Outcome / Stage
// vocabularies force a fixed mapping: terminal actions → terminal
// event types, continuation actions → retry/switch event types, waits →
// RetryScheduled. Non-terminal RetryReason and SwitchReason carry the
// original journal Action for forensics. Action/Counts/Attempt are
// informational and are dropped — the seq-monotonic invariant is what
// preserves order in the journey store.
func journalEntryToJourneyEvent(instance, tenantID, requestID string, baseSeq int64, offset int, entry dispatch.JournalEntry) (requestjourney.JourneyEvent, bool) {
	var (
		eventType requestjourney.EventType
		stage     requestjourney.JourneyStage
		outcome   requestjourney.Outcome
	)
	switch entry.Action {
	case dispatch.NextActionCompleted:
		eventType, stage, outcome = requestjourney.EventRequestSucceeded, requestjourney.StageTerminal, requestjourney.OutcomeSuccess
	case dispatch.NextActionFailed:
		eventType, stage, outcome = requestjourney.EventRequestFailed, requestjourney.StageTerminal, requestjourney.OutcomeFailure
	case dispatch.NextActionCanceled:
		eventType, stage, outcome = requestjourney.EventRequestCanceled, requestjourney.StageTerminal, requestjourney.OutcomeCanceled
	case dispatch.NextActionRetrySameCred:
		eventType, stage = requestjourney.EventRetryScheduled, requestjourney.StageRetrying
	case dispatch.NextActionSwitchCred:
		eventType, stage = requestjourney.EventNodeSwitched, requestjourney.StageCredentialQueue
	case dispatch.NextActionSwitchModel:
		eventType, stage = requestjourney.EventModelSwitched, requestjourney.StageRouting
	case dispatch.NextActionCapacityWait, dispatch.NextActionScheduledWait:
		eventType, stage = requestjourney.EventRetryScheduled, requestjourney.StageRetrying
	default:
		// Unknown / future action: degrade rather than drop the trace so the
		// forensics path is never silently truncated.
		eventType, stage = requestjourney.EventObservationDegraded, requestjourney.StageUpstream
	}
	occurredAt := entry.At
	if occurredAt.IsZero() {
		occurredAt = entry.At // recordDecision always fills; defensive against future zero-valued entries
	}
	return requestjourney.JourneyEvent{
		TenantID:          tenantID,
		GatewayInstanceID: instance,
		RequestID:         requestID,
		Seq:               baseSeq + int64(offset) + 1,
		Type:              eventType,
		Stage:             stage,
		ResolvedModel:     entry.Model,
		Model:             entry.Model,
		ProviderID:        int64(entry.ProviderID),
		CredentialID:      int64(entry.CredentialID),
		Outcome:           outcome,
		ErrorKind:         entry.ErrorKind,
		HTTPStatus:        entry.HTTPStatus,
		RetryReason:       string(entry.Action),
		ObservationStatus: requestjourney.ObservationComplete,
		OccurredAt:        occurredAt,
	}, true
}
