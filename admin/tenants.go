package admin

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type tenantInfo struct {
	Code         string    `json:"code"`
	Name         string    `json:"name"`
	Status       string    `json:"status"`
	Description  string    `json:"description"`
	ContactEmail string    `json:"contact_email"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`

	// Aggregate stats (populated for list/detail)
	UserCount       int   `json:"user_count,omitempty"`
	APIKeyCount     int   `json:"api_key_count,omitempty"`
	Requests7d      int64 `json:"requests_7d,omitempty"`
	Tokens7d        int64 `json:"tokens_7d,omitempty"`
	Cost7d          float64 `json:"cost_7d_usd,omitempty"`
	TotalRequests   int64 `json:"total_requests,omitempty"`
}

type createTenantRequest struct {
	Code         string `json:"code"`
	Name         string `json:"name"`
	Status       string `json:"status"`
	Description  string `json:"description"`
	ContactEmail string `json:"contact_email"`
}

type updateTenantRequest struct {
	Name         *string `json:"name"`
	Status       *string `json:"status"`
	Description  *string `json:"description"`
	ContactEmail *string `json:"contact_email"`
}

const tenantValidStatuses = "active|trial|suspended|expired|disabled"

// isValidTenantStatus checks if status is one of 5 allowed values.
func isValidTenantStatus(s string) bool {
	for _, v := range strings.Split(tenantValidStatuses, "|") {
		if v == s {
			return true
		}
	}
	return false
}

// handleTenants dispatches /api/admin/tenants and /api/admin/tenants/{code}/*
// All routes are super_admin only (enforced via h.superAdmin() wrapper).
func (h *Handler) handleTenants(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}

	// Strip /api/admin/tenants prefix
	path := strings.TrimPrefix(r.URL.Path, "/api/admin/tenants")
	path = strings.Trim(path, "/")

	if path == "" {
		// /api/admin/tenants
		switch r.Method {
		case http.MethodGet:
			h.listTenants(w, r)
		case http.MethodPost:
			h.createTenant(w, r)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}

	// /api/admin/tenants/{code}[/{sub}]
	parts := strings.SplitN(path, "/", 2)
	code := parts[0]
	sub := ""
	if len(parts) > 1 {
		sub = parts[1]
	}

	if sub == "" {
		// /api/admin/tenants/{code}
		switch r.Method {
		case http.MethodGet:
			h.getTenant(w, r, code)
		case http.MethodPatch:
			h.updateTenant(w, r, code)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}

	// /api/admin/tenants/{code}/users|keys|stats
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	switch sub {
	case "users":
		h.listTenantUsers(w, r, code)
	case "keys":
		h.listTenantKeys(w, r, code)
	case "stats":
		h.getTenantStats(w, r, code)
	default:
		writeError(w, http.StatusNotFound, "unknown sub-resource: "+sub)
	}
}

// ── listTenants: GET /api/admin/tenants ──────────────────────────

func (h *Handler) listTenants(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	statusFilter := queryString(r, "status")
	where := "1=1"
	args := []any{}
	if statusFilter != "" {
		where = "status = $1"
		args = append(args, statusFilter)
	}

	rows, err := h.db.Query(ctx, `
		SELECT t.code, t.name, t.status, t.description, t.contact_email,
		       t.created_at, t.updated_at,
		       COALESCE(u.user_count, 0) AS user_count,
		       COALESCE(k.key_count, 0) AS key_count,
		       COALESCE(k.total_requests, 0) AS total_requests
		FROM tenants t
		LEFT JOIN (
			SELECT tenant_id, COUNT(*) AS user_count
			FROM users GROUP BY tenant_id
		) u ON u.tenant_id = t.code
		LEFT JOIN (
			SELECT tenant_id,
			       COUNT(*) AS key_count,
			       SUM(COALESCE(total_requests, 0)) AS total_requests
			FROM api_keys GROUP BY tenant_id
		) k ON k.tenant_id = t.code
		WHERE `+where+`
		ORDER BY t.code
	`, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}
	defer rows.Close()

	tenants := make([]tenantInfo, 0)
	for rows.Next() {
		var t tenantInfo
		if err := rows.Scan(&t.Code, &t.Name, &t.Status, &t.Description, &t.ContactEmail,
			&t.CreatedAt, &t.UpdatedAt, &t.UserCount, &t.APIKeyCount, &t.TotalRequests); err != nil {
			continue
		}
		tenants = append(tenants, t)
	}
	if tenants == nil {
		tenants = []tenantInfo{}
	}
	writeJSON(w, http.StatusOK, tenants)
}

// ── createTenant: POST /api/admin/tenants ─────────────────────────

func (h *Handler) createTenant(w http.ResponseWriter, r *http.Request) {
	var req createTenantRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Code == "" || req.Name == "" {
		writeError(w, http.StatusBadRequest, "code and name are required")
		return
	}
	if !isValidTenantStatus(req.Status) {
		writeError(w, http.StatusBadRequest, "status must be one of: "+tenantValidStatuses)
		return
	}
	if req.Status == "" {
		req.Status = "active"
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var t tenantInfo
	err := h.db.QueryRow(ctx, `
		INSERT INTO tenants (code, name, status, description, contact_email)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING code, name, status, description, contact_email, created_at, updated_at
	`, req.Code, req.Name, req.Status, req.Description, req.ContactEmail).Scan(
		&t.Code, &t.Name, &t.Status, &t.Description, &t.ContactEmail, &t.CreatedAt, &t.UpdatedAt,
	)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key") {
			writeError(w, http.StatusConflict, "tenant code already exists")
			return
		}
		if strings.Contains(err.Error(), "check constraint") {
			writeError(w, http.StatusBadRequest, "invalid status value")
			return
		}
		writeError(w, http.StatusInternalServerError, "create tenant failed: "+err.Error())
		return
	}

	h.auditLog(getActorFromRequest(r), "tenant.create", "tenant", 0, map[string]any{
		"code":   t.Code,
		"name":   t.Name,
		"status": t.Status,
	})
	writeJSON(w, http.StatusCreated, t)
}

// ── getTenant: GET /api/admin/tenants/{code} ──────────────────────

func (h *Handler) getTenant(w http.ResponseWriter, r *http.Request, code string) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var t tenantInfo
	err := h.db.QueryRow(ctx, `
		SELECT code, name, status, description, contact_email, created_at, updated_at
		FROM tenants WHERE code = $1
	`, code).Scan(&t.Code, &t.Name, &t.Status, &t.Description, &t.ContactEmail, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		writeError(w, http.StatusNotFound, "tenant not found")
		return
	}

	// Populate aggregate stats
	_ = h.db.QueryRow(ctx, `SELECT COUNT(*) FROM users WHERE tenant_id = $1`, code).Scan(&t.UserCount)
	_ = h.db.QueryRow(ctx, `SELECT COUNT(*) FROM api_keys WHERE tenant_id = $1`, code).Scan(&t.APIKeyCount)

	// 7-day usage
	row := h.db.QueryRow(ctx, `
		SELECT COUNT(*), COALESCE(SUM(total_tokens), 0), COALESCE(SUM(cost_usd), 0.0)
		FROM usage_ledger
		WHERE tenant_id = $1 AND ts >= now() - INTERVAL '7 days'
	`, code)
	_ = row.Scan(&t.Requests7d, &t.Tokens7d, &t.Cost7d)
	_ = h.db.QueryRow(ctx, `
		SELECT COALESCE(SUM(COALESCE(total_requests, 0)), 0) FROM api_keys WHERE tenant_id = $1
	`, code).Scan(&t.TotalRequests)

	writeJSON(w, http.StatusOK, t)
}

// ── updateTenant: PATCH /api/admin/tenants/{code} ──────────────────

func (h *Handler) updateTenant(w http.ResponseWriter, r *http.Request, code string) {
	var req updateTenantRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Status != nil && !isValidTenantStatus(*req.Status) {
		writeError(w, http.StatusBadRequest, "status must be one of: "+tenantValidStatuses)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// Build dynamic SET
	var sets []string
	var args []any
	idx := 1
	if req.Name != nil {
		sets = append(sets, "name = $"+strconv.Itoa(idx))
		args = append(args, *req.Name)
		idx++
	}
	if req.Status != nil {
		sets = append(sets, "status = $"+strconv.Itoa(idx))
		args = append(args, *req.Status)
		idx++
	}
	if req.Description != nil {
		sets = append(sets, "description = $"+strconv.Itoa(idx))
		args = append(args, *req.Description)
		idx++
	}
	if req.ContactEmail != nil {
		sets = append(sets, "contact_email = $"+strconv.Itoa(idx))
		args = append(args, *req.ContactEmail)
		idx++
	}
	if len(sets) == 0 {
		writeError(w, http.StatusBadRequest, "no fields to update")
		return
	}
	sets = append(sets, "updated_at = now()")
	args = append(args, code)

	query := "UPDATE tenants SET " + strings.Join(sets, ", ") + " WHERE code = $" + strconv.Itoa(idx) +
		" RETURNING code, name, status, description, contact_email, created_at, updated_at"

	var t tenantInfo
	err := h.db.QueryRow(ctx, query, args...).Scan(
		&t.Code, &t.Name, &t.Status, &t.Description, &t.ContactEmail, &t.CreatedAt, &t.UpdatedAt,
	)
	if err != nil {
		if strings.Contains(err.Error(), "no rows") {
			writeError(w, http.StatusNotFound, "tenant not found")
			return
		}
		if strings.Contains(err.Error(), "check constraint") {
			writeError(w, http.StatusBadRequest, "invalid status value")
			return
		}
		writeError(w, http.StatusInternalServerError, "update failed: "+err.Error())
		return
	}

	// Audit: differentiate status changes vs other updates
	action := "tenant.update"
	if req.Status != nil {
		if *req.Status == "disabled" {
			action = "tenant.disable"
		} else if t.Status == "active" || t.Status == "trial" {
			action = "tenant.enable"
		}
	}
	h.auditLog(getActorFromRequest(r), action, "tenant", 0, map[string]any{
		"code":         t.Code,
		"new_status":   t.Status,
		"update_keys":  sets,
	})
	writeJSON(w, http.StatusOK, t)
}

// ── listTenantUsers: GET /api/admin/tenants/{code}/users ───────────

func (h *Handler) listTenantUsers(w http.ResponseWriter, r *http.Request, code string) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// Verify tenant exists
	var exists bool
	_ = h.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tenants WHERE code = $1)`, code).Scan(&exists)
	if !exists {
		writeError(w, http.StatusNotFound, "tenant not found")
		return
	}

	rows, err := h.db.Query(ctx, `
		SELECT id, tenant_id, username, display_name, email, role, enabled, last_login_at, created_at
		FROM users WHERE tenant_id = $1 ORDER BY id
	`, code)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}
	defer rows.Close()

	users := make([]userInfo, 0)
	for rows.Next() {
		var u userInfo
		if err := rows.Scan(&u.ID, &u.TenantID, &u.Username, &u.DisplayName, &u.Email,
			&u.Role, &u.Enabled, &u.LastLoginAt, &u.CreatedAt); err != nil {
			continue
		}
		users = append(users, u)
	}
	if users == nil {
		users = []userInfo{}
	}
	writeJSON(w, http.StatusOK, users)
}

// ── listTenantKeys: GET /api/admin/tenants/{code}/keys ─────────────

func (h *Handler) listTenantKeys(w http.ResponseWriter, r *http.Request, code string) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// Verify tenant exists
	var exists bool
	_ = h.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tenants WHERE code = $1)`, code).Scan(&exists)
	if !exists {
		writeError(w, http.StatusNotFound, "tenant not found")
		return
	}

	rows, err := h.db.Query(ctx, `
		SELECT ak.id, ak.tenant_id, ak.key_prefix, ak.key_alias, ak.owner_user,
		       ak.enabled, ak.status, ak.application_id, app.code AS app_code,
		       ak.total_requests, ak.total_prompt_tokens, ak.total_completion_tokens,
		       ak.total_cost_usd, ak.expires_at, ak.created_at
		FROM api_keys ak
		LEFT JOIN applications app ON app.id = ak.application_id
		WHERE ak.tenant_id = $1
		ORDER BY ak.id DESC
	`, code)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}
	defer rows.Close()

	type tenantKeyInfo struct {
		ID         int     `json:"id"`
		TenantID   string  `json:"tenant_id"`
		KeyPrefix  string  `json:"key_prefix"`
		KeyAlias   string  `json:"key_alias"`
		OwnerUser  string  `json:"owner_user"`
		Enabled    bool    `json:"enabled"`
		Status     string  `json:"status"`
		AppID      int     `json:"application_id"`
		AppCode    string  `json:"application_code"`
		TotalReqs  int64   `json:"total_requests"`
		TotalCost  float64 `json:"total_cost_usd"`
		ExpiresAt  *time.Time `json:"expires_at,omitempty"`
		CreatedAt  time.Time `json:"created_at"`
	}

	keys := make([]tenantKeyInfo, 0)
	for rows.Next() {
		var k tenantKeyInfo
		var expiresAt *time.Time
		if err := rows.Scan(&k.ID, &k.TenantID, &k.KeyPrefix, &k.KeyAlias, &k.OwnerUser,
			&k.Enabled, &k.Status, &k.AppID, &k.AppCode,
			&k.TotalReqs, &k.TotalCost, &expiresAt, &k.CreatedAt); err != nil {
			continue
		}
		k.ExpiresAt = expiresAt
		keys = append(keys, k)
	}
	if keys == nil {
		keys = []tenantKeyInfo{}
	}
	writeJSON(w, http.StatusOK, keys)
}

// ── getTenantStats: GET /api/admin/tenants/{code}/stats ───────────

func (h *Handler) getTenantStats(w http.ResponseWriter, r *http.Request, code string) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// Verify tenant exists
	var exists bool
	_ = h.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tenants WHERE code = $1)`, code).Scan(&exists)
	if !exists {
		writeError(w, http.StatusNotFound, "tenant not found")
		return
	}

	daysStr := queryString(r, "days")
	days, _ := strconv.Atoi(daysStr)
	if days < 1 {
		days = 7
	}
	if days > 365 {
		days = 365
	}

	type tenantStats struct {
		Days            int                `json:"days"`
		TotalRequests   int64              `json:"total_requests"`
		TotalTokens     int64              `json:"total_tokens"`
		TotalCost       float64            `json:"total_cost_usd"`
		UniqueKeys      int                `json:"unique_keys"`
		UniqueModels    int                `json:"unique_models"`
		UniqueApps      int                `json:"unique_apps"`
		ByModel         []modelBreakdown  `json:"by_model"`
		ByApplication   []appBreakdown    `json:"by_application"`
	}

	type modelBreakdown struct {
		Model     string  `json:"model"`
		Requests  int64   `json:"requests"`
		Tokens    int64   `json:"tokens"`
		Cost      float64 `json:"cost_usd"`
	}
	type appBreakdown struct {
		AppCode   string  `json:"application_code"`
		Requests  int64   `json:"requests"`
		Tokens    int64   `json:"tokens"`
		Cost      float64 `json:"cost_usd"`
	}

	var s tenantStats
	s.Days = days

	// Overall totals
	_ = h.db.QueryRow(ctx, `
		SELECT COUNT(*), COALESCE(SUM(total_tokens), 0), COALESCE(SUM(cost_usd), 0.0),
		       COUNT(DISTINCT api_key_id), COUNT(DISTINCT COALESCE(NULLIF(raw_model_name, ''), canonical_id::text)),
		       COUNT(DISTINCT application_id)
		FROM usage_ledger
		WHERE tenant_id = $1 AND ts >= now() - ($2 * INTERVAL '1 day')
	`, code, days).Scan(&s.TotalRequests, &s.TotalTokens, &s.TotalCost,
		&s.UniqueKeys, &s.UniqueModels, &s.UniqueApps)

	// By model
	modelRows, err := h.db.Query(ctx, `
		SELECT COALESCE(raw_model_name, '<unknown>') AS model,
		       COUNT(*), COALESCE(SUM(total_tokens), 0), COALESCE(SUM(cost_usd), 0.0)
		FROM usage_ledger
		WHERE tenant_id = $1 AND ts >= now() - ($2 * INTERVAL '1 day')
		GROUP BY raw_model_name
		ORDER BY SUM(cost_usd) DESC
		LIMIT 20
	`, code, days)
	if err == nil {
		defer modelRows.Close()
		for modelRows.Next() {
			var m modelBreakdown
			_ = modelRows.Scan(&m.Model, &m.Requests, &m.Tokens, &m.Cost)
			s.ByModel = append(s.ByModel, m)
		}
	}
	if s.ByModel == nil {
		s.ByModel = []modelBreakdown{}
	}

	// By application
	appRows, err := h.db.Query(ctx, `
		SELECT COALESCE(app.code, '<none>') AS app_code,
		       COUNT(*), COALESCE(SUM(ul.total_tokens), 0), COALESCE(SUM(ul.cost_usd), 0.0)
		FROM usage_ledger ul
		LEFT JOIN applications app ON app.id = ul.application_id
		WHERE ul.tenant_id = $1 AND ul.ts >= now() - ($2 * INTERVAL '1 day')
		GROUP BY app.code
		ORDER BY SUM(ul.cost_usd) DESC
		LIMIT 20
	`, code, days)
	if err == nil {
		defer appRows.Close()
		for appRows.Next() {
			var a appBreakdown
			_ = appRows.Scan(&a.AppCode, &a.Requests, &a.Tokens, &a.Cost)
			s.ByApplication = append(s.ByApplication, a)
		}
	}
	if s.ByApplication == nil {
		s.ByApplication = []appBreakdown{}
	}

	writeJSON(w, http.StatusOK, s)
}

// getActorFromRequest returns the username from AuthContext, or "unknown".
func getActorFromRequest(r *http.Request) string {
	auth := GetAuthContext(r)
	if auth != nil && auth.Username != "" {
		return auth.Username
	}
	return "unknown"
}
