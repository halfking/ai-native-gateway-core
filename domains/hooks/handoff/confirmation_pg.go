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
	_, err := s.db.ExecContext(ctx, `
INSERT INTO handoff_pending_confirmations (id,tenant_id,api_key_id,previous_session_id,token_hash,status,expires_at,trigger_mode,trigger_reason,tokens_at_trigger,context_window,messages_at_trigger,tokens_in_session,summary_engine,summary_text,handoff_prompt,skill_name,duration_ms,proposal_created_at)
VALUES ($1,$2,$3,$4,$5,'pending',$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
ON CONFLICT (tenant_id,previous_session_id) WHERE status='pending' DO UPDATE SET
id=EXCLUDED.id,api_key_id=EXCLUDED.api_key_id,token_hash=EXCLUDED.token_hash,expires_at=EXCLUDED.expires_at,trigger_mode=EXCLUDED.trigger_mode,trigger_reason=EXCLUDED.trigger_reason,tokens_at_trigger=EXCLUDED.tokens_at_trigger,context_window=EXCLUDED.context_window,messages_at_trigger=EXCLUDED.messages_at_trigger,tokens_in_session=EXCLUDED.tokens_in_session,summary_engine=EXCLUDED.summary_engine,summary_text=EXCLUDED.summary_text,handoff_prompt=EXCLUDED.handoff_prompt,skill_name=EXCLUDED.skill_name,duration_ms=EXCLUDED.duration_ms,proposal_created_at=EXCLUDED.proposal_created_at,updated_at=NOW()`,
		p.ID, p.TenantID, p.APIKeyID, p.PreviousSessionID, p.TokenHash, p.ExpiresAt, p.Record.TriggerMode, p.Record.TriggerReason, p.Record.TokensAtTrigger, p.Record.ContextWindow, p.Record.MessagesAtTrigger, p.Record.TokensInSession, p.Record.SummaryEngine, p.Record.SummaryText, p.Record.HandoffPrompt, p.Record.SkillName, p.Record.DurationMs, p.Record.CreatedAt)
	return err
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
	err = tx.QueryRowContext(ctx, `SELECT id,tenant_id,api_key_id,previous_session_id,token_hash,status,expires_at,confirmed_at,new_session_id,idempotency_hash,trigger_mode,trigger_reason,tokens_at_trigger,context_window,messages_at_trigger,tokens_in_session,summary_engine,summary_text,handoff_prompt,skill_name,duration_ms,proposal_created_at FROM handoff_pending_confirmations WHERE id=$1 AND tenant_id=$2 FOR UPDATE`, in.ProposalID, in.TenantID).Scan(&p.ID, &p.TenantID, &p.APIKeyID, &p.PreviousSessionID, &p.TokenHash, &p.Status, &p.ExpiresAt, &confirmed, &target, &idem, &p.Record.TriggerMode, &p.Record.TriggerReason, &p.Record.TokensAtTrigger, &p.Record.ContextWindow, &p.Record.MessagesAtTrigger, &p.Record.TokensInSession, &p.Record.SummaryEngine, &p.Record.SummaryText, &p.Record.HandoffPrompt, &p.Record.SkillName, &p.Record.DurationMs, &p.Record.CreatedAt)
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
	if p.APIKeyID != in.APIKeyID || !p.MatchesToken(in.Token) {
		return nil, ErrConfirmationInvalid
	}
	inputHash := hashConfirmationValue(in.IdempotencyKey)
	if p.Status == "confirmed" {
		if p.IdempotencyHash == inputHash && p.NewSessionID == in.NewSessionID {
			return &ConfirmationResult{ProposalID: p.ID, PreviousSessionID: p.PreviousSessionID, NewSessionID: p.NewSessionID, ConfirmedAt: p.ConfirmedAt, Record: p.Record}, tx.Commit()
		}
		return nil, ErrConfirmationReplay
	}
	if !time.Now().Before(p.ExpiresAt) {
		_, err = tx.ExecContext(ctx, `UPDATE handoff_pending_confirmations SET status='expired',updated_at=NOW() WHERE id=$1`, p.ID)
		if err != nil {
			return nil, err
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		return nil, ErrConfirmationExpired
	}
	if p.Status != "pending" || p.PreviousSessionID == in.NewSessionID || (!in.TargetCreatedAt.IsZero() && in.TargetCreatedAt.Before(p.Record.CreatedAt)) {
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
	err = tx.QueryRowContext(ctx, `INSERT INTO handoff_logs (session_id,tenant_id,trigger_reason,tokens_at_handoff,context_window,handoff_prompt,new_session_id,summary_text,summary_engine,trigger_mode,tokens_in_session,messages_in_session,skill_name,duration_ms,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15) RETURNING id`, p.Record.SessionKey, p.Record.TenantID, p.Record.TriggerReason, p.Record.TokensAtTrigger, p.Record.ContextWindow, p.Record.HandoffPrompt, p.Record.NewSessionID, p.Record.SummaryText, p.Record.SummaryEngine, p.Record.TriggerMode, p.Record.TokensInSession, p.Record.MessagesAtTrigger, p.Record.SkillName, p.Record.DurationMs, p.Record.CreatedAt).Scan(&logID)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE session_summaries SET handoff_count=COALESCE(handoff_count,0)+1,last_handoff_at=$3,tokens_at_trigger=$4,messages_at_trigger=$5,last_trigger_reason=$6,last_trigger_at=$3 WHERE session_key=$1 AND tenant_id=$2`, p.PreviousSessionID, p.TenantID, now, p.Record.TokensAtTrigger, p.Record.MessagesAtTrigger, p.Record.TriggerReason)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE handoff_pending_confirmations SET status='confirmed',confirmed_at=$2,new_session_id=$3,idempotency_hash=$4,handoff_log_id=$5,updated_at=NOW() WHERE id=$1 AND status='pending'`, p.ID, now, in.NewSessionID, inputHash, logID)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return &ConfirmationResult{ProposalID: p.ID, PreviousSessionID: p.PreviousSessionID, NewSessionID: in.NewSessionID, ConfirmedAt: now, FirstConfirmation: true, Record: p.Record}, nil
}
