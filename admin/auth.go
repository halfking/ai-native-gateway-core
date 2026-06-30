package admin

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

var errPasswordChangeRequired = errors.New("password change required before accessing other APIs")

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type keyCreatedResponse struct {
	APIKey    string `json:"api_key"`
	KeyPrefix string `json:"key_prefix"`
}

func getAdminPassword() string {
	if p := os.Getenv("LLM_GATEWAY_ADMIN_PASSWORD"); p != "" {
		return p
	}
	return "admin"
}

func hashAPIKey(secretKey, rawKey string) string {
	mac := hmac.New(sha256.New, []byte(secretKey))
	mac.Write([]byte(rawKey))
	return hex.EncodeToString(mac.Sum(nil))
}

func verifyAdminAuth(r *http.Request, db *pgxpool.Pool, secretKey string) bool {
	auth := r.Header.Get("Authorization")
	if len(auth) < 7 || auth[:7] != "Bearer " {
		return false
	}
	rawKey := auth[7:]
	keyHash := hashAPIKey(secretKey, rawKey)

	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	var appCode string
	err := db.QueryRow(ctx, `
		SELECT app.code
		FROM api_keys ak
		JOIN applications app ON app.id = ak.application_id
		WHERE ak.key_hash = $1 AND ak.enabled = TRUE
		  AND COALESCE(ak.status, 'active') <> 'revoked'
		  AND (ak.expires_at IS NULL OR ak.expires_at > now())
	`, keyHash).Scan(&appCode)
	if err != nil {
		return false
	}
	return appCode == "admin"
}

// handleLogout clears the session cookie (rule 20 §6.1).
func (h *Handler) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	clearSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func AdminMiddleware(next http.HandlerFunc, db *pgxpool.Pool, secretKey string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// ── Try JWT auth first (Bearer header or session cookie) ──
		if tokenStr, ok := extractBearerOrCookieToken(r); ok {
			claims, err := VerifyToken(tokenStr, secretKey)
			if err == nil && claims.UserID > 0 {
				authReq := SetAuthContext(r, &AuthContext{
					UserID:             claims.UserID,
					TenantID:           claims.TenantID,
					Username:           claims.Username,
					Role:               claims.Role,
					IsJWT:              true,
					MustChangePassword: claims.MustChangePassword,
				})
				if enforceMustChangePassword(w, r, claims) {
					return
				}
				next(w, authReq)
				return
			}
		}

		// ── Fall back to legacy admin API key (DB lookup) ──────
		// Restored after f88a96aa regression: the previous code returned 401
		// unconditionally when JWT verification failed, leaving this branch
		// as dead code. Admin API keys (sk-...) registered in the api_keys
		// table with application code 'admin' must still authenticate.
		// ── Fall back to legacy admin API key (DB lookup) ──────
		// DEPRECATED (rule 20 §10): scheduled for removal on 2026-07-27.
		if db != nil && verifyAdminAuth(r, db, secretKey) {
			slog.Warn("admin api key fallback deprecated, migrate to JWT (removal 2026-07-27)",
				"path", r.URL.Path, "remote", r.RemoteAddr)
			authReq := SetAuthContext(r, &AuthContext{
				TenantID: "default",
				Username: "admin",
				Role:     "admin_key",
				IsJWT:    false,
			})
			next(w, authReq)
			return
		}

		// No valid JWT and no valid API key → 401
		writeError(w, http.StatusUnauthorized, "authentication required")
	}
}

func isPasswordChangeAllowedPath(path string) bool {
	switch path {
	case "/api/auth/me",
		"/api/auth/change-password",
		"/api/auth/logout":
		return true
	}
	return false
}

// enforceMustChangePassword applies rule 20 §6.2 — users with
// MustChangePassword=true can only hit whitelisted paths (me,
// change-password, logout). All other requests return 403. Returns
// true if the request was handled (blocked), false if processing
// should continue.
//
// 2026-06-30 PR-4: extracted from AdminMiddleware so SuperAdminMiddleware
// can apply the same gate. Without this, super_admin requests with
// must_change_password=true could bypass the password-change requirement
// via the Authorization: Bearer header path (C4.1 in weekly audit).
func enforceMustChangePassword(w http.ResponseWriter, r *http.Request, claims *JWTClaims) bool {
	if claims == nil || !claims.MustChangePassword {
		return false
	}
	if isPasswordChangeAllowedPath(r.URL.Path) {
		return false
	}
	writeError(w, http.StatusForbidden, errPasswordChangeRequired.Error())
	return true
}

func (h *Handler) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}

	// Rate limit by client IP
	clientIP := clientIPFromRequest(r)
	if !loginLimiter.Allow(clientIP) {
		h.auditLog("unknown", "authentication.rate_limited", "user", 0, map[string]any{"ip": clientIP})
		writeError(w, http.StatusTooManyRequests, "too many login attempts, try again later")
		return
	}

	var req loginRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// ── Try JWT auth (users table) first ──────────────────────────────
	if req.Username != "" && req.Password != "" {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		var u struct {
			ID                 int
			TenantID           string
			Username           string
			PasswordHash       string
			DisplayName        string
			Email              string
			Role               string
			Enabled            bool
			MustChangePassword bool
		}
		err := h.db.QueryRow(ctx, `
			SELECT id, tenant_id, username, password_hash, display_name, email, role, enabled, must_change_password
			FROM users WHERE username = $1
		`, req.Username).Scan(&u.ID, &u.TenantID, &u.Username, &u.PasswordHash, &u.DisplayName, &u.Email, &u.Role, &u.Enabled, &u.MustChangePassword)

		if err == nil && u.Enabled {
			if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(req.Password)) == nil {
				// Check tenant is not disabled (bypass for 'default' super_admin)
				if u.TenantID != "default" {
					var tenantStatus string
					err := h.db.QueryRow(ctx, `SELECT status FROM tenants WHERE code = $1`, u.TenantID).Scan(&tenantStatus)
					if err != nil || tenantStatus == "disabled" {
						h.auditLog(req.Username, "authentication.login_failed", "user", u.ID, fmt.Sprintf("method=jwt reason=tenant_disabled tenant=%s ip=%s", u.TenantID, clientIP))
						writeError(w, http.StatusForbidden, "tenant is disabled, contact your administrator")
						return
					}
				}
				// Update last_login_at
				//nolint:errcheck // best-effort exec, non-critical
				h.db.Exec(ctx, `UPDATE users SET last_login_at = now() WHERE id = $1`, u.ID)

				token, expiresAt, signErr := SignToken(u.ID, u.TenantID, u.Username, u.Role, h.secret, u.MustChangePassword)
				if signErr != nil {
					slog.Error("handleLogin: sign jwt failed", "error", signErr)
					writeError(w, http.StatusInternalServerError, "token generation failed")
					return
				}

				h.auditLog(u.Username, "authentication.login", "user", u.ID, fmt.Sprintf("method=jwt role=%s tenant=%s ip=%s", u.Role, u.TenantID, r.RemoteAddr))
				// Dual-write (rule 20 §6.1): set HttpOnly cookie AND return access_token
				setSessionCookie(w, r, token, expiresAt)
				writeJSON(w, http.StatusOK, map[string]any{
					"access_token": token,
					"token_type":   "Bearer",
					"expires_at":   expiresAt.Format(time.RFC3339),
					"user": map[string]any{
						"id":                   u.ID,
						"tenant_id":            u.TenantID,
						"username":             u.Username,
						"display_name":         u.DisplayName,
						"email":                u.Email,
						"role":                 u.Role,
						"enabled":              u.Enabled,
						"must_change_password": u.MustChangePassword,
					},
				})
				return
			}
		}
		h.auditLog(req.Username, "authentication.login_failed", "user", 0, fmt.Sprintf("method=jwt ip=%s", r.RemoteAddr))
	}

	// ── Fall back to legacy admin key auth ────────────────────────────
	if subtle.ConstantTimeCompare([]byte(req.Username), []byte("admin")) != 1 ||
		subtle.ConstantTimeCompare([]byte(req.Password), []byte(getAdminPassword())) != 1 {
		writeError(w, http.StatusUnauthorized, "Invalid credentials")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var appID int
	err := h.db.QueryRow(ctx, `SELECT id FROM applications WHERE code = 'admin' AND tenant_id = 'default'`).Scan(&appID)
	if err != nil {
		slog.Error("admin app not found", "error", err)
		writeError(w, http.StatusInternalServerError, "Admin application not initialized")
		return
	}

	var existingID int
	var ciphertext string
	err = h.db.QueryRow(ctx, `
		SELECT id, key_ciphertext FROM api_keys
		WHERE application_id = $1 AND tenant_id = 'default'
		  AND enabled = TRUE AND COALESCE(status, 'active') <> 'revoked'
		  AND key_ciphertext IS NOT NULL AND key_ciphertext != ''
		ORDER BY id ASC LIMIT 1
	`, appID).Scan(&existingID, &ciphertext)
	if err == nil && ciphertext != "" {
		decrypted, decErr := h.decryptCredStr(ciphertext)
		if decErr == nil {
			//nolint:errcheck // best-effort exec, non-critical
			h.db.Exec(ctx, `UPDATE api_keys SET is_system = TRUE, remark = 'admin login: reused existing key' WHERE id = $1 AND (remark IS NULL OR remark = '')`, existingID)
			prefix := decrypted[:12]
			loginLimiter.Reset(clientIP)
			h.auditLog("admin", "authentication.login", "user", 0, fmt.Sprintf("method=legacy_key ip=%s", clientIP))
			writeJSON(w, http.StatusOK, keyCreatedResponse{
				APIKey:    decrypted,
				KeyPrefix: prefix + "****",
			})
			return
		}
		slog.Warn("failed to decrypt existing admin key, creating new", "error", decErr)
	}

	raw, keyHash, keyPrefix, keyCiphertext := h.generateAdminKey(h.secret)
	_, err = h.db.Exec(ctx, `
		INSERT INTO api_keys (application_id, tenant_id, key_hash, key_prefix, key_ciphertext, owner_user, enabled, is_system, remark)
		VALUES ($1, 'default', $2, $3, $4, 'admin', TRUE, TRUE, 'admin login: no usable existing key')
	`, appID, keyHash, keyPrefix, keyCiphertext)
	if err != nil {
		slog.Error("failed to create admin key", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create key")
		return
	}

	writeJSON(w, http.StatusOK, keyCreatedResponse{
		APIKey:    raw,
		KeyPrefix: keyPrefix,
	})
}

func (h *Handler) generateAdminKey(secretKey string) (raw, hash, prefix, ciphertext string) {
	raw = fmt.Sprintf("sk-%s", randomAlphanum(48))
	hash = hashAPIKey(secretKey, raw)
	prefix = raw[:10] + "****"
	enc, err := h.encryptCred([]byte(raw))
	if err == nil {
		ciphertext = enc
	}
	return
}

// SuperAdminMiddleware wraps admin auth + role check (super_admin or admin_key only).
// tenant_admin requests get 403 Forbidden.
func SuperAdminMiddleware(next http.HandlerFunc, db *pgxpool.Pool, secretKey string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Try JWT first (no DB needed) — from Authorization Bearer or llmgw_session cookie
		tokenStr, ok := extractBearerOrCookieToken(r)
		if ok {
			claims, err := VerifyToken(tokenStr, secretKey)
			if err == nil && claims.UserID > 0 {
				if claims.Role != "super_admin" {
					writeError(w, http.StatusForbidden, "super_admin role required for this endpoint")
					return
				}
				// 2026-06-30 PR-4: super_admin must also satisfy the password
				// change requirement (rule 20 §6.2). Previously this was only
				// checked in AdminMiddleware; SuperAdminMiddleware skipped it,
				// allowing must_change_password=true super_admins to bypass the
				// gate via Authorization: Bearer <jwt>.
				if enforceMustChangePassword(w, r, claims) {
					return
				}
				authReq := SetAuthContext(r, &AuthContext{
					UserID:   claims.UserID,
					TenantID: claims.TenantID,
					Username: claims.Username,
					Role:     claims.Role,
					IsJWT:    true,
				})
				next(w, authReq)
				return
			}
		}

		// Fall back to legacy admin API key (DB lookup).
		// Restored after f88a96aa regression.
		// ── Fall back to legacy admin API key (DB lookup) ──────
		// DEPRECATED (rule 20 §10): scheduled for removal on 2026-07-27.
		if db != nil && verifyAdminAuth(r, db, secretKey) {
			slog.Warn("admin api key fallback deprecated, migrate to JWT (removal 2026-07-27)",
				"path", r.URL.Path, "remote", r.RemoteAddr)
			authReq := SetAuthContext(r, &AuthContext{
				TenantID: "default",
				Username: "admin",
				Role:     "admin_key",
				IsJWT:    false,
			})
			next(w, authReq)
			return
		}

		// No valid JWT and no valid API key → 401
		writeError(w, http.StatusUnauthorized, "authentication required")
	}
}

// clientIPFromRequest extracts the client IP, preferring X-Forwarded-For header.
func clientIPFromRequest(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		for i := 0; i < len(xff); i++ {
			if xff[i] == ',' {
				return xff[:i]
			}
		}
		return xff
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return xri
	}
	return r.RemoteAddr
}
