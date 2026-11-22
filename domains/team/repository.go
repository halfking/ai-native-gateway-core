package team

import (
	"context"
	"database/sql"
	"fmt"
)

type Repository interface {
	CreateTeam(ctx context.Context, tx *sql.Tx, team *Team) error
	GetTeamByID(ctx context.Context, tx *sql.Tx, teamID int64) (*Team, error)
	ListTeamsByOwner(ctx context.Context, tx *sql.Tx, ownerID int64) ([]Team, error)
	UpdateTeam(ctx context.Context, tx *sql.Tx, team *Team) error
	DeleteTeam(ctx context.Context, tx *sql.Tx, teamID int64) error
	
	AddUserToTeam(ctx context.Context, tx *sql.Tx, userTeam *UserTeam) error
	RemoveUserFromTeam(ctx context.Context, tx *sql.Tx, userID, teamID int64) error
	ListTeamMembers(ctx context.Context, tx *sql.Tx, teamID int64) ([]UserTeam, error)
	
	GetTeamBudget(ctx context.Context, tx *sql.Tx, teamID int64) (*TeamBudget, error)
	UpsertTeamBudget(ctx context.Context, tx *sql.Tx, budget *TeamBudget) error
}

type pgRepository struct {
	db *sql.DB
}

func NewPgRepository(db *sql.DB) Repository {
	return &pgRepository{db: db}
}

func (r *pgRepository) CreateTeam(ctx context.Context, tx *sql.Tx, team *Team) error {
	query := `INSERT INTO teams (name, display_name, owner_id, tenant_id, description, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, now(), now()) RETURNING id, created_at, updated_at`
	return tx.QueryRowContext(ctx, query, team.Name, team.DisplayName, team.OwnerID, team.TenantID, team.Description).
		Scan(&team.ID, &team.CreatedAt, &team.UpdatedAt)
}

func (r *pgRepository) GetTeamByID(ctx context.Context, tx *sql.Tx, teamID int64) (*Team, error) {
	query := `SELECT id, name, display_name, owner_id, tenant_id, description, created_at, updated_at FROM teams WHERE id = $1`
	team := &Team{}
	err := tx.QueryRowContext(ctx, query, teamID).Scan(&team.ID, &team.Name, &team.DisplayName, &team.OwnerID, 
		&team.TenantID, &team.Description, &team.CreatedAt, &team.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("team not found: %d", teamID)
	}
	return team, err
}

func (r *pgRepository) ListTeamsByOwner(ctx context.Context, tx *sql.Tx, ownerID int64) ([]Team, error) {
	query := `SELECT id, name, display_name, owner_id, tenant_id, description, created_at, updated_at 
		FROM teams WHERE owner_id = $1 ORDER BY created_at DESC`
	rows, err := tx.QueryContext(ctx, query, ownerID)
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
	return teams, rows.Err()
}

func (r *pgRepository) UpdateTeam(ctx context.Context, tx *sql.Tx, team *Team) error {
	query := `UPDATE teams SET display_name = $1, description = $2, updated_at = now() 
		WHERE id = $3 RETURNING updated_at`
	return tx.QueryRowContext(ctx, query, team.DisplayName, team.Description, team.ID).Scan(&team.UpdatedAt)
}

func (r *pgRepository) DeleteTeam(ctx context.Context, tx *sql.Tx, teamID int64) error {
	result, err := tx.ExecContext(ctx, `DELETE FROM teams WHERE id = $1`, teamID)
	if err != nil {
		return err
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return fmt.Errorf("team not found: %d", teamID)
	}
	return nil
}

func (r *pgRepository) AddUserToTeam(ctx context.Context, tx *sql.Tx, userTeam *UserTeam) error {
	query := `INSERT INTO user_teams (user_id, team_id, role, joined_at)
		VALUES ($1, $2, $3, now()) RETURNING id, joined_at`
	return tx.QueryRowContext(ctx, query, userTeam.UserID, userTeam.TeamID, userTeam.Role).
		Scan(&userTeam.ID, &userTeam.JoinedAt)
}

func (r *pgRepository) RemoveUserFromTeam(ctx context.Context, tx *sql.Tx, userID, teamID int64) error {
	result, err := tx.ExecContext(ctx, `DELETE FROM user_teams WHERE user_id = $1 AND team_id = $2`, userID, teamID)
	if err != nil {
		return err
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return fmt.Errorf("user_team not found")
	}
	return nil
}

func (r *pgRepository) ListTeamMembers(ctx context.Context, tx *sql.Tx, teamID int64) ([]UserTeam, error) {
	query := `SELECT id, user_id, team_id, role, joined_at FROM user_teams WHERE team_id = $1 ORDER BY joined_at`
	rows, err := tx.QueryContext(ctx, query, teamID)
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

func (r *pgRepository) GetTeamBudget(ctx context.Context, tx *sql.Tx, teamID int64) (*TeamBudget, error) {
	query := `SELECT id, team_id, monthly_limit_credits, enforcement_mode, created_at, updated_at 
		FROM team_budgets WHERE team_id = $1`
	budget := &TeamBudget{}
	err := tx.QueryRowContext(ctx, query, teamID).Scan(&budget.ID, &budget.TeamID, &budget.MonthlyLimitCredits,
		&budget.EnforcementMode, &budget.CreatedAt, &budget.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return budget, err
}

func (r *pgRepository) UpsertTeamBudget(ctx context.Context, tx *sql.Tx, budget *TeamBudget) error {
	query := `INSERT INTO team_budgets (team_id, monthly_limit_credits, enforcement_mode, created_at, updated_at)
		VALUES ($1, $2, $3, now(), now())
		ON CONFLICT (team_id) DO UPDATE SET monthly_limit_credits = EXCLUDED.monthly_limit_credits,
		enforcement_mode = EXCLUDED.enforcement_mode, updated_at = now()
		RETURNING id, created_at, updated_at`
	return tx.QueryRowContext(ctx, query, budget.TeamID, budget.MonthlyLimitCredits, budget.EnforcementMode).
		Scan(&budget.ID, &budget.CreatedAt, &budget.UpdatedAt)
}
