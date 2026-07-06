package session

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

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
	if w == nil {
		return
	}
	go w.runFlushLoop(ctx)
}

func (w *DBWriter) Stop() {
	if w == nil {
		return
	}
	close(w.stopCh)
	<-w.doneCh
}

func (w *DBWriter) Enqueue(sessionID string, entry CredRotationEntry) {
	if w == nil || w.db == nil || sessionID == "" {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pending[sessionID] = append(w.pending[sessionID], entry)
	if len(w.pending[sessionID]) >= w.batchSize {
		entries := append([]CredRotationEntry(nil), w.pending[sessionID]...)
		delete(w.pending, sessionID)
		go w.flushSession(context.Background(), sessionID, entries)
	}
}

func (w *DBWriter) FlushSession(ctx context.Context, sessionID string) {
	if w == nil {
		return
	}
	w.mu.Lock()
	entries := append([]CredRotationEntry(nil), w.pending[sessionID]...)
	delete(w.pending, sessionID)
	w.mu.Unlock()
	if len(entries) > 0 {
		w.flushSession(ctx, sessionID, entries)
	}
}

func (w *DBWriter) FlushAll(ctx context.Context) {
	if w == nil {
		return
	}
	w.mu.Lock()
	snapshot := make(map[string][]CredRotationEntry, len(w.pending))
	for k, v := range w.pending {
		snapshot[k] = append([]CredRotationEntry(nil), v...)
	}
	w.pending = make(map[string][]CredRotationEntry)
	w.mu.Unlock()
	for sessionID, entries := range snapshot {
		w.flushSession(ctx, sessionID, entries)
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
		snapshot[k] = append([]CredRotationEntry(nil), v...)
	}
	w.pending = make(map[string][]CredRotationEntry)
	w.mu.Unlock()
	for sessionID, entries := range snapshot {
		w.flushSession(ctx, sessionID, entries)
	}
}

func (w *DBWriter) flushSession(ctx context.Context, sessionID string, entries []CredRotationEntry) {
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
	if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(seq), 0) FROM session_credential_rotations WHERE session_id = $1`, sessionID).Scan(&maxSeq); err != nil {
		slog.Warn("session_db_writer: query max seq failed", "session_id", sessionID, "error", err)
		return
	}

	for i, entry := range entries {
		seq := maxSeq + i + 1
		costUSD := float64(entry.CostUSDCents) / 10000.0
		var endedAt any
		if entry.EndedAt != nil {
			endedAt = *entry.EndedAt
		}
		durationSec := 0
		if entry.EndedAt != nil {
			durationSec = int(entry.EndedAt.Sub(entry.StartedAt).Seconds())
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO session_credential_rotations (
				session_id, tenant_id, seq,
				credential_id, model, provider,
				started_at, ended_at, turns, duration_sec,
				prompt_tokens, completion_tokens, cost_usd,
				switch_reason, fp_slot_index
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
			sessionID, "default", seq,
			entry.CredentialID, entry.Model, entry.Provider,
			entry.StartedAt, endedAt, entry.Turns, durationSec,
			entry.PromptTokens, entry.CompletionTokens, costUSD,
			entry.SwitchReason, entry.FPSlotIndex,
		); err != nil {
			slog.Warn("session_db_writer: insert failed", "session_id", sessionID, "error", err)
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		slog.Warn("session_db_writer: commit failed", "session_id", sessionID, "error", err)
		return
	}
}

type SnapshotArgs struct {
	StoppedAt  time.Time
	StopReason string
}

func (w *DBWriter) WriteSnapshot(ctx context.Context, sess *Session, stats *SessionStats, args SnapshotArgs) error {
	if w == nil || w.db == nil || sess == nil {
		return nil
	}
	raw, err := MarshalSnapshot(sess, stats)
	if err != nil {
		return err
	}
	costUSD := 0.0
	var firstReq, lastReq any
	var totalTurns, promptTokens, completionTokens int64
	if stats != nil {
		costUSD = stats.TotalCostUSD
		totalTurns = stats.TotalTurns
		promptTokens = stats.TotalPromptTokens
		completionTokens = stats.TotalCompletionTokens
		if !stats.FirstRequestAt.IsZero() {
			firstReq = stats.FirstRequestAt
		}
		if !stats.LastRequestAt.IsZero() {
			lastReq = stats.LastRequestAt
		}
	}
	durationSec := 0
	if stats != nil && !stats.FirstRequestAt.IsZero() {
		endTime := time.Now()
		if !args.StoppedAt.IsZero() {
			endTime = args.StoppedAt
		}
		durationSec = int(endTime.Sub(stats.FirstRequestAt).Seconds())
	}
	_, err = w.db.Exec(ctx, `
		INSERT INTO session_state_snapshots (
			session_id, tenant_id, api_key_id, task_id,
			status, created_at, first_request_at, last_request_at,
			stopped_at, stop_reason,
			total_turns, total_duration_sec,
			total_prompt_tokens, total_completion_tokens, total_cost_usd,
			title, annotation, raw_snapshot
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18::jsonb)
		ON CONFLICT (session_id) DO UPDATE SET
			status = EXCLUDED.status,
			last_request_at = EXCLUDED.last_request_at,
			stopped_at = EXCLUDED.stopped_at,
			stop_reason = EXCLUDED.stop_reason,
			total_turns = EXCLUDED.total_turns,
			total_duration_sec = EXCLUDED.total_duration_sec,
			total_prompt_tokens = EXCLUDED.total_prompt_tokens,
			total_completion_tokens = EXCLUDED.total_completion_tokens,
			total_cost_usd = EXCLUDED.total_cost_usd,
			title = EXCLUDED.title,
			annotation = EXCLUDED.annotation,
			raw_snapshot = EXCLUDED.raw_snapshot`,
		sess.SessionID, sess.TenantID, sess.APIKeyID, sess.TaskID,
		defaultStringLocal(sess.Status, StatusActive), sess.CreatedAt, firstReq, lastReq,
		args.StoppedAt, args.StopReason,
		totalTurns, durationSec,
		promptTokens, completionTokens, costUSD,
		sess.Title, sess.Annotation, raw,
	)
	return err
}

func MarshalSnapshot(sess *Session, stats *SessionStats) (string, error) {
	b, err := json.Marshal(map[string]any{"session": sess, "stats": stats})
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func defaultStringLocal(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
