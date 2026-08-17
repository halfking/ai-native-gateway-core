package handoff

import (
	"context"
	"errors"
	"testing"
	"time"
)

// secondAnchoredNow returns a UTC time anchored to a whole second, so tests
// can add sub-second offsets without crossing into the next second. All
// expiry fixtures derive from it and never depend on wall-clock dates.
func secondAnchoredNow() (now, secondStart time.Time) {
	now = time.Now().UTC()
	return now, now.Truncate(time.Second)
}

func TestConfirmationProposalStoresOnlyHashAndRotates(t *testing.T) {
	store := NewMemoryConfirmationStore()
	record := &HandoffRecord{SessionKey: "gw_previous", TenantID: "tenant-a", TriggerReason: "manual", CreatedAt: time.Now()}
	first, firstToken, err := NewConfirmationProposal(record, 42, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if first.TokenHash == firstToken || !first.MatchesToken(firstToken) || first.MatchesToken("wrong") {
		t.Fatalf("unexpected token handling: %+v", first)
	}
	if err := store.SavePending(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	second, secondToken, err := NewConfirmationProposal(record, 42, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SavePending(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Confirm(context.Background(), ConfirmationInput{ProposalID: first.ID, TenantID: "tenant-a", APIKeyID: 42, Token: firstToken, NewSessionID: "gw_new", IdempotencyKey: "idempotency-key-123"}); err != ErrConfirmationInvalid {
		t.Fatalf("old proposal = %v, want invalid", err)
	}
	result, err := store.Confirm(context.Background(), ConfirmationInput{ProposalID: second.ID, TenantID: "tenant-a", APIKeyID: 42, Token: secondToken, NewSessionID: "gw_new", IdempotencyKey: "idempotency-key-123"})
	if err != nil || !result.FirstConfirmation {
		t.Fatalf("first confirmation result=%+v err=%v", result, err)
	}
	retry, err := store.Confirm(context.Background(), ConfirmationInput{ProposalID: second.ID, TenantID: "tenant-a", APIKeyID: 42, Token: secondToken, NewSessionID: "gw_new", IdempotencyKey: "idempotency-key-123"})
	if err != nil || retry.FirstConfirmation {
		t.Fatalf("retry result=%+v err=%v", retry, err)
	}
}

func TestConfirmationProposalRejectsNonFutureExpiry(t *testing.T) {
	now, _ := secondAnchoredNow()
	record := &HandoffRecord{SessionKey: "gw_previous", TenantID: "tenant-a", CreatedAt: now}
	for name, expiresAt := range map[string]time.Time{
		"expiry in the past": now.Add(-time.Minute),
		"expiry at now":      now,
	} {
		if _, _, err := NewConfirmationProposal(record, 1, expiresAt); err == nil {
			t.Fatalf("%s: NewConfirmationProposal must reject expiresAt=%v", name, expiresAt)
		}
	}
}

// TestConfirmationStoreAcceptsTargetCreatedWithinProposalSecond pins the
// same-second boundary: a target session that only carries second precision
// still confirms when the proposal record was created later within that same
// second. Anchoring to the current whole second keeps the sub-second offset
// (0.9s) from spilling into the next second, so the fixture is stable no
// matter when the test runs.
func TestConfirmationStoreAcceptsTargetCreatedWithinProposalSecond(t *testing.T) {
	store := NewMemoryConfirmationStore()
	now, secondStart := secondAnchoredNow()
	proposalTime := secondStart.Add(900 * time.Millisecond)
	record := &HandoffRecord{SessionKey: "gw_previous", TenantID: "tenant-a", CreatedAt: proposalTime}
	proposal, token, err := NewConfirmationProposal(record, 1, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SavePending(context.Background(), proposal); err != nil {
		t.Fatal(err)
	}
	if proposalTime.Truncate(time.Second) != secondStart {
		t.Fatalf("fixture crossed a second boundary: proposalTime=%v secondStart=%v", proposalTime, secondStart)
	}
	result, err := store.Confirm(context.Background(), ConfirmationInput{
		ProposalID: proposal.ID, TenantID: "tenant-a", APIKeyID: 1, Token: token,
		NewSessionID: "gw_new", TargetCreatedAt: secondStart,
		IdempotencyKey: "idempotency-key-second-precision",
	})
	if err != nil || result == nil || !result.FirstConfirmation {
		t.Fatalf("same-second target confirmation failed: result=%+v err=%v", result, err)
	}
}

func TestConfirmationStoreRejectsJustExpiredProposalAndStaysExpiredOnRetry(t *testing.T) {
	store := NewMemoryConfirmationStore()
	now, _ := secondAnchoredNow()
	record := &HandoffRecord{SessionKey: "gw_previous", TenantID: "tenant-a", CreatedAt: now}
	proposal, token, err := NewConfirmationProposal(record, 1, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	proposal.ExpiresAt = time.Now().Add(-time.Millisecond)
	if err = store.SavePending(context.Background(), proposal); err != nil {
		t.Fatal(err)
	}
	input := ConfirmationInput{ProposalID: proposal.ID, TenantID: "tenant-a", APIKeyID: 1, Token: token, NewSessionID: "gw_new", IdempotencyKey: "idempotency-key-123"}
	for attempt := 1; attempt <= 2; attempt++ {
		if _, err = store.Confirm(context.Background(), input); !errors.Is(err, ErrConfirmationExpired) {
			t.Fatalf("attempt %d = %v, want ErrConfirmationExpired", attempt, err)
		}
	}
}

func TestConfirmationReplayWithDifferentIdempotencyKeyRejected(t *testing.T) {
	store := NewMemoryConfirmationStore()
	now, _ := secondAnchoredNow()
	record := &HandoffRecord{SessionKey: "gw_previous", TenantID: "tenant-a", CreatedAt: now}
	proposal, token, err := NewConfirmationProposal(record, 1, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SavePending(context.Background(), proposal); err != nil {
		t.Fatal(err)
	}
	confirm := func(idempotencyKey string) (*ConfirmationResult, error) {
		return store.Confirm(context.Background(), ConfirmationInput{ProposalID: proposal.ID, TenantID: "tenant-a", APIKeyID: 1, Token: token, NewSessionID: "gw_new", IdempotencyKey: idempotencyKey})
	}
	if _, err := confirm("idempotency-key-original"); err != nil {
		t.Fatal(err)
	}
	if _, err := confirm("idempotency-key-original"); err != nil {
		t.Fatalf("same-key replay must be idempotent: %v", err)
	}
	if _, err := confirm("idempotency-key-attacker"); !errors.Is(err, ErrConfirmationReplay) {
		t.Fatalf("different-key replay = %v, want ErrConfirmationReplay", err)
	}
}

// TestConfirmationGoalRestoreStateSurvivesAccountingCommit walks the restart
// recovery path: after accounting commits, the restore state machine advances
// independently (attempt -> restored) and stays queryable, terminal
// transitions are safe to re-apply, and replay of the confirmation stays
// idempotent throughout.
func TestConfirmationGoalRestoreStateSurvivesAccountingCommit(t *testing.T) {
	store := NewMemoryConfirmationStore()
	now, _ := secondAnchoredNow()
	record := &HandoffRecord{SessionKey: "gw_previous", TenantID: "tenant-a", CreatedAt: now}
	proposal, token, err := NewConfirmationProposal(record, 1, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	proposal.GoalState = &GoalState{Version: GoalStateVersion, TenantID: "tenant-a", SourceSessionID: "gw_previous", TaskDescription: "resume migration"}
	if err := store.SavePending(context.Background(), proposal); err != nil {
		t.Fatal(err)
	}
	input := ConfirmationInput{ProposalID: proposal.ID, TenantID: "tenant-a", APIKeyID: 1, Token: token, NewSessionID: "gw_new", IdempotencyKey: "idempotency-key-restore"}
	result, err := store.Confirm(context.Background(), input)
	if err != nil || !result.FirstConfirmation {
		t.Fatalf("confirm result=%+v err=%v", result, err)
	}
	state, err := store.GetGoalRestoreState(context.Background(), proposal.ID, "tenant-a")
	if err != nil || state.Status != confirmationStatusAccountingConfirmed || state.NewSessionID != "gw_new" || state.GoalState == nil || state.GoalState.TaskDescription != "resume migration" {
		t.Fatalf("post-confirm restore state=%+v err=%v", state, err)
	}
	if err := store.MarkGoalRestoreAttempt(context.Background(), proposal.ID, "tenant-a", "serializer timeout"); err != nil {
		t.Fatal(err)
	}
	state, err = store.GetGoalRestoreState(context.Background(), proposal.ID, "tenant-a")
	if err != nil || state.Status != confirmationStatusAccountingConfirmed || state.RestoreError != "serializer timeout" || !state.RestoreAttemptedAt.After(time.Time{}) {
		t.Fatalf("attempt state=%+v err=%v", state, err)
	}
	if err := store.MarkGoalRestored(context.Background(), proposal.ID, "tenant-a"); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkGoalRestored(context.Background(), proposal.ID, "tenant-a"); err != nil {
		t.Fatalf("terminal MarkGoalRestored must be safe to retry: %v", err)
	}
	state, err = store.GetGoalRestoreState(context.Background(), proposal.ID, "tenant-a")
	if err != nil || state.Status != confirmationStatusRestored || state.RestoreError != "" || state.RestoredAt.IsZero() {
		t.Fatalf("restored state=%+v err=%v", state, err)
	}
	if replay, err := store.Confirm(context.Background(), input); err != nil || replay.FirstConfirmation {
		t.Fatalf("post-restore replay result=%+v err=%v", replay, err)
	}
}

func TestConfirmationStoreRejectsExpiryAndPreexistingTarget(t *testing.T) {
	store := NewMemoryConfirmationStore()
	now, _ := secondAnchoredNow()
	record := &HandoffRecord{SessionKey: "gw_previous", TenantID: "tenant-a", CreatedAt: now}
	proposal, token, err := NewConfirmationProposal(record, 1, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	proposal.ExpiresAt = now.Add(-time.Second)
	if err = store.SavePending(context.Background(), proposal); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Confirm(context.Background(), ConfirmationInput{ProposalID: proposal.ID, TenantID: "tenant-a", APIKeyID: 1, Token: token, NewSessionID: "gw_new", IdempotencyKey: "idempotency-key-123"}); err != ErrConfirmationExpired {
		t.Fatalf("expired = %v", err)
	}
	proposal, token, err = NewConfirmationProposal(record, 1, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SavePending(context.Background(), proposal); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Confirm(context.Background(), ConfirmationInput{ProposalID: proposal.ID, TenantID: "tenant-a", APIKeyID: 1, Token: token, NewSessionID: "gw_new", TargetCreatedAt: record.CreatedAt.Add(-time.Second), IdempotencyKey: "idempotency-key-456"}); err != ErrConfirmationInvalid {
		t.Fatalf("old target = %v", err)
	}
}
