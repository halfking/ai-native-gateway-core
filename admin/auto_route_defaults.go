package admin

// auto_route_defaults.go — M2: task_default_routing 的 admin CRUD。
//
// 端点（仿 routing_overrides，挂在 /api/admin/auto-route/defaults）：
//   GET    /api/admin/auto-route/defaults          列表（支持 task_type/profile/tenant_id/active 过滤）
//   POST   /api/admin/auto-route/defaults          创建
//   DELETE /api/admin/auto-route/defaults/:id      删除（带审计）
//   PATCH  /api/admin/auto-route/defaults/:id      更新（reason/expires_at/priority/tier）
//   GET    /api/admin/auto-route/defaults/audit    审计列表
//
// 权限：super_admin 可操作任意行；tenant_admin 仅可见/操作本租户行（tenant_id
// 必须等于调用者 tenant）。本文件暂以 superAdmin 中间件保护（与
// RegisterAutoRouteRoutes 一致），tenant_admin 细粒度过滤为后续 P1。
//
// 审计：直接写 task_default_routing_audit（无 DB trigger，事务内原子提交）。
// 详见 docs/拆分/22-Auto智能路由与任务识别.md §22.6。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// DefaultRoutingWire 是 task_default_routing 行的 JSON 格式。
type DefaultRoutingWire struct {
	ID             int64      `json:"id"`
	TaskType       string     `json:"task_type"`
	Profile        string     `json:"profile"`
	Tier           string     `json:"tier"`
	CanonicalModel string     `json:"canonical_model"`
	TenantID       *int64     `json:"tenant_id,omitempty"`
	Priority       int        `json:"priority"`
	Reason         string     `json:"reason"`
	CreatedBy      *string    `json:"created_by,omitempty"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// DefaultRoutingCreateReq 是 POST body。
type DefaultRoutingCreateReq struct {
	TaskType       string     `json:"task_type"`
	Profile        string     `json:"profile"`
	Tier           string     `json:"tier"`
	CanonicalModel string     `json:"canonical_model"`
	TenantID       *int64     `json:"tenant_id,omitempty"`
	Priority       int        `json:"priority"`
	Reason         string     `json:"reason"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
}

// DefaultRoutingUpdateReq 是 PATCH body（全部可选）。
type DefaultRoutingUpdateReq struct {
	Tier      *string    `json:"tier,omitempty"`
	Priority  *int       `json:"priority,omitempty"`
	Reason    *string    `json:"reason,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// validTiers 与 SQL CHECK 一致。
var validDefaultTiers = map[string]bool{"primary": true, "secondary": true, "fallback": true}

// validProfiles 与 SQL CHECK 一致（” 表示通用）。
var validDefaultProfiles = map[string]bool{"": true, "smart": true, "speed_first": true, "cost_first": true}

// HandleDefaultRoutingCollection: GET (list) / POST (create).
func (h *AutoRouteHandlers) HandleDefaultRoutingCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.listDefaultRouting(w, r)
	case http.MethodPost:
		h.createDefaultRouting(w, r)
	default:
		writeJSONErrCtx(w, r, http.StatusMethodNotAllowed, "admin_method_not_allowed")
	}
}

// HandleDefaultRoutingItem: DELETE / PATCH /:id 和 /audit 子路径。
func (h *AutoRouteHandlers) HandleDefaultRoutingItem(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/admin/auto-route/defaults")
	path = strings.Trim(path, "/")
	parts := strings.Split(path, "/")
	if len(parts) == 0 || parts[0] == "" {
		writeJSONErr(w, http.StatusBadRequest, "expected /:id or /audit")
		return
	}
	if parts[0] == "audit" {
		h.listDefaultRoutingAudit(w, r)
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		writeJSONErrCtx(w, r, http.StatusBadRequest, "admin_invalid_default_routing_id")
		return
	}
	if len(parts) > 1 {
		writeJSONErr(w, http.StatusBadRequest, "unknown sub-path")
		return
	}
	switch r.Method {
	case http.MethodDelete:
		h.deleteDefaultRouting(w, r, id)
	case http.MethodPatch:
		h.updateDefaultRouting(w, r, id)
	default:
		writeJSONErrCtx(w, r, http.StatusMethodNotAllowed, "admin_method_not_allowed")
	}
}

// ── GET (list) ──────────────────────────────────────────────────

func (h *AutoRouteHandlers) listDefaultRouting(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	activeOnly := q.Get("active") == "true"
	taskType := q.Get("task_type")
	profile := q.Get("profile")

	sb := strings.Builder{}
	sb.WriteString(`SELECT id, task_type, profile, tier, canonical_model, tenant_id,
	       priority, reason, created_by, expires_at, created_at, updated_at
	       FROM task_default_routing WHERE 1=1`)
	args := []any{}
	if activeOnly {
		sb.WriteString(` AND (expires_at IS NULL OR expires_at > NOW())`)
	}
	if taskType != "" {
		args = append(args, taskType)
		sb.WriteString(fmt.Sprintf(` AND task_type = $%d`, len(args)))
	}
	if profile != "" {
		args = append(args, profile)
		sb.WriteString(fmt.Sprintf(` AND profile = $%d`, len(args)))
	}
	sb.WriteString(` ORDER BY task_type, profile, COALESCE(tenant_id,0), priority DESC`)

	rows, err := h.db.Query(r.Context(), sb.String(), args...)
	if err != nil {
		writeInternalErr(w, err)
		return
	}
	defer rows.Close()

	out := make([]DefaultRoutingWire, 0)
	for rows.Next() {
		var dr DefaultRoutingWire
		var createdBy *string
		if err := rows.Scan(&dr.ID, &dr.TaskType, &dr.Profile, &dr.Tier,
			&dr.CanonicalModel, &dr.TenantID, &dr.Priority, &dr.Reason,
			&createdBy, &dr.ExpiresAt, &dr.CreatedAt, &dr.UpdatedAt); err != nil {
			writeInternalErr(w, err)
			return
		}
		dr.CreatedBy = createdBy
		out = append(out, dr)
	}
	if err := rows.Err(); err != nil {
		writeInternalErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"defaults": out,
		"count":    len(out),
		"filter":   map[string]string{"task_type": taskType, "profile": profile, "active": strconv.FormatBool(activeOnly)},
	})
}

// ── POST (create) ───────────────────────────────────────────────

func (h *AutoRouteHandlers) createDefaultRouting(w http.ResponseWriter, r *http.Request) {
	var req DefaultRoutingCreateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONErr(w, http.StatusBadRequest, fmt.Sprintf("invalid body: %v", err))
		return
	}
	if req.TaskType == "" {
		writeJSONErr(w, http.StatusBadRequest, "task_type is required")
		return
	}
	if req.CanonicalModel == "" {
		writeJSONErr(w, http.StatusBadRequest, "canonical_model is required")
		return
	}
	if req.Tier == "" {
		req.Tier = "primary"
	}
	if !validDefaultTiers[req.Tier] {
		writeJSONErr(w, http.StatusBadRequest, "tier must be primary/secondary/fallback")
		return
	}
	if !validDefaultProfiles[req.Profile] {
		writeJSONErr(w, http.StatusBadRequest, "profile must be '', smart, speed_first, cost_first")
		return
	}

	createdBy := requestUser(r)
	if createdBy == "" {
		createdBy = "admin"
	}

	ctx := r.Context()
	tx, err := h.db.Begin(ctx)
	if err != nil {
		writeInternalErr(w, err)
		return
	}
	//nolint:errcheck // deferred rollback, best-effort
	defer tx.Rollback(ctx)

	// 校验 canonical_model 存在且 active。
	var status string
	err = tx.QueryRow(ctx,
		`SELECT status FROM models_canonical WHERE canonical_name = $1`, req.CanonicalModel).Scan(&status)
	if err != nil {
		writeJSONErr(w, http.StatusBadRequest,
			fmt.Sprintf("canonical_model %q not found: %v", req.CanonicalModel, err))
		return
	}
	if status != "active" {
		writeJSONErr(w, http.StatusBadRequest,
			fmt.Sprintf("canonical_model %q status is %q, must be active", req.CanonicalModel, status))
		return
	}

	var newID int64
	err = tx.QueryRow(ctx, `
		INSERT INTO task_default_routing
		  (task_type, profile, tier, canonical_model, tenant_id, priority, reason, created_by, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id`,
		req.TaskType, req.Profile, req.Tier, req.CanonicalModel, req.TenantID,
		req.Priority, req.Reason, createdBy, req.ExpiresAt,
	).Scan(&newID)
	if err != nil {
		// 唯一约束冲突 → 409
		if strings.Contains(err.Error(), "uq_task_default_routing") {
			writeJSONErr(w, http.StatusConflict,
				"a default for the same (task_type, profile, tier, tenant) already exists")
			return
		}
		writeInternalErr(w, err)
		return
	}

	// 审计
	if _, err := tx.Exec(ctx, `
		INSERT INTO task_default_routing_audit
		  (action, routing_id, task_type, profile, tier, canonical_model, tenant_id, priority, reason, expires_at, actor)
		VALUES ('insert', $1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		newID, req.TaskType, req.Profile, req.Tier, req.CanonicalModel, req.TenantID,
		req.Priority, req.Reason, req.ExpiresAt, createdBy); err != nil {
		writeInternalErr(w, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeInternalErr(w, err)
		return
	}

	h.writeAuditLog(r, "default_routing.create", newID, map[string]any{
		"task_type": req.TaskType, "profile": req.Profile, "tier": req.Tier,
		"canonical_model": req.CanonicalModel, "ip": clientIP(r),
	})

	writeJSON(w, http.StatusCreated, map[string]any{
		"id":      newID,
		"status":  "created",
		"message": "default created. DefaultRoutingStore refreshes on the next 1-min reload (within 60s).",
	})
}

// ── DELETE ──────────────────────────────────────────────────────

func (h *AutoRouteHandlers) deleteDefaultRouting(w http.ResponseWriter, r *http.Request, id int64) {
	createdBy := requestUser(r)
	if createdBy == "" {
		createdBy = "admin"
	}
	ctx := r.Context()
	tx, err := h.db.Begin(ctx)
	if err != nil {
		writeInternalErr(w, err)
		return
	}
	//nolint:errcheck // deferred rollback
	defer tx.Rollback(ctx)

	var dr DefaultRoutingWire
	err = tx.QueryRow(ctx, `
		SELECT id, task_type, profile, tier, canonical_model, tenant_id, priority, reason, expires_at
		FROM task_default_routing WHERE id = $1`, id).
		Scan(&dr.ID, &dr.TaskType, &dr.Profile, &dr.Tier, &dr.CanonicalModel,
			&dr.TenantID, &dr.Priority, &dr.Reason, &dr.ExpiresAt)
	if err != nil {
		writeJSONErr(w, http.StatusNotFound, fmt.Sprintf("id %d not found", id))
		return
	}

	if _, err := tx.Exec(ctx, `DELETE FROM task_default_routing WHERE id = $1`, id); err != nil {
		writeInternalErr(w, err)
		return
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO task_default_routing_audit
		  (action, routing_id, task_type, profile, tier, canonical_model, tenant_id, priority, reason, expires_at, actor)
		VALUES ('delete', $1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		id, dr.TaskType, dr.Profile, dr.Tier, dr.CanonicalModel, dr.TenantID,
		dr.Priority, dr.Reason, dr.ExpiresAt, createdBy); err != nil {
		writeInternalErr(w, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeInternalErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "status": "deleted"})
}

// ── PATCH (update) ──────────────────────────────────────────────

func (h *AutoRouteHandlers) updateDefaultRouting(w http.ResponseWriter, r *http.Request, id int64) {
	var req DefaultRoutingUpdateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONErr(w, http.StatusBadRequest, fmt.Sprintf("invalid body: %v", err))
		return
	}
	if req.Tier != nil && !validDefaultTiers[*req.Tier] {
		writeJSONErr(w, http.StatusBadRequest, "tier must be primary/secondary/fallback")
		return
	}
	createdBy := requestUser(r)
	if createdBy == "" {
		createdBy = "admin"
	}
	ctx := r.Context()
	tx, err := h.db.Begin(ctx)
	if err != nil {
		writeInternalErr(w, err)
		return
	}
	//nolint:errcheck // deferred rollback
	defer tx.Rollback(ctx)

	res, err := tx.Exec(ctx, `
		UPDATE task_default_routing SET
		  tier        = COALESCE($2, tier),
		  priority    = COALESCE($3, priority),
		  reason      = COALESCE($4, reason),
		  expires_at  = COALESCE($5, expires_at),
		  updated_at  = NOW()
		WHERE id = $1`,
		id, req.Tier, req.Priority, req.Reason, req.ExpiresAt)
	if err != nil {
		writeInternalErr(w, err)
		return
	}
	if res.RowsAffected() == 0 {
		writeJSONErr(w, http.StatusNotFound, fmt.Sprintf("id %d not found", id))
		return
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO task_default_routing_audit
		  (action, routing_id, actor, reason, expires_at)
		VALUES ('update', $1, $2, $3, $4)`,
		id, createdBy, req.Reason, req.ExpiresAt); err != nil {
		writeInternalErr(w, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeInternalErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "status": "updated"})
}

// ── GET /audit ──────────────────────────────────────────────────

func (h *AutoRouteHandlers) listDefaultRoutingAudit(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.Query(r.Context(), `
		SELECT id, ts, action, routing_id, task_type, profile, tier,
		       canonical_model, tenant_id, priority, reason, expires_at, actor
		FROM task_default_routing_audit
		ORDER BY ts DESC LIMIT 500`)
	if err != nil {
		writeInternalErr(w, err)
		return
	}
	defer rows.Close()

	type auditRow struct {
		ID        int64      `json:"id"`
		TS        time.Time  `json:"ts"`
		Action    string     `json:"action"`
		RoutingID *int64     `json:"routing_id,omitempty"`
		TaskType  *string    `json:"task_type,omitempty"`
		Profile   *string    `json:"profile,omitempty"`
		Tier      *string    `json:"tier,omitempty"`
		Model     *string    `json:"canonical_model,omitempty"`
		TenantID  *int64     `json:"tenant_id,omitempty"`
		Priority  *int       `json:"priority,omitempty"`
		Reason    *string    `json:"reason,omitempty"`
		ExpiresAt *time.Time `json:"expires_at,omitempty"`
		Actor     *string    `json:"actor,omitempty"`
	}
	out := make([]auditRow, 0)
	for rows.Next() {
		var a auditRow
		if err := rows.Scan(&a.ID, &a.TS, &a.Action, &a.RoutingID, &a.TaskType,
			&a.Profile, &a.Tier, &a.Model, &a.TenantID, &a.Priority,
			&a.Reason, &a.ExpiresAt, &a.Actor); err != nil {
			writeInternalErr(w, err)
			return
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		writeInternalErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"audit": out, "count": len(out)})
}
