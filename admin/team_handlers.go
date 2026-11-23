package admin

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/kaixuan/llm-gateway-go/domains/team"
)

// registerTeamRoutes registers team management endpoints (W2-Q2)
func (h *Handler) registerTeamRoutes(mux *http.ServeMux) {
	// Team CRUD
	mux.HandleFunc("/api/admin/teams", h.admin(h.handleTeams))
	mux.HandleFunc("/api/admin/teams/", h.admin(h.handleTeamByID))
	
	// Member management
	mux.HandleFunc("/api/admin/teams/members/add", h.admin(h.handleAddTeamMember))
	mux.HandleFunc("/api/admin/teams/members/remove", h.admin(h.handleRemoveTeamMember))
	
	// Budget management
	mux.HandleFunc("/api/admin/teams/budget", h.admin(h.handleTeamBudget))
	mux.HandleFunc("/api/admin/teams/budget/stats", h.admin(h.handleTeamBudgetStats))
}

func (h *Handler) teamRepo() team.Repository {
	if h.db == nil {
		return nil
	}
	return team.NewPgRepository(h.db)
}

// handleTeams handles POST (create) and GET (list) for teams
func (h *Handler) handleTeams(w http.ResponseWriter, r *http.Request) {
	repo := h.teamRepo()
	if repo == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}

	auth := GetAuthContext(r)
	if auth == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	switch r.Method {
	case http.MethodPost:
		h.handleCreateTeam(w, r, repo, auth)
	case http.MethodGet:
		h.handleListTeams(w, r, repo, auth)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

type createTeamRequest struct {
	Name        string  `json:"name"`
	DisplayName *string `json:"display_name"`
	Description *string `json:"description"`
}

type teamResponse struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	DisplayName *string   `json:"display_name"`
	OwnerID     int64     `json:"owner_id"`
	TenantID    string    `json:"tenant_id"`
	Description *string   `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (h *Handler) handleCreateTeam(w http.ResponseWriter, r *http.Request, repo team.Repository, auth *AuthContext) {
	var req createTeamRequest
	if err := readJSONRequired(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}

	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	tx, err := h.db.Begin(ctx)
	if err != nil {
		writeInternalErr(w, "failed to start transaction", err)
		return
	}
	defer tx.Rollback(ctx)

	newTeam := &team.Team{
		Name:        req.Name,
		DisplayName: req.DisplayName,
		OwnerID:     int64(auth.UserID),
		TenantID:    auth.TenantID,
		Description: req.Description,
	}

	if err := repo.CreateTeam(ctx, tx, newTeam); err != nil {
		writeInternalErr(w, "failed to create team", err)
		return
	}

	if err := tx.Commit(ctx); err != nil {
		writeInternalErr(w, "failed to commit transaction", err)
		return
	}

	writeJSON(w, http.StatusCreated, teamResponse{
		ID:          newTeam.ID,
		Name:        newTeam.Name,
		DisplayName: newTeam.DisplayName,
		OwnerID:     newTeam.OwnerID,
		TenantID:    newTeam.TenantID,
		Description: newTeam.Description,
		CreatedAt:   newTeam.CreatedAt,
		UpdatedAt:   newTeam.UpdatedAt,
	})
}

func (h *Handler) handleListTeams(w http.ResponseWriter, r *http.Request, repo team.Repository, auth *AuthContext) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	tx, err := h.db.Begin(ctx)
	if err != nil {
		writeInternalErr(w, "failed to start transaction", err)
		return
	}
	defer tx.Rollback(ctx)

	teams, err := repo.ListTeamsByOwner(ctx, tx, int64(auth.UserID))
	if err != nil {
		writeInternalErr(w, "failed to list teams", err)
		return
	}

	response := make([]teamResponse, len(teams))
	for i, t := range teams {
		response[i] = teamResponse{
			ID:          t.ID,
			Name:        t.Name,
			DisplayName: t.DisplayName,
			OwnerID:     t.OwnerID,
			TenantID:    t.TenantID,
			Description: t.Description,
			CreatedAt:   t.CreatedAt,
			UpdatedAt:   t.UpdatedAt,
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"teams": response})
}

// handleTeamByID handles GET (retrieve), PUT (update), and DELETE operations on a specific team
func (h *Handler) handleTeamByID(w http.ResponseWriter, r *http.Request) {
	repo := h.teamRepo()
	if repo == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}

	auth := GetAuthContext(r)
	if auth == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	// Parse team ID from URL path: /api/admin/teams/{id}
	path := strings.TrimPrefix(r.URL.Path, "/api/admin/teams/")
	teamID, err := strconv.ParseInt(path, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid team id")
		return
	}

	switch r.Method {
	case http.MethodGet:
		h.handleGetTeam(w, r, repo, auth, teamID)
	case http.MethodPut:
		h.handleUpdateTeam(w, r, repo, auth, teamID)
	case http.MethodDelete:
		h.handleDeleteTeam(w, r, repo, auth, teamID)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) handleGetTeam(w http.ResponseWriter, r *http.Request, repo team.Repository, auth *AuthContext, teamID int64) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	tx, err := h.db.Begin(ctx)
	if err != nil {
		writeInternalErr(w, "failed to start transaction", err)
		return
	}
	defer tx.Rollback(ctx)

	t, err := repo.GetTeamByID(ctx, tx, teamID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) || strings.Contains(err.Error(), "not found") {
			writeError(w, http.StatusNotFound, "team not found")
			return
		}
		writeInternalErr(w, "failed to get team", err)
		return
	}

	// Authorization: only team owner can access
	if t.OwnerID != int64(auth.UserID) {
		writeError(w, http.StatusForbidden, "access denied")
		return
	}

	writeJSON(w, http.StatusOK, teamResponse{
		ID:          t.ID,
		Name:        t.Name,
		DisplayName: t.DisplayName,
		OwnerID:     t.OwnerID,
		TenantID:    t.TenantID,
		Description: t.Description,
		CreatedAt:   t.CreatedAt,
		UpdatedAt:   t.UpdatedAt,
	})
}

type updateTeamRequest struct {
	DisplayName *string `json:"display_name"`
	Description *string `json:"description"`
}

func (h *Handler) handleUpdateTeam(w http.ResponseWriter, r *http.Request, repo team.Repository, auth *AuthContext, teamID int64) {
	var req updateTeamRequest
	if err := readJSONRequired(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	tx, err := h.db.Begin(ctx)
	if err != nil {
		writeInternalErr(w, "failed to start transaction", err)
		return
	}
	defer tx.Rollback(ctx)

	// Verify team exists and user is owner
	t, err := repo.GetTeamByID(ctx, tx, teamID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) || strings.Contains(err.Error(), "not found") {
			writeError(w, http.StatusNotFound, "team not found")
			return
		}
		writeInternalErr(w, "failed to get team", err)
		return
	}

	if t.OwnerID != int64(auth.UserID) {
		writeError(w, http.StatusForbidden, "access denied")
		return
	}

	// Update fields
	t.DisplayName = req.DisplayName
	t.Description = req.Description

	if err := repo.UpdateTeam(ctx, tx, t); err != nil {
		writeInternalErr(w, "failed to update team", err)
		return
	}

	if err := tx.Commit(ctx); err != nil {
		writeInternalErr(w, "failed to commit transaction", err)
		return
	}

	writeJSON(w, http.StatusOK, teamResponse{
		ID:          t.ID,
		Name:        t.Name,
		DisplayName: t.DisplayName,
		OwnerID:     t.OwnerID,
		TenantID:    t.TenantID,
		Description: t.Description,
		CreatedAt:   t.CreatedAt,
		UpdatedAt:   t.UpdatedAt,
	})
}

func (h *Handler) handleDeleteTeam(w http.ResponseWriter, r *http.Request, repo team.Repository, auth *AuthContext, teamID int64) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	tx, err := h.db.Begin(ctx)
	if err != nil {
		writeInternalErr(w, "failed to start transaction", err)
		return
	}
	defer tx.Rollback(ctx)

	// Verify team exists and user is owner
	t, err := repo.GetTeamByID(ctx, tx, teamID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) || strings.Contains(err.Error(), "not found") {
			writeError(w, http.StatusNotFound, "team not found")
			return
		}
		writeInternalErr(w, "failed to get team", err)
		return
	}

	if t.OwnerID != int64(auth.UserID) {
		writeError(w, http.StatusForbidden, "access denied")
		return
	}

	if err := repo.DeleteTeam(ctx, tx, teamID); err != nil {
		writeInternalErr(w, "failed to delete team", err)
		return
	}

	if err := tx.Commit(ctx); err != nil {
		writeInternalErr(w, "failed to commit transaction", err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// Member management

type addMemberRequest struct {
	TeamID int64  `json:"team_id"`
	UserID int64  `json:"user_id"`
	Role   string `json:"role"`
}

func (h *Handler) handleAddTeamMember(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	repo := h.teamRepo()
	if repo == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}

	auth := GetAuthContext(r)
	if auth == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	var req addMemberRequest
	if err := readJSONRequired(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}

	if req.TeamID <= 0 || req.UserID <= 0 {
		writeError(w, http.StatusBadRequest, "team_id and user_id are required")
		return
	}

	role := team.UserTeamRole(req.Role)
	if role != team.RoleOwner && role != team.RoleAdmin && role != team.RoleMember {
		writeError(w, http.StatusBadRequest, "invalid role, must be owner, admin, or member")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	tx, err := h.db.Begin(ctx)
	if err != nil {
		writeInternalErr(w, "failed to start transaction", err)
		return
	}
	defer tx.Rollback(ctx)

	// Verify team exists and user is owner
	t, err := repo.GetTeamByID(ctx, tx, req.TeamID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) || strings.Contains(err.Error(), "not found") {
			writeError(w, http.StatusNotFound, "team not found")
			return
		}
		writeInternalErr(w, "failed to get team", err)
		return
	}

	if t.OwnerID != int64(auth.UserID) {
		writeError(w, http.StatusForbidden, "only team owner can add members")
		return
	}

	userTeam := &team.UserTeam{
		UserID: req.UserID,
		TeamID: req.TeamID,
		Role:   role,
	}

	if err := repo.AddUserToTeam(ctx, tx, userTeam); err != nil {
		if pgErr, ok := err.(*pgconn.PgError); ok && pgErr.Code == "23505" { // unique_violation
			writeError(w, http.StatusConflict, "user already in team")
			return
		}
		writeInternalErr(w, "failed to add member", err)
		return
	}

	if err := tx.Commit(ctx); err != nil {
		writeInternalErr(w, "failed to commit transaction", err)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"id":        userTeam.ID,
		"user_id":   userTeam.UserID,
		"team_id":   userTeam.TeamID,
		"role":      userTeam.Role,
		"joined_at": userTeam.JoinedAt,
	})
}

type removeMemberRequest struct {
	TeamID int64 `json:"team_id"`
	UserID int64 `json:"user_id"`
}

func (h *Handler) handleRemoveTeamMember(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	repo := h.teamRepo()
	if repo == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}

	auth := GetAuthContext(r)
	if auth == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	var req removeMemberRequest
	if err := readJSONRequired(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}

	if req.TeamID <= 0 || req.UserID <= 0 {
		writeError(w, http.StatusBadRequest, "team_id and user_id are required")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	tx, err := h.db.Begin(ctx)
	if err != nil {
		writeInternalErr(w, "failed to start transaction", err)
		return
	}
	defer tx.Rollback(ctx)

	// Verify team exists and user is owner
	t, err := repo.GetTeamByID(ctx, tx, req.TeamID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) || strings.Contains(err.Error(), "not found") {
			writeError(w, http.StatusNotFound, "team not found")
			return
		}
		writeInternalErr(w, "failed to get team", err)
		return
	}

	if t.OwnerID != int64(auth.UserID) {
		writeError(w, http.StatusForbidden, "only team owner can remove members")
		return
	}

	if err := repo.RemoveUserFromTeam(ctx, tx, req.UserID, req.TeamID); err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeError(w, http.StatusNotFound, "member not found")
			return
		}
		writeInternalErr(w, "failed to remove member", err)
		return
	}

	if err := tx.Commit(ctx); err != nil {
		writeInternalErr(w, "failed to commit transaction", err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// Budget management

type teamBudgetRequest struct {
	TeamID              int64   `json:"team_id"`
	MonthlyLimitCredits float64 `json:"monthly_limit_credits"`
	EnforcementMode     string  `json:"enforcement_mode"`
}

func (h *Handler) handleTeamBudget(w http.ResponseWriter, r *http.Request) {
	repo := h.teamRepo()
	if repo == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}

	auth := GetAuthContext(r)
	if auth == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	switch r.Method {
	case http.MethodGet:
		h.handleGetTeamBudget(w, r, repo, auth)
	case http.MethodPut, http.MethodPost:
		h.handleUpsertTeamBudget(w, r, repo, auth)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) handleGetTeamBudget(w http.ResponseWriter, r *http.Request, repo team.Repository, auth *AuthContext) {
	teamIDStr := r.URL.Query().Get("team_id")
	if teamIDStr == "" {
		writeError(w, http.StatusBadRequest, "team_id query parameter required")
		return
	}

	teamID, err := strconv.ParseInt(teamIDStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid team_id")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	tx, err := h.db.Begin(ctx)
	if err != nil {
		writeInternalErr(w, "failed to start transaction", err)
		return
	}
	defer tx.Rollback(ctx)

	// Verify team exists and user is owner
	t, err := repo.GetTeamByID(ctx, tx, teamID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) || strings.Contains(err.Error(), "not found") {
			writeError(w, http.StatusNotFound, "team not found")
			return
		}
		writeInternalErr(w, "failed to get team", err)
		return
	}

	if t.OwnerID != int64(auth.UserID) {
		writeError(w, http.StatusForbidden, "access denied")
		return
	}

	budget, err := repo.GetTeamBudget(ctx, tx, teamID)
	if err != nil {
		writeInternalErr(w, "failed to get budget", err)
		return
	}

	if budget == nil {
		writeJSON(w, http.StatusOK, map[string]any{"budget": nil})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"id":                    budget.ID,
		"team_id":               budget.TeamID,
		"monthly_limit_credits": budget.MonthlyLimitCredits,
		"enforcement_mode":      budget.EnforcementMode,
		"created_at":            budget.CreatedAt,
		"updated_at":            budget.UpdatedAt,
	})
}

func (h *Handler) handleUpsertTeamBudget(w http.ResponseWriter, r *http.Request, repo team.Repository, auth *AuthContext) {
	var req teamBudgetRequest
	if err := readJSONRequired(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}

	if req.TeamID <= 0 {
		writeError(w, http.StatusBadRequest, "team_id is required")
		return
	}

	if req.MonthlyLimitCredits < 0 {
		writeError(w, http.StatusBadRequest, "monthly_limit_credits must be non-negative")
		return
	}

	mode := team.EnforcementMode(req.EnforcementMode)
	if mode != team.EnforcementWarn && mode != team.EnforcementHard {
		writeError(w, http.StatusBadRequest, "enforcement_mode must be 'warn' or 'hard'")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	tx, err := h.db.Begin(ctx)
	if err != nil {
		writeInternalErr(w, "failed to start transaction", err)
		return
	}
	defer tx.Rollback(ctx)

	// Verify team exists and user is owner
	t, err := repo.GetTeamByID(ctx, tx, req.TeamID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) || strings.Contains(err.Error(), "not found") {
			writeError(w, http.StatusNotFound, "team not found")
			return
		}
		writeInternalErr(w, "failed to get team", err)
		return
	}

	if t.OwnerID != int64(auth.UserID) {
		writeError(w, http.StatusForbidden, "access denied")
		return
	}

	budget := &team.TeamBudget{
		TeamID:              req.TeamID,
		MonthlyLimitCredits: req.MonthlyLimitCredits,
		EnforcementMode:     mode,
	}

	if err := repo.UpsertTeamBudget(ctx, tx, budget); err != nil {
		writeInternalErr(w, "failed to upsert budget", err)
		return
	}

	if err := tx.Commit(ctx); err != nil {
		writeInternalErr(w, "failed to commit transaction", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"id":                    budget.ID,
		"team_id":               budget.TeamID,
		"monthly_limit_credits": budget.MonthlyLimitCredits,
		"enforcement_mode":      budget.EnforcementMode,
		"created_at":            budget.CreatedAt,
		"updated_at":            budget.UpdatedAt,
	})
}

func (h *Handler) handleTeamBudgetStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	auth := GetAuthContext(r)
	if auth == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	teamIDStr := r.URL.Query().Get("team_id")
	if teamIDStr == "" {
		writeError(w, http.StatusBadRequest, "team_id query parameter required")
		return
	}

	teamID, err := strconv.ParseInt(teamIDStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid team_id")
		return
	}

	// Use maas.Service to check team budget via team_budget_gate.go
	svc := h.maasSvc()
	if svc == nil || !svc.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "billing service not available")
		return
	}

	verdict, err := svc.CheckTeamMonthlyBudget(r.Context(), teamID)
	if err != nil {
		writeInternalErr(w, "failed to check team budget", err)
		return
	}

	if verdict == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"team_id": teamID,
			"message": "no budget configured",
			"over":    false,
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"team_id":               teamID,
		"enforcement_mode":      verdict.EnforcementMode,
		"monthly_limit_credits": verdict.MonthlyLimitCredits,
		"spent_credits":         verdict.SpentCredits,
		"spent_cents":           verdict.SpentCents,
		"over":                  verdict.Over,
	})
}
