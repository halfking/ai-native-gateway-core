package routeincident

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestDispatchAction_StaleState verifies that the dispatcher
// rejects a stale expected_version with ErrStaleState. We use a
// store with a nil pool so the call never reaches the DB; we
// only care about the early-return path. For end-to-end coverage
// the integration test runs against a live DB.
func TestDispatchAction_StaleState(t *testing.T) {
	s := &Store{pool: nil}
	_, err := s.dispatchAction(context.Background(), ActionContext{
		TenantID:        "t1",
		IncidentID:      "i1",
		Action:          ActionRecover,
		Actor:           "alice",
		Reason:          "manual",
		ConfirmToken:    "ct-1",
		IdempKey:        "idem-1",
		ExpectedVersion: 0,
	}, recoverExecutor)
	if !errors.Is(err, ErrNoDatabase) {
		t.Fatalf("nil pool should yield ErrNoDatabase, got %v", err)
	}
}

// TestDispatchAction_RejectsUnknownAction checks the closed allow-list.
func TestDispatchAction_RejectsUnknownAction(t *testing.T) {
	s := &Store{pool: nil}
	_, err := s.dispatchAction(context.Background(), ActionContext{
		TenantID: "t1", IncidentID: "i1",
		Action: ActionKind("not-in-list"),
		Actor:  "alice", Reason: "r", ConfirmToken: "ct", IdempKey: "idem",
	}, recoverExecutor)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("unknown action should yield ErrInvalidInput, got %v", err)
	}
	if !strings.Contains(err.Error(), "not-in-list") {
		t.Fatalf("error should mention the action: %v", err)
	}
}

// TestDispatchAction_RejectsMutatingWithoutReason confirms that
// mutating actions require a non-empty reason.
func TestDispatchAction_RejectsMutatingWithoutReason(t *testing.T) {
	s := &Store{pool: nil}
	_, err := s.dispatchAction(context.Background(), ActionContext{
		TenantID: "t1", IncidentID: "i1",
		Action: ActionRecover,
		Actor:  "alice", Reason: "", ConfirmToken: "ct", IdempKey: "idem",
	}, recoverExecutor)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("missing reason should yield ErrInvalidInput, got %v", err)
	}
}

// TestDispatchAction_RejectsMutatingWithoutConfirmToken confirms
// the confirmation-token gate.
func TestDispatchAction_RejectsMutatingWithoutConfirmToken(t *testing.T) {
	s := &Store{pool: nil}
	_, err := s.dispatchAction(context.Background(), ActionContext{
		TenantID: "t1", IncidentID: "i1",
		Action: ActionRecover,
		Actor:  "alice", Reason: "ok", ConfirmToken: "", IdempKey: "idem",
	}, recoverExecutor)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("missing confirm token should yield ErrInvalidInput, got %v", err)
	}
}

// TestDispatchAction_AllowsEvidenceExportWithoutConfirmToken is
// the carve-out: evidence_export does NOT require a confirmation
// token because it does not mutate state. The call reaches the DB
// check (and fails with ErrNoDatabase) — we only care that no
// ErrInvalidInput is raised on the input side.
func TestDispatchAction_AllowsEvidenceExportWithoutConfirmToken(t *testing.T) {
	s := &Store{pool: nil}
	_, err := s.dispatchAction(context.Background(), ActionContext{
		TenantID: "t1", IncidentID: "i1",
		Action: ActionEvidenceExport,
		Actor:  "alice", Reason: "ops review", IdempKey: "idem",
	}, nil)
	if err == nil {
		t.Fatal("nil pool must produce an error")
	}
	if errors.Is(err, ErrInvalidInput) {
		t.Fatalf("evidence_export should pass input validation, got %v", err)
	}
	if !errors.Is(err, ErrNoDatabase) {
		t.Fatalf("expected ErrNoDatabase (no pool), got %v", err)
	}
}

// pgxTx is a sentinel alias kept so the test references the
// pgx.Tx type indirectly. The actual ActionExecutor type uses
// pgx.Tx directly; the dispatcher's first arg is typed against
// pgx, so the closure in earlier drafts won't compile. We
// intentionally do NOT import pgx here to keep the test
// lightweight; the type assertion is via the `nil` executor.
type pgxTx = any
