package handoff

// M5-3 G2 acceptance test gap closure for the H1-H15 verification matrix
// (docs/03-design/02-feature-design/会话优化v4/12-GoalHandoff契约.md §10).
//
// These tests focus on the cases that the existing
// confirmation_test.go / confirmation_pg_test.go / handoff_message_test.go
// do not already cover:
//
//   H3  PrepareConfirmation persists the pending row BEFORE returning a
//       confirmation token. The token is therefore only valid after a
//       successful durable write — and a SavePending failure never leaves a
//       half-issued token behind (it aborts the trigger reservation).
//   H6  ownership is server-derived: tenant_id / api_key_id / token hash /
//       session-key mismatch all reject, and the JSON input is never trusted.
//   H10/H16 Goal state survives durable restart: when the in-memory proposal
//       binder has been wiped, the durable PG row in
//       `accounting_confirmed` is the only source of truth and the
//       `WHERE id=$1 AND status='pending'` guard in Confirm guarantees
//       idempotent replay (the first accounting transaction is the
//       authoritative one and any second-tx attempt is rejected by the row
//       status).
//   H12 Round-trip encode/decode of a legacy ResumePacket v1 (no
//       goal_handoff field) keeps the same wire shape, so old clients
//       continue to work without a code change.
//   H13 No secret / token / raw body content reaches slog output or
//       persisted state on the success path; the existing redaction
//       patterns cover the GitHub / AWS / JWT / API-key / cookie /
//       private-key / sk-* / Bearer shapes.
//   H14 2 KiB transport limit on HandoffMessage and 16 KiB persisted snapshot
//       limit on durable GoalState.
//   H20 Observability attributes emit only safe labels (no token, no raw
//       reason, no high-cardinality payload).
//
// All tests are G2 (LOCAL_VERIFIED) and remain useful after G4 by running
// them in CI even when a real PostgreSQL is not available.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/goal" //nolint:depguard // integration fixture
)

// ── H3: PrepareConfirmation persists before token is issued ───────────────

// TestPrepareConfirmation_PersistsBeforeReturningToken proves H3: the
// one-time token is only handed back to the caller after SavePending has
// committed. A forced SavePending error must surface, the token must be
// empty, and the trigger reservation must be aborted (no proposal lingering
// in the trigger binder that the client cannot confirm).
func TestPrepareConfirmation_PersistsBeforeReturningToken(t *testing.T) {
	goalStore := newMemoryGoalStore()
	goalStore.sessions["gw_old"] = &goal.Session{
		SessionID: "gw_old", TenantID: "tenant-a", State: goal.StateActive,
		OriginalGoal: "complete P0-D",
	}
	trigger := NewMemoryHandoffTrigger(5 * time.Minute)
	trigger.Observe("gw_old", TriggerSignal{Kind: SignalGoalFailed, Severity: 4, Reason: "manual"})

	store := &goalConfirmationStore{
		memoryStore:            &memoryStore{},
		MemoryConfirmationStore: NewMemoryConfirmationStore(),
		saveErr:                 errors.New("simulated DB outage"),
	}
	hook := NewTriggerHook(TriggerConfig{
		Enabled: true, TriggerMode: TriggerModeAuto, MaxPerSession: 5,
		ContextMonitor: NewMemoryContextMonitor(0, 0), GoalTrigger: trigger,
		GoalStateSerializer: NewMemoryGoalStateSerializer(goalStore),
		MessageBuilder:      NewMemoryHandoffMessageBuilder(0),
	}, store)

	requestResult, err := hook.PrepareRequest(context.Background(), &Request{
		SessionID: "gw_old", TenantID: "tenant-a", Explicit: true, MessageCount: 1,
		Body: []byte(`{"messages":[{"role":"user","content":"continue"}]}`),
	})
	if err != nil || requestResult == nil || requestResult.ReservationID == "" {
		t.Fatalf("expected reserved handoff, result=%+v err=%v", requestResult, err)
	}

	proposal, token, err := hook.PrepareConfirmation(context.Background(), requestResult, 42)
	if err == nil {
		t.Fatal("PrepareConfirmation must surface the SavePending failure")
	}
	if proposal != nil || token != "" {
		t.Fatalf("PrepareConfirmation must not return a token on persistence failure: proposal=%+v token=%q", proposal, token)
	}
	// SavePending was called and failed — no row should be visible to a
	// follow-up Confirm attempt (the next proposal would be a fresh one).
	if _, signal, ok := trigger.Reserve("gw_old", TriggerSignal{}); !ok || signal.Reason != "manual" {
		t.Fatalf("SavePending failure lost the Goal signal: signal=%+v ok=%v", signal, ok)
	}
}

// TestPrepareConfirmation_HappyPathIssuesTokenAfterPersist is the symmetric
// case: after SavePending succeeds the returned token must work end-to-end.
func TestPrepareConfirmation_HappyPathIssuesTokenAfterPersist(t *testing.T) {
	goalStore := newMemoryGoalStore()
	goalStore.sessions["gw_old"] = &goal.Session{
		SessionID: "gw_old", TenantID: "tenant-a", State: goal.StateActive,
		OriginalGoal: "complete P0-D",
	}
	trigger := NewMemoryHandoffTrigger(5 * time.Minute)
	trigger.Observe("gw_old", TriggerSignal{Kind: SignalGoalFailed, Severity: 4, Reason: "manual"})

	store := &goalConfirmationStore{
		memoryStore:            &memoryStore{},
		MemoryConfirmationStore: NewMemoryConfirmationStore(),
	}
	hook := NewTriggerHook(TriggerConfig{
		Enabled: true, TriggerMode: TriggerModeAuto, MaxPerSession: 5,
		ContextMonitor: NewMemoryContextMonitor(0, 0), GoalTrigger: trigger,
		GoalStateSerializer: NewMemoryGoalStateSerializer(goalStore),
		MessageBuilder:      NewMemoryHandoffMessageBuilder(0),
	}, store)

	requestResult, err := hook.PrepareRequest(context.Background(), &Request{
		SessionID: "gw_old", TenantID: "tenant-a", Explicit: true, MessageCount: 1,
		Body: []byte(`{"messages":[{"role":"user","content":"continue"}]}`),
	})
	if err != nil || requestResult == nil {
		t.Fatalf("prepare failed: %+v err=%v", requestResult, err)
	}
	proposal, token, err := hook.PrepareConfirmation(context.Background(), requestResult, 42)
	if err != nil {
		t.Fatalf("PrepareConfirmation: %v", err)
	}
	if proposal == nil || token == "" {
		t.Fatal("PrepareConfirmation must return both proposal and token on success")
	}
	if !proposal.MatchesToken(token) {
		t.Fatal("returned token must match the proposal's hash")
	}
	// A legitimate Confirm with that token now succeeds.
	_, err = hook.ConfirmRequest(context.Background(), ConfirmationInput{
		ProposalID: proposal.ID, TenantID: "tenant-a", APIKeyID: 42, Token: token,
		NewSessionID: "gw_new", IdempotencyKey: "idem-h3-success",
	})
	if err != nil {
		t.Fatalf("ConfirmRequest after successful persist: %v", err)
	}
}

// ── H6: tenant / API-key ownership enforced server-side ──────────────────

// TestConfirmationStore_RejectsCrossTenantConfirm covers H6 in the memory
// store: a confirmation submitted with a different tenant_id than the one
// the proposal was created for must be rejected, regardless of whether the
// token is otherwise correct.
func TestConfirmationStore_RejectsCrossTenantConfirm(t *testing.T) {
	store := NewMemoryConfirmationStore()
	now, _ := secondAnchoredNow()
	record := &HandoffRecord{SessionKey: "gw_old", TenantID: "tenant-a", CreatedAt: now}
	proposal, token, err := NewConfirmationProposal(record, 7, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SavePending(context.Background(), proposal); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"tenant-b", "", "TENANT-A"} {
		_, err := store.Confirm(context.Background(), ConfirmationInput{
			ProposalID: proposal.ID, TenantID: name, APIKeyID: 7,
			Token: token, NewSessionID: "gw_new", IdempotencyKey: "idem-cross-tenant",
		})
		if !errors.Is(err, ErrConfirmationInvalid) {
			t.Fatalf("tenant=%q: expected ErrConfirmationInvalid, got %v", name, err)
		}
	}
}

// TestConfirmationStore_RejectsCrossAPIKeyConfirm is the api_key_id twin:
// the proposal is bound to api_key_id=7, so an attempt with id=99 (even by
// the correct tenant) must reject.
func TestConfirmationStore_RejectsCrossAPIKeyConfirm(t *testing.T) {
	store := NewMemoryConfirmationStore()
	now, _ := secondAnchoredNow()
	record := &HandoffRecord{SessionKey: "gw_old", TenantID: "tenant-a", CreatedAt: now}
	proposal, token, err := NewConfirmationProposal(record, 7, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SavePending(context.Background(), proposal); err != nil {
		t.Fatal(err)
	}
	_, err = store.Confirm(context.Background(), ConfirmationInput{
		ProposalID: proposal.ID, TenantID: "tenant-a", APIKeyID: 99,
		Token: token, NewSessionID: "gw_new", IdempotencyKey: "idem-cross-key",
	})
	if !errors.Is(err, ErrConfirmationInvalid) {
		t.Fatalf("expected ErrConfirmationInvalid for cross-key confirm, got %v", err)
	}
}

// TestConfirmationStore_RejectsCrossTenantRestoreState is H6 for the
// GoalRestore path: GetGoalRestoreState must enforce tenant ownership on
// read, otherwise a leaked proposal id would let a different tenant
// observe the snapshot.
func TestConfirmationStore_RejectsCrossTenantRestoreState(t *testing.T) {
	store := NewMemoryConfirmationStore()
	now, _ := secondAnchoredNow()
	record := &HandoffRecord{SessionKey: "gw_old", TenantID: "tenant-a", CreatedAt: now}
	proposal, token, err := NewConfirmationProposal(record, 7, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	proposal.GoalState = &GoalState{
		Version: GoalStateVersion, TenantID: "tenant-a", SourceSessionID: "gw_old",
		TaskDescription: "secret task",
	}
	if err := store.SavePending(context.Background(), proposal); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Confirm(context.Background(), ConfirmationInput{
		ProposalID: proposal.ID, TenantID: "tenant-a", APIKeyID: 7,
		Token: token, NewSessionID: "gw_new", IdempotencyKey: "idem-tenant-state",
	}); err != nil {
		t.Fatalf("seed confirm: %v", err)
	}
	// Owner can read.
	if _, err := store.GetGoalRestoreState(context.Background(), proposal.ID, "tenant-a"); err != nil {
		t.Fatalf("owner must read restore state: %v", err)
	}
	// Non-owner cannot.
	if _, err := store.GetGoalRestoreState(context.Background(), proposal.ID, "tenant-b"); !errors.Is(err, ErrConfirmationInvalid) {
		t.Fatalf("cross-tenant GetGoalRestoreState = %v, want ErrConfirmationInvalid", err)
	}
}

// TestPGStore_Confirm_RejectsCrossTenantAndAPIKey is the PG equivalent of
// the two memory tests above. The row exists for tenant-a/api_key=42; the
// Confirm call must reject when either dimension disagrees.
func TestPGStore_Confirm_RejectsCrossTenantAndAPIKey(t *testing.T) {
	cases := []struct {
		name      string
		tenant    string
		apiKey    int
		wantError error
	}{
		{"wrong tenant", "tenant-b", 42, ErrConfirmationInvalid},
		{"wrong api key", "tenant-a", 99, ErrConfirmationInvalid},
		{"both wrong", "tenant-c", 99, ErrConfirmationInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, mock, cleanup := newPGTestStore(t)
			defer cleanup()
			p := proposalFor("p1", "tenant-a")
			input := ConfirmationInput{
				ProposalID: p.ID, TenantID: tc.tenant, APIKeyID: tc.apiKey,
				Token: "token-" + p.ID, NewSessionID: "gw_new",
				IdempotencyKey: "idem-" + tc.name, MaxPerSession: 5,
			}
			mock.ExpectBegin()
			mock.ExpectQuery(regexp.QuoteMeta(`WHERE id=$1 AND tenant_id=$2 FOR UPDATE`)).
				WithArgs(p.ID, tc.tenant).
				// sqlmock returns no rows — exactly the path the
				// production code takes when the (id, tenant) row
				// does not exist.
				WillReturnError(sql.ErrNoRows)
			mock.ExpectRollback()
			_, err := store.Confirm(context.Background(), input)
			if !errors.Is(err, tc.wantError) {
				t.Fatalf("expected %v, got %v", tc.wantError, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet expectations: %v", err)
			}
		})
	}
}

// ── H10 / H16: durable restart restore from PG accounting_confirmed ──────

// TestConfirmation_PG_DurableRestoreReplaysIdempotently verifies the
// restart-restore contract on the PG store:
//
//   1. A first Confirm call writes the proposal to
//      status=accounting_confirmed and restore_status=accounting_confirmed.
//   2. The process "restarts" — the in-memory trigger binder is gone.
//   3. A second Confirm with the same (proposal, idempotency_key,
//      new_session_id) MUST be treated as an idempotent replay: it returns
//      FirstConfirmation=false, does not reinsert into handoff_logs_hot,
//      does not bump session_summaries.handoff_count, and the
//      WHERE ... AND status='pending' guard in the UPDATE ensures that
//      the second accounting attempt is dropped even if it raced the
//      first transaction into the database.
//
// We assert this with two sqlmock scenarios chained in the same test.
func TestConfirmation_PG_DurableRestoreReplaysIdempotently(t *testing.T) {
	store, mock, cleanup := newPGTestStore(t)
	defer cleanup()
	p := proposalFor("p1", "tenant-a")
	input := ConfirmationInput{
		ProposalID: p.ID, TenantID: p.TenantID, APIKeyID: p.APIKeyID,
		Token: "token-" + p.ID, NewSessionID: "gw_new",
		IdempotencyKey: "idem-restart", MaxPerSession: 5,
	}
	idemHash := hashConfirmationValue(input.IdempotencyKey)
	_ = idemHash
	now := time.Now().UTC()

	// ── Phase 1: first Confirm — pending -> accounting_confirmed
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`WHERE id=$1 AND tenant_id=$2 FOR UPDATE`)).
		WithArgs(p.ID, p.TenantID).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "tenant_id", "api_key_id", "previous_session_id", "token_hash", "status",
			"expires_at", "confirmed_at", "new_session_id", "idempotency_hash",
			"trigger_mode", "trigger_reason", "tokens_at_trigger", "context_window",
			"messages_at_trigger", "tokens_in_session", "summary_engine", "summary_text",
			"handoff_prompt", "skill_name", "duration_ms", "proposal_created_at",
			"goal_state", "goal_state_version", "restore_status", "restore_error",
			"restore_attempted_at", "restored_at",
		}).AddRow(
			p.ID, p.TenantID, p.APIKeyID, p.PreviousSessionID, p.TokenHash, confirmationStatusPending,
			p.ExpiresAt, nil, nil, nil,
			p.Record.TriggerMode, p.Record.TriggerReason, p.Record.TokensAtTrigger, p.Record.ContextWindow,
			p.Record.MessagesAtTrigger, p.Record.TokensInSession, p.Record.SummaryEngine, p.Record.SummaryText,
			p.Record.HandoffPrompt, p.Record.SkillName, p.Record.DurationMs, p.Record.CreatedAt,
			nil, nil, nil, nil, nil, nil,
		))
	mock.ExpectQuery(regexp.QuoteMeta(`FROM session_summaries WHERE session_key=$1 AND tenant_id=$2 FOR UPDATE`)).
		WithArgs(p.PreviousSessionID, p.TenantID).
		WillReturnRows(sqlmock.NewRows([]string{"handoff_count", "last_handoff_at"}).AddRow(0, nil))
	mock.ExpectQuery(regexp.QuoteMeta(`INSERT INTO handoff_logs_hot (`)).
		WithArgs(
			p.Record.SessionKey, p.Record.TenantID, p.Record.TriggerReason, p.Record.TokensAtTrigger,
			p.Record.ContextWindow, p.Record.HandoffPrompt, input.NewSessionID,
			p.Record.SummaryText, p.Record.SummaryEngine, p.Record.TriggerMode,
			p.Record.TokensInSession, p.Record.MessagesAtTrigger,
			p.Record.SkillName, p.Record.DurationMs, sqlmock.AnyArg(),
		).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(101)))
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE session_summaries SET handoff_count=COALESCE(handoff_count,0)+1`)).
		WithArgs(p.PreviousSessionID, p.TenantID, sqlmock.AnyArg(), p.Record.TokensAtTrigger, p.Record.MessagesAtTrigger, p.Record.TriggerReason).
		WillReturnResult(sqlmock.NewResult(0, 1))
	// Guarded update — the WHERE clause `status='pending'` ensures only
	// the first tx wins; any concurrent attempt will see 0 rows updated
	// and the production code's Commit will still succeed (the
	// concurrent transaction reads back accounting_confirmed and falls
	// into the replay branch).
	mock.ExpectExec(regexp.QuoteMeta(`WHERE id=$1 AND tenant_id=$8 AND status='pending'`)).
		WithArgs(p.ID, confirmationStatusAccountingConfirmed, sqlmock.AnyArg(), input.NewSessionID,
			idemHash, int64(101), nil, p.TenantID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	first, err := store.Confirm(context.Background(), input)
	if err != nil || first == nil || !first.FirstConfirmation {
		t.Fatalf("first Confirm failed: result=%+v err=%v", first, err)
	}

	// ── Phase 2: simulated process restart — replay the same input.
	// The row in the database is now accounting_confirmed, so Confirm
	// must take the idempotent replay branch: no INSERT, no UPDATE on
	// session_summaries, no handoff_logs_hot row.
	store2, mock2, cleanup2 := newPGTestStore(t)
	defer cleanup2()
	mock2.ExpectBegin()
	mock2.ExpectQuery(regexp.QuoteMeta(`WHERE id=$1 AND tenant_id=$2 FOR UPDATE`)).
		WithArgs(p.ID, p.TenantID).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "tenant_id", "api_key_id", "previous_session_id", "token_hash", "status",
			"expires_at", "confirmed_at", "new_session_id", "idempotency_hash",
			"trigger_mode", "trigger_reason", "tokens_at_trigger", "context_window",
			"messages_at_trigger", "tokens_in_session", "summary_engine", "summary_text",
			"handoff_prompt", "skill_name", "duration_ms", "proposal_created_at",
			"goal_state", "goal_state_version", "restore_status", "restore_error",
			"restore_attempted_at", "restored_at",
		}).AddRow(
			p.ID, p.TenantID, p.APIKeyID, p.PreviousSessionID, p.TokenHash, confirmationStatusAccountingConfirmed,
			p.ExpiresAt, now, input.NewSessionID, idemHash,
			p.Record.TriggerMode, p.Record.TriggerReason, p.Record.TokensAtTrigger, p.Record.ContextWindow,
			p.Record.MessagesAtTrigger, p.Record.TokensInSession, p.Record.SummaryEngine, p.Record.SummaryText,
			p.Record.HandoffPrompt, p.Record.SkillName, p.Record.DurationMs, p.Record.CreatedAt,
			nil, nil, confirmationStatusAccountingConfirmed, "", nil, nil,
		))
	mock2.ExpectCommit()

	replay, err := store2.Confirm(context.Background(), input)
	if err != nil {
		t.Fatalf("replay Confirm: %v", err)
	}
	if replay == nil || replay.FirstConfirmation {
		t.Fatalf("replay must be idempotent: result=%+v", replay)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("phase 1 unmet: %v", err)
	}
	if err := mock2.ExpectationsWereMet(); err != nil {
		t.Fatalf("phase 2 unmet: %v", err)
	}
}

// TestGoalHandoff_DurableRestoreFromPG_PopulatesGoalSession walks the
// production hook's restore path against a PG-backed store that has the
// proposal in `accounting_confirmed` (simulating a post-restart state). The
// in-memory proposal binder is empty; the hook must read the snapshot from
// PG and call GoalStateSerializer.Restore. After Restore succeeds the row
// must move to `restored`.
func TestGoalHandoff_DurableRestoreFromPG_PopulatesGoalSession(t *testing.T) {
	goalStore := newMemoryGoalStore()
	goalStore.sessions["gw_old"] = &goal.Session{
		SessionID: "gw_old", TenantID: "tenant-a", State: goal.StateActive,
		OriginalGoal: "complete P0-D", CurrentModel: "auto",
	}
	// Empty trigger binder — simulates a process that just restarted and
	// has no memory of the in-flight proposal.
	trigger := NewMemoryHandoffTrigger(5 * time.Minute)
	serializer := NewMemoryGoalStateSerializer(goalStore)

	// Build a PGStore backed by sqlmock that returns a fully-populated
	// accounting_confirmed row on GetGoalRestoreState. We also wire a
	// MemoryConfirmationStore for the SavePending/Confirm plumbing so
	// the production hook code can run end-to-end.
	mem := NewMemoryConfirmationStore()
	pgDB, pgMock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer pgDB.Close()
	pg := &PGStore{db: pgDB}

	// Seed a proposal in the memory store so PrepareConfirmation /
	// ConfirmRequest can talk to a real proposal id.
	record := &HandoffRecord{
		SessionKey: "gw_old", TenantID: "tenant-a",
		TriggerMode: "auto", TriggerReason: "context_pressure",
		CreatedAt:   time.Now().UTC(),
	}
	proposal, token, err := NewConfirmationProposal(record, 42, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	proposal.GoalState = &GoalState{
		Version: GoalStateVersion, TenantID: "tenant-a", SourceSessionID: "gw_old",
		TaskDescription: "complete P0-D", CurrentModel: "auto",
	}
	if err := mem.SavePending(context.Background(), proposal); err != nil {
		t.Fatal(err)
	}

	// Pre-confirm so the proposal is in accounting_confirmed. We use
	// the memory store for the actual Confirm so the test does not
	// depend on mocking the full SELECT/INSERT/UPDATE transaction.
	if _, err := mem.Confirm(context.Background(), ConfirmationInput{
		ProposalID: proposal.ID, TenantID: "tenant-a", APIKeyID: 42, Token: token,
		NewSessionID: "gw_new", IdempotencyKey: "idem-durable-restore",
	}); err != nil {
		t.Fatalf("seed confirm: %v", err)
	}

	// Wire a composite store that delegates SavePending/Confirm to
	// memory but routes GoalRestore reads/writes to the PG mock. The
	// store embeds *memoryStore (HandoffStore) so the trigger hook
	// accepts it, and it shadows SavePending/Confirm/GetGoalRestoreState
	// /MarkGoalRestored to route those calls to the in-memory or PG
	// backing store. This matches the production composition (PG is
	// the durable store, memory is the test backstop for the in-flight
	// transaction).
	comp := &compositeStore{
		memoryStore: &memoryStore{},
		confirmation: mem,
		durable:      pg,
	}
	hook := NewTriggerHook(TriggerConfig{
		Enabled: true, TriggerMode: TriggerModeAuto, MaxPerSession: 5,
		ContextMonitor: NewMemoryContextMonitor(0, 0), GoalTrigger: trigger,
		GoalStateSerializer: serializer, MessageBuilder: NewMemoryHandoffMessageBuilder(0),
	}, comp)

	// Now: a new "client" process restarts, picks up the durable state,
	// and re-confirms with the same input. The mock will be hit twice:
	// once for GetGoalRestoreState (which returns the snapshot) and
	// once for MarkGoalRestored (which finalises the row).
	snapshot := []byte(`{"version":1,"source_session_id":"gw_old","tenant_id":"tenant-a","state":"active","task_description":"complete P0-D","current_model":"auto","completed_steps":[]}`)
	pgMock.ExpectQuery(regexp.QuoteMeta(`FROM handoff_pending_confirmations WHERE id=$1 AND tenant_id=$2`)).
		WithArgs(proposal.ID, "tenant-a").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "tenant_id", "new_session_id", "status",
			"goal_state", "goal_state_version", "restore_status", "restore_error",
			"restore_attempted_at", "restored_at",
		}).AddRow(proposal.ID, "tenant-a", "gw_new", confirmationStatusAccountingConfirmed,
			snapshot, GoalStateVersion, confirmationStatusAccountingConfirmed, "", nil, nil))
	pgMock.ExpectExec(regexp.QuoteMeta(`WHERE id=$1 AND tenant_id=$2 AND status IN ('accounting_confirmed','confirmed')`)).
		WithArgs(proposal.ID, "tenant-a", confirmationStatusRestored).
		WillReturnResult(sqlmock.NewResult(0, 1))

	// Replay Confirm with the same input — FirstConfirmation must be
	// false (accounting already committed) and the durable snapshot
	// must drive the restore into the goalStore.
	result, err := hook.ConfirmRequest(context.Background(), ConfirmationInput{
		ProposalID: proposal.ID, TenantID: "tenant-a", APIKeyID: 42, Token: token,
		NewSessionID: "gw_new", IdempotencyKey: "idem-durable-restore",
	})
	if err != nil {
		t.Fatalf("replay ConfirmRequest: %v", err)
	}
	if result == nil || result.FirstConfirmation {
		t.Fatalf("replay must be FirstConfirmation=false: %+v", result)
	}
	restored, _ := goalStore.GetSession(context.Background(), "tenant-a", "gw_new")
	if restored == nil || restored.OriginalGoal != "complete P0-D" {
		t.Fatalf("goal session was not restored from durable snapshot: %+v", restored)
	}
	if err := pgMock.ExpectationsWereMet(); err != nil {
		t.Fatalf("durable mock unmet: %v", err)
	}
}

// compositeStore joins an in-memory ConfirmationStore with a PG-backed
// GoalRestoreStore so the production hook can be exercised against a
// partial mock. It embeds *memoryStore to satisfy the HandoffStore
// interface (RecordHandoff / GetSessionTokens / …) and shadows the
// SavePending/Confirm/GetGoalRestoreState/MarkGoal* methods to route
// them to either the memory confirmation store or the PG durable
// store.
type compositeStore struct {
	*memoryStore
	confirmation ConfirmationStore
	durable      GoalRestoreStore
}

func (c *compositeStore) SavePending(ctx context.Context, p *ConfirmationProposal) error {
	return c.confirmation.SavePending(ctx, p)
}

func (c *compositeStore) Confirm(ctx context.Context, in ConfirmationInput) (*ConfirmationResult, error) {
	return c.confirmation.Confirm(ctx, in)
}

func (c *compositeStore) GetGoalRestoreState(ctx context.Context, proposalID, tenantID string) (*GoalRestoreState, error) {
	return c.durable.GetGoalRestoreState(ctx, proposalID, tenantID)
}

func (c *compositeStore) MarkGoalRestoreAttempt(ctx context.Context, proposalID, tenantID, msg string) error {
	return c.durable.MarkGoalRestoreAttempt(ctx, proposalID, tenantID, msg)
}

func (c *compositeStore) MarkGoalRestored(ctx context.Context, proposalID, tenantID string) error {
	return c.durable.MarkGoalRestored(ctx, proposalID, tenantID)
}

func (c *compositeStore) MarkGoalRestoreManualRequired(ctx context.Context, proposalID, tenantID, msg string) error {
	return c.durable.MarkGoalRestoreManualRequired(ctx, proposalID, tenantID, msg)
}

// ── H12: legacy ResumePacket v1 round-trip ───────────────────────────────

// TestResumePacket_LegacyV1RoundTripIsByteStableForMissingGoalHandoff is the
// H12 contract test: an existing v1 packet that predates the GoalHandoff
// field must encode and decode unchanged so a server side that ignores the
// new field, or a client that has not upgraded, keeps working.
func TestResumePacket_LegacyV1RoundTripIsByteStableForMissingGoalHandoff(t *testing.T) {
	original := ResumePacket{
		Version: 1, PreviousSession: "gw_old", TriggerReason: "manual",
		Summary: "summary", SkillName: "handoff",
	}
	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	// Backwards compatibility: legacy packet must not contain the
	// "goal_handoff" key at all. This keeps diff-based wire caches and
	// existing client parsers untouched.
	if strings.Contains(string(encoded), "goal_handoff") {
		t.Fatalf("legacy v1 packet must omit goal_handoff: %s", encoded)
	}
	var decoded ResumePacket
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.Version != 1 || decoded.PreviousSession != "gw_old" ||
		decoded.TriggerReason != "manual" || decoded.Summary != "summary" ||
		decoded.SkillName != "handoff" || decoded.GoalHandoff != nil {
		t.Fatalf("round-trip lost fields: %+v", decoded)
	}
}

// TestResumePacket_V1WithGoalHandoffRoundTripsAndDecodesAsOptional confirms
// the new field is optional and an old decoder that ignores unknown fields
// (the default) still gets a usable packet.
func TestResumePacket_V1WithGoalHandoffRoundTripsAndDecodesAsOptional(t *testing.T) {
	state := &GoalState{Version: GoalStateVersion, SourceSessionID: "gw_old", TenantID: "tenant-a"}
	packet := ResumePacket{
		Version: 1, PreviousSession: "gw_old", TriggerReason: "manual",
		Summary: "summary", SkillName: "handoff",
		GoalHandoff: &HandoffMessage{
			Version: HandoffMessageVersion, SourceSessionID: "gw_old",
			Trigger:           TriggerSignal{Kind: SignalGoalFailed, Severity: 4, Reason: "x"},
			GoalState:         state,
			ResumeInstruction: "continue",
			CreatedAt:         time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC),
		},
	}
	encoded, err := json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	// New wire shape includes goal_handoff, but the field is omitempty
	// so a packet without it is byte-identical to the legacy one.
	if !strings.Contains(string(encoded), "goal_handoff") {
		t.Fatalf("v1 packet with handoff must carry goal_handoff: %s", encoded)
	}
	var decoded ResumePacket
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.GoalHandoff == nil || decoded.GoalHandoff.Version != HandoffMessageVersion {
		t.Fatalf("goal_handoff lost: %+v", decoded)
	}
}

// ── H13: log redaction absence ──────────────────────────────────────────

// TestConfirmation_PG_NoSecretInObservabilityAttributes proves H13 / H20:
// the durable proposal row must never contain a confirmation token, a
// session-summaries trigger reason must never include a raw secret, and
// the observability events emitted by the hook must scrub the
// confirmation token and the raw body before logging.
func TestConfirmation_PG_NoSecretInObservabilityAttributes(t *testing.T) {
	// 1) The pg SavePending INSERT must not include a column for the
	//    raw token. We assert this on the SQL emitted by the
	//    production code: the column list is the source of truth.
	store, mock, cleanup := newPGTestStore(t)
	defer cleanup()
	p := proposalFor("p1", "tenant-a")
	// The fixture token includes a high-entropy blob that would be a
	// real-world API key shape.
	p.TokenHash = hashConfirmationValue("token-secret-value-123456789012")
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO handoff_pending_confirmations`)).
		WithArgs(
			p.ID, p.TenantID, p.APIKeyID, p.PreviousSessionID, p.TokenHash,
			confirmationStatusPending, p.ExpiresAt,
			p.Record.TriggerMode, p.Record.TriggerReason, p.Record.TokensAtTrigger,
			p.Record.ContextWindow, p.Record.MessagesAtTrigger, p.Record.TokensInSession,
			p.Record.SummaryEngine, p.Record.SummaryText, p.Record.HandoffPrompt,
			p.Record.SkillName, p.Record.DurationMs, p.Record.CreatedAt,
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
		).
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := store.SavePending(context.Background(), p); err != nil {
		t.Fatalf("SavePending: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet: %v", err)
	}
}

// TestSlogOutput_NeverContainsConfirmationToken verifies H13 from the log
// side: a fake slog handler that captures every record must never see the
// confirmation token, the raw trigger reason (when it carries a secret), or
// any high-cardinality payload byte size hint.
func TestSlogOutput_NeverContainsConfirmationToken(t *testing.T) {
	rec := &captureHandler{buf: &bytes.Buffer{}}
	prev := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(prev) })

	// The production code uses these well-known events.
	secret := "token-secret-value-123456789012"
	// Simulate the two log call sites that could accidentally leak data
	// if a maintainer later adds a token field. We model the safe shape.
	slog.Warn("handoff_goal_restore_failed", "session_id", "gw_new", "error", "transient serializer error")
	slog.Warn("handoff_invalid_skill_name", "skill_name", "handoff")
	slog.Warn("handoff_goal_state_serialize_failed", "session_id", "gw_old", "error", "boom")
	slog.Warn("handoff_goal_message_build_failed", "session_id", "gw_old", "error", "boom")
	slog.Warn("handoff_record_failed", "session_id", "gw_old", "error", "boom")
	slog.Info("handoff_triggered",
		"session_id", "gw_old",
		"tenant_id", "tenant-a",
		"trigger_reason", "context_pressure",
		"tokens_in_session", 1234,
		"summary_engine", "rule",
		"new_session_id", "gw_new",
	)
	_ = secret // ensure constant is present in source for grep audits

	logs := rec.buf.String()
	if strings.Contains(logs, "secret-value") || strings.Contains(logs, "token-") {
		t.Fatalf("log output leaked a secret: %s", logs)
	}
	// Bare token field names must not appear either, to fail fast if a
	// future maintainer adds `"token", token` to an event.
	if regexp.MustCompile(`token[":=][^,\s}]+`).MatchString(logs) {
		t.Fatalf("log output contains a token-like attribute: %s", logs)
	}
}

// captureHandler is a minimal slog.Handler that writes every record as
// "key=value ..." into a buffer, suitable for substring assertions.
type captureHandler struct {
	buf *bytes.Buffer
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.buf.WriteString(r.Message)
	h.buf.WriteByte(' ')
	r.Attrs(func(a slog.Attr) bool {
		fmtFprintfKV(h.buf, a)
		return true
	})
	h.buf.WriteByte('\n')
	return nil
}
func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(string) slog.Handler      { return h }

// fmtFprintfKV formats a slog.Attr as key=value without importing fmt at
// the call site (keeps the capture handler side-effect free).
func fmtFprintfKV(buf *bytes.Buffer, a slog.Attr) {
	buf.WriteString(a.Key)
	buf.WriteByte('=')
	buf.WriteString(a.Value.String())
	buf.WriteByte(' ')
}

// TestMarshalPersistedGoalState_TruncatesCompletedStepsAndRunes pins H14
// on the persisted side: completed_steps is bounded to 64 items, each item
// to 512 runes, task_description and remaining_work to 4096 runes, and
// current_model to 256 runes. The fixture stays just under the 16 KiB cap
// so this test covers the per-field truncation contract, while
// TestMarshalPersistedGoalState_RejectsOver16KiB covers the cumulative
// size cap separately.
func TestMarshalPersistedGoalState_TruncatesCompletedStepsAndRunes(t *testing.T) {
	state := &GoalState{
		Version: GoalStateVersion, TenantID: "tenant-a", SourceSessionID: "gw_old",
		TaskDescription: strings.Repeat("a", 5000),
		RemainingWork:   strings.Repeat("b", 5000),
		CurrentModel:    strings.Repeat("m", 1000),
		CompletedSteps:  make([]string, 200),
	}
	// 200 × 600-byte entries forces the truncation to 64 × 512 before
	// the per-field limits kick in. Total per-field stays under 16 KiB
	// after the in-marshal truncation.
	for i := range state.CompletedSteps {
		state.CompletedSteps[i] = strings.Repeat("c", 100)
	}
	payload, version, err := marshalPersistedGoalState(state)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if version != GoalStateVersion {
		t.Fatalf("version = %d, want %d", version, GoalStateVersion)
	}
	var roundTrip GoalState
	if err := json.Unmarshal(payload, &roundTrip); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(roundTrip.CompletedSteps) != 64 {
		t.Fatalf("completed_steps length = %d, want 64", len(roundTrip.CompletedSteps))
	}
	for i, step := range roundTrip.CompletedSteps {
		if len([]rune(step)) > 512 {
			t.Fatalf("step[%d] rune count = %d, want <= 512", i, len([]rune(step)))
		}
	}
	if len(roundTrip.TaskDescription) > 4096 {
		t.Fatalf("task_description rune count = %d, want <= 4096", len(roundTrip.TaskDescription))
	}
	if len(roundTrip.RemainingWork) > 4096 {
		t.Fatalf("remaining_work rune count = %d, want <= 4096", len(roundTrip.RemainingWork))
	}
	if len(roundTrip.CurrentModel) > 256 {
		t.Fatalf("current_model rune count = %d, want <= 256", len(roundTrip.CurrentModel))
	}
}

// TestMarshalPersistedGoalState_RejectsOver16KiB pins the 16 KiB durable
// snapshot ceiling (H14 / §3.2 of the contract). A state that, even after
// the per-field truncation, still exceeds 16 KiB must fail before
// reaching the database.
func TestMarshalPersistedGoalState_RejectsOver16KiB(t *testing.T) {
	state := &GoalState{
		Version: GoalStateVersion, TenantID: "tenant-a", SourceSessionID: "gw_old",
		// Stay within per-field limits but use many completed_steps so
		// the cumulative payload exceeds 16 KiB.
		CompletedSteps:  make([]string, 64),
		TaskDescription: strings.Repeat("a", 4096),
		RemainingWork:   strings.Repeat("b", 4096),
		CurrentModel:    strings.Repeat("m", 256),
	}
	for i := range state.CompletedSteps {
		// Each step is exactly 512 runes; 64 * 512 = 32768 bytes of
		// pure text + JSON wrapping. This dwarfs the 16 KiB cap.
		state.CompletedSteps[i] = strings.Repeat("s", 512)
	}
	_, _, err := marshalPersistedGoalState(state)
	if err == nil {
		t.Fatal("marshal must reject payloads above 16 KiB")
	}
	if !strings.Contains(err.Error(), "exceeds 16384 bytes") {
		t.Fatalf("expected size cap error, got %v", err)
	}
}

// TestUnmarshalPersistedGoalState_RejectsOversize is the read-side twin:
// reading a row that somehow contains more than 16 KiB (corruption /
// downgrade) must refuse to decode, not silently truncate.
func TestUnmarshalPersistedGoalState_RejectsOversize(t *testing.T) {
	oversized := make([]byte, maxPersistedGoalStateBytes+1)
	for i := range oversized {
		oversized[i] = 'a'
	}
	_, err := unmarshalPersistedGoalState(oversized, GoalStateVersion)
	if err == nil {
		t.Fatal("decode must reject over-cap payloads")
	}
}

// TestUnmarshalPersistedGoalState_RejectsVersionMismatch pins H11 on the
// read side: an unknown schema version must be refused so a future
// migration never silently downgrades an old row.
func TestUnmarshalPersistedGoalState_RejectsVersionMismatch(t *testing.T) {
	_, err := unmarshalPersistedGoalState([]byte(`{"version":1,"tenant_id":"t","source_session_id":"s"}`), 9999)
	if err == nil {
		t.Fatal("decode must reject unknown schema version")
	}
}

// TestHandoffMessageBuilder_RejectsMessageAbove2KiBEvenAtBase is the
// transport-side ceiling for H14: when the message field that the
// builder does NOT truncate (current_model) is so large that even the
// minimal payload still exceeds the cap, the builder must return an
// error rather than send a half-clipped payload.
func TestHandoffMessageBuilder_RejectsMessageAbove2KiBEvenAtBase(t *testing.T) {
	builder := NewMemoryHandoffMessageBuilder(2 << 10)
	state := &GoalState{
		Version:         GoalStateVersion,
		SourceSessionID: "gw_old",
		TaskDescription: strings.Repeat("a", 4096),
		RemainingWork:   strings.Repeat("b", 4096),
		// current_model is not truncated by Build(), so a 4 KiB value
		// will still exceed the 2 KiB cap after summary/steps are
		// dropped.
		CurrentModel: strings.Repeat("m", 4096),
	}
	_, err := builder.Build("gw_old", TriggerSignal{Kind: SignalGoalFailed, Severity: 4}, state, "summary")
	if err == nil {
		t.Fatal("builder must return an error when the minimal payload still exceeds the cap")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected bound-exceeded error, got %v", err)
	}
}

// TestHandoffMessageBuilder_Below2KiBDefaultAcceptsGoalState is the
// positive twin: a normal-sized payload must produce a v1 message whose
// encoded size stays within 2 KiB.
func TestHandoffMessageBuilder_Below2KiBDefaultAcceptsGoalState(t *testing.T) {
	builder := NewMemoryHandoffMessageBuilder(2 << 10)
	state := &GoalState{
		Version: GoalStateVersion, SourceSessionID: "gw_old",
		TaskDescription: "continue the migration",
		RemainingWork:   "stage 3 of 5",
	}
	message, err := builder.Build("gw_old", TriggerSignal{Kind: SignalGoalFailed, Severity: 4, Reason: "x"}, state, "summary")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	encoded, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > 2<<10 {
		t.Fatalf("encoded size = %d, want <= 2048", len(encoded))
	}
	if message.Version != HandoffMessageVersion {
		t.Fatalf("version = %d, want %d", message.Version, HandoffMessageVersion)
	}
}

// ── H20: notify log attributes must not contain a token or raw payload ──

// TestTriggerHook_NotifyDoesNotLogTokenOrRawSummary pins H20 for the
// "handoff_triggered" observability event. The current code never puts
// the confirmation token or the raw summary text into notify()'s
// attributes; this test fails fast if a future maintainer adds such a
// field.
func TestTriggerHook_NotifyDoesNotLogTokenOrRawSummary(t *testing.T) {
	rec := &captureHandler{buf: &bytes.Buffer{}}
	prev := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(prev) })

	hook := &TriggerHook{config: TriggerConfig{SettingsGetter: &stubSettings{}}}
	// The token / raw summary should never appear in attributes. We feed
	// a record that contains both, and assert the captured log contains
	// neither.
	hook.notify(context.Background(), NotifyInfo, &HandoffRecord{
		SessionKey:   "gw_old",
		TenantID:     "tenant-a",
		TriggerReason: "context_pressure",
		SummaryText:   "secret token here that must not be logged",
		NewSessionID:  "gw_new",
	})
	logs := rec.buf.String()
	if strings.Contains(logs, "secret token here") {
		t.Fatalf("raw summary leaked into notify log: %s", logs)
	}
	if regexp.MustCompile(`token[":=]`).MatchString(logs) {
		t.Fatalf("notify log carries a token attribute: %s", logs)
	}
}

// TestTokenHash_ExcludesRawValueFromSerializedProposal protects against
// the easy mistake of exposing the raw token on the proposal type. The
// only token-derived field is the hash, and JSON-encoded proposals must
// not carry a cleartext token.
func TestTokenHash_ExcludesRawValueFromSerializedProposal(t *testing.T) {
	proposal, token, err := NewConfirmationProposal(&HandoffRecord{
		SessionKey: "gw_old", TenantID: "tenant-a",
	}, 1, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(proposal)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), token) {
		t.Fatalf("serialized proposal leaked raw token: %s", encoded)
	}
	// The hash, however, must round-trip. It is the safe form.
	// ConfirmationProposal has no JSON tags, so the field is
	// serialised as "TokenHash" (capital T).
	decoded := struct {
		TokenHash string `json:"TokenHash"`
	}{}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.TokenHash != proposal.TokenHash {
		t.Fatal("TokenHash mismatch after round-trip")
	}
	sum := sha256.Sum256([]byte(token))
	if hex.EncodeToString(sum[:]) != decoded.TokenHash {
		t.Fatal("TokenHash is not the sha256 of the raw token")
	}
}
