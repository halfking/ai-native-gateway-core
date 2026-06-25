// Package armor — logger.go
//
// Logger writes armor judgment records to armor_judgments table (B1-4).
// Every Judge.Score or pattern match generates one row for:
//   - v1 observe-only audit (no blocking in production)
//   - recall/precision stats (B1-5 test set eval)
//   - false-positive analysis (ops tuning)
//
// Design contract:
//   - Logger does NOT own policy loading (armor/policy.go)
//   - Logger does NOT own judge scoring (armor/judge.go)
//   - Logger ONLY writes audit records after decisions are made
//   - RLS: every insert sets app.current_tenant GUC (same as apihub.PGStore)
//
// Usage (in relay handler):
//   logger := armor.NewLogger(pool)
//   resp, err := judge.Score(ctx, req)
//   logger.Log(ctx, armor.Judgment{...})  // async, never blocks relay

package armor

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Judgment is the data written to armor_judgments table.
type Judgment struct {
	RequestID   string    // from X-Request-Id or generated
	TenantID    string    // from auth context
	CheckType   string    // "prompt_inject", "pii", "hallucination"
	Decision    Decision  // safe / warn / block
	Score       float64   // [0,1] from judge
	Threshold   float64   // policy threshold at judgment time
	JudgeModel  string    // which model scored (e.g. "gpt-4o-mini")
	LatencyMS   int       // judge latency
	PatternHit  *string   // if pattern matched, which one
	ErrorKind   *string   // if judge failed, error category
	CreatedAt   time.Time // when judgment was made
}

// Logger writes armor judgments to PG. Safe for concurrent use.
type Logger struct {
	pool *pgxpool.Pool
}

// NewLogger returns a Logger backed by pgxpool. If pool is nil, Log becomes no-op.
func NewLogger(pool *pgxpool.Pool) *Logger {
	return &Logger{pool: pool}
}

const insertJudgmentSQL = `
INSERT INTO armor_judgments (
	request_id, tenant_id, check_type, decision, score, threshold,
	judge_model, latency_ms, pattern_hit, error_kind, created_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
`

// Log writes a Judgment to armor_judgments. Never returns error; failures are
// logged at WARN level. This keeps armor audit from blocking the relay hot path.
//
// RLS: sets app.current_tenant = j.TenantID before insert so tenant_isolation
// policy passes. If tenant is empty, logs a warning and skips insert.
func (l *Logger) Log(ctx context.Context, j Judgment) {
	if l.pool == nil {
		return // no-op if no pool
	}
	if j.TenantID == "" {
		slog.Warn("armor logger: skipping judgment with empty tenant_id", "request_id", j.RequestID)
		return
	}

	// Use a separate context with 3s timeout so slow inserts don't block relay.
	insertCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// RLS: set tenant GUC in a transaction (same pattern as apihub.PGStore).
	err := l.withTenantTx(insertCtx, j.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(insertCtx, insertJudgmentSQL,
			j.RequestID,
			j.TenantID,
			j.CheckType,
			j.Decision.String(), // Decision marshals to "safe"/"warn"/"block"
			j.Score,
			j.Threshold,
			j.JudgeModel,
			j.LatencyMS,
			j.PatternHit,
			j.ErrorKind,
			j.CreatedAt,
		)
		return err
	})

	if err != nil {
		slog.Warn("armor logger: insert failed", "error", err, "request_id", j.RequestID)
	}
}

// withTenantTx runs fn inside a pgx transaction with app.current_tenant set.
// Copied from apihub.PGStore pattern (B1-1).
func (l *Logger) withTenantTx(ctx context.Context, tenantID string, fn func(pgx.Tx) error) error {
	tx, err := l.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("armor logger: begin tx: %w", err)
	}
	defer tx.Rollback(ctx) // safe if already committed

	// Set RLS GUC
	_, err = tx.Exec(ctx, "SELECT set_config('app.current_tenant', $1, true)", tenantID)
	if err != nil {
		return fmt.Errorf("armor logger: set tenant GUC: %w", err)
	}

	if err := fn(tx); err != nil {
		return err
	}

	return tx.Commit(ctx)
}
