package native

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"github.com/kaixuan/llm-gateway-go/admin"
	"github.com/kaixuan/llm-gateway-go/pkg/authplugin"
)

// NativeUserManager implements authplugin.UserManager using the existing
// PostgreSQL users table, preserving all original admin/users.go logic.
type NativeUserManager struct {
	db *pgxpool.Pool
}

// NewNativeUserManager creates a NativeUserManager.
func NewNativeUserManager(db *pgxpool.Pool) (*NativeUserManager, error) {
	if db == nil {
		return nil, errors.New("native user manager: db must not be nil")
	}
	return &NativeUserManager{db: db}, nil
}

// Name returns the manager identifier.
func (m *NativeUserManager) Name() string { return "native" }

// SupportsFeature reports which optional UserFeature flags this manager handles.
func (m *NativeUserManager) SupportsFeature(f authplugin.UserFeature) bool {
	switch f {
	case authplugin.FeatureUserSearch, authplugin.FeatureUserGroups:
		return true
	default:
		return false
	}
}

// GetUser retrieves a user by internal ID.
func (m *NativeUserManager) GetUser(ctx context.Context, userID int) (*authplugin.User, error) {
	qctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	var u authplugin.User
	err := m.db.QueryRow(qctx, `
		SELECT id, tenant_id, username, display_name, email, role, enabled, must_change_password, last_login_at, created_at
		FROM users WHERE id = $1
	`, userID).Scan(
		&u.ID, &u.TenantID, &u.Username, &u.DisplayName, &u.Email,
		&u.Role, &u.Enabled, &u.MustChangePassword, &u.LastLoginAt, &u.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("get user %d: %w", userID, authplugin.ErrNotFound)
	}
	u.ExternalID = fmt.Sprintf("native:%d", u.ID)
	return &u, nil
}

// GetUserByUsername retrieves a user by username.
func (m *NativeUserManager) GetUserByUsername(ctx context.Context, username string) (*authplugin.User, error) {
	qctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	var u authplugin.User
	err := m.db.QueryRow(qctx, `
		SELECT id, tenant_id, username, display_name, email, role, enabled, must_change_password, last_login_at, created_at
		FROM users WHERE username = $1
	`, username).Scan(
		&u.ID, &u.TenantID, &u.Username, &u.DisplayName, &u.Email,
		&u.Role, &u.Enabled, &u.MustChangePassword, &u.LastLoginAt, &u.CreatedAt,
	)
	if err != nil {
		return nil, authplugin.ErrNotFound
	}
	u.ExternalID = fmt.Sprintf("native:%d", u.ID)
	return &u, nil
}

// GetUserByExternalID retrieves a user by external ID (format: "native:<id>").
func (m *NativeUserManager) GetUserByExternalID(ctx context.Context, externalID string) (*authplugin.User, error) {
	// Native external IDs have the form "native:<numeric-id>"
	id := 0
	if _, err := fmt.Sscanf(externalID, "native:%d", &id); err != nil || id == 0 {
		return nil, authplugin.ErrNotFound
	}
	return m.GetUser(ctx, id)
}

// ListUsers returns users with optional filtering.
func (m *NativeUserManager) ListUsers(ctx context.Context, filter *authplugin.UserFilter) ([]*authplugin.User, error) {
	qctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	var clauses []string
	var args []any
	idx := 1

	if filter != nil {
		if filter.TenantID != "" {
			clauses = append(clauses, fmt.Sprintf("tenant_id = $%d", idx))
			args = append(args, filter.TenantID)
			idx++
		}
		if filter.Role != "" {
			clauses = append(clauses, fmt.Sprintf("role = $%d", idx))
			args = append(args, filter.Role)
			idx++
		}
		if filter.Enabled != nil {
			clauses = append(clauses, fmt.Sprintf("enabled = $%d", idx))
			args = append(args, *filter.Enabled)
			idx++
		}
		if filter.SearchTerm != "" {
			clauses = append(clauses, fmt.Sprintf(
				"(username ILIKE $%d OR display_name ILIKE $%d OR email ILIKE $%d)",
				idx, idx, idx,
			))
			args = append(args, "%"+filter.SearchTerm+"%")
			idx++
		}
	}

	query := `SELECT id, tenant_id, username, display_name, email, role, enabled, must_change_password, last_login_at, created_at FROM users`
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	query += " ORDER BY id"

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
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()

	var users []*authplugin.User
	for rows.Next() {
		var u authplugin.User
		if err := rows.Scan(
			&u.ID, &u.TenantID, &u.Username, &u.DisplayName, &u.Email,
			&u.Role, &u.Enabled, &u.MustChangePassword, &u.LastLoginAt, &u.CreatedAt,
		); err != nil {
			continue
		}
		u.ExternalID = fmt.Sprintf("native:%d", u.ID)
		users = append(users, &u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list users rows: %w", err)
	}
	if users == nil {
		users = []*authplugin.User{}
	}
	return users, nil
}

// CreateUser creates a new user. Preserves admin/users.go createUser logic.
func (m *NativeUserManager) CreateUser(ctx context.Context, req *authplugin.CreateUserRequest) (*authplugin.User, error) {
	if req.Username == "" || req.Password == "" {
		return nil, fmt.Errorf("username and password are required")
	}
	// F-A1（2026-11-14 24h 审计轮）：复杂度策略与 admin/users.go:372 同源
	// （ValidatePasswordComplexity：≥8 位+大小写+数字）——首版只查长度，
	// 「保留所有原有逻辑」的声明与事实不符，弱化密码策略。
	if err := admin.ValidatePasswordComplexity(req.Password); err != nil {
		return nil, err
	}
	if req.TenantID == "" {
		req.TenantID = "default"
	}
	if req.Role == "" {
		req.Role = "tenant_admin"
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}

	qctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	var u authplugin.User
	err = m.db.QueryRow(qctx, `
		INSERT INTO users (tenant_id, username, password_hash, display_name, email, role, enabled, must_change_password)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id, tenant_id, username, display_name, email, role, enabled, must_change_password, created_at
	`, req.TenantID, req.Username, string(hash), req.DisplayName, req.Email,
		req.Role, req.Enabled, req.MustChangePassword,
	).Scan(
		&u.ID, &u.TenantID, &u.Username, &u.DisplayName, &u.Email,
		&u.Role, &u.Enabled, &u.MustChangePassword, &u.CreatedAt,
	)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key") {
			return nil, fmt.Errorf("username already exists: %w", authplugin.ErrConflict)
		}
		return nil, fmt.Errorf("create user: %w", err)
	}
	u.ExternalID = fmt.Sprintf("native:%d", u.ID)
	return &u, nil
}

// UpdateUser updates an existing user's attributes.
func (m *NativeUserManager) UpdateUser(ctx context.Context, userID int, req *authplugin.UpdateUserRequest) (*authplugin.User, error) {
	var sets []string
	var args []any
	idx := 1

	if req.DisplayName != nil {
		sets = append(sets, fmt.Sprintf("display_name = $%d", idx))
		args = append(args, *req.DisplayName)
		idx++
	}
	if req.Email != nil {
		sets = append(sets, fmt.Sprintf("email = $%d", idx))
		args = append(args, *req.Email)
		idx++
	}
	if req.Role != nil {
		sets = append(sets, fmt.Sprintf("role = $%d", idx))
		args = append(args, *req.Role)
		idx++
	}
	if req.Enabled != nil {
		sets = append(sets, fmt.Sprintf("enabled = $%d", idx))
		args = append(args, *req.Enabled)
		idx++
	}
	if req.MustChangePassword != nil {
		sets = append(sets, fmt.Sprintf("must_change_password = $%d", idx))
		args = append(args, *req.MustChangePassword)
		idx++
	}
	if req.Password != nil && *req.Password != "" {
		hash, err := bcrypt.GenerateFromPassword([]byte(*req.Password), bcrypt.DefaultCost)
		if err != nil {
			return nil, fmt.Errorf("hash password: %w", err)
		}
		sets = append(sets, fmt.Sprintf("password_hash = $%d", idx))
		args = append(args, string(hash))
		idx++
		sets = append(sets, fmt.Sprintf("password_changed_at = $%d", idx))
		args = append(args, time.Now())
		idx++
	}
	if len(sets) == 0 {
		return m.GetUser(ctx, userID)
	}
	sets = append(sets, "updated_at = now()")
	args = append(args, userID)

	qctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	var u authplugin.User
	err := m.db.QueryRow(qctx,
		`UPDATE users SET `+strings.Join(sets, ", ")+
			fmt.Sprintf(` WHERE id = $%d`, idx)+
			` RETURNING id, tenant_id, username, display_name, email, role, enabled, must_change_password, last_login_at, created_at`,
		args...,
	).Scan(
		&u.ID, &u.TenantID, &u.Username, &u.DisplayName, &u.Email,
		&u.Role, &u.Enabled, &u.MustChangePassword, &u.LastLoginAt, &u.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("update user %d: %w", userID, err)
	}
	u.ExternalID = fmt.Sprintf("native:%d", u.ID)
	return &u, nil
}

// DeleteUser hard-deletes a user.
func (m *NativeUserManager) DeleteUser(ctx context.Context, userID int) error {
	qctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	tag, err := m.db.Exec(qctx, `DELETE FROM users WHERE id = $1`, userID)
	if err != nil {
		return fmt.Errorf("delete user %d: %w", userID, err)
	}
	if tag.RowsAffected() == 0 {
		return authplugin.ErrNotFound
	}
	return nil
}

// ResetPassword resets a user's password (admin action, no old password check).
func (m *NativeUserManager) ResetPassword(ctx context.Context, userID int, newPassword string) error {
	// F-A1：复杂度策略与 admin/users.go:606 同源；R56 承诺=凭据泄露响应
	// （admin 强制重置）必须吊销目标用户既有 token——与 B4 自助换密同 trade：
	// 吊销失败不阻塞重置本身，WARN 留痕。
	if err := admin.ValidatePasswordComplexity(newPassword); err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	qctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	tag, err := m.db.Exec(qctx,
		`UPDATE users SET password_hash = $1, must_change_password = TRUE, password_changed_at = now(), updated_at = now() WHERE id = $2`,
		string(hash), userID,
	)
	if err != nil {
		return fmt.Errorf("reset password user %d: %w", userID, err)
	}
	if tag.RowsAffected() == 0 {
		return authplugin.ErrNotFound
	}
	if revokeErr := admin.RevokeUserTokensForPasswordChange(ctx, m.db, userID); revokeErr != nil {
		slog.Warn("native reset password succeeded but token revocation failed",
			"user_id", userID,
			"error", revokeErr,
		)
	}
	return nil
}

// ChangePassword changes a user's own password (requires old password).
func (m *NativeUserManager) ChangePassword(ctx context.Context, userID int, oldPassword, newPassword string) error {
	// F-A1：复杂度策略与 admin/users.go:706 同源；B4 承诺=自助换密必须
	// 吊销换密前签发的全部 token（旧 JWT 到期前仍然密码学有效，吊销 epoch
	// 是 auth 中间件拒绝它们的方式）——吊销失败不阻塞换密，WARN 留痕。
	if err := admin.ValidatePasswordComplexity(newPassword); err != nil {
		return err
	}

	qctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	var currentHash string
	if err := m.db.QueryRow(qctx, `SELECT password_hash FROM users WHERE id = $1`, userID).Scan(&currentHash); err != nil {
		return authplugin.ErrNotFound
	}
	if bcrypt.CompareHashAndPassword([]byte(currentHash), []byte(oldPassword)) != nil {
		return authplugin.ErrInvalidCredentials
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	_, err = m.db.Exec(qctx,
		`UPDATE users SET password_hash = $1, must_change_password = FALSE, password_changed_at = now(), updated_at = now() WHERE id = $2`,
		string(hash), userID,
	)
	if err != nil {
		return err
	}
	if revokeErr := admin.RevokeUserTokensForPasswordChange(ctx, m.db, userID); revokeErr != nil {
		slog.Warn("native change password succeeded but token revocation failed",
			"user_id", userID,
			"error", revokeErr,
		)
	}
	return nil
}

// UpdateLastLogin updates the last_login_at timestamp.
func (m *NativeUserManager) UpdateLastLogin(ctx context.Context, userID int) error {
	qctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	//nolint:errcheck
	m.db.Exec(qctx, `UPDATE users SET last_login_at = now() WHERE id = $1`, userID)
	return nil
}
