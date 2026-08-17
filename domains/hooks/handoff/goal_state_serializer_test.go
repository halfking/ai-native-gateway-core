package handoff

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/goal" //nolint:depguard // serializer contract test
)

type memoryGoalStore struct {
	mu       sync.Mutex
	sessions map[string]*goal.Session
}

func newMemoryGoalStore() *memoryGoalStore {
	return &memoryGoalStore{sessions: make(map[string]*goal.Session)}
}

func (s *memoryGoalStore) GetSession(_ context.Context, tenantID, id string) (*goal.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if session := s.sessions[id]; session != nil {
		clone := *session
		clone.AuditResult = append(json.RawMessage(nil), session.AuditResult...)
		return &clone, nil
	}
	return nil, nil
}

func (s *memoryGoalStore) CreateSession(_ context.Context, session *goal.Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	clone := *session
	s.sessions[session.SessionID] = &clone
	return nil
}
func (s *memoryGoalStore) UpdateSessionState(_ context.Context, tenantID, id string, state goal.State) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if session := s.sessions[id]; session != nil {
		session.State = state
	}
	return nil
}
func (s *memoryGoalStore) CompareAndSetState(_ context.Context, tenantID, id string, allowedFrom []goal.State, target goal.State) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[id]
	if !ok || session.TenantID != tenantID {
		return false, nil
	}
	for _, st := range allowedFrom {
		if session.State == st {
			session.State = target
			return true, nil
		}
	}
	return false, nil
}
func (s *memoryGoalStore) IncrementAutoContinueCount(context.Context, string, string) error {
	return nil
}
func (s *memoryGoalStore) IncrementDecisionCount(context.Context, string, string) error {
	return nil
}
func (s *memoryGoalStore) UpdateSessionAudit(context.Context, string, string, []byte) (bool, error) {
	return true, nil
}
func (s *memoryGoalStore) AtomicAutoContinue(context.Context, string, string, int) (bool, error) {
	return true, nil
}
func (s *memoryGoalStore) RecordResponse(context.Context, string, string, string, bool) (int, error) {
	return 0, nil
}
func (s *memoryGoalStore) AtomicModelSwitch(context.Context, string, string, string, int) (bool, error) {
	return true, nil
}

func TestMemoryGoalStateSerializer_SerializeAndRestore(t *testing.T) {
	store := newMemoryGoalStore()
	store.sessions["gw_old"] = &goal.Session{
		SessionID: "gw_old", TenantID: "tenant-a", State: goal.StateRetrying,
		OriginalGoal: "finish migration", RetryCount: 2, DecisionCount: 3,
		AutoContinueCount: 4, RepeatCount: 5, ModelSwitchCount: 1,
		CurrentModel: "auto", LastResponseHash: "must-not-transfer", AuditResult: json.RawMessage(`{"ok":true}`),
	}
	capturedAt := time.Date(2026, 8, 16, 2, 0, 0, 0, time.UTC)
	serializer := NewMemoryGoalStateSerializer(store)
	serializer.now = func() time.Time { return capturedAt }

	state, err := serializer.Serialize(context.Background(), GoalStateInput{
		TenantID: "tenant-a", SessionID: "gw_old", CostMode: "balanced", TokensUsed: 85_000,
		MessageCount: 18, CompletedSteps: []string{"schema"}, RemainingWork: "tests",
	})
	if err != nil {
		t.Fatal(err)
	}
	if state.Version != GoalStateVersion || state.RetryCount != 2 || state.RepeatCount != 5 || state.ModelSwitchCount != 1 {
		t.Fatalf("unexpected state: %+v", state)
	}
	if !state.AuditCompleted || state.CapturedAt != capturedAt || state.CostMode != "balanced" {
		t.Fatalf("missing state metadata: %+v", state)
	}
	state.CompletedSteps[0] = "changed"
	if err := serializer.Restore(context.Background(), "gw_new", state); err != nil {
		t.Fatal(err)
	}

	restored, _ := store.GetSession(context.Background(), "tenant-a", "gw_new")
	if restored == nil || restored.State != goal.StateActive || restored.OriginalGoal != "finish migration" {
		t.Fatalf("unexpected restored session: %+v", restored)
	}
	if restored.RetryCount != 0 || restored.AutoContinueCount != 0 || restored.RepeatCount != 0 || restored.ModelSwitchCount != 0 {
		t.Fatalf("runtime budgets must reset in new session: %+v", restored)
	}
	old, _ := store.GetSession(context.Background(), "tenant-a", "gw_old")
	if old.RetryCount != 2 || old.ModelSwitchCount != 1 || old.LastResponseHash != "must-not-transfer" {
		t.Fatalf("source session was modified: %+v", old)
	}
}

func TestMemoryGoalStateSerializer_MissingSessionAndVersion(t *testing.T) {
	store := newMemoryGoalStore()
	serializer := NewMemoryGoalStateSerializer(store)
	state, err := serializer.Serialize(context.Background(), GoalStateInput{SessionID: "missing"})
	if err != nil || state != nil {
		t.Fatalf("missing session must be a compatible no-op, state=%+v err=%v", state, err)
	}
	if err := serializer.Restore(context.Background(), "gw_new", &GoalState{Version: 99, SourceSessionID: "gw_old"}); err == nil {
		t.Fatal("unsupported schema version must fail validation")
	}

	store.sessions["gw_existing"] = &goal.Session{
		SessionID: "gw_existing", TenantID: "other-tenant", State: goal.StateActive, OriginalGoal: "other goal",
	}
	if err := serializer.Restore(context.Background(), "gw_existing", &GoalState{
		Version: GoalStateVersion, SourceSessionID: "gw_old", TenantID: "tenant-a", TaskDescription: "expected goal",
	}); err == nil {
		t.Fatal("conflicting target Goal session must not be accepted as restored")
	}

	store.sessions["gw_consumed"] = &goal.Session{
		SessionID: "gw_consumed", TenantID: "tenant-a", State: goal.StateActive,
		OriginalGoal: "expected goal", RetryCount: 1, CurrentModel: "auto",
	}
	if err := serializer.Restore(context.Background(), "gw_consumed", &GoalState{
		Version: GoalStateVersion, SourceSessionID: "gw_old", TenantID: "tenant-a",
		TaskDescription: "expected goal", CurrentModel: "auto",
	}); err == nil {
		t.Fatal("target Goal session with consumed runtime budget must not be accepted as restored")
	}
}
