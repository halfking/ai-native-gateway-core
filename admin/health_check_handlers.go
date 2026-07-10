package admin

import (
	"encoding/json"
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
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
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

// ExecuteFix applies the fix_sql of a finding.
func (h *HealthCheckHandler) ExecuteFix(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]any{"error": "invalid json"})
		return
	}
	var fixSQL string
	err := h.db.QueryRow(r.Context(), `
		SELECT fix_sql FROM routing_health_checks WHERE id = $1 AND status = 'open'`, body.ID).Scan(&fixSQL)
	if err != nil || fixSQL == "" {
		writeJSON(w, 404, map[string]any{"error": "not found or no fix_sql"})
		return
	}
	tag, execErr := h.db.Exec(r.Context(), fixSQL)
	if execErr != nil {
		writeJSON(w, 500, map[string]any{"error": execErr.Error(), "sql": fixSQL})
		return
	}
	h.db.Exec(r.Context(), `
		UPDATE routing_health_checks SET status = 'manual_fixed', auto_fixed_at = now(), auto_fix_result = 'applied', updated_at = now() WHERE id = $1`, body.ID)
	writeJSON(w, 200, map[string]any{"ok": true, "rows_affected": tag.RowsAffected()})
}

func (h *HealthCheckHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin/api/v1/health-checks", h.List)
	mux.HandleFunc("POST /admin/api/v1/health-checks/dismiss", h.Dismiss)
	mux.HandleFunc("POST /admin/api/v1/health-checks/fix", h.ExecuteFix)
}
