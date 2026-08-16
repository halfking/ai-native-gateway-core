package durable

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kaixuan/llm-gateway-go/secret"
)

// SettlementIntent is the durable write-ahead record for a terminal result.
// SourceOwner and SourceFencingToken are frozen from the original task lease.
type SettlementIntent struct {
	TaskID                                      string
	TenantID, RequestID, SessionID, RequestHash string
	SourceOwner                                 string
	SourceFencingToken                          int64
	Outcome                                     Status
	Body                                        []byte
	ContentType, ReasonCode, ErrorKind          string
	Attempt                                     int
	ResultHash                                  string
	ResultCiphertext                            string
	Attempts                                    int
}

// ClaimedSettlement is an intent held by an independent outbox lease.
type ClaimedSettlement struct {
	SettlementIntent
	ClaimOwner        string
	ClaimUntil        time.Time
	ClaimFencingToken int64
}

func (s *Store) PersistSettlementIntent(ctx context.Context, c TerminalCommit) error {
	if c.Task == nil {
		return errors.New("durable: settlement intent requires task")
	}
	if !IsTerminalStatus(c.Outcome) {
		return fmt.Errorf("durable: outcome %q is not terminal", c.Outcome)
	}
	if c.Outcome == StatusCompleted && len(c.Body) == 0 {
		return errors.New("durable: completed settlement requires result body")
	}
	if s.kr == nil {
		return ErrNoKeyring
	}
	h := sha256.Sum256(c.Body)
	var ciphertext any
	var keyID string
	if len(c.Body) > 0 {
		env, kid, err := secret.EncryptWithAAD(c.Body, s.kr, secret.AADDomainDurableResult,
			secret.AADBinding{TenantID: c.Task.TenantID, TaskID: c.Task.ID, RequestHash: c.Task.RequestHash})
		if err != nil {
			return fmt.Errorf("durable: encrypt settlement: %w", err)
		}
		ciphertext, keyID = env, kid
	}
	tag, err := s.db.Exec(ctx, `
		INSERT INTO durable_task_settlement_intents (
			task_id, tenant_id, request_id, session_id, request_hash,
			source_lease_owner, source_fencing_token, outcome, result_ciphertext,
			encryption_key_id, content_type, reason_code, error_kind, attempt, result_hash,
			next_attempt_at, created_at, updated_at
		) SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$16,$16
		WHERE EXISTS (
			SELECT 1 FROM durable_llm_tasks
			WHERE id=$1 AND lease_owner=$6 AND fencing_token=$7
			  AND status NOT IN ('completed','failed','expired','canceled')
		)
		ON CONFLICT (task_id) DO UPDATE SET updated_at = EXCLUDED.updated_at
		WHERE durable_task_settlement_intents.source_lease_owner = EXCLUDED.source_lease_owner
		  AND durable_task_settlement_intents.source_fencing_token = EXCLUDED.source_fencing_token
		  AND durable_task_settlement_intents.outcome = EXCLUDED.outcome
		  AND durable_task_settlement_intents.result_hash = EXCLUDED.result_hash`,
		c.Task.ID, c.Task.TenantID, c.Task.RequestID, c.Task.SessionID, c.Task.RequestHash,
		c.Task.LeaseOwner, c.Task.FencingToken, string(c.Outcome), ciphertext, keyID,
		c.ContentType, c.ReasonCode, c.ErrorKind, c.Attempt, hex.EncodeToString(h[:]), s.clock())
	if err != nil {
		return fmt.Errorf("durable: persist settlement intent: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrLeaseLost
	}
	return nil
}

// ClaimSettlementIntent claims one specific pending intent with an independent
// lease. Foreground settlement uses it so it can never lease another task's
// recovery work.
func (s *Store) ClaimSettlementIntent(ctx context.Context, taskID, owner string, lease time.Duration, now time.Time) (*ClaimedSettlement, error) {
	if taskID == "" {
		return nil, errors.New("durable: settlement task id required")
	}
	claims, err := s.claimSettlementIntents(ctx, taskID, owner, lease, 1, now)
	if err != nil || len(claims) == 0 {
		return nil, err
	}
	return claims[0], nil
}

// ClaimSettlementIntents claims pending intents with SKIP LOCKED and an
// independent lease/fencing token. It never changes the source task lease.
func (s *Store) ClaimSettlementIntents(ctx context.Context, owner string, lease time.Duration, limit int, now time.Time) ([]*ClaimedSettlement, error) {
	return s.claimSettlementIntents(ctx, "", owner, lease, limit, now)
}

func (s *Store) claimSettlementIntents(ctx context.Context, taskID, owner string, lease time.Duration, limit int, now time.Time) ([]*ClaimedSettlement, error) {
	if owner == "" {
		return nil, errors.New("durable: settlement claim owner required")
	}
	if lease <= 0 {
		lease = time.Minute
	}
	if limit <= 0 {
		limit = 8
	}
	if now.IsZero() {
		now = s.clock()
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("durable: begin settlement claim: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck
	query := `
		SELECT task_id, tenant_id, request_id, session_id, request_hash,
			source_lease_owner, source_fencing_token, outcome, result_ciphertext,
			encryption_key_id, content_type, reason_code, error_kind, attempt, result_hash,
			claim_fencing_token, attempts
		FROM durable_task_settlement_intents
		WHERE next_attempt_at <= $2 AND (claim_until IS NULL OR claim_until < $2)`
	args := []any{limit, now}
	if taskID != "" {
		query += " AND task_id=$3"
		args = append(args, taskID)
	}
	query += " ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT $1"
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("durable: select settlement intents: %w", err)
	}
	defer rows.Close()
	var out []*ClaimedSettlement
	for rows.Next() {
		var c ClaimedSettlement
		var ct, kid, content, reason, kind pgtype.Text
		if err := rows.Scan(&c.TaskID, &c.TenantID, &c.RequestID, &c.SessionID, &c.RequestHash, &c.SourceOwner, &c.SourceFencingToken, &c.Outcome, &ct, &kid, &content, &reason, &kind, &c.Attempt, &c.ResultHash, &c.ClaimFencingToken, &c.Attempts); err != nil {
			return nil, err
		}
		if ct.Valid {
			c.ResultCiphertext = ct.String
		}
		c.ClaimOwner, c.ClaimUntil = owner, now.Add(lease)
		c.ClaimFencingToken++
		if err := tx.QueryRow(ctx, `UPDATE durable_task_settlement_intents SET claim_owner=$2, claim_until=$3, claim_fencing_token=$4, attempts=attempts+1, updated_at=$5 WHERE task_id=$1 RETURNING claim_fencing_token, attempts`, c.TaskID, owner, c.ClaimUntil, c.ClaimFencingToken, now).Scan(&c.ClaimFencingToken, &c.Attempts); err != nil {
			return nil, err
		}
		out = append(out, &c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("durable: commit settlement claim: %w", err)
	}
	return out, nil
}

// RetrySettlementIntent releases a failed repair claim with durable backoff.
// It intentionally retains the intent: terminal settlement is not execution retry.
func (s *Store) RetrySettlementIntent(ctx context.Context, c ClaimedSettlement, next time.Time, cause error) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE durable_task_settlement_intents
		SET claim_owner=NULL, claim_until=NULL,
			last_error=$4, next_attempt_at=$5, updated_at=$5
		WHERE task_id=$1 AND claim_owner=$2 AND claim_fencing_token=$3`,
		c.TaskID, c.ClaimOwner, c.ClaimFencingToken, fmt.Sprint(cause), next)
	if err != nil {
		return fmt.Errorf("durable: retry settlement intent: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrLeaseLost
	}
	return nil
}

// FinalizeSettlement atomically applies the persisted, claimed intent using
// its frozen source owner/token, writes the lifecycle event and pending
// projection outbox, then removes the intent. The caller supplies only the
// independent claim fence; all terminal content and metadata are re-read from
// the locked intent row.
func (s *Store) FinalizeSettlement(ctx context.Context, claim ClaimedSettlement) (*TerminalProjection, error) {
	now := s.clock()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck

	var c ClaimedSettlement
	var ciphertext, keyID pgtype.Text
	if err := tx.QueryRow(ctx, `
		SELECT task_id, tenant_id, request_id, session_id, request_hash,
			source_lease_owner, source_fencing_token, outcome, result_ciphertext,
			encryption_key_id, content_type, reason_code, error_kind, attempt, result_hash, attempts
		FROM durable_task_settlement_intents
		WHERE task_id=$1 AND claim_owner=$2 AND claim_fencing_token=$3
		FOR UPDATE`, claim.TaskID, claim.ClaimOwner, claim.ClaimFencingToken).Scan(
		&c.TaskID, &c.TenantID, &c.RequestID, &c.SessionID, &c.RequestHash,
		&c.SourceOwner, &c.SourceFencingToken, &c.Outcome, &ciphertext, &keyID,
		&c.ContentType, &c.ReasonCode, &c.ErrorKind, &c.Attempt, &c.ResultHash, &c.Attempts,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrLeaseLost
		}
		return nil, fmt.Errorf("durable: lock settlement intent: %w", err)
	}
	c.ClaimOwner, c.ClaimFencingToken = claim.ClaimOwner, claim.ClaimFencingToken
	if !IsTerminalStatus(c.Outcome) {
		return nil, fmt.Errorf("durable: persisted outcome %q is not terminal", c.Outcome)
	}
	if ciphertext.Valid {
		if s.kr == nil {
			return nil, ErrNoKeyring
		}
		body, _, err := secret.DecryptWithAAD(ciphertext.String, s.kr, secret.AADDomainDurableResult,
			secret.AADBinding{TenantID: c.TenantID, TaskID: c.TaskID, RequestHash: c.RequestHash})
		if err != nil {
			return nil, fmt.Errorf("durable: decrypt settlement %s: %w", c.TaskID, err)
		}
		c.Body = body
		c.ResultCiphertext = ciphertext.String
	}
	h := sha256.Sum256(c.Body)
	if c.ResultHash != hex.EncodeToString(h[:]) {
		return nil, fmt.Errorf("durable: settlement result hash mismatch for %s", c.TaskID)
	}
	var storedCiphertext any
	if ciphertext.Valid {
		storedCiphertext = ciphertext.String
	}
	var version int64
	err = tx.QueryRow(ctx, `UPDATE durable_llm_tasks SET status=$4, result_ciphertext=$5, result_hash=$6, content_type=$7, completed_at=$8, reason_code=$9, error_kind=$10, result_version=COALESCE(result_version,0)+1, commit_state='terminal', semantic_content_committed=TRUE, attempt_count=$11, lease_owner=NULL, lease_until=NULL, updated_at=$12 WHERE id=$1 AND lease_owner=$2 AND fencing_token=$3 AND status NOT IN ('completed','failed','expired','canceled') RETURNING result_version`, c.TaskID, c.SourceOwner, c.SourceFencingToken, string(c.Outcome), storedCiphertext, c.ResultHash, c.ContentType, now, c.ReasonCode, c.ErrorKind, c.Attempt, now).Scan(&version)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			if _, deleteErr := tx.Exec(ctx, `DELETE FROM durable_task_settlement_intents WHERE task_id=$1 AND claim_owner=$2 AND claim_fencing_token=$3`, c.TaskID, c.ClaimOwner, c.ClaimFencingToken); deleteErr != nil {
				return nil, fmt.Errorf("durable: discard stale settlement intent: %w", deleteErr)
			}
			if commitErr := tx.Commit(ctx); commitErr != nil {
				return nil, fmt.Errorf("durable: commit stale settlement discard: %w", commitErr)
			}
			return nil, ErrLeaseLost
		}
		return nil, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO durable_llm_task_events (task_id,request_id,session_id,tenant_id,attempt,from_status,to_status,reason,fencing_token,created_at) VALUES ($1,$2,$3,$4,$5,'running',$6,$7,$8,$9)`, c.TaskID, c.RequestID, c.SessionID, c.TenantID, c.Attempt, string(c.Outcome), orDefault(c.ReasonCode, string(c.Outcome)), c.SourceFencingToken, now); err != nil {
		return nil, err
	}
	if err := enqueuePendingOutbox(ctx, tx, c.TaskID, c.TenantID, version, now); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM durable_task_settlement_intents WHERE task_id=$1 AND claim_owner=$2 AND claim_fencing_token=$3`, c.TaskID, c.ClaimOwner, c.ClaimFencingToken); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &TerminalProjection{Committed: true, TaskID: c.TaskID, TenantID: c.TenantID, RequestID: c.RequestID, SessionID: c.SessionID, RequestHash: c.RequestHash, Status: c.Outcome, Body: string(c.Body), ContentType: c.ContentType, ReasonCode: c.ReasonCode, FencingToken: c.SourceFencingToken, ResultVersion: version, ResultHash: c.ResultHash, CompletedAt: now}, nil
}
