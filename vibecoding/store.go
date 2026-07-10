package vibecoding

import (
	"context"
	"time"
)

// Store VibeCoding存储接口
type Store interface {
	// Project CRUD
	CreateProject(ctx context.Context, project *Project) error
	GetProject(ctx context.Context, id int64) (*Project, error)
	ListProjects(ctx context.Context, tenantID string, status ProjectStatus, offset, limit int) ([]Project, int, error)
	UpdateProject(ctx context.Context, project *Project) error
	DeleteProject(ctx context.Context, id int64) error

	// Session CRUD
	CreateSession(ctx context.Context, session *Session) error
	GetSession(ctx context.Context, id int64) (*Session, error)
	GetSessionBySessionID(ctx context.Context, sessionID string) (*Session, error)
	ListSessions(ctx context.Context, projectID *int64, status SessionStatus, offset, limit int) ([]Session, int, error)
	UpdateSession(ctx context.Context, session *Session) error
	UpdateSessionStatus(ctx context.Context, id int64, status SessionStatus, completedAt *time.Time) error

	// Review CRUD
	CreateReview(ctx context.Context, review *Review) error
	GetReview(ctx context.Context, id int64) (*Review, error)
	ListReviews(ctx context.Context, sessionID *int64, offset, limit int) ([]Review, int, error)
	GetReviewsBySession(ctx context.Context, sessionID int64) ([]Review, error)
}
