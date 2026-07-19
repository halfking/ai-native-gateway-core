package v2

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// SessionAggregator updates session snapshots in gateway.sessions
//
// It maintains session-level aggregated data like total_turns, total_tokens,
// total_cost, and last turn summaries. Updates can be incremental (add to
// existing) or full replace.
type SessionAggregator struct {
	db *pgxpool.Pool
}

// NewSessionAggregator creates a new SessionAggregator instance
func NewSessionAggregator(db *pgxpool.Pool) *SessionAggregator {
	return &SessionAggregator{db: db}
}

// SessionUpdate represents an incremental update to a session snapshot
type SessionUpdate struct {
	SessionID string
	TenantID  string

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

// UpdateSession updates the session snapshot with incremental data
//
// This uses INSERT ... ON CONFLICT DO UPDATE to handle both creation
// and updates atomically. Counters are incremented, summaries are replaced.
func (a *SessionAggregator) UpdateSession(ctx context.Context, update SessionUpdate) error {
	partitionDate := update.UpdatedAt.Truncate(24 * time.Hour)

	_, err := a.db.Exec(ctx, `
		INSERT INTO gateway.sessions (
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
			total_turns = gateway.sessions.total_turns + EXCLUDED.total_turns,
			total_tokens = gateway.sessions.total_tokens + EXCLUDED.total_tokens,
			total_cost_usd = gateway.sessions.total_cost_usd + EXCLUDED.total_cost_usd,
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
		FROM gateway.sessions
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
		UPDATE gateway.sessions
		SET status = 'closed', closed_at = NOW()
		WHERE tenant_id = $1 AND session_id = $2
	`, tenantID, sessionID)

	return err
}

// SetSessionMetadata sets session-level metadata (task_type, topic, intent, etc.)
//
// This is typically called by async analysis workers after session closes.
func (a *SessionAggregator) SetSessionMetadata(ctx context.Context, tenantID, sessionID string, meta SessionMetadata) error {
	_, err := a.db.Exec(ctx, `
		UPDATE gateway.sessions
		SET 
			task_type = COALESCE(NULLIF($3, ''), task_type),
			client_type = COALESCE(NULLIF($4, ''), client_type),
			topic = COALESCE(NULLIF($5, ''), topic),
			intent = COALESCE(NULLIF($6, ''), intent)
		WHERE tenant_id = $1 AND session_id = $2
	`, tenantID, sessionID, meta.TaskType, meta.ClientType, meta.Topic, meta.Intent)

	return err
}

// SessionMetadata represents session-level metadata
type SessionMetadata struct {
	TaskType   string
	ClientType string
	Topic      string
	Intent     string
}
