package admin

// routing_overrides.go — P7.6: admin endpoints for routing overrides.
//
// Endpoints:
//
//	GET    /api/admin/routing/overrides?active=true
//	POST   /api/admin/routing/overrides              — create
//	DELETE /api/admin/routing/overrides/:id         — delete (soft: set expires_at)
//	PATCH  /api/admin/routing/overrides/:id/extend  — extend/reduce expires_at
//
// All routes use the standard adminWrap middleware (bearer token).
// Validation is strict: missing fields → 400, invalid mode → 400,
// missing model_chosen for ban → 400.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// OverrideWire is the JSON wire format for routing_overrides rows.
type OverrideWire struct {
	ID          int64      `json:"id"`
	TaskType    string     `json:"task_type"`
	Profile     string     `json:"profile"`
	Mode        string     `json:"mode"`
	ModelChosen *string    `json:"model_chosen,omitempty"`
	Reason      string     `json:"reason"`
	CreatedBy   *string    `json:"created_by,omitempty"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// OverrideCreateReq is the JSON body for POST /overrides.
type OverrideCreateReq struct {
	TaskType    string     `json:"task_type"`
	Profile     string     `json:"profile"`
	Mode        string     `json:"mode"`
	ModelChosen *string    `json:"model_chosen,omitempty"`
	Reason      string     `json:"reason"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
}

// jsonDecode is a thin wrapper around json.NewDecoder for clarity
// in handlers.
func jsonDecode(r *http.Request, dst any) error {
	return json.NewDecoder(r.Body).Decode(dst)
}

// ── handlers ─────────────────────────────────────────────────────

// handleRoutingOverridesCollection: GET (list) and POST (create).
func (h *AutoRouteHandlers) handleRoutingOverridesCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.listRoutingOverrides(w, r)
	case http.MethodPost:
		h.createRoutingOverride(w, r)
	default:
		writeJSONErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleRoutingOverrideItem: DELETE and PATCH /:id/extend.
func (h *AutoRouteHandlers) handleRoutingOverrideItem(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/admin/routing/overrides/")
	parts := strings.Split(path, "/")
	if len(parts) < 1 || parts[0] == "" {
		writeJSONErr(w, http.StatusBadRequest, "expected /:id or /:id/extend")
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		writeJSONErr(w, http.StatusBadRequest, "invalid override id")
		return
	}

	if len(parts) == 2 && parts[1] == "extend" {
		if r.Method != http.MethodPatch {
			writeJSONErr(w, http.StatusMethodNotAllowed, "extend requires PATCH")
			return
		}
		h.extendRoutingOverride(w, r, id)
		return
	}
	if len(parts) > 1 {
		writeJSONErr(w, http.StatusBadRequest, "unknown sub-path")
		return
	}

	switch r.Method {
	case http.MethodDelete:
		h.deleteRoutingOverride(w, r, id)
	default:
		writeJSONErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// ── GET ─────────────────────────────────────────────────────────

func (h *AutoRouteHandlers) listRoutingOverrides(w http.ResponseWriter, r *http.Request) {
	activeOnly := r.URL.Query().Get("active") == "true"
	taskType := r.URL.Query().Get("task_type")
	profile := r.URL.Query().Get("profile")

	q := `SELECT id, task_type, profile, mode, model_chosen, reason, created_by, expires_at, created_at, updated_at
	      FROM routing_overrides WHERE 1=1`
	args := []any{}
	if activeOnly {
		q += ` AND (expires_at IS NULL OR expires_at > NOW())`
	}
	if taskType != "" {
		args = append(args, taskType)
		q += fmt.Sprintf(" AND task_type = $%d", len(args))
	}
	if profile != "" {
		args = append(args, profile)
		q += fmt.Sprintf(" AND profile = $%d", len(args))
	}
	q += " ORDER BY task_type, profile, mode, model_chosen"

	rows, err := h.db.Query(r.Context(), q, args...)
	if err != nil {
		writeInternalErr(w, err)
		return
	}
	defer rows.Close()

	var out []OverrideWire
	for rows.Next() {
		var row OverrideWire
		var mode string
		if err := rows.Scan(&row.ID, &row.TaskType, &row.Profile, &mode,
			&row.ModelChosen, &row.Reason, &row.CreatedBy,
			&row.ExpiresAt, &row.CreatedAt, &row.UpdatedAt); err != nil {
			writeInternalErr(w, err)
			return
		}
		row.Mode = mode
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		writeInternalErr(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"overrides": out,
		"count":     len(out),
		"filter":    map[string]string{"task_type": taskType, "profile": profile, "active": strconv.FormatBool(activeOnly)},
	})
}

// ── POST ────────────────────────────────────────────────────────

func (h *AutoRouteHandlers) createRoutingOverride(w http.ResponseWriter, r *http.Request) {
	var req OverrideCreateReq
	if err := jsonDecode(r, &req); err != nil {
		writeJSONErr(w, http.StatusBadRequest, fmt.Sprintf("invalid body: %v", err))
		return
	}

	if strings.TrimSpace(req.TaskType) == "" {
		writeJSONErr(w, http.StatusBadRequest, "task_type is required")
		return
	}
	if req.Profile == "" {
		req.Profile = "smart"
	}
	if req.Profile != "smart" && req.Profile != "speed_first" && req.Profile != "cost_first" {
		writeJSONErr(w, http.StatusBadRequest, "profile must be smart| speed_first| cost_first")
		return
	}
	if req.Mode != "pin" && req.Mode != "ban" {
		writeJSONErr(w, http.StatusBadRequest, "mode must be 'pin' or 'ban'")
		return
	}
	if req.Mode == "ban" && (req.ModelChosen == nil || *req.ModelChosen == "") {
		writeJSONErr(w, http.StatusBadRequest, "ban mode requires model_chosen")
		return
	}
	if strings.TrimSpace(req.Reason) == "" {
		writeJSONErr(w, http.StatusBadRequest, "reason is required (audit trail)")
		return
	}

	createdBy := requestUser(r)
	if createdBy == "" {
		createdBy = "admin"
	}

	var newID int64
	err := h.db.QueryRow(r.Context(), `
		INSERT INTO routing_overrides
		    (task_type, profile, mode, model_chosen, reason, created_by, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id
	`, req.TaskType, req.Profile, req.Mode, req.ModelChosen, req.Reason, createdBy, req.ExpiresAt).Scan(&newID)
	if err != nil {
		if isUniqueViolation(err) {
			writeJSONErr(w, http.StatusConflict, "an override with the same (task_type, profile, model_chosen, mode) already exists")
			return
		}
		writeInternalErr(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"id":      newID,
		"status":  "created",
		"message": "override created. OverrideStore refreshes on the next 1-min reload (within 60s).",
	})
}

// ── DELETE (soft) ──────────────────────────────────────────────

func (h *AutoRouteHandlers) deleteRoutingOverride(w http.ResponseWriter, r *http.Request, id int64) {
	// Soft delete: set expires_at to 1s in the past.
	res, err := h.db.Exec(r.Context(), `
		UPDATE routing_overrides
		SET expires_at = NOW() - INTERVAL '1 second',
		    updated_at = NOW()
		WHERE id = $1
		  AND (expires_at IS NULL OR expires_at > NOW())
	`, id)
	if err != nil {
		writeInternalErr(w, err)
		return
	}
	if res.RowsAffected() == 0 {
		writeJSONErr(w, http.StatusNotFound, "override not found or already expired")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":     id,
		"status": "expired",
		"note":   "soft-deleted; OverrideStore filter excludes expires_at <= now",
	})
}

// ── PATCH /:id/extend ──────────────────────────────────────────

func (h *AutoRouteHandlers) extendRoutingOverride(w http.ResponseWriter, r *http.Request, id int64) {
	var body struct {
		ExpiresAt *time.Time `json:"expires_at"`
	}
	if err := jsonDecode(r, &body); err != nil {
		writeJSONErr(w, http.StatusBadRequest, fmt.Sprintf("invalid body: %v", err))
		return
	}
	if body.ExpiresAt != nil && body.ExpiresAt.Before(time.Now()) {
		writeJSONErr(w, http.StatusBadRequest, "expires_at must be in the future")
		return
	}

	res, err := h.db.Exec(r.Context(), `
		UPDATE routing_overrides
		SET expires_at = $2, updated_at = NOW()
		WHERE id = $1
	`, id, body.ExpiresAt)
	if err != nil {
		writeInternalErr(w, err)
		return
	}
	if res.RowsAffected() == 0 {
		writeJSONErr(w, http.StatusNotFound, "override not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":     id,
		"status": "updated",
	})
}

// ── helpers ─────────────────────────────────────────────────────

// requestUser pulls the admin username from the request context.
func requestUser(r *http.Request) string {
	if v := r.Context().Value("admin_user"); v != nil {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// isUniqueViolation returns true if err is a Postgres unique-constraint
// violation (SQLSTATE 23505).
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	var pgErr *pgconn.PgError
	if errorsAsPgError(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	// Substring fallback (works for wrapped errors)
	return strings.Contains(err.Error(), "duplicate key") ||
		strings.Contains(err.Error(), "unique constraint") ||
		strings.Contains(err.Error(), "23505")
}

// errorsAsPgError is a tiny shim to avoid the pgx errors.As import
// in this file. Returns true if err is a pgconn.PgError.
func errorsAsPgError(err error, target **pgconn.PgError) bool {
	if pgErr, ok := err.(*pgconn.PgError); ok {
		*target = pgErr
		return true
	}
	return false
}
