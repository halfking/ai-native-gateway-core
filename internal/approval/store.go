package approval

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PgxApprovalStore implements ApprovalStore using PostgreSQL.
type PgxApprovalStore struct {
	pool *pgxpool.Pool
}

// NewPgxApprovalStore creates a new PostgreSQL-based approval store.
func NewPgxApprovalStore(pool *pgxpool.Pool) *PgxApprovalStore {
	return &PgxApprovalStore{pool: pool}
}

// Save inserts a new approval record.
func (s *PgxApprovalStore) Save(ctx context.Context, approval *Approval) error {
	query := `
		INSERT INTO approval_casdoor_mapping (
			request_id, session_id, tenant_id, status, provider,
			casdoor_task_id, created_at, expires_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`
	_, err := s.pool.Exec(ctx, query,
		approval.ID,
		approval.SessionID,
		approval.TenantID,
		approval.Status,
		approval.Provider,
		approval.CasdoorTaskID,
		approval.CreatedAt,
		approval.ExpiresAt,
	)
	if err != nil {
		return fmt.Errorf("insert approval: %w", err)
	}
	return nil
}

// Get retrieves an approval by request ID.
func (s *PgxApprovalStore) Get(ctx context.Context, requestID string) (*Approval, error) {
	query := `
		SELECT request_id, session_id, tenant_id, status, provider,
		       casdoor_task_id, approved_by, approved_at, reason,
		       created_at, expires_at
		FROM approval_casdoor_mapping
		WHERE request_id = $1
	`

	var approval Approval
	var approvedAt *time.Time

	err := s.pool.QueryRow(ctx, query, requestID).Scan(
		&approval.ID,
		&approval.SessionID,
		&approval.TenantID,
		&approval.Status,
		&approval.Provider,
		&approval.CasdoorTaskID,
		&approval.ApprovedBy,
		&approvedAt,
		&approval.Reason,
		&approval.CreatedAt,
		&approval.ExpiresAt,
	)

	if err != nil {
		if err.Error() == "no rows in result set" {
			return nil, nil
		}
		return nil, fmt.Errorf("query approval: %w", err)
	}

	approval.ApprovedAt = approvedAt
	return &approval, nil
}

// Update updates an existing approval record.
func (s *PgxApprovalStore) Update(ctx context.Context, approval *Approval) error {
	query := `
		UPDATE approval_casdoor_mapping
		SET status = $2,
		    approved_by = $3,
		    approved_at = $4,
		    reason = $5,
		    updated_at = NOW()
		WHERE request_id = $1
	`
	_, err := s.pool.Exec(ctx, query,
		approval.ID,
		approval.Status,
		approval.ApprovedBy,
		approval.ApprovedAt,
		approval.Reason,
	)
	if err != nil {
		return fmt.Errorf("update approval: %w", err)
	}
	return nil
}

// Migration SQL (add to db/migrations/):
//
// -- 202611150001_approval_casdoor_mapping.sql
// CREATE TABLE IF NOT EXISTS approval_casdoor_mapping (
//     request_id VARCHAR(100) PRIMARY KEY,
//     session_id VARCHAR(100) NOT NULL,
//     tenant_id VARCHAR(100) NOT NULL,
//     status VARCHAR(20) NOT NULL,
//     provider VARCHAR(20) NOT NULL,
//     casdoor_task_id INTEGER,
//     approved_by VARCHAR(200),
//     approved_at TIMESTAMPTZ,
//     reason TEXT,
//     created_at TIMESTAMPTZ NOT NULL,
//     expires_at TIMESTAMPTZ NOT NULL,
//     updated_at TIMESTAMPTZ DEFAULT NOW()
// );
//
// CREATE INDEX idx_casdoor_mapping_session ON approval_casdoor_mapping(session_id);
// CREATE INDEX idx_casdoor_mapping_status ON approval_casdoor_mapping(status, created_at);
// CREATE INDEX idx_casdoor_mapping_tenant ON approval_casdoor_mapping(tenant_id, status);
