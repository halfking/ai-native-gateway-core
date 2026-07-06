// Package session — session_db_writer.go
package session

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DBWriter 批量写入凭据轮换记录
type DBWriter struct {
	db            *pgxpool.Pool
	batchSize     int
	flushInterval time.Duration

	mu      sync.Mutex
	pending map[string][]CredRotationEntry
	stopCh  chan struct{}
	doneCh  chan struct{}
}

func NewDBWriter(db *pgxpool.Pool, batchSize int, flushInterval time.Duration) *DBWriter {
	if batchSize <= 0 {
		batchSize = 10
	}
	if flushInterval <= 0 {
		flushInterval = 60 * time.Second
	}
	return &DBWriter{
		db:            db,
		batchSize:     batchSize,
		flushInterval: flushInterval,
		pending:       make(map[string][]CredRotationEntry),
		stopCh:        make(chan struct{}),
		doneCh:        make(chan struct{}),
	}
}

func (w *DBWriter) Start(ctx context.Context) {
	go w.runFlushLoop(ctx)
}

func (w *DBWriter) Stop() {
	if w == nil {
		return
	}
	close(w.stopCh)
	<-w.doneCh
}

func (w *DBWriter) Enqueue(sessionID, tenantID string, entry CredRotationEntry) {
	if w == nil || w.db == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pending[sessionID] = append(w.pending[sessionID], entry)
	if len(w.pending[sessionID]) >= w.batchSize {
		entries := w.pending[sessionID]
		delete(w.pending, sessionID)
		go w.flushSession(context.Background(), sessionID, tenantID, entries)
	}
}

func (w *DBWriter) FlushSession(ctx context.Context, sessionID, tenantID string) {
	if w == nil {
		return
	}
	w.mu.Lock()
	entries := w.pending[sessionID]
	delete(w.pending, sessionID)
	w.mu.Unlock()
	if len(entries) > 0 {
		w.flushSession(ctx, sessionID, tenantID, entries)
	}
}

func (w *DBWriter) FlushAll(ctx context.Context) {
	if w == nil {
		return
	}
	w.mu.Lock()
	snapshot := make(map[string][]CredRotationEntry, len(w.pending))
	for k, v := range w.pending {
		snapshot[k] = v
	}
	w.pending = make(map[string][]CredRotationEntry)
	w.mu.Unlock()
	for sessionID, entries := range snapshot {
		w.flushSession(ctx, sessionID, "", entries)
	}
}

func (w *DBWriter) runFlushLoop(ctx context.Context) {
	defer close(w.doneCh)
	ticker := time.NewTicker(w.flushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-w.stopCh:
			w.FlushAll(context.Background())
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.flushAllInternal(ctx)
		}
	}
}

func (w *DBWriter) flushAllInternal(ctx context.Context) {
	w.mu.Lock()
	snapshot := make(map[string][]CredRotationEntry, len(w.pending))
	for k, v := range w.pending {
		snapshot[k] = v
	}
	w.pending = make(map[string][]CredRotationEntry)
	w.mu.Unlock()
	for sessionID, entries := range snapshot {
		tenantID := ""
		if len(entries) > 0 {
			tenantID = entries[0].Provider
		}
		w.flushSession(ctx, sessionID, tenantID, entries)
	}
}

func (w *DBWriter) flushSession(ctx context.Context, sessionID, tenantID string, entries []CredRotationEntry) {
	if w.db == nil || len(entries) == 0 {
		return
	}
	tx, err := w.db.Begin(ctx)
	if err != nil {
		slog.Warn("session_db_writer: begin failed", "session_id", sessionID, "error", err)
		return
	}
	defer tx.Rollback(ctx)

	var maxSeq int
	err = tx.QueryRow(ctx, `SELECT COALESCE(MAX(seq), 0) FROM session_credential_rotations WHERE session_id = $1`, sessionID).Scan(&maxSeq)
	if err != nil {
		slog.Warn("session_db_writer: query max seq failed", "session_id", sessionID, "error", err)
		return
	}

	for i, entry := range entries {
		seq := maxSeq + i + 1
		costUSD := float64(entry.CostUSDCents) / 10000.0
		endedAt := time.Time{}
		if entry.EndedAt != nil {
			endedAt = *entry.EndedAt
		}
		durationSec := 0
		if !endedAt.IsZero() {
			durationSec = int(endedAt.Sub(entry.StartedAt).Seconds())
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO session_credential_rotations (
				session_id, tenant_id, seq,
				credential_id, model, provider,
				started_at, ended_at, turns, duration_sec,
				prompt_tokens, completion_tokens, cost_usd,
				switch_reason, fp_slot_index
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
			sessionID, tenantID, seq,
			entry.CredentialID, entry.Model, entry.Provider,
			entry.StartedAt, endedAt, entry.Turns, durationSec,
			entry.PromptTokens, entry.CompletionTokens, costUSD,
			entry.SwitchReason, entry.FPSlotIndex,
		)
		if err != nil {
			slog.Warn("session_db_writer: insert failed", "session_id", sessionID, "error", err)
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		slog.Warn("session_db_writer: commit failed", "session_id", sessionID, "error", err)
		return
	}
	slog.Debug("session_db_writer: flushed", "session_id", sessionID, "count", len(entries))
}

// SnapshotArgs 快照参数
type SnapshotArgs struct {
	StoppedAt  time.Time
	StopReason string
}

// WriteSnapshot 写入会话状态快照
func (w *DBWriter) WriteSnapshot(ctx context.Context, sess *Session, stats *SessionStats, args SnapshotArgs, rawJSON string) error {
	if w == nil || w.db == nil {
		return nil
	}
	costUSD := float64(0)
	durationSec := 0
	firstReq := time.Time{}
	lastReq := time.Now()
	if stats != nil {
		costUSD = stats.TotalCostUSD
		firstReq = stats.FirstRequestAt
		lastReq = stats.LastRequestAt
	}
	if !firstReq.IsZero() {
		endTime := time.Now()
		if !args.StoppedAt.IsZero() {
			endTime = args.StoppedAt
		}
		durationSec = int(endTime.Sub(firstReq).Seconds())
	}
	var totalTurns int64
	if stats != nil {
		totalTurns = stats.TotalTurns
	}
	var promptTokens, completionTokens int64
	if stats != nil {
		promptTokens = stats.TotalPromptTokens
		completionTokens = stats.TotalCompletionTokens
	}

	_, err := w.db.Exec(ctx, `
		INSERT INTO session_state_snapshots (
			session_id, tenant_id, api_key_id, task_id,
			status, created_at, first_request_at, last_request_at,
			stopped_at, stop_reason,
			total_turns, total_duration_sec,
			total_prompt_tokens, total_completion_tokens, total_cost_usd,
			raw_snapshot
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16::jsonb)
		ON CONFLICT (session_id) DO UPDATE SET
			last_request_at = EXCLUDED.last_request_at,
			total_turns = EXCLUDED.total_turns,
			total_prompt_tokens = EXCLUDED.total_prompt_tokens,
			total_completion_tokens = EXCLUDED.total_completion_tokens,
			total_cost_usd = EXCLUDED.total_cost_usd,
			stopped_at = EXCLUDED.stopped_at,
			stop_reason = EXCLUDED.stop_reason,
			total_duration_sec = EXCLUDED.total_duration_sec,
			status = EXCLUDED.status,
			raw_snapshot = EXCLUDED.raw_snapshot
	`,
		sess.SessionID, sess.TenantID, sess.APIKeyID, sess.TaskID,
		StatusActive, sess.CreatedAt, firstReq, lastReq,
		args.StoppedAt, args.StopReason,
		totalTurns, durationSec,
		promptTokens, completionTokens, costUSD,
		rawJSON,
	)
	return err
}

// MarshalSnapshot 序列化
func MarshalSnapshot(sess *Session, stats *SessionStats) (string, error) {
	if sess == nil {
		return "", nil
	}
	b, err := json.Marshal(map[string]any{
		"session": sess,
		"stats":   stats,
	})
	if err != nil {
		return "", fmt.Errorf("marshal snapshot failed: %w", err)
	}
	return string(b), nil
}
