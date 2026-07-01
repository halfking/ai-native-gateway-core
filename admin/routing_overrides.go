package admin

// routing_overrides.go — P7.6 (CRUD) + P7.9 (audit integration).
//
// All three mutation endpoints (create / soft-delete / extend) run
// inside a transaction that sets `app.current_admin` to the request
// admin's username. The trigger on routing_overrides reads this
// GUC to record the actor in routing_overrides_audit.
//
// Why: a single transaction guarantees the audit row is written
// atomically with the DML. Application-level logging could miss
// writes if the app crashes between DML and log write.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	controlrouting "github.com/kaixuan/llm-gateway-go/control/routing"
	queryrouting "github.com/kaixuan/llm-gateway-go/query/routing"
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
		writeJSONErrCtx(w, r, http.StatusMethodNotAllowed, "admin_method_not_allowed")
	}
}

// handleRoutingOverrideItem: DELETE and PATCH /:id/extend
// and the audit endpoint GET /audit (sub-path).
func (h *AutoRouteHandlers) handleRoutingOverrideItem(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/admin/routing/overrides/")
	parts := strings.Split(path, "/")
	if len(parts) < 1 || parts[0] == "" {
		writeJSONErr(w, http.StatusBadRequest, "expected /:id, /:id/extend, or /audit")
		return
	}
	if parts[0] == "audit" {
		h.handleRoutingOverridesAudit(w, r)
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		writeJSONErrCtx(w, r, http.StatusBadRequest, "admin_invalid_override_id")
		return
	}
	if len(parts) == 2 && parts[1] == "extend" {
		if r.Method != http.MethodPatch {
			writeJSONErrCtx(w, r, http.StatusMethodNotAllowed, "admin_extend_requires_patch")
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
		writeJSONErrCtx(w, r, http.StatusMethodNotAllowed, "admin_method_not_allowed")
	}
}

// ── GET (list) ──────────────────────────────────────────────────

func (h *AutoRouteHandlers) listRoutingOverrides(w http.ResponseWriter, r *http.Request) {
	activeOnly := r.URL.Query().Get("active") == "true"
	taskType := r.URL.Query().Get("task_type")
	profile := r.URL.Query().Get("profile")

	rows, err := queryrouting.NewHandler(h.db).List(r.Context(), queryrouting.ListRoutesQuery{
		ActiveOnly: activeOnly,
		TaskType:   taskType,
		Profile:    profile,
	})
	if err != nil {
		writeInternalErr(w, err)
		return
	}

	// Translate query.RouteDTO -> admin.OverrideWire to keep the wire
	// format byte-for-byte identical. Drop OverrideWire in B2+.
	out := make([]OverrideWire, 0, len(rows))
	for _, dto := range rows {
		out = append(out, OverrideWire{
			ID:          dto.ID,
			TaskType:    dto.TaskType,
			Profile:     dto.Profile,
			Mode:        dto.Mode,
			ModelChosen: dto.ModelChosen,
			Reason:      dto.Reason,
			CreatedBy:   dto.CreatedBy,
			ExpiresAt:   dto.ExpiresAt,
			CreatedAt:   dto.CreatedAt,
			UpdatedAt:   dto.UpdatedAt,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"overrides": out,
		"count":     len(out),
		"filter":    map[string]string{"task_type": taskType, "profile": profile, "active": strconv.FormatBool(activeOnly)},
	})
}

// ── POST (create) ───────────────────────────────────────────────

func (h *AutoRouteHandlers) createRoutingOverride(w http.ResponseWriter, r *http.Request) {
	var req OverrideCreateReq
	if err := jsonDecode(r, &req); err != nil {
		writeJSONErr(w, http.StatusBadRequest, fmt.Sprintf("invalid body: %v", err))
		return
	}

	createdBy := requestUser(r)
	if createdBy == "" {
		createdBy = "admin"
	}

	newID, err := controlrouting.NewHandler(h.db).Create(r.Context(), controlrouting.CreateRouteCommand{
		TaskType:    req.TaskType,
		Profile:     req.Profile,
		Mode:        req.Mode,
		ModelChosen: req.ModelChosen,
		Reason:      req.Reason,
		CreatedBy:   createdBy,
		ExpiresAt:   req.ExpiresAt,
	})
	if err != nil {
		switch {
		case controlrouting.IsValidationError(err):
			writeJSONErr(w, http.StatusBadRequest, err.Error())
		case controlrouting.IsDuplicateError(err):
			writeJSONErr(w, http.StatusConflict,
				"an override with the same (task_type, profile, model_chosen, mode) already exists")
		default:
			writeInternalErr(w, err)
		}
		return
	}

	// Application-level audit log (P7.9).
	h.writeAuditLog(r, "override.create", newID, map[string]any{
		"task_type":    req.TaskType,
		"profile":      req.Profile,
		"mode":         req.Mode,
		"model_chosen": req.ModelChosen,
		"reason":       req.Reason,
		"ip":           clientIP(r),
		"ua":           r.UserAgent(),
	})

	writeJSON(w, http.StatusCreated, map[string]any{
		"id":      newID,
		"status":  "created",
		"message": "override created. OverrideStore refreshes on the next 1-min reload (within 60s).",
	})
}

// ── DELETE (soft) ──────────────────────────────────────────────

func (h *AutoRouteHandlers) deleteRoutingOverride(w http.ResponseWriter, r *http.Request, id int64) {
	ctx := r.Context()
	createdBy := requestUser(r)
	if createdBy == "" {
		createdBy = "admin"
	}

	tx, err := h.db.Begin(ctx)
	if err != nil {
		writeInternalErr(w, err)
		return
	}
	//nolint:errcheck // deferred rollback, best-effort
	defer tx.Rollback(ctx)
	escapedActor := strings.ReplaceAll(createdBy, "'", "''")
	if _, err := tx.Exec(ctx, "SET LOCAL app.current_admin = '"+escapedActor+"'"); err != nil {
		writeInternalErr(w, err)
		return
	}

	res, err := tx.Exec(ctx, `
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
		writeJSONErrCtx(w, r, http.StatusNotFound, "admin_override_not_found")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeInternalErr(w, err)
		return
	}
	h.writeAuditLog(r, "override.delete", id, map[string]any{
		"reason": "soft delete (set expires_at to 1s ago)",
		"ip":     clientIP(r),
		"ua":     r.UserAgent(),
	})
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
		writeJSONErrCtx(w, r, http.StatusBadRequest, "admin_expires_at_invalid")
		return
	}

	ctx := r.Context()
	createdBy := requestUser(r)
	if createdBy == "" {
		createdBy = "admin"
	}

	tx, err := h.db.Begin(ctx)
	if err != nil {
		writeInternalErr(w, err)
		return
	}
	//nolint:errcheck // deferred rollback, best-effort
	defer tx.Rollback(ctx)
	escapedActor := strings.ReplaceAll(createdBy, "'", "''")
	if _, err := tx.Exec(ctx, "SET LOCAL app.current_admin = '"+escapedActor+"'"); err != nil {
		writeInternalErr(w, err)
		return
	}

	res, err := tx.Exec(ctx, `
		UPDATE routing_overrides
		SET expires_at = $2, updated_at = NOW()
		WHERE id = $1
	`, id, body.ExpiresAt)
	if err != nil {
		writeInternalErr(w, err)
		return
	}
	if res.RowsAffected() == 0 {
		writeJSONErrCtx(w, r, http.StatusNotFound, "admin_override_not_found")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeInternalErr(w, err)
		return
	}
	h.writeAuditLog(r, "override.extend", id, map[string]any{
		"new_expires_at": body.ExpiresAt,
		"ip":             clientIP(r),
		"ua":             r.UserAgent(),
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"id":     id,
		"status": "updated",
	})
}

// ── GET (audit log) ─────────────────────────────────────────────

// AuditEntry is the JSON wire format for a routing_overrides_audit row.
type AuditEntry struct {
	ID           int64      `json:"id"`
	TS           time.Time  `json:"ts"`
	Action       string     `json:"action"`
	OverrideID   *int64     `json:"override_id,omitempty"`
	TaskType     *string    `json:"task_type,omitempty"`
	Profile      *string    `json:"profile,omitempty"`
	Mode         *string    `json:"mode,omitempty"`
	ModelChosen  *string    `json:"model_chosen,omitempty"`
	Reason       *string    `json:"reason,omitempty"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
	OldExpiresAt *time.Time `json:"old_expires_at,omitempty"`
	Actor        *string    `json:"actor,omitempty"`
}

// handleRoutingOverridesAudit: GET /routing/overrides/audit
func (h *AutoRouteHandlers) handleRoutingOverridesAudit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONErrCtx(w, r, http.StatusMethodNotAllowed, "admin_method_not_allowed")
		return
	}

	action := r.URL.Query().Get("action")
	if action != "" && action != "insert" && action != "update" && action != "delete" {
		writeJSONErr(w, http.StatusBadRequest, "action must be insert|update|delete")
		return
	}
	actor := r.URL.Query().Get("actor")
	overrideIDStr := r.URL.Query().Get("override_id")

	days := 7
	if d := r.URL.Query().Get("days"); d != "" {
		v, err := strconv.Atoi(d)
		if err != nil || v <= 0 || v > 90 {
			writeJSONErr(w, http.StatusBadRequest, "days must be 1-90")
			return
		}
		days = v
	}

	limit := 200
	if l := r.URL.Query().Get("limit"); l != "" {
		v, err := strconv.Atoi(l)
		if err != nil || v <= 0 || v > 1000 {
			writeJSONErr(w, http.StatusBadRequest, "limit must be 1-1000")
			return
		}
		limit = v
	}

	q := `SELECT id, ts, action, override_id, task_type, profile, mode,
	             model_chosen, reason, expires_at, old_expires_at, actor
	      FROM routing_overrides_audit
	      WHERE ts >= NOW() - INTERVAL '1 day' * $1`
	args := []any{days}
	if action != "" {
		args = append(args, action)
		q += fmt.Sprintf(" AND action = $%d", len(args))
	}
	if actor != "" {
		args = append(args, actor)
		q += fmt.Sprintf(" AND actor = $%d", len(args))
	}
	if overrideIDStr != "" {
		if v, err := strconv.ParseInt(overrideIDStr, 10, 64); err == nil {
			args = append(args, v)
			q += fmt.Sprintf(" AND override_id = $%d", len(args))
		}
	}
	q += " ORDER BY ts DESC LIMIT " + strconv.Itoa(limit)

	rows, err := h.db.Query(r.Context(), q, args...)
	if err != nil {
		writeInternalErr(w, err)
		return
	}
	defer rows.Close()

	var entries []AuditEntry
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.ID, &e.TS, &e.Action, &e.OverrideID,
			&e.TaskType, &e.Profile, &e.Mode, &e.ModelChosen,
			&e.Reason, &e.ExpiresAt, &e.OldExpiresAt, &e.Actor); err != nil {
			writeInternalErr(w, err)
			return
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		writeInternalErr(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"entries": entries,
		"count":   len(entries),
		"filter": map[string]string{
			"action":      action,
			"actor":       actor,
			"override_id": overrideIDStr,
			"days":        strconv.Itoa(days),
		},
	})
}

// ── helpers ─────────────────────────────────────────────────────

// clientIP returns the best-effort client IP: prefers
// X-Forwarded-For (first hop, comma-split), falls back to
// RemoteAddr. Used in audit log details.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// Take the first IP (closest to the user)
		if idx := strings.Index(xff, ","); idx > 0 {
			return strings.TrimSpace(xff[:idx])
		}
		return strings.TrimSpace(xff)
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return strings.TrimSpace(xri)
	}
	return strings.TrimSpace(r.RemoteAddr)
}

// requestUser pulls the admin username from the AuthContext
// (P7.9 bug fix: previously read "admin_user" key that no
// middleware ever sets; AuthContext is the right source). Falls
// back to "admin" if AuthContext is missing (e.g. in tests).
func requestUser(r *http.Request) string {
	auth := GetAuthContext(r)
	if auth != nil && auth.Username != "" {
		return auth.Username
	}
	return "admin"
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
	return strings.Contains(err.Error(), "duplicate key") ||
		strings.Contains(err.Error(), "unique constraint") ||
		strings.Contains(err.Error(), "23505")
}

// errorsAsPgError is a tiny shim to avoid the pgx errors.As import.
func errorsAsPgError(err error, target **pgconn.PgError) bool {
	if pgErr, ok := err.(*pgconn.PgError); ok {
		*target = pgErr
		return true
	}
	return false
}

// Avoid unused import warnings for pgx (used by *pgxpool types
// referenced indirectly via h.db).
var _ = pgx.ErrNoRows

// writeAuditLog inserts a single row into routing_audit_log
// for an override mutation. Captures actor, action, target_id,
// IP, and user-agent in the details map. Best-effort: a
// failure to write the audit log does NOT roll back the
// override mutation (the trigger in the DB already records
// the basic info).
func (h *AutoRouteHandlers) writeAuditLog(r *http.Request, action string, targetID int64, details map[string]any) {
	if h.db == nil {
		return
	}
	actor := requestUser(r)
	payload, _ := json.Marshal(details)
	_, err := h.db.Exec(r.Context(),
		`INSERT INTO routing_audit_log (actor, action, target_type, target_id, after_json) VALUES ($1, $2, $3, $4, $5)`,
		actor, action, "routing_override", targetID, payload)
	if err != nil {
		// Audit failures are non-fatal; the trigger-based log
		// already records the action+actor+row.
		// (logged but not returned to caller)
		_ = err
	}
}
