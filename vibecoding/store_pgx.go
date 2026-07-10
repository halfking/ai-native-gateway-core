package vibecoding

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PgxStore PostgreSQL实现
type PgxStore struct {
	db *pgxpool.Pool
}

// NewPgxStore 创建PostgreSQL存储
func NewPgxStore(db *pgxpool.Pool) *PgxStore {
	return &PgxStore{db: db}
}

// CreateProject 创建项目
func (s *PgxStore) CreateProject(ctx context.Context, project *Project) error {
	settingsJSON, _ := json.Marshal(project.Settings)
	query := `
		INSERT INTO vibe_coding_projects (tenant_id, name, description, language, framework, status, settings, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id, created_at, updated_at
	`
	return s.db.QueryRow(ctx, query,
		project.TenantID, project.Name, project.Description, project.Language,
		project.Framework, project.Status, settingsJSON, project.CreatedBy,
	).Scan(&project.ID, &project.CreatedAt, &project.UpdatedAt)
}

// GetProject 获取项目
func (s *PgxStore) GetProject(ctx context.Context, id int64) (*Project, error) {
	query := `
		SELECT id, tenant_id, name, description, language, framework, status, settings, created_by, created_at, updated_at
		FROM vibe_coding_projects
		WHERE id = $1
	`
	project := &Project{}
	var settingsJSON []byte
	err := s.db.QueryRow(ctx, query, id).Scan(
		&project.ID, &project.TenantID, &project.Name, &project.Description,
		&project.Language, &project.Framework, &project.Status, &settingsJSON,
		&project.CreatedBy, &project.CreatedAt, &project.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	if len(settingsJSON) > 0 {
		_ = json.Unmarshal(settingsJSON, &project.Settings)
	}

	return project, nil
}

// ListProjects 列出项目
func (s *PgxStore) ListProjects(ctx context.Context, tenantID string, status ProjectStatus, offset, limit int) ([]Project, int, error) {
	// 查询总数
	var total int
	countQuery := `SELECT COUNT(*) FROM vibe_coding_projects WHERE tenant_id = $1 AND ($2 = '' OR status = $2)`
	if err := s.db.QueryRow(ctx, countQuery, tenantID, status).Scan(&total); err != nil {
		return nil, 0, err
	}

	// 查询列表
	query := `
		SELECT id, tenant_id, name, description, language, framework, status, settings, created_by, created_at, updated_at
		FROM vibe_coding_projects
		WHERE tenant_id = $1 AND ($2 = '' OR status = $2)
		ORDER BY created_at DESC
		OFFSET $3 LIMIT $4
	`
	rows, err := s.db.Query(ctx, query, tenantID, status, offset, limit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var projects []Project
	for rows.Next() {
		var project Project
		var settingsJSON []byte
		if err := rows.Scan(
			&project.ID, &project.TenantID, &project.Name, &project.Description,
			&project.Language, &project.Framework, &project.Status, &settingsJSON,
			&project.CreatedBy, &project.CreatedAt, &project.UpdatedAt,
		); err != nil {
			return nil, 0, err
		}

		if len(settingsJSON) > 0 {
			_ = json.Unmarshal(settingsJSON, &project.Settings)
		}

		projects = append(projects, project)
	}

	return projects, total, nil
}

// UpdateProject 更新项目
func (s *PgxStore) UpdateProject(ctx context.Context, project *Project) error {
	settingsJSON, _ := json.Marshal(project.Settings)
	query := `
		UPDATE vibe_coding_projects
		SET name = $2, description = $3, language = $4, framework = $5, status = $6, settings = $7, updated_at = now()
		WHERE id = $1
		RETURNING updated_at
	`
	return s.db.QueryRow(ctx, query,
		project.ID, project.Name, project.Description, project.Language,
		project.Framework, project.Status, settingsJSON,
	).Scan(&project.UpdatedAt)
}

// DeleteProject 删除项目（软删除）
func (s *PgxStore) DeleteProject(ctx context.Context, id int64) error {
	query := `UPDATE vibe_coding_projects SET status = $2, updated_at = now() WHERE id = $1`
	_, err := s.db.Exec(ctx, query, id, ProjectStatusDeleted)
	return err
}

// CreateSession 创建会话
func (s *PgxStore) CreateSession(ctx context.Context, session *Session) error {
	messagesJSON, _ := json.Marshal(session.Messages)
	metadataJSON, _ := json.Marshal(session.Metadata)
	query := `
		INSERT INTO vibe_coding_sessions (project_id, tenant_id, session_id, task_type, status, messages, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, created_at
	`
	return s.db.QueryRow(ctx, query,
		session.ProjectID, session.TenantID, session.SessionID, session.TaskType,
		session.Status, messagesJSON, metadataJSON,
	).Scan(&session.ID, &session.CreatedAt)
}

// GetSession 获取会话
func (s *PgxStore) GetSession(ctx context.Context, id int64) (*Session, error) {
	query := `
		SELECT id, project_id, tenant_id, session_id, task_type, status, messages, metadata, created_at, completed_at
		FROM vibe_coding_sessions
		WHERE id = $1
	`
	session := &Session{}
	var messagesJSON, metadataJSON []byte
	err := s.db.QueryRow(ctx, query, id).Scan(
		&session.ID, &session.ProjectID, &session.TenantID, &session.SessionID,
		&session.TaskType, &session.Status, &messagesJSON, &metadataJSON,
		&session.CreatedAt, &session.CompletedAt,
	)
	if err != nil {
		return nil, err
	}

	if len(messagesJSON) > 0 {
		_ = json.Unmarshal(messagesJSON, &session.Messages)
	}
	if len(metadataJSON) > 0 {
		_ = json.Unmarshal(metadataJSON, &session.Metadata)
	}

	return session, nil
}

// GetSessionBySessionID 根据SessionID获取会话
func (s *PgxStore) GetSessionBySessionID(ctx context.Context, sessionID string) (*Session, error) {
	query := `
		SELECT id, project_id, tenant_id, session_id, task_type, status, messages, metadata, created_at, completed_at
		FROM vibe_coding_sessions
		WHERE session_id = $1
	`
	session := &Session{}
	var messagesJSON, metadataJSON []byte
	err := s.db.QueryRow(ctx, query, sessionID).Scan(
		&session.ID, &session.ProjectID, &session.TenantID, &session.SessionID,
		&session.TaskType, &session.Status, &messagesJSON, &metadataJSON,
		&session.CreatedAt, &session.CompletedAt,
	)
	if err != nil {
		return nil, err
	}

	if len(messagesJSON) > 0 {
		_ = json.Unmarshal(messagesJSON, &session.Messages)
	}
	if len(metadataJSON) > 0 {
		_ = json.Unmarshal(metadataJSON, &session.Metadata)
	}

	return session, nil
}

// ListSessions 列出会话
func (s *PgxStore) ListSessions(ctx context.Context, projectID *int64, status SessionStatus, offset, limit int) ([]Session, int, error) {
	// 查询总数
	var total int
	countQuery := `
		SELECT COUNT(*) FROM vibe_coding_sessions
		WHERE ($1::bigint IS NULL OR project_id = $1) AND ($2 = '' OR status = $2)
	`
	if err := s.db.QueryRow(ctx, countQuery, projectID, status).Scan(&total); err != nil {
		return nil, 0, err
	}

	// 查询列表
	query := `
		SELECT id, project_id, tenant_id, session_id, task_type, status, messages, metadata, created_at, completed_at
		FROM vibe_coding_sessions
		WHERE ($1::bigint IS NULL OR project_id = $1) AND ($2 = '' OR status = $2)
		ORDER BY created_at DESC
		OFFSET $3 LIMIT $4
	`
	rows, err := s.db.Query(ctx, query, projectID, status, offset, limit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var sessions []Session
	for rows.Next() {
		var session Session
		var messagesJSON, metadataJSON []byte
		if err := rows.Scan(
			&session.ID, &session.ProjectID, &session.TenantID, &session.SessionID,
			&session.TaskType, &session.Status, &messagesJSON, &metadataJSON,
			&session.CreatedAt, &session.CompletedAt,
		); err != nil {
			return nil, 0, err
		}

		if len(messagesJSON) > 0 {
			_ = json.Unmarshal(messagesJSON, &session.Messages)
		}
		if len(metadataJSON) > 0 {
			_ = json.Unmarshal(metadataJSON, &session.Metadata)
		}

		sessions = append(sessions, session)
	}

	return sessions, total, nil
}

// UpdateSession 更新会话
func (s *PgxStore) UpdateSession(ctx context.Context, session *Session) error {
	messagesJSON, _ := json.Marshal(session.Messages)
	metadataJSON, _ := json.Marshal(session.Metadata)
	query := `
		UPDATE vibe_coding_sessions
		SET task_type = $2, status = $3, messages = $4, metadata = $5, completed_at = $6
		WHERE id = $1
	`
	_, err := s.db.Exec(ctx, query,
		session.ID, session.TaskType, session.Status, messagesJSON, metadataJSON, session.CompletedAt,
	)
	return err
}

// UpdateSessionStatus 更新会话状态
func (s *PgxStore) UpdateSessionStatus(ctx context.Context, id int64, status SessionStatus, completedAt *time.Time) error {
	query := `UPDATE vibe_coding_sessions SET status = $2, completed_at = $3 WHERE id = $1`
	_, err := s.db.Exec(ctx, query, id, status, completedAt)
	return err
}

// CreateReview 创建代码审查
func (s *PgxStore) CreateReview(ctx context.Context, review *Review) error {
	reviewResultJSON, _ := json.Marshal(review.ReviewResult)
	query := `
		INSERT INTO vibe_code_reviews (session_id, tenant_id, file_path, language, original_code, review_result, score)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, created_at
	`
	return s.db.QueryRow(ctx, query,
		review.SessionID, review.TenantID, review.FilePath, review.Language,
		review.OriginalCode, reviewResultJSON, review.Score,
	).Scan(&review.ID, &review.CreatedAt)
}

// GetReview 获取代码审查
func (s *PgxStore) GetReview(ctx context.Context, id int64) (*Review, error) {
	query := `
		SELECT id, session_id, tenant_id, file_path, language, original_code, review_result, score, created_at
		FROM vibe_code_reviews
		WHERE id = $1
	`
	review := &Review{}
	var reviewResultJSON []byte
	err := s.db.QueryRow(ctx, query, id).Scan(
		&review.ID, &review.SessionID, &review.TenantID, &review.FilePath,
		&review.Language, &review.OriginalCode, &reviewResultJSON, &review.Score, &review.CreatedAt,
	)
	if err != nil {
		return nil, err
	}

	if len(reviewResultJSON) > 0 {
		_ = json.Unmarshal(reviewResultJSON, &review.ReviewResult)
	}

	return review, nil
}

// ListReviews 列出代码审查
func (s *PgxStore) ListReviews(ctx context.Context, sessionID *int64, offset, limit int) ([]Review, int, error) {
	// 查询总数
	var total int
	countQuery := `SELECT COUNT(*) FROM vibe_code_reviews WHERE ($1::bigint IS NULL OR session_id = $1)`
	if err := s.db.QueryRow(ctx, countQuery, sessionID).Scan(&total); err != nil {
		return nil, 0, err
	}

	// 查询列表
	query := `
		SELECT id, session_id, tenant_id, file_path, language, original_code, review_result, score, created_at
		FROM vibe_code_reviews
		WHERE ($1::bigint IS NULL OR session_id = $1)
		ORDER BY created_at DESC
		OFFSET $2 LIMIT $3
	`
	rows, err := s.db.Query(ctx, query, sessionID, offset, limit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var reviews []Review
	for rows.Next() {
		var review Review
		var reviewResultJSON []byte
		if err := rows.Scan(
			&review.ID, &review.SessionID, &review.TenantID, &review.FilePath,
			&review.Language, &review.OriginalCode, &reviewResultJSON, &review.Score, &review.CreatedAt,
		); err != nil {
			return nil, 0, err
		}

		if len(reviewResultJSON) > 0 {
			_ = json.Unmarshal(reviewResultJSON, &review.ReviewResult)
		}

		reviews = append(reviews, review)
	}

	return reviews, total, nil
}

// GetReviewsBySession 获取会话的所有审查
func (s *PgxStore) GetReviewsBySession(ctx context.Context, sessionID int64) ([]Review, error) {
	query := `
		SELECT id, session_id, tenant_id, file_path, language, original_code, review_result, score, created_at
		FROM vibe_code_reviews
		WHERE session_id = $1
		ORDER BY created_at DESC
	`
	rows, err := s.db.Query(ctx, query, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var reviews []Review
	for rows.Next() {
		var review Review
		var reviewResultJSON []byte
		if err := rows.Scan(
			&review.ID, &review.SessionID, &review.TenantID, &review.FilePath,
			&review.Language, &review.OriginalCode, &reviewResultJSON, &review.Score, &review.CreatedAt,
		); err != nil {
			return nil, err
		}

		if len(reviewResultJSON) > 0 {
			_ = json.Unmarshal(reviewResultJSON, &review.ReviewResult)
		}

		reviews = append(reviews, review)
	}

	return reviews, nil
}

// Ensure interface compliance
var _ Store = (*PgxStore)(nil)
