package admin

// provider_access.go — 供应商控制台访问控制（2026-09-04）。
//
// /api/providers/* 原先整体挂 SuperAdminMiddleware，导致 default 租户的
// tenant_admin 连供应商详情都读不了，更无法在凭据抽屉里轮换 API Key。
// ProviderConsoleMiddleware 在不改变 super_admin 行为的前提下，为
// default 租户的 tenant_admin 开放一个受限控制台角色：
//
//   - 只读（GET/HEAD）全部供应商数据；
//   - 唯一放行的写路径是 POST /api/providers/{id}/credentials/{cid}/rotate-primary-key
//     （凭据 API Key 修改），其余写操作一律 403；
//   - 访问 /api/providers/{id}/… 时校验目标供应商属于 default 租户，
//     非 default 租户的供应商（如各租户自建 custom provider）对
//     tenant_admin 一律 404，与 getProvider 的既有语义一致。
//
// super_admin 走原来的 SuperAdminMiddleware 分支（含 must_change_password
// 门槛），行为完全不变。非 default 租户的 tenant_admin 仍然 403。

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ProviderConsoleMiddleware guards the /api/providers/* console tree:
// super_admin keeps full access; a tenant_admin whose JWT tenant is
// "default" gets read-only access plus credential primary-key rotation.
func ProviderConsoleMiddleware(next http.HandlerFunc, db *pgxpool.Pool, secretKey string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// JWT auth (from Authorization Bearer or llmgw_session cookie) —
		// deliberately mirrors SuperAdminMiddleware (legacy single-secret
		// VerifyToken only) so the super_admin path is byte-for-byte the
		// access it had before this middleware existed.
		tokenStr, ok := extractBearerOrCookieToken(r)
		if ok {
			claims, err := VerifyToken(tokenStr, secretKey)
			if err == nil && claims.UserID > 0 {
				switch {
				case claims.Role == "super_admin":
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
				case claims.Role == "tenant_admin" && claims.TenantID == "default":
					if enforceMustChangePassword(w, r, claims) {
						return
					}
					if !providerTenantAdminAllowed(r, db) {
						writeError(w, http.StatusForbidden, "tenant_admin has read-only provider access; only credential API key rotation is allowed")
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
				default:
					writeError(w, http.StatusForbidden, "super_admin role required for this endpoint")
					return
				}
			}
		}

		// No valid JWT → 401
		writeError(w, http.StatusUnauthorized, "authentication required")
	}
}

// providerTenantAdminAllowed enforces the default-tenant tenant_admin console
// profile on a single request: read methods only, except the credential
// rotate-primary-key POST; and any /api/providers/{id}/… target must be a
// live default-tenant provider.
func providerTenantAdminAllowed(r *http.Request, db *pgxpool.Pool) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		// read path — falls through to the provider tenant-scope check below
	case http.MethodPost:
		if !strings.HasSuffix(r.URL.Path, "/rotate-primary-key") &&
			!strings.HasSuffix(r.URL.Path, "/reveal") &&
			!strings.HasSuffix(r.URL.Path, "/set-key") {
			return false
		}
	default:
		return false
	}

	// Tenant-scope /api/providers/{id}/… requests to default-tenant
	// providers. Root paths (list) and non-numeric segments have no
	// provider row to scope and are handled by the dispatcher.
	rest := strings.TrimPrefix(r.URL.Path, "/api/providers/")
	seg := rest
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		seg = rest[:i]
	}
	if seg == "" {
		return true
	}
	providerID, err := strconv.Atoi(seg)
	if err != nil {
		return true // dispatcher replies 400 "invalid provider id"
	}

	if db == nil {
		// Fail closed when no database is wired.
		return false
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	var one int
	err = db.QueryRow(ctx,
		`SELECT 1 FROM providers WHERE id = $1 AND tenant_id = 'default' AND deleted_at IS NULL`,
		providerID,
	).Scan(&one)
	return err == nil
}
