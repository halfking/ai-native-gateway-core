package admin

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

type userInfo struct {
	ID          int        `json:"id"`
	TenantID    string     `json:"tenant_id"`
	Username    string     `json:"username"`
	DisplayName string     `json:"display_name"`
	Email       string     `json:"email"`
	Role        string     `json:"role"`
	Enabled     bool       `json:"enabled"`
	LastLoginAt *time.Time `json:"last_login_at"`
	CreatedAt   time.Time  `json:"created_at"`
}

type createUserRequest struct {
	Username    string `json:"username"`
	Password    string `json:"password"`
	TenantID    string `json:"tenant_id"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
	Role        string `json:"role"`
}

type updateUserRequest struct {
	DisplayName *string `json:"display_name"`
	Email       *string `json:"email"`
	Role        *string `json:"role"`
	Enabled     *bool   `json:"enabled"`
	Password    *string `json:"password"`
}

type changePasswordRequest struct {
	OldPassword string `json:"old_password"`
	NewPassword string `json:"new_password"`
}

// EnsureSeedAdmin creates the initial super_admin user if the users table is empty.
func EnsureSeedAdmin(pool *pgxpool.Pool) {
	if pool == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var count int
	if err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM users").Scan(&count); err != nil {
		slog.Warn("seed admin: failed to query users table", "error", err)
		return
	}
	if count > 0 {
		return
	}

	seedPassword := os.Getenv("LLM_GATEWAY_SEED_ADMIN_PASSWORD")
	if seedPassword == "" {
		seedPassword = "<ADMIN_PASSWORD_REDACTED>"
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(seedPassword), bcrypt.DefaultCost)
	if err != nil {
		slog.Error("seed admin: bcrypt failed", "error", err)
		return
	}

	_, err = pool.Exec(ctx, `
		INSERT INTO users (tenant_id, username, password_hash, display_name, role, enabled)
		VALUES ('default', 'admin', $1, '系统管理员', 'super_admin', TRUE)
		ON CONFLICT (username) DO NOTHING
	`, string(hash))
	if err != nil {
		slog.Error("seed admin: insert failed", "error", err)
		return
	}
	slog.Info("seed admin user created", "username", "admin", "role", "super_admin")
}

func (h *Handler) handleUsers(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api/users" || r.URL.Path == "/api/users/" {
		switch r.Method {
		case http.MethodGet:
			h.listUsers(w, r)
		case http.MethodPost:
			h.createUser(w, r)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}
	idStr := strings.TrimPrefix(r.URL.Path, "/api/users/")
	idStr = strings.TrimSuffix(idStr, "/")

	if strings.Contains(idStr, "/password") {
		idStr = strings.TrimSuffix(idStr, "/password")
		id, err := strconv.Atoi(idStr)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid user id")
			return
		}
		if r.Method != http.MethodPut {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.resetUserPassword(w, r, id)
		return
	}

	id, err := strconv.Atoi(idStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}

	switch r.Method {
	case http.MethodPut:
		h.updateUser(w, r, id)
	case http.MethodDelete:
		h.deleteUser(w, r, id)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) listUsers(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	rows, err := h.db.Query(ctx, `SELECT id, tenant_id, username, display_name, email, role, enabled, last_login_at, created_at FROM users ORDER BY id`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}
	defer rows.Close()
	var users []userInfo
	for rows.Next() {
		var u userInfo
		if err := rows.Scan(&u.ID, &u.TenantID, &u.Username, &u.DisplayName, &u.Email, &u.Role, &u.Enabled, &u.LastLoginAt, &u.CreatedAt); err != nil {
			continue
		}
		users = append(users, u)
	}
	if users == nil {
		users = []userInfo{}
	}
	writeJSON(w, http.StatusOK, users)
}

func (h *Handler) createUser(w http.ResponseWriter, r *http.Request) {
	var req createUserRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Username == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "username and password are required")
		return
	}
	if len(req.Password) < 8 {
		writeError(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}
	if req.TenantID == "" {
		req.TenantID = "default"
	}
	if req.Role == "" {
		req.Role = "tenant_admin"
	}
	if req.Role != "super_admin" && req.Role != "tenant_admin" {
		writeError(w, http.StatusBadRequest, "role must be super_admin or tenant_admin")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "password hash failed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	var u userInfo
	err = h.db.QueryRow(ctx, `
		INSERT INTO users (tenant_id, username, password_hash, display_name, email, role)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, tenant_id, username, display_name, email, role, enabled, created_at
	`, req.TenantID, req.Username, string(hash), req.DisplayName, req.Email, req.Role).Scan(
		&u.ID, &u.TenantID, &u.Username, &u.DisplayName, &u.Email, &u.Role, &u.Enabled, &u.CreatedAt,
	)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key") {
			writeError(w, http.StatusConflict, "username already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, "create user failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, u)
}

func (h *Handler) updateUser(w http.ResponseWriter, r *http.Request, id int) {
	var req updateUserRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	var sets []string
	var args []any
	argIdx := 1
	if req.DisplayName != nil {
		sets = append(sets, "display_name = $"+strconv.Itoa(argIdx))
		args = append(args, *req.DisplayName)
		argIdx++
	}
	if req.Email != nil {
		sets = append(sets, "email = $"+strconv.Itoa(argIdx))
		args = append(args, *req.Email)
		argIdx++
	}
	if req.Role != nil {
		if *req.Role != "super_admin" && *req.Role != "tenant_admin" {
			writeError(w, http.StatusBadRequest, "role must be super_admin or tenant_admin")
			return
		}
		sets = append(sets, "role = $"+strconv.Itoa(argIdx))
		args = append(args, *req.Role)
		argIdx++
	}
	if req.Enabled != nil {
		sets = append(sets, "enabled = $"+strconv.Itoa(argIdx))
		args = append(args, *req.Enabled)
		argIdx++
	}
	if req.Password != nil {
		if len(*req.Password) < 8 {
			writeError(w, http.StatusBadRequest, "password must be at least 8 characters")
			return
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(*req.Password), bcrypt.DefaultCost)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "password hash failed")
			return
		}
		sets = append(sets, "password_hash = $"+strconv.Itoa(argIdx))
		args = append(args, string(hash))
		argIdx++
	}
	if len(sets) == 0 {
		writeError(w, http.StatusBadRequest, "no fields to update")
		return
	}
	sets = append(sets, "updated_at = now()")
	args = append(args, id)
	query := "UPDATE users SET " + strings.Join(sets, ", ") + " WHERE id = $" + strconv.Itoa(argIdx) +
		" RETURNING id, tenant_id, username, display_name, email, role, enabled, last_login_at, created_at"
	var u userInfo
	err := h.db.QueryRow(ctx, query, args...).Scan(
		&u.ID, &u.TenantID, &u.Username, &u.DisplayName, &u.Email, &u.Role, &u.Enabled, &u.LastLoginAt, &u.CreatedAt,
	)
	if err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	writeJSON(w, http.StatusOK, u)
}

func (h *Handler) deleteUser(w http.ResponseWriter, r *http.Request, id int) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	tag, err := h.db.Exec(ctx, "DELETE FROM users WHERE id = $1", id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "delete failed: "+err.Error())
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (h *Handler) resetUserPassword(w http.ResponseWriter, r *http.Request, id int) {
	var req struct {
		Password string `json:"password"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.Password) < 8 {
		writeError(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "password hash failed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	tag, err := h.db.Exec(ctx, `UPDATE users SET password_hash = $1, updated_at = now() WHERE id = $2`, string(hash), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "update failed: "+err.Error())
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "password_reset"})
}

func (h *Handler) handleAuthMe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	auth := GetAuthContext(r)
	if auth == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	if !auth.IsJWT {
		writeJSON(w, http.StatusOK, userInfo{
			ID: 0, TenantID: "default", Username: "admin",
			DisplayName: "管理员 (API Key)", Role: "super_admin", Enabled: true,
		})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	var u userInfo
	err := h.db.QueryRow(ctx, `
		SELECT id, tenant_id, username, display_name, email, role, enabled, last_login_at, created_at
		FROM users WHERE id = $1
	`, auth.UserID).Scan(&u.ID, &u.TenantID, &u.Username, &u.DisplayName, &u.Email, &u.Role, &u.Enabled, &u.LastLoginAt, &u.CreatedAt)
	if err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	writeJSON(w, http.StatusOK, u)
}

func (h *Handler) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	auth := GetAuthContext(r)
	if auth == nil || !auth.IsJWT {
		writeError(w, http.StatusUnauthorized, "jwt authentication required")
		return
	}
	var req changePasswordRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.NewPassword) < 8 {
		writeError(w, http.StatusBadRequest, "new password must be at least 8 characters")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	var currentHash string
	err := h.db.QueryRow(ctx, "SELECT password_hash FROM users WHERE id = $1", auth.UserID).Scan(&currentHash)
	if err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(currentHash), []byte(req.OldPassword)); err != nil {
		writeError(w, http.StatusForbidden, "old password is incorrect")
		return
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "password hash failed")
		return
	}
	_, err = h.db.Exec(ctx, `UPDATE users SET password_hash = $1, updated_at = now() WHERE id = $2`, string(newHash), auth.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "update failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "password_changed"})
}
