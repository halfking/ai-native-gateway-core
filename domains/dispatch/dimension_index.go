package dispatch

import (
	"strconv"
	"sync"
	"time"
)

// DimensionIndex (分维队列, v6 G-Ⅳ / docs/架构优化v6/08-dispatch-executor-loop.md
// §R6) is a request-membership index keyed by execution dimension —
// model / credential / provider. It is NOT an execution queue: it never gates
// scheduling. It answers "which requests recently ran (or are still waiting)
// on this credential/model/provider" for ops and audit.
//
// Membership contract (user requirement): a completed request is NOT removed
// from its dimension queues — entries leave only via TTL expiry or per-ring
// capacity eviction. State transitions mutate the shared entry in place.

// DimensionKind enumerates the membership dimensions.
type DimensionKind string

const (
	DimensionModel      DimensionKind = "model"
	DimensionCredential DimensionKind = "credential"
	DimensionProvider   DimensionKind = "provider"
)

// DimensionEntryState mirrors the request lifecycle within the index.
type DimensionEntryState string

const (
	DimensionStatePending   DimensionEntryState = "pending"
	DimensionStateInFlight  DimensionEntryState = "in_flight"
	DimensionStateCompleted DimensionEntryState = "completed"
)

// DimensionEntry is one request's membership record inside a dimension ring.
// Entries are shared per request across dimensions via the same pointer, so
// Complete updates every dimension at once.
type DimensionEntry struct {
	RequestID    string
	TenantID     string
	SessionID    string
	Model        string
	CredentialID int
	ProviderID   int
	Vendor       string
	State        DimensionEntryState
	Outcome      string // success|failure|canceled (terminal only)
	ErrorKind    string
	RetryAt      time.Time
	LastAction   NextActionKind
	Attempts     int
	// Class is the request class snapshot (immediate|scheduled, V6-W1.6 R10)
	// stamped at Track/MarkNode from qr.requestClass().
	Class string
	// NOTE (V6-W1.6 scope correction, 2026-08-27): entries deliberately do
	// NOT carry the AttemptJournal. The execution trace is attached to the
	// REQUEST (QueuedRequest.AttemptJournal → JournalSnapshot()/journey
	// projection), never replicated into this process-wide index. Dimension
	// entries keep membership metadata only (state/outcome/last action/…).
	EnqueuedAt  time.Time
	StartedAt   time.Time
	CompletedAt time.Time
	ExpiresAt   time.Time
	// LastUpdated is the last membership mutation time (drives TTL from the
	// terminal transition, not admission).
	LastUpdated time.Time
}

// DimensionIndexConfig bounds the index memory.
type DimensionIndexConfig struct {
	// TTL is how long an entry survives after its last update (default
	// 15m). Hotconfig: llmgw_dispatch_dimension_ttl_seconds.
	TTL time.Duration
	// PerKeyCapacity is the ring capacity per dimension key (default 128).
	// 0 disables tracking entirely.
	PerKeyCapacity int
	// MaxKeys bounds the number of distinct dimension keys (default 2048).
	MaxKeys int
}

// Shared defaults (mirrored by Config.DimensionTTLSeconds / DimensionCapacity
// and their llmgw_dispatch_dimension_* hotconfig keys).
const (
	DefaultDimensionTTLSeconds = 900
	DefaultDimensionCapacity   = 128
	defaultDimensionMaxKeys    = 2048
)

// DefaultDimensionIndexConfig returns the production defaults.
func DefaultDimensionIndexConfig() DimensionIndexConfig {
	return DimensionIndexConfig{
		TTL:            DefaultDimensionTTLSeconds * time.Second,
		PerKeyCapacity: DefaultDimensionCapacity,
		MaxKeys:        defaultDimensionMaxKeys,
	}
}

type dimensionRing struct {
	entries []*DimensionEntry // ring semantics: append, evict oldest on overflow
}

// DimensionIndex is safe for concurrent use. All hot-path methods are
// O(1)-ish pointer/bookkeeping operations guarded by one mutex; snapshots
// copy under the same lock.
type DimensionIndex struct {
	mu      sync.Mutex
	cfg     DimensionIndexConfig
	rings   map[string]*dimensionRing // key: "<kind>:<id>"
	byReq   map[string][]*DimensionEntry
	tracked uint64
	evicted uint64
}

// NewDimensionIndex builds an index. A zero PerKeyCapacity yields a disabled
// no-op index (all methods safe).
func NewDimensionIndex(cfg DimensionIndexConfig) *DimensionIndex {
	if cfg.TTL <= 0 {
		cfg.TTL = DefaultDimensionIndexConfig().TTL
	}
	if cfg.MaxKeys <= 0 {
		cfg.MaxKeys = defaultDimensionMaxKeys
	}
	return &DimensionIndex{
		cfg:   cfg,
		rings: make(map[string]*dimensionRing),
		byReq: make(map[string][]*DimensionEntry),
	}
}

func dimensionKey(kind DimensionKind, id string) string {
	return string(kind) + ":" + id
}

func (ix *DimensionIndex) enabled() bool {
	return ix != nil && ix.cfg.PerKeyCapacity > 0
}

// Track registers a request under its model dimension at admission (state
// pending). Later MarkNode/Complete/UpdateWait mutate the same entry set.
func (ix *DimensionIndex) Track(qr *QueuedRequest, now time.Time) {
	if !ix.enabled() || qr == nil {
		return
	}
	entry := &DimensionEntry{
		RequestID:   qr.ID,
		TenantID:    qr.TenantID,
		SessionID:   qr.SessionID,
		Model:       queueKeyFor(qr.RequestedModel),
		State:       DimensionStatePending,
		Attempts:    qr.AttemptCount,
		Class:       qr.requestClass(),
		EnqueuedAt:  now,
		LastUpdated: now,
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.insertLocked(entry, dimensionKey(DimensionModel, entry.Model))
}

// MarkNode registers the request under the credential and provider
// dimensions and flips it to in_flight. Called when the request is enqueued
// into a Tier-2 credential lane.
func (ix *DimensionIndex) MarkNode(qr *QueuedRequest, cred CredentialRef, now time.Time) {
	if !ix.enabled() || qr == nil {
		return
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	entries := ix.byReq[qr.ID]
	credentialKey := dimensionKey(DimensionCredential, strconv.Itoa(cred.CredentialID))
	providerKey := ""
	if cred.ProviderID > 0 {
		providerKey = dimensionKey(DimensionProvider, strconv.Itoa(cred.ProviderID))
	}
	keys := []string{credentialKey}
	if providerKey != "" {
		keys = append(keys, providerKey)
	}
	missing := 0
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		if _, ok := ix.rings[key]; !ok {
			missing++
		}
	}
	if len(ix.rings)+missing > ix.cfg.MaxKeys {
		return
	}
	if len(entries) == 0 && len(ix.byReq) >= ix.cfg.MaxKeys*4 {
		return
	}
	for _, e := range entries {
		e.State = DimensionStateInFlight
		e.StartedAt = now
		e.LastUpdated = now
		e.Attempts = qr.AttemptCount
	}
	node := &DimensionEntry{
		RequestID: qr.ID, TenantID: qr.TenantID, SessionID: qr.SessionID,
		Model: qr.ResolvedModel, CredentialID: cred.CredentialID, ProviderID: cred.ProviderID,
		Vendor: cred.Vendor, State: DimensionStateInFlight, Attempts: qr.AttemptCount,
		Class: qr.requestClass(), EnqueuedAt: now, StartedAt: now,
	}
	ix.insertLocked(node, credentialKey)
	if providerKey != "" {
		providerEntry := *node
		ix.insertLocked(&providerEntry, providerKey)
	}
}

// Complete marks every entry of the request terminal. Entries REMAIN in
// their dimension rings (TTL/capacity evict them) — the user-required
// "完成后不移除" semantics. The full journal (terminal entry included) is
// copied onto every ring so the three snapshots align (invariant 4).
func (ix *DimensionIndex) Complete(qr *QueuedRequest, out ForwardOutcome, now time.Time) {
	if !ix.enabled() || qr == nil {
		return
	}
	// Defensive terminal backfill on the REQUEST's journal (request-attached,
	// V6-W1.6 R9): pipeline.complete normally records the terminal entry
	// inside its CAS before this call; this covers direct-use call sites so
	// the trace always ends on a terminal action. Idempotent — a terminal
	// tail is never extended (invariant 3). Nothing is copied into the
	// dimension entries (scope correction: trace follows the request).
	if n := len(qr.AttemptJournal); n == 0 || !isTerminalAction(qr.AttemptJournal[n-1].Action) {
		terminalKind := out.ErrorKind
		if terminalKind == "" && out.Err != nil {
			terminalKind = classifyError(out.Err)
		}
		cred := qr.selectedCredential()
		qr.recordDecision(JournalEntry{
			Model:        qr.ResolvedModel,
			CredentialID: cred.CredentialID,
			ProviderID:   cred.ProviderID,
			Vendor:       cred.Vendor,
			Action:       terminalActionOf(out),
			ErrorKind:    terminalKind,
			HTTPStatus:   out.HTTPStatus,
			Attempt:      qr.AttemptCount,
		})
	}
	outcome := "success"
	if out.Err != nil {
		outcome = "failure"
	}
	terminalKind := out.ErrorKind
	if terminalKind == "" && out.Err != nil {
		terminalKind = classifyError(out.Err)
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	for _, e := range ix.byReq[qr.ID] {
		e.State = DimensionStateCompleted
		e.Outcome = outcome
		e.ErrorKind = terminalKind
		e.Attempts = qr.AttemptCount
		e.CompletedAt = now
		e.LastUpdated = now
		e.ExpiresAt = now.Add(ix.cfg.TTL)
	}
}

// isTerminalAction reports whether the journal action is one of the three
// terminal kinds (after which no entry may follow, invariant 3).
func isTerminalAction(a NextActionKind) bool {
	switch a {
	case NextActionCompleted, NextActionFailed, NextActionCanceled:
		return true
	}
	return false
}

// UpdateWait stamps requeue metadata (retry_at, last action) onto the
// request's entries while it parks back to pending. The TTL window restarts
// from the requeue so a parked-forever request still ages out of the index.
func (ix *DimensionIndex) UpdateWait(qr *QueuedRequest, retryAt time.Time, action NextActionKind, now time.Time) {
	if !ix.enabled() || qr == nil {
		return
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	for _, e := range ix.byReq[qr.ID] {
		e.State = DimensionStatePending
		e.RetryAt = retryAt
		e.LastAction = action
		e.Attempts = qr.AttemptCount
		e.LastUpdated = now
		e.ExpiresAt = now.Add(ix.cfg.TTL)
	}
}

// insertLocked appends the entry to the request's entry set and the target
// dimension ring, evicting expired/oldest entries on overflow.
func (ix *DimensionIndex) insertLocked(entry *DimensionEntry, key string) {
	if len(ix.byReq) >= ix.cfg.MaxKeys*4 && len(ix.byReq[entry.RequestID]) == 0 {
		// Bound request-level bookkeeping; dimension rings stay the
		// authoritative view. Drop tracking for brand-new requests only.
		return
	}
	if _, exists := ix.rings[key]; !exists && len(ix.rings) >= ix.cfg.MaxKeys {
		// Do not retain a request-only reference when its dimension cannot be
		// indexed: Complete/UpdateWait operate through byReq, so an orphan here
		// would retain the request until unrelated eviction without appearing in
		// any admin-visible ring.
		return
	}
	entry.LastUpdated = time.Now()
	ix.byReq[entry.RequestID] = append(ix.byReq[entry.RequestID], entry)
	ix.tracked++
	metricDimensionTracked.Inc()

	ring := ix.rings[key]
	if ring == nil {
		ring = &dimensionRing{}
		ix.rings[key] = ring
	}
	ring.entries = append(ring.entries, entry)
	ix.trimRingLocked(key, ring)
}

// trimRingLocked enforces the per-ring capacity and TTL floor.
func (ix *DimensionIndex) trimRingLocked(key string, ring *dimensionRing) {
	now := time.Now()
	limit := ix.cfg.PerKeyCapacity
	evicted := 0
	kept := ring.entries[:0]
	for _, e := range ring.entries {
		if !e.ExpiresAt.IsZero() && now.After(e.ExpiresAt) {
			ix.dropEntryLocked(e)
			evicted++
			continue
		}
		kept = append(kept, e)
	}
	ring.entries = kept
	for len(ring.entries) > limit {
		ix.dropEntryLocked(ring.entries[0])
		ring.entries = ring.entries[1:]
		evicted++
	}
	if evicted > 0 {
		ix.evicted += uint64(evicted)
		metricDimensionEvicted.Add(float64(evicted))
	}
	if len(ring.entries) == 0 {
		delete(ix.rings, key)
	}
}

// dropEntryLocked removes the request-level reference for the entry's last
// remaining dimension copy. Entries appear in up to 3 rings; byReq holds each
// pointer once per insert, so removal mirrors the insert count.
func (ix *DimensionIndex) dropEntryLocked(entry *DimensionEntry) {
	list := ix.byReq[entry.RequestID]
	for i, e := range list {
		if e == entry {
			ix.byReq[entry.RequestID] = append(list[:i], list[i+1:]...)
			break
		}
	}
	if len(ix.byReq[entry.RequestID]) == 0 {
		delete(ix.byReq, entry.RequestID)
	}
}

// Sweep removes entries whose ExpiresAt passed. Returns the evicted count.
// Called opportunistically by Snapshot and safe to call from a ticker.
func (ix *DimensionIndex) Sweep(now time.Time) int {
	if !ix.enabled() {
		return 0
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	evicted := 0
	for key, ring := range ix.rings {
		for len(ring.entries) > 0 {
			e := ring.entries[0]
			if e.ExpiresAt.IsZero() || !now.After(e.ExpiresAt) {
				break
			}
			ix.dropEntryLocked(e)
			ring.entries = ring.entries[1:]
			evicted++
		}
		if len(ring.entries) == 0 {
			delete(ix.rings, key)
		}
	}
	if evicted > 0 {
		ix.evicted += uint64(evicted)
		metricDimensionEvicted.Add(float64(evicted))
	}
	return evicted
}

// DimensionSnapshot is a copied view for admin APIs.
type DimensionSnapshot struct {
	Kind         DimensionKind
	ID           string
	Entries      []DimensionEntry
	Total        int
	EvictedTotal uint64
}

// Snapshot returns up to limit entries for the dimension, newest first.
// An implicit Sweep keeps TTL honest on read paths.
func (ix *DimensionIndex) Snapshot(kind DimensionKind, id string, limit int) DimensionSnapshot {
	if !ix.enabled() {
		return DimensionSnapshot{Kind: kind, ID: id}
	}
	ix.Sweep(time.Now())
	ix.mu.Lock()
	defer ix.mu.Unlock()
	ring := ix.rings[dimensionKey(kind, id)]
	snap := DimensionSnapshot{Kind: kind, ID: id, EvictedTotal: ix.evicted}
	if ring == nil {
		return snap
	}
	snap.Total = len(ring.entries)
	if limit <= 0 {
		return snap
	}
	if limit > len(ring.entries) {
		limit = len(ring.entries)
	}
	snap.Entries = make([]DimensionEntry, 0, limit)
	for i := len(ring.entries) - 1; i >= 0 && len(snap.Entries) < limit; i-- {
		snap.Entries = append(snap.Entries, *ring.entries[i])
	}
	return snap
}

// Dimensions lists the tracked dimension keys with live entry counts.
func (ix *DimensionIndex) Dimensions() map[DimensionKind]map[string]int {
	out := map[DimensionKind]map[string]int{
		DimensionModel:      {},
		DimensionCredential: {},
		DimensionProvider:   {},
	}
	if !ix.enabled() {
		return out
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	for key, ring := range ix.rings {
		for _, kind := range []DimensionKind{DimensionModel, DimensionCredential, DimensionProvider} {
			prefix := string(kind) + ":"
			if len(key) > len(prefix) && key[:len(prefix)] == prefix {
				out[kind][key[len(prefix):]] = len(ring.entries)
				break
			}
		}
	}
	return out
}

// Stats reports index bookkeeping counters.
func (ix *DimensionIndex) Stats() (tracked, evicted uint64, requests int) {
	if !ix.enabled() {
		return 0, 0, 0
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	return ix.tracked, ix.evicted, len(ix.byReq)
}

// EntriesByRequest returns detached copies of the request's dimension
// membership entries (model/credential/provider). Membership metadata only —
// the execution trace is NOT served here (scope correction 2026-08-27: the
// AttemptJournal is attached to the request itself; post-hoc path queries go
// through the request's own journey projection, not this process index).
func (ix *DimensionIndex) EntriesByRequest(requestID string) ([]DimensionEntry, bool) {
	if !ix.enabled() || requestID == "" {
		return nil, false
	}
	ix.Sweep(time.Now())
	ix.mu.Lock()
	defer ix.mu.Unlock()
	entries := ix.byReq[requestID]
	if len(entries) == 0 {
		return nil, false
	}
	out := make([]DimensionEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, *e)
	}
	return out, true
}
