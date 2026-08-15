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
	"github.com/kaixuan/llm-gateway-go/pending"
	"github.com/kaixuan/llm-gateway-go/secret"
)

func enqueuePendingOutbox(ctx context.Context, tx pgx.Tx, taskID, tenantID string, resultVersion int64, now time.Time) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO durable_pending_outbox (
			task_id, tenant_id, result_version, next_attempt_at, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $4, $4)
		ON CONFLICT (task_id) DO UPDATE
		SET result_version = GREATEST(durable_pending_outbox.result_version, EXCLUDED.result_version),
		    next_attempt_at = LEAST(durable_pending_outbox.next_attempt_at, EXCLUDED.next_attempt_at),
		    updated_at = EXCLUDED.updated_at`,
		taskID, tenantID, resultVersion, now)
	if err != nil {
		return fmt.Errorf("durable: enqueue pending outbox: %w", err)
	}
	return nil
}

// ProjectPendingOutbox repairs terminal PendingStore projections. It reads the
// authoritative encrypted result from PostgreSQL, verifies its hash, then uses
// PendingStore's Lua CAS. Redis failures only advance the outbox retry fields.
func (s *Store) ProjectPendingOutbox(ctx context.Context, target *pending.Store, limit int, now time.Time) (int, error) {
	if target == nil || !target.Enabled() {
		return 0, pending.ErrUnavailable
	}
	if limit <= 0 {
		limit = 32
	}
	if now.IsZero() {
		now = s.clock()
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("durable: begin pending repair: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck

	rows, err := tx.Query(ctx, `
		SELECT o.task_id, o.result_version,
		       t.tenant_id, t.request_id, t.session_id, t.status,
		       t.result_ciphertext, t.request_hash, t.result_hash, t.content_type,
		       t.reason_code, t.fencing_token, t.completed_at, t.expires_at
		FROM durable_pending_outbox o
		JOIN durable_llm_tasks t ON t.id = o.task_id
		WHERE o.next_attempt_at <= $2
		ORDER BY o.created_at
		FOR UPDATE OF o SKIP LOCKED
		LIMIT $1`, limit, now)
	if err != nil {
		return 0, fmt.Errorf("durable: select pending outbox: %w", err)
	}

	type rowData struct {
		taskID, tenantID, requestID, sessionID string
		status                                 Status
		resultCT                               pgtype.Text
		requestHash                            string
		resultHash, contentType                pgtype.Text
		reasonCode                             string
		fencingToken, resultVersion            int64
		completedAt                            pgtype.Timestamptz
		expiresAt                              time.Time
	}
	var batch []rowData
	for rows.Next() {
		var r rowData
		if err := rows.Scan(&r.taskID, &r.resultVersion, &r.tenantID, &r.requestID, &r.sessionID,
			&r.status, &r.resultCT, &r.requestHash, &r.resultHash, &r.contentType,
			&r.reasonCode, &r.fencingToken, &r.completedAt, &r.expiresAt); err != nil {
			rows.Close()
			return 0, fmt.Errorf("durable: scan pending outbox: %w", err)
		}
		batch = append(batch, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("durable: pending outbox rows: %w", err)
	}

	projected := 0
	for _, r := range batch {
		resp := &pending.Response{
			SessionID: r.sessionID, TenantID: r.tenantID, RequestID: r.requestID,
			ContentType: r.contentType.String, RequestHash: r.requestHash,
			TaskID: r.taskID, FencingToken: r.fencingToken,
			ResultVersion: r.resultVersion, ResultHash: r.resultHash.String,
		}
		if r.completedAt.Valid {
			resp.CompletedAt = r.completedAt.Time.Unix()
		} else {
			resp.CompletedAt = now.Unix()
		}
		if r.status == StatusCompleted {
			if !r.resultCT.Valid || s.kr == nil {
				if err := markProjectionFailed(ctx, tx, r.taskID, now, "completed result cannot be decrypted"); err != nil {
					return projected, err
				}
				continue
			}
			body, _, err := secret.DecryptWithAAD(r.resultCT.String, s.kr, secret.AADDomainDurableResult,
				secret.AADBinding{TenantID: r.tenantID, TaskID: r.taskID, RequestHash: r.requestHash})
			if err != nil {
				if markErr := markProjectionFailed(ctx, tx, r.taskID, now, err.Error()); markErr != nil {
					return projected, markErr
				}
				continue
			}
			hash := sha256.Sum256(body)
			if !r.resultHash.Valid || hex.EncodeToString(hash[:]) != r.resultHash.String {
				if err := markProjectionFailed(ctx, tx, r.taskID, now, "result hash mismatch"); err != nil {
					return projected, err
				}
				continue
			}
			resp.Status = pending.StatusCompleted
			resp.Body = string(body)
		} else {
			resp.Status = pending.StatusFailed
			resp.ErrorMessage = r.reasonCode
		}

		decision, err := target.SaveDurableCAS(ctx, resp, r.expiresAt)
		if err != nil || decision == pending.DurableCASRejectedTask {
			message := "pending CAS rejected task"
			if err != nil {
				message = err.Error()
			}
			if markErr := markProjectionFailed(ctx, tx, r.taskID, now, message); markErr != nil {
				return projected, markErr
			}
			continue
		}
		if _, err := tx.Exec(ctx, `DELETE FROM durable_pending_outbox WHERE task_id = $1`, r.taskID); err != nil {
			return projected, fmt.Errorf("durable: delete pending outbox: %w", err)
		}
		projected++
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("durable: commit pending repair: %w", err)
	}
	return projected, nil
}

func markProjectionFailed(ctx context.Context, tx pgx.Tx, taskID string, now time.Time, message string) error {
	if message == "" {
		message = errors.New("unknown projection error").Error()
	}
	_, err := tx.Exec(ctx, `
		UPDATE durable_pending_outbox
		SET attempts = attempts + 1, last_error = $2,
		    next_attempt_at = $3, updated_at = $3
		WHERE task_id = $1`, taskID, message, now.Add(time.Minute))
	if err != nil {
		return fmt.Errorf("durable: mark pending projection failed: %w", err)
	}
	return nil
}
