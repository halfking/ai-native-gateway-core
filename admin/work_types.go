// Package admin — work type config handlers (Phase 1).
//
// Endpoints:
//
//	GET    /api/admin/work-types              — list
//	POST   /api/admin/work-types              — create
//	GET    /api/admin/work-types/stats        — 24h stats
//	POST   /api/admin/work-types/sync-from-acc — ACC sync placeholder
//	GET    /api/admin/work-types/:key         — detail + model routes
//	PATCH  /api/admin/work-types/:key         — update
//	DELETE /api/admin/work-types/:key         — soft delete (enabled=false)
//	PUT    /api/admin/work-types/:key/routes  — replace model routes
package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// WorkTypeHandlers serves admin CRUD for work_type_config.
type WorkTypeHandlers struct {
	db *pgxpool.Pool
}

// NewWorkTypeHandlers constructs the handler set.
func NewWorkTypeHandlers(db *pgxpool.Pool) *WorkTypeHandlers {
	return &WorkTypeHandlers{db: db}
}

// RegisterWorkTypeRoutes mounts work-type admin endpoints.
func (h *WorkTypeHandlers) RegisterWorkTypeRoutes(mux *http.ServeMux, adminWrap func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("/api/admin/work-types", adminWrap(h.handleRoot))
	mux.HandleFunc("/api/admin/work-types/stats", adminWrap(h.handleStats))
	mux.HandleFunc("/api/admin/work-types/sync-from-acc", adminWrap(h.handleSyncFromACC))
	mux.HandleFunc("/api/admin/work-types/", adminWrap(h.handleSub))
}

func (h *WorkTypeHandlers) handleRoot(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.listWorkTypes(w, r)
	case http.MethodPost:
		h.createWorkType(w, r)
	default:
		writeJSONErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *WorkTypeHandlers) handleSub(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/admin/work-types/")
	rest = strings.Trim(rest, "/")
	if rest == "" {
		writeJSONErr(w, http.StatusNotFound, "not found")
		return
	}

	parts := strings.SplitN(rest, "/", 2)
	key := parts[0]
	suffix := ""
	if len(parts) > 1 {
		suffix = parts[1]
	}

	if suffix == "routes" {
		if r.Method == http.MethodPut {
			h.putRoutes(w, r, key)
			return
		}
		writeJSONErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if suffix != "" {
		writeJSONErr(w, http.StatusNotFound, "not found")
		return
	}

	switch r.Method {
	case http.MethodGet:
		h.getWorkType(w, r, key)
	case http.MethodPatch:
		h.updateWorkType(w, r, key)
	case http.MethodDelete:
		h.deleteWorkType(w, r, key)
	default:
		writeJSONErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *WorkTypeHandlers) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	out := map[string]interface{}{
		"window_hours": 24,
		"by_work_type": map[string]interface{}{},
		"by_l1_task":   map[string]int{},
		"total_auto":   0,
	}

	// Direct work_type column (future)
	wtDirect := map[string]int{}
	rows, err := h.db.Query(ctx, `
		SELECT COALESCE(work_type, 'unknown'), COUNT(*)
		FROM request_logs
		WHERE is_auto_request = TRUE
		  AND ts >= NOW() - INTERVAL '24 hours'
		  AND work_type IS NOT NULL AND work_type <> ''
		GROUP BY work_type
	`)
	if err == nil {
		for rows.Next() {
			var k string
			var c int
			if err := rows.Scan(&k, &c); err == nil {
				wtDirect[k] = c
			}
		}
		rows.Close()
	}

	// L1 task_type distribution (mapped to work types)
	l1Dist := map[string]int{}
	rows, err = h.db.Query(ctx, `
		SELECT COALESCE(task_type, 'unknown'), COUNT(*)
		FROM request_logs
		WHERE is_auto_request = TRUE
		  AND ts >= NOW() - INTERVAL '24 hours'
		GROUP BY task_type
	`)
	if err == nil {
		for rows.Next() {
			var k string
			var c int
			if err := rows.Scan(&k, &c); err == nil {
				l1Dist[k] = c
			}
		}
		rows.Close()
	}
	out["by_l1_task"] = l1Dist

	var totalAuto int
	_ = h.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM request_logs
		WHERE is_auto_request = TRUE AND ts >= NOW() - INTERVAL '24 hours'
	`).Scan(&totalAuto)
	out["total_auto"] = totalAuto

	// Build per-work-type stats from config + L1 mapping
	type wtRow struct {
		Key         string
		Label       string
		Category    string
		L1TaskType  string
		CountDirect int
		CountL1     int
	}
	configRows, err := h.db.Query(ctx, `
		SELECT key, label, category, l1_task_type
		FROM work_type_config
		WHERE enabled = TRUE
		ORDER BY sort_order, key
	`)
	if err != nil {
		writeInternalErr(w, err)
		return
	}
	defer configRows.Close()

	byWT := map[string]interface{}{}
	for configRows.Next() {
		var row wtRow
		if err := configRows.Scan(&row.Key, &row.Label, &row.Category, &row.L1TaskType); err != nil {
			continue
		}
		row.CountDirect = wtDirect[row.Key]
		row.CountL1 = l1Dist[row.L1TaskType]
		// Prefer direct work_type count; fall back to L1 share (split evenly among same L1 keys is too noisy — use L1 total as proxy)
		count := row.CountDirect
		if count == 0 {
			count = row.CountL1
		}
		byWT[row.Key] = map[string]interface{}{
			"key":           row.Key,
			"label":         row.Label,
			"category":      row.Category,
			"l1_task_type":  row.L1TaskType,
			"count_24h":     count,
			"count_direct":  row.CountDirect,
			"count_l1_proxy": row.CountL1,
		}
	}
	out["by_work_type"] = byWT

	// Top models 24h
	topModels := make([]map[string]interface{}, 0)
	rows, err = h.db.Query(ctx, `
		SELECT outbound_model, COUNT(*) AS c
		FROM request_logs
		WHERE is_auto_request = TRUE
		  AND ts >= NOW() - INTERVAL '24 hours'
		  AND outbound_model IS NOT NULL
		GROUP BY outbound_model
		ORDER BY c DESC
		LIMIT 10
	`)
	if err == nil {
		for rows.Next() {
			var m string
			var c int
			if err := rows.Scan(&m, &c); err == nil {
				topModels = append(topModels, map[string]interface{}{"model": m, "count": c})
			}
		}
		rows.Close()
	}
	out["top_models"] = topModels

	writeJSONOk(w, out)
}

func (h *WorkTypeHandlers) handleSyncFromACC(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSONOk(w, map[string]interface{}{
		"synced":  false,
		"message": "Phase 3",
	})
}

type workTypeConfig struct {
	Key              string     `json:"key"`
	Label            string     `json:"label"`
	Category         string     `json:"category"`
	L1TaskType       string     `json:"l1_task_type"`
	DefaultProfile   string     `json:"default_profile"`
	Tags             []string   `json:"tags"`
	PromptKeywords   []string   `json:"prompt_keywords"`
	ACCTaskType      *string    `json:"acc_task_type,omitempty"`
	Enabled          bool       `json:"enabled"`
	SortOrder        int        `json:"sort_order"`
	SyncedFromACCAt  *time.Time `json:"synced_from_acc_at,omitempty"`
	UpdatedAt        time.Time  `json:"updated_at"`
	ModelRoutes      []modelRoute `json:"model_routes,omitempty"`
}

type modelRoute struct {
	ID             int     `json:"id,omitempty"`
	CanonicalName  string  `json:"canonical_name"`
	Weight         float64 `json:"weight"`
	MinScore       float64 `json:"min_score"`
	Enabled        bool    `json:"enabled"`
}

func (h *WorkTypeHandlers) listWorkTypes(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	includeDisabled := r.URL.Query().Get("include_disabled") == "true"
	query := `
		SELECT key, label, category, l1_task_type, default_profile,
		       tags, prompt_keywords, acc_task_type, enabled, sort_order,
		       synced_from_acc_at, updated_at
		FROM work_type_config
	`
	if !includeDisabled {
		query += ` WHERE enabled = TRUE`
	}
	query += ` ORDER BY sort_order, key`

	rows, err := h.db.Query(ctx, query)
	if err != nil {
		writeInternalErr(w, err)
		return
	}
	defer rows.Close()

	out := make([]workTypeConfig, 0)
	for rows.Next() {
		wt, err := scanWorkType(rows)
		if err != nil {
			continue
		}
		out = append(out, wt)
	}
	writeJSONOk(w, out)
}

func (h *WorkTypeHandlers) getWorkType(w http.ResponseWriter, r *http.Request, key string) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	wt, err := h.fetchWorkType(ctx, key)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONErr(w, http.StatusNotFound, "work type not found")
			return
		}
		writeInternalErr(w, err)
		return
	}

	routes, err := h.fetchRoutes(ctx, key)
	if err != nil {
		writeInternalErr(w, err)
		return
	}
	wt.ModelRoutes = routes
	writeJSONOk(w, wt)
}

func (h *WorkTypeHandlers) createWorkType(w http.ResponseWriter, r *http.Request) {
	var req workTypeConfig
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	req.Key = strings.TrimSpace(req.Key)
	if req.Key == "" || req.Label == "" || req.Category == "" || req.L1TaskType == "" {
		writeJSONErr(w, http.StatusBadRequest, "key, label, category, l1_task_type are required")
		return
	}
	if req.DefaultProfile == "" {
		req.DefaultProfile = "smart"
	}
	if req.DefaultProfile != "smart" && req.DefaultProfile != "speed_first" && req.DefaultProfile != "cost_first" {
		writeJSONErr(w, http.StatusBadRequest, "default_profile must be smart, speed_first, or cost_first")
		return
	}
	if req.Tags == nil {
		req.Tags = []string{}
	}
	if req.PromptKeywords == nil {
		req.PromptKeywords = []string{}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	_, err := h.db.Exec(ctx, `
		INSERT INTO work_type_config
		    (key, label, category, l1_task_type, default_profile, tags, prompt_keywords,
		     acc_task_type, enabled, sort_order, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,NOW())
	`, req.Key, req.Label, req.Category, req.L1TaskType, req.DefaultProfile,
		req.Tags, req.PromptKeywords, req.ACCTaskType, req.Enabled, req.SortOrder)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate") || strings.Contains(err.Error(), "unique") {
			writeJSONErr(w, http.StatusConflict, "work type key already exists")
			return
		}
		writeInternalErr(w, err)
		return
	}

	wt, err := h.fetchWorkType(ctx, req.Key)
	if err != nil {
		writeInternalErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, wt)
}

func (h *WorkTypeHandlers) updateWorkType(w http.ResponseWriter, r *http.Request, key string) {
	var req map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	setClauses := []string{"updated_at = NOW()"}
	args := []interface{}{}
	argN := 1

	stringFields := map[string]string{
		"label":           "label",
		"category":        "category",
		"l1_task_type":    "l1_task_type",
		"default_profile": "default_profile",
		"acc_task_type":   "acc_task_type",
	}
	for jsonKey, col := range stringFields {
		if v, ok := req[jsonKey]; ok {
			s, ok := v.(string)
			if !ok {
				writeJSONErr(w, http.StatusBadRequest, fmt.Sprintf("%s must be a string", jsonKey))
				return
			}
			if jsonKey == "default_profile" && s != "smart" && s != "speed_first" && s != "cost_first" {
				writeJSONErr(w, http.StatusBadRequest, "default_profile must be smart, speed_first, or cost_first")
				return
			}
			setClauses = append(setClauses, fmt.Sprintf("%s = $%d", col, argN))
			args = append(args, s)
			argN++
		}
	}
	if v, ok := req["enabled"]; ok {
		b, ok := v.(bool)
		if !ok {
			writeJSONErr(w, http.StatusBadRequest, "enabled must be a boolean")
			return
		}
		setClauses = append(setClauses, fmt.Sprintf("enabled = $%d", argN))
		args = append(args, b)
		argN++
	}
	if v, ok := req["sort_order"]; ok {
		switch n := v.(type) {
		case float64:
			setClauses = append(setClauses, fmt.Sprintf("sort_order = $%d", argN))
			args = append(args, int(n))
			argN++
		default:
			writeJSONErr(w, http.StatusBadRequest, "sort_order must be a number")
			return
		}
	}
	if v, ok := req["tags"]; ok {
		tags, err := parseStringSlice(v)
		if err != nil {
			writeJSONErr(w, http.StatusBadRequest, "tags must be a string array")
			return
		}
		setClauses = append(setClauses, fmt.Sprintf("tags = $%d", argN))
		args = append(args, tags)
		argN++
	}
	if v, ok := req["prompt_keywords"]; ok {
		kw, err := parseStringSlice(v)
		if err != nil {
			writeJSONErr(w, http.StatusBadRequest, "prompt_keywords must be a string array")
			return
		}
		setClauses = append(setClauses, fmt.Sprintf("prompt_keywords = $%d", argN))
		args = append(args, kw)
		argN++
	}

	if len(setClauses) == 1 {
		writeJSONErr(w, http.StatusBadRequest, "no fields to update")
		return
	}

	args = append(args, key)
	query := fmt.Sprintf(`UPDATE work_type_config SET %s WHERE key = $%d`, strings.Join(setClauses, ", "), argN)
	tag, err := h.db.Exec(ctx, query, args...)
	if err != nil {
		writeInternalErr(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeJSONErr(w, http.StatusNotFound, "work type not found")
		return
	}

	wt, err := h.fetchWorkType(ctx, key)
	if err != nil {
		writeInternalErr(w, err)
		return
	}
	writeJSONOk(w, wt)
}

func (h *WorkTypeHandlers) deleteWorkType(w http.ResponseWriter, r *http.Request, key string) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	tag, err := h.db.Exec(ctx, `
		UPDATE work_type_config SET enabled = FALSE, updated_at = NOW() WHERE key = $1
	`, key)
	if err != nil {
		writeInternalErr(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeJSONErr(w, http.StatusNotFound, "work type not found")
		return
	}
	writeJSONOk(w, map[string]interface{}{"key": key, "enabled": false, "deleted": true})
}

func (h *WorkTypeHandlers) putRoutes(w http.ResponseWriter, r *http.Request, key string) {
	var routes []modelRoute
	if err := json.NewDecoder(r.Body).Decode(&routes); err != nil {
		writeJSONErr(w, http.StatusBadRequest, "invalid JSON body; expected array of routes")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	var exists bool
	if err := h.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM work_type_config WHERE key = $1)`, key).Scan(&exists); err != nil {
		writeInternalErr(w, err)
		return
	}
	if !exists {
		writeJSONErr(w, http.StatusNotFound, "work type not found")
		return
	}

	tx, err := h.db.Begin(ctx)
	if err != nil {
		writeInternalErr(w, err)
		return
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `DELETE FROM work_type_model_route WHERE work_type_key = $1`, key); err != nil {
		writeInternalErr(w, err)
		return
	}

	for _, rt := range routes {
		if strings.TrimSpace(rt.CanonicalName) == "" {
			continue
		}
		wt := rt.Weight
		if wt <= 0 {
			wt = 1
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO work_type_model_route (work_type_key, canonical_name, weight, min_score, enabled)
			VALUES ($1, $2, $3, $4, $5)
		`, key, rt.CanonicalName, wt, rt.MinScore, rt.Enabled)
		if err != nil {
			writeInternalErr(w, err)
			return
		}
	}

	if err := tx.Commit(ctx); err != nil {
		writeInternalErr(w, err)
		return
	}

	out, err := h.fetchRoutes(ctx, key)
	if err != nil {
		writeInternalErr(w, err)
		return
	}
	writeJSONOk(w, map[string]interface{}{"key": key, "model_routes": out})
}

func (h *WorkTypeHandlers) fetchWorkType(ctx context.Context, key string) (workTypeConfig, error) {
	row := h.db.QueryRow(ctx, `
		SELECT key, label, category, l1_task_type, default_profile,
		       tags, prompt_keywords, acc_task_type, enabled, sort_order,
		       synced_from_acc_at, updated_at
		FROM work_type_config WHERE key = $1
	`, key)
	return scanWorkTypeRow(row)
}

func (h *WorkTypeHandlers) fetchRoutes(ctx context.Context, key string) ([]modelRoute, error) {
	rows, err := h.db.Query(ctx, `
		SELECT id, canonical_name, weight, min_score, enabled
		FROM work_type_model_route
		WHERE work_type_key = $1
		ORDER BY weight DESC, canonical_name
	`, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]modelRoute, 0)
	for rows.Next() {
		var rt modelRoute
		var weight, minScore float64
		if err := rows.Scan(&rt.ID, &rt.CanonicalName, &weight, &minScore, &rt.Enabled); err != nil {
			continue
		}
		rt.Weight = weight
		rt.MinScore = minScore
		out = append(out, rt)
	}
	return out, nil
}

type scannable interface {
	Scan(dest ...any) error
}

func scanWorkType(rows scannable) (workTypeConfig, error) {
	return scanWorkTypeRow(rows)
}

func scanWorkTypeRow(row scannable) (workTypeConfig, error) {
	var wt workTypeConfig
	var accTask *string
	var syncedAt *time.Time
	err := row.Scan(
		&wt.Key, &wt.Label, &wt.Category, &wt.L1TaskType, &wt.DefaultProfile,
		&wt.Tags, &wt.PromptKeywords, &accTask, &wt.Enabled, &wt.SortOrder,
		&syncedAt, &wt.UpdatedAt,
	)
	if err != nil {
		return wt, err
	}
	wt.ACCTaskType = accTask
	wt.SyncedFromACCAt = syncedAt
	if wt.Tags == nil {
		wt.Tags = []string{}
	}
	if wt.PromptKeywords == nil {
		wt.PromptKeywords = []string{}
	}
	return wt, nil
}

func parseStringSlice(v interface{}) ([]string, error) {
	arr, ok := v.([]interface{})
	if !ok {
		return nil, errors.New("not an array")
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		s, ok := item.(string)
		if !ok {
			return nil, errors.New("not all strings")
		}
		out = append(out, s)
	}
	return out, nil
}
