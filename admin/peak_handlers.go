// Package admin provides admin HTTP handlers for llm-gateway-go.
package admin

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PeakHandlers exposes peak statistics and auto-tune actions via the
// admin API. Mounted under /api/admin/*.
type PeakHandlers struct {
	db *pgxpool.Pool
}

// NewPeakHandlers creates a new handler set.
func NewPeakHandlers(db *pgxpool.Pool) *PeakHandlers {
	return &PeakHandlers{db: db}
}

// RegisterPeakRoutes registers peak-related admin routes onto the mux.
// The adminWrap function should be the bearer-token middleware.
func (h *PeakHandlers) RegisterPeakRoutes(mux *http.ServeMux, adminWrap func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("/api/admin/credential-model-peaks", adminWrap(h.handleGetPeaks))
	mux.HandleFunc("/api/admin/concurrency-limit/preview", adminWrap(h.handlePreview))
	mux.HandleFunc("/api/admin/concurrency-limit/apply", adminWrap(h.handleApply))
	mux.HandleFunc("/api/admin/auto-tune/audit", adminWrap(h.handleAuditLog))
	mux.HandleFunc("/api/admin/auto-tune/stats", adminWrap(h.handleStats))
}

func (h *PeakHandlers) handleGetPeaks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	credID := r.URL.Query().Get("credential_id")
	model := r.URL.Query().Get("model")
	weeks := r.URL.Query().Get("weeks")
	if weeks == "" {
		weeks = "4"
	}
	weeksInt, _ := strconv.Atoi(weeks)
	if weeksInt < 1 || weeksInt > 12 {
		weeksInt = 4
	}

	ctx := r.Context()
	query := `
		SELECT
			week_start, credential_id, raw_model,
			peak_concurrent, p95_concurrent, avg_concurrent,
			total_requests, sample_days, current_limit,
			suggested_limit, suggestion_reason, updated_at
		FROM credential_model_weekly_peak
		WHERE week_start >= NOW() - ($1 || ' weeks')::interval
	`
	args := []interface{}{strconv.Itoa(weeksInt)}
	if credID != "" {
		args = append(args, credID)
		query += " AND credential_id = $" + strconv.Itoa(len(args))
	}
	if model != "" {
		args = append(args, model)
		query += " AND raw_model = $" + strconv.Itoa(len(args))
	}
	query += " ORDER BY week_start DESC, peak_concurrent DESC LIMIT 200"

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	results := make([]map[string]interface{}, 0)
	for rows.Next() {
		var weekStart, updatedAt time.Time
		var credID int64
		var rawModel, reason string
		var peak, sampleDays, currentLimit int
		var p95, avg float64
		var total int64
		var suggested *int

		if err := rows.Scan(&weekStart, &credID, &rawModel, &peak, &p95, &avg,
			&total, &sampleDays, &currentLimit, &suggested, &reason, &updatedAt); err != nil {
			continue
		}
		entry := map[string]interface{}{
			"week_start":      weekStart.Format(time.RFC3339),
			"credential_id":   credID,
			"raw_model":       rawModel,
			"peak_concurrent": peak,
			"p95_concurrent":  p95,
			"avg_concurrent":  avg,
			"total_requests":  total,
			"sample_days":     sampleDays,
			"current_limit":   currentLimit,
			"updated_at":      updatedAt.Format(time.RFC3339),
		}
		if suggested != nil {
			entry["suggested_limit"] = *suggested
			entry["suggestion_reason"] = reason
		}
		results = append(results, entry)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"peaks": results,
		"count": len(results),
	})
}

func (h *PeakHandlers) handlePreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx := r.Context()
	rows, err := h.db.Query(ctx, `
		SELECT
			wp.credential_id, wp.raw_model,
			wp.current_limit, wp.suggested_limit,
			wp.suggestion_reason, wp.peak_concurrent, wp.p95_concurrent,
			wp.week_start, wp.updated_at
		FROM credential_model_weekly_peak wp
		WHERE wp.suggested_limit IS NOT NULL
		  AND wp.suggested_limit > wp.current_limit
		  AND wp.updated_at > NOW() - INTERVAL '7 days'
		ORDER BY wp.suggested_limit - wp.current_limit DESC
		LIMIT 50
	`)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	out := make([]map[string]interface{}, 0)
	for rows.Next() {
		var credID int64
		var rawModel, reason string
		var current, suggested, peak int
		var p95 float64
		var weekStart, updatedAt time.Time
		if err := rows.Scan(&credID, &rawModel, &current, &suggested, &reason,
			&peak, &p95, &weekStart, &updatedAt); err != nil {
			continue
		}
		previewEnd := updatedAt.Add(24 * time.Hour)
		remaining := time.Until(previewEnd)
		out = append(out, map[string]interface{}{
			"credential_id":   credID,
			"raw_model":       rawModel,
			"current_limit":   current,
			"suggested_limit": suggested,
			"increase":        suggested - current,
			"reason":          reason,
			"peak_concurrent": peak,
			"p95_concurrent":  p95,
			"week_start":      weekStart.Format(time.RFC3339),
			"preview_ends_at": previewEnd.Format(time.RFC3339),
			"ready_to_apply":  remaining <= 0,
			"remaining_hours": int(remaining.Hours()),
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"suggestions": out,
		"count":       len(out),
	})
}

func (h *PeakHandlers) handleApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		CredentialID int64  `json:"credential_id"`
		Model        string `json:"model"`
		NewLimit     int    `json:"new_limit"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.CredentialID == 0 || req.NewLimit < 1 {
		http.Error(w, "credential_id and new_limit required", http.StatusBadRequest)
		return
	}
	if req.NewLimit > 500 {
		http.Error(w, "new_limit cannot exceed 500", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	var current int
	// The per-credential concurrency cap lives in credentials.concurrency_limit.
	// routing_policy is a global singleton row (no credential_id column),
	// so the previous JOIN against it was returning 0/NotFound and
	// blocking the manual apply path.
	err := h.db.QueryRow(ctx, `
		SELECT COALESCE(concurrency_limit, 0)
		FROM credentials WHERE id = $1
	`, req.CredentialID).Scan(&current)
	if err != nil {
		http.Error(w, "credential not found", http.StatusNotFound)
		return
	}
	if req.NewLimit <= current {
		http.Error(w, "new_limit must be greater than current limit", http.StatusBadRequest)
		return
	}
	if _, err := h.db.Exec(ctx, `
		UPDATE credentials SET concurrency_limit = $1, updated_at = NOW()
		WHERE id = $2
	`, req.NewLimit, req.CredentialID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_, _ = h.db.Exec(ctx, `
		INSERT INTO auto_tune_audit (credential_id, raw_model, action, old_limit, new_limit, reason, applied_by)
		VALUES ($1, $2, 'apply', $3, $4, 'manual apply via admin API', 'admin')
	`, req.CredentialID, req.Model, current, req.NewLimit)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"success":       true,
		"credential_id": req.CredentialID,
		"old_limit":     current,
		"new_limit":     req.NewLimit,
	})
}

func (h *PeakHandlers) handleAuditLog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx := r.Context()
	rows, err := h.db.Query(ctx, `
		SELECT id, credential_id, raw_model, action, old_limit, new_limit,
		       reason, peak_concurrent, p95_concurrent, week_start,
		       created_at, applied_by
		FROM auto_tune_audit
		ORDER BY created_at DESC
		LIMIT 100
	`)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	entries := make([]map[string]interface{}, 0)
	for rows.Next() {
		var id, credID int64
		var rawModel, action string
		var oldLimit, newLimit, peak *int
		var p95 *float64
		var reason *string
		var weekStart *time.Time
		var createdAt time.Time
		var appliedBy *string
		if err := rows.Scan(&id, &credID, &rawModel, &action, &oldLimit, &newLimit,
			&reason, &peak, &p95, &weekStart, &createdAt, &appliedBy); err != nil {
			continue
		}
		entry := map[string]interface{}{
			"id":            id,
			"credential_id": credID,
			"raw_model":     rawModel,
			"action":        action,
			"created_at":    createdAt.Format(time.RFC3339),
		}
		if oldLimit != nil {
			entry["old_limit"] = *oldLimit
		}
		if newLimit != nil {
			entry["new_limit"] = *newLimit
		}
		if reason != nil {
			entry["reason"] = *reason
		}
		if peak != nil {
			entry["peak_concurrent"] = *peak
		}
		if p95 != nil {
			entry["p95_concurrent"] = *p95
		}
		if weekStart != nil {
			entry["week_start"] = weekStart.Format(time.RFC3339)
		}
		if appliedBy != nil {
			entry["applied_by"] = *appliedBy
		}
		entries = append(entries, entry)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"entries": entries,
		"count":   len(entries),
	})
}

func (h *PeakHandlers) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx := r.Context()
	var weeklyCount, pendingCount, appliedCount int
	_ = h.db.QueryRow(ctx, `SELECT COUNT(*) FROM credential_model_weekly_peak`).Scan(&weeklyCount)
	_ = h.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM credential_model_weekly_peak
		WHERE suggested_limit IS NOT NULL AND suggested_limit > current_limit
	`).Scan(&pendingCount)
	_ = h.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM auto_tune_audit WHERE action = 'apply'
	`).Scan(&appliedCount)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"weekly_peak_count":   weeklyCount,
		"pending_suggestions": pendingCount,
		"applied_count":       appliedCount,
	})
}
