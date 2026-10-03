package admin

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/kaixuan/llm-gateway-go/licensing"
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
	UserCount     int     `json:"user_count,omitempty"`
	APIKeyCount   int     `json:"api_key_count,omitempty"`
	Requests7d    int64   `json:"requests_7d,omitempty"`
	Tokens7d      int64   `json:"tokens_7d,omitempty"`
	Credits7d     int64   `json:"credits_7d,omitempty"`
	Cost7d        float64 `json:"cost_7d_usd,omitempty"`
	TotalRequests int64   `json:"total_requests,omitempty"`
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

type createTenantResponse struct {
	tenantInfo
	DefaultAdmin    *userInfo `json:"default_admin,omitempty"`
	InitialPassword string    `json:"initial_password,omitempty"`
}

type tenantModelBreakdown struct {
	Model    string  `json:"model"`
	Requests int64   `json:"requests"`
	Tokens   int64   `json:"tokens"`
	Credits  int64   `json:"credits"`
	Cost     float64 `json:"cost_usd"`
}

type tenantAppBreakdown struct {
	AppCode  string  `json:"application_code"`
	Requests int64   `json:"requests"`
	Tokens   int64   `json:"tokens"`
	Credits  int64   `json:"credits"`
	Cost     float64 `json:"cost_usd"`
}

// tenantDailyStat — 租户统计按天时序行（2026-09-30 统计 UI 优化轮）。
// 无流量日由 SQL generate_series 左连接补零，前端趋势图拿到连续序列。
type tenantDailyStat struct {
	Date     string  `json:"date"` // YYYY-MM-DD
	Requests int64   `json:"requests"`
	Success  int64   `json:"success"`
	Errors   int64   `json:"errors"`
	Tokens   int64   `json:"tokens"`
	Credits  int64   `json:"credits"`
	Cost     float64 `json:"cost_usd"`
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

// isValidTenantCode validates tenant code format: 2-64 chars, lowercase ASCII
// letters/digits, hyphens, underscores. Must start with a letter or digit.
// No spaces, no Unicode, no uppercase - prevents injection and path traversal.
var tenantCodeBuf [256]bool

func init() {
	for c := 'a'; c <= 'z'; c++ {
		tenantCodeBuf[c] = true
	}
	for c := '0'; c <= '9'; c++ {
		tenantCodeBuf[c] = true
	}
	tenantCodeBuf['-'] = true
	tenantCodeBuf['_'] = true
}

// isValidTenantCode returns true if code is a valid tenant identifier:
// 2-64 chars, starts with [a-z0-9], only [a-z0-9_-] after.
func isValidTenantCode(code string) bool {
	if len(code) < 2 || len(code) > 64 {
		return false
	}
	// First char must be alphanumeric (not - or _)
	first := code[0]
	if (first < 'a' || first > 'z') && (first < '0' || first > '9') {
		return false
	}
	for i := 0; i < len(code); i++ {
		if !tenantCodeBuf[code[i]] {
			return false
		}
	}
	return true
}

// handleTenants dispatches /api/admin/tenants and /api/admin/tenants/{code}/*
// All routes are super_admin only (enforced via h.superAdmin() wrapper).
func (h *Handler) handleTenants(w http.ResponseWriter, r *http.Request) {
	if auth := GetAuthContext(r); auth != nil && auth.Role != "super_admin" && auth.Role != "admin_key" {
		writeError(w, http.StatusForbidden, "super_admin role required for this endpoint")
		return
	}
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
			h.listTenantsAdmin(w, r)
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

	// /api/admin/tenants/{code}/users|keys|stats|model-policies[/{check|audit|{id}...}]
	// sub is the SplitN tail, so for "/tenants/{code}/model-policies/audit"
	// sub == "model-policies/audit". We must match the prefix, not the bare
	// string, otherwise /check and /audit fall through to the unknown-sub-resource
	// branch (incident 2026-06-23: "unknown sub-resource: model-policies/audit"
	// on page load + "method not allowed" on /check).
	if isModelPoliciesSubResource(sub) {
		h.handleTenantModelPolicies(w, r, code)
		return
	}
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

// isModelPoliciesSubResource reports whether the SplitN tail names the
// model-policies sub-tree (with or without a deeper path like /audit,
// /check, /{id}/undelete). Extracted as a pure helper so the routing rule
// is unit-testable without a DB pool (the handleTenants db guard otherwise
// short-circuits to 503 before routing is observable in tests).
//
// Pre-fix this matched only `sub == "model-policies"`, which silently
// dropped every /model-policies/{deeper} request.
func isModelPoliciesSubResource(sub string) bool {
	return sub == "model-policies" || strings.HasPrefix(sub, "model-policies/")
}

// ── listTenants: GET /api/admin/tenants ──────────────────────────

func (h *Handler) listTenantsAdmin(w http.ResponseWriter, r *http.Request) {
	// 2026-10-03：列表主查询和 7 天用量富化**必须用不同的预算**。
	//
	// 原来两者共用同一个 5s ctx，而 attachTenantUsage7d 那条聚合在真实数据量下
	// 要 20s+（request_logs_with_current_month 7 天 32 万行）。于是每次打开
	// /api/admin/tenants 都先白等满 5 秒，富化再被 ctx 掐断、用量列永远为空 ——
	// 代价全付了，收益一点没拿到（实测 duration_ms=5001，HTTP 仍 200）。
	//
	// 主查询保持 5s（它本身 12ms，预算只是兜底）；富化单独给 1.5s 且**不阻塞**
	// 列表返回：查不到就按 0 渲染，并在服务端留痕。页面对运营的价值来自租户名单，
	// 7 天用量是锦上添花，不该拖住前者。
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
		writeInternalErr(w, "query failed", err)
		return
	}
	defer rows.Close()

	tenants := make([]tenantInfo, 0)
	for rows.Next() {
		var t tenantInfo
		if err := rows.Scan(&t.Code, &t.Name, &t.Status, &t.Description, &t.ContactEmail,
			&t.CreatedAt, &t.UpdatedAt, &t.UserCount, &t.APIKeyCount, &t.TotalRequests); err != nil {
			warnRowSkip("tenants.list", err)
			continue
		}
		tenants = append(tenants, t)
	}
	if writeAggRowsErr(w, "tenants.list", rows.Err()) {
		return
	}
	if tenants == nil {
		tenants = []tenantInfo{}
	}
	// 富化用独立且更短的预算：不占用列表主查询的 5s，也不把整页拖到超时。
	// 派生自 r.Context() 而非 ctx，这样列表主查询的 cancel() 不会连带掐掉它。
	usageCtx, usageCancel := context.WithTimeout(r.Context(), 1500*time.Millisecond)
	defer usageCancel()
	h.attachTenantUsage7d(usageCtx, tenants)
	writeJSON(w, http.StatusOK, tenants)
}

// attachTenantUsage7d fills 7-day credits (tenant billing) + cost_usd (upstream) from request_logs.
func (h *Handler) attachTenantUsage7d(ctx context.Context, tenants []tenantInfo) {
	if len(tenants) == 0 {
		return
	}
	codes := make([]string, len(tenants))
	idx := make(map[string]int, len(tenants))
	for i, t := range tenants {
		codes[i] = t.Code
		idx[t.Code] = i
	}
	rows, err := h.db.Query(ctx, `
		SELECT tenant_id,
		       COUNT(*)::bigint,
		       COALESCE(SUM(COALESCE(prompt_tokens, 0) + COALESCE(completion_tokens, 0)), 0)::bigint,
		       COALESCE(SUM(COALESCE(credits_charged, 0)), 0)::bigint,
		       COALESCE(SUM(COALESCE(cost_usd, 0)), 0)::float8
		FROM request_logs_with_current_month
		WHERE tenant_id = ANY($1)
		  AND ts >= now() - INTERVAL '7 days'
		GROUP BY tenant_id
	`, codes)
	if err != nil {
		// 静默 return 会让「超时降级」和「这个租户确实没用量」长得一模一样。
		// 这里必须留痕：调用方只差一个 0 值，看不出来源。
		slog.Warn("tenants 7d usage enrichment failed; fields left at zero",
			"error", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var code string
		var reqs, tokens, credits int64
		var cost float64
		if err := rows.Scan(&code, &reqs, &tokens, &credits, &cost); err != nil {
			warnRowSkip("tenants.attachUsage7d", err)
			continue
		}
		if i, ok := idx[code]; ok {
			tenants[i].Requests7d = reqs
			tenants[i].Tokens7d = tokens
			tenants[i].Credits7d = credits
			tenants[i].Cost7d = cost
		}
	}
	// 富化降级通道（无 ResponseWriter）：查询失败时上面已经直接 return，
	// 迭代中断同样不能静默——留痕后按已取到的租户继续，不把 7 天用量升格成
	// listTenants 的 500。
	if rerr := rows.Err(); rerr != nil {
		slog.Warn("tenants usage 7d enrichment iteration aborted; fields degraded", "error", rerr)
	}
}

// ── createTenant: POST /api/admin/tenants ─────────────────────────

func (h *Handler) createTenant(w http.ResponseWriter, r *http.Request) {
	var req createTenantRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.Code) == "" || strings.TrimSpace(req.Name) == "" {
		writeError(w, http.StatusBadRequest, "code and name are required")
		return
	}
	req.Code = strings.TrimSpace(req.Code)
	req.Name = strings.TrimSpace(req.Name)
	if !isValidTenantCode(req.Code) {
		writeError(w, http.StatusBadRequest, "code must be 2-64 chars, lowercase alphanumeric, hyphens or underscores only")
		return
	}
	if req.Status != "" && !isValidTenantStatus(req.Status) {
		writeError(w, http.StatusBadRequest, "status must be one of: "+tenantValidStatuses)
		return
	}
	if req.Status == "" {
		req.Status = "active"
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	// 2026-07-21: Community mode tenant limit guard (issue: docs/TODO_COMMUNITY_MODE.md).
	// In community mode (license missing/expired/unactivated) the gateway
	// caps tenants at licensing.MaxCommunityTenants. Reject new tenants
	// above that cap with 403 so the operator gets a clear upgrade hint
	// instead of a silent half-configured instance.
	if err := h.checkCommunityTenantLimit(ctx); err != nil {
		var cmErr *CommunityTenantLimitError
		if errors.As(err, &cmErr) {
			writeError(w, http.StatusForbidden, cmErr.Error())
			return
		}
		// DB failure — log + 500 (do not silently bypass limit)
		writeInternalErr(w, "community mode limit check failed", err)
		return
	}

	adminUsername := DefaultTenantAdminUsername(req.Code)
	initialPassword := GenerateTenantAdminPassword(req.Code)
	hash, err := bcrypt.GenerateFromPassword([]byte(initialPassword), bcrypt.DefaultCost)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "password hash failed")
		return
	}

	tx, err := h.db.Begin(ctx)
	if err != nil {
		writeInternalErr(w, "transaction start failed", err)
		return
	}
	//nolint:errcheck // deferred rollback, best-effort
	defer tx.Rollback(ctx)

	var t tenantInfo
	err = tx.QueryRow(ctx, `
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
		writeInternalErr(w, "create tenant failed", err)
		return
	}

	displayName := req.Name + " 管理员"
	if req.ContactEmail != "" {
		displayName = req.Name + " Admin"
	}
	var admin userInfo
	err = tx.QueryRow(ctx, `
		INSERT INTO users (tenant_id, username, password_hash, display_name, email, role, enabled, must_change_password)
		VALUES ($1, $2, $3, $4, $5, 'tenant_admin', TRUE, TRUE)
		RETURNING id, tenant_id, username, display_name, email, role, enabled, must_change_password, created_at
	`, req.Code, adminUsername, string(hash), displayName, req.ContactEmail).Scan(
		&admin.ID, &admin.TenantID, &admin.Username, &admin.DisplayName, &admin.Email,
		&admin.Role, &admin.Enabled, &admin.MustChangePassword, &admin.CreatedAt,
	)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key") {
			writeError(w, http.StatusConflict, "default admin username already exists: "+adminUsername)
			return
		}
		writeInternalErr(w, "create default admin failed", err)
		return
	}

	if err := tx.Commit(ctx); err != nil {
		writeInternalErr(w, "transaction commit failed", err)
		return
	}

	t.UserCount = 1
	h.auditLog(getActorFromRequest(r), "tenant.create", "tenant", 0, map[string]any{
		"code":          t.Code,
		"name":          t.Name,
		"status":        t.Status,
		"default_admin": admin.Username,
	})
	h.auditLog(getActorFromRequest(r), "user.create", "user", admin.ID, map[string]any{
		"username": admin.Username,
		"role":     admin.Role,
		"tenant":   admin.TenantID,
		"source":   "tenant.create.auto",
	})
	writeJSON(w, http.StatusCreated, createTenantResponse{
		tenantInfo:      t,
		DefaultAdmin:    &admin,
		InitialPassword: initialPassword,
	})
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

	// 7-day usage (billing credits + upstream cost from request_logs)
	_ = h.db.QueryRow(ctx, `
		SELECT COUNT(*)::bigint,
		       COALESCE(SUM(COALESCE(prompt_tokens, 0) + COALESCE(completion_tokens, 0)), 0)::bigint,
		       COALESCE(SUM(COALESCE(credits_charged, 0)), 0)::bigint,
		       COALESCE(SUM(COALESCE(cost_usd, 0)), 0)::float8
		FROM request_logs_with_current_month
		WHERE tenant_id = $1 AND ts >= now() - INTERVAL '7 days'
	`, code).Scan(&t.Requests7d, &t.Tokens7d, &t.Credits7d, &t.Cost7d)
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
	// The 'default' tenant is the system fallback and must never be disabled.
	if code == "default" && req.Status != nil && (*req.Status == "disabled" || *req.Status == "suspended") {
		writeError(w, http.StatusBadRequest, "the default tenant cannot be disabled or suspended")
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
		writeInternalErr(w, "update failed", err)
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
		"code":        t.Code,
		"new_status":  t.Status,
		"update_keys": sets,
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
		SELECT id, tenant_id, username, display_name, email, role, enabled, must_change_password, last_login_at, created_at
		FROM users WHERE tenant_id = $1 ORDER BY id
	`, code)
	if err != nil {
		writeInternalErr(w, "query failed", err)
		return
	}
	defer rows.Close()

	users := make([]userInfo, 0)
	for rows.Next() {
		var u userInfo
		if err := rows.Scan(&u.ID, &u.TenantID, &u.Username, &u.DisplayName, &u.Email,
			&u.Role, &u.Enabled, &u.MustChangePassword, &u.LastLoginAt, &u.CreatedAt); err != nil {
			warnRowSkip("tenants.listUsers", err)
			continue
		}
		users = append(users, u)
	}
	if writeAggRowsErr(w, "tenants.listUsers", rows.Err()) {
		return
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
		       ak.total_requests, ak.total_cost_usd, ak.expires_at, ak.created_at
		FROM api_keys ak
		LEFT JOIN applications app ON app.id = ak.application_id
		WHERE ak.tenant_id = $1
		ORDER BY ak.id DESC
	`, code)
	if err != nil {
		writeInternalErr(w, "query failed", err)
		return
	}
	defer rows.Close()

	type tenantKeyInfo struct {
		ID        int        `json:"id"`
		TenantID  string     `json:"tenant_id"`
		KeyPrefix string     `json:"key_prefix"`
		KeyAlias  *string    `json:"key_alias,omitempty"`
		OwnerUser *string    `json:"owner_user,omitempty"`
		Enabled   bool       `json:"enabled"`
		Status    string     `json:"status"`
		AppID     int        `json:"application_id"`
		AppCode   *string    `json:"application_code,omitempty"`
		TotalReqs int64      `json:"total_requests"`
		TotalCost float64    `json:"total_cost_usd"`
		ExpiresAt *time.Time `json:"expires_at,omitempty"`
		CreatedAt time.Time  `json:"created_at"`
	}

	keys := make([]tenantKeyInfo, 0)
	for rows.Next() {
		var k tenantKeyInfo
		var expiresAt *time.Time
		if err := rows.Scan(&k.ID, &k.TenantID, &k.KeyPrefix, &k.KeyAlias, &k.OwnerUser,
			&k.Enabled, &k.Status, &k.AppID, &k.AppCode,
			&k.TotalReqs, &k.TotalCost, &expiresAt, &k.CreatedAt); err != nil {
			warnRowSkip("tenants.listKeys", err)
			continue
		}
		k.ExpiresAt = expiresAt
		keys = append(keys, k)
	}
	if writeAggRowsErr(w, "tenants.listKeys", rows.Err()) {
		return
	}
	if keys == nil {
		keys = []tenantKeyInfo{}
	}
	writeJSON(w, http.StatusOK, keys)
}

// ── getTenantStats: GET /api/admin/tenants/{code}/stats ───────────

func (h *Handler) getTenantStats(w http.ResponseWriter, r *http.Request, code string) {
	// 2026-09-30 R36-B3：5s 在真库上不够——byModel/byApp/daily 聚合在
	// 百万行级 usage/request 家族上实测可达 3s+（252-dev 实测），5s 会把
	// 后续查询饿死成静默空序列。上调至 10s（与 users 端点 15s 同量级）。
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	// Verify tenant exists.
	//
	// 2026-09-30 (R36-B4): the error was discarded as `_ =`, so ANY database
	// failure — deadline, connection loss, 57014 statement_timeout — left
	// `exists` at its zero value and the handler answered 404 "tenant not
	// found" for a tenant that exists. A database error is not a missing
	// tenant; surface it as a failure so the caller does not cache a false 404.
	var exists bool
	if err := h.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tenants WHERE code = $1)`, code).Scan(&exists); err != nil {
		writeTenantStatsError(w, ctx, code, "tenant existence check", err)
		return
	}
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
		Days          int                    `json:"days"`
		TotalRequests int64                  `json:"total_requests"`
		TotalTokens   int64                  `json:"total_tokens"`
		TotalCredits  int64                  `json:"total_credits"`
		TotalCost     float64                `json:"total_cost_usd"`
		UniqueKeys    int                    `json:"unique_keys"`
		UniqueModels  int                    `json:"unique_models"`
		UniqueApps    int                    `json:"unique_apps"`
		InputTokens   int64                  `json:"input_tokens"`
		OutputTokens  int64                  `json:"output_tokens"`
		CacheRead     int64                  `json:"cache_read_tokens"`
		CacheWrite    int64                  `json:"cache_write_tokens"`
		AvgLatencyMs  float64                `json:"avg_latency_ms"`
		ByModel       []tenantModelBreakdown `json:"by_model"`
		ByApplication []tenantAppBreakdown   `json:"by_application"`
		Daily         []tenantDailyStat      `json:"daily"`
	}

	var s tenantStats
	s.Days = days

	// 2026-09-30 三十六轮合并定稿（远端十五轮 P1 实勘 + 本轮 R36-B3 真库根修）：
	// ① hot 保留窗仅 8h（partition_manager.DefaultRetentionWindow）——days<=7
	// 走 hot-only 会让默认"近 7 天"视图 totals/credits/daily 只见最近 ~8h、
	// 前 ~6 天恒零，且与同响应 byModel/byApp 自相矛盾 → 全窗口统一读并集。
	// ② 富化视图 request_logs_with_current_month（LATERAL request_class + 双
	// NOT EXISTS 反连接 session_turns）带租户过滤 COUNT 即 30s 超时（252-dev
	// 实测，users 端点头注同因放弃该视图）→ 聚合读面改走内联 raw union
	//（migration 330 首发形态）：显式列投影——真库 hot 与父表列数已漂移，
	// SELECT * UNION 报 42601。语义：不排除已进 session_turns 的请求（与
	// totals/修复前父表读法一致，统计聚合数全量请求）。
	logsTable := `(SELECT tenant_id, ts, application_id, outbound_model, client_model,
	                       success, total_tokens, prompt_tokens, completion_tokens,
	                       credits_charged, cost_usd, latency_ms,
	                       cache_read_tokens, cache_write_tokens
	                FROM request_logs_hot
	        UNION ALL
	               SELECT tenant_id, ts, application_id, outbound_model, client_model,
	                      success, total_tokens, prompt_tokens, completion_tokens,
	                      credits_charged, cost_usd, latency_ms,
	                      cache_read_tokens, cache_write_tokens
	        FROM request_logs) -- sqlreadguard:allow R36-A1 漏热尾根修的双腿之母表腿（hot 腿同查询内联；252-dev 实测 NOT EXISTS 反连接 + 租户过滤 COUNT 30s 超时，故走 hot∪母表 UNION ALL）`
	usageTable := "usage_ledger_with_current_month"

	// Overall totals (upstream cost from usage_ledger; credits from request_logs)
	//
	// 2026-09-30 (R36-B4): both totals were `_ =`, so a dead context or a
	// statement_timeout silently published TotalRequests=0 / TotalCredits=0
	// alongside whatever the later queries managed to return — an internally
	// inconsistent 200 that reads as "this tenant burned nothing". Fail loudly
	// instead of fabricating zeros.
	if err := h.db.QueryRow(ctx, `
		SELECT COUNT(*), COALESCE(SUM(total_tokens), 0), COALESCE(SUM(cost_usd), 0.0),
		       COUNT(DISTINCT api_key_id), COUNT(DISTINCT COALESCE(NULLIF(raw_model_name, ''), canonical_id::text)),
		       COUNT(DISTINCT application_id)
		FROM `+usageTable+`
		WHERE tenant_id = $1 AND ts >= now() - ($2 * INTERVAL '1 day')
	`, code, days).Scan(&s.TotalRequests, &s.TotalTokens, &s.TotalCost,
		&s.UniqueKeys, &s.UniqueModels, &s.UniqueApps); err != nil {
		writeTenantStatsError(w, ctx, code, "totals aggregate", err)
		return
	}
	if err := h.db.QueryRow(ctx, `
		SELECT COALESCE(SUM(COALESCE(credits_charged, 0)), 0)::bigint,
		       COALESCE(SUM(COALESCE(prompt_tokens, 0)), 0)::bigint,
		       COALESCE(SUM(COALESCE(completion_tokens, 0)), 0)::bigint,
		       COALESCE(SUM(COALESCE(cache_read_tokens, 0)), 0)::bigint,
		       COALESCE(SUM(COALESCE(cache_write_tokens, 0)), 0)::bigint,
		       COALESCE(AVG(latency_ms), 0)::float8
		FROM `+logsTable+`
		WHERE tenant_id = $1 AND ts >= now() - ($2 * INTERVAL '1 day')
	`, code, days).Scan(
		&s.TotalCredits, &s.InputTokens, &s.OutputTokens,
		&s.CacheRead, &s.CacheWrite, &s.AvgLatencyMs); err != nil {
		writeTenantStatsError(w, ctx, code, "credits aggregate", err)
		return
	}

	// By model (credits + cost from request_logs)
	modelRows, err := h.db.Query(ctx, `
		SELECT COALESCE(NULLIF(TRIM(outbound_model), ''), NULLIF(TRIM(client_model), ''), '<unknown>') AS model,
		       COUNT(*)::bigint,
		       COALESCE(SUM(COALESCE(prompt_tokens, 0) + COALESCE(completion_tokens, 0)), 0)::bigint,
		       COALESCE(SUM(COALESCE(credits_charged, 0)), 0)::bigint,
		       COALESCE(SUM(COALESCE(cost_usd, 0)), 0)::float8
		FROM `+logsTable+`
		WHERE tenant_id = $1 AND ts >= now() - ($2 * INTERVAL '1 day')
		GROUP BY 1
		ORDER BY SUM(COALESCE(credits_charged, 0)) DESC, SUM(COALESCE(cost_usd, 0)) DESC
		LIMIT 20
	`, code, days)
	if err != nil {
		// R36-B4: `if err == nil { ... }` with no else published an empty
		// by_model list on failure — the client cannot tell "no traffic" from
		// "the query died". Report the failure.
		writeTenantStatsError(w, ctx, code, "by-model aggregate", err)
		return
	}
	{
		defer modelRows.Close()
		for modelRows.Next() {
			var m tenantModelBreakdown
			// R78: 扫描失败原本是 `_ =`，坏行会把全零的 model 条目塞进
			// by_model（客户端看不出是坏数据还是真零流量）。跳行留痕。
			if scanErr := modelRows.Scan(&m.Model, &m.Requests, &m.Tokens, &m.Credits, &m.Cost); scanErr != nil {
				warnRowSkip("tenants.stats.byModel", scanErr)
				continue
			}
			s.ByModel = append(s.ByModel, m)
		}
		// 迭代中断会静默截断 top-20 列表；同 daily 段口径显式失败。
		if rerr := modelRows.Err(); rerr != nil {
			writeTenantStatsError(w, ctx, code, "by-model aggregate iteration", rerr)
			return
		}
	}
	if s.ByModel == nil {
		s.ByModel = []tenantModelBreakdown{}
	}

	// By application (credits + cost from request_logs)
	//
	// R65: 别名 `rl` 必须从新行开始——logsTable 片段以 `-- sqlreadguard:allow`
	// 行注释收尾，同一行拼接的任何 token 都会被注释吞掉（修前实锤
	// 42P01 missing FROM-clause entry for table "rl"，TestTenantStatsDaily_Live
	// 是本修法的回归网）。
	appRows, err := h.db.Query(ctx, `
		SELECT COALESCE(app.code, '<none>') AS app_code,
		       COUNT(*)::bigint,
		       COALESCE(SUM(COALESCE(rl.prompt_tokens, 0) + COALESCE(rl.completion_tokens, 0)), 0)::bigint,
		       COALESCE(SUM(COALESCE(rl.credits_charged, 0)), 0)::bigint,
		       COALESCE(SUM(COALESCE(rl.cost_usd, 0)), 0)::float8
		FROM `+logsTable+`
		rl
		LEFT JOIN applications app ON app.id = rl.application_id
		WHERE rl.tenant_id = $1 AND rl.ts >= now() - ($2 * INTERVAL '1 day')
		GROUP BY app.code
		ORDER BY SUM(COALESCE(rl.credits_charged, 0)) DESC, SUM(COALESCE(rl.cost_usd, 0)) DESC
		LIMIT 20
	`, code, days)
	if err != nil {
		writeTenantStatsError(w, ctx, code, "by-application aggregate", err)
		return
	}
	{
		defer appRows.Close()
		for appRows.Next() {
			var a tenantAppBreakdown
			if scanErr := appRows.Scan(&a.AppCode, &a.Requests, &a.Tokens, &a.Credits, &a.Cost); scanErr != nil {
				warnRowSkip("tenants.stats.byApplication", scanErr)
				continue
			}
			s.ByApplication = append(s.ByApplication, a)
		}
		if rerr := appRows.Err(); rerr != nil {
			writeTenantStatsError(w, ctx, code, "by-application aggregate iteration", rerr)
			return
		}
	}
	if s.ByApplication == nil {
		s.ByApplication = []tenantAppBreakdown{}
	}

	// Daily time series (2026-09-30 统计 UI 优化轮)：generate_series 左连接
	// 补零，保证前端趋势图拿到 days 条连续日期。表选择与上方 credits/byModel/
	// byApp 同口径（全窗口 raw union 含热尾，三十六轮合并定稿）。
	// 日切显式钉 Asia/Shanghai（R36-A3）：与 usage_facts 日分区边界（迁移
	// 750/751）及用户统计 daily 同口径，不随会话时区漂移；对账页保持显式
	// UTC 日（结算口径，有意分叉）。
	dailyRows, err := h.db.Query(ctx, `
		WITH days AS (
			SELECT generate_series(
				date_trunc('day', now() AT TIME ZONE 'Asia/Shanghai') - (($2::int - 1) * INTERVAL '1 day'),
				date_trunc('day', now() AT TIME ZONE 'Asia/Shanghai'),
				INTERVAL '1 day')::date AS d
		), agg AS (
			SELECT date_trunc('day', ts AT TIME ZONE 'Asia/Shanghai')::date AS d,
			       COUNT(*)::bigint AS requests,
			       COUNT(*) FILTER (WHERE success IS TRUE)::bigint AS success,
			       COUNT(*) FILTER (WHERE success IS NOT TRUE)::bigint AS errors,
			       COALESCE(SUM(COALESCE(total_tokens, 0)), 0)::bigint AS tokens,
			       COALESCE(SUM(COALESCE(credits_charged, 0)), 0)::bigint AS credits,
			       COALESCE(SUM(COALESCE(cost_usd, 0)), 0)::float8 AS cost
			FROM `+logsTable+`
			WHERE tenant_id = $1 AND ts >= now() - ($2 * INTERVAL '1 day')
			GROUP BY 1
		)
		SELECT to_char(days.d, 'YYYY-MM-DD'),
		       COALESCE(agg.requests, 0), COALESCE(agg.success, 0), COALESCE(agg.errors, 0),
		       COALESCE(agg.tokens, 0), COALESCE(agg.credits, 0), COALESCE(agg.cost, 0)
		FROM days LEFT JOIN agg ON agg.d = days.d
		ORDER BY days.d
	`, code, days)
	if err != nil {
		// R36-A2 logged this; R36-B4 raises it to an explicit failure. A warn
		// plus an all-zero trend is still a 200 the client will render as
		// "this tenant had no traffic", which is a materially wrong answer
		// rather than a missing one.
		writeTenantStatsError(w, ctx, code, "daily aggregate", err)
		return
	}
	for dailyRows.Next() {
		var d tenantDailyStat
		if scanErr := dailyRows.Scan(&d.Date, &d.Requests, &d.Success, &d.Errors, &d.Tokens, &d.Credits, &d.Cost); scanErr != nil {
			dailyRows.Close()
			writeTenantStatsError(w, ctx, code, "daily aggregate scan", scanErr)
			return
		}
		s.Daily = append(s.Daily, d)
	}
	if rerr := dailyRows.Err(); rerr != nil {
		// Truncated series: pgx surfaces mid-iteration failures (connection
		// drop, statement_timeout) here, after Next() already returned true.
		dailyRows.Close()
		writeTenantStatsError(w, ctx, code, "daily aggregate iteration", rerr)
		return
	}
	dailyRows.Close()
	if s.Daily == nil {
		s.Daily = []tenantDailyStat{}
	}

	writeJSON(w, http.StatusOK, s)
}

// writeTenantStatsError reports a failed stage of the tenant stats pipeline as
// an explicit HTTP error instead of letting the zero value reach the client.
//
// 2026-09-30 (R36-B4): every aggregate in getTenantStats used to swallow its
// error, so one dead context produced a fully-formed 200 whose totals were
// zero and whose breakdowns were empty. That is the worst failure shape for a
// stats endpoint — the caller cannot distinguish "no traffic" from "the query
// was killed", and both reconcile and billing read these numbers.
//
// A context deadline is reported as 504 with a hint to narrow `days`, because
// the aggregates are sequential on one shared 10s budget and the 30-day window
// is the one that overruns it. Anything else is a 500.
func writeTenantStatsError(w http.ResponseWriter, ctx context.Context, code, stage string, err error) {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		slog.Warn("tenant stats: stage exceeded context budget",
			"tenant", code, "stage", stage, "err", err)
		writeError(w, http.StatusGatewayTimeout,
			"tenant stats query timed out; retry with a smaller days window")
		return
	}
	slog.Error("tenant stats: stage failed", "tenant", code, "stage", stage, "err", err)
	writeError(w, http.StatusInternalServerError, "tenant stats query failed")
}

// getActorFromRequest returns the username from AuthContext, or "unknown".
func getActorFromRequest(r *http.Request) string {
	auth := GetAuthContext(r)
	if auth != nil && auth.Username != "" {
		return auth.Username
	}
	return "unknown"
}

// CommunityTenantLimitError signals that createTenant is blocked because
// the gateway is running in community mode and the tenant cap is reached.
// Returning a typed error lets the caller writeError with a 403 without
// inspecting free-form strings.
type CommunityTenantLimitError struct {
	Current int
	Max     int
}

func (e *CommunityTenantLimitError) Error() string {
	return "community_mode: tenant limit reached (" +
		strconv.Itoa(e.Current) + "/" + strconv.Itoa(e.Max) +
		"). Activate a license to add more tenants."
}

// checkCommunityTenantLimit enforces the community-mode tenant cap.
//
// Returns nil if not in community mode or if the cap is not yet reached.
// Returns *CommunityTenantLimitError (HTTP 403) when the cap is full.
// Returns a generic error for DB failures (callers should 500, NOT bypass).
//
// Cheap O(1) — only does a SELECT COUNT(*) when community mode is active.
func (h *Handler) checkCommunityTenantLimit(ctx context.Context) error {
	if !licensing.IsCommunityMode() {
		return nil
	}
	var count int
	if err := h.db.QueryRow(ctx, `SELECT COUNT(*) FROM tenants`).Scan(&count); err != nil {
		return err
	}
	if count >= licensing.MaxCommunityTenants {
		return &CommunityTenantLimitError{
			Current: count,
			Max:     licensing.MaxCommunityTenants,
		}
	}
	return nil
}
