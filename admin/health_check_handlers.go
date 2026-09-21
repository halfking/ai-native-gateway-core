package admin

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// HealthCheckHandler serves /admin/api/v1/health-checks/*
type HealthCheckHandler struct {
	db *pgxpool.Pool
}

func NewHealthCheckHandler(db *pgxpool.Pool) *HealthCheckHandler {
	return &HealthCheckHandler{db: db}
}

// List returns open findings, grouped by severity.
func (h *HealthCheckHandler) List(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	if status == "" {
		status = "open"
	}
	limit := 200

	rows, err := h.db.Query(r.Context(), `
		SELECT id, check_id, severity, entity_type, entity_id, entity_name,
		       detail, fix_sql, status, auto_fixed_at, auto_fix_result,
		       dismissed_at, dismissed_by, dismissed_reason,
		       created_at, updated_at
		FROM routing_health_checks
		WHERE status = $1 OR $1 = 'all'
		ORDER BY
			CASE severity WHEN 'critical' THEN 0 WHEN 'warning' THEN 1 ELSE 2 END,
			created_at DESC
		LIMIT $2`, status, limit)
	if err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	defer rows.Close()

	var results []map[string]any
	for rows.Next() {
		var id, entityID int64
		var checkID, severity, entityType, entityName, detail, fixSQL, itemStatus string
		var autoFixedAt *time.Time
		var autoFixResult, dismissedAt, dismissedBy, dismissedReason *string
		var createdAt, updatedAt time.Time

		if err := rows.Scan(
			&id, &checkID, &severity, &entityType, &entityID, &entityName,
			&detail, &fixSQL, &itemStatus, &autoFixedAt, &autoFixResult,
			&dismissedAt, &dismissedBy, &dismissedReason,
			&createdAt, &updatedAt); err != nil {
			continue
		}
		item := map[string]any{
			"id": id, "check_id": checkID, "severity": severity,
			"entity_type": entityType, "entity_id": entityID, "entity_name": entityName,
			"detail": detail, "fix_sql": fixSQL, "status": itemStatus,
			"created_at": createdAt, "updated_at": updatedAt,
		}
		if autoFixedAt != nil {
			item["auto_fixed_at"] = *autoFixedAt
		}
		if autoFixResult != nil {
			item["auto_fix_result"] = *autoFixResult
		}
		results = append(results, item)
	}

	summary := map[string]int{}
	allRows, _ := h.db.Query(r.Context(), `
		SELECT status, count(*) FROM routing_health_checks GROUP BY status`)
	if allRows != nil {
		defer allRows.Close()
		for allRows.Next() {
			var s string
			var c int
			if allRows.Scan(&s, &c) == nil {
				summary[s] = c
			}
		}
	}

	writeJSON(w, 200, map[string]any{"items": results, "summary": summary})
}

// Dismiss marks a finding as intentionally ignored.
func (h *HealthCheckHandler) Dismiss(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID     int64  `json:"id"`
		By     string `json:"by"`
		Reason string `json:"reason"`
	}
	if err := readJSONRequired(r, &body); err != nil {
		writeJSON(w, 400, map[string]any{"error": "invalid json"})
		return
	}
	if body.ID == 0 || body.By == "" {
		writeJSON(w, 400, map[string]any{"error": "id and by are required"})
		return
	}
	tag, err := h.db.Exec(r.Context(), `
		UPDATE routing_health_checks
		SET status = 'dismissed', dismissed_at = now(), dismissed_by = $1, dismissed_reason = $2, updated_at = now()
		WHERE id = $3 AND status = 'open'`, body.By, body.Reason, body.ID)
	if err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	if tag.RowsAffected() == 0 {
		writeJSON(w, 404, map[string]any{"error": "not found or already closed"})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// cannedFix maps a finding's entity_type to the parameterized statement that
// implements its one-click fix. The fix_sql column is display/copy text built
// by bg.RunChecks from DB string columns that upstream model catalogs can
// influence — executing it verbatim would be a stored-SQL injection, so only
// these canned statements (with entity_id as the sole argument) may run.
func cannedFix(entityType string) (string, bool) {
	switch entityType {
	case "billing_mismatch":
		// plan_type is re-read from the credential at fix time (fresher than
		// the check snapshot and never attacker-controlled: CHECK-constrained).
		return `UPDATE credential_model_bindings cmb
			SET billing_mode = c.plan_type, plan_type_origin = 'manual_fix', plan_type_updated_at = now()
			FROM credentials c
			WHERE c.id = cmb.credential_id AND cmb.id = $1`, true
	case "canonical_id_null":
		// Same exact-match semantics as bg.autoFixCanonicalID, including the
		// migration-693 admin-unbind guard.
		return `UPDATE provider_models pm
			SET canonical_id = mc.id
			FROM models_canonical mc
			WHERE mc.canonical_name = pm.raw_model_name
			  AND pm.canonical_cleared_at IS NULL
			  AND pm.id = $1`, true
	default:
		return "", false
	}
}

// ExecuteFix applies the fix for a finding via its canned parameterized
// statement. Stored fix_sql is never executed.
func (h *HealthCheckHandler) ExecuteFix(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID int64 `json:"id"`
	}
	if err := readJSONRequired(r, &body); err != nil {
		writeJSON(w, 400, map[string]any{"error": "invalid json"})
		return
	}
	status, resp := runHealthCheckFix(r.Context(), h.db, body.ID)
	writeJSON(w, status, resp)
}

// runHealthCheckFix is the pgxExecRower-parameterized core of ExecuteFix so
// tests can drive it against pgxmock.
func runHealthCheckFix(ctx context.Context, db pgxExecRower, id int64) (int, map[string]any) {
	var entityType string
	var entityID int64
	err := db.QueryRow(ctx, `
		SELECT entity_type, entity_id FROM routing_health_checks WHERE id = $1 AND status = 'open'`, id).Scan(&entityType, &entityID)
	if err != nil {
		return 404, map[string]any{"error": "not found or no fix available"}
	}
	stmt, ok := cannedFix(entityType)
	if !ok {
		return 400, map[string]any{"error": "this finding has no one-click fix; follow the guidance in detail", "entity_type": entityType}
	}
	tag, execErr := db.Exec(ctx, stmt, entityID)
	if execErr != nil {
		return 500, map[string]any{"error": execErr.Error()}
	}
	db.Exec(ctx, `
		UPDATE routing_health_checks SET status = 'manual_fixed', auto_fixed_at = now(), auto_fix_result = 'applied', updated_at = now() WHERE id = $1`, id)
	return 200, map[string]any{"ok": true, "rows_affected": tag.RowsAffected()}
}

func (h *HealthCheckHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin/api/v1/health-checks", h.List)
	mux.HandleFunc("POST /admin/api/v1/health-checks/dismiss", h.Dismiss)
	mux.HandleFunc("POST /admin/api/v1/health-checks/fix", h.ExecuteFix)
}
