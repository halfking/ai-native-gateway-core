package dispatch

import (
	"sync"
	"time"
)

// Request lifecycle registry (会话优化 v4 R1.1 / T2).
//
// The LifecycleRegistry is the "total request queue" bookkeeping entity: every
// request that enters the pipeline is registered here and tracked through the
// three-state lifecycle mandated by R1.1:
//
//	pending   — admitted, waiting for execution (a timed retry with retry_at
//	            also parks in pending, carrying its RetryAt)
//	in_flight — dequeued for execution (upstream attempt running or re-enqueued
//	            by the immediate failover ladder)
//	completed — terminal (success or terminal failure); retained until evicted
//
// Capacity layering (v1.4, see config.go for the full ladder):
//   - registry total capacity: DefaultRegistryCapacity (1000) — NOT a
//     rejection threshold; when full the OLDEST completed entry is dropped
//   - completed soft watermark: DefaultCompletedWatermark (50) — completed
//     entries are evicted FIFO by CompletedAt once the count exceeds it
//   - unfinished limit: pending+in_flight entries are NEVER evicted; once
//     they reach the unfinished limit (bound to Config.MaxQueueDepth, 300)
//     RegisterPending refuses admission and the caller surfaces an immediate
//     overflow error (R1.8)
//
// All operations are mutex-guarded map/bookkeeping work with no I/O: the
// registry is a non-blocking observation bypass. Losing or dropping registry
// entries must never change a request's execution outcome (UT-DQ-07).

// LifecycleState is the closed three-state request lifecycle vocabulary.
// The JSON/stage mapping to journey stages is documented in the v4 spec §6:
// received/routing/model_queue/credential_queue → pending;
// upstream/streaming/retrying → in_flight (retrying with retry_at → pending);
// terminal → completed.
type LifecycleState string

const (
	// LifecyclePending is the waiting-for-execution state (incl. timed retry).
	LifecyclePending LifecycleState = "pending"
	// LifecycleInFlight is the executing state.
	LifecycleInFlight LifecycleState = "in_flight"
	// LifecycleCompleted is the terminal state (kept until evicted).
	LifecycleCompleted LifecycleState = "completed"
)

// Valid reports whether the state belongs to the closed vocabulary.
func (s LifecycleState) Valid() bool {
	return s == LifecyclePending || s == LifecycleInFlight || s == LifecycleCompleted
}

// Registry defaults and hotconfig keys (all live-reloadable via Config).
const (
	// DefaultRegistryCapacity is the total entry bound of the request
	// registry. It is a memory guard, NOT a rejection threshold (R1.1).
	DefaultRegistryCapacity = 1000
	// DefaultCompletedWatermark is the completed-entry soft watermark: once
	// exceeded, oldest-completed entries are evicted FIFO by CompletedAt
	// (R1.8). Only completed entries are ever evicted.
	DefaultCompletedWatermark = 50

	// HotKeyRegistryCapacity mirrors Config.RegistryCapacity.
	HotKeyRegistryCapacity = "llmgw_dispatch_registry_capacity"
	// HotKeyCompletedWatermark mirrors Config.CompletedWatermark.
	HotKeyCompletedWatermark = "llmgw_dispatch_completed_watermark"
)

// RegistryEntry is one request's registry record. All timestamps are wall
// clock stamps taken at the pipeline transition sites.
type RegistryEntry struct {
	// RequestID is the request identity (QueuedRequest.ID).
	RequestID string
	// State is the current lifecycle state.
	State LifecycleState
	// RetryAt is set while the entry is parked as a timed retry (pending).
	RetryAt *time.Time
	// EnqueuedAt is the pending admission time (T1-ish).
	EnqueuedAt time.Time
	// StartedAt is the first in-flight transition time.
	StartedAt *time.Time
	// CompletedAt is the terminal transition time; drives FIFO eviction.
	CompletedAt *time.Time
	// RequestedModel is the client-requested model (may be "auto").
	RequestedModel string
	// ResolvedModel is the resolved routable model, when known.
	ResolvedModel string
}

// RegistrySnapshot is a point-in-size detached view of the registry.
type RegistrySnapshot struct {
	// Total is the number of registered entries (all states).
	Total int
	// Unfinished is pending + in_flight (never evicted).
	Unfinished int
	// Pending, InFlight, Completed are per-state counts.
	Pending   int
	InFlight  int
	Completed int
	// Entries are the filtered entries (detached copies).
	Entries []RegistryEntry
}

// LifecycleRegistry tracks request lifecycle states in process memory.
// Construct via NewLifecycleRegistry; all methods are safe for concurrent use.
type LifecycleRegistry struct {
	mu sync.Mutex

	capacity           int // total entry bound; full → evict oldest completed
	completedWatermark int // evict oldest completed beyond this count
	unfinishedLimit    int // refuse RegisterPending at this unfinished count; 0 = unlimited

	entries         map[string]*RegistryEntry
	completedOrder  []string // request IDs in completion (CompletedAt) order
	unfinishedCount int

	// evictHook, when set, receives the request IDs evicted by watermark or
	// capacity pressure. Invoked OUTSIDE the registry mutex.
	evictHook func(evicted []RegistryEntry)
}

// NewLifecycleRegistry builds a registry. Non-positive capacity/watermark
// fall back to the documented defaults; unfinishedLimit <= 0 disables the
// admission refusal (still bounded by capacity's completed eviction).
func NewLifecycleRegistry(capacity, completedWatermark, unfinishedLimit int) *LifecycleRegistry {
	if capacity <= 0 {
		capacity = DefaultRegistryCapacity
	}
	if completedWatermark < 0 {
		completedWatermark = DefaultCompletedWatermark
	}
	return &LifecycleRegistry{
		capacity:           capacity,
		completedWatermark: completedWatermark,
		unfinishedLimit:    unfinishedLimit,
		entries:            make(map[string]*RegistryEntry),
	}
}

// SetEvictHook installs the eviction observer (journey/action events). The
// hook must not call back into the registry.
func (r *LifecycleRegistry) SetEvictHook(hook func(evicted []RegistryEntry)) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.evictHook = hook
	r.mu.Unlock()
}

// UpdateLimits swaps the hot-reloadable knobs. In-flight entries are never
// dropped by limit changes.
func (r *LifecycleRegistry) UpdateLimits(capacity, completedWatermark, unfinishedLimit int) []RegistryEntry {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	r.capacity = capacity
	r.completedWatermark = completedWatermark
	r.unfinishedLimit = unfinishedLimit
	evicted := r.evictLocked()
	r.mu.Unlock()
	r.notifyEvicted(evicted)
	return evicted
}

// RegisterPending admits a request into the registry. It returns false ONLY
// when the unfinished limit is reached (caller must surface an immediate
// overflow error, R1.8) — capacity pressure evicts completed entries instead
// of rejecting. Re-registering an ID that is already unfinished is always
// allowed (it consumes no additional slot); reviving a completed ID counts
// as a fresh admission and respects the limit.
func (r *LifecycleRegistry) RegisterPending(requestID, requestedModel string, now time.Time) bool {
	if r == nil || requestID == "" {
		return true // bookkeeping bypass; never block execution
	}
	r.mu.Lock()
	if existing, ok := r.entries[requestID]; ok && existing.State != LifecycleCompleted {
		r.resetToPendingLocked(existing, now)
		r.mu.Unlock()
		return true
	}
	if r.unfinishedLimit > 0 && r.unfinishedCount >= r.unfinishedLimit {
		r.mu.Unlock()
		return false
	}
	if existing, ok := r.entries[requestID]; ok {
		// Completed revival: leaves the completed FIFO and re-admits as
		// unfinished (count incremented by resetToPendingLocked).
		r.resetToPendingLocked(existing, now)
	} else {
		r.entries[requestID] = &RegistryEntry{
			RequestID:      requestID,
			State:          LifecyclePending,
			EnqueuedAt:     now,
			RequestedModel: requestedModel,
		}
		r.unfinishedCount++
	}
	evicted := r.evictLocked()
	r.mu.Unlock()
	r.notifyEvicted(evicted)
	return true
}

// MarkInFlight transitions a pending entry to in_flight. Unknown IDs and
// completed entries are ignored (the registry is a best-effort projection).
func (r *LifecycleRegistry) MarkInFlight(requestID string, now time.Time) {
	if r == nil || requestID == "" {
		return
	}
	r.mu.Lock()
	entry, ok := r.entries[requestID]
	if !ok || entry.State == LifecycleCompleted {
		r.mu.Unlock()
		return
	}
	entry.State = LifecycleInFlight
	entry.RetryAt = nil
	if entry.StartedAt == nil {
		started := now
		entry.StartedAt = &started
	}
	r.mu.Unlock()
}

// MarkCompleted transitions an entry to its terminal state. Idempotent;
// completed entries stay until watermark/capacity eviction.
func (r *LifecycleRegistry) MarkCompleted(requestID string, now time.Time) {
	if r == nil || requestID == "" {
		return
	}
	r.mu.Lock()
	entry, ok := r.entries[requestID]
	if !ok || entry.State == LifecycleCompleted {
		r.mu.Unlock()
		return
	}
	entry.State = LifecycleCompleted
	entry.RetryAt = nil
	completed := now
	entry.CompletedAt = &completed
	r.unfinishedCount--
	if r.unfinishedCount < 0 {
		r.unfinishedCount = 0
	}
	r.completedOrder = append(r.completedOrder, requestID)
	evicted := r.evictLocked()
	r.mu.Unlock()
	r.notifyEvicted(evicted)
}

// MarkRetryScheduled parks a non-terminal entry back into pending with the
// given retry_at (R1.1: a retrying request carrying retry_at is pending).
func (r *LifecycleRegistry) MarkRetryScheduled(requestID string, retryAt time.Time) {
	if r == nil || requestID == "" {
		return
	}
	r.mu.Lock()
	entry, ok := r.entries[requestID]
	if !ok {
		r.mu.Unlock()
		return
	}
	if entry.State == LifecycleCompleted {
		// Defensive revival (spec: "completed 转 pending"): remove from the
		// completed FIFO and re-admit as an unfinished pending entry.
		r.removeCompletedLocked(requestID)
		r.unfinishedCount++
	}
	entry.State = LifecyclePending
	at := retryAt
	entry.RetryAt = &at
	r.mu.Unlock()
}

// Get returns a detached copy of one entry.
func (r *LifecycleRegistry) Get(requestID string) (RegistryEntry, bool) {
	if r == nil || requestID == "" {
		return RegistryEntry{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.entries[requestID]
	if !ok {
		return RegistryEntry{}, false
	}
	return *entry, true
}

// Snapshot returns per-state counts plus the entries filtered by states.
// An empty filter returns every entry.
func (r *LifecycleRegistry) Snapshot(states ...LifecycleState) RegistrySnapshot {
	if r == nil {
		return RegistrySnapshot{Entries: []RegistryEntry{}}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	snapshot := RegistrySnapshot{Entries: make([]RegistryEntry, 0, len(r.entries))}
	filter := make(map[LifecycleState]struct{}, len(states))
	for _, s := range states {
		filter[s] = struct{}{}
	}
	for _, entry := range r.entries {
		switch entry.State {
		case LifecyclePending:
			snapshot.Pending++
		case LifecycleInFlight:
			snapshot.InFlight++
		case LifecycleCompleted:
			snapshot.Completed++
		}
		if len(filter) > 0 {
			if _, keep := filter[entry.State]; !keep {
				continue
			}
		}
		snapshot.Entries = append(snapshot.Entries, *entry)
	}
	snapshot.Total = len(r.entries)
	snapshot.Unfinished = snapshot.Pending + snapshot.InFlight
	return snapshot
}

// UnfinishedCount reports pending + in-flight entries.
func (r *LifecycleRegistry) UnfinishedCount() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.unfinishedCount
}

// resetToPendingLocked re-admits an existing entry as pending. A completed
// entry revived this way leaves the completed FIFO and counts as unfinished
// again; an in-flight entry simply parks back to pending.
func (r *LifecycleRegistry) resetToPendingLocked(entry *RegistryEntry, now time.Time) {
	if entry.State == LifecycleCompleted {
		r.removeCompletedLocked(entry.RequestID)
		r.unfinishedCount++
	}
	entry.State = LifecyclePending
	entry.RetryAt = nil
	entry.EnqueuedAt = now
}

// removeCompletedLocked drops one ID from the completed FIFO bookkeeping.
func (r *LifecycleRegistry) removeCompletedLocked(requestID string) {
	for i, id := range r.completedOrder {
		if id == requestID {
			r.completedOrder = append(r.completedOrder[:i], r.completedOrder[i+1:]...)
			return
		}
	}
}

// evictLocked enforces the completed watermark and total capacity. Only
// completed entries are ever evicted (R1.8); pending/in-flight/retrying
// entries are retained until their terminal state or client cancel.
func (r *LifecycleRegistry) evictLocked() []RegistryEntry {
	var evicted []RegistryEntry
	drop := func() bool {
		for len(r.completedOrder) > 0 {
			id := r.completedOrder[0]
			r.completedOrder = r.completedOrder[1:]
			entry, ok := r.entries[id]
			if !ok {
				continue
			}
			delete(r.entries, id)
			evicted = append(evicted, *entry)
			return true
		}
		return false
	}
	// Soft watermark: completed entries beyond the watermark go FIFO.
	if r.completedWatermark >= 0 {
		for len(r.completedOrder) > r.completedWatermark {
			if !drop() {
				break
			}
		}
	}
	// Hard capacity: total entries beyond capacity evict oldest completed.
	// If no completed entries remain the registry overflows its soft bound
	// rather than rejecting — admission refusal is exclusively the
	// unfinished limit's job (UT-DQ-07).
	for len(r.entries) > r.capacity {
		if !drop() {
			break
		}
	}
	return evicted
}

func (r *LifecycleRegistry) notifyEvicted(evicted []RegistryEntry) {
	if len(evicted) == 0 {
		return
	}
	r.mu.Lock()
	hook := r.evictHook
	r.mu.Unlock()
	if hook != nil {
		func() {
			defer func() { _ = recover() }()
			hook(evicted)
		}()
	}
}
