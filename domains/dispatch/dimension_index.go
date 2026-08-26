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
	// Journal is a DETACHED snapshot of the request's AttemptJournal tail
	// (dimensionJournalTail entries on UpdateWait, the full ring on
	// Complete). The authority lives on the QueuedRequest; three-ring copies
	// may lag one step until Complete aligns them (E9).
	Journal    []JournalEntry
	EnqueuedAt time.Time
	StartedAt  time.Time
	CompletedAt time.Time
	ExpiresAt   time.Time
	// LastUpdated is the last membership mutation time (drives TTL from the
	// terminal transition, not admission).
	LastUpdated time.Time
}

// dimensionJournalTail bounds the per-entry journal snapshot copied on
// UpdateWait (R10: tail 16 + counters; the full ring only lands at Complete).
const dimensionJournalTail = 16

// journalTail returns a detached copy of the last n journal entries.
func journalTail(qr *QueuedRequest, n int) []JournalEntry {
	if n <= 0 || len(qr.AttemptJournal) == 0 {
		return nil
	}
	src := qr.AttemptJournal
	if len(src) > n {
		src = src[len(src)-n:]
	}
	out := make([]JournalEntry, len(src))
	copy(out, src)
	return out
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
	for _, e := range entries {
		e.State = DimensionStateInFlight
		e.StartedAt = now
		e.LastUpdated = now
		e.Attempts = qr.AttemptCount
	}
	node := &DimensionEntry{
		RequestID:    qr.ID,
		TenantID:     qr.TenantID,
		SessionID:    qr.SessionID,
		Model:        qr.ResolvedModel,
		CredentialID: cred.CredentialID,
		ProviderID:   cred.ProviderID,
		Vendor:       cred.Vendor,
		State:        DimensionStateInFlight,
		Attempts:     qr.AttemptCount,
		Class:        qr.requestClass(),
		EnqueuedAt:   now,
		StartedAt:    now,
	}
	ix.insertLocked(node, dimensionKey(DimensionCredential, strconv.Itoa(cred.CredentialID)))
	if cred.ProviderID > 0 {
		ix.insertLocked(&DimensionEntry{
			RequestID:    qr.ID,
			TenantID:     qr.TenantID,
			SessionID:    qr.SessionID,
			Model:        qr.ResolvedModel,
			CredentialID: cred.CredentialID,
			ProviderID:   cred.ProviderID,
			Vendor:       cred.Vendor,
			State:        DimensionStateInFlight,
			Attempts:     qr.AttemptCount,
			Class:        qr.requestClass(),
			EnqueuedAt:   now,
			StartedAt:    now,
		}, dimensionKey(DimensionProvider, strconv.Itoa(cred.ProviderID)))
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
	// Invariant-4 backfill: pipeline.complete records the terminal entry
	// inside its CAS before this call; if the tail is not terminal (direct
	// use, future call sites), record it here so completed entries always
	// end on a terminal journal action. Idempotent — a terminal tail is
	// never extended (invariant 3).
	if n := len(qr.AttemptJournal); n == 0 || !isTerminalAction(qr.AttemptJournal[n-1].Action) {
		terminalKind := out.ErrorKind
		if terminalKind == "" && out.Err != nil {
			terminalKind = classifyError(out.Err)
		}
		qr.recordDecision(JournalEntry{
			Model:        qr.ResolvedModel,
			CredentialID: qr.SelectedCred.CredentialID,
			ProviderID:   qr.SelectedCred.ProviderID,
			Vendor:       qr.SelectedCred.Vendor,
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
	ix.mu.Lock()
	defer ix.mu.Unlock()
	full := journalTail(qr, journalCapacity)
	for _, e := range ix.byReq[qr.ID] {
		e.State = DimensionStateCompleted
		e.Outcome = outcome
		e.ErrorKind = out.ErrorKind
		e.Attempts = qr.AttemptCount
		e.Journal = full
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
// request's entries while it parks back to pending. Each entry receives its
// own detached copy of the journal tail; the TTL window restarts from the
// requeue so a parked-forever request still ages out of the index.
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
		e.Journal = journalTail(qr, dimensionJournalTail)
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
	entry.LastUpdated = time.Now()
	ix.byReq[entry.RequestID] = append(ix.byReq[entry.RequestID], entry)
	ix.tracked++
	metricDimensionTracked.Inc()

	ring, ok := ix.rings[key]
	if !ok {
		if len(ix.rings) >= ix.cfg.MaxKeys {
			return // dimension cardinality bound; request entry stays in byReq
		}
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
	// First pass: drop expired heads (they are the oldest).
	for len(ring.entries) > 0 {
		e := ring.entries[0]
		if !e.ExpiresAt.IsZero() && now.After(e.ExpiresAt) {
			ix.dropEntryLocked(e)
			ring.entries = ring.entries[1:]
			evicted++
			continue
		}
		break
	}
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

// RequestJournalView is the per-request projection behind
// GET /api/admin/dispatch/journal/{request_id} (V6-W1.6 R10): every
// dimension entry of the request plus the freshest journal snapshot. The
// authority stays on the QueuedRequest; once TTL/capacity evicts the last
// entry the view is gone and the endpoint answers 404 (ops falls back to
// the requestjourney persistent projection).
type RequestJournalView struct {
	RequestID string           `json:"request_id"`
	Class     string           `json:"class,omitempty"`
	Entries   []DimensionEntry `json:"entries"`
	Journal   []JournalEntry   `json:"journal,omitempty"`
}

// JournalByRequest serves the per-request journal view from the byReq index.
// The three ring copies may lag one step until Complete aligns them (E9);
// the snapshot with the highest tail Seq wins.
func (ix *DimensionIndex) JournalByRequest(requestID string) (RequestJournalView, bool) {
	if !ix.enabled() || requestID == "" {
		return RequestJournalView{}, false
	}
	ix.Sweep(time.Now())
	ix.mu.Lock()
	defer ix.mu.Unlock()
	entries := ix.byReq[requestID]
	if len(entries) == 0 {
		return RequestJournalView{}, false
	}
	view := RequestJournalView{
		RequestID: requestID,
		Entries:   make([]DimensionEntry, 0, len(entries)),
	}
	for _, e := range entries {
		view.Entries = append(view.Entries, *e)
		if view.Class == "" {
			view.Class = e.Class
		}
		if n := len(e.Journal); n > 0 {
			if m := len(view.Journal); m == 0 || e.Journal[n-1].Seq > view.Journal[m-1].Seq {
				view.Journal = e.Journal
			}
		}
	}
	return view, true
}
