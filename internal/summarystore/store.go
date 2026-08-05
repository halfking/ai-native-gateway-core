// Package summarystore persists session-level LLM summaries to the
// session_summaries table. Both the v2 dispatch summarizer
// (domains/sessionsummary/summarizer.go) and the new on-request auto
// summarizer (admin/auto_summary_generator.go) write here so that the row
// shape stays consistent across all summary paths.
//
// 2026-08-06: extracted from domains/sessionsummary/summarizer.go so the
// admin package can write summaries without importing the v2 dispatch code
// (which would create a cycle admin → sessionsummary → admin).
package summarystore

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Summary is the persistent shape of a session-level LLM summary. The
// columns written match the session_summaries table DDL shipped in
// sql/init-complete-minimal.sql + the LLM-specific columns added by
// migration 358 (title, summary, key_topics, user_intent,
// last_summarized_at, summary_version).
type Summary struct {
	SessionKey     string    // gw_session_id (PK)
	TenantID       string    // tenant namespace
	Title          string    // short session title
	Summary        string    // 80-200 字 Chinese summary
	KeyTopics      []string  // 3-5 key points (15-40 字 each)
	UserIntent     string    // user's underlying goal
	LastSummarized time.Time // when this row was last generated (read by the rolling gate)
}

// Store persists Summary rows. Construct with NewStore; nil-safe methods
// are no-ops so unit tests can pass a zero-value Store.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore returns a Store backed by pool. A nil pool is tolerated so
// tests / disabled deployments can construct a Store and have Upsert become
// a logged no-op.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Upsert writes the summary row, creating it on first insert and bumping
// summary_version on subsequent updates. Mirrors the schema of the v2
// dispatch writer (domains/sessionsummary/summarizer.go::saveSummaryToDB)
// so dashboards see one shape regardless of which generator wrote the row.
//
// Idempotent: safe to call concurrently from many goroutines for the same
// session; PG's UPSERT handles the race. summary_version is monotonically
// incremented via session_summaries.summary_version + 1.
func (s *Store) Upsert(ctx context.Context, sum Summary) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("summarystore: pool not configured")
	}
	const query = `
		INSERT INTO session_summaries (
			session_key, tenant_id, title, summary, key_topics,
			user_intent, last_summarized_at, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, NOW(), NOW())
		ON CONFLICT (session_key) DO UPDATE SET
			title = EXCLUDED.title,
			summary = EXCLUDED.summary,
			key_topics = EXCLUDED.key_topics,
			user_intent = EXCLUDED.user_intent,
			last_summarized_at = EXCLUDED.last_summarized_at,
			summary_version = COALESCE(session_summaries.summary_version, 0) + 1,
			updated_at = NOW()
	`
	_, err := s.pool.Exec(ctx, query,
		sum.SessionKey,
		sum.TenantID,
		sum.Title,
		sum.Summary,
		sum.KeyTopics,
		sum.UserIntent,
		sum.LastSummarized,
	)
	return err
}

// LastSummarized returns the last_summarized_at timestamp for the session
// (used by the rolling-gate trigger). Returns the zero time and nil error
// when no row exists yet — that signals "never summarized".
func (s *Store) LastSummarized(ctx context.Context, sessionKey string) (time.Time, error) {
	if s == nil || s.pool == nil {
		return time.Time{}, fmt.Errorf("summarystore: pool not configured")
	}
	var ts *time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT last_summarized_at FROM session_summaries WHERE session_key = $1`,
		sessionKey).Scan(&ts)
	if err != nil {
		// pgx returns ErrNoRows for missing rows; the caller treats that as
		// "never summarized" by checking the returned time.
		return time.Time{}, err
	}
	if ts == nil {
		return time.Time{}, nil
	}
	return *ts, nil
}

// CountNewTurns returns the number of successful request_logs rows for the
// session whose ts > since. Used by the rolling-gate trigger to decide
// whether enough new turns have accumulated since the last summary.
func (s *Store) CountNewTurns(ctx context.Context, sessionKey string, since time.Time) (int, error) {
	if s == nil || s.pool == nil {
		return 0, fmt.Errorf("summarystore: pool not configured")
	}
	var n int
	err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*)::int FROM request_logs_hot
		WHERE gw_session_id = $1 AND success = TRUE AND ts > $2
	`, sessionKey, since).Scan(&n)
	return n, err
}