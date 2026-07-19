package admin

// auto_route_defaults.go — task_default_routing admin CRUD.
//
// Endpoints (mounted under /api/admin/auto-route/defaults):
//   GET    /api/admin/auto-route/defaults          list (task_type/profile/active filters)
//   POST   /api/admin/auto-route/defaults          create
//   DELETE /api/admin/auto-route/defaults/:id      delete (with audit)
//   PATCH  /api/admin/auto-route/defaults/:id      update (tier/priority/reason/profile/model/tenant/expires)
//   GET    /api/admin/auto-route/defaults/audit    audit list
//
// Permission: wrapped with superAdmin (same as RegisterAutoRouteRoutes).

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// DefaultRoutingWire is the JSON format for task_default_routing rows.
type DefaultRoutingWire struct {
	ID             int64      `json:"id"`
	TaskType       string     `json:"task_type"`
	Profile        string     `json:"profile"`
	Tier           string     `json:"tier"`
	CanonicalModel string     `json:"canonical_model"`
	TenantID       *string    `json:"tenant_id,omitempty"`
	Priority       int        `json:"priority"`
	Reason         string     `json:"reason"`
	CreatedBy      *string    `json:"created_by,omitempty"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// DefaultRoutingCreateReq is the POST body.
type DefaultRoutingCreateReq struct {
	TaskType       string     `json:"task_type"`
	Profile        string     `json:"profile"`
	Tier           string     `json:"tier"`
	CanonicalModel string     `json:"canonical_model"`
	TenantID       *string    `json:"tenant_id,omitempty"`
	Priority       int        `json:"priority"`
	Reason         string     `json:"reason"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
}

// DefaultRoutingUpdateReq is the PATCH body (all optional).
// clear_tenant / clear_expires allow explicit nulling (COALESCE cannot clear).
type DefaultRoutingUpdateReq struct {
	Tier           *string    `json:"tier,omitempty"`
	Priority       *int       `json:"priority,omitempty"`
	Reason         *string    `json:"reason,omitempty"`
	Profile        *string    `json:"profile,omitempty"`
	CanonicalModel *string    `json:"canonical_model,omitempty"`
	TenantID       *string    `json:"tenant_id,omitempty"`
	ClearTenant    bool       `json:"clear_tenant,omitempty"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	ClearExpires   bool       `json:"clear_expires,omitempty"`
}

var validDefaultTiers = map[string]bool{"primary": true, "secondary": true, "fallback": true}

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

// HandleDefaultRoutingItem: DELETE / PATCH /:id and /audit sub-path.
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
	switch r.Method {
	case http.MethodDelete:
		h.deleteDefaultRouting(w, r, id)
	case http.MethodPatch:
		h.updateDefaultRouting(w, r, id)
	default:
		writeJSONErrCtx(w, r, http.StatusMethodNotAllowed, "admin_method_not_allowed")
	}
}

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
	sb.WriteString(` ORDER BY task_type, profile, COALESCE(tenant_id,''), priority DESC, id ASC`)

	rows, err := h.db.Query(r.Context(), sb.String(), args...)
	if err != nil {
		writeInternalErr(w, err)
		return
	}
	defer rows.Close()

	out := make([]DefaultRoutingWire, 0)
	for rows.Next() {
		var row DefaultRoutingWire
		if err := rows.Scan(
			&row.ID, &row.TaskType, &row.Profile, &row.Tier, &row.CanonicalModel,
			&row.TenantID, &row.Priority, &row.Reason, &row.CreatedBy,
			&row.ExpiresAt, &row.CreatedAt, &row.UpdatedAt,
		); err != nil {
			writeInternalErr(w, err)
			return
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		writeInternalErr(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"defaults": out,
		"count":    len(out),
		"filter": map[string]string{
			"task_type": taskType,
			"profile":   profile,
			"active":    strconv.FormatBool(activeOnly),
		},
	})
}

func (h *AutoRouteHandlers) createDefaultRouting(w http.ResponseWriter, r *http.Request) {
	var req DefaultRoutingCreateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONErr(w, http.StatusBadRequest, fmt.Sprintf("invalid body: %v", err))
		return
	}
	req.TaskType = strings.TrimSpace(req.TaskType)
	req.CanonicalModel = strings.TrimSpace(req.CanonicalModel)
	req.Profile = strings.TrimSpace(req.Profile)
	req.Tier = strings.TrimSpace(req.Tier)
	req.Reason = strings.TrimSpace(req.Reason)
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
		writeJSONErr(w, http.StatusBadRequest, "profile must be ''/smart/speed_first/cost_first")
		return
	}
	if req.Priority == 0 {
		req.Priority = 100
	}
	if req.TenantID != nil {
		trimmed := strings.TrimSpace(*req.TenantID)
		if trimmed == "" {
			req.TenantID = nil
		} else {
			req.TenantID = &trimmed
		}
	}

	if err := h.validateCanonicalModelActive(r, req.CanonicalModel); err != nil {
		writeJSONErr(w, http.StatusBadRequest, err.Error())
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
	defer tx.Rollback(ctx) //nolint:errcheck

	var newID int64
	err = tx.QueryRow(ctx, `
		INSERT INTO task_default_routing
		  (task_type, profile, tier, canonical_model, tenant_id, priority, reason, created_by, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		RETURNING id`,
		req.TaskType, req.Profile, req.Tier, req.CanonicalModel, req.TenantID,
		req.Priority, req.Reason, createdBy, req.ExpiresAt,
	).Scan(&newID)
	if err != nil {
		if isUniqueViolation(err) {
			writeJSONErr(w, http.StatusConflict,
				"a default with the same (task_type, profile, tier, tenant_id) already exists")
			return
		}
		writeInternalErr(w, err)
		return
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO task_default_routing_audit
		  (action, routing_id, task_type, profile, tier, canonical_model, tenant_id, priority, reason, expires_at, actor)
		VALUES ('insert',$1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		newID, req.TaskType, req.Profile, req.Tier, req.CanonicalModel, req.TenantID,
		req.Priority, req.Reason, req.ExpiresAt, createdBy,
	); err != nil {
		writeInternalErr(w, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeInternalErr(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"id":      newID,
		"status":  "created",
		"message": "default routing created; hot path refreshes within ~1 minute",
	})
}

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
	defer tx.Rollback(ctx) //nolint:errcheck

	var row DefaultRoutingWire
	err = tx.QueryRow(ctx, `
		SELECT id, task_type, profile, tier, canonical_model, tenant_id, priority, reason, expires_at
		FROM task_default_routing WHERE id = $1`, id).Scan(
		&row.ID, &row.TaskType, &row.Profile, &row.Tier, &row.CanonicalModel,
		&row.TenantID, &row.Priority, &row.Reason, &row.ExpiresAt,
	)
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
		VALUES ('delete',$1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		row.ID, row.TaskType, row.Profile, row.Tier, row.CanonicalModel, row.TenantID,
		row.Priority, row.Reason, row.ExpiresAt, createdBy,
	); err != nil {
		writeInternalErr(w, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeInternalErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "status": "deleted"})
}

func (h *AutoRouteHandlers) updateDefaultRouting(w http.ResponseWriter, r *http.Request, id int64) {
	var req DefaultRoutingUpdateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONErr(w, http.StatusBadRequest, fmt.Sprintf("invalid body: %v", err))
		return
	}
	if req.Tier != nil {
		*req.Tier = strings.TrimSpace(*req.Tier)
		if !validDefaultTiers[*req.Tier] {
			writeJSONErr(w, http.StatusBadRequest, "tier must be primary/secondary/fallback")
			return
		}
	}
	if req.Profile != nil {
		*req.Profile = strings.TrimSpace(*req.Profile)
		if !validDefaultProfiles[*req.Profile] {
			writeJSONErr(w, http.StatusBadRequest, "profile must be ''/smart/speed_first/cost_first")
			return
		}
	}
	if req.CanonicalModel != nil {
		*req.CanonicalModel = strings.TrimSpace(*req.CanonicalModel)
		if *req.CanonicalModel == "" {
			writeJSONErr(w, http.StatusBadRequest, "canonical_model cannot be empty")
			return
		}
		if err := h.validateCanonicalModelActive(r, *req.CanonicalModel); err != nil {
			writeJSONErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if req.Reason != nil {
		*req.Reason = strings.TrimSpace(*req.Reason)
	}
	if req.ClearTenant {
		req.TenantID = nil
	} else if req.TenantID != nil {
		trimmed := strings.TrimSpace(*req.TenantID)
		if trimmed == "" {
			req.ClearTenant = true
			req.TenantID = nil
		} else {
			req.TenantID = &trimmed
		}
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
	defer tx.Rollback(ctx) //nolint:errcheck

	res, err := tx.Exec(ctx, `
		UPDATE task_default_routing SET
		  tier            = COALESCE($2, tier),
		  priority        = COALESCE($3, priority),
		  reason          = COALESCE($4, reason),
		  profile         = COALESCE($5, profile),
		  canonical_model = COALESCE($6, canonical_model),
		  tenant_id       = CASE
		                      WHEN $7::boolean THEN NULL
		                      WHEN $8::boolean THEN $9
		                      ELSE tenant_id
		                    END,
		  expires_at      = CASE
		                      WHEN $10::boolean THEN NULL
		                      WHEN $11::boolean THEN $12
		                      ELSE expires_at
		                    END,
		  updated_at      = NOW()
		WHERE id = $1`,
		id,
		req.Tier,
		req.Priority,
		req.Reason,
		req.Profile,
		req.CanonicalModel,
		req.ClearTenant,
		req.TenantID != nil,
		req.TenantID,
		req.ClearExpires,
		req.ExpiresAt != nil,
		req.ExpiresAt,
	)
	if err != nil {
		if isUniqueViolation(err) {
			writeJSONErr(w, http.StatusConflict,
				"a default with the same (task_type, profile, tier, tenant_id) already exists")
			return
		}
		writeInternalErr(w, err)
		return
	}
	if res.RowsAffected() == 0 {
		writeJSONErr(w, http.StatusNotFound, fmt.Sprintf("id %d not found", id))
		return
	}

	var row DefaultRoutingWire
	if err := tx.QueryRow(ctx, `
		SELECT id, task_type, profile, tier, canonical_model, tenant_id, priority, reason, expires_at
		FROM task_default_routing WHERE id = $1`, id).Scan(
		&row.ID, &row.TaskType, &row.Profile, &row.Tier, &row.CanonicalModel,
		&row.TenantID, &row.Priority, &row.Reason, &row.ExpiresAt,
	); err != nil {
		writeInternalErr(w, err)
		return
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO task_default_routing_audit
		  (action, routing_id, task_type, profile, tier, canonical_model, tenant_id, priority, reason, expires_at, actor)
		VALUES ('update',$1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		row.ID, row.TaskType, row.Profile, row.Tier, row.CanonicalModel, row.TenantID,
		row.Priority, row.Reason, row.ExpiresAt, createdBy,
	); err != nil {
		writeInternalErr(w, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeInternalErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "status": "updated"})
}

func (h *AutoRouteHandlers) listDefaultRoutingAudit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONErrCtx(w, r, http.StatusMethodNotAllowed, "admin_method_not_allowed")
		return
	}
	rows, err := h.db.Query(r.Context(), `
		SELECT id, ts, action, routing_id, task_type, profile, tier, canonical_model,
		       tenant_id, priority, reason, expires_at, actor
		FROM task_default_routing_audit
		ORDER BY ts DESC
		LIMIT 500`)
	if err != nil {
		writeInternalErr(w, err)
		return
	}
	defer rows.Close()

	type auditRow struct {
		ID             int64      `json:"id"`
		TS             time.Time  `json:"ts"`
		Action         string     `json:"action"`
		RoutingID      *int64     `json:"routing_id,omitempty"`
		TaskType       *string    `json:"task_type,omitempty"`
		Profile        *string    `json:"profile,omitempty"`
		Tier           *string    `json:"tier,omitempty"`
		CanonicalModel *string    `json:"canonical_model,omitempty"`
		TenantID       *string    `json:"tenant_id,omitempty"`
		Priority       *int       `json:"priority,omitempty"`
		Reason         *string    `json:"reason,omitempty"`
		ExpiresAt      *time.Time `json:"expires_at,omitempty"`
		Actor          *string    `json:"actor,omitempty"`
	}
	out := make([]auditRow, 0)
	for rows.Next() {
		var row auditRow
		if err := rows.Scan(
			&row.ID, &row.TS, &row.Action, &row.RoutingID, &row.TaskType, &row.Profile,
			&row.Tier, &row.CanonicalModel, &row.TenantID, &row.Priority, &row.Reason,
			&row.ExpiresAt, &row.Actor,
		); err != nil {
			writeInternalErr(w, err)
			return
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		writeInternalErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"audit": out, "count": len(out)})
}

func (h *AutoRouteHandlers) validateCanonicalModelActive(r *http.Request, name string) error {
	var status string
	err := h.db.QueryRow(r.Context(), `
		SELECT COALESCE(status, 'active') FROM models_canonical
		WHERE lower(canonical_name) = lower($1)
		LIMIT 1`, name).Scan(&status)
	if err != nil {
		return fmt.Errorf("canonical_model %q not found in models_canonical", name)
	}
	if status != "active" {
		return fmt.Errorf("canonical_model %q is not active (status=%s)", name, status)
	}
	return nil
}
