// Package titlestore owns durable session-level title state.
// Redis locks may reduce duplicate LLM work, but only this store decides whether
// a title mutation is still allowed to commit.
package titlestore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

var (
	ErrNotConfigured = errors.New("titlestore: database not configured")
	ErrFenced        = errors.New("titlestore: fencing token is no longer current")
	ErrLeaseExpired  = errors.New("titlestore: mutation lease expired")
	ErrDeleted       = errors.New("titlestore: title is tombstoned")
	ErrPriority      = errors.New("titlestore: source priority is insufficient")
)

const (
	SourceAutoTitle       = "auto-title"
	SourceAutoSummary     = "auto-summary"
	SourceUserSummarize   = "user-summarize"
	SourceManual          = "manual"
	SourceNoTopic         = "no-topic"
	SourceSessionSummary  = "session-summary"
	SourcePriorityAuto    = 10
	SourcePrioritySummary = 20
	SourcePriorityUser    = 30
	SourcePriorityManual  = 40
)

type sourceInfo struct {
	name     string
	priority int
	explicit bool
}

func source(name string, priority int, explicit bool) sourceInfo {
	return sourceInfo{name: strings.TrimSpace(name), priority: priority, explicit: explicit}
}

// Source returns the standard priority for a writer. Unknown sources are
// deliberately low priority and cannot clear a tombstone.
func Source(name string) sourceInfo {
	switch strings.TrimSpace(name) {
	case SourceManual:
		return source(SourceManual, SourcePriorityManual, true)
	case SourceUserSummarize:
		return source(SourceUserSummarize, SourcePriorityUser, true)
	case SourceSessionSummary:
		return source(SourceSessionSummary, SourcePrioritySummary, true)
	case SourceAutoSummary:
		return source(SourceAutoSummary, SourcePrioritySummary, false)
	case SourceAutoTitle:
		return source(SourceAutoTitle, SourcePriorityAuto, false)
	case SourceNoTopic:
		return source(SourceNoTopic, SourcePriorityAuto, false)
	default:
		return source(strings.TrimSpace(name), SourcePriorityAuto, false)
	}
}

type State struct {
	TenantID       string
	SessionID      string
	Title          string
	Deleted        bool
	DeletedAt      *time.Time
	FencingToken   int64
	LeaseOwner     string
	LeaseExpiresAt *time.Time
	Source         string
	SourcePriority int
	SourceTaskID   string
}

type Claim struct {
	TenantID       string
	SessionID      string
	Owner          string
	TTL            time.Duration
	Source         string
	SourcePriority int
	Explicit       bool
	TaskID         string
}

type ClaimResult struct {
	Token          int64
	Owner          string
	LeaseExpiresAt time.Time
	Source         string
	SourcePriority int
}

type CommitResult struct {
	Committed            bool
	ProjectionRows       int64
	LegacyProjectionRows int64
}

type DeleteResult struct {
	Deleted bool
	Token   int64
}

type dbPool interface {
	Begin(ctx context.Context) (pgx.Tx, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type Store struct {
	pool dbPool
}

func New(pool dbPool) *Store { return &Store{pool: pool} }

func (s *Store) BeginMutation(ctx context.Context, req Claim) (ClaimResult, error) {
	if s == nil || s.pool == nil {
		return ClaimResult{}, ErrNotConfigured
	}
	tenantID := strings.TrimSpace(req.TenantID)
	sessionID := strings.TrimSpace(req.SessionID)
	owner := strings.TrimSpace(req.Owner)
	if tenantID == "" || sessionID == "" || owner == "" {
		return ClaimResult{}, fmt.Errorf("titlestore: tenant, session and owner are required")
	}
	if req.TTL <= 0 {
		req.TTL = 60 * time.Second
	}
	info := source(req.Source, req.SourcePriority, req.Explicit)
	if info.name == "" {
		info = Source(req.Source)
	}
	if info.priority < 0 {
		return ClaimResult{}, fmt.Errorf("titlestore: source priority must be non-negative")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ClaimResult{}, fmt.Errorf("titlestore: begin claim: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	if _, err = tx.Exec(ctx, `
		INSERT INTO public.session_title_states (tenant_id, scoped_session_id, source, source_priority, source_task_id)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''))
		ON CONFLICT (tenant_id, scoped_session_id) DO NOTHING
	`, tenantID, sessionID, info.name, info.priority, strings.TrimSpace(req.TaskID)); err != nil {
		return ClaimResult{}, fmt.Errorf("titlestore: create state: %w", err)
	}

	var current State
	var deletedAt, leaseExpires *time.Time
	err = tx.QueryRow(ctx, `
		SELECT COALESCE(title, ''), deleted, deleted_at, fencing_token,
		       COALESCE(lease_owner, ''), lease_expires_at,
		       COALESCE(source, ''), source_priority, COALESCE(source_task_id, '')
		FROM public.session_title_states
		WHERE tenant_id = $1 AND scoped_session_id = $2
		FOR UPDATE
	`, tenantID, sessionID).Scan(&current.Title, &current.Deleted, &deletedAt,
		&current.FencingToken, &current.LeaseOwner, &leaseExpires,
		&current.Source, &current.SourcePriority, &current.SourceTaskID)
	if err != nil {
		return ClaimResult{}, fmt.Errorf("titlestore: lock state: %w", err)
	}
	current.DeletedAt, current.LeaseExpiresAt = deletedAt, leaseExpires
	if current.Deleted && !info.explicit {
		return ClaimResult{}, ErrDeleted
	}
	// Explicit writers may clear a tombstone, but they still cannot replace an
	// active title written by a higher-priority source.
	if !current.Deleted && info.priority < current.SourcePriority {
		return ClaimResult{}, ErrPriority
	}

	expires := time.Now().UTC().Add(req.TTL)
	newToken := current.FencingToken + 1
	_, err = tx.Exec(ctx, `
		UPDATE public.session_title_states
		SET fencing_token = $3,
		    lease_owner = $4,
		    lease_expires_at = $5,
		    source = $6,
		    source_priority = $7,
		    source_task_id = NULLIF($8, ''),
		    deleted = CASE WHEN $9 THEN false ELSE deleted END,
		    deleted_at = CASE WHEN $9 THEN NULL ELSE deleted_at END,
		    updated_at = NOW()
		WHERE tenant_id = $1 AND scoped_session_id = $2
	`, tenantID, sessionID, newToken, owner, expires, info.name, info.priority,
		strings.TrimSpace(req.TaskID), info.explicit)
	if err != nil {
		return ClaimResult{}, fmt.Errorf("titlestore: advance fencing token: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return ClaimResult{}, fmt.Errorf("titlestore: commit claim: %w", err)
	}
	committed = true
	return ClaimResult{Token: newToken, Owner: owner, LeaseExpiresAt: expires,
		Source: info.name, SourcePriority: info.priority}, nil
}

func (s *Store) CommitTitle(ctx context.Context, claim ClaimResult, tenantID, sessionID, title, taskID string) (CommitResult, error) {
	if s == nil || s.pool == nil {
		return CommitResult{}, ErrNotConfigured
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return CommitResult{}, fmt.Errorf("titlestore: title is empty")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return CommitResult{}, fmt.Errorf("titlestore: begin commit: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	var n int64
	err = tx.QueryRow(ctx, `
		UPDATE public.session_title_states
		SET title = $5, deleted = false, deleted_at = NULL,
		    source_task_id = NULLIF($6, ''), updated_at = NOW()
		WHERE tenant_id = $1 AND scoped_session_id = $2
		  AND fencing_token = $3 AND lease_owner = $4
		  AND lease_expires_at > NOW()
		RETURNING fencing_token
	`, strings.TrimSpace(tenantID), strings.TrimSpace(sessionID), claim.Token, claim.Owner, title, strings.TrimSpace(taskID)).Scan(&n)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return CommitResult{}, ErrFenced
		}
		return CommitResult{}, fmt.Errorf("titlestore: conditional commit: %w", err)
	}

	var projectionRows int64
	err = tx.QueryRow(ctx, `
		WITH changed AS (
			UPDATE public.sessions
			SET title = $3, updated_at = NOW()
			WHERE tenant_id = $1 AND session_id = $2
			  AND partition_date = (SELECT MAX(partition_date) FROM public.sessions WHERE tenant_id = $1 AND session_id = $2)
			RETURNING 1
		)
		SELECT COUNT(*) FROM changed
	`, tenantID, sessionID, title).Scan(&projectionRows)
	if err != nil {
		return CommitResult{}, fmt.Errorf("titlestore: project sessions: %w", err)
	}
	if _, err = tx.Exec(ctx, `UPDATE public.session_summaries SET title = $3, updated_at = NOW() WHERE session_key = $1 AND tenant_id = $2`, sessionID, tenantID, title); err != nil {
		return CommitResult{}, fmt.Errorf("titlestore: project summaries: %w", err)
	}
	var legacyRows int64
	if strings.TrimSpace(taskID) != "" {
		tag, e := tx.Exec(ctx, `
			INSERT INTO public.session_titles (task_id, scoped_session_id, title, generated_at, model, api_key_id)
			VALUES ($1, $2, $3, NOW(), $4, NULL)
			ON CONFLICT (task_id, scoped_session_id) DO UPDATE SET title = EXCLUDED.title, generated_at = EXCLUDED.generated_at, model = EXCLUDED.model
		`, taskID, sessionID, title, claim.Source)
		if e != nil {
			return CommitResult{}, fmt.Errorf("titlestore: project legacy title: %w", e)
		}
		legacyRows = tag.RowsAffected()
	}
	if err = tx.Commit(ctx); err != nil {
		return CommitResult{}, fmt.Errorf("titlestore: commit title: %w", err)
	}
	committed = true
	return CommitResult{Committed: true, ProjectionRows: projectionRows, LegacyProjectionRows: legacyRows}, nil
}

func (s *Store) DeleteTitle(ctx context.Context, tenantID, sessionID, taskID, owner string) (DeleteResult, error) {
	claim, err := s.BeginMutation(ctx, Claim{TenantID: tenantID, SessionID: sessionID, Owner: owner, TTL: 30 * time.Second, Source: SourceManual, SourcePriority: SourcePriorityManual, Explicit: true, TaskID: taskID})
	if err != nil {
		return DeleteResult{}, err
	}
	if s == nil || s.pool == nil {
		return DeleteResult{}, ErrNotConfigured
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return DeleteResult{}, fmt.Errorf("titlestore: begin delete: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	var token int64
	err = tx.QueryRow(ctx, `
		UPDATE public.session_title_states
		SET title = NULL, deleted = true, deleted_at = NOW(), lease_owner = NULL,
		    lease_expires_at = NULL, source = $3, source_priority = $4, updated_at = NOW()
		WHERE tenant_id = $1 AND scoped_session_id = $2 AND fencing_token = $5
		RETURNING fencing_token
	`, tenantID, sessionID, SourceManual, SourcePriorityManual, claim.Token).Scan(&token)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return DeleteResult{}, ErrFenced
		}
		return DeleteResult{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE public.sessions SET title = NULL, updated_at = NOW() WHERE tenant_id = $1 AND session_id = $2 AND partition_date = (SELECT MAX(partition_date) FROM public.sessions WHERE tenant_id = $1 AND session_id = $2)`, tenantID, sessionID); err != nil {
		return DeleteResult{}, fmt.Errorf("titlestore: clear sessions title: %w", err)
	}
	if _, err = tx.Exec(ctx, `UPDATE public.session_summaries SET title = NULL, updated_at = NOW() WHERE session_key = $1 AND tenant_id = $2`, sessionID, tenantID); err != nil {
		return DeleteResult{}, fmt.Errorf("titlestore: clear summary title: %w", err)
	}
	if strings.TrimSpace(taskID) != "" {
		if _, err = tx.Exec(ctx, `DELETE FROM public.session_titles WHERE task_id = $1 AND scoped_session_id = $2`, taskID, sessionID); err != nil {
			return DeleteResult{}, fmt.Errorf("titlestore: clear legacy title: %w", err)
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return DeleteResult{}, fmt.Errorf("titlestore: commit delete: %w", err)
	}
	committed = true
	return DeleteResult{Deleted: true, Token: token}, nil
}

func (s *Store) Get(ctx context.Context, tenantID, sessionID string) (State, error) {
	if s == nil || s.pool == nil {
		return State{}, ErrNotConfigured
	}
	var st State
	var deletedAt, leaseExpires *time.Time
	err := s.pool.QueryRow(ctx, `SELECT $1, $2, COALESCE(title, ''), deleted, deleted_at, fencing_token, COALESCE(lease_owner, ''), lease_expires_at, COALESCE(source, ''), source_priority, COALESCE(source_task_id, '') FROM public.session_title_states WHERE tenant_id = $1 AND scoped_session_id = $2`, tenantID, sessionID).Scan(&st.TenantID, &st.SessionID, &st.Title, &st.Deleted, &deletedAt, &st.FencingToken, &st.LeaseOwner, &leaseExpires, &st.Source, &st.SourcePriority, &st.SourceTaskID)
	st.DeletedAt, st.LeaseExpiresAt = deletedAt, leaseExpires
	return st, err
}
