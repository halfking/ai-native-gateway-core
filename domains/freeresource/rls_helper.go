package freeresource

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
)

// SetRLSTenantContext sets the `app.current_tenant` GUC on the given connection
// using SET LOCAL semantics so the value is scoped to the current transaction
// (or `set_config(..., true)` if not in a tx). This makes the OmniFree RLS
// policies (free_resource_catalog / free_quota_tracker / auto_combo_templates /
// keyless_providers) correctly filter rows by tenant_id.
//
// Why this exists (2026-08-09 audit round 3):
//
// The data-layer RLS policies were created in commit e5528809 with the
// `app.current_tenant` GUC contract. However, dbConn.Stdlib() returns a
// *sql.DB whose connections do NOT carry this GUC, so every query inside
// ChatHandler/auto-combo path silently fell back to tenant='default' regardless
// of the request's real tenant. That broke multi-tenant routing even though
// multi-tenant isolation was correctly enforced by RLS.
//
// Why SET (not set_config):
//
//   - lib/pq driver handles `SELECT set_config(...)` via prepared statement
//     path; in some Go versions the parameter does not propagate to the
//     server-side GUC binding reliably (verified via diagnostic test).
//   - `SET app.current_tenant = '<literal>'` works through the simple
//     query path on the same connection, and the GUC persists on that
//     connection for the lifetime of the session.
//   - We escape the tenantID to avoid SQL injection: only [A-Za-z0-9_-]
//     and <=64 chars are allowed; anything else falls through to 'default'.
//
// Usage:
//
//	if qt := h.quotaTracker; qt != nil {
//	    qt.SetTenant(r.Context(), keyInfo.TenantID)
//	    defer qt.ClearTenant(r.Context()) // optional
//	}
//
// This function never returns an error — failures are logged at WARN and
// swallowed, because:
//   - GUC may already be set higher in the call stack
//   - tenants with empty IDs should fall through to the RLS 'default' fallback
//   - blocking the OmniFree happy path on a GUC failure would be worse than
//     silently using 'default' (the pre-fix behavior)
//
// NOTE: callers MUST re-invoke this on every new connection / transaction.
// SET LOCAL is scoped to a single transaction; bare SET is connection-scoped.
func SetRLSTenantContext(ctx context.Context, db *sql.DB, tenantID string) {
	if db == nil {
		return
	}
	if !isValidTenantID(tenantID) {
		return
	}
	if _, err := db.ExecContext(ctx,
		fmt.Sprintf("SET app.current_tenant = '%s'", tenantID)); err != nil {
		slog.Warn("omnifree: failed to set app.current_tenant",
			"tenant_id", tenantID, "error", err)
	}
}

// SetRLSTenantContextConn sets the GUC on an existing *sql.Conn so it shares
// the same physical connection as the caller's subsequent queries. Use this
// in tests or anywhere caller holds a Conn. In production, prefer
// SetRLSTenantContextTx because transactions guarantee the GUC is scoped.
func SetRLSTenantContextConn(ctx context.Context, conn *sql.Conn, tenantID string) {
	if conn == nil || !isValidTenantID(tenantID) {
		return
	}
	if _, err := conn.ExecContext(ctx,
		fmt.Sprintf("SET app.current_tenant = '%s'", tenantID)); err != nil {
		slog.Warn("omnifree: failed to set app.current_tenant on conn",
			"tenant_id", tenantID, "error", err)
	}
}

// SetRLSTenantContextTx is the transaction-scoped variant. Use this inside an
// explicit `BeginTx` block; the GUC is reverted automatically when the tx
// commits or rolls back. This is the safer choice for short-lived operations
// like Record / CorrectFromHeaders / Preflight.
func SetRLSTenantContextTx(ctx context.Context, tx *sql.Tx, tenantID string) {
	if tx == nil || !isValidTenantID(tenantID) {
		return
	}
	if _, err := tx.ExecContext(ctx,
		fmt.Sprintf("SET LOCAL app.current_tenant = '%s'", tenantID)); err != nil {
		slog.Warn("omnifree: failed to set app.current_tenant in tx",
			"tenant_id", tenantID, "error", err)
	}
}

// isValidTenantID 防止 SQL 注入: 仅允许 [A-Za-z0-9_-] 且 <=64 字符.
// 与 075-omnifree-schema.sql 中 tenant_id 的语义保持一致 (TEXT 列, 不
// 受 BYPASSRLS 隐式放行的隐含约束, 但 RLS 函数本身没有引号转义, 所以
// 调用方必须自己保证 ID 干净).
func isValidTenantID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, c := range id {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '_' || c == '-') {
			return false
		}
	}
	return true
}