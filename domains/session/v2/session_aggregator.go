package v2

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// aggregatorDB is the minimal pool surface that SessionAggregator needs.
//
// Defined as an interface so unit tests can wire pgxmock without spinning up
// a live PostgreSQL instance (mirrors the turnDB pattern in turn_writer.go).
type aggregateExecutor interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

type aggregatorDB interface {
	aggregateExecutor
	Begin(ctx context.Context) (pgx.Tx, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// SessionAggregator updates session snapshots in public.sessions
//
// It maintains session-level aggregated data like total_turns, total_tokens,
// total_cost, and last turn summaries. Updates can be incremental (add to
// existing) or full replace.
//
// Idempotency (request-flow Step 3 / spec §6.2):
//
//	When SessionUpdate.RequestID is non-empty, UpdateSession atomically claims
//	the corresponding session_turns row by setting aggregate_applied_at inside
//	the SAME transaction as the sessions upsert. Only a row whose marker is
//	NULL can be claimed. Concurrent/replayed updates therefore do not double-
//	accumulate counters, while a failed upsert rolls the claim back and remains
//	retryable.
//
//	An empty RequestID bypasses the claim and falls through to the plain
//	aggregate INSERT, preserving legacy backfill / fan-in paths that
//	intentionally aggregate without a single request_id.
type SessionAggregator struct {
	db aggregatorDB
}

// NewSessionAggregator creates a new SessionAggregator instance
func NewSessionAggregator(db *pgxpool.Pool) *SessionAggregator {
	return newSessionAggregator(db)
}

// newSessionAggregator is the seam used by unit tests with pgxmock.
func newSessionAggregator(db aggregatorDB) *SessionAggregator {
	return &SessionAggregator{db: db}
}

// SessionUpdate represents an incremental update to a session snapshot
type SessionUpdate struct {
	SessionID string
	TenantID  string

	// RequestID is the dedup key (see SessionAggregator doc). Empty
	// disables idempotency and falls through to the aggregate INSERT.
	RequestID string

	// Last turn info (replace)
	LastTurnNo          int
	LastRequestSummary  string
	LastResponseSummary string
	LastModel           string
	LastProvider        string

	// Incremental counters (add to existing)
	TurnIncrement   int
	TokensIncrement int
	CostIncrement   float64

	// Timestamp
	UpdatedAt time.Time
}

// UpdateSession updates the session snapshot with incremental data.
//
// This uses INSERT ... ON CONFLICT DO UPDATE to handle both creation and
// updates atomically. Counters are incremented, summaries are replaced.
//
// When update.RequestID is non-empty, the aggregate claim and snapshot upsert
// share one transaction. A replay whose turn is already marked is a no-op.
func (a *SessionAggregator) UpdateSession(ctx context.Context, update SessionUpdate) error {
	partitionDate := calendarDate(update.UpdatedAt)

	if update.RequestID == "" {
		return upsertSessionSnapshot(ctx, a.db, update, partitionDate)
	}

	tx, err := a.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin aggregate transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	claimed, err := claimAggregateTurn(ctx, tx, update, partitionDate)
	if err != nil {
		return fmt.Errorf("claim aggregate turn: %w", err)
	}
	if !claimed {
		// The row was already claimed by a successful/concurrent aggregate.
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit duplicate aggregate transaction: %w", err)
		}
		committed = true
		return nil
	}

	if err := upsertSessionSnapshot(ctx, tx, update, partitionDate); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit aggregate transaction: %w", err)
	}
	committed = true
	return nil
}

// claimAggregateTurn marks one persisted turn as consumed by the session
// snapshot. UPDATE ... WHERE aggregate_applied_at IS NULL is the durable,
// concurrent-safe claim; pgx.ErrNoRows means another caller already won.
func claimAggregateTurn(ctx context.Context, tx pgx.Tx, update SessionUpdate, partitionDate time.Time) (bool, error) {
	var claimed int
	err := tx.QueryRow(ctx, `
		UPDATE public.session_turns
		SET aggregate_applied_at = NOW()
		WHERE session_id = $1
		  AND tenant_id = $2
		  AND request_id = $3
		  AND partition_date = $4
		  AND aggregate_applied_at IS NULL
		RETURNING 1
	`, update.SessionID, update.TenantID, update.RequestID, partitionDate).Scan(&claimed)
	if err == pgx.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return claimed == 1, nil
}

func upsertSessionSnapshot(ctx context.Context, db aggregateExecutor, update SessionUpdate, partitionDate time.Time) error {
	_, err := db.Exec(ctx, `
		INSERT INTO public.sessions (
			session_id, tenant_id,
			created_at, updated_at, status,
			total_turns, total_tokens, total_cost_usd,
			last_turn_no, last_request_summary, last_response_summary,
			last_model, last_provider,
			partition_date
		) VALUES (
			$1, $2,
			$3, $3, 'active',
			$4, $5, $6,
			$7, $8, $9,
			$10, $11,
			$12
		)
		ON CONFLICT (session_id, partition_date)
		DO UPDATE SET
			updated_at = EXCLUDED.updated_at,
			total_turns = public.sessions.total_turns + EXCLUDED.total_turns,
			total_tokens = public.sessions.total_tokens + EXCLUDED.total_tokens,
			total_cost_usd = public.sessions.total_cost_usd + EXCLUDED.total_cost_usd,
			last_turn_no = EXCLUDED.last_turn_no,
			last_request_summary = EXCLUDED.last_request_summary,
			last_response_summary = EXCLUDED.last_response_summary,
			last_model = EXCLUDED.last_model,
			last_provider = EXCLUDED.last_provider
	`,
		update.SessionID, update.TenantID,
		update.UpdatedAt,
		update.TurnIncrement, update.TokensIncrement, update.CostIncrement,
		update.LastTurnNo, update.LastRequestSummary, update.LastResponseSummary,
		update.LastModel, update.LastProvider,
		partitionDate,
	)
	if err != nil {
		return fmt.Errorf("update session: %w", err)
	}
	return nil
}

// GetSession retrieves a session snapshot
func (a *SessionAggregator) GetSession(ctx context.Context, tenantID, sessionID string) (*SessionSnapshot, error) {
	var snap SessionSnapshot
	var turnLogsSummaryJSON []byte

	query := `
		SELECT
			session_id, tenant_id,
			created_at, updated_at, closed_at, status,
			total_turns, total_tokens, total_cost_usd,
			last_turn_no,
			COALESCE(last_request_summary, ''),
			COALESCE(last_response_summary, ''),
			COALESCE(last_model, ''),
			COALESCE(last_provider, ''),
			COALESCE(task_type, ''),
			COALESCE(client_type, ''),
			COALESCE(topic, ''),
			COALESCE(intent, ''),
			COALESCE(primary_request_id, ''),
			turn_logs_summary
		FROM public.sessions
		WHERE tenant_id = $1 AND session_id = $2
		LIMIT 1
	`

	err := a.db.QueryRow(ctx, query, tenantID, sessionID).Scan(
		&snap.SessionID, &snap.TenantID,
		&snap.CreatedAt, &snap.UpdatedAt, &snap.ClosedAt, &snap.Status,
		&snap.TotalTurns, &snap.TotalTokens, &snap.TotalCostUSD,
		&snap.LastTurnNo,
		&snap.LastRequestSummary,
		&snap.LastResponseSummary,
		&snap.LastModel,
		&snap.LastProvider,
		&snap.TaskType,
		&snap.ClientType,
		&snap.Topic,
		&snap.Intent,
		&snap.PrimaryRequestID,
		&turnLogsSummaryJSON,
	)

	if err != nil {
		return nil, fmt.Errorf("query session: %w", err)
	}

	// Parse turn logs summary
	if len(turnLogsSummaryJSON) > 0 {
		json.Unmarshal(turnLogsSummaryJSON, &snap.TurnLogsSummary)
	}

	return &snap, nil
}

// SessionSnapshot represents a session's aggregated state
type SessionSnapshot struct {
	SessionID string
	TenantID  string

	CreatedAt time.Time
	UpdatedAt time.Time
	ClosedAt  *time.Time
	Status    string

	TotalTurns   int
	TotalTokens  int
	TotalCostUSD float64

	LastTurnNo          int
	LastRequestSummary  string
	LastResponseSummary string
	LastModel           string
	LastProvider        string

	TaskType   string
	ClientType string
	Topic      string
	Intent     string

	PrimaryRequestID string
	TurnLogsSummary  map[string]interface{}
}

// CloseSession marks a session as closed
func (a *SessionAggregator) CloseSession(ctx context.Context, tenantID, sessionID string) error {
	_, err := a.db.Exec(ctx, `
		UPDATE public.sessions
		SET status = 'closed', closed_at = NOW()
		WHERE tenant_id = $1 AND session_id = $2
	`, tenantID, sessionID)

	return err
}

// SetSessionMetadata sets session-level metadata (task_type, topic, intent, title, user_tags, etc.)
//
// This is typically called by async analysis workers after session closes, or by
// the auto-title generator / user tag updates (M2/M3).
func (a *SessionAggregator) SetSessionMetadata(ctx context.Context, tenantID, sessionID string, meta SessionMetadata) error {
	_, err := a.db.Exec(ctx, `
		UPDATE public.sessions
		SET 
			task_type = COALESCE(NULLIF($3, ''), task_type),
			client_type = COALESCE(NULLIF($4, ''), client_type),
			topic = COALESCE(NULLIF($5, ''), topic),
			intent = COALESCE(NULLIF($6, ''), intent),
			title = COALESCE(NULLIF($7, ''), title),
			user_tags = CASE WHEN $8::text[] IS NOT NULL THEN $8 ELSE user_tags END
		WHERE tenant_id = $1 AND session_id = $2
	`, tenantID, sessionID, meta.TaskType, meta.ClientType, meta.Topic, meta.Intent, meta.Title, meta.UserTags)

	return err
}

// SessionMetadata represents session-level metadata
type SessionMetadata struct {
	TaskType   string
	ClientType string
	Topic      string
	Intent     string
	Title      string   // docs/omni-ref3 M2: unified fact source (from Redis/session_titles)
	UserTags   []string // docs/omni-ref3 M3: user-supplied tags (X-Gw-Tags), distinct from session_tags (auto)
}

// GetSessionMetadata retrieves session metadata from public.sessions, optionally
// merging auto-generated tags from session_tags (M3).
//
// Returns nil if the session does not exist. Auto tags (tag_source='auto') are
// merged with user_tags if mergeAutoTags is true.
func (a *SessionAggregator) GetSessionMetadata(ctx context.Context, tenantID, sessionID string, mergeAutoTags bool) (*SessionMetadata, error) {
	var meta SessionMetadata
	var userTags []string

	err := a.db.QueryRow(ctx, `
		SELECT 
			COALESCE(task_type, ''),
			COALESCE(client_type, ''),
			COALESCE(topic, ''),
			COALESCE(intent, ''),
			COALESCE(title, ''),
			COALESCE(user_tags, ARRAY[]::text[])
		FROM public.sessions
		WHERE tenant_id = $1 AND session_id = $2
	`, tenantID, sessionID).Scan(
		&meta.TaskType, &meta.ClientType, &meta.Topic,
		&meta.Intent, &meta.Title, &userTags,
	)

	if err != nil {
		if err.Error() == "no rows in result set" {
			return nil, nil // session does not exist
		}
		return nil, err
	}

	meta.UserTags = userTags

	// M3: merge auto tags from session_tags if requested
	if mergeAutoTags {
		var autoTags []string
		rows, err := a.db.Query(ctx, `
			SELECT DISTINCT tag_value
			FROM gateway.session_tags
			WHERE tenant_id = $1 AND session_id = $2 AND tag_source = 'auto'
			ORDER BY tag_value
		`, tenantID, sessionID)
		if err != nil {
			return nil, err
		}
		defer rows.Close()

		for rows.Next() {
			var tag string
			if err := rows.Scan(&tag); err != nil {
				return nil, err
			}
			autoTags = append(autoTags, tag)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}

		// Merge and deduplicate: user tags + auto tags
		seen := make(map[string]bool)
		for _, tag := range meta.UserTags {
			seen[tag] = true
		}
		for _, tag := range autoTags {
			if !seen[tag] {
				meta.UserTags = append(meta.UserTags, tag)
				seen[tag] = true
			}
		}
	}

	return &meta, nil
}
