package handoff

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// newPGTestStore wires a PGStore onto a sqlmock-backed *sql.DB. The caller
// owns the mock and must Close it after the test.
func newPGTestStore(t *testing.T) (*PGStore, sqlmock.Sqlmock, func()) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	store := &PGStore{db: db}
	return store, mock, func() { _ = db.Close() }
}

// proposalFor returns a fully-populated ConfirmationProposal suitable for
// SavePending / Confirm exercises. Proposal IDs and tenants are parameterised
// so each test can isolate its data.
func proposalFor(id, tenant string) *ConfirmationProposal {
	return &ConfirmationProposal{
		ID:                id,
		TenantID:          tenant,
		APIKeyID:          42,
		PreviousSessionID: "gw_old",
		TokenHash:         hashConfirmationValue("token-" + id),
		ExpiresAt:         time.Now().Add(5 * time.Minute),
		Record: HandoffRecord{
			SessionKey:        "gw_old",
			TenantID:          tenant,
			TriggerMode:       "manual",
			TriggerReason:     "user_requested",
			TokensAtTrigger:   1234,
			ContextWindow:     8192,
			MessagesAtTrigger: 12,
			TokensInSession:   5432,
			SummaryEngine:     "engine-a",
			SummaryText:       "summary",
			HandoffPrompt:     "prompt",
			SkillName:         "default",
			DurationMs:        1200,
			CreatedAt:         time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC),
		},
		Status: confirmationStatusPending,
		GoalState: &GoalState{
			Version:         GoalStateVersion,
			TenantID:        tenant,
			SourceSessionID: "gw_old",
			TaskDescription: "refactor goal handoff",
			RemainingWork:   "stage 3 of 5",
			CompletedSteps:  []string{"stage1", "stage2"},
			CurrentModel:    "auto",
		},
	}
}

// ── SavePending ────────────────────────────────────────────────────────────

func TestPGStore_SavePending_FirstInsert(t *testing.T) {
	store, mock, cleanup := newPGTestStore(t)
	defer cleanup()

	p := proposalFor("p1", "tenant-a")
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
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGStore_SavePending_RejectsMissingFields(t *testing.T) {
	store, mock, cleanup := newPGTestStore(t)
	defer cleanup()
	// No SQL expected — input validation rejects before any DB call.
	bad := &ConfirmationProposal{ID: "p1"} // missing tenant + previous session + token hash
	if err := store.SavePending(context.Background(), bad); err == nil {
		t.Fatal("SavePending must reject proposal with missing required fields")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("no SQL should have been issued; unmet expectations: %v", err)
	}
}

// ── Confirm: full happy-path transaction ───────────────────────────────────

func TestPGStore_Confirm_FirstConfirmation_Success(t *testing.T) {
	store, mock, cleanup := newPGTestStore(t)
	defer cleanup()

	p := proposalFor("p1", "tenant-a")
	input := ConfirmationInput{
		ProposalID:      p.ID,
		TenantID:        p.TenantID,
		APIKeyID:        p.APIKeyID,
		Token:           "token-" + p.ID,
		NewSessionID:    "gw_new",
		IdempotencyKey:  "idem-1",
		MaxPerSession:   5,
		CooldownSeconds: 0,
	}

	mock.ExpectBegin()
	// 1. SELECT proposal FOR UPDATE
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id,tenant_id,api_key_id,previous_session_id,token_hash,status,expires_at,confirmed_at,new_session_id,idempotency_hash,trigger_mode,trigger_reason,tokens_at_trigger,context_window,messages_at_trigger,tokens_in_session,summary_engine,summary_text,handoff_prompt,skill_name,duration_ms,proposal_created_at,goal_state,goal_state_version,restore_status,restore_error,restore_attempted_at,restored_at FROM handoff_pending_confirmations WHERE id=$1 AND tenant_id=$2 FOR UPDATE`)).
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
			[]byte(`{"version":1,"tenant_id":"tenant-a","source_session_id":"gw_old","task_description":"refactor","remaining_work":"stage 3","completed_steps":["s1","s2"],"current_model":"auto"}`),
			GoalStateVersion, nil, nil, nil, nil,
		))
	// 2. SELECT session_summaries FOR UPDATE
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT COALESCE(handoff_count,0),last_handoff_at FROM session_summaries WHERE session_key=$1 AND tenant_id=$2 FOR UPDATE`)).
		WithArgs(p.PreviousSessionID, p.TenantID).
		WillReturnRows(sqlmock.NewRows([]string{"handoff_count", "last_handoff_at"}).AddRow(0, nil))
	// 3. INSERT handoff_logs_hot RETURNING id
	mock.ExpectQuery(regexp.QuoteMeta(`INSERT INTO handoff_logs_hot (`)).
		WithArgs(
			p.Record.SessionKey, p.Record.TenantID, p.Record.TriggerReason, p.Record.TokensAtTrigger,
			p.Record.ContextWindow, p.Record.HandoffPrompt, input.NewSessionID,
			p.Record.SummaryText, p.Record.SummaryEngine, p.Record.TriggerMode,
			p.Record.TokensInSession, p.Record.MessagesAtTrigger,
			p.Record.SkillName, p.Record.DurationMs, sqlmock.AnyArg(),
		).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(99)))
	// 4. UPDATE session_summaries
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE session_summaries SET handoff_count=COALESCE(handoff_count,0)+1`)).
		WithArgs(p.PreviousSessionID, p.TenantID, sqlmock.AnyArg(), p.Record.TokensAtTrigger, p.Record.MessagesAtTrigger, p.Record.TriggerReason).
		WillReturnResult(sqlmock.NewResult(0, 1))
	// 5. UPDATE proposal -> accounting_confirmed. In Confirm, the proposal's
	// GoalState field is only populated by GetGoalRestoreState (unmarshal), so
	// Confirm sees p.GoalState=nil here even when the column has bytes — the
	// restore_status arg is therefore nil in the WHERE update.
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE handoff_pending_confirmations SET status=$2,confirmed_at=$3,new_session_id=$4,idempotency_hash=$5,handoff_log_id=$6,restore_status=$7,updated_at=NOW() WHERE id=$1 AND tenant_id=$8 AND status='pending'`)).
		WithArgs(p.ID, confirmationStatusAccountingConfirmed, sqlmock.AnyArg(), input.NewSessionID,
			hashConfirmationValue(input.IdempotencyKey), int64(99), nil, p.TenantID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	res, err := store.Confirm(context.Background(), input)
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if res == nil || !res.FirstConfirmation {
		t.Fatalf("expected FirstConfirmation=true result, got %+v", res)
	}
	if res.NewSessionID != input.NewSessionID {
		t.Fatalf("NewSessionID = %q, want %q", res.NewSessionID, input.NewSessionID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// ── Confirm: replay branches ───────────────────────────────────────────────

func TestPGStore_Confirm_ReplaySameIdempotency_NoNewLog(t *testing.T) {
	store, mock, cleanup := newPGTestStore(t)
	defer cleanup()

	p := proposalFor("p1", "tenant-a")
	input := ConfirmationInput{
		ProposalID: p.ID, TenantID: p.TenantID, APIKeyID: p.APIKeyID,
		Token: "token-" + p.ID, NewSessionID: "gw_new",
		IdempotencyKey: "idem-1", MaxPerSession: 5,
	}
	idemHash := hashConfirmationValue(input.IdempotencyKey)
	now := time.Now().UTC()

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
			p.ID, p.TenantID, p.APIKeyID, p.PreviousSessionID, p.TokenHash, confirmationStatusAccountingConfirmed,
			p.ExpiresAt, now, input.NewSessionID, idemHash,
			p.Record.TriggerMode, p.Record.TriggerReason, p.Record.TokensAtTrigger, p.Record.ContextWindow,
			p.Record.MessagesAtTrigger, p.Record.TokensInSession, p.Record.SummaryEngine, p.Record.SummaryText,
			p.Record.HandoffPrompt, p.Record.SkillName, p.Record.DurationMs, p.Record.CreatedAt,
			nil, nil, confirmationStatusAccountingConfirmed, "", nil, nil,
		))
	mock.ExpectCommit()

	res, err := store.Confirm(context.Background(), input)
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if res == nil || res.FirstConfirmation {
		t.Fatalf("replay must return FirstConfirmation=false, got %+v", res)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGStore_Confirm_ReplayDifferentIdempotency_Rejected(t *testing.T) {
	store, mock, cleanup := newPGTestStore(t)
	defer cleanup()

	p := proposalFor("p1", "tenant-a")
	input := ConfirmationInput{
		ProposalID: p.ID, TenantID: p.TenantID, APIKeyID: p.APIKeyID,
		Token: "token-" + p.ID, NewSessionID: "gw_new",
		IdempotencyKey: "idem-2", MaxPerSession: 5,
	}

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
			p.ID, p.TenantID, p.APIKeyID, p.PreviousSessionID, p.TokenHash, confirmationStatusAccountingConfirmed,
			p.ExpiresAt, time.Now().UTC(), "gw_other", hashConfirmationValue("idem-1"),
			p.Record.TriggerMode, p.Record.TriggerReason, p.Record.TokensAtTrigger, p.Record.ContextWindow,
			p.Record.MessagesAtTrigger, p.Record.TokensInSession, p.Record.SummaryEngine, p.Record.SummaryText,
			p.Record.HandoffPrompt, p.Record.SkillName, p.Record.DurationMs, p.Record.CreatedAt,
			nil, nil, "", "", nil, nil,
		))
	mock.ExpectRollback()

	_, err := store.Confirm(context.Background(), input)
	if !errors.Is(err, ErrConfirmationReplay) {
		t.Fatalf("expected ErrConfirmationReplay, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// ── Confirm: budget / cooldown / expired ───────────────────────────────────

func TestPGStore_Confirm_BudgetExhausted(t *testing.T) {
	store, mock, cleanup := newPGTestStore(t)
	defer cleanup()

	p := proposalFor("p1", "tenant-a")
	input := ConfirmationInput{
		ProposalID: p.ID, TenantID: p.TenantID, APIKeyID: p.APIKeyID,
		Token: "token-" + p.ID, NewSessionID: "gw_new",
		IdempotencyKey: "idem-1", MaxPerSession: 3,
	}

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
		WillReturnRows(sqlmock.NewRows([]string{"handoff_count", "last_handoff_at"}).AddRow(3, nil))
	mock.ExpectRollback()

	_, err := store.Confirm(context.Background(), input)
	if !errors.Is(err, ErrConfirmationBudgetExhausted) {
		t.Fatalf("expected ErrConfirmationBudgetExhausted, got %v", err)
	}
}

func TestPGStore_Confirm_CooldownActive(t *testing.T) {
	store, mock, cleanup := newPGTestStore(t)
	defer cleanup()

	p := proposalFor("p1", "tenant-a")
	input := ConfirmationInput{
		ProposalID: p.ID, TenantID: p.TenantID, APIKeyID: p.APIKeyID,
		Token: "token-" + p.ID, NewSessionID: "gw_new",
		IdempotencyKey: "idem-1", MaxPerSession: 5, CooldownSeconds: 60,
	}
	recentHandoff := time.Now().Add(-10 * time.Second)

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
		WillReturnRows(sqlmock.NewRows([]string{"handoff_count", "last_handoff_at"}).AddRow(0, recentHandoff))
	mock.ExpectRollback()

	_, err := store.Confirm(context.Background(), input)
	if !errors.Is(err, ErrConfirmationCooldownActive) {
		t.Fatalf("expected ErrConfirmationCooldownActive, got %v", err)
	}
}

func TestPGStore_Confirm_Expired(t *testing.T) {
	store, mock, cleanup := newPGTestStore(t)
	defer cleanup()

	p := proposalFor("p1", "tenant-a")
	p.ExpiresAt = time.Now().Add(-time.Minute) // already expired
	input := ConfirmationInput{
		ProposalID: p.ID, TenantID: p.TenantID, APIKeyID: p.APIKeyID,
		Token: "token-" + p.ID, NewSessionID: "gw_new",
		IdempotencyKey: "idem-1", MaxPerSession: 5,
	}

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
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE handoff_pending_confirmations SET status='expired'`)).
		WithArgs(p.ID, p.TenantID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	_, err := store.Confirm(context.Background(), input)
	if !errors.Is(err, ErrConfirmationExpired) {
		t.Fatalf("expected ErrConfirmationExpired, got %v", err)
	}
}

// ── GetGoalRestoreState ─────────────────────────────────────────────────────

func TestPGStore_GetGoalRestoreState_NotFound(t *testing.T) {
	store, mock, cleanup := newPGTestStore(t)
	defer cleanup()

	mock.ExpectQuery(regexp.QuoteMeta(`FROM handoff_pending_confirmations WHERE id=$1 AND tenant_id=$2`)).
		WithArgs("missing", "tenant-a").
		WillReturnError(sql.ErrNoRows)

	_, err := store.GetGoalRestoreState(context.Background(), "missing", "tenant-a")
	if !errors.Is(err, ErrConfirmationInvalid) {
		t.Fatalf("expected ErrConfirmationInvalid on missing row, got %v", err)
	}
}

func TestPGStore_GetGoalRestoreState_CorruptSnapshot(t *testing.T) {
	store, mock, cleanup := newPGTestStore(t)
	defer cleanup()

	mock.ExpectQuery(regexp.QuoteMeta(`FROM handoff_pending_confirmations WHERE id=$1 AND tenant_id=$2`)).
		WithArgs("p1", "tenant-a").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "tenant_id", "new_session_id", "status",
			"goal_state", "goal_state_version", "restore_status", "restore_error",
			"restore_attempted_at", "restored_at",
		}).AddRow("p1", "tenant-a", "gw_new", confirmationStatusAccountingConfirmed,
			[]byte("not-valid-json"), GoalStateVersion, confirmationStatusAccountingConfirmed, "", nil, nil))

	_, err := store.GetGoalRestoreState(context.Background(), "p1", "tenant-a")
	if err == nil || !errors.Is(err, ErrGoalRestoreStateInvalid) {
		t.Fatalf("expected ErrGoalRestoreStateInvalid wrap on corrupt JSON, got %v", err)
	}
}

func TestPGStore_GetGoalRestoreState_VersionMismatch(t *testing.T) {
	store, mock, cleanup := newPGTestStore(t)
	defer cleanup()

	mock.ExpectQuery(regexp.QuoteMeta(`FROM handoff_pending_confirmations WHERE id=$1 AND tenant_id=$2`)).
		WithArgs("p1", "tenant-a").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "tenant_id", "new_session_id", "status",
			"goal_state", "goal_state_version", "restore_status", "restore_error",
			"restore_attempted_at", "restored_at",
		}).AddRow("p1", "tenant-a", "gw_new", confirmationStatusAccountingConfirmed,
			[]byte(`{}`), 9999, confirmationStatusAccountingConfirmed, "", nil, nil))

	_, err := store.GetGoalRestoreState(context.Background(), "p1", "tenant-a")
	if err == nil || !errors.Is(err, ErrGoalRestoreStateInvalid) {
		t.Fatalf("expected ErrGoalRestoreStateInvalid wrap on version mismatch, got %v", err)
	}
}

// ── MarkGoalRestored / MarkGoalRestoreManualRequired / Attempt ─────────────

func TestPGStore_MarkGoalRestored_TerminalTransition(t *testing.T) {
	store, mock, cleanup := newPGTestStore(t)
	defer cleanup()

	mock.ExpectExec(regexp.QuoteMeta(`UPDATE handoff_pending_confirmations SET status=$3,restore_status=$3,restore_error=NULL,restored_at=NOW(),updated_at=NOW() WHERE id=$1 AND tenant_id=$2 AND status IN ('accounting_confirmed','confirmed')`)).
		WithArgs("p1", "tenant-a", confirmationStatusRestored).
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := store.MarkGoalRestored(context.Background(), "p1", "tenant-a"); err != nil {
		t.Fatalf("MarkGoalRestored: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGStore_MarkGoalRestoreAttempt_PreservesRetryableStatus(t *testing.T) {
	store, mock, cleanup := newPGTestStore(t)
	defer cleanup()

	mock.ExpectExec(regexp.QuoteMeta(`UPDATE handoff_pending_confirmations SET restore_status=$3,restore_error=$4,restore_attempted_at=NOW(),updated_at=NOW() WHERE id=$1 AND tenant_id=$2 AND status IN ('accounting_confirmed','confirmed')`)).
		WithArgs("p1", "tenant-a", confirmationStatusAccountingConfirmed, "transient serializer error").
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := store.MarkGoalRestoreAttempt(context.Background(), "p1", "tenant-a", "transient serializer error"); err != nil {
		t.Fatalf("MarkGoalRestoreAttempt: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGStore_MarkGoalRestoreManualRequired_OnCorruption(t *testing.T) {
	store, mock, cleanup := newPGTestStore(t)
	defer cleanup()

	mock.ExpectExec(regexp.QuoteMeta(`UPDATE handoff_pending_confirmations SET status=$3,restore_status=$3,restore_error=$4,restore_attempted_at=NOW(),updated_at=NOW() WHERE id=$1 AND tenant_id=$2 AND status IN ('accounting_confirmed','confirmed')`)).
		WithArgs("p1", "tenant-a", confirmationStatusManualRequired, "tenant mismatch").
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := store.MarkGoalRestoreManualRequired(context.Background(), "p1", "tenant-a", "tenant mismatch"); err != nil {
		t.Fatalf("MarkGoalRestoreManualRequired: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGStore_MarkGoalRestoreManualRequired_NoopOnRestored(t *testing.T) {
	store, mock, cleanup := newPGTestStore(t)
	defer cleanup()

	// The row is already in status='restored', so the WHERE clause doesn't match.
	// sqlmock returns 0 rows affected; PGStore just returns nil.
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE handoff_pending_confirmations SET status=$3,restore_status=$3,restore_error=$4,restore_attempted_at=NOW(),updated_at=NOW() WHERE id=$1 AND tenant_id=$2 AND status IN ('accounting_confirmed','confirmed')`)).
		WithArgs("p1", "tenant-a", confirmationStatusManualRequired, "stale").
		WillReturnResult(sqlmock.NewResult(0, 0))

	if err := store.MarkGoalRestoreManualRequired(context.Background(), "p1", "tenant-a", "stale"); err != nil {
		t.Fatalf("MarkGoalRestoreManualRequired on already-restored row should be a silent no-op; got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}
