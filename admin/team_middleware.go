package admin

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/kaixuan/llm-gateway-go/domains/team"
)

// TeamMembershipCheck verifies if the authenticated user has the required role in a team
type TeamMembershipCheck struct {
	TeamID       int64
	RequiredRole team.UserTeamRole // minimum role required (member, admin, or owner)
}

// requireTeamMember is a middleware that verifies the user is a member of the specified team
// with at least the required role level. Role hierarchy: owner > admin > member
func (h *Handler) requireTeamMember(teamID int64, minRole team.UserTeamRole) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			auth := GetAuthContext(r)
			if auth == nil {
				writeError(w, http.StatusUnauthorized, "authentication required")
				return
			}

			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			defer cancel()

			repo := h.teamRepo()
			if repo == nil {
				writeError(w, http.StatusServiceUnavailable, "database not configured")
				return
			}

			tx, err := h.db.Begin(ctx)
			if err != nil {
				writeInternalErr(w, "failed to start transaction", err)
				return
			}
			defer tx.Rollback(ctx)

			// Check if user is team owner
			t, err := repo.GetTeamByID(ctx, tx, teamID)
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					writeError(w, http.StatusNotFound, "team not found")
					return
				}
				writeInternalErr(w, "failed to get team", err)
				return
			}

			// Owner has all permissions
			if t.OwnerID == int64(auth.UserID) {
				next(w, r)
				return
			}

			// Check user_teams membership
			var userRole string
			err = tx.QueryRow(ctx, `
				SELECT role FROM user_teams 
				WHERE user_id = $1 AND team_id = $2
			`, auth.UserID, teamID).Scan(&userRole)

			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					writeError(w, http.StatusForbidden, "not a team member")
					return
				}
				writeInternalErr(w, "failed to check team membership", err)
				return
			}

			// Verify role meets minimum requirement
			memberRole := team.UserTeamRole(userRole)
			if !hasRequiredRole(memberRole, minRole) {
				writeError(w, http.StatusForbidden, "insufficient permissions")
				return
			}

			next(w, r)
		}
	}
}

// hasRequiredRole checks if the user's role meets the minimum required role
// Role hierarchy: owner > admin > member
func hasRequiredRole(userRole, minRequired team.UserTeamRole) bool {
	roleLevel := map[team.UserTeamRole]int{
		team.RoleMember: 1,
		team.RoleAdmin:  2,
		team.RoleOwner:  3,
	}

	userLevel := roleLevel[userRole]
	requiredLevel := roleLevel[minRequired]

	return userLevel >= requiredLevel
}

// GetUserTeamRole returns the user's role in a team (including owner check)
func (h *Handler) GetUserTeamRole(ctx context.Context, tx pgx.Tx, userID int, teamID int64) (team.UserTeamRole, error) {
	repo := h.teamRepo()
	if repo == nil {
		return "", errors.New("database not configured")
	}

	// Check if user is team owner
	t, err := repo.GetTeamByID(ctx, tx, teamID)
	if err != nil {
		return "", err
	}

	if t.OwnerID == int64(userID) {
		return team.RoleOwner, nil
	}

	// Check user_teams membership
	var userRole string
	err = tx.QueryRow(ctx, `
		SELECT role FROM user_teams 
		WHERE user_id = $1 AND team_id = $2
	`, userID, teamID).Scan(&userRole)

	if err != nil {
		return "", err
	}

	return team.UserTeamRole(userRole), nil
}

// IsTeamMember checks if a user is a member of a team (any role)
func (h *Handler) IsTeamMember(ctx context.Context, userID int, teamID int64) (bool, error) {
	if h.db == nil {
		return false, errors.New("database not configured")
	}

	tx, err := h.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)

	repo := h.teamRepo()
	if repo == nil {
		return false, errors.New("repository not available")
	}

	// Check if user is team owner
	t, err := repo.GetTeamByID(ctx, tx, teamID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}

	if t.OwnerID == int64(userID) {
		return true, nil
	}

	// Check user_teams membership
	var exists bool
	err = tx.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM user_teams WHERE user_id = $1 AND team_id = $2)
	`, userID, teamID).Scan(&exists)

	return exists, err
}

// CanManageTeam checks if a user can manage a team (owner or admin)
func (h *Handler) CanManageTeam(ctx context.Context, userID int, teamID int64) (bool, error) {
	if h.db == nil {
		return false, errors.New("database not configured")
	}

	tx, err := h.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)

	role, err := h.GetUserTeamRole(ctx, tx, userID, teamID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}

	return role == team.RoleOwner || role == team.RoleAdmin, nil
}
