package admin

import (
	"context"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// tenantLogsClause returns an SQL AND-fragment filtering request_logs by tenant_id
// for tenant_admin callers on non-default tenants. Super-admin, legacy admin_key,
// and any user on the default tenant see all tenants (no isolation).
// argStart is the next $N placeholder index.
func tenantLogsClause(r *http.Request, argStart int) (fragment string, args []any, nextArg int) {
	if r == nil || !IsTenantAdmin(r) {
		return "", nil, argStart
	}
	tenantID := GetTenantID(r)
	if tenantID == "" || tenantID == "default" {
		return "", nil, argStart
	}
	fragment = fmt.Sprintf(" AND tenant_id = $%d", argStart)
	return fragment, []any{tenantID}, argStart + 1
}

// requireSessionTaskAccess returns false and writes 404 when a scoped tenant_admin
// tries to access a task_id that does not belong to their tenant.
func requireSessionTaskAccess(w http.ResponseWriter, r *http.Request, ctx context.Context, db *pgxpool.Pool, taskID string) bool {
	if r == nil || !IsTenantAdmin(r) {
		return true
	}
	tenantID := GetTenantID(r)
	if tenantID == "" || tenantID == "default" {
		return true
	}
	if assertTaskInTenant(ctx, db, taskID, tenantID) {
		return true
	}
	writeError(w, http.StatusNotFound, "task not found: "+taskID)
	return false
}

// assertTaskInTenant verifies that taskID has at least one request_log row
// belonging to tenantID. Used to block cross-tenant session detail access.
func assertTaskInTenant(ctx context.Context, db *pgxpool.Pool, taskID, tenantID string) bool {
	if db == nil || taskID == "" || tenantID == "" {
		return false
	}
	var exists bool
	err := db.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM request_logs
			WHERE gw_task_id = $1 AND tenant_id = $2
			LIMIT 1
		)
	`, taskID, tenantID).Scan(&exists)
	return err == nil && exists
}

// ============================================================
// Owner scope (2026-07-07 会话归属建模)
//
// 三层可见性：
//   - super_admin / admin_key → 跨租户、全部 owner（不过滤）
//   - tenant_admin            → 本租户全部 owner（仅 tenant_id 过滤）
//   - 其他角色（普通 user）    → 本租户 + 仅自己名下（tenant_id + owner_user）
//
// 与 tenantLogsClause 并存：后者负责 tenant_id 过滤，本组 helper 在此之上
// 叠加 owner_user 过滤，用于 session_dim / session_owners 等带 owner_user
// 列的会话级表。session_summaries 无 owner 列时，通过 JOIN session_dim 间接过滤。
// ============================================================

// IsRegularUser returns true for an authenticated non-admin user (role other
// than super_admin / admin_key / tenant_admin). Such callers are scoped to
// their own owner_user within their tenant.
func IsRegularUser(r *http.Request) bool {
	auth := GetAuthContext(r)
	if auth == nil {
		return false
	}
	switch auth.Role {
	case "super_admin", "admin_key", "tenant_admin":
		return false
	}
	return auth.IsJWT
}

// effectiveScopeTenant returns the tenant_id to filter by for session-scope
// queries, covering three tiers:
//   - super_admin / admin_key → "" (all tenants)
//   - tenant_admin / regular user → their own tenant_id
//
// Unlike EffectiveTenantIDAll (which returns "" for any non-tenant_admin,
// leaking all tenants to regular users), this forces regular users onto
// their own tenant so the owner_user filter has a tenant boundary.
func effectiveScopeTenant(r *http.Request) string {
	if IsSuperAdminOrLegacy(r) {
		return ""
	}
	return GetTenantID(r)
}

// ownerScopeClause returns an SQL AND-fragment filtering by the owner_user
// column for regular (non-admin) users. Admin tiers see no owner filter.
// col is the qualified column name to filter on (e.g. "owner_user" or
// "sd.owner_user"); it is injected verbatim so must come from trusted code.
// argStart is the next $N placeholder index.
func ownerScopeClause(r *http.Request, col string, argStart int) (fragment string, args []any, nextArg int) {
	if !IsRegularUser(r) {
		return "", nil, argStart
	}
	owner := GetAuthContext(r).Username
	fragment = fmt.Sprintf(" AND %s = $%d", col, argStart)
	if owner == "" {
		// No username to scope by — deny by emitting an unsatisfiable filter
		// rather than leaking cross-owner data.
		return fragment, []any{""}, argStart + 1
	}
	return fragment, []any{owner}, argStart + 1
}

// requireSessionOwnerAccess returns false and writes 404 when a regular user
// tries to access a session whose primary owner_user is not theirs. Admin
// tiers (super_admin/admin_key/tenant_admin) always pass. The check runs
// inside a read-only tenant transaction so RLS on session_dim also applies.
func requireSessionOwnerAccess(w http.ResponseWriter, r *http.Request, ctx context.Context, pool *pgxpool.Pool, gwSessionID string) bool {
	if !IsRegularUser(r) || gwSessionID == "" {
		return true
	}
	tenantID := GetTenantID(r)
	owner := GetAuthContext(r).Username
	if owner == "" {
		writeError(w, http.StatusNotFound, "session not found: "+gwSessionID)
		return false
	}
	var ok bool
	err := withTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM session_dim
				WHERE gw_session_id = $1
				  AND tenant_id = $2
				  AND owner_user = $3
				LIMIT 1
			)
		`, gwSessionID, tenantID, owner).Scan(&ok)
	})
	if err != nil || !ok {
		writeError(w, http.StatusNotFound, "session not found: "+gwSessionID)
		return false
	}
	return true
}
