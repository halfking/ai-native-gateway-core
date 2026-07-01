package goal

import (
	"context"
	"database/sql"
	"encoding/json"
)

// PGStore implements GoalStore using PostgreSQL.
type PGStore struct {
	db *sql.DB
}

// NewPGStore creates a new PostgreSQL-backed goal store.
func NewPGStore(db *sql.DB) *PGStore {
	return &PGStore{db: db}
}

// GetSession retrieves a goal session by session ID.
func (s *PGStore) GetSession(ctx context.Context, sessionID string) (*Session, error) {
	query := `SELECT session_id, tenant_id, state, original_goal, retry_count, 
	                 decision_count, auto_continue_count, last_activity_at, 
	                 completed_at, audit_result, created_at
	          FROM goal_sessions WHERE session_id = $1`

	var session Session
	var completedAt sql.NullTime
	var auditResult sql.NullString

	err := s.db.QueryRowContext(ctx, query, sessionID).Scan(
		&session.SessionID, &session.TenantID, &session.State, &session.OriginalGoal,
		&session.RetryCount, &session.DecisionCount, &session.AutoContinueCount,
		&session.LastActivityAt, &completedAt, &auditResult, &session.CreatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if completedAt.Valid {
		session.CompletedAt = &completedAt.Time
	}
	if auditResult.Valid {
		session.AuditResult = json.RawMessage(auditResult.String)
	}

	return &session, nil
}

// CreateSession creates a new goal session.
func (s *PGStore) CreateSession(ctx context.Context, session *Session) error {
	query := `INSERT INTO goal_sessions (session_id, tenant_id, state, original_goal, 
	                                      retry_count, decision_count, auto_continue_count,
	                                      last_activity_at, created_at)
	          VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`

	_, err := s.db.ExecContext(ctx, query,
		session.SessionID, session.TenantID, session.State, session.OriginalGoal,
		session.RetryCount, session.DecisionCount, session.AutoContinueCount,
		session.LastActivityAt, session.CreatedAt,
	)
	return err
}

// UpdateSessionState updates the state of a goal session.
func (s *PGStore) UpdateSessionState(ctx context.Context, sessionID string, state State) error {
	query := `UPDATE goal_sessions 
	          SET state = $2, last_activity_at = NOW(), 
	              completed_at = CASE WHEN $2 = 'completed' THEN NOW() ELSE completed_at END
	          WHERE session_id = $1`

	_, err := s.db.ExecContext(ctx, query, sessionID, state)
	return err
}

// IncrementAutoContinueCount increments the auto-continue counter.
func (s *PGStore) IncrementAutoContinueCount(ctx context.Context, sessionID string) error {
	query := `UPDATE goal_sessions 
	          SET auto_continue_count = auto_continue_count + 1, last_activity_at = NOW()
	          WHERE session_id = $1`

	_, err := s.db.ExecContext(ctx, query, sessionID)
	return err
}

// IncrementDecisionCount increments the decision counter.
func (s *PGStore) IncrementDecisionCount(ctx context.Context, sessionID string) error {
	query := `UPDATE goal_sessions
	          SET decision_count = decision_count + 1, last_activity_at = NOW()
	          WHERE session_id = $1`

	_, err := s.db.ExecContext(ctx, query, sessionID)
	return err
}

// UpdateSessionAudit persists the audit result for a session.
// Uses atomic CAS pattern: only updates if audit_result is NULL, preventing
// concurrent audits from overwriting each other. Returns true if the update
// was applied (this caller won the race), false if another audit already
// wrote a result (caller should skip its own audit).
func (s *PGStore) UpdateSessionAudit(ctx context.Context, sessionID string, auditResult []byte) (bool, error) {
	query := `UPDATE goal_sessions
	          SET audit_result = $2, last_activity_at = NOW()
	          WHERE session_id = $1 AND audit_result IS NULL`

	res, err := s.db.ExecContext(ctx, query, sessionID, auditResult)
	if err != nil {
		return false, err
	}
	rows, _ := res.RowsAffected()
	return rows == 1, nil
}

// AtomicAutoContinue atomically increments auto_continue_count only if
// the new value would still be <= maxAllowed. Returns true if the
// caller "won" the increment (and should proceed with auto-continue),
// false if the count is already at the limit.
func (s *PGStore) AtomicAutoContinue(ctx context.Context, sessionID string, maxAllowed int) (bool, error) {
	query := `UPDATE goal_sessions
	          SET auto_continue_count = auto_continue_count + 1, last_activity_at = NOW()
	          WHERE session_id = $1 AND auto_continue_count < $2`

	res, err := s.db.ExecContext(ctx, query, sessionID, maxAllowed)
	if err != nil {
		return false, err
	}
	rows, _ := res.RowsAffected()
	return rows == 1, nil
}
