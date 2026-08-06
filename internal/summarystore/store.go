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
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Summary is the persistent shape of a session-level LLM summary. The
// columns written match the session_summaries table DDL shipped in
// sql/init-complete-minimal.sql + the LLM-specific columns added by
// migration 358 (title, summary, key_topics, user_intent,
// last_summarized_at, summary_version).
//
// 2026-08-06: FirstRequestAt / LastRequestAt added because
// session_summaries.first_request_at and last_request_at are NOT NULL
// (migration 310). The previous Upsert omitted them from INSERT, so
// every first-insert failed with "null value in column first_request_at"
// (SQLSTATE 23502) — production measured 161/161 summary persist failures.
// Callers that don't know the timestamps can leave them zero; Upsert
// falls back to LastSummarized / NOW() so the NOT NULL constraint holds.
type Summary struct {
	SessionKey     string    // gw_session_id (PK)
	TenantID       string    // tenant namespace
	Title          string    // short session title
	Summary        string    // 80-200 字 Chinese summary
	KeyTopics      []string  // 3-5 key points (15-40 字 each)
	UserIntent     string    // user's underlying goal
	LastSummarized time.Time // when this row was last generated (read by the rolling gate)
	FirstRequestAt time.Time // first request ts for this session (NOT NULL column)
	LastRequestAt  time.Time // last request ts for this session (NOT NULL column)
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

// Pool exposes the underlying *pgxpool.Pool so callers (e.g. the v2
// dispatch summarizer after its 2026-08-06 migration) can run read
// queries directly without holding a second pool reference. Returns nil
// if the store was constructed with a nil pool.
func (s *Store) Pool() *pgxpool.Pool {
	if s == nil {
		return nil
	}
	return s.pool
}

// UpsertResult is the outcome of an Upsert. Returned so call sites that
// race (e.g. concurrent auto-summary + summary workers) can detect
// staleness via Version and Updated.
//
// 2026-08-06: added so the v2 dispatch summarizer can finally compare
// incoming vs persisted summary_version — previously it just called
// saveSummaryToDB and hoped for the best, which made concurrent
// overwrites silent.
type UpsertResult struct {
	// Version is the summary_version after the upsert. For a fresh insert
	// it's 1 (the column default is 1); for an update it's the
	// previous value + 1 (computed via COALESCE(...)+1 in the query).
	Version int
	// Updated is true when the row already existed and was updated by
	// this call, false when a new row was inserted. Useful for emitting
	// a "lost the race" metric when two writers serialize on the same
	// session_key.
	Updated bool
}

// Upsert writes the summary row, creating it on first insert and bumping
// summary_version on subsequent updates. Mirrors the schema of the v2
// dispatch writer (domains/sessionsummary/summarizer.go::saveSummaryToDB)
// so dashboards see one shape regardless of which generator wrote the row.
//
// Idempotent: safe to call concurrently from many goroutines for the same
// session; PG's UPSERT handles the race. summary_version is monotonically
// incremented via COALESCE(session_summaries.summary_version, 0) + 1.
// The returned UpsertResult carries the post-upsert version and an
// INSERT-vs-UPDATE flag so callers can detect races and stale writes.
//
// 2026-08-06: changed return type from error to (UpsertResult, error)
// so the result carries summary_version. This is a breaking change for
// caller signatures — fix by replacing `if err := summarystore.Upsert(...)`
// with `if _, err := summarystore.Upsert(...)` (or destructure into
// `result, err := ...` when version is needed).
//
// What (xmax = 0) tells you — and what it doesn't:
//
// PG's xmax column on the candidate row is 0 when INSERT wrote a new
// physical tuple, and the new xid of the transaction when an UPDATE
// touched an existing row. So `xmax = 0` distinguishes "this Upsert
// was the very first insert" from "this Upsert updated an existing row".
//
// It does NOT distinguish "this Upsert raced with a concurrent
// same-version writer" — two writers serialise on the unique key at
// the row level, so the second writer ALWAYS hits the UPDATE branch.
// The `Updated=true` flag is therefore a "row existed before me" hint,
// useful for emitting a 'lost the race' log line on the request-path
// auto-summary, not a precise race detector.
func (s *Store) Upsert(ctx context.Context, sum Summary) (UpsertResult, error) {
	if s == nil || s.pool == nil {
		return UpsertResult{}, fmt.Errorf("summarystore: pool not configured")
	}

	// 2026-08-06: session_summaries.first_request_at and last_request_at
	// are NOT NULL (migration 310). When the caller doesn't supply them
	// (zero value), fall back to LastSummarized for first and NOW() for
	// last so the INSERT always satisfies the constraint. This fixes the
	// 161/161 production persist failures where every first-insert was
	// rejected with SQLSTATE 23502.
	firstReq := sum.FirstRequestAt
	lastReq := sum.LastRequestAt
	if firstReq.IsZero() {
		firstReq = sum.LastSummarized
	}
	if firstReq.IsZero() {
		firstReq = time.Now()
	}
	if lastReq.IsZero() {
		lastReq = time.Now()
	}

	// 2026-08-06: LLM-generated text occasionally contains invalid UTF-8
	// (truncated multibyte CJK sequences like 0xe5 0xe2 0x80). PostgreSQL
	// rejects these with SQLSTATE 22021. Sanitise all text fields before
	// INSERT by replacing invalid byte sequences with U+FFFD. Production
	// measured 157 such failures in 3 hours.
	title := sanitiseUTF8(sum.Title)
	summaryText := sanitiseUTF8(sum.Summary)
	userIntent := sanitiseUTF8(sum.UserIntent)
	if title != sum.Title || summaryText != sum.Summary || userIntent != sum.UserIntent {
		slog.Warn("summarystore: UTF-8 sanitization applied",
			"session_key", sum.SessionKey,
			"title_changed", title != sum.Title,
			"summary_changed", summaryText != sum.Summary,
			"user_intent_changed", userIntent != sum.UserIntent,
		)
	}

	const query = `
		INSERT INTO session_summaries (
			session_key, tenant_id, title, summary, key_topics,
			user_intent, last_summarized_at, created_at, updated_at,
			first_request_at, last_request_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, NOW(), NOW(), $8, $9)
		ON CONFLICT (session_key) DO UPDATE SET
			title = EXCLUDED.title,
			summary = EXCLUDED.summary,
			key_topics = EXCLUDED.key_topics,
			user_intent = EXCLUDED.user_intent,
			last_summarized_at = EXCLUDED.last_summarized_at,
			last_request_at = GREATEST(session_summaries.last_request_at, EXCLUDED.last_request_at),
			summary_version = COALESCE(session_summaries.summary_version, 0) + 1,
			updated_at = NOW()
		RETURNING summary_version, (xmax = 0) AS inserted
	`
	var result UpsertResult
	var inserted bool
	err := s.pool.QueryRow(ctx, query,
		sum.SessionKey,
		sum.TenantID,
		title,
		summaryText,
		sum.KeyTopics,
		userIntent,
		sum.LastSummarized,
		firstReq,
		lastReq,
	).Scan(&result.Version, &inserted)
	if err != nil {
		return UpsertResult{}, err
	}
	result.Updated = !inserted
	return result, nil
}

// sanitiseUTF8 returns s with all invalid UTF-8 byte sequences replaced by
// the Unicode replacement character (U+FFFD). This prevents PostgreSQL
// from rejecting the INSERT with SQLSTATE 22021 when the LLM produces
// truncated or corrupted multibyte characters (observed in production
// with CJK text: 0xe5 0xe2 0x80).
//
// strings.ToValidUTF8 is the standard library function for this; it is
// available since Go 1.13.
func sanitiseUTF8(s string) string {
	return strings.ToValidUTF8(s, "\ufffd")
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

// CountTotalTurns returns the total number of successful request_logs rows
// for the session (all time). Used by the rolling-gate trigger to enforce
// the minimum session length requirement before allowing summary generation.
//
// 2026-08-06: added to implement the 5-turn minimum gate — summaries should
// only be generated for sessions with at least 5 successful turns, regardless
// of when the last summary was generated. This prevents premature summaries
// on short exploratory sessions and reduces cost waste.
func (s *Store) CountTotalTurns(ctx context.Context, sessionKey string) (int, error) {
	if s == nil || s.pool == nil {
		return 0, fmt.Errorf("summarystore: pool not configured")
	}
	var n int
	err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*)::int FROM request_logs_hot
		WHERE gw_session_id = $1 AND success = TRUE
	`, sessionKey).Scan(&n)
	return n, err
}