package handoff

import (
	"context"
	"testing"
	"time"
)

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

func TestConfirmationStoreAcceptsTargetCreatedWithinProposalSecond(t *testing.T) {
	store := NewMemoryConfirmationStore()
	proposalTime := time.Date(2026, 8, 17, 3, 0, 0, 900_000_000, time.UTC)
	record := &HandoffRecord{SessionKey: "gw_previous", TenantID: "tenant-a", CreatedAt: proposalTime}
	proposal, token, err := NewConfirmationProposal(record, 1, proposalTime.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SavePending(context.Background(), proposal); err != nil {
		t.Fatal(err)
	}
	result, err := store.Confirm(context.Background(), ConfirmationInput{
		ProposalID: proposal.ID, TenantID: "tenant-a", APIKeyID: 1, Token: token,
		NewSessionID: "gw_new", TargetCreatedAt: proposalTime.Truncate(time.Second),
		IdempotencyKey: "idempotency-key-second-precision",
	})
	if err != nil || result == nil || !result.FirstConfirmation {
		t.Fatalf("same-second target confirmation failed: result=%+v err=%v", result, err)
	}
}

func TestConfirmationStoreRejectsExpiryAndPreexistingTarget(t *testing.T) {
	store := NewMemoryConfirmationStore()
	record := &HandoffRecord{SessionKey: "gw_previous", TenantID: "tenant-a", CreatedAt: time.Now()}
	proposal, token, err := NewConfirmationProposal(record, 1, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	proposal.ExpiresAt = time.Now().Add(-time.Second)
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
	if err = store.SavePending(context.Background(), proposal); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Confirm(context.Background(), ConfirmationInput{ProposalID: proposal.ID, TenantID: "tenant-a", APIKeyID: 1, Token: token, NewSessionID: "gw_new", TargetCreatedAt: record.CreatedAt.Add(-time.Second), IdempotencyKey: "idempotency-key-456"}); err != ErrConfirmationInvalid {
		t.Fatalf("old target = %v", err)
	}
}
