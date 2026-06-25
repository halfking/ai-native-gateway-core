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
	RequestID    string    // from X-Request-Id or generated
	TenantID     string    // from auth context
	CheckType    string    // "prompt_inject", "pii", "hallucination"
	Decision     Decision  // safe / warn / block
	Source       string    // "pattern" | "judge" — defaults to "judge"
	PatternIDs   []string  // source=pattern 时命中的 Pattern.ID 列表
	JudgeModel   string    // which model scored (e.g. "gpt-4o-mini")
	Score        float64   // [0,1] from judge
	Threshold    float64   // policy threshold at judgment time
	Mode         Mode      // observe | enforce — defaults to ModeObserve
	LatencyMS    int       // judge latency
	PromptSHA256 string    // prompt 的 SHA256 (隐私: 不存原文)
	Snippet      string    // 命中片段 (≤80 字符)
	Reason       string    // judge 给的可读解释
	CreatedAt    time.Time // when judgment was made
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
	request_id, tenant_id, check_type, decision, source, pattern_ids,
	judge_model, score, threshold, mode, latency_ms,
	prompt_sha256, snippet, reason, created_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
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

	// Defaults for required NOT NULL columns
	source := j.Source
	if source == "" {
		source = "judge"
	}
	mode := string(j.Mode)
	if mode == "" {
		mode = string(ModeObserve)
	}
	patternIDs := j.PatternIDs
	if patternIDs == nil {
		patternIDs = []string{}
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
			source,
			patternIDs,
			j.JudgeModel,
			j.Score,
			j.Threshold,
			mode,
			j.LatencyMS,
			nullIfEmpty(j.PromptSHA256),
			nullIfEmpty(j.Snippet),
			nullIfEmpty(j.Reason),
			j.CreatedAt,
		)
		return err
	})

	if err != nil {
		slog.Warn("armor logger: insert failed", "error", err, "request_id", j.RequestID)
	}
}

// nullIfEmpty returns nil for empty strings so the DB column receives NULL
// instead of an empty string. Useful for nullable TEXT columns.
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
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
