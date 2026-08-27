package requestarchive

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// RequestState is the lifecycle phase of a request owned by this process. The
// terminal value deliberately echoes the archive stage names so a registry
// listing can be correlated with on-disk recovery without a translation
// table.
type RequestState string

const (
	// StateInFlight covers the phase between registration and a built fact.
	StateInFlight RequestState = "in_flight"
	// StateTerminal means the fact is built; only database acknowledgement
	// and archive cleanup remain.
	StateTerminal RequestState = "terminal"
)

// PersistOutcome is the caller's report of what happened when the fact was
// handed to the database. The registry never talks to the database, so this is
// bookkeeping for humans and recovery tooling, not a driver state.
type PersistOutcome string

const (
	PersistOutcomePending PersistOutcome = "pending"
	PersistOutcomeSuccess PersistOutcome = "success"
	PersistOutcomeFailure PersistOutcome = "failure"
)

var (
	// ErrMissingRequestID guards every entry point that needs an id.
	ErrMissingRequestID = errors.New("requestarchive: request id required")
	// ErrInvalidRequestID rejects ids that cannot serve as a filename inside
	// a stage directory.
	ErrInvalidRequestID = errors.New("requestarchive: request id not usable as archive filename")
	// ErrRequestRegistered keeps a live id owned by exactly one entry.
	ErrRequestRegistered = errors.New("requestarchive: request already registered")
	// ErrRequestUnknown turns out-of-order transitions into loud failures
	// instead of silent inserts.
	ErrRequestUnknown = errors.New("requestarchive: request not registered")
)

// ActiveRequest is a value snapshot of one in-flight request. Every field is
// an immutable value type so a stored entry can never alias a running
// request's mutable state — in particular no IR pointers are accepted.
type ActiveRequest struct {
	RequestID      string
	TenantID       string
	SessionID      string
	State          RequestState
	Endpoint       string
	PersistOutcome PersistOutcome
	RegisteredAt   time.Time
	UpdatedAt      time.Time
}

// ActiveRequestRegistry indexes the requests this process currently owns.
//
// Lock strategy: a single sync.RWMutex rather than shards, because every
// critical section is a short map operation with no I/O and no callbacks, so
// writers cannot starve the read-heavy Get/List paths that observability is
// expected to poll; sharding would only add reordering complexity at the
// in-flight volumes one mutex handles comfortably.
type ActiveRequestRegistry struct {
	mu      sync.RWMutex
	entries map[string]ActiveRequest
}

// NewActiveRequestRegistry returns an empty registry.
func NewActiveRequestRegistry() *ActiveRequestRegistry {
	return &ActiveRequestRegistry{entries: make(map[string]ActiveRequest)}
}

// Register installs the initial record for a request. State and
// PersistOutcome default to the start of the lifecycle so a caller cannot
// accidentally register a request as already persisted. Duplicate
// registration is rejected: a live id must have exactly one owner, otherwise
// Remove-based cleanup would racily delete a successor's entry.
func (r *ActiveRequestRegistry) Register(entry ActiveRequest) error {
	if entry.RequestID == "" {
		return ErrMissingRequestID
	}
	if entry.State == "" {
		entry.State = StateInFlight
	}
	if entry.PersistOutcome == "" {
		entry.PersistOutcome = PersistOutcomePending
	}
	now := time.Now()
	if entry.RegisteredAt.IsZero() {
		entry.RegisteredAt = now
	}
	entry.UpdatedAt = now

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.entries[entry.RequestID]; exists {
		return fmt.Errorf("%w: %s", ErrRequestRegistered, entry.RequestID)
	}
	r.entries[entry.RequestID] = entry
	return nil
}

// UpdateState advances the lifecycle phase. An unknown id is an error rather
// than a silent insert: a transition for an unregistered request means the
// caller lost track of ordering and that bug should surface immediately.
func (r *ActiveRequestRegistry) UpdateState(requestID string, state RequestState) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.entries[requestID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrRequestUnknown, requestID)
	}
	entry.State = state
	entry.UpdatedAt = time.Now()
	r.entries[requestID] = entry
	return nil
}

// MarkPersistOutcome records the caller's database persistence result. The
// archive package never issues the persist itself, so this is the only place
// the outcome becomes visible to recovery tooling.
func (r *ActiveRequestRegistry) MarkPersistOutcome(requestID string, outcome PersistOutcome) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.entries[requestID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrRequestUnknown, requestID)
	}
	entry.PersistOutcome = outcome
	entry.UpdatedAt = time.Now()
	r.entries[requestID] = entry
	return nil
}

// Get returns a copy of the current record. Value semantics guarantee the
// caller cannot mutate registry state through the result.
func (r *ActiveRequestRegistry) Get(requestID string) (ActiveRequest, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.entries[requestID]
	return entry, ok
}

// List returns every entry sorted by request id so snapshots, diffs, and
// tests observe a deterministic order regardless of map iteration.
func (r *ActiveRequestRegistry) List() []ActiveRequest {
	r.mu.RLock()
	entries := make([]ActiveRequest, 0, len(r.entries))
	for _, entry := range r.entries {
		entries = append(entries, entry)
	}
	r.mu.RUnlock()
	sort.Slice(entries, func(i, j int) bool { return entries[i].RequestID < entries[j].RequestID })
	return entries
}

// Remove drops the record once the request is fully archived and confirmed.
// The boolean reports whether a record existed, letting callers detect double
// removal without treating it as a hard error.
func (r *ActiveRequestRegistry) Remove(requestID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.entries[requestID]
	delete(r.entries, requestID)
	return ok
}
