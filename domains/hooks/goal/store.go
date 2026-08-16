package goal

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/lib/pq"
)

// PGStore implements GoalStore using PostgreSQL.
type PGStore struct {
	db *sql.DB
}

// NewPGStore creates a new PostgreSQL-backed goal store.
func NewPGStore(db *sql.DB) *PGStore {
	return &PGStore{db: db}
}

// GetSession retrieves a tenant-owned goal session by session ID.
func (s *PGStore) GetSession(ctx context.Context, tenantID, sessionID string) (*Session, error) {
	query := `SELECT session_id, tenant_id, state, original_goal, retry_count,
	                 decision_count, auto_continue_count, last_activity_at,
	                 completed_at, audit_result, created_at,
	                 COALESCE(model_switch_count, 0),
	                 COALESCE(repeat_count, 0),
	                 COALESCE(last_response_hash, ''),
	                 COALESCE(current_model, '')
	          FROM goal_sessions WHERE tenant_id = $1 AND session_id = $2`

	var session Session
	var completedAt sql.NullTime
	var auditResult sql.NullString

	err := s.db.QueryRowContext(ctx, query, tenantID, sessionID).Scan(
		&session.SessionID, &session.TenantID, &session.State, &session.OriginalGoal,
		&session.RetryCount, &session.DecisionCount, &session.AutoContinueCount,
		&session.LastActivityAt, &completedAt, &auditResult, &session.CreatedAt,
		&session.ModelSwitchCount, &session.RepeatCount, &session.LastResponseHash,
		&session.CurrentModel,
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
	                                      last_activity_at, created_at,
	                                      model_switch_count, repeat_count,
	                                      last_response_hash, current_model)
	          VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 0, 0, '', $10)`
	_, err := s.db.ExecContext(ctx, query,
		session.SessionID, session.TenantID, session.State, session.OriginalGoal,
		session.RetryCount, session.DecisionCount, session.AutoContinueCount,
		session.LastActivityAt, session.CreatedAt, session.CurrentModel,
	)
	return err
}

func (s *PGStore) UpdateSessionState(ctx context.Context, tenantID, sessionID string, state State) error {
	_, err := s.db.ExecContext(ctx, `UPDATE goal_sessions
		SET state = $3, last_activity_at = NOW(),
		    completed_at = CASE WHEN $3 = 'completed' THEN NOW() ELSE completed_at END
		WHERE tenant_id = $1 AND session_id = $2`, tenantID, sessionID, state)
	return err
}

// CompareAndSetState atomically transitions state to target only when the
// current state is in allowedFrom. The terminal states (completed/failed) are
// sticky: once a concurrent writer wins either, racing writers (retry-exhaustion
// giving up, provider retry exhaustion) observe rows-affected == 0 and return
// (false, nil) without overwriting.
//
// The allowedFrom parameter is rendered into a PostgreSQL text[] literal so
// callers don't have to know the parameter count at the SQL boundary. An empty
// allowedFrom behaves like a never-transition CAS: always returns false.
func (s *PGStore) CompareAndSetState(ctx context.Context, tenantID, sessionID string, allowedFrom []State, target State) (bool, error) {
	if len(allowedFrom) == 0 {
		return false, nil
	}
	states := make([]string, len(allowedFrom))
	for i, st := range allowedFrom {
		states[i] = string(st)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE goal_sessions
		SET state = $3, last_activity_at = NOW(),
		    completed_at = CASE WHEN $3 = 'completed' THEN NOW() ELSE completed_at END
		WHERE tenant_id = $1 AND session_id = $2 AND state = ANY($4)`,
		tenantID, sessionID, string(target), pq.Array(states))
	if err != nil {
		return false, err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return rows == 1, nil
}

func (s *PGStore) IncrementAutoContinueCount(ctx context.Context, tenantID, sessionID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE goal_sessions
		SET auto_continue_count = auto_continue_count + 1, last_activity_at = NOW()
		WHERE tenant_id = $1 AND session_id = $2`, tenantID, sessionID)
	return err
}

func (s *PGStore) IncrementDecisionCount(ctx context.Context, tenantID, sessionID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE goal_sessions
		SET decision_count = decision_count + 1, last_activity_at = NOW()
		WHERE tenant_id = $1 AND session_id = $2`, tenantID, sessionID)
	return err
}

func (s *PGStore) UpdateSessionAudit(ctx context.Context, tenantID, sessionID string, auditResult []byte) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE goal_sessions
		SET audit_result = $3, last_activity_at = NOW()
		WHERE tenant_id = $1 AND session_id = $2 AND audit_result IS NULL`, tenantID, sessionID, auditResult)
	if err != nil {
		return false, err
	}
	rows, _ := res.RowsAffected()
	return rows == 1, nil
}

func (s *PGStore) AtomicAutoContinue(ctx context.Context, tenantID, sessionID string, maxAllowed int) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE goal_sessions
		SET auto_continue_count = auto_continue_count + 1, last_activity_at = NOW()
		WHERE tenant_id = $1 AND session_id = $2 AND auto_continue_count < $3`, tenantID, sessionID, maxAllowed)
	if err != nil {
		return false, err
	}
	rows, _ := res.RowsAffected()
	return rows == 1, nil
}

func (s *PGStore) RecordResponse(ctx context.Context, tenantID, sessionID, responseHash string, resetOnProgress bool) (int, error) {
	var newCount int
	err := s.db.QueryRowContext(ctx, `UPDATE goal_sessions
		SET repeat_count = CASE
		      WHEN last_response_hash = $3 THEN repeat_count + 1
		      WHEN $4 THEN 1
		      ELSE repeat_count + 1
		    END,
		    last_response_hash = $3,
		    last_activity_at = NOW()
		WHERE tenant_id = $1 AND session_id = $2
		RETURNING repeat_count`, tenantID, sessionID, responseHash, resetOnProgress).Scan(&newCount)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return newCount, nil
}

func (s *PGStore) AtomicModelSwitch(ctx context.Context, tenantID, sessionID, newModel string, maxAllowed int) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE goal_sessions
		SET model_switch_count = model_switch_count + 1,
		    auto_continue_count = 0,
		    current_model = $3,
		    last_activity_at = NOW()
		WHERE tenant_id = $1 AND session_id = $2 AND model_switch_count < $4`, tenantID, sessionID, newModel, maxAllowed)
	if err != nil {
		return false, err
	}
	rows, _ := res.RowsAffected()
	return rows == 1, nil
}

// AddRetryCount atomically increments retry_count by delta. It remains
// fail-open when Goal mode is not active or tenant/session identity is absent.
func (s *PGStore) AddRetryCount(ctx context.Context, tenantID, sessionID string, delta int) error {
	if tenantID == "" || sessionID == "" || delta <= 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `UPDATE goal_sessions
		SET retry_count = retry_count + $3, last_activity_at = NOW()
		WHERE tenant_id = $1 AND session_id = $2`, tenantID, sessionID, delta)
	return err
}
