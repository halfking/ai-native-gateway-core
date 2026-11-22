// repository.go —— 团队仓储层（pgx v5）
//
// ★ 为什么是 pgx 而不是 database/sql（2026-11-23 12h 修订审计轮修正）：
// 本文件原先用 `database/sql`（*sql.DB / *sql.Tx / sql.ErrNoRows），
// 而调用方 `admin/team_handlers.go` 持有的 DB 句柄是
// `*pgxpool.Pool`（admin.Handler.db），事务是 `pgx.Tx`
// ⇒ 两边根本接不上，`go build ./...` 直接失败。
//
// ★ 判据不是"哪个库更先进"，而是**仓内既有事实**：
//   · admin 包所有 handler 一律用 `h.db.Query(ctx, ...)`（pgx 原生）；
//   · `maas/team_budget_gate.go` 同一功能族也是 `s.pool.QueryRow(ctx, ...)`；
//   · `admin/analytics.go:969-971` 明确记录「pgx v5 的 ErrNoRows 与
//     sql.ErrNoRows 不是同一值，裸 == 会漏判」。
// ⇒ 统一到 pgx v5，与 admin / maas 同族，不再引入第三种方言。
//
// ★ 事务契约：`tx pgx.Tx` 由调用方（admin handler）开启并负责 Commit/Rollback。
// 本层不自己开事务，也不吞错误。
package team

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repository 是团队仓储契约。
//
// ★ `tx pgx.Tx` 而非 `*sql.Tx`：与 admin/maas 同族（见文件头判据）。
type Repository interface {
	CreateTeam(ctx context.Context, tx pgx.Tx, team *Team) error
	GetTeamByID(ctx context.Context, tx pgx.Tx, teamID int64) (*Team, error)
	ListTeamsByOwner(ctx context.Context, tx pgx.Tx, ownerID int64) ([]Team, error)
	UpdateTeam(ctx context.Context, tx pgx.Tx, team *Team) error
	DeleteTeam(ctx context.Context, tx pgx.Tx, teamID int64) error

	AddUserToTeam(ctx context.Context, tx pgx.Tx, userTeam *UserTeam) error
	RemoveUserFromTeam(ctx context.Context, tx pgx.Tx, userID, teamID int64) error
	ListTeamMembers(ctx context.Context, tx pgx.Tx, teamID int64) ([]UserTeam, error)

	GetTeamBudget(ctx context.Context, tx pgx.Tx, teamID int64) (*TeamBudget, error)
	UpsertTeamBudget(ctx context.Context, tx pgx.Tx, budget *TeamBudget) error
}

type pgRepository struct {
	db *pgxpool.Pool
}

// NewPgRepository 构造 pgx 仓储。
//
// ★ db 句柄只用于**无事务**路径（当前无）；事务路径一律由调用方传入 tx。
// 保留它是为了让「仓储是否可用」这件事有一个明确的事实来源（admin handler
// 用 `h.db == nil` 判「数据库未配置」），而不是在仓储里再判一次。
func NewPgRepository(db *pgxpool.Pool) Repository {
	return &pgRepository{db: db}
}

func (r *pgRepository) CreateTeam(ctx context.Context, tx pgx.Tx, team *Team) error {
	query := `INSERT INTO teams (name, display_name, owner_id, tenant_id, description, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, now(), now()) RETURNING id, created_at, updated_at`
	return tx.QueryRow(ctx, query, team.Name, team.DisplayName, team.OwnerID, team.TenantID, team.Description).
		Scan(&team.ID, &team.CreatedAt, &team.UpdatedAt)
}

func (r *pgRepository) GetTeamByID(ctx context.Context, tx pgx.Tx, teamID int64) (*Team, error) {
	query := `SELECT id, name, display_name, owner_id, tenant_id, description, created_at, updated_at FROM teams WHERE id = $1`
	team := &Team{}
	err := tx.QueryRow(ctx, query, teamID).Scan(&team.ID, &team.Name, &team.DisplayName, &team.OwnerID,
		&team.TenantID, &team.Description, &team.CreatedAt, &team.UpdatedAt)
	// ★ pgx v5 的「无行」是 pgx.ErrNoRows（与 sql.ErrNoRows 不是同一个值）。
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("team not found: %d", teamID)
	}
	return team, err
}

func (r *pgRepository) ListTeamsByOwner(ctx context.Context, tx pgx.Tx, ownerID int64) ([]Team, error) {
	query := `SELECT id, name, display_name, owner_id, tenant_id, description, created_at, updated_at
		FROM teams WHERE owner_id = $1 ORDER BY created_at DESC`
	rows, err := tx.Query(ctx, query, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var teams []Team
	for rows.Next() {
		var team Team
		if err := rows.Scan(&team.ID, &team.Name, &team.DisplayName, &team.OwnerID, &team.TenantID,
			&team.Description, &team.CreatedAt, &team.UpdatedAt); err != nil {
			return nil, err
		}
		teams = append(teams, team)
	}
	// ★ pgx.Rows 的迭代错误必须在这里取（Err()），否则最后一批行丢失且不报错。
	return teams, rows.Err()
}

func (r *pgRepository) UpdateTeam(ctx context.Context, tx pgx.Tx, team *Team) error {
	query := `UPDATE teams SET display_name = $1, description = $2, updated_at = now()
		WHERE id = $3 RETURNING updated_at`
	return tx.QueryRow(ctx, query, team.DisplayName, team.Description, team.ID).Scan(&team.UpdatedAt)
}

func (r *pgRepository) DeleteTeam(ctx context.Context, tx pgx.Tx, teamID int64) error {
	result, err := tx.Exec(ctx, `DELETE FROM teams WHERE id = $1`, teamID)
	if err != nil {
		return err
	}
	if rows := result.RowsAffected(); rows == 0 {
		return fmt.Errorf("team not found: %d", teamID)
	}
	return nil
}

func (r *pgRepository) AddUserToTeam(ctx context.Context, tx pgx.Tx, userTeam *UserTeam) error {
	query := `INSERT INTO user_teams (user_id, team_id, role, joined_at)
		VALUES ($1, $2, $3, now()) RETURNING id, joined_at`
	return tx.QueryRow(ctx, query, userTeam.UserID, userTeam.TeamID, userTeam.Role).
		Scan(&userTeam.ID, &userTeam.JoinedAt)
}

func (r *pgRepository) RemoveUserFromTeam(ctx context.Context, tx pgx.Tx, userID, teamID int64) error {
	result, err := tx.Exec(ctx, `DELETE FROM user_teams WHERE user_id = $1 AND team_id = $2`, userID, teamID)
	if err != nil {
		return err
	}
	if rows := result.RowsAffected(); rows == 0 {
		return fmt.Errorf("user_team not found")
	}
	return nil
}

func (r *pgRepository) ListTeamMembers(ctx context.Context, tx pgx.Tx, teamID int64) ([]UserTeam, error) {
	query := `SELECT id, user_id, team_id, role, joined_at FROM user_teams WHERE team_id = $1 ORDER BY joined_at`
	rows, err := tx.Query(ctx, query, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var members []UserTeam
	for rows.Next() {
		var m UserTeam
		if err := rows.Scan(&m.ID, &m.UserID, &m.TeamID, &m.Role, &m.JoinedAt); err != nil {
			return nil, err
		}
		members = append(members, m)
	}
	return members, rows.Err()
}

func (r *pgRepository) GetTeamBudget(ctx context.Context, tx pgx.Tx, teamID int64) (*TeamBudget, error) {
	query := `SELECT id, team_id, monthly_limit_credits, enforcement_mode, created_at, updated_at
		FROM team_budgets WHERE team_id = $1`
	budget := &TeamBudget{}
	err := tx.QueryRow(ctx, query, teamID).Scan(&budget.ID, &budget.TeamID, &budget.MonthlyLimitCredits,
		&budget.EnforcementMode, &budget.CreatedAt, &budget.UpdatedAt)
	// ★ 未配置预算**不是错误**：返回 (nil, nil)，让调用方渲染「未配置预算」。
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return budget, err
}

func (r *pgRepository) UpsertTeamBudget(ctx context.Context, tx pgx.Tx, budget *TeamBudget) error {
	query := `INSERT INTO team_budgets (team_id, monthly_limit_credits, enforcement_mode, created_at, updated_at)
		VALUES ($1, $2, $3, now(), now())
		ON CONFLICT (team_id) DO UPDATE SET monthly_limit_credits = EXCLUDED.monthly_limit_credits,
		enforcement_mode = EXCLUDED.enforcement_mode, updated_at = now()
		RETURNING id, created_at, updated_at`
	return tx.QueryRow(ctx, query, budget.TeamID, budget.MonthlyLimitCredits, budget.EnforcementMode).
		Scan(&budget.ID, &budget.CreatedAt, &budget.UpdatedAt)
}
