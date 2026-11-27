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
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"github.com/kaixuan/llm-gateway-go/pkg/authplugin"
	"github.com/kaixuan/llm-gateway-go/pkg/identity"
)

var errPasswordChangeRequired = errors.New("password change required before accessing other APIs")

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	// RememberMe 勾选「保持登录」时为 true：session cookie 带 Max-Age，
	// 浏览器重启后仍然有效（默认 7d，见 auth_params.go CookieMaxAge）。
	// false（不勾选）时下发**会话级** cookie——不带 Max-Age/Expires，
	// 浏览器会话结束即失效。对应前端 auth store 只写 sessionStorage。
	RememberMe bool `json:"remember_me"`
	// CaptchaID / CaptchaCode 来自 GET /api/auth/captcha。
	// 连续失败 CaptchaFailureThreshold 次后两者为必填（见 auth_captcha.go）。
	CaptchaID   string `json:"captcha_id"`
	CaptchaCode string `json:"captcha_code"`
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

// hashAPIKey hashes an API key using HMAC-SHA256 (used in keys.go for sk-* generation).
// Note: sk-* keys are no longer used for admin authentication (JWT-only since 2026-07-10),
// but are still generated for data-plane /v1/* API access.
func hashAPIKey(secretKey, rawKey string) string {
	mac := hmac.New(sha256.New, []byte(secretKey))
	mac.Write([]byte(rawKey))
	return hex.EncodeToString(mac.Sum(nil))
}

// handleLogout clears the session cookie (rule 20 §6.1).
func (h *Handler) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	clearSessionCookie(w, r)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// legacyAdapter returns an identity.LegacyVerifier that delegates to
// admin.VerifyToken. Bound to a specific secretKey so the verifier doesn't
// have to plumb the secret through every call.
func legacyAdapter(secretKey string) identity.LegacyVerifier {
	return legacyVerifierAdapter{secretKey: secretKey}
}

type legacyVerifierAdapter struct {
	secretKey string
}

func (a legacyVerifierAdapter) VerifyLegacy(tok string) (*identity.LegacyClaims, error) {
	return VerifyLegacy(tok, a.secretKey)
}

func AdminMiddleware(next http.HandlerFunc, db *pgxpool.Pool, secretKey string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Browser EventSource cannot set Authorization headers, so we
		// accept the bearer token via ?token= query param as well.
		// Promote it to Authorization header when no Bearer is set.
		// This is the only middleware that accepts query-string tokens;
		// the rest stay Bearer-only. (rule 20 §6 + v2 ws_relay pattern)
		if r.Header.Get("Authorization") == "" {
			if t := strings.TrimSpace(r.URL.Query().Get("token")); t != "" {
				r.Header.Set("Authorization", "Bearer "+t)
			}
		}

		// ── JWT auth (Bearer header or session cookie) ──
		if tokenStr, ok := extractBearerOrCookieToken(r); ok {
			// Normalize both shared and canonical local JWTs once. Verify
			// already handles the legacy verifier and authorization policy;
			// retrying VerifyToken here would bypass a policy rejection.
			p, err := identity.Verify(tokenStr, legacyAdapter(secretKey))
			if err == nil && p != nil && p.Source == "multi_issuer" {
				p, err = resolveSharedPrincipal(r.Context(), db, p)
			}
			if err == nil && p != nil && p.UserID > 0 {
				// B4 (2026-09-22): a token issued before the user's last
				// password change is revoked. Only local (legacy) tokens —
				// cross-project principals have foreign user_id semantics.
				if p.Source == "legacy" && authRevocationProbe(r.Context(), db, p.UserID, p.IssuedAt) {
					writeError(w, http.StatusUnauthorized, "session revoked: password changed")
					return
				}
				if p.MustChangePassword {
					if !isPasswordChangeAllowedPath(r.URL.Path) {
						writeError(w, http.StatusForbidden, errPasswordChangeRequired.Error())
						return
					}
				}
				authReq := SetAuthContext(r, &AuthContext{
					UserID:             p.UserID,
					TenantID:           p.TenantID,
					Username:           p.Username,
					Role:               p.Role,
					Roles:              p.Roles, // P1-b：多角色有效集透传（Principal）
					IsJWT:              true,
					MustChangePassword: p.MustChangePassword,
					Issuer:             p.Issuer,
					Subject:            p.Subject,
					CanonicalUserID:    p.CanonicalUserID,
				})
				next(w, authReq)
				return
			}
		}

		// No valid JWT → 401
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

// isLocallyIssuedToken 判断 provider 的 AuthResult.Token 是否本地 HS256 JWT。
//
// native provider 走 admin.SignToken 同一条签发链（LLM_GATEWAY_JWT_SECRET →
// secretKey），能过 AdminMiddleware → identity.Verify 的 legacy 路径。
// casdoor（以及未来任何外部 IdP）返回的是外部签发 token，必须换发。
// 新 provider 接入时默认按「外部签发」处理（白名单 = 本地签发者集合）。
// effectiveLoginRoles 计算登录签发用有效角色集（P1-b，2026-11-27）。
//
// roles 列非空 ⇒ 主角色必须在内（不在则前置，保证 Roles[0]=主角色契约）；
// 空/NULL ⇒ 回落 [role] 单值（存量用户逐字节同形）。
func effectiveLoginRoles(primary string, roles []string) []string {
	if len(roles) == 0 {
		if primary == "" {
			return nil
		}
		return []string{primary}
	}
	// 主角色前置 + 去重保序（normalizeUserRoles 同一语义，签发侧镜像实现——
	// 两处必须同序，否则「库里存的顺序」与「token 里的顺序」漂移）。
	seen := make(map[string]bool, len(roles)+1)
	out := make([]string, 0, len(roles)+1)
	if primary != "" {
		out = append(out, primary)
		seen[primary] = true
	}
	for _, r := range roles {
		if r == "" || seen[r] {
			continue
		}
		out = append(out, r)
		seen[r] = true
	}
	return out
}

func isLocallyIssuedToken(providerName string) bool {
	switch providerName {
	case "native":
		return true
	default:
		return false
	}
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

// authDB返回 handleLogin 的读写面：显式注入的窄口优先，否则回落到
// 具体连接池 h.db。单测可注入 pgxmock（*pgxpool.Pool 无法承载它）。
// 注意：h.db 是具体指针，typed-nil装进接口后 != nil（Go 经典坑），
// 所以 nil判定必须在具体类型上做，不能在返回的接口值上做。
func (h *Handler) authDB() loginDB {
	if h.loginDB != nil {
		return h.loginDB
	}
	if h.db == nil {
		return nil
	}
	return h.db
}

// handleLogin handles user authentication via multiple paths:
// 1. Plugin-based authentication (registry.GetAuthProvider) - Phase 2 refactored path
// 2. Direct database authentication (legacy fallback when registry unavailable)
// 3. Legacy admin key authentication (env LLM_GATEWAY_ADMIN_PASSWORD)
//
// Phase 2 Step 2.3: Refactored to use AuthProvider interface while preserving all
// existing behavior: rate limiting, captcha verification, audit logging, identity
// shadow mapping, cookie handling, and the 2026-07-09 no-fallback policy.
func (h *Handler) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.authDB() == nil { //窄口注入（单测pgxmock）或真实连接池，二者其一非nil即可
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

	// ── 验证码闸门（2026-10-10）─────────────────────────────────────
	// 刻意放在任何 DB 查询与 bcrypt 之前：验证码的判据是纯内存比较，
	// 放这里可以让无效流量在碰数据库之前就被挡掉（bcrypt 一次约 100ms，
	// 撞库者最想要的就是把这笔 CPU 账单转嫁给网关）。
	//
	// 未达失败阈值时验证码可选（自适应），达到则强制。失败**不**计入
	// 连续失败计数——否则反复输错验证码就能把阈值刷爆，让正常用户
	// 永久被强制验证（自我 DoS）。
	if !h.verifyLoginCaptcha(w, clientIP, req.CaptchaID, req.CaptchaCode) {
		return
	}

	// ── Try JWT auth (users table) via AuthProvider ───────────────────
	// Phase 2 Step 2.3: Use registry-based AuthProvider when available,
	// preserving all existing behavior (audit logging, rate limiting,
	// identity shadow mapping, legacy admin fallback).
	if req.Username != "" && req.Password != "" {
		// Try to get AuthProvider from registry (may be nil if registry not initialized)
		var authProvider authplugin.AuthProvider
		if h.registry != nil {
			var err error
			authProvider, err = h.registry.GetAuthProvider()
			if err != nil {
				slog.Warn("handleLogin: registry GetAuthProvider failed, falling back to direct DB", "error", err)
				authProvider = nil
			}
		}

		if authProvider != nil {
			// ── Plugin-based authentication path (Phase 2 refactored) ──
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			defer cancel()

			authReq := &authplugin.AuthRequest{
				Username:    req.Username,
				Password:    req.Password,
				RememberMe:  req.RememberMe,
				CaptchaID:   req.CaptchaID,
				CaptchaCode: req.CaptchaCode,
				IPAddress:   clientIP,
				UserAgent:   r.UserAgent(),
			}

			result, authErr := authProvider.Authenticate(ctx, authReq)

			// Handle authentication errors with proper audit logging
			if authErr != nil {
				// Check if user exists in database to determine if we should fall back to legacy admin
				var userExists bool
				checkErr := h.authDB().QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE username = $1)`, req.Username).Scan(&userExists)

				if checkErr == nil && userExists {
					// User exists in DB, don't fall back to legacy admin (2026-07-09 policy)
					var userID int
					_ = h.authDB().QueryRow(ctx, `SELECT id FROM users WHERE username = $1`, req.Username).Scan(&userID)

					switch {
					case errors.Is(authErr, authplugin.ErrUserDisabled):
						h.auditLog(req.Username, "authentication.login_failed", "user", userID, fmt.Sprintf("method=plugin reason=user_disabled ip=%s", clientIP))
						recordLoginFailure(clientIP)
						writeError(w, http.StatusForbidden, "User account is disabled")
						return
					case errors.Is(authErr, authplugin.ErrTenantDisabled):
						h.auditLog(req.Username, "authentication.login_failed", "user", userID, fmt.Sprintf("method=plugin reason=tenant_disabled ip=%s", clientIP))
						recordLoginFailure(clientIP)
						writeError(w, http.StatusForbidden, "tenant is disabled, contact your administrator")
						return
					case errors.Is(authErr, authplugin.ErrInvalidCredentials):
						h.auditLog(req.Username, "authentication.login_failed", "user", userID, fmt.Sprintf("method=plugin reason=invalid_password ip=%s", clientIP))
						recordLoginFailure(clientIP)
						writeError(w, http.StatusUnauthorized, "Invalid credentials: password does not match the users table record (env admin password is not used when this username exists)")
						return
					default:
						h.auditLog(req.Username, "authentication.login_failed", "user", userID, fmt.Sprintf("method=plugin reason=auth_error ip=%s error=%v", clientIP, authErr))
						recordLoginFailure(clientIP)
						writeError(w, http.StatusUnauthorized, "Authentication failed")
						return
					}
				}
				// User doesn't exist, continue to legacy admin fallback
			} else if result != nil && result.Success {
				// ── Successful authentication via plugin ──

				// Shadow mapping (provider=llm-gateway, subject=userID) into
				// identity_shadow — used by cross-project trust to resolve
				// canonical identities. DSN 未配置时 RecordShadow 内部 noop。
				identity.RecordShadow("llm-gateway", result.UserID, result.TenantID, result.DisplayName, result.Email)

				h.auditLog(result.Username, "authentication.login", "user", result.UserID, fmt.Sprintf("method=plugin provider=%s role=%s tenant=%s ip=%s", authProvider.Name(), result.Role, result.TenantID, clientIP))

				// Reset rate limiter and clear failures (2026-10-17 fix)
				loginLimiter.Reset(clientIP)
				clearLoginFailures(clientIP)

				// ★ 2026-11-27 L135 轮（断点①修复）：provider 下发的 token 只有
				//   native provider 的才是本地 HS256 JWT（能过 AdminMiddleware →
				//   identity.Verify）。casdoor provider 返回的是 casdoor 证书签名
				//   JWT —— issuer 白名单与验签通道都不含它，直接下发 ⇒ 下一个请求
				//   即 401（登录成功但会话保持断链）。
				//   ⇒ 非本地签发一律**换发**本地 JWT：admin.SignToken(同一把
				//   jwtSecret 链: LLM_GATEWAY_JWT_SECRET → cfg.SecretKey)，claims
				//   取自 provider 的 AuthResult。provider token 只作登录凭证，
				//   不出网关。
				sessionToken, expiresAt := result.Token, result.ExpiresAt
				tokenSource := "provider"
				if !isLocallyIssuedToken(authProvider.Name()) {
					// ★ 2026-11-28 审计轮：换发改 SignTokenWithRoles——外部 IdP /
					//   plugin 结果若带 Roles（native 现在带；其它 provider nil 回落
					//   [Role]），换发不得把多角色降级成单角色 token。
					local, exp, signErr := SignTokenWithRoles(result.UserID, result.TenantID, result.Username, result.Role, result.Roles, h.secret, result.MustChangePassword)
					if signErr != nil {
						slog.Error("handleLogin: re-sign plugin token failed", "provider", authProvider.Name(), "error", signErr)
						writeError(w, http.StatusInternalServerError, "token generation failed")
						return
					}
					sessionToken, expiresAt = local, exp
					tokenSource = "re-signed"
				}

				// Dual-write (rule 20 §6.1): set HttpOnly cookie AND return access_token。
				// remember_me=false → 会话级 cookie（无 Max-Age/Expires，关掉即失效）。
				setSessionCookieRemember(w, r, sessionToken, expiresAt, req.RememberMe)

				writeJSON(w, http.StatusOK, map[string]any{
					"access_token": sessionToken,
					"token_type":   result.TokenType,
					"expires_at":   expiresAt.Format(time.RFC3339),
					"token_source": tokenSource,
					"user": map[string]any{
						"id":                   result.UserID,
						"tenant_id":            result.TenantID,
						"username":             result.Username,
						"display_name":         result.DisplayName,
						"email":                result.Email,
						"role":                 result.Role,
						"roles":                effectiveLoginRoles(result.Role, result.Roles),
						"enabled":              result.Enabled,
						"must_change_password": result.MustChangePassword,
					},
				})
				return
			}
		} else {
			// ── Legacy direct DB authentication path (fallback when registry not available) ──
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
				Roles              []string
				Enabled            bool
				MustChangePassword bool
			}
			// roles（P1-b，2026-11-27）：多角色数组列；NULL → 空切片 ⇒
			// 签发侧回落 [Role]（存量兼容）。
			err := h.authDB().QueryRow(ctx, `
				SELECT id, tenant_id, username, password_hash, display_name, email, role,
				       COALESCE(roles, '{}'), enabled, must_change_password
				FROM users WHERE username = $1
			`, req.Username).Scan(&u.ID, &u.TenantID, &u.Username, &u.PasswordHash, &u.DisplayName, &u.Email, &u.Role, &u.Roles, &u.Enabled, &u.MustChangePassword)

			// 2026-07-09: 如果在users表中找到用户，无论密码是否正确，都不再fallback到legacy admin
			if err == nil {
				// 用户存在，检查是否启用
				if !u.Enabled {
					h.auditLog(req.Username, "authentication.login_failed", "user", u.ID, fmt.Sprintf("method=jwt reason=user_disabled ip=%s", clientIP))
					recordLoginFailure(clientIP)
					writeError(w, http.StatusForbidden, "User account is disabled")
					return
				}
				// 验证密码
				if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(req.Password)) != nil {
					// 密码错误 — users 表存在则不回退 env LLM_GATEWAY_ADMIN_PASSWORD
					h.auditLog(req.Username, "authentication.login_failed", "user", u.ID, fmt.Sprintf("method=jwt reason=invalid_password ip=%s", clientIP))
					recordLoginFailure(clientIP)
					writeError(w, http.StatusUnauthorized, "Invalid credentials: password does not match the users table record (env admin password is not used when this username exists)")
					return
				}
				// 密码正确，检查租户状态
				if u.TenantID != "default" {
					var tenantStatus string
					err := h.authDB().QueryRow(ctx, `SELECT status FROM tenants WHERE code = $1`, u.TenantID).Scan(&tenantStatus)
					if err != nil || tenantStatus == "disabled" {
						h.auditLog(req.Username, "authentication.login_failed", "user", u.ID, fmt.Sprintf("method=jwt reason=tenant_disabled tenant=%s ip=%s", u.TenantID, clientIP))
						recordLoginFailure(clientIP)
						writeError(w, http.StatusForbidden, "tenant is disabled, contact your administrator")
						return
					}
				}
				// Update last_login_at
				//nolint:errcheck // best-effort exec, non-critical
				h.authDB().Exec(ctx, `UPDATE users SET last_login_at = now() WHERE id = $1`, u.ID)

				token, expiresAt, signErr := SignTokenWithRoles(u.ID, u.TenantID, u.Username, u.Role, effectiveLoginRoles(u.Role, u.Roles), h.secret, u.MustChangePassword)
				if signErr != nil {
					slog.Error("handleLogin: sign jwt failed", "error", signErr)
					writeError(w, http.StatusInternalServerError, "token generation failed")
					return
				}

				// Shadow mapping (provider=llm-gateway, subject=userID) into
				// identity_shadow — used by cross-project trust to resolve
				// canonical identities. DSN 未配置时 RecordShadow 内部 noop。
				identity.RecordShadow("llm-gateway", u.ID, u.TenantID, u.DisplayName, u.Email)

				h.auditLog(u.Username, "authentication.login", "user", u.ID, fmt.Sprintf("method=jwt role=%s tenant=%s ip=%s", u.Role, u.TenantID, clientIP))
				// 成功登录必须 Reset 限流桶：Allow() 在入口已计数，不 Reset 则
				// 5 次成功登录后同一 IP 整分钟 429（legacy key 路径早已 Reset，
				// JWT 路径漏了——2026-10-17 llm.itestu.cn 无法登录根因）。
				loginLimiter.Reset(clientIP)
				clearLoginFailures(clientIP)
				// Dual-write (rule 20 §6.1): set HttpOnly cookie AND return access_token。
				// remember_me=false → 会话级 cookie（无 Max-Age/Expires，关掉即失效）。
				setSessionCookieRemember(w, r, token, expiresAt, req.RememberMe)
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
						"roles":                effectiveLoginRoles(u.Role, u.Roles),
						"enabled":              u.Enabled,
						"must_change_password": u.MustChangePassword,
					},
				})
				return
			}
			// 用户不存在于users表，继续尝试legacy admin认证
		}
	}

	// ── Fall back to legacy admin key auth ────────────────────────────
	// This path is preserved exactly as-is for backward compatibility
	if subtle.ConstantTimeCompare([]byte(req.Username), []byte("admin")) != 1 ||
		subtle.ConstantTimeCompare([]byte(req.Password), []byte(getAdminPassword())) != 1 {
		recordLoginFailure(clientIP)
		writeError(w, http.StatusUnauthorized, "Invalid credentials")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var appID int
	err := h.authDB().QueryRow(ctx, `SELECT id FROM applications WHERE code = 'admin' AND tenant_id = 'default'`).Scan(&appID)
	if err != nil {
		slog.Error("admin app not found", "error", err)
		writeError(w, http.StatusInternalServerError, "Admin application not initialized")
		return
	}

	var existingID int
	var ciphertext string
	err = h.authDB().QueryRow(ctx, `
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
			h.authDB().Exec(ctx, `UPDATE api_keys SET is_system = TRUE, remark = 'admin login: reused existing key' WHERE id = $1 AND (remark IS NULL OR remark = '')`, existingID)
			prefix := decrypted[:12]
			loginLimiter.Reset(clientIP)
			clearLoginFailures(clientIP)
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
	clearLoginFailures(clientIP)
	_, err = h.authDB().Exec(ctx, `
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

// FormatAnomalyTenantAdminMiddleware guards /api/admin/format-anomalies*:
// super_admin (and admin_key) keeps full access; tenant_admin gets
// read-only access to the format anomaly tables via RLS policy isolation
// (set app.current_tenant on the connection). The /resolve subroute is
// super_admin-only — the subrouter enforces this gate before the SQL
// runs (see handleFormatAnomalySubrouter).
//
// (2026-10-16, 批 157) — modelled after ProviderConsoleMiddleware
// (admin/provider_access.go). Authorization premise: the bypass branch
// is reached only when tenantID == "", which only happens for
// super_admin / admin_key (handler 入口). tenant_admin always lands in
// the second case, where EffectiveTenantIDAll returns the caller's own
// tenantID, satisfying withTenantOrAllReadOnlyTx's "bypass only on
// empty tenantID" constraint.
//
// ★ 2026-11-27 非管理员菜单重构轮：普通 role=user 也放行读（菜单本轮对
//   非管理员放开「格式异常监控」）。数据面安全性由既有租户收窄承载：
//   EffectiveTenantIDAll 对 user 走 GetTenantID 分支前的判定不足——
//   本轮在 formatAnomalyTenantID 收口（user → 本租户，绝不返回空串 =
//   绝不 bypass RLS）；resolve 写端点保持 super_admin-only。
//   （原实现直接 403 拒 user——菜单放开后按三档口径放宽读、收数据。）
func FormatAnomalyTenantAdminMiddleware(next http.HandlerFunc, db *pgxpool.Pool, secretKey string) http.HandlerFunc {
	return formatAnomalyOrSuperAdmin(next, db, secretKey)
}

// ModelIntegrityTenantAdminMiddleware guards /api/admin/model-integrity*:
// same shape as FormatAnomalyTenantAdminMiddleware; /resolve is
// super_admin-only, enforced in handleModelIntegrity.
func ModelIntegrityTenantAdminMiddleware(next http.HandlerFunc, db *pgxpool.Pool, secretKey string) http.HandlerFunc {
	return tenantAdminOrSuperAdmin(next, db, secretKey, "model-integrity")
}

// tenantAdminOrSuperAdmin is the shared three-case dispatch (super_admin
// / tenant_admin / 其它 403). The label is purely for error messages.
func tenantAdminOrSuperAdmin(next http.HandlerFunc, db *pgxpool.Pool, secretKey, label string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Mirror SuperAdminMiddleware auth surface so the super_admin path
		// is byte-for-byte the access it had before this middleware existed.
		tokenStr, ok := extractBearerOrCookieToken(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		claims, err := VerifyToken(tokenStr, secretKey)
		if err != nil || claims.UserID <= 0 {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		issuedAt := time.Time{}
		if claims.IssuedAt != nil {
			issuedAt = claims.IssuedAt.Time
		}
		if authRevocationProbe(r.Context(), db, claims.UserID, issuedAt) {
			writeError(w, http.StatusUnauthorized, "session revoked: password changed")
			return
		}
		if enforceMustChangePassword(w, r, claims) {
			return
		}
		switch {
		case claims.Role == "super_admin" || claims.Role == "admin_key":
			authReq := SetAuthContext(r, &AuthContext{
				UserID:   claims.UserID,
				TenantID: claims.TenantID,
				Username: claims.Username,
				Role:     claims.Role,
				Roles:    claims.EffectiveRoles(), // P1-b：多角色有效集透传
				IsJWT:    true,
			})
			next(w, authReq)
			return
		case claims.Role == "tenant_admin":
			if claims.TenantID == "" {
				// tenant_admin without a tenant binding cannot be safely
				// isolated — refuse rather than risk bypass.
				writeError(w, http.StatusForbidden, "tenant_admin must have a tenant binding to access "+label)
				return
			}
			authReq := SetAuthContext(r, &AuthContext{
				UserID:   claims.UserID,
				TenantID: claims.TenantID,
				Username: claims.Username,
				Role:     claims.Role,
				Roles:    claims.EffectiveRoles(), // P1-b：多角色有效集透传
				IsJWT:    true,
			})
			next(w, authReq)
			return
		default:
			writeError(w, http.StatusForbidden, "super_admin or tenant_admin role required for this endpoint")
			return
		}
	}
}

// formatAnomalyOrSuperAdmin 是 tenantAdminOrSuperAdmin 的四档变体
//（2026-11-27 非管理员菜单重构轮）：前三档（super_admin / admin_key /
// tenant_admin）与共享实现逐字同链（认证 → 撤销探针 → 改密拦截）；
// 第四档放行**普通 role=user 的读**——「格式异常监控」菜单本轮对非管理员
// 放开（owner 指令），数据面由 formatAnomalyTenantID 收进本租户
//（绝不返回空串 ⇒ 绝不 bypass RLS）；写端点（resolve）在
// handleFormatAnomalySubrouter 内另有 super 闸，与本中间件无关。
func formatAnomalyOrSuperAdmin(next http.HandlerFunc, db *pgxpool.Pool, secretKey string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tokenStr, ok := extractBearerOrCookieToken(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		claims, err := VerifyToken(tokenStr, secretKey)
		if err != nil || claims.UserID <= 0 {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		issuedAt := time.Time{}
		if claims.IssuedAt != nil {
			issuedAt = claims.IssuedAt.Time
		}
		if authRevocationProbe(r.Context(), db, claims.UserID, issuedAt) {
			writeError(w, http.StatusUnauthorized, "session revoked: password changed")
			return
		}
		if enforceMustChangePassword(w, r, claims) {
			return
		}
		switch {
		case claims.Role == "super_admin" || claims.Role == "admin_key" || claims.Role == "tenant_admin":
			if claims.Role == "tenant_admin" && claims.TenantID == "" {
				// 同 tenantAdminOrSuperAdmin：无租户绑定的 tenant_admin
				// 无法安全隔离 ⇒ 拒绝而非冒 bypass 风险。
				writeError(w, http.StatusForbidden, "tenant_admin must have a tenant binding to access format-anomalies")
				return
			}
			authReq := SetAuthContext(r, &AuthContext{
				UserID:   claims.UserID,
				TenantID: claims.TenantID,
				Username: claims.Username,
				Role:     claims.Role,
				Roles:    claims.EffectiveRoles(), // P1-b：多角色有效集透传
				IsJWT:    true,
			})
			next(w, authReq)
			return
		default:
			// 普通 role=user：只放行 GET（读）。写端点族后续由 handler 侧
			// method 判定兜底（format-anomalies 读 handler 只接受 GET；
			// resolve 是 POST 且另有 super 闸）。
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				writeError(w, http.StatusForbidden, "write operations require super_admin")
				return
			}
			if claims.TenantID == "" {
				// user 档必须有租户绑定：无租户 ⇒ 无法收窄 ⇒ 拒绝
				//（否则 formatAnomalyTenantID 会退化为空串 = bypass RLS）。
				writeError(w, http.StatusForbidden, "user must have a tenant binding to access format-anomalies")
				return
			}
			authReq := SetAuthContext(r, &AuthContext{
				UserID:   claims.UserID,
				TenantID: claims.TenantID,
				Username: claims.Username,
				Role:     claims.Role,
				Roles:    claims.EffectiveRoles(), // P1-b：多角色有效集透传
				IsJWT:    true,
			})
			next(w, authReq)
			return
		}
	}
}

// modelCatalogReadGate 是「模型与目录」页非管理员读面的统一闸
//（2026-11-27 非管理员菜单重构轮第二轮）：
//
//	super_admin / admin_key / tenant_admin → 全放行（GET/HEAD；写面在
//	  handler 侧由 RequireSuperAdminForWrite 兜底，ta 无租户绑定拒）
//	role=user → 仅 GET/HEAD 放行且必须有租户绑定（写 403）；
//	  其他角色（channel 等）→ 403 维持旧口径
//
// ★ 为什么需要它：「模型与目录」页（owner 指令对非管理员放开）调用四个
//   元数据端点，其中 /api/catalog、/api/routing/featured、/api/tags 原先
//   挂 SuperAdminMiddleware —— ta 落在该页默认「提供商目录」页签即 403
//   （真机实证：GET /api/catalog → 403 "super_admin role required"）。
//   三个端点均为**纯读元数据**（供应商目录模板 / 精选模型列表 / 标签
//   命名空间），不含凭据、key、租户敏感数据；featured 的 PATCH（写）由
//   本闸 method 判定拦下（仅 super 有写权），catalog/tags 的写方法由
//   handler 侧判定兜底（handleCatalog/handleTags 只分 GET，其余
//   405/404；handleCatalogRoot 纯 GET）。数据面收窄沿用各 handler 既有
//   口径：catalog 全量公开目录、featured 是 default 租户的路由策略
//   配置、tags 是模型标签聚合（均为「模型与目录」页展示所需的最小面）。
func modelCatalogReadGate(next http.HandlerFunc, db *pgxpool.Pool, secretKey string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tokenStr, ok := extractBearerOrCookieToken(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		claims, err := VerifyToken(tokenStr, secretKey)
		if err != nil || claims.UserID <= 0 {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		issuedAt := time.Time{}
		if claims.IssuedAt != nil {
			issuedAt = claims.IssuedAt.Time
		}
		if authRevocationProbe(r.Context(), db, claims.UserID, issuedAt) {
			writeError(w, http.StatusUnauthorized, "session revoked: password changed")
			return
		}
		if enforceMustChangePassword(w, r, claims) {
			return
		}
		switch {
		case claims.Role == "super_admin" || claims.Role == "admin_key" || claims.Role == "tenant_admin":
			if claims.Role == "tenant_admin" && claims.TenantID == "" {
				writeError(w, http.StatusForbidden, "tenant_admin must have a tenant binding to access model catalog")
				return
			}
			authReq := SetAuthContext(r, &AuthContext{
				UserID:   claims.UserID,
				TenantID: claims.TenantID,
				Username: claims.Username,
				Role:     claims.Role,
				Roles:    claims.EffectiveRoles(), // P1-b：多角色有效集透传
				IsJWT:    true,
			})
			next(w, authReq)
			return
		case claims.Role == "user":
			// 普通 role=user：仅 GET/HEAD（读）。
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				writeError(w, http.StatusForbidden, "write operations require super_admin")
				return
			}
			if claims.TenantID == "" {
				// user 档必须有租户绑定（与 formatAnomalyOrSuperAdmin 同判据）。
				writeError(w, http.StatusForbidden, "user must have a tenant binding to access model catalog")
				return
			}
			authReq := SetAuthContext(r, &AuthContext{
				UserID:   claims.UserID,
				TenantID: claims.TenantID,
				Username: claims.Username,
				Role:     claims.Role,
				Roles:    claims.EffectiveRoles(), // P1-b：多角色有效集透传
				IsJWT:    true,
			})
			next(w, authReq)
			return
		default:
			writeError(w, http.StatusForbidden, "super_admin role required for this endpoint")
			return
		}
	}
}

// SuperAdminMiddleware wraps admin auth + role check (super_admin only).
// tenant_admin requests get 403 Forbidden.
func SuperAdminMiddleware(next http.HandlerFunc, db *pgxpool.Pool, secretKey string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// JWT auth (from Authorization Bearer or llmgw_session cookie)
		tokenStr, ok := extractBearerOrCookieToken(r)
		if ok {
			claims, err := VerifyToken(tokenStr, secretKey)
			if err == nil && claims.UserID > 0 {
				if claims.Role != "super_admin" {
					writeError(w, http.StatusForbidden, "super_admin role required for this endpoint")
					return
				}
				// B4 (2026-09-22): password-change revocation, same gate as
				// AdminMiddleware.
				issuedAt := time.Time{}
				if claims.IssuedAt != nil {
					issuedAt = claims.IssuedAt.Time
				}
				if authRevocationProbe(r.Context(), db, claims.UserID, issuedAt) {
					writeError(w, http.StatusUnauthorized, "session revoked: password changed")
					return
				}
				if enforceMustChangePassword(w, r, claims) {
					return
				}
				authReq := SetAuthContext(r, &AuthContext{
					UserID:   claims.UserID,
					TenantID: claims.TenantID,
					Username: claims.Username,
					Role:     claims.Role,
					Roles:    claims.EffectiveRoles(), // P1-b：多角色有效集透传
					IsJWT:    true,
				})
				next(w, authReq)
				return
			}
		}

		// No valid JWT → 401
		writeError(w, http.StatusUnauthorized, "authentication required")
	}
}

// clientIPFromRequest extracts the client IP for login rate-limit / captcha.
// Order: X-Forwarded-For (left-most) → X-Real-IP → RemoteAddr host.
// Strips :port so "192.168.1.2:54321" and "192.168.1.2" share one bucket.
func clientIPFromRequest(r *http.Request) string {
	if xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); xff != "" {
		if i := strings.IndexByte(xff, ','); i >= 0 {
			xff = strings.TrimSpace(xff[:i])
		}
		if xff != "" {
			return stripHostPort(xff)
		}
	}
	if xri := strings.TrimSpace(r.Header.Get("X-Real-IP")); xri != "" {
		return stripHostPort(xri)
	}
	return stripHostPort(r.RemoteAddr)
}

func stripHostPort(hostport string) string {
	hostport = strings.TrimSpace(hostport)
	if hostport == "" {
		return hostport
	}
	// net.SplitHostPort needs brackets for IPv6; fall back to raw on error.
	if h, _, err := net.SplitHostPort(hostport); err == nil && h != "" {
		return h
	}
	return hostport
}
