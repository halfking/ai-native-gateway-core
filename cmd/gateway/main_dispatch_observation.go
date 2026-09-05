package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"sync"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/dispatch"
	"github.com/kaixuan/llm-gateway-go/domains/requestjourney"
	"github.com/kaixuan/llm-gateway-go/metrics"
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
	hash      [sha256.Size]byte
	createdAt time.Time
}

const journalSnapshotReceiptCapacity = 10000

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

	lifecycleMu    sync.Mutex
	closed         bool
	cleanupStarted bool
	cleanupTicker  *time.Ticker
	stopCleanup    chan struct{}
	cleanupDone    chan struct{}
	applyWG        sync.WaitGroup
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
	adapter := &dispatchJourneyJournalAdapter{
		recorder:    recorder,
		instance:    instanceID,
		store:       store,
		receipt:     receipt,
		receipts:    make(map[journalSnapshotReceiptKey]journalSnapshotReceipt),
		stopCleanup: make(chan struct{}),
	}
	adapter.startCleanup(24 * time.Hour)
	return adapter
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

	// Admission is coordinated with Close so no new delivery starts after the
	// adapter has been shut down, while Close waits for deliveries already in
	// flight to finish.
	a.lifecycleMu.Lock()
	if a.closed {
		a.lifecycleMu.Unlock()
		return
	}
	a.applyWG.Add(1)
	a.lifecycleMu.Unlock()
	defer a.applyWG.Done()
	if snap.CallerTenantID == "" || snap.CallerTenantID != snap.TenantID {
		slog.Warn("dispatch journal sink rejected caller tenant", "request_id", snap.RequestID)
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
	baseSeq := a.recorder.MaxSeq(snap.TenantID, snap.RequestID)
	if a.receipt != nil {
		hash, err := requestjourney.SnapshotPayloadHash(snap.TenantID, snap.RequestID, snap.Entries, snap.Truncated, snap.TruncatedCount, snap.SnapshotVersion)
		if err != nil {
			slog.Warn("dispatch journal snapshot hash failed", "request_id", snap.RequestID, "error", err)
			return
		}
		claim, err = a.receipt.ClaimWithProjectionBase(ctx, snap.TenantID, snap.RequestID, snap.SnapshotVersion, hash, baseSeq)
		if err != nil || claim.AlreadyCompleted || !claim.Claimed {
			if err != nil {
				slog.Warn("dispatch journal snapshot receipt claim failed", "request_id", snap.RequestID, "snapshot_version", snap.SnapshotVersion, "error", err)
			}
			// Record deduplication metrics (2026-08-29)
			if claim.AlreadyCompleted {
				metrics.Global().RecordJournalSnapshotDeduplicated(snap.TenantID, "already_completed")
			} else if !claim.Claimed {
				metrics.Global().RecordJournalSnapshotDeduplicated(snap.TenantID, "not_claimed")
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
				// Record version conflict deduplication (2026-08-29)
				metrics.Global().RecordJournalSnapshotDeduplicated(snap.TenantID, "version_conflict")
			} else {
				// Same hash, already processed - record deduplication (2026-08-29)
				metrics.Global().RecordJournalSnapshotDeduplicated(snap.TenantID, "already_completed")
			}
			return
		}
		a.rememberReceiptLocked(key, journalSnapshotReceipt{hash: hash, createdAt: time.Now()})
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
		// Record snapshot stored metric (2026-08-29)
		metrics.Global().RecordJournalSnapshotStored(snap.TenantID)
	}
	baseSeq = claim.ProjectionBaseSeq
	if a.receipt == nil {
		baseSeq = a.recorder.MaxSeq(snap.TenantID, snap.RequestID)
	}
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
	// Record snapshot apply metric once per snapshot (2026-08-29)
	metrics.Global().RecordJournalSnapshotApplied(snap.TenantID, !failed)
}

func (a *dispatchJourneyJournalAdapter) rememberReceiptLocked(key journalSnapshotReceiptKey, receipt journalSnapshotReceipt) {
	if a.receipts == nil {
		a.receipts = make(map[journalSnapshotReceiptKey]journalSnapshotReceipt)
	}
	a.receipts[key] = receipt
	if len(a.receipts) <= journalSnapshotReceiptCapacity {
		return
	}

	var oldestKey journalSnapshotReceiptKey
	var oldestAt time.Time
	foundOldest := false
	for candidateKey, candidate := range a.receipts {
		if candidateKey == key {
			continue
		}
		if !foundOldest || candidate.createdAt.Before(oldestAt) {
			oldestKey = candidateKey
			oldestAt = candidate.createdAt
			foundOldest = true
		}
	}
	if foundOldest {
		delete(a.receipts, oldestKey)
	}
}

func journalSnapshotHash(snap dispatch.JournalSnapshot) [sha256.Size]byte {
	hash, err := requestjourney.SnapshotPayloadHash(snap.TenantID, snap.RequestID, snap.Entries, snap.Truncated, snap.TruncatedCount, snap.SnapshotVersion)
	if err != nil {
		return [sha256.Size]byte{}
	}
	var result [sha256.Size]byte
	decoded, err := hex.DecodeString(hash)
	if err != nil || len(decoded) != len(result) {
		return result
	}
	copy(result[:], decoded)
	return result
}

// journalEntryToJourneyEvent translates one dispatch.JournalEntry into a
// requestjourney.JourneyEvent. The closed EventType / Outcome / Stage
// vocabularies force a fixed mapping: terminal actions → terminal
// event types, continuation actions → retry/switch event types, waits →
// RetryScheduled. Non-terminal RetryReason and SwitchReason carry the
// original journal Action for forensics. Action/Counts/Attempt are
// informational and are dropped — the seq-monotonic invariant is what
// preserves order in the journey store.
//
// 2026-09-01 P0 fix: switch events now populate From*/To* fields from the
// journal entry so EventNodeSwitched / EventModelSwitched pass journey contract
// validation. Missing from/to fields will cause the event to degrade to
// ObservationDegraded rather than silently dropping the trace.
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
		// P0 fix: validate that from/to credential fields are present
		if entry.FromCredentialID <= 0 || (entry.ToCredentialID <= 0 && entry.CredentialID <= 0) {
			// Degrade rather than drop: emit observation_degraded so the trace
			// isn't silently truncated, with a reason explaining the gap.
			eventType, stage = requestjourney.EventObservationDegraded, requestjourney.StageUpstream
		} else {
			eventType, stage = requestjourney.EventNodeSwitched, requestjourney.StageCredentialQueue
		}
	case dispatch.NextActionSwitchModel:
		// P0 fix: validate that from/to model fields are present
		if entry.FromModel == "" || (entry.ToModel == "" && entry.Model == "") {
			// Degrade rather than drop
			eventType, stage = requestjourney.EventObservationDegraded, requestjourney.StageUpstream
		} else {
			eventType, stage = requestjourney.EventModelSwitched, requestjourney.StageRouting
		}
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
	
	// Build base event
	event := requestjourney.JourneyEvent{
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
	}
	
	// When degraded due to missing switch fields, mark observation as degraded
	if eventType == requestjourney.EventObservationDegraded {
		event.ObservationStatus = requestjourney.ObservationDegraded
	}
	
	// P0 fix: populate from/to fields for switch events
	if entry.Action == dispatch.NextActionSwitchCred && eventType == requestjourney.EventNodeSwitched {
		event.FromCredentialID = int64(entry.FromCredentialID)
		event.ToCredentialID = int64(entry.ToCredentialID)
		if event.ToCredentialID == 0 {
			event.ToCredentialID = int64(entry.CredentialID)
		}
		// Note: FromProviderID/ToProviderID are tracked in journal for forensics
		// but not required by journey contract, so we don't populate them here.
	}
	
	if entry.Action == dispatch.NextActionSwitchModel && eventType == requestjourney.EventModelSwitched {
		event.FromModel = entry.FromModel
		event.ToModel = entry.ToModel
		if event.ToModel == "" {
			event.ToModel = entry.Model
		}
	}
	
	// When degraded due to missing switch fields, add context to RetryReason
	if eventType == requestjourney.EventObservationDegraded && 
	   (entry.Action == dispatch.NextActionSwitchCred || entry.Action == dispatch.NextActionSwitchModel) {
		event.RetryReason = string(entry.Action) + "_missing_endpoints"
	}
	
	return event, true
}

// startCleanup initiates a background goroutine that periodically cleans up
// old receipts from the in-memory map based on the provided TTL.
func (a *dispatchJourneyJournalAdapter) startCleanup(ttl time.Duration) {
	if a == nil {
		return
	}
	a.lifecycleMu.Lock()
	if a.closed || a.cleanupStarted {
		a.lifecycleMu.Unlock()
		return
	}
	if a.stopCleanup == nil {
		a.stopCleanup = make(chan struct{})
	}
	ticker := time.NewTicker(1 * time.Hour)
	a.cleanupTicker = ticker
	a.cleanupDone = make(chan struct{})
	a.cleanupStarted = true
	stopCleanup := a.stopCleanup
	cleanupDone := a.cleanupDone
	a.lifecycleMu.Unlock()

	go func() {
		defer close(cleanupDone)
		for {
			select {
			case <-ticker.C:
				a.cleanupOldReceipts(ttl)
			case <-stopCleanup:
				return
			}
		}
	}()
}

// cleanupOldReceipts removes receipts that are older than the specified TTL.
func (a *dispatchJourneyJournalAdapter) cleanupOldReceipts(ttl time.Duration) {
	a.mu.Lock()
	defer a.mu.Unlock()

	cutoff := time.Now().Add(-ttl)
	for key, receipt := range a.receipts {
		if receipt.createdAt.Before(cutoff) {
			delete(a.receipts, key)
		}
	}
}

// Close stops the cleanup goroutine and releases resources. It is safe to call
// repeatedly and concurrently; after the first call, new snapshot deliveries
// are ignored.
func (a *dispatchJourneyJournalAdapter) Close() error {
	if a == nil {
		return nil
	}

	a.lifecycleMu.Lock()
	if a.closed {
		cleanupDone := a.cleanupDone
		a.lifecycleMu.Unlock()
		if cleanupDone != nil {
			<-cleanupDone
		}
		return nil
	}
	a.closed = true
	ticker := a.cleanupTicker
	stopCleanup := a.stopCleanup
	cleanupDone := a.cleanupDone
	a.lifecycleMu.Unlock()

	if ticker != nil {
		ticker.Stop()
	}
	if stopCleanup != nil {
		close(stopCleanup)
	}
	a.applyWG.Wait()
	if cleanupDone != nil {
		<-cleanupDone
	}
	return nil
}
