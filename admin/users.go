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
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

type userInfo struct {
	ID                 int        `json:"id"`
	TenantID           string     `json:"tenant_id"`
	Username           string     `json:"username"`
	DisplayName        string     `json:"display_name"`
	Email              string     `json:"email"`
	Role               string     `json:"role"`
	Enabled            bool       `json:"enabled"`
	MustChangePassword bool       `json:"must_change_password"`
	LastLoginAt        *time.Time `json:"last_login_at"`
	CreatedAt          time.Time  `json:"created_at"`
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

// ── 角色口径（2026-11-26 角色治理轮）────────────────────────────────
//
// 平台角色闭集（管理端可指派）：
//
//	RoleSuperAdmin  平台超级管理员 —— **只属于 default 租户**
//	RoleTenantAdmin 租户管理员     —— 只管本租户
//	RoleUser        普通用户       —— 只用密钥调网关，不进管理台
//
// ★ 不变量：**非 default 租户内不得存在 super_admin**。
//   理由：super_admin 是**平台级**身份（能看全部租户、能改全局配置），
//   把它挂在某个业务租户下 = 该租户拿到了平台权限 ⇒ 租户边界失效。
//   ⇒ 判据只在这里一份（roleAllowedInTenant），创建与变更角色两条写路径同源调用。
//   ⇒ 前端下拉只是提示层，真正的闸门在服务端（前端可被绕过）。
//
// ★ 口径来源：live 211 实测（2026-11-26）租户 chenb 下存在 super_admin 账号，
//   且 POST /api/users 对任意租户都接受 role=super_admin ⇒ 本轮收口。
const (
	RoleSuperAdmin  = "super_admin"
	RoleTenantAdmin = "tenant_admin"
	RoleUser        = "user"
)

// DefaultTenantCode 是唯一允许拥有 super_admin 的租户。
const DefaultTenantCode = "default"

// assignableRoles 是管理端可指派的角色闭集（闭集之外的字符串一律拒）。
var assignableRoles = map[string]bool{
	RoleSuperAdmin:  true,
	RoleTenantAdmin: true,
	RoleUser:        true,
}

// roleAllowedInTenant 判「角色能否落在这个租户」。
// 两条规则：① 角色在闭集内；② super_admin 只能落在 default 租户。
func roleAllowedInTenant(role, tenantID string) bool {
	if !assignableRoles[role] {
		return false
	}
	if role == RoleSuperAdmin && tenantID != DefaultTenantCode {
		return false
	}
	return true
}

// roleOptionsForTenant 返回某租户可用的角色闭集（前端下拉同源口径）。
func roleOptionsForTenant(tenantID string) []string {
	if tenantID == DefaultTenantCode {
		return []string{RoleSuperAdmin, RoleTenantAdmin, RoleUser}
	}
	return []string{RoleTenantAdmin, RoleUser}
}

// ValidateAssignableRole 校验「角色能否落在这个租户」，越界时返回可回给调用方的文案。
//
// ★ 这一份是**唯一口径**：admin 内部两条写路径（createUser / updateUser）与
//   pkg/authplugin/native 共用它 ⇒ 不会因为多一条入口就漏掉规则。
func ValidateAssignableRole(role, tenantID string) error {
	if !assignableRoles[role] {
		return fmt.Errorf("role must be one of: super_admin, tenant_admin, user")
	}
	if role == RoleSuperAdmin && tenantID != DefaultTenantCode {
		return fmt.Errorf("super_admin role is only allowed in the default tenant")
	}
	return nil
}

// userDB 返回用户管理写路径的查询面（窄注入面；nil ⇒ 生产 h.db）。
func (h *Handler) userDB() usersDB {
	if h.usersDB != nil {
		return h.usersDB
	}
	return h.db
}

// writeAuditLog inserts a row into routing_audit_log using r's AuthContext (best-effort, async).
func (h *Handler) writeAuditLog(r *http.Request, action, targetType string, targetID int, details any) {
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
//
// 2026-07-16 hardening (incident: 4× "audit_log insert failed" with
// SQLSTATE 22P02 in gateway.stderr):
//
//   - JSONB column after_json is fed a marshaled []byte. json.Marshal
//     will fail for Go values containing NaN/±Inf floats, and PG JSONB
//     rejects null bytes (\x00) with SQLSTATE 22021.
//
//   - Best-effort: scrub the payload so it always parses as valid
//     JSONB. Strings are scrubbed of invalid UTF-8 / null bytes, map
//     values are recursively sanitized for NaN/Inf. If scrubbing
//     still fails, we fall back to a plain text payload so the audit
//     row is preserved (the alternative — silently dropping the
//     login event — is worse than storing raw text in after_json).
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
	payload = sanitizeJSONBPayload(payload)
	// 2026-07-16: Force text protocol to avoid pgx binary encoding issues (same fix as apihub/telemetry)
	_, err := h.db.Exec(ctx, `INSERT INTO routing_audit_log (actor, action, target_type, target_id, after_json) VALUES ($1, $2, $3, $4, $5::text::jsonb)`, actor, action, targetType, targetID, string(payload))
	if err != nil {
		// Last-resort: PG still rejected (could be a transient type
		// issue we cannot sanitize). Try inserting the raw text form
		// so we never silently drop a login event. Cast through
		// ::text to bypass JSONB validation.
		if isJSONBValidationError(err) {
			fallback := auditJSONBValidationFallback(payload)
			if _, fbErr := h.db.Exec(ctx,
				`INSERT INTO routing_audit_log (actor, action, target_type, target_id, after_json) VALUES ($1, $2, $3, $4, $5::text::jsonb)`,

				actor, action, targetType, targetID, string(fallback)); fbErr != nil {
				slog.Warn("audit_log insert failed (fallback also failed)",
					"action", action, "actor", actor, "error", err, "fallback_error", fbErr)
				return
			}
			slog.Warn("audit_log insert fell back to escaped payload",
				"action", action, "actor", actor, "error", err)
			return
		}
		slog.Warn("audit_log insert failed", "action", action, "actor", actor, "error", err)
	}
}

// sanitizeJSONBPayload ensures payload parses as valid JSON and that
// every string leaf contains valid UTF-8 with no null bytes. PG
// rejects "\x00" with SQLSTATE 22021 and any invalid JSON with
// SQLSTATE 22P02.
func sanitizeJSONBPayload(payload []byte) []byte {
	if len(payload) == 0 {
		return []byte("null")
	}
	// json.Valid only checks JSON syntax; it accepts invalid UTF-8
	// bytes because the JSON spec permits arbitrary bytes inside
	// string literals (escaped or not). PostgreSQL JSONB is stricter
	// and rejects invalid UTF-8 with SQLSTATE 22021. So we must also
	// verify utf8.Valid here before declaring a payload safe.
	if json.Valid(payload) && !strings.ContainsRune(string(payload), 0) && utf8.Valid(payload) {
		return payload
	}
	// Try to scrub and re-validate.
	scrubbed := scrubUTF8Bytes(payload)
	if !json.Valid(scrubbed) || !utf8.Valid(scrubbed) {
		return []byte("null")
	}
	return scrubbed
}

// scrubUTF8Bytes replaces invalid UTF-8 sequences with U+FFFD and
// drops NUL bytes. Operates on a []byte to preserve the original
// string boundaries around JSON tokens.
func scrubUTF8Bytes(b []byte) []byte {
	if utf8.Valid(b) && !containsNUL(b) {
		return b
	}
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); {
		r, size := utf8.DecodeRune(b[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			out = append(out, 0xEF, 0xBF, 0xBD) // U+FFFD UTF-8
		case r == 0:
			// strip NUL
		default:
			out = append(out, b[i:i+size]...)
		}
		i += size
	}
	return out
}

func containsNUL(b []byte) bool {
	for _, c := range b {
		if c == 0 {
			return true
		}
	}
	return false
}

// scrubUTF8 is a string wrapper for the fallback path.
func scrubUTF8(s string) string {
	return string(scrubUTF8Bytes([]byte(s)))
}

func auditJSONBValidationFallback(payload []byte) []byte {
	fallback, err := json.Marshal(map[string]string{"raw": scrubUTF8(string(payload))})
	if err != nil {
		return []byte(`{"raw":""}`)
	}
	return fallback
}

// isJSONBValidationError returns true for SQLSTATE 22P02 (invalid
// JSON) and 22021 (invalid UTF-8 / NUL byte). Used to decide whether
// to retry with the escaped-payload fallback.
func isJSONBValidationError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "22P02") ||
		strings.Contains(msg, "22021") ||
		strings.Contains(msg, "invalid input syntax for type json") ||
		strings.Contains(msg, "invalid byte sequence")
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
		INSERT INTO users (tenant_id, username, password_hash, display_name, role, enabled, must_change_password)
		VALUES ('default', 'admin', $1, '系统管理员', 'super_admin', TRUE, TRUE)
		ON CONFLICT (username) DO NOTHING
	`, string(hash))
	if err != nil {
		slog.Error("seed admin: insert failed", "error", err)
		return
	}
	slog.Info("seed admin user created", "username", "admin", "role", "super_admin")
}

func (h *Handler) handleUsers(w http.ResponseWriter, r *http.Request) {
	if !requireUserManager(w, r) {
		return
	}
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

// requireUserManager prevents ordinary user principals from reaching the account
// directory and write surface. AdminMiddleware authenticates but intentionally
// does not enforce an administrative role.
func requireUserManager(w http.ResponseWriter, r *http.Request) bool {
	auth := GetAuthContext(r)
	if auth != nil && auth.Role == "tenant_admin" && strings.TrimSpace(auth.TenantID) == "" {
		writeError(w, http.StatusForbidden, "tenant scope unavailable")
		return false
	}
	if auth != nil && (auth.Role == "super_admin" || auth.Role == "tenant_admin" || auth.Role == "admin_key") {
		return true
	}
	writeError(w, http.StatusForbidden, "administrator role required")
	return false
}

func (h *Handler) listUsers(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	// tenant_admin can only list users in their own tenant
	query := `SELECT id, tenant_id, username, display_name, email, role, enabled, must_change_password, last_login_at, created_at FROM users`
	args := []any{}
	if IsTenantAdmin(r) {
		query += ` WHERE tenant_id = $1`
		args = append(args, GetTenantID(r))
	}
	query += ` ORDER BY id`
	rows, err := h.userDB().Query(ctx, query, args...)
	if err != nil {
		writeInternalErr(w, "query failed", err)
		return
	}
	defer rows.Close()
	var users []userInfo
	for rows.Next() {
		var u userInfo
		if err := rows.Scan(&u.ID, &u.TenantID, &u.Username, &u.DisplayName, &u.Email, &u.Role, &u.Enabled, &u.MustChangePassword, &u.LastLoginAt, &u.CreatedAt); err != nil {
			warnRowSkip("users.list", err)
			continue
		}
		users = append(users, u)
	}
	// 账号清单少一截 = 管理员看不见某个租户的用户（且前端以为就是全部）。
	if writeAggRowsErr(w, "users.list", rows.Err()) {
		return
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
		req.TenantID = DefaultTenantCode
	}
	// tenant_admin can only create users in their own tenant, and only with role tenant_admin
	if IsTenantAdmin(r) {
		if req.TenantID != GetTenantID(r) {
			writeError(w, http.StatusForbidden, "tenant_admin can only create users in own tenant")
			return
		}
		if req.Role == RoleSuperAdmin {
			writeError(w, http.StatusForbidden, "tenant_admin cannot create super_admin users")
			return
		}
	}
	// Verify tenant exists and is not disabled
	ctxCheck, cancelCheck := context.WithTimeout(r.Context(), 3*time.Second)
	var tenantStatus string
	err := h.userDB().QueryRow(ctxCheck, `SELECT status FROM tenants WHERE code = $1`, req.TenantID).Scan(&tenantStatus)
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
		req.Role = RoleTenantAdmin
	}
	// ★ 角色闭集 + 租户不变量（唯一口径）：普通用户（user）是合法角色；
	//   super_admin 只允许落在 default 租户 ⇒ 非 default 租户只能建
	//   tenant_admin 或 user。
	if err := ValidateAssignableRole(req.Role, req.TenantID); err != nil {
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
	var u userInfo
	err = h.userDB().QueryRow(ctx, `
		INSERT INTO users (tenant_id, username, password_hash, display_name, email, role, must_change_password)
		VALUES ($1, $2, $3, $4, $5, $6, TRUE)
		RETURNING id, tenant_id, username, display_name, email, role, enabled, must_change_password, created_at
	`, req.TenantID, req.Username, string(hash), req.DisplayName, req.Email, req.Role).Scan(
		&u.ID, &u.TenantID, &u.Username, &u.DisplayName, &u.Email, &u.Role, &u.Enabled, &u.MustChangePassword, &u.CreatedAt,
	)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key") {
			writeError(w, http.StatusConflict, "username already exists")
			return
		}
		writeInternalErr(w, "create user failed", err)
		return
	}
	h.writeAuditLog(r, "user.create", "user", u.ID, fmt.Sprintf("username=%s role=%s tenant=%s", u.Username, u.Role, u.TenantID))
	writeJSON(w, http.StatusCreated, u)
}

func (h *Handler) updateUser(w http.ResponseWriter, r *http.Request, id int) {
	if !requireUserManager(w, r) {
		return
	}
	var req updateUserRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	// 目标用户的租户与当前角色：租户边界判定、角色不变量、审计「旧角色→新角色」
	// 三处都要用 ⇒ 只查一次（变更角色时必查；tenant_admin 操作时必查）。
	var targetTenant, targetRole string
	if req.Role != nil || IsTenantAdmin(r) {
		ctxLookup, cancelLookup := context.WithTimeout(r.Context(), 3*time.Second)
		err := h.userDB().QueryRow(ctxLookup, `SELECT tenant_id, role FROM users WHERE id = $1`, id).Scan(&targetTenant, &targetRole)
		cancelLookup()
		if err != nil {
			writeLookupErr(w, "user not found", err)
			return
		}
	}
	// tenant_admin can only update users in their own tenant
	if IsTenantAdmin(r) && targetTenant != GetTenantID(r) {
		writeError(w, http.StatusForbidden, "tenant_admin can only update users in own tenant")
		return
	}
	if req.Role != nil {
		// ② tenant_admin 永远不能授予 super_admin（与租户无关）。
		if *req.Role == RoleSuperAdmin && IsTenantAdmin(r) {
			writeError(w, http.StatusForbidden, "tenant_admin cannot set super_admin role")
			return
		}
		// ① 角色闭集 + ③ 租户不变量（与 createUser 同一份口径）。
		if err := ValidateAssignableRole(*req.Role, targetTenant); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		// ④ 不能把最后一个（启用的）平台超管降级 ⇒ 等于关掉管理台唯一入口。
		if targetRole == RoleSuperAdmin && *req.Role != RoleSuperAdmin {
			ctxOthers, cancelOthers := context.WithTimeout(r.Context(), 3*time.Second)
			var others int
			err := h.userDB().QueryRow(ctxOthers, `SELECT count(*) FROM users WHERE role = $1 AND enabled AND id <> $2`, RoleSuperAdmin, id).Scan(&others)
			cancelOthers()
			if err == nil && others == 0 {
				writeError(w, http.StatusConflict, "cannot demote the last super_admin")
				return
			}
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
		// 角色值本身已在上面校验过（闭集 + 租户不变量 + 最后一个超管保护）。
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
		sets = append(sets, "password_hash = $"+strconv.Itoa(argIdx), "must_change_password = TRUE")
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
		" RETURNING id, tenant_id, username, display_name, email, role, enabled, must_change_password, last_login_at, created_at"
	var u userInfo
	err := h.userDB().QueryRow(ctx, query, args...).Scan(
		&u.ID, &u.TenantID, &u.Username, &u.DisplayName, &u.Email, &u.Role, &u.Enabled, &u.MustChangePassword, &u.LastLoginAt, &u.CreatedAt,
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
		// 角色变更记「旧→新」，否则审计里只看到新值 ⇒ 看不出是谁被降级。
		if targetRole != "" {
			auditDetail += fmt.Sprintf(" role=%s->%s", targetRole, *req.Role)
		} else {
			auditDetail += fmt.Sprintf(" role=%s", *req.Role)
		}
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
		err := h.userDB().QueryRow(ctxCheck, `SELECT tenant_id FROM users WHERE id = $1`, id).Scan(&userTenant)
		cancelCheck()
		if err != nil {
			writeLookupErr(w, "user not found", err)
			return
		}
		if userTenant != GetTenantID(r) {
			writeError(w, http.StatusForbidden, "tenant_admin can only delete users in own tenant")
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	tag, err := h.userDB().Exec(ctx, "DELETE FROM users WHERE id = $1", id)
	if err != nil {
		writeInternalErr(w, "delete failed", err)
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
		err := h.userDB().QueryRow(ctxCheck, `SELECT tenant_id FROM users WHERE id = $1`, id).Scan(&userTenant)
		cancelCheck()
		if err != nil {
			writeLookupErr(w, "user not found", err)
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
	tag, err := h.userDB().Exec(ctx, `UPDATE users SET password_hash = $1, must_change_password = TRUE, updated_at = now() WHERE id = $2`, string(hash), id)
	if err != nil {
		writeInternalErr(w, "update failed", err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	// R56 audit: an admin-forced reset (e.g. credential-leak response) must
	// revoke the target user's existing tokens exactly like the B4
	// self-service change does — otherwise the old JWT stays valid until its
	// remaining TTL. A failed write must not fail the reset itself (same
	// trade as the B4 self-change path).
	if revokeErr := revokeUserTokensForPasswordChange(ctx, h.db, id); revokeErr != nil {
		slog.Warn("admin password reset succeeded but token revocation failed",
			"user_id", id,
			"error", revokeErr,
		)
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
	if h.db == nil {
		// Without DB we can't load the user, but the JWT is valid. Return
		// a minimal userInfo synthesized from the JWT claims so the SPA can
		// keep working in offline / no-DB mode.
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	// AdminMiddleware now only accepts JWT (no sk-* fallback), so auth.IsJWT is always true
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	var u userInfo
	err := h.db.QueryRow(ctx, `
		SELECT id, tenant_id, username, display_name, email, role, enabled, must_change_password, last_login_at, created_at
		FROM users WHERE id = $1
	`, auth.UserID).Scan(&u.ID, &u.TenantID, &u.Username, &u.DisplayName, &u.Email, &u.Role, &u.Enabled, &u.MustChangePassword, &u.LastLoginAt, &u.CreatedAt)
	if err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}

	// 2026-07-10: Also return a fresh JWT in the response so the SPA can persist it
	// to localStorage and use it as Authorization: Bearer header on subsequent calls.
	// This fixes the "cookie-only hydration" gap where the user has a valid cookie
	// but no JWT in localStorage (e.g., logged in before the unified-auth change).
	token, expiresAt, signErr := SignToken(u.ID, u.TenantID, u.Username, u.Role, h.secret, u.MustChangePassword)
	if signErr != nil {
		// Don't fail the /me call if signing fails — just return user info
		writeJSON(w, http.StatusOK, u)
		return
	}
	// Refresh the HttpOnly cookie too
	setSessionCookie(w, r, token, expiresAt)
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": token,
		"expires_at":   expiresAt.Format(time.RFC3339),
		"user":         u,
	})
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
		writeLookupErr(w, "user not found", err)
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
	_, err = h.db.Exec(ctx, `UPDATE users SET password_hash = $1, must_change_password = FALSE, updated_at = now() WHERE id = $2`, string(newHash), auth.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "update failed")
		return
	}
	// B4 (2026-09-22): revoke every token issued before now — old JWTs stay
	// cryptographically valid until expiry, so the epoch record is what
	// makes the auth middlewares reject them (401) immediately. A failed
	// write must not fail the password change itself; the exposure window
	// is then bounded by the remaining token TTL as before.
	if revokeErr := revokeUserTokensForPasswordChange(ctx, h.db, auth.UserID); revokeErr != nil {
		slog.Warn("password change succeeded but token revocation failed",
			"user_id", auth.UserID,
			"error", revokeErr,
		)
	}
	h.writeAuditLog(r, "user.change_password", "user", auth.UserID, "self")
	writeJSON(w, http.StatusOK, map[string]string{"status": "password_changed"})
}
