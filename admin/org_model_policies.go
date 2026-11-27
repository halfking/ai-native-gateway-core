package admin

// 组织/用户级模型策略 API（三级配额制，2026-11-27）。
//
// 路由（super_admin only，orgs dispatcher 同款）：
//
//	GET    /api/admin/orgs/{id}/model-policies            列表（含软删行 when include_deleted=true）
//	POST   /api/admin/orgs/{id}/model-policies            创建 {canonical_name, mode, reason}
//	PATCH  /api/admin/orgs/{id}/model-policies/{pid}      改 reason/mode
//	DELETE /api/admin/orgs/{id}/model-policies/{pid}      软删
//	POST   /api/admin/orgs/{id}/model-policies/{pid}/undelete  恢复
//
//	GET    /api/admin/users/{id}/model-policies           同形（user 级）
//	POST   /api/admin/users/{id}/model-policies
//	PATCH  /api/admin/users/{id}/model-policies/{pid}
//	DELETE /api/admin/users/{id}/model-policies/{pid}
//	POST   /api/admin/users/{id}/model-policies/{pid}/undelete
//
// 语义（迁移 1004）：mode deny=黑名单（既有语义）、allow=白名单（空名单=全放行）。
// 写后 Invalidate 对应 subject 的 Checker 缓存（org: InvalidateOrg / user: InvalidateUser）。
// org/user 两表无 RLS（写入面 admin-only）⇒ 不需要 tenant GUC 事务形态；
// app.current_admin GUC 仍然设置（审计触发器 actor 取值）。

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// modelPolicyMode validates the policy mode.
func modelPolicyMode(mode string) string {
	if mode == "" {
		return "deny"
	}
	return mode
}

func isValidPolicyMode(mode string) bool {
	return mode == "deny" || mode == "allow"
}

// orgPolicyDB 是 org/user 级模型策略 + org/user 钱包读写路径的窄注入面
//（2026-11-27 三级配额制轮）。nil ⇒ h.db。作用：db 字段是具体
// *pgxpool.Pool，单测里无法注入 pgxmock —— 按 pluginConfigDB 同款范式开窄口，
// 让「落库形状」「调用点存在」这类判据可以在单测里被钉住。
type orgPolicyDB interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Begin(ctx context.Context) (pgx.Tx, error)
}

// policyDB() returns the injected narrow face or the real pool.
func (h *Handler) policyDB() orgPolicyDB {
	if h.orgPolicyDB != nil {
		return h.orgPolicyDB
	}
	return h.db
}

// modelPolicyWire is the wire shape shared by org/user policy rows.
type modelPolicyWire struct {
	ID           int64      `json:"id"`
	SubjectID    int        `json:"subject_id"`
	Mode         string     `json:"mode"`
	CanonicalName string    `json:"canonical_name"`
	Reason       string     `json:"reason"`
	CreatedBy    string     `json:"created_by"`
	DeletedAt    *time.Time `json:"deleted_at,omitempty"`
	DeletedBy    *string    `json:"deleted_by,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

type modelPolicyCreateReq struct {
	CanonicalName string `json:"canonical_name"`
	Mode          string `json:"mode,omitempty"`
	Reason        string `json:"reason,omitempty"`
}

type modelPolicyPatchReq struct {
	Reason *string `json:"reason,omitempty"`
	Mode   *string `json:"mode,omitempty"`
}

// ── org 级 dispatcher（由 handleOrgs 的 sub 分支调用）───────────────

func (h *Handler) handleOrgModelPolicies(w http.ResponseWriter, r *http.Request, orgID int, rest string) {
	h.handleSubjectModelPolicies(w, r, "org", orgID, rest)
}

// handleUserModelPolicies handles /api/admin/users/{id}/model-policies/*.
func (h *Handler) handleUserModelPolicies(w http.ResponseWriter, r *http.Request, userID int, rest string) {
	h.handleSubjectModelPolicies(w, r, "user", userID, rest)
}

// handleSubjectModelPolicies is the shared dispatcher for both scopes.
// rest is the path tail after "model-policies".
func (h *Handler) handleSubjectModelPolicies(w http.ResponseWriter, r *http.Request, scope string, subjectID int, rest string) {
	if h.policyDB() == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}

	if rest == "" {
		switch r.Method {
		case http.MethodGet:
			h.listSubjectModelPolicies(w, r, scope, subjectID)
		case http.MethodPost:
			h.createSubjectModelPolicy(w, r, scope, subjectID)
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

	id, err := strconv.ParseInt(head, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "expected numeric policy id")
		return
	}
	switch tail {
	case "":
		switch r.Method {
		case http.MethodPatch:
			h.patchSubjectModelPolicy(w, r, scope, subjectID, id)
		case http.MethodDelete:
			h.deleteSubjectModelPolicy(w, r, scope, subjectID, id)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	case "undelete":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "undelete requires POST")
			return
		}
		h.undeleteSubjectModelPolicy(w, r, scope, subjectID, id)
	default:
		writeError(w, http.StatusNotFound, "unknown sub-path: "+tail)
	}
}

// subjectTables maps scope → (table, subject column, label).
func subjectTables(scope string) (table, idCol, label string, err error) {
	switch scope {
	case "org":
		return "org_model_policies", "org_id", "org", nil
	case "user":
		return "user_model_policies", "user_id", "user", nil
	default:
		return "", "", "", errors.New("unknown scope: " + scope)
	}
}

// invalidateSubject clears the Checker cache for one scope/subject.
func (h *Handler) invalidateSubject(scope string, subjectID int) {
	if h.modelPolicy == nil {
		return
	}
	switch scope {
	case "org":
		h.modelPolicy.InvalidateOrg(int64(subjectID))
	case "user":
		h.modelPolicy.InvalidateUser(int64(subjectID))
	}
}

// subjectExists verifies the org/user row exists (typo ⇒ 404, not FK error).
func (h *Handler) subjectExists(ctx context.Context, scope string, subjectID int) bool {
	var q string
	switch scope {
	case "org":
		q = `SELECT EXISTS(SELECT 1 FROM organizations WHERE id = $1)`
	case "user":
		q = `SELECT EXISTS(SELECT 1 FROM users WHERE id = $1)`
	default:
		return false
	}
	var exists bool
	if err := h.policyDB().QueryRow(ctx, q, subjectID).Scan(&exists); err != nil {
		return false
	}
	return exists
}

// listSubjectModelPolicies: GET .../model-policies
func (h *Handler) listSubjectModelPolicies(w http.ResponseWriter, r *http.Request, scope string, subjectID int) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	table, idCol, _, err := subjectTables(scope)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !h.subjectExists(ctx, scope, subjectID) {
		writeError(w, http.StatusNotFound, scope+" not found")
		return
	}

	includeDeleted := r.URL.Query().Get("include_deleted") == "true"
	sqlText := fmt.Sprintf(`
		SELECT id, %s, mode, canonical_name, reason, created_by,
		       deleted_at, deleted_by, created_at, updated_at
		FROM %s
		WHERE %s = $1`, idCol, table, idCol)
	if !includeDeleted {
		sqlText += ` AND deleted_at IS NULL`
	}
	sqlText += ` ORDER BY id DESC`

	rows, err := h.policyDB().Query(ctx, sqlText, subjectID)
	if err != nil {
		writeInternalErr(w, "query failed", err)
		return
	}
	defer rows.Close()

	out := make([]modelPolicyWire, 0)
	for rows.Next() {
		var p modelPolicyWire
		if err := rows.Scan(&p.ID, &p.SubjectID, &p.Mode, &p.CanonicalName, &p.Reason, &p.CreatedBy,
			&p.DeletedAt, &p.DeletedBy, &p.CreatedAt, &p.UpdatedAt); err != nil {
			warnRowSkip(scope+"Policies.list", err)
			continue
		}
		out = append(out, p)
	}
	if writeAggRowsErr(w, scope+"Policies.list", rows.Err()) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"policies": out,
		"count":    len(out),
	})
}

// createSubjectModelPolicy: POST .../model-policies
func (h *Handler) createSubjectModelPolicy(w http.ResponseWriter, r *http.Request, scope string, subjectID int) {
	var req modelPolicyCreateReq
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	canonical := strings.TrimSpace(req.CanonicalName)
	if canonical == "" {
		writeError(w, http.StatusBadRequest, "canonical_name is required")
		return
	}
	mode := modelPolicyMode(strings.TrimSpace(req.Mode))
	if !isValidPolicyMode(mode) {
		writeError(w, http.StatusBadRequest, "mode must be 'allow' or 'deny'")
		return
	}
	reason := strings.TrimSpace(req.Reason)

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	table, idCol, label, err := subjectTables(scope)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !h.subjectExists(ctx, scope, subjectID) {
		writeError(w, http.StatusNotFound, scope+" not found")
		return
	}

	createdBy := requestUser(r)
	if createdBy == "" {
		createdBy = "admin"
	}

	tx, err := h.policyDB().Begin(ctx)
	if err != nil {
		writeInternalErr(w, "transaction start failed", err)
		return
	}
	//nolint:errcheck // deferred rollback, best-effort
	defer tx.Rollback(ctx)

	// app.current_admin GUC for the audit trigger actor (no RLS on these
	// tables; the GUC is only read by the trigger fn).
	if err := tx.QueryRow(ctx, `SELECT set_config('app.current_admin', $1, true)`, createdBy).Scan(&createdBy); err != nil {
		//nolint:errcheck // set_config returns a row; failure is non-fatal for the write itself
		_ = err
	}

	var p modelPolicyWire
	err = tx.QueryRow(ctx, fmt.Sprintf(`
		INSERT INTO %s (%s, mode, canonical_name, reason, created_by)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, %s, mode, canonical_name, reason, created_by,
		          deleted_at, deleted_by, created_at, updated_at
	`, table, idCol, idCol), subjectID, mode, canonical, reason, createdBy).Scan(
		&p.ID, &p.SubjectID, &p.Mode, &p.CanonicalName, &p.Reason, &p.CreatedBy,
		&p.DeletedAt, &p.DeletedBy, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict,
				"a policy for this "+label+" + canonical_name already exists (or was soft-deleted; restore or use a new name)")
			return
		}
		writeInternalErr(w, "insert failed", err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeInternalErr(w, "commit failed", err)
		return
	}

	h.invalidateSubject(scope, subjectID)
	h.writeAuditLog(r, "model_policy."+scope+".create", "model_policy", int(p.ID), auditDetails(map[string]any{
		scope + "_id":      subjectID,
		"canonical_name":   canonical,
		"mode":             mode,
		"reason":           reason,
		"ip":               clientIP(r),
		"ua":               r.UserAgent(),
	}))

	writeJSON(w, http.StatusCreated, map[string]any{
		"id":      p.ID,
		"status":  "created",
		"policy":  p,
		"message": fmt.Sprintf("policy active for %s %d; cache invalidated", scope, subjectID),
	})
}

// patchSubjectModelPolicy: PATCH .../model-policies/{id}（reason/mode）。
func (h *Handler) patchSubjectModelPolicy(w http.ResponseWriter, r *http.Request, scope string, subjectID int, id int64) {
	var req modelPolicyPatchReq
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Mode != nil && !isValidPolicyMode(strings.TrimSpace(*req.Mode)) {
		writeError(w, http.StatusBadRequest, "mode must be 'allow' or 'deny'")
		return
	}
	reason := ""
	if req.Reason != nil {
		reason = strings.TrimSpace(*req.Reason)
	}
	mode := ""
	if req.Mode != nil {
		mode = strings.TrimSpace(*req.Mode)
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	table, idCol, _, err := subjectTables(scope)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	createdBy := requestUser(r)
	if createdBy == "" {
		createdBy = "admin"
	}

	tx, err := h.policyDB().Begin(ctx)
	if err != nil {
		writeInternalErr(w, "transaction start failed", err)
		return
	}
	//nolint:errcheck // deferred rollback, best-effort
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT set_config('app.current_admin', $1, true)`, createdBy); err != nil {
		writeInternalErr(w, "set tx GUC failed", err)
		return
	}

	var p modelPolicyWire
	err = tx.QueryRow(ctx, fmt.Sprintf(`
		UPDATE %s
		SET reason = COALESCE(NULLIF($1, ''), reason),
		    mode = COALESCE(NULLIF($2, ''), mode),
		    updated_at = now()
		WHERE id = $3 AND %s = $4 AND deleted_at IS NULL
		RETURNING id, %s, mode, canonical_name, reason, created_by,
		          deleted_at, deleted_by, created_at, updated_at
	`, table, idCol, idCol), reason, mode, id, subjectID).Scan(
		&p.ID, &p.SubjectID, &p.Mode, &p.CanonicalName, &p.Reason, &p.CreatedBy,
		&p.DeletedAt, &p.DeletedBy, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "policy not found (or soft-deleted)")
			return
		}
		writeInternalErr(w, "update failed", err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeInternalErr(w, "commit failed", err)
		return
	}

	h.invalidateSubject(scope, subjectID)
	h.writeAuditLog(r, "model_policy."+scope+".patch", "model_policy", int(p.ID), auditDetails(map[string]any{
		scope + "_id": subjectID,
		"policy_id":   id,
		"ip":          clientIP(r),
	}))

	writeJSON(w, http.StatusOK, map[string]any{"status": "updated", "policy": p})
}

// deleteSubjectModelPolicy: DELETE .../model-policies/{id}（软删）。
func (h *Handler) deleteSubjectModelPolicy(w http.ResponseWriter, r *http.Request, scope string, subjectID int, id int64) {
	deletedBy := requestUser(r)
	if deletedBy == "" {
		deletedBy = "admin"
	}
	n, err := h.softDeleteSubjectPolicy(scope, subjectID, id, deletedBy, false)
	if err != nil {
		writeInternalErr(w, "delete failed", err)
		return
	}
	if n == 0 {
		writeError(w, http.StatusNotFound, "policy not found (or already deleted)")
		return
	}
	h.invalidateSubject(scope, subjectID)
	h.writeAuditLog(r, "model_policy."+scope+".delete", "model_policy", int(id), auditDetails(map[string]any{
		scope + "_id": subjectID,
		"policy_id":   id,
		"ip":          clientIP(r),
	}))
	writeJSON(w, http.StatusOK, map[string]any{"status": "deleted", "id": id})
}

// undeleteSubjectModelPolicy: POST .../model-policies/{id}/undelete。
func (h *Handler) undeleteSubjectModelPolicy(w http.ResponseWriter, r *http.Request, scope string, subjectID int, id int64) {
	actor := requestUser(r)
	if actor == "" {
		actor = "admin"
	}
	n, err := h.softDeleteSubjectPolicy(scope, subjectID, id, actor, true)
	if err != nil {
		writeInternalErr(w, "undelete failed", err)
		return
	}
	if n == 0 {
		writeError(w, http.StatusNotFound, "policy not found (nothing to restore)")
		return
	}
	h.invalidateSubject(scope, subjectID)
	h.writeAuditLog(r, "model_policy."+scope+".undelete", "model_policy", int(id), auditDetails(map[string]any{
		scope + "_id": subjectID,
		"policy_id":   id,
		"ip":          clientIP(r),
	}))
	writeJSON(w, http.StatusOK, map[string]any{"status": "restored", "id": id})
}

// softDeleteSubjectPolicy flips deleted_at for one scoped policy row.
// restore=true ⇒ clear the soft-delete (only for currently deleted rows);
// restore=false ⇒ set it (only for currently active rows).  Returns rows
// affected (0 ⇒ nothing matched the precondition).
func (h *Handler) softDeleteSubjectPolicy(scope string, subjectID int, id int64, actor string, restore bool) (int64, error) {
	table, idCol, _, err := subjectTables(scope)
	if err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var q string
	var args []any
	if restore {
		q = fmt.Sprintf(`
			UPDATE %s
			SET deleted_at = NULL, deleted_by = NULL, updated_at = now()
			WHERE id = $1 AND %s = $2 AND deleted_at IS NOT NULL
		`, table, idCol)
		args = []any{id, subjectID}
	} else {
		q = fmt.Sprintf(`
			UPDATE %s
			SET deleted_at = now(), deleted_by = $3, updated_at = now()
			WHERE id = $1 AND %s = $2 AND deleted_at IS NULL
		`, table, idCol)
		args = []any{id, subjectID, actor}
	}
	tag, err := h.policyDB().Exec(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
