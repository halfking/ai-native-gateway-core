package admin

import (
	"context"
	"fmt"
	"net/http"

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
