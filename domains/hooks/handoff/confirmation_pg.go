package handoff

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

func (s *PGStore) SavePending(ctx context.Context, p *ConfirmationProposal) error {
	if s == nil || s.db == nil || p == nil {
		return fmt.Errorf("handoff confirmation store is unavailable")
	}
	goalState, goalVersion, err := marshalPersistedGoalState(p.GoalState)
	if err != nil {
		return err
	}
	restoreStatus := any(nil)
	if p.GoalState != nil {
		restoreStatus = confirmationStatusPending
	}
	_, err = s.db.ExecContext(ctx, `
	INSERT INTO handoff_pending_confirmations (id,tenant_id,api_key_id,previous_session_id,token_hash,status,expires_at,trigger_mode,trigger_reason,tokens_at_trigger,context_window,messages_at_trigger,tokens_in_session,summary_engine,summary_text,handoff_prompt,skill_name,duration_ms,proposal_created_at,goal_state,goal_state_version,restore_status)
	VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20::text::jsonb,$21,$22)
	ON CONFLICT (tenant_id,previous_session_id) WHERE status='pending' DO UPDATE SET
	id=EXCLUDED.id,api_key_id=EXCLUDED.api_key_id,token_hash=EXCLUDED.token_hash,expires_at=EXCLUDED.expires_at,trigger_mode=EXCLUDED.trigger_mode,trigger_reason=EXCLUDED.trigger_reason,tokens_at_trigger=EXCLUDED.tokens_at_trigger,context_window=EXCLUDED.context_window,messages_at_trigger=EXCLUDED.messages_at_trigger,tokens_in_session=EXCLUDED.tokens_in_session,summary_engine=EXCLUDED.summary_engine,summary_text=EXCLUDED.summary_text,handoff_prompt=EXCLUDED.handoff_prompt,skill_name=EXCLUDED.skill_name,duration_ms=EXCLUDED.duration_ms,proposal_created_at=EXCLUDED.proposal_created_at,goal_state=EXCLUDED.goal_state,goal_state_version=EXCLUDED.goal_state_version,restore_status=EXCLUDED.restore_status,restore_error=NULL,restore_attempted_at=NULL,restored_at=NULL,updated_at=NOW()`,
		p.ID, p.TenantID, p.APIKeyID, p.PreviousSessionID, p.TokenHash, confirmationStatusPending, p.ExpiresAt, p.Record.TriggerMode, p.Record.TriggerReason, p.Record.TokensAtTrigger, p.Record.ContextWindow, p.Record.MessagesAtTrigger, p.Record.TokensInSession, p.Record.SummaryEngine, p.Record.SummaryText, p.Record.HandoffPrompt, p.Record.SkillName, p.Record.DurationMs, p.Record.CreatedAt, jsonbBindArg(goalState), goalVersion, restoreStatus)
	return err
}

// jsonbBindArg converts a []byte JSON payload into the binding form required by
// the gateway pool's pgx QueryExecModeSimpleProtocol: []byte would be inlined
// as bytea hex and any jsonb cast then fails with 22P02
// ("invalid input syntax for type json"; doc §3.2, internal/dbx/jsonb.go).
// The string form flows through $N::text::jsonb; an empty payload stays SQL
// NULL so the `goal_state jsonb` column keeps its historical NULL semantics
// for proposals without a goal state.
func jsonbBindArg(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return string(b)
}

func (s *PGStore) Confirm(ctx context.Context, in ConfirmationInput) (*ConfirmationResult, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("handoff confirmation store is unavailable")
	}
	if in.ProposalID == "" || in.TenantID == "" || in.APIKeyID <= 0 || in.Token == "" || in.NewSessionID == "" || in.IdempotencyKey == "" {
		return nil, ErrConfirmationInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var p ConfirmationProposal
	var confirmed sql.NullTime
	var target, idem sql.NullString
	var goalPayload []byte
	var goalVersion sql.NullInt64
	var restoreStatus, restoreError sql.NullString
	var restoreAttemptedAt, restoredAt sql.NullTime
	err = tx.QueryRowContext(ctx, `SELECT id,tenant_id,api_key_id,previous_session_id,token_hash,status,expires_at,confirmed_at,new_session_id,idempotency_hash,trigger_mode,trigger_reason,tokens_at_trigger,context_window,messages_at_trigger,tokens_in_session,summary_engine,summary_text,handoff_prompt,skill_name,duration_ms,proposal_created_at,goal_state,goal_state_version,restore_status,restore_error,restore_attempted_at,restored_at FROM handoff_pending_confirmations WHERE id=$1 AND tenant_id=$2 FOR UPDATE`, in.ProposalID, in.TenantID).Scan(&p.ID, &p.TenantID, &p.APIKeyID, &p.PreviousSessionID, &p.TokenHash, &p.Status, &p.ExpiresAt, &confirmed, &target, &idem, &p.Record.TriggerMode, &p.Record.TriggerReason, &p.Record.TokensAtTrigger, &p.Record.ContextWindow, &p.Record.MessagesAtTrigger, &p.Record.TokensInSession, &p.Record.SummaryEngine, &p.Record.SummaryText, &p.Record.HandoffPrompt, &p.Record.SkillName, &p.Record.DurationMs, &p.Record.CreatedAt, &goalPayload, &goalVersion, &restoreStatus, &restoreError, &restoreAttemptedAt, &restoredAt)
	if err == sql.ErrNoRows {
		return nil, ErrConfirmationInvalid
	}
	if err != nil {
		return nil, err
	}
	p.Record.SessionKey, p.Record.TenantID = p.PreviousSessionID, p.TenantID
	p.ConfirmedAt = confirmed.Time
	p.NewSessionID = target.String
	p.IdempotencyHash = idem.String
	p.GoalStateVersion = int(goalVersion.Int64)
	p.RestoreStatus = restoreStatus.String
	p.RestoreError = restoreError.String
	// Materialise the persisted goal state so the accounting-confirm UPDATE
	// writes a real restore_status when goal_state bytes are present. The
	// in-row GoalState is only otherwise unmarshalled by GetGoalRestoreState,
	// so Confirm saw p.GoalState==nil and wrote NULL on the first confirm.
	if goalState, err := unmarshalPersistedGoalState(goalPayload, int(goalVersion.Int64)); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrGoalRestoreStateInvalid, err)
	} else {
		p.GoalState = goalState
	}
	if p.APIKeyID != in.APIKeyID || !p.MatchesToken(in.Token) {
		return nil, ErrConfirmationInvalid
	}
	inputHash := hashConfirmationValue(in.IdempotencyKey)
	if p.Status == legacyConfirmationStatusConfirmed || p.Status == confirmationStatusAccountingConfirmed || p.Status == confirmationStatusRestored || p.Status == confirmationStatusManualRequired {
		if p.IdempotencyHash == inputHash && p.NewSessionID == in.NewSessionID {
			result := confirmationResult(&p, false)
			return result, tx.Commit()
		}
		return nil, ErrConfirmationReplay
	}
	if !time.Now().Before(p.ExpiresAt) {
		_, err = tx.ExecContext(ctx, `UPDATE handoff_pending_confirmations SET status='expired',updated_at=NOW() WHERE id=$1 AND tenant_id=$2`, p.ID, p.TenantID)
		if err != nil {
			return nil, err
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		return nil, ErrConfirmationExpired
	}
	if p.Status != confirmationStatusPending || p.PreviousSessionID == in.NewSessionID || (!in.TargetCreatedAt.IsZero() && in.TargetCreatedAt.Truncate(time.Second).Before(p.Record.CreatedAt.Truncate(time.Second))) {
		return nil, ErrConfirmationInvalid
	}
	var count int
	var last sql.NullTime
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(handoff_count,0),last_handoff_at FROM session_summaries WHERE session_key=$1 AND tenant_id=$2 FOR UPDATE`, p.PreviousSessionID, p.TenantID).Scan(&count, &last)
	if err == sql.ErrNoRows {
		return nil, ErrConfirmationInvalid
	}
	if err != nil {
		return nil, err
	}
	if in.MaxPerSession > 0 && count >= in.MaxPerSession {
		return nil, ErrConfirmationBudgetExhausted
	}
	now := time.Now().UTC()
	if in.CooldownSeconds > 0 && last.Valid && now.Sub(last.Time) < time.Duration(in.CooldownSeconds)*time.Second {
		return nil, ErrConfirmationCooldownActive
	}
	p.Record.NewSessionID, p.Record.CreatedAt = in.NewSessionID, now
	var logID int64
	err = tx.QueryRowContext(ctx, `INSERT INTO handoff_logs_hot (session_id,tenant_id,trigger_reason,tokens_at_handoff,context_window,handoff_prompt,new_session_id,summary_text,summary_engine,trigger_mode,tokens_in_session,messages_in_session,skill_name,duration_ms,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15) RETURNING id`, p.Record.SessionKey, p.Record.TenantID, p.Record.TriggerReason, p.Record.TokensAtTrigger, p.Record.ContextWindow, p.Record.HandoffPrompt, p.Record.NewSessionID, p.Record.SummaryText, p.Record.SummaryEngine, p.Record.TriggerMode, p.Record.TokensInSession, p.Record.MessagesAtTrigger, p.Record.SkillName, p.Record.DurationMs, p.Record.CreatedAt).Scan(&logID)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE session_summaries SET handoff_count=COALESCE(handoff_count,0)+1,last_handoff_at=$3,tokens_at_trigger=$4,messages_at_trigger=$5,last_trigger_reason=$6,last_trigger_at=$3 WHERE session_key=$1 AND tenant_id=$2`, p.PreviousSessionID, p.TenantID, now, p.Record.TokensAtTrigger, p.Record.MessagesAtTrigger, p.Record.TriggerReason)
	if err != nil {
		return nil, err
	}
	newRestoreStatus := any(nil)
	if p.GoalState != nil {
		newRestoreStatus = confirmationStatusAccountingConfirmed
	}
	_, err = tx.ExecContext(ctx, `UPDATE handoff_pending_confirmations SET status=$2,confirmed_at=$3,new_session_id=$4,idempotency_hash=$5,handoff_log_id=$6,restore_status=$7,updated_at=NOW() WHERE id=$1 AND tenant_id=$8 AND status='pending'`, p.ID, confirmationStatusAccountingConfirmed, now, in.NewSessionID, inputHash, logID, newRestoreStatus, p.TenantID)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	p.Status = confirmationStatusAccountingConfirmed
	p.RestoreStatus = stringValue(newRestoreStatus)
	p.ConfirmedAt = now
	// Mirror MemoryConfirmationStore.Confirm: populate the proposal-level
	// NewSessionID from the input on first confirmation so downstream
	// ConfirmationResult consumers see the right value. Without this the
	// scanned target is empty (the DB row was pending) and
	// confirmationResult would return NewSessionID="".
	p.NewSessionID = in.NewSessionID
	return confirmationResult(&p, true), nil
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	return value.(string)
}

func (s *PGStore) GetGoalRestoreState(ctx context.Context, proposalID, tenantID string) (*GoalRestoreState, error) {
	if s == nil || s.db == nil || proposalID == "" || tenantID == "" {
		return nil, ErrConfirmationInvalid
	}
	var state GoalRestoreState
	var payload []byte
	var version sql.NullInt64
	var rowStatus, rowRestoreStatus sql.NullString
	var restoreError sql.NullString
	var attempted, restored sql.NullTime
	if err := s.db.QueryRowContext(ctx, `SELECT id,tenant_id,new_session_id,status,goal_state,goal_state_version,restore_status,restore_error,restore_attempted_at,restored_at FROM handoff_pending_confirmations WHERE id=$1 AND tenant_id=$2`, proposalID, tenantID).Scan(&state.ProposalID, &state.TenantID, &state.NewSessionID, &rowStatus, &payload, &version, &rowRestoreStatus, &restoreError, &attempted, &restored); err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrConfirmationInvalid
		}
		return nil, err
	}
	state.Status = rowRestoreStatus.String
	if state.Status == "" {
		state.Status = rowStatus.String
	}
	state.RestoreError = restoreError.String
	state.RestoreAttemptedAt = attempted.Time
	state.RestoredAt = restored.Time
	goalState, err := unmarshalPersistedGoalState(payload, int(version.Int64))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrGoalRestoreStateInvalid, err)
	}
	state.GoalState = goalState
	return &state, nil
}

func (s *PGStore) MarkGoalRestoreAttempt(ctx context.Context, proposalID, tenantID, restoreErr string) error {
	if s == nil || s.db == nil || proposalID == "" || tenantID == "" {
		return ErrConfirmationInvalid
	}
	_, err := s.db.ExecContext(ctx, `UPDATE handoff_pending_confirmations SET restore_status=$3,restore_error=$4,restore_attempted_at=NOW(),updated_at=NOW() WHERE id=$1 AND tenant_id=$2 AND status IN ('accounting_confirmed','confirmed')`, proposalID, tenantID, confirmationStatusAccountingConfirmed, truncateRunes(restoreErr, 512))
	return err
}

func (s *PGStore) MarkGoalRestored(ctx context.Context, proposalID, tenantID string) error {
	if s == nil || s.db == nil || proposalID == "" || tenantID == "" {
		return ErrConfirmationInvalid
	}
	_, err := s.db.ExecContext(ctx, `UPDATE handoff_pending_confirmations SET status=$3,restore_status=$3,restore_error=NULL,restored_at=NOW(),updated_at=NOW() WHERE id=$1 AND tenant_id=$2 AND status IN ('accounting_confirmed','confirmed')`, proposalID, tenantID, confirmationStatusRestored)
	return err
}

func (s *PGStore) MarkGoalRestoreManualRequired(ctx context.Context, proposalID, tenantID, restoreErr string) error {
	if s == nil || s.db == nil || proposalID == "" || tenantID == "" {
		return ErrConfirmationInvalid
	}
	_, err := s.db.ExecContext(ctx, `UPDATE handoff_pending_confirmations SET status=$3,restore_status=$3,restore_error=$4,restore_attempted_at=NOW(),updated_at=NOW() WHERE id=$1 AND tenant_id=$2 AND status IN ('accounting_confirmed','confirmed')`, proposalID, tenantID, confirmationStatusManualRequired, truncateRunes(restoreErr, 512))
	return err
}
