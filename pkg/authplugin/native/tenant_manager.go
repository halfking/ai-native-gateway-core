package native

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/pkg/authplugin"
)

// NativeTenantManager implements authplugin.TenantManager using the existing
// PostgreSQL tenants table, preserving all original admin/tenants.go logic.
type NativeTenantManager struct {
	db *pgxpool.Pool
}

// NewNativeTenantManager creates a NativeTenantManager.
func NewNativeTenantManager(db *pgxpool.Pool) (*NativeTenantManager, error) {
	if db == nil {
		return nil, errors.New("native tenant manager: db must not be nil")
	}
	return &NativeTenantManager{db: db}, nil
}

// Name returns the manager identifier.
func (m *NativeTenantManager) Name() string { return "native" }

// SupportsFeature reports which optional TenantFeature flags this manager handles.
func (m *NativeTenantManager) SupportsFeature(f authplugin.TenantFeature) bool {
	return false // native manager provides core CRUD only
}

// GetTenant retrieves a tenant by its code/ID.
func (m *NativeTenantManager) GetTenant(ctx context.Context, tenantID string) (*authplugin.Tenant, error) {
	qctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	var t authplugin.Tenant
	err := m.db.QueryRow(qctx, `
		SELECT code, name, display_name, status, created_at, updated_at
		FROM tenants WHERE code = $1
	`, tenantID).Scan(&t.ID, &t.Name, &t.DisplayName, &t.Status, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return nil, authplugin.ErrNotFound
	}
	return &t, nil
}

// ListTenants returns tenants with optional filtering.
func (m *NativeTenantManager) ListTenants(ctx context.Context, filter *authplugin.TenantFilter) ([]*authplugin.Tenant, error) {
	qctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	var clauses []string
	var args []any
	idx := 1

	if filter != nil {
		if filter.Status != "" {
			clauses = append(clauses, fmt.Sprintf("status = $%d", idx))
			args = append(args, filter.Status)
			idx++
		}
		if filter.SearchTerm != "" {
			clauses = append(clauses, fmt.Sprintf("(name ILIKE $%d OR display_name ILIKE $%d)", idx, idx))
			args = append(args, "%"+filter.SearchTerm+"%")
			idx++
		}
	}

	query := `SELECT code, name, display_name, status, created_at, updated_at FROM tenants`
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	query += " ORDER BY code"

	if filter != nil && filter.Limit > 0 {
		query += fmt.Sprintf(" LIMIT $%d", idx)
		args = append(args, filter.Limit)
		idx++
	}
	if filter != nil && filter.Offset > 0 {
		query += fmt.Sprintf(" OFFSET $%d", idx)
		args = append(args, filter.Offset)
	}

	rows, err := m.db.Query(qctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list tenants: %w", err)
	}
	defer rows.Close()

	var tenants []*authplugin.Tenant
	for rows.Next() {
		var t authplugin.Tenant
		if err := rows.Scan(&t.ID, &t.Name, &t.DisplayName, &t.Status, &t.CreatedAt, &t.UpdatedAt); err != nil {
			continue
		}
		tenants = append(tenants, &t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list tenants rows: %w", err)
	}
	if tenants == nil {
		tenants = []*authplugin.Tenant{}
	}
	return tenants, nil
}

// CreateTenant creates a new tenant.
func (m *NativeTenantManager) CreateTenant(ctx context.Context, req *authplugin.CreateTenantRequest) (*authplugin.Tenant, error) {
	if req.ID == "" || req.Name == "" {
		return nil, fmt.Errorf("tenant ID and name are required")
	}
	if req.DisplayName == "" {
		req.DisplayName = req.Name
	}

	qctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	var t authplugin.Tenant
	err := m.db.QueryRow(qctx, `
		INSERT INTO tenants (code, name, display_name, status)
		VALUES ($1, $2, $3, 'active')
		RETURNING code, name, display_name, status, created_at, updated_at
	`, req.ID, req.Name, req.DisplayName).Scan(
		&t.ID, &t.Name, &t.DisplayName, &t.Status, &t.CreatedAt, &t.UpdatedAt,
	)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key") {
			return nil, fmt.Errorf("tenant already exists: %w", authplugin.ErrConflict)
		}
		return nil, fmt.Errorf("create tenant: %w", err)
	}
	return &t, nil
}

// UpdateTenant updates tenant metadata.
func (m *NativeTenantManager) UpdateTenant(ctx context.Context, tenantID string, req *authplugin.UpdateTenantRequest) (*authplugin.Tenant, error) {
	var sets []string
	var args []any
	idx := 1

	if req.Name != nil {
		sets = append(sets, fmt.Sprintf("name = $%d", idx))
		args = append(args, *req.Name)
		idx++
	}
	if req.DisplayName != nil {
		sets = append(sets, fmt.Sprintf("display_name = $%d", idx))
		args = append(args, *req.DisplayName)
		idx++
	}
	if len(sets) == 0 {
		return m.GetTenant(ctx, tenantID)
	}
	sets = append(sets, "updated_at = now()")
	args = append(args, tenantID)

	qctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	var t authplugin.Tenant
	err := m.db.QueryRow(qctx,
		`UPDATE tenants SET `+strings.Join(sets, ", ")+
			fmt.Sprintf(` WHERE code = $%d`, idx)+
			` RETURNING code, name, display_name, status, created_at, updated_at`,
		args...,
	).Scan(&t.ID, &t.Name, &t.DisplayName, &t.Status, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("update tenant %s: %w", tenantID, err)
	}
	return &t, nil
}

// DeleteTenant deletes a tenant.
func (m *NativeTenantManager) DeleteTenant(ctx context.Context, tenantID string) error {
	qctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	tag, err := m.db.Exec(qctx, `DELETE FROM tenants WHERE code = $1`, tenantID)
	if err != nil {
		return fmt.Errorf("delete tenant %s: %w", tenantID, err)
	}
	if tag.RowsAffected() == 0 {
		return authplugin.ErrNotFound
	}
	return nil
}

// EnableTenant enables a previously disabled tenant.
func (m *NativeTenantManager) EnableTenant(ctx context.Context, tenantID string) error {
	return m.setTenantStatus(ctx, tenantID, "active")
}

// DisableTenant disables a tenant.
func (m *NativeTenantManager) DisableTenant(ctx context.Context, tenantID string) error {
	return m.setTenantStatus(ctx, tenantID, "disabled")
}

func (m *NativeTenantManager) setTenantStatus(ctx context.Context, tenantID, status string) error {
	qctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	tag, err := m.db.Exec(qctx,
		`UPDATE tenants SET status = $1, updated_at = now() WHERE code = $2`,
		status, tenantID,
	)
	if err != nil {
		return fmt.Errorf("set tenant %s status %s: %w", tenantID, status, err)
	}
	if tag.RowsAffected() == 0 {
		return authplugin.ErrNotFound
	}
	return nil
}
