package handoff

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/goal" //nolint:depguard // implements Goal outcome observation
)

// HandoffTrigger accepts observed signals, reserves them while a proposal is
// prepared, and retains Goal state until restoration is acknowledged.
type HandoffTrigger interface {
	Observe(sessionID string, signal TriggerSignal)
	Reserve(sessionID string, contextSignal TriggerSignal) (string, TriggerSignal, bool)
	Commit(reservationID string)
	Abort(reservationID string)
	Bind(proposalID string, state *GoalState)
	Peek(proposalID, tenantID string) *GoalState
	Ack(proposalID, tenantID string)
	IsAcknowledged(proposalID, tenantID string) bool
}

const defaultTriggerEntryLimit = 10_000

type triggerLease struct {
	signal    TriggerSignal
	expiresAt time.Time
}

type pendingSignal struct {
	signal    TriggerSignal
	expiresAt time.Time
}

type triggerReservation struct {
	id        string
	sessionID string
	signal    TriggerSignal
	expiresAt time.Time
}

type proposalState struct {
	tenantID  string
	state     *GoalState
	expiresAt time.Time
}

// MemoryHandoffTrigger is a concurrency-safe in-memory trigger and proposal
// binder. Durable cooldown and limits remain the HandoffStore's responsibility.
type MemoryHandoffTrigger struct {
	mu             sync.Mutex
	ttl            time.Duration
	limit          int
	now            func() time.Time
	goalStore      goal.GoalStore
	pending        map[string]pendingSignal
	leases         map[string]triggerLease
	reservations   map[string]triggerReservation
	reservedBySess map[string]string
	proposals      map[string]proposalState
	acknowledged   map[string]proposalState
}

// NewMemoryHandoffTrigger creates a concurrency-safe trigger. A non-positive
// ttl uses the confirmation TTL; an optional GoalStore filters Goal outcomes.
func NewMemoryHandoffTrigger(ttl time.Duration, stores ...goal.GoalStore) *MemoryHandoffTrigger {
	if ttl <= 0 {
		ttl = defaultConfirmationTTL
	}
	var goalStore goal.GoalStore
	if len(stores) > 0 {
		goalStore = stores[0]
	}
	return &MemoryHandoffTrigger{
		ttl:            ttl,
		limit:          defaultTriggerEntryLimit,
		now:            time.Now,
		goalStore:      goalStore,
		pending:        make(map[string]pendingSignal),
		leases:         make(map[string]triggerLease),
		reservations:   make(map[string]triggerReservation),
		reservedBySess: make(map[string]string),
		proposals:      make(map[string]proposalState),
		acknowledged:   make(map[string]proposalState),
	}
}

func (t *MemoryHandoffTrigger) Observe(sessionID string, signal TriggerSignal) {
	if t == nil || sessionID == "" || !signalEligible(signal) {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	t.cleanupLocked(now)
	t.mergePendingLocked(sessionID, signal, now.Add(t.ttl))
	t.enforceLimitLocked()
}

// ObserveGoalOutcome adapts the Goal observer contract into TriggerSignal.
func (t *MemoryHandoffTrigger) ObserveGoalOutcome(ctx context.Context, outcome goal.Outcome) error {
	if t == nil {
		return nil
	}
	if t.goalStore != nil {
		session, err := t.goalStore.GetSession(ctx, outcome.SessionID)
		if err != nil || session == nil {
			return err
		}
		if outcome.TenantID != "" && session.TenantID != outcome.TenantID {
			return fmt.Errorf("goal outcome tenant mismatch")
		}
		if outcome.Kind == goal.OutcomeFailed {
			if err := t.goalStore.UpdateSessionState(ctx, outcome.SessionID, goal.StateFailed); err != nil {
				return fmt.Errorf("persist failed goal outcome: %w", err)
			}
		}
	}
	if outcome.ObservedAt.IsZero() {
		outcome.ObservedAt = t.now().UTC()
	}
	kind := SignalGoalDegraded
	severity := 3
	switch outcome.Kind {
	case goal.OutcomeCompleted:
		kind, severity = SignalGoalCompleted, 1
	case goal.OutcomeFailed:
		kind, severity = SignalGoalFailed, 4
	}
	t.Observe(outcome.SessionID, TriggerSignal{
		Kind:       kind,
		Source:     nonEmpty(outcome.Source, "goal"),
		Reason:     outcome.Reason,
		Severity:   severity,
		ObservedAt: outcome.ObservedAt,
	})
	return nil
}

// Reserve selects a signal without consuming it permanently. Commit starts the
// debounce lease; Abort makes the signal immediately eligible again.
func (t *MemoryHandoffTrigger) Reserve(sessionID string, contextSignal TriggerSignal) (string, TriggerSignal, bool) {
	if t == nil || sessionID == "" {
		return "", TriggerSignal{}, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	t.cleanupLocked(now)
	if _, exists := t.reservedBySess[sessionID]; exists {
		return "", TriggerSignal{}, false
	}
	selected := contextSignal
	if pending, ok := t.pending[sessionID]; ok && pending.signal.Severity > selected.Severity {
		selected = pending.signal
	}
	if !signalEligible(selected) {
		return "", TriggerSignal{}, false
	}
	if lease, ok := t.leases[sessionID]; ok && now.Before(lease.expiresAt) && lease.signal.Severity >= selected.Severity {
		return "", TriggerSignal{}, false
	}
	reservationID := uuid.NewString()
	t.reservations[reservationID] = triggerReservation{
		id: reservationID, sessionID: sessionID, signal: selected, expiresAt: now.Add(t.ttl),
	}
	t.reservedBySess[sessionID] = reservationID
	delete(t.pending, sessionID)
	t.enforceLimitLocked()
	return reservationID, selected, true
}

func (t *MemoryHandoffTrigger) Commit(reservationID string) {
	if t == nil || reservationID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	t.cleanupLocked(now)
	reservation, ok := t.reservations[reservationID]
	if !ok {
		return
	}
	delete(t.reservations, reservationID)
	delete(t.reservedBySess, reservation.sessionID)
	t.leases[reservation.sessionID] = triggerLease{signal: reservation.signal, expiresAt: now.Add(t.ttl)}
	t.enforceLimitLocked()
}

func (t *MemoryHandoffTrigger) Abort(reservationID string) {
	if t == nil || reservationID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	t.cleanupLocked(now)
	reservation, ok := t.reservations[reservationID]
	if !ok {
		return
	}
	delete(t.reservations, reservationID)
	delete(t.reservedBySess, reservation.sessionID)
	t.mergePendingLocked(reservation.sessionID, reservation.signal, now.Add(t.ttl))
	t.enforceLimitLocked()
}

func (t *MemoryHandoffTrigger) Bind(proposalID string, state *GoalState) {
	if t == nil || proposalID == "" || state == nil || state.TenantID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	t.cleanupLocked(now)
	t.proposals[proposalID] = proposalState{
		tenantID: state.TenantID, state: cloneGoalState(state), expiresAt: now.Add(t.ttl),
	}
	t.enforceLimitLocked()
}

func (t *MemoryHandoffTrigger) Peek(proposalID, tenantID string) *GoalState {
	if t == nil || proposalID == "" || tenantID == "" {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	t.cleanupLocked(now)
	proposal, ok := t.proposals[proposalID]
	if !ok || proposal.tenantID != tenantID {
		return nil
	}
	return cloneGoalState(proposal.state)
}

func (t *MemoryHandoffTrigger) Ack(proposalID, tenantID string) {
	if t == nil || proposalID == "" || tenantID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	proposal, ok := t.proposals[proposalID]
	if ok && proposal.tenantID == tenantID {
		delete(t.proposals, proposalID)
		proposal.state = nil
		proposal.expiresAt = t.now().Add(t.ttl)
		t.acknowledged[proposalID] = proposal
		t.enforceLimitLocked()
	}
}

func (t *MemoryHandoffTrigger) IsAcknowledged(proposalID, tenantID string) bool {
	if t == nil || proposalID == "" || tenantID == "" {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cleanupLocked(t.now())
	proposal, ok := t.acknowledged[proposalID]
	return ok && proposal.tenantID == tenantID
}

func (t *MemoryHandoffTrigger) mergePendingLocked(sessionID string, signal TriggerSignal, expiresAt time.Time) {
	if current, ok := t.pending[sessionID]; !ok || signal.Severity > current.signal.Severity {
		t.pending[sessionID] = pendingSignal{signal: signal, expiresAt: expiresAt}
	}
}

func (t *MemoryHandoffTrigger) cleanupLocked(now time.Time) {
	for sessionID, pending := range t.pending {
		if !now.Before(pending.expiresAt) {
			delete(t.pending, sessionID)
		}
	}
	for sessionID, lease := range t.leases {
		if !now.Before(lease.expiresAt) {
			delete(t.leases, sessionID)
		}
	}
	for id, reservation := range t.reservations {
		if !now.Before(reservation.expiresAt) {
			delete(t.reservations, id)
			delete(t.reservedBySess, reservation.sessionID)
		}
	}
	for proposalID, proposal := range t.proposals {
		if !now.Before(proposal.expiresAt) {
			delete(t.proposals, proposalID)
		}
	}
	for proposalID, proposal := range t.acknowledged {
		if !now.Before(proposal.expiresAt) {
			delete(t.acknowledged, proposalID)
		}
	}
}

func (t *MemoryHandoffTrigger) enforceLimitLocked() {
	for t.entryCountLocked() > t.limit {
		if t.evictOldestLeaseLocked() || t.evictOldestPendingLocked() || t.evictOldestReservationLocked() || t.evictOldestAcknowledgedLocked() || t.evictOldestProposalLocked() {
			continue
		}
		return
	}
}

func (t *MemoryHandoffTrigger) entryCountLocked() int {
	return len(t.pending) + len(t.leases) + len(t.reservations) + len(t.proposals) + len(t.acknowledged)
}

func (t *MemoryHandoffTrigger) evictOldestLeaseLocked() bool {
	var key string
	var oldest time.Time
	for candidate, value := range t.leases {
		if key == "" || value.expiresAt.Before(oldest) {
			key, oldest = candidate, value.expiresAt
		}
	}
	if key != "" {
		delete(t.leases, key)
		return true
	}
	return false
}

func (t *MemoryHandoffTrigger) evictOldestPendingLocked() bool {
	var key string
	var oldest time.Time
	for candidate, value := range t.pending {
		if key == "" || value.expiresAt.Before(oldest) {
			key, oldest = candidate, value.expiresAt
		}
	}
	if key != "" {
		delete(t.pending, key)
		return true
	}
	return false
}

func (t *MemoryHandoffTrigger) evictOldestReservationLocked() bool {
	var key string
	var oldest time.Time
	for candidate, value := range t.reservations {
		if key == "" || value.expiresAt.Before(oldest) {
			key, oldest = candidate, value.expiresAt
		}
	}
	if key != "" {
		reservation := t.reservations[key]
		delete(t.reservations, key)
		delete(t.reservedBySess, reservation.sessionID)
		return true
	}
	return false
}

func (t *MemoryHandoffTrigger) evictOldestAcknowledgedLocked() bool {
	var key string
	var oldest time.Time
	for candidate, value := range t.acknowledged {
		if key == "" || value.expiresAt.Before(oldest) {
			key, oldest = candidate, value.expiresAt
		}
	}
	if key != "" {
		delete(t.acknowledged, key)
		return true
	}
	return false
}

func (t *MemoryHandoffTrigger) evictOldestProposalLocked() bool {
	var key string
	var oldest time.Time
	for candidate, value := range t.proposals {
		if key == "" || value.expiresAt.Before(oldest) {
			key, oldest = candidate, value.expiresAt
		}
	}
	if key != "" {
		delete(t.proposals, key)
		return true
	}
	return false
}

func signalEligible(signal TriggerSignal) bool {
	return signal.Kind == SignalContextPressure || signal.Kind == SignalGoalFailed || (signal.Kind == SignalGoalDegraded && signal.Reason != "model_switched")
}

func cloneGoalState(state *GoalState) *GoalState {
	if state == nil {
		return nil
	}
	clone := *state
	clone.CompletedSteps = append([]string(nil), state.CompletedSteps...)
	return &clone
}
