package handoff

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/goal" //nolint:depguard // integration fixture
)

type goalConfirmationStore struct {
	*memoryStore
	*MemoryConfirmationStore
	saveErr error
}

func (s *goalConfirmationStore) SavePending(ctx context.Context, proposal *ConfirmationProposal) error {
	if s.saveErr != nil {
		return s.saveErr
	}
	return s.MemoryConfirmationStore.SavePending(ctx, proposal)
}

type retryGoalStateSerializer struct {
	delegate GoalStateSerializer
	failures int
	calls    int
}

func (s *retryGoalStateSerializer) Serialize(ctx context.Context, input GoalStateInput) (*GoalState, error) {
	return s.delegate.Serialize(ctx, input)
}

func (s *retryGoalStateSerializer) Restore(ctx context.Context, newSessionID string, state *GoalState) error {
	s.calls++
	if s.calls <= s.failures {
		return errors.New("transient restore failure")
	}
	return s.delegate.Restore(ctx, newSessionID, state)
}

func TestGoalHandoff_SavePendingFailureAbortsReservation(t *testing.T) {
	goalStore := newMemoryGoalStore()
	goalStore.sessions["gw_old"] = &goal.Session{SessionID: "gw_old", TenantID: "tenant-a", State: goal.StateFailed, OriginalGoal: "resume"}
	trigger := NewMemoryHandoffTrigger(time.Minute)
	trigger.Observe("gw_old", TriggerSignal{Kind: SignalGoalFailed, Severity: 4, Reason: "failed"})
	store := &goalConfirmationStore{memoryStore: &memoryStore{}, MemoryConfirmationStore: NewMemoryConfirmationStore(), saveErr: errors.New("db unavailable")}
	hook := NewTriggerHook(TriggerConfig{
		Enabled: true, TriggerMode: TriggerModeAuto, MaxPerSession: 5,
		ContextMonitor: NewMemoryContextMonitor(0, 0), GoalTrigger: trigger,
		GoalStateSerializer: NewMemoryGoalStateSerializer(goalStore), MessageBuilder: NewMemoryHandoffMessageBuilder(0),
	}, store)
	requestResult, err := hook.PrepareRequest(context.Background(), &Request{
		SessionID: "gw_old", TenantID: "tenant-a", Explicit: true, MessageCount: 1,
		Body: []byte(`{"messages":[{"role":"user","content":"continue"}]}`),
	})
	if err != nil || requestResult == nil || requestResult.ReservationID == "" {
		t.Fatalf("expected reserved handoff, result=%+v err=%v", requestResult, err)
	}
	if _, _, err := hook.PrepareConfirmation(context.Background(), requestResult, 42); err == nil {
		t.Fatal("SavePending failure must be returned")
	}
	if _, signal, ok := trigger.Reserve("gw_old", TriggerSignal{}); !ok || signal.Reason != "failed" {
		t.Fatalf("SavePending failure lost Goal signal: signal=%+v ok=%v", signal, ok)
	}
}

func TestGoalHandoff_RestoreFailureIsFailOpenAndTenantScoped(t *testing.T) {
	goalStore := newMemoryGoalStore()
	goalStore.sessions["gw_old"] = &goal.Session{
		SessionID: "gw_old", TenantID: "tenant-a", State: goal.StateActive, OriginalGoal: "complete P0-D",
	}
	trigger := NewMemoryHandoffTrigger(5 * time.Minute)
	serializer := &retryGoalStateSerializer{delegate: NewMemoryGoalStateSerializer(goalStore), failures: 1}
	store := &goalConfirmationStore{memoryStore: &memoryStore{}, MemoryConfirmationStore: NewMemoryConfirmationStore()}
	hook := NewTriggerHook(TriggerConfig{
		Enabled: true, TriggerMode: TriggerModeManual, SkillName: "handoff",
		SummaryEngine: SummaryRule, MaxPerSession: 5,
		GoalStateSerializer: serializer, GoalTrigger: trigger, MessageBuilder: NewMemoryHandoffMessageBuilder(0),
	}, store)
	requestResult, err := hook.PrepareRequest(context.Background(), &Request{
		SessionID: "gw_old", TenantID: "tenant-a", Body: []byte(`{"messages":[{"role":"user","content":"/handoff continue"}]}`), MessageCount: 1,
	})
	if err != nil || requestResult == nil {
		t.Fatalf("prepare failed: result=%+v err=%v", requestResult, err)
	}
	proposal, token, err := hook.PrepareConfirmation(context.Background(), requestResult, 42)
	if err != nil {
		t.Fatal(err)
	}
	input := ConfirmationInput{
		ProposalID: proposal.ID, TenantID: "tenant-a", APIKeyID: 42, Token: token,
		NewSessionID: "gw_new", TargetCreatedAt: time.Now().UTC(), IdempotencyKey: "0123456789abcdef",
	}
	first, err := hook.ConfirmRequest(context.Background(), input)
	if err != nil || first == nil || !first.FirstConfirmation {
		t.Fatalf("confirmation must remain successful after fail-open restore: result=%+v err=%v", first, err)
	}
	if trigger.Peek(proposal.ID, "tenant-a") == nil || trigger.Peek(proposal.ID, "tenant-b") != nil {
		t.Fatal("failed restore must retain tenant-scoped state")
	}
	if serializer.calls != 1 || trigger.IsAcknowledged(proposal.ID, "tenant-a") {
		t.Fatalf("failed restore must remain retryable in memory: calls=%d", serializer.calls)
	}

	second, err := hook.ConfirmRequest(context.Background(), input)
	if err != nil || second == nil || second.FirstConfirmation {
		t.Fatalf("idempotent retry did not restore: result=%+v err=%v", second, err)
	}
	if serializer.calls != 2 || !trigger.IsAcknowledged(proposal.ID, "tenant-a") {
		t.Fatalf("restore was not acknowledged after retry: calls=%d", serializer.calls)
	}
	restored, _ := goalStore.GetSession(context.Background(), "gw_new")
	if restored == nil || restored.TenantID != "tenant-a" {
		t.Fatalf("Goal state not restored on retry: %+v", restored)
	}

	wrongTenant := input
	wrongTenant.TenantID = "tenant-b"
	if _, err := hook.ConfirmRequest(context.Background(), wrongTenant); !errors.Is(err, ErrConfirmationInvalid) && !strings.Contains(errString(err), "tenant") {
		t.Fatalf("cross-tenant confirmation was not rejected: %v", err)
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func TestGoalHandoff_RequestConfirmRestoresNewSession(t *testing.T) {
	goalStore := newMemoryGoalStore()
	goalStore.sessions["gw_old"] = &goal.Session{
		SessionID: "gw_old", TenantID: "tenant-a", State: goal.StateActive,
		OriginalGoal: "complete P0-D", RetryCount: 2, AutoContinueCount: 3,
		RepeatCount: 2, ModelSwitchCount: 1, CurrentModel: "auto",
	}
	trigger := NewMemoryHandoffTrigger(5 * time.Minute)
	store := &goalConfirmationStore{memoryStore: &memoryStore{}, MemoryConfirmationStore: NewMemoryConfirmationStore()}
	hook := NewTriggerHook(TriggerConfig{
		Enabled: true, TriggerMode: TriggerModeManual, SkillName: "handoff",
		SummaryEngine: SummaryRule, MaxPerSession: 5,
		GoalStateSerializer: NewMemoryGoalStateSerializer(goalStore),
		GoalTrigger:         trigger, MessageBuilder: NewMemoryHandoffMessageBuilder(0),
	}, store)

	requestResult, err := hook.PrepareRequest(context.Background(), &Request{
		SessionID: "gw_old", TenantID: "tenant-a", ClientModel: "auto",
		Body:         []byte(`{"messages":[{"role":"user","content":"/handoff continue P0-D"}]}`),
		MessageCount: 1,
	})
	if err != nil || requestResult == nil || requestResult.ResumePacket.GoalHandoff == nil {
		t.Fatalf("expected Goal handoff packet, result=%+v err=%v", requestResult, err)
	}
	if requestResult.ResumePacket.GoalHandoff.GoalState.RetryCount != 2 {
		t.Fatalf("Goal history missing from packet: %+v", requestResult.ResumePacket.GoalHandoff.GoalState)
	}

	proposal, token, err := hook.PrepareConfirmation(context.Background(), requestResult, 42)
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := hook.ConfirmRequest(context.Background(), ConfirmationInput{
		ProposalID: proposal.ID, TenantID: "tenant-a", APIKeyID: 42, Token: token,
		NewSessionID: "gw_new", TargetCreatedAt: time.Now().UTC(), IdempotencyKey: "0123456789abcdef",
	})
	if err != nil || confirmed == nil || !confirmed.FirstConfirmation {
		t.Fatalf("confirmation failed: result=%+v err=%v", confirmed, err)
	}
	restored, _ := goalStore.GetSession(context.Background(), "gw_new")
	if restored == nil || restored.State != goal.StateActive || restored.OriginalGoal != "complete P0-D" {
		t.Fatalf("Goal state was not restored: %+v", restored)
	}
	if restored.RetryCount != 0 || restored.AutoContinueCount != 0 || restored.RepeatCount != 0 || restored.ModelSwitchCount != 0 {
		t.Fatalf("new session inherited exhausted runtime budgets: %+v", restored)
	}

	replayed, err := hook.ConfirmRequest(context.Background(), ConfirmationInput{
		ProposalID: proposal.ID, TenantID: "tenant-a", APIKeyID: 42, Token: token,
		NewSessionID: "gw_new", IdempotencyKey: "0123456789abcdef",
	})
	if err != nil || replayed == nil || replayed.FirstConfirmation {
		t.Fatalf("idempotent replay changed confirmation semantics: result=%+v err=%v", replayed, err)
	}
}
