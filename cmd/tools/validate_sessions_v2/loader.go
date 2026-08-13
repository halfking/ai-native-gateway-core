package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// V1Turn represents a turn from request_logs (V1 schema)
type V1Turn struct {
	RequestID    string
	Ts           time.Time
	SessionID    string
	TenantID     string
	ClientModel  string
	ProviderID   string
	CredentialID string

	// Usage is stored as JSONB in request_logs
	Usage   json.RawMessage
	CostUSD float64

	// Compression metadata
	CompressionMeta json.RawMessage

	// Request body (for bodies validation)
	RequestBody  json.RawMessage
	ResponseBody json.RawMessage

	Success bool
}

// V2Turn represents a turn from session_turns (V2 schema)
type V2Turn struct {
	RequestID string
	TurnNo    int
	Ts        time.Time
	SessionID string
	TenantID  string

	SubmitMode string

	Model        string
	Provider     string
	CredentialID string

	PromptTokens     int
	CompletionTokens int
	CacheReadTokens  int
	CacheWriteTokens int
	CostUSD          float64

	InjectionVerdict string
	OutputVerdict    string

	LatencyMs  int
	StatusCode int
	Success    bool
	ErrorKind  string

	SourceKind string
	Quality    string
}

// V2Body represents a turn's bodies from session_bodies
type V2Body struct {
	SessionID string
	TurnNo    int
	TenantID  string
	RequestID string
	Ts        time.Time

	RequestDelta  json.RawMessage
	ResponseDelta json.RawMessage
	OutboundBody  json.RawMessage

	RequestAttachments  json.RawMessage
	ResponseAttachments json.RawMessage
}

// V2Session represents the session snapshot from sessions table
type V2Session struct {
	SessionID string
	TenantID  string
	CreatedAt time.Time
	UpdatedAt time.Time
	Status    string

	TotalTurns   int
	TotalTokens  int
	TotalCostUSD float64

	LastTurnNo          int
	LastRequestSummary  string
	LastResponseSummary string
	LastModel           string
	LastProvider        string

	PrimaryRequestID string
}

// SessionLoader loads V1 and V2 data for validation
type SessionLoader struct {
	db *pgxpool.Pool
}

// NewSessionLoader creates a new session loader
func NewSessionLoader(db *pgxpool.Pool) *SessionLoader {
	return &SessionLoader{db: db}
}

// LoadV1Turns loads all turns for a session from request_logs
func (l *SessionLoader) LoadV1Turns(ctx context.Context, tenantID, sessionID string) ([]V1Turn, error) {
	query := `
		SELECT 
			request_id,
			ts,
			session_id,
			tenant_id,
			COALESCE(client_model, '') as client_model,
			COALESCE(provider_id, '') as provider_id,
			COALESCE(credential_id, '') as credential_id,
			COALESCE(usage, '{}'::jsonb) as usage,
			COALESCE(cost_usd, 0) as cost_usd,
			COALESCE(compression_meta, '{}'::jsonb) as compression_meta,
			COALESCE(body, '{}'::jsonb) as request_body,
			COALESCE(response, '{}'::jsonb) as response_body,
			COALESCE(success, false) as success
		FROM gateway.request_logs
		WHERE tenant_id = $1 AND session_id = $2
		ORDER BY ts ASC
	`

	rows, err := l.db.Query(ctx, query, tenantID, sessionID)
	if err != nil {
		return nil, fmt.Errorf("query request_logs: %w", err)
	}
	defer rows.Close()

	var turns []V1Turn
	for rows.Next() {
		var turn V1Turn
		err := rows.Scan(
			&turn.RequestID,
			&turn.Ts,
			&turn.SessionID,
			&turn.TenantID,
			&turn.ClientModel,
			&turn.ProviderID,
			&turn.CredentialID,
			&turn.Usage,
			&turn.CostUSD,
			&turn.CompressionMeta,
			&turn.RequestBody,
			&turn.ResponseBody,
			&turn.Success,
		)
		if err != nil {
			return nil, fmt.Errorf("scan request_logs row: %w", err)
		}
		turns = append(turns, turn)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate request_logs: %w", err)
	}

	return turns, nil
}

// LoadV2Turns loads all turns for a session from session_turns
func (l *SessionLoader) LoadV2Turns(ctx context.Context, tenantID, sessionID string) ([]V2Turn, error) {
	query := `
		SELECT 
			request_id,
			turn_no,
			ts,
			session_id,
			tenant_id,
			submit_mode,
			COALESCE(model, '') as model,
			COALESCE(provider, '') as provider,
			COALESCE(credential_id, '') as credential_id,
			COALESCE(prompt_tokens, 0) as prompt_tokens,
			COALESCE(completion_tokens, 0) as completion_tokens,
			COALESCE(cache_read_tokens, 0) as cache_read_tokens,
			COALESCE(cache_write_tokens, 0) as cache_write_tokens,
			COALESCE(cost_usd, 0) as cost_usd,
			COALESCE(injection_verdict, 'skip') as injection_verdict,
			COALESCE(output_verdict, 'skip') as output_verdict,
			COALESCE(latency_ms, 0) as latency_ms,
			COALESCE(status_code, 0) as status_code,
			COALESCE(success, false) as success,
			COALESCE(error_kind, '') as error_kind,
			source_kind,
			quality
		FROM public.session_turns
		WHERE tenant_id = $1 AND session_id = $2
		ORDER BY turn_no ASC
	`

	rows, err := l.db.Query(ctx, query, tenantID, sessionID)
	if err != nil {
		return nil, fmt.Errorf("query session_turns: %w", err)
	}
	defer rows.Close()

	var turns []V2Turn
	for rows.Next() {
		var turn V2Turn
		err := rows.Scan(
			&turn.RequestID,
			&turn.TurnNo,
			&turn.Ts,
			&turn.SessionID,
			&turn.TenantID,
			&turn.SubmitMode,
			&turn.Model,
			&turn.Provider,
			&turn.CredentialID,
			&turn.PromptTokens,
			&turn.CompletionTokens,
			&turn.CacheReadTokens,
			&turn.CacheWriteTokens,
			&turn.CostUSD,
			&turn.InjectionVerdict,
			&turn.OutputVerdict,
			&turn.LatencyMs,
			&turn.StatusCode,
			&turn.Success,
			&turn.ErrorKind,
			&turn.SourceKind,
			&turn.Quality,
		)
		if err != nil {
			return nil, fmt.Errorf("scan session_turns row: %w", err)
		}
		turns = append(turns, turn)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate session_turns: %w", err)
	}

	return turns, nil
}

// LoadV2Bodies loads all bodies for a session from session_bodies
func (l *SessionLoader) LoadV2Bodies(ctx context.Context, tenantID, sessionID string) ([]V2Body, error) {
	query := `
		SELECT 
			session_id,
			turn_no,
			tenant_id,
			request_id,
			ts,
			COALESCE(request_delta, '[]'::jsonb) as request_delta,
			COALESCE(response_delta, '[]'::jsonb) as response_delta,
			COALESCE(outbound_body, '[]'::jsonb) as outbound_body,
			COALESCE(request_attachments, '[]'::jsonb) as request_attachments,
			COALESCE(response_attachments, '[]'::jsonb) as response_attachments
		FROM public.session_bodies
		WHERE tenant_id = $1 AND session_id = $2
		ORDER BY turn_no ASC
	`

	rows, err := l.db.Query(ctx, query, tenantID, sessionID)
	if err != nil {
		return nil, fmt.Errorf("query session_bodies: %w", err)
	}
	defer rows.Close()

	var bodies []V2Body
	for rows.Next() {
		var body V2Body
		err := rows.Scan(
			&body.SessionID,
			&body.TurnNo,
			&body.TenantID,
			&body.RequestID,
			&body.Ts,
			&body.RequestDelta,
			&body.ResponseDelta,
			&body.OutboundBody,
			&body.RequestAttachments,
			&body.ResponseAttachments,
		)
		if err != nil {
			return nil, fmt.Errorf("scan session_bodies row: %w", err)
		}
		bodies = append(bodies, body)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate session_bodies: %w", err)
	}

	return bodies, nil
}

// LoadV2Session loads the session snapshot from sessions table
func (l *SessionLoader) LoadV2Session(ctx context.Context, tenantID, sessionID string) (*V2Session, error) {
	query := `
		SELECT 
			session_id,
			tenant_id,
			created_at,
			updated_at,
			status,
			total_turns,
			total_tokens,
			total_cost_usd,
			COALESCE(last_turn_no, 0) as last_turn_no,
			COALESCE(last_request_summary, '') as last_request_summary,
			COALESCE(last_response_summary, '') as last_response_summary,
			COALESCE(last_model, '') as last_model,
			COALESCE(last_provider, '') as last_provider,
			COALESCE(primary_request_id, '') as primary_request_id
		FROM public.sessions
		WHERE tenant_id = $1 AND session_id = $2
		LIMIT 1
	`

	var session V2Session
	err := l.db.QueryRow(ctx, query, tenantID, sessionID).Scan(
		&session.SessionID,
		&session.TenantID,
		&session.CreatedAt,
		&session.UpdatedAt,
		&session.Status,
		&session.TotalTurns,
		&session.TotalTokens,
		&session.TotalCostUSD,
		&session.LastTurnNo,
		&session.LastRequestSummary,
		&session.LastResponseSummary,
		&session.LastModel,
		&session.LastProvider,
		&session.PrimaryRequestID,
	)

	if err == pgx.ErrNoRows {
		return nil, nil // Session not found in V2
	}
	if err != nil {
		return nil, fmt.Errorf("query sessions: %w", err)
	}

	return &session, nil
}

// LoadSessionsInRange loads session IDs within a date range for batch validation
func (l *SessionLoader) LoadSessionsInRange(ctx context.Context, tenantID string, startDate, endDate time.Time, settleWindow time.Duration, maxSessions int) ([]string, error) {
	settleThreshold := time.Now().Add(-settleWindow)

	query := `
		SELECT DISTINCT session_id
		FROM gateway.request_logs
		WHERE tenant_id = $1
		  AND ts >= $2
		  AND ts < $3
		  AND session_id IS NOT NULL
		  AND session_id != ''
		ORDER BY session_id
		LIMIT $4
	`

	// For batch mode, we select from request_logs and filter by settle window
	// We'll additionally filter by updated_at from sessions table if it exists
	rows, err := l.db.Query(ctx, query, tenantID, startDate, endDate, maxSessions)
	if err != nil {
		return nil, fmt.Errorf("query session IDs: %w", err)
	}
	defer rows.Close()

	var sessionIDs []string
	for rows.Next() {
		var sessionID string
		if err := rows.Scan(&sessionID); err != nil {
			return nil, fmt.Errorf("scan session_id: %w", err)
		}

		// Check if session is settled (last update > settle window ago)
		var lastUpdate time.Time
		err := l.db.QueryRow(ctx, `
			SELECT MAX(ts) FROM gateway.request_logs
			WHERE tenant_id = $1 AND session_id = $2
		`, tenantID, sessionID).Scan(&lastUpdate)

		if err == nil && lastUpdate.Before(settleThreshold) {
			sessionIDs = append(sessionIDs, sessionID)
		}
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate session IDs: %w", err)
	}

	return sessionIDs, nil
}
