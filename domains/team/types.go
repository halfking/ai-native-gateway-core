package team

import "time"

type Team struct {
	ID          int64     `json:"id" db:"id"`
	Name        string    `json:"name" db:"name"`
	DisplayName *string   `json:"display_name" db:"display_name"`
	OwnerID     int64     `json:"owner_id" db:"owner_id"`
	TenantID    string    `json:"tenant_id" db:"tenant_id"`
	Description *string   `json:"description" db:"description"`
	CreatedAt   time.Time `json:"created_at" db:"created_at"`
	UpdatedAt   time.Time `json:"updated_at" db:"updated_at"`
}

type UserTeamRole string

const (
	RoleOwner  UserTeamRole = "owner"
	RoleAdmin  UserTeamRole = "admin"
	RoleMember UserTeamRole = "member"
)

type UserTeam struct {
	ID       int64        `json:"id" db:"id"`
	UserID   int64        `json:"user_id" db:"user_id"`
	TeamID   int64        `json:"team_id" db:"team_id"`
	Role     UserTeamRole `json:"role" db:"role"`
	JoinedAt time.Time    `json:"joined_at" db:"joined_at"`
}

type EnforcementMode string

const (
	EnforcementWarn EnforcementMode = "warn"
	EnforcementHard EnforcementMode = "hard"
)

type TeamBudget struct {
	ID                  int64           `json:"id" db:"id"`
	TeamID              int64           `json:"team_id" db:"team_id"`
	MonthlyLimitCredits float64         `json:"monthly_limit_credits" db:"monthly_limit_credits"`
	EnforcementMode     EnforcementMode `json:"enforcement_mode" db:"enforcement_mode"`
	CreatedAt           time.Time       `json:"created_at" db:"created_at"`
	UpdatedAt           time.Time       `json:"updated_at" db:"updated_at"`
}
