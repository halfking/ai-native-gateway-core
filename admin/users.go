package admin

import (
	"context"
	crypto_rand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
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


// writeAuditLog inserts a row into routing_audit_log using r's AuthContext (best-effort, async).
func (h *Handler) writeAuditLog(r *http.Request, action, targetType string, targetID int, details string) {
	auth := GetAuthContext(r)
	actor := "unknown"
	if auth != nil && auth.Username != "" {
		actor = auth.Username
	}
	h.auditLog(actor, action, targetType, targetID, details)
}

// auditLog inserts a row into routing_audit_log synchronously (3s timeout).
// details is json-encoded so it can be a string, struct, map, or nil.
// Errors are logged but do not block the caller.
func (h *Handler) auditLog(actor, action, targetType string, targetID int, details any) {
	if h.db == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var payload []byte
	if details == nil {
		payload = []byte("null")
	} else {
		var err error
		payload, err = json.Marshal(details)
		if err != nil {
			slog.Warn("audit_log: failed to marshal details", "error", err)
			payload = []byte("null")
		}
	}
	_, err := h.db.Exec(ctx, `INSERT INTO routing_audit_log (actor, action, target_type, target_id, after_json) VALUES ($1, $2, $3, $4, $5)`, actor, action, targetType, targetID, payload)
	if err != nil {
		slog.Warn("audit_log insert failed", "action", action, "actor", actor, "error", err)
	}
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
		// Generate a random password if not provided via environment variable
		// This is more secure than using a hardcoded default
		randomBytes := make([]byte, 16)
		if _, err := crypto_rand.Read(randomBytes); err != nil {
			slog.Error("seed admin: failed to generate random password", "error", err)
			return
		}
		seedPassword = fmt.Sprintf("Admin_%s", hex.EncodeToString(randomBytes))
		slog.Warn("seed admin: no LLM_GATEWAY_SEED_ADMIN_PASSWORD set, using generated password", "hint", "Set LLM_GATEWAY_SEED_ADMIN_PASSWORD env var for predictable password")
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
	// tenant_admin can only list users in their own tenant
	query := `SELECT id, tenant_id, username, display_name, email, role, enabled, last_login_at, created_at FROM users`
	args := []any{}
	if IsTenantAdmin(r) {
		query += ` WHERE tenant_id = $1`
		args = append(args, GetTenantID(r))
	}
	query += ` ORDER BY id`
	rows, err := h.db.Query(ctx, query, args...)
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
	if err := ValidatePasswordComplexity(req.Password); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.TenantID == "" {
		req.TenantID = "default"
	}
	// tenant_admin can only create users in their own tenant, and only with role tenant_admin
	if IsTenantAdmin(r) {
		if req.TenantID != GetTenantID(r) {
			writeError(w, http.StatusForbidden, "tenant_admin can only create users in own tenant")
			return
		}
		if req.Role == "super_admin" {
			writeError(w, http.StatusForbidden, "tenant_admin cannot create super_admin users")
			return
		}
	}
	// Verify tenant exists and is not disabled
	ctxCheck, cancelCheck := context.WithTimeout(r.Context(), 3*time.Second)
	var tenantStatus string
	err := h.db.QueryRow(ctxCheck, `SELECT status FROM tenants WHERE code = $1`, req.TenantID).Scan(&tenantStatus)
	cancelCheck()
	if err != nil {
		writeError(w, http.StatusBadRequest, "tenant does not exist: "+req.TenantID)
		return
	}
	if tenantStatus == "disabled" {
		writeError(w, http.StatusBadRequest, "cannot create user in disabled tenant: "+req.TenantID)
		return
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
	h.writeAuditLog(r, "user.create", "user", u.ID, fmt.Sprintf("username=%s role=%s tenant=%s", u.Username, u.Role, u.TenantID))
	writeJSON(w, http.StatusCreated, u)
}

func (h *Handler) updateUser(w http.ResponseWriter, r *http.Request, id int) {
	var req updateUserRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	// tenant_admin can only update users in their own tenant
	if IsTenantAdmin(r) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		var userTenant string
		err := h.db.QueryRow(ctx, `SELECT tenant_id FROM users WHERE id = $1`, id).Scan(&userTenant)
		cancel()
		if err != nil {
			writeError(w, http.StatusNotFound, "user not found")
			return
		}
		if userTenant != GetTenantID(r) {
			writeError(w, http.StatusForbidden, "tenant_admin can only update users in own tenant")
			return
		}
		// tenant_admin cannot promote users to super_admin
		if req.Role != nil && *req.Role == "super_admin" {
			writeError(w, http.StatusForbidden, "tenant_admin cannot set super_admin role")
			return
		}
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
		if err := ValidatePasswordComplexity(*req.Password); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
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
	// Enrich audit: distinguish enable/disable/role/password changes
	auditDetail := fmt.Sprintf("username=%s", u.Username)
	if req.Enabled != nil {
		if *req.Enabled {
			auditDetail += " enabled=true"
		} else {
			auditDetail += " enabled=false(disabled)"
		}
	}
	if req.Role != nil {
		auditDetail += fmt.Sprintf(" role=%s", *req.Role)
	}
	if req.Password != nil {
		auditDetail += " password_changed=true"
	}
	h.writeAuditLog(r, "user.update", "user", u.ID, auditDetail)
	writeJSON(w, http.StatusOK, u)
}

func (h *Handler) deleteUser(w http.ResponseWriter, r *http.Request, id int) {
	// tenant_admin can only delete users in their own tenant
	if IsTenantAdmin(r) {
		ctxCheck, cancelCheck := context.WithTimeout(r.Context(), 3*time.Second)
		var userTenant string
		err := h.db.QueryRow(ctxCheck, `SELECT tenant_id FROM users WHERE id = $1`, id).Scan(&userTenant)
		cancelCheck()
		if err != nil {
			writeError(w, http.StatusNotFound, "user not found")
			return
		}
		if userTenant != GetTenantID(r) {
			writeError(w, http.StatusForbidden, "tenant_admin can only delete users in own tenant")
			return
		}
	}
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
	h.writeAuditLog(r, "user.delete", "user", id, fmt.Sprintf("user_id=%d", id))
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (h *Handler) resetUserPassword(w http.ResponseWriter, r *http.Request, id int) {
	// tenant_admin can only reset passwords for users in their own tenant
	if IsTenantAdmin(r) {
		ctxCheck, cancelCheck := context.WithTimeout(r.Context(), 3*time.Second)
		var userTenant string
		err := h.db.QueryRow(ctxCheck, `SELECT tenant_id FROM users WHERE id = $1`, id).Scan(&userTenant)
		cancelCheck()
		if err != nil {
			writeError(w, http.StatusNotFound, "user not found")
			return
		}
		if userTenant != GetTenantID(r) {
			writeError(w, http.StatusForbidden, "tenant_admin can only reset passwords for users in own tenant")
			return
		}
	}
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
	if err := ValidatePasswordComplexity(req.Password); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
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
	h.writeAuditLog(r, "user.reset_password", "user", id, fmt.Sprintf("user_id=%d password_reset_by_admin", id))
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
	h.writeAuditLog(r, "user.change_password", "user", auth.UserID, "self")
	writeJSON(w, http.StatusOK, map[string]string{"status": "password_changed"})
}
