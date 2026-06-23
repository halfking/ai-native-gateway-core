package admin

// model_policies.go — Round 48 (2026-06-21) Tenant Model Policy admin endpoints.
//
//   GET    /api/admin/tenants/{code}/model-policies                 list (active or all)
//   POST   /api/admin/tenants/{code}/model-policies                 create (super_admin)
//   PATCH  /api/admin/tenants/{code}/model-policies/{id}           edit reason (super_admin)
//   DELETE /api/admin/tenants/{code}/model-policies/{id}           soft delete (super_admin)
//   POST   /api/admin/tenants/{code}/model-policies/{id}/undelete  restore (super_admin)
//   POST   /api/admin/tenants/{code}/model-policies/check          autocomplete (admin)
//   GET    /api/admin/tenants/{code}/model-policies/audit          audit log (admin)
//
// All mutations:
//   1. Begin a transaction
//   2. SET LOCAL app.current_admin = actor
//   3. INSERT/UPDATE/DELETE
//   4. Commit (trigger writes audit row atomically)
//   5. Invalidate the per-tenant Checker cache (admin.Handler.modelPolicy)
//   6. Write application-level audit log via h.writeAuditLog
//
// Read endpoints filter by tenant_id explicitly (no reliance on RLS
// GUC for admin reads — admin sees everything within their scope).
//
// Cross-tenant write prevention (audit H4): every write endpoint
// re-checks auth.TenantID == urlTenantCode before doing the
// INSERT/UPDATE/DELETE.  RLS USING does NOT block INSERTs without
// a WITH CHECK policy, so we MUST validate in application code.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// ── Wire types ──────────────────────────────────────────────────────

// TenantModelPolicyWire is the JSON wire format for tenant_model_policies rows.
type TenantModelPolicyWire struct {
	ID            int64      `json:"id"`
	TenantID      string     `json:"tenant_id"`
	CanonicalName string     `json:"canonical_name"`
	Reason        string     `json:"reason"`
	CreatedBy     string     `json:"created_by"`
	DeletedAt     *time.Time `json:"deleted_at,omitempty"`
	DeletedBy     *string    `json:"deleted_by,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

// TenantModelPolicyCreateReq is the JSON body for POST /model-policies.
type TenantModelPolicyCreateReq struct {
	CanonicalName string `json:"canonical_name"`
	Reason        string `json:"reason"`
}

// TenantModelPolicyPatchReq is the JSON body for PATCH /model-policies/{id}.
type TenantModelPolicyPatchReq struct {
	Reason string `json:"reason"`
}

// TenantModelPolicyCheckReq is the JSON body for POST /model-policies/check.
// Used by the Vue UI autocomplete to validate the input exists in
// models_canonical before the user submits the create form.
type TenantModelPolicyCheckReq struct {
	CanonicalName string `json:"canonical_name"`
}

// TenantModelPolicyCheckResp is what /check returns.
type TenantModelPolicyCheckResp struct {
	Exists        bool   `json:"exists"`
	CanonicalName string `json:"canonical_name"`
	Family        string `json:"family,omitempty"`
	Vendor        string `json:"vendor,omitempty"`
	Modality      string `json:"modality,omitempty"`
}

// ── Routing ──────────────────────────────────────────────────────────

// handleTenantModelPolicies dispatches /api/admin/tenants/{code}/model-policies/*
// Mounted in tenants.go (handleTenants) when sub == "model-policies".
func (h *Handler) handleTenantModelPolicies(w http.ResponseWriter, r *http.Request, tenantCode string) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}

	// Strip the /api/admin/tenants/{code}/model-policies prefix.
	const prefix = "/api/admin/tenants/"
	path := strings.TrimPrefix(r.URL.Path, prefix)
	// After trim: "{code}/model-policies[/...]"
	idx := strings.Index(path, "/model-policies")
	if idx < 0 {
		writeError(w, http.StatusBadRequest, "expected /model-policies sub-path")
		return
	}
	rest := strings.Trim(path[idx+len("/model-policies"):], "/")

	if rest == "" {
		switch r.Method {
		case http.MethodGet:
			h.listTenantModelPolicies(w, r, tenantCode)
		case http.MethodPost:
			h.createTenantModelPolicy(w, r, tenantCode)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}

	parts := strings.SplitN(rest, "/", 2)
	head := parts[0]
	tail := ""
	if len(parts) > 1 {
		tail = parts[1]
	}

	switch head {
	case "check":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "check requires POST")
			return
		}
		h.checkTenantModelPolicy(w, r, tenantCode)
	case "audit":
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "audit requires GET")
			return
		}
		h.listTenantModelPoliciesAudit(w, r, tenantCode)
	default:
		// Expect {id} or {id}/{action}
		id, err := strconv.ParseInt(head, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "expected numeric policy id")
			return
		}
		switch tail {
		case "":
			switch r.Method {
			case http.MethodPatch:
				h.patchTenantModelPolicy(w, r, tenantCode, id)
			case http.MethodDelete:
				h.deleteTenantModelPolicy(w, r, tenantCode, id)
			default:
				writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			}
		case "undelete":
			if r.Method != http.MethodPost {
				writeError(w, http.StatusMethodNotAllowed, "undelete requires POST")
				return
			}
			h.undeleteTenantModelPolicy(w, r, tenantCode, id)
		default:
			writeError(w, http.StatusNotFound, "unknown sub-path: "+tail)
		}
	}
}

// ── List (GET) ─────────────────────────────────────────────────────

func (h *Handler) listTenantModelPolicies(w http.ResponseWriter, r *http.Request, tenantCode string) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// Verify tenant exists so a typo returns 404, not empty 200.
	var exists bool
	if err := h.db.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM tenants WHERE code = $1)`, tenantCode,
	).Scan(&exists); err != nil || !exists {
		writeError(w, http.StatusNotFound, "tenant not found")
		return
	}

	includeDeleted := r.URL.Query().Get("include_deleted") == "true"
	sqlText := `
		SELECT id, tenant_id, canonical_name, reason, created_by,
		       deleted_at, deleted_by, created_at, updated_at
		FROM tenant_model_policies
		WHERE tenant_id = $1`
	if !includeDeleted {
		sqlText += ` AND deleted_at IS NULL`
	}
	sqlText += ` ORDER BY id DESC`

	rows, err := h.db.Query(ctx, sqlText, tenantCode)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}
	defer rows.Close()

	out := make([]TenantModelPolicyWire, 0)
	for rows.Next() {
		var p TenantModelPolicyWire
		if err := rows.Scan(
			&p.ID, &p.TenantID, &p.CanonicalName, &p.Reason, &p.CreatedBy,
			&p.DeletedAt, &p.DeletedBy, &p.CreatedAt, &p.UpdatedAt,
		); err != nil {
			continue
		}
		out = append(out, p)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"policies": out,
		"count":    len(out),
		"tenant":   tenantCode,
	})
}

// ── Create (POST) ─────────────────────────────────────────────────

func (h *Handler) createTenantModelPolicy(w http.ResponseWriter, r *http.Request, tenantCode string) {
	// Audit H4: cross-tenant write prevention.  tenant_admin can
	// only create policies for their own tenant; super_admin can
	// create for any tenant.
	if !h.canAdministerTenant(r, tenantCode) {
		writeError(w, http.StatusForbidden, "cannot create policy for another tenant")
		return
	}

	var req TenantModelPolicyCreateReq
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	canonical := strings.TrimSpace(req.CanonicalName)
	if canonical == "" {
		writeError(w, http.StatusBadRequest, "canonical_name is required")
		return
	}
	reason := strings.TrimSpace(req.Reason)

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// Verify tenant exists so a typo returns 404, not a FK error.
	var exists bool
	if err := h.db.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM tenants WHERE code = $1)`, tenantCode,
	).Scan(&exists); err != nil || !exists {
		writeError(w, http.StatusNotFound, "tenant not found")
		return
	}

	createdBy := requestUser(r)
	if createdBy == "" {
		createdBy = "admin"
	}

	tx, err := h.db.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaction start failed: "+err.Error())
		return
	}
	//nolint:errcheck // deferred rollback, best-effort
	defer tx.Rollback(ctx)

	// Set BOTH GUCs: app.current_admin (audit trigger actor) AND
	// app.current_tenant (RLS tenant_isolation_tmp USING clause).
	// Without current_tenant the INSERT/UPDATE is rejected by RLS
	// (FORCE ROW LEVEL SECURITY + rolbypassrls=false).
	if err := setPolicyTxGUCs(ctx, tx, tenantCode, createdBy); err != nil {
		writeError(w, http.StatusInternalServerError, "set tx GUCs failed: "+err.Error())
		return
	}

	var p TenantModelPolicyWire
	err = tx.QueryRow(ctx, `
		INSERT INTO tenant_model_policies (tenant_id, canonical_name, reason, created_by)
		VALUES ($1, $2, $3, $4)
		RETURNING id, tenant_id, canonical_name, reason, created_by,
		          deleted_at, deleted_by, created_at, updated_at
	`, tenantCode, canonical, reason, createdBy).Scan(
		&p.ID, &p.TenantID, &p.CanonicalName, &p.Reason, &p.CreatedBy,
		&p.DeletedAt, &p.DeletedBy, &p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict,
				"a policy for this tenant + canonical_name already exists (or was soft-deleted; restore or use a new name)")
			return
		}
		writeError(w, http.StatusInternalServerError, "insert failed: "+err.Error())
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "commit failed: "+err.Error())
		return
	}

	// Cache invalidation (admin.Handler.modelPolicy.Invalidate).
	if h.modelPolicy != nil {
		h.modelPolicy.Invalidate(tenantCode)
	}

	h.writeAuditLog(r, "model_policy.create", "model_policy", int(p.ID), auditDetails(map[string]any{
		"tenant_id":      tenantCode,
		"canonical_name": canonical,
		"reason":         reason,
		"ip":             clientIP(r),
		"ua":             r.UserAgent(),
	}))

	writeJSON(w, http.StatusCreated, map[string]any{
		"id":      p.ID,
		"status":  "created",
		"policy":  p,
		"message": fmt.Sprintf("policy active for tenant %q; cache invalidated", tenantCode),
	})
}

// ── Patch reason (PATCH) ──────────────────────────────────────────

func (h *Handler) patchTenantModelPolicy(w http.ResponseWriter, r *http.Request, tenantCode string, id int64) {
	if !h.canAdministerTenant(r, tenantCode) {
		writeError(w, http.StatusForbidden, "cannot edit policy for another tenant")
		return
	}

	var req TenantModelPolicyPatchReq
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	reason := strings.TrimSpace(req.Reason)

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	createdBy := requestUser(r)
	if createdBy == "" {
		createdBy = "admin"
	}

	tx, err := h.db.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaction start failed: "+err.Error())
		return
	}
	//nolint:errcheck // deferred rollback, best-effort
	defer tx.Rollback(ctx)
	// Set BOTH GUCs: app.current_admin (audit trigger actor) AND
	// app.current_tenant (RLS tenant_isolation_tmp USING clause).
	// Without current_tenant the INSERT/UPDATE is rejected by RLS
	// (FORCE ROW LEVEL SECURITY + rolbypassrls=false).
	if err := setPolicyTxGUCs(ctx, tx, tenantCode, createdBy); err != nil {
		writeError(w, http.StatusInternalServerError, "set tx GUCs failed: "+err.Error())
		return
	}

	var p TenantModelPolicyWire
	err = tx.QueryRow(ctx, `
		UPDATE tenant_model_policies
		SET reason = $1, updated_at = now()
		WHERE id = $2 AND tenant_id = $3 AND deleted_at IS NULL
		RETURNING id, tenant_id, canonical_name, reason, created_by,
		          deleted_at, deleted_by, created_at, updated_at
	`, reason, id, tenantCode).Scan(
		&p.ID, &p.TenantID, &p.CanonicalName, &p.Reason, &p.CreatedBy,
		&p.DeletedAt, &p.DeletedBy, &p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			writeError(w, http.StatusNotFound, "policy not found or soft-deleted")
			return
		}
		writeError(w, http.StatusInternalServerError, "update failed: "+err.Error())
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "commit failed: "+err.Error())
		return
	}

	if h.modelPolicy != nil {
		h.modelPolicy.Invalidate(tenantCode)
	}

	h.writeAuditLog(r, "model_policy.update", "model_policy", int(id), auditDetails(map[string]any{
		"tenant_id": tenantCode,
		"reason":    reason,
		"ip":        clientIP(r),
		"ua":        r.UserAgent(),
	}))

	writeJSON(w, http.StatusOK, p)
}

// ── Soft delete (DELETE) ─────────────────────────────────────────

func (h *Handler) deleteTenantModelPolicy(w http.ResponseWriter, r *http.Request, tenantCode string, id int64) {
	if !h.canAdministerTenant(r, tenantCode) {
		writeError(w, http.StatusForbidden, "cannot delete policy for another tenant")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	createdBy := requestUser(r)
	if createdBy == "" {
		createdBy = "admin"
	}

	tx, err := h.db.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaction start failed: "+err.Error())
		return
	}
	//nolint:errcheck // deferred rollback, best-effort
	defer tx.Rollback(ctx)
	// Set BOTH GUCs: app.current_admin (audit trigger actor) AND
	// app.current_tenant (RLS tenant_isolation_tmp USING clause).
	// Without current_tenant the INSERT/UPDATE is rejected by RLS
	// (FORCE ROW LEVEL SECURITY + rolbypassrls=false).
	if err := setPolicyTxGUCs(ctx, tx, tenantCode, createdBy); err != nil {
		writeError(w, http.StatusInternalServerError, "set tx GUCs failed: "+err.Error())
		return
	}

	tag, err := tx.Exec(ctx, `
		UPDATE tenant_model_policies
		SET deleted_at = now(), deleted_by = $1, updated_at = now()
		WHERE id = $2 AND tenant_id = $3 AND deleted_at IS NULL
	`, createdBy, id, tenantCode)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "delete failed: "+err.Error())
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "policy not found or already deleted")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "commit failed: "+err.Error())
		return
	}

	if h.modelPolicy != nil {
		h.modelPolicy.Invalidate(tenantCode)
	}

	h.writeAuditLog(r, "model_policy.delete", "model_policy", int(id), auditDetails(map[string]any{
		"tenant_id": tenantCode,
		"reason":    "soft delete",
		"ip":        clientIP(r),
		"ua":        r.UserAgent(),
	}))

	writeJSON(w, http.StatusOK, map[string]any{
		"id":      id,
		"status":  "soft_deleted",
		"message": "policy soft-deleted; active view no longer returns it; audit row preserved",
	})
}

// ── Undelete (POST /{id}/undelete) ───────────────────────────────

func (h *Handler) undeleteTenantModelPolicy(w http.ResponseWriter, r *http.Request, tenantCode string, id int64) {
	if !h.canAdministerTenant(r, tenantCode) {
		writeError(w, http.StatusForbidden, "cannot restore policy for another tenant")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	createdBy := requestUser(r)
	if createdBy == "" {
		createdBy = "admin"
	}

	tx, err := h.db.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaction start failed: "+err.Error())
		return
	}
	//nolint:errcheck // deferred rollback, best-effort
	defer tx.Rollback(ctx)
	// Set BOTH GUCs: app.current_admin (audit trigger actor) AND
	// app.current_tenant (RLS tenant_isolation_tmp USING clause).
	// Without current_tenant the INSERT/UPDATE is rejected by RLS
	// (FORCE ROW LEVEL SECURITY + rolbypassrls=false).
	if err := setPolicyTxGUCs(ctx, tx, tenantCode, createdBy); err != nil {
		writeError(w, http.StatusInternalServerError, "set tx GUCs failed: "+err.Error())
		return
	}

	tag, err := tx.Exec(ctx, `
		UPDATE tenant_model_policies
		SET deleted_at = NULL, deleted_by = NULL, updated_at = now()
		WHERE id = $1 AND tenant_id = $2 AND deleted_at IS NOT NULL
	`, id, tenantCode)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "undelete failed: "+err.Error())
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "policy not found or not in soft-deleted state")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "commit failed: "+err.Error())
		return
	}

	if h.modelPolicy != nil {
		h.modelPolicy.Invalidate(tenantCode)
	}

	h.writeAuditLog(r, "model_policy.undelete", "model_policy", int(id), auditDetails(map[string]any{
		"tenant_id": tenantCode,
		"reason":    "soft delete restored",
		"ip":        clientIP(r),
		"ua":        r.UserAgent(),
	}))

	writeJSON(w, http.StatusOK, map[string]any{
		"id":      id,
		"status":  "active",
		"message": "policy restored; cache invalidated",
	})
}

// ── Check (POST /check) ───────────────────────────────────────────

func (h *Handler) checkTenantModelPolicy(w http.ResponseWriter, r *http.Request, tenantCode string) {
	var req TenantModelPolicyCheckReq
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	canonical := strings.TrimSpace(req.CanonicalName)
	if canonical == "" {
		writeError(w, http.StatusBadRequest, "canonical_name is required")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	// Look up in models_canonical.  We allow non-matches (the
	// policy can denylist a model not yet in the catalog —
	// defence in depth), but we surface metadata when present.
	var (
		familyRaw  *string
		modality   string
	)
	err := h.db.QueryRow(ctx, `
		SELECT mc.family, COALESCE(NULLIF(TRIM(mc.modality), ''), 'text')
		FROM models_canonical mc
		WHERE lower(mc.canonical_name) = lower($1)
		  AND COALESCE(mc.status, 'active') = 'active'
		LIMIT 1
	`, canonical).Scan(&familyRaw, &modality)

	resp := TenantModelPolicyCheckResp{CanonicalName: canonical, Modality: "text"}
	if err == nil {
		resp.Exists = true
		if familyRaw != nil {
			resp.Family = *familyRaw
		}
		resp.Modality = modality
		// Vendor: best-effort from canonical family via a simple
		// lookup; we don't import the catalog package to avoid a
		// cycle.  Returning empty string is acceptable for the
		// UI which falls back to "Unknown vendor".
	} else if err != pgx.ErrNoRows {
		writeError(w, http.StatusInternalServerError, "lookup failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// ── Audit log (GET /audit) ────────────────────────────────────────

func (h *Handler) listTenantModelPoliciesAudit(w http.ResponseWriter, r *http.Request, tenantCode string) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	limit := 100
	if s := r.URL.Query().Get("limit"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}

	rows, err := h.db.Query(ctx, `
		SELECT id, ts, action, policy_id, tenant_id, canonical_name, reason, actor
		FROM tenant_model_policies_audit
		WHERE tenant_id = $1
		ORDER BY ts DESC
		LIMIT $2
	`, tenantCode, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "audit query failed: "+err.Error())
		return
	}
	defer rows.Close()

	type auditRow struct {
		ID            int64     `json:"id"`
		TS            time.Time `json:"ts"`
		Action        string    `json:"action"`
		PolicyID      *int64    `json:"policy_id,omitempty"`
		TenantID      string    `json:"tenant_id"`
		CanonicalName string    `json:"canonical_name"`
		Reason        string    `json:"reason"`
		Actor         string    `json:"actor"`
	}
	out := make([]auditRow, 0)
	for rows.Next() {
		var a auditRow
		if err := rows.Scan(
			&a.ID, &a.TS, &a.Action, &a.PolicyID, &a.TenantID,
			&a.CanonicalName, &a.Reason, &a.Actor,
		); err != nil {
			continue
		}
		out = append(out, a)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"audit": out,
		"count": len(out),
	})
}

// ── Helpers ────────────────────────────────────────────────────────

// canAdministerTenant returns true if the authenticated user is
// allowed to mutate policies for tenantCode.  super_admin and
// admin_key (legacy Bearer sk-...) can mutate any tenant;
// tenant_admin only their own.
func (h *Handler) canAdministerTenant(r *http.Request, tenantCode string) bool {
	auth := GetAuthContext(r)
	if auth == nil {
		return false
	}
	switch auth.Role {
	case "super_admin", "admin_key":
		return true
	case "tenant_admin":
		return auth.TenantID == tenantCode || tenantCode == "" || tenantCode == auth.TenantID
	default:
		return false
	}
}

// setPolicyTxGUCs sets the two session GUCs every policy write must
// establish inside its transaction:
//
//   - app.current_admin → captured by tenant_model_policies_audit_fn()
//     trigger as the audit "actor".
//   - app.current_tenant → required by the tenant_isolation_tmp RLS policy.
//
// tenant_model_policies has ENABLE + FORCE ROW LEVEL SECURITY and the
// application role llm_gateway has rolbypassrls=false, so RLS applies
// even to the table owner. The policy (polcmd = *, no explicit WITH
// CHECK) makes the USING clause act as an implicit WITH CHECK on
// INSERT/UPDATE/DELETE. Without setting app.current_tenant to the row's
// tenant_id, get_current_tenant() falls back to 'default' and the
// INSERT is rejected with SQLSTATE 42501
// (incident 2026-06-23: "new row violates row-level security policy").
//
// SET LOCAL does not accept placeholders, so values are single-quote
// escaped to prevent GUC injection. The setting is transaction-scoped
// and auto-clears on commit/rollback.
//
// Mirrors the read-path convention in tenant_ctx.go::withTenantTx.
func setPolicyTxGUCs(ctx context.Context, tx pgx.Tx, tenantCode, actor string) error {
	escapedTenant := strings.ReplaceAll(tenantCode, "'", "''")
	if _, err := tx.Exec(ctx, "SET LOCAL app.current_tenant = '"+escapedTenant+"'"); err != nil {
		return fmt.Errorf("set tenant GUC: %w", err)
	}
	escapedActor := strings.ReplaceAll(actor, "'", "''")
	if _, err := tx.Exec(ctx, "SET LOCAL app.current_admin = '"+escapedActor+"'"); err != nil {
		return fmt.Errorf("set admin GUC: %w", err)
	}
	return nil
}

// auditDetails marshals a map to a compact JSON string for the
// Handler.writeAuditLog(r, action, targetType, targetID, details)
// signature used in users.go (the "details" parameter is a
// pre-serialised string, not a map).  On marshal failure we
// fall back to "{}" so the audit row is still written.
func auditDetails(v map[string]any) string {
	if v == nil {
		return "{}"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// isUniqueViolation is defined in routing_overrides.go (single
// source of truth, exported package-private).