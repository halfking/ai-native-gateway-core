package admin

// 组织管理 API（三级配额制：租户→组织→用户，2026-11-27）。
//
// 路由（super_admin only，走 handleTenants 同款鉴权包装）：
//
//	GET    /api/admin/orgs                 组织列表（带钱包余额/成员数，?tenant_id= 筛选）
//	POST   /api/admin/orgs                 创建组织（自动建钱包，balance=0，策略 warn）
//	GET    /api/admin/orgs/{id}            组织详情（钱包 + 成员名单）
//	PATCH  /api/admin/orgs/{id}            更新组织（name/description/enabled）
//	DELETE /api/admin/orgs/{id}            删除组织（级联：钱包删、成员 org_id 置 NULL）
//	PATCH  /api/admin/orgs/{id}/wallet     调整钱包（balance/credit_limit/policy 调整）
//
// 口径：余额/信用额度 NULL = 不限制（budget_usd NULL 先例）；policy warn|block。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type orgInfo struct {
	ID          int        `json:"id"`
	TenantID    string     `json:"tenant_id"`
	Code        string     `json:"code"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Enabled     bool       `json:"enabled"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	MemberCount int        `json:"member_count,omitempty"`
	Wallet      *orgWallet `json:"wallet,omitempty"`
}

type orgWallet struct {
	BalanceCredits     *int64 `json:"balance_credits"`                // 与租户钱包不同：可 NULL=未配置（展示层按 0 处理）
	CreditLimitCredits *int64 `json:"credit_limit_credits"`           // NULL = 不限制
	CreditPolicy       string `json:"credit_policy"`                  // warn | block
}

type createOrgRequest struct {
	TenantID    string `json:"tenant_id"`
	Code        string `json:"code"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type updateOrgRequest struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	Enabled     *bool   `json:"enabled"`
}

// updateOrgWalletRequest 钱包调整。
//	credit_limit_credits 三态（**int64 双指针在 encoding/json 下无法区分
//	「缺省」与「null」—— 两者都把外层指针置 nil ⇒ 双指针设计是死的）：
//	  缺省          = 保持现状
//	  数值          = 设值
//	  "unlimited"   = 设 NULL（不限制）
//	解析用 creditLimitPayload：json.RawMessage 先收，再按形态分流。
type updateOrgWalletRequest struct {
	BalanceCredits     *int64           `json:"balance_credits"`
	CreditLimitCredits json.RawMessage  `json:"credit_limit_credits,omitempty"`
	CreditPolicy       *string          `json:"credit_policy"`
}

// parseCreditLimit resolves the three-state limit payload:
// returns (valuePtr, setNull, keep, error).
func parseCreditLimit(raw json.RawMessage) (*int64, bool, bool, error) {
	if len(raw) == 0 {
		return nil, false, true, nil // keep
	}
	s := strings.TrimSpace(string(raw))
	if s == "null" {
		return nil, false, true, nil // JSON null == keep (absent ≡ null in encoding/json)
	}
	if s == `"unlimited"` {
		return nil, true, false, nil // set NULL
	}
	var v int64
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, false, false, fmt.Errorf("credit_limit_credits must be a number or \"unlimited\"")
	}
	return &v, false, false, nil
}

// isValidOrgCode 与租户代码同规则（小写字母数字 _-，2-64 长）。
func isValidOrgCode(code string) bool { return isValidTenantCode(code) }

// handleOrgs dispatches /api/admin/orgs and /api/admin/orgs/{id}[/{sub}].
func (h *Handler) handleOrgs(w http.ResponseWriter, r *http.Request) {
	if auth := GetAuthContext(r); auth != nil && auth.Role != "super_admin" && auth.Role != "admin_key" {
		writeError(w, http.StatusForbidden, "super_admin role required for this endpoint")
		return
	}
	if h.policyDB() == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/api/admin/orgs")
	path = strings.Trim(path, "/")

	if path == "" {
		switch r.Method {
		case http.MethodGet:
			h.listOrgs(w, r)
		case http.MethodPost:
			h.createOrg(w, r)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}

	parts := strings.SplitN(path, "/", 2)
	id, err := strconv.Atoi(parts[0])
	if err != nil || id <= 0 {
		writeError(w, http.StatusNotFound, "invalid org id")
		return
	}
	sub := ""
	if len(parts) > 1 {
		sub = parts[1]
	}

	if sub == "" {
		switch r.Method {
		case http.MethodGet:
			h.getOrg(w, r, id)
		case http.MethodPatch:
			h.updateOrg(w, r, id)
		case http.MethodDelete:
			h.deleteOrg(w, r, id)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}

	switch {
	case sub == "wallet" && r.Method == http.MethodPatch:
		h.updateOrgWallet(w, r, id)
	case sub == "members" && r.Method == http.MethodGet:
		h.listOrgMembers(w, r, id)
	case strings.HasPrefix(sub, "model-policies"):
		// 三级配额制（2026-11-27）：org 级模型策略管理。
		h.handleOrgModelPolicies(w, r, id, strings.TrimPrefix(sub, "model-policies"))
	default:
		writeError(w, http.StatusNotFound, "unknown sub-resource: "+sub)
	}
}

const orgWalletSelect = `
	COALESCE(w.balance_credits, 0), w.credit_limit_credits, COALESCE(w.credit_policy, 'warn')`

func scanOrgWallet(orgID int, balance int64, limit *int64, policy string) *orgWallet {
	return &orgWallet{
		BalanceCredits:     &balance,
		CreditLimitCredits: limit,
		CreditPolicy:       policy,
	}
}

// listOrgs: GET /api/admin/orgs?tenant_id=
func (h *Handler) listOrgs(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	where := "1=1"
	args := []any{}
	if t := r.URL.Query().Get("tenant_id"); t != "" {
		where = "o.tenant_id = $1"
		args = append(args, t)
	}

	rows, err := h.policyDB().Query(ctx, `
		SELECT o.id, o.tenant_id, o.code, o.name, o.description, o.enabled, o.created_at, o.updated_at,
		       COALESCE(m.member_count, 0),
		       `+orgWalletSelect+`
		FROM organizations o
		LEFT JOIN (
			SELECT org_id, COUNT(*) AS member_count FROM users WHERE org_id IS NOT NULL GROUP BY org_id
		) m ON m.org_id = o.id
		LEFT JOIN org_credit_wallets w ON w.org_id = o.id
		WHERE `+where+`
		ORDER BY o.tenant_id, o.code
	`, args...)
	if err != nil {
		writeInternalErr(w, "query failed", err)
		return
	}
	defer rows.Close()

	orgs := make([]orgInfo, 0)
	for rows.Next() {
		var o orgInfo
		var balance int64
		var limit *int64
		var policy string
		if err := rows.Scan(&o.ID, &o.TenantID, &o.Code, &o.Name, &o.Description, &o.Enabled,
			&o.CreatedAt, &o.UpdatedAt, &o.MemberCount, &balance, &limit, &policy); err != nil {
			warnRowSkip("orgs.list", err)
			continue
		}
		o.Wallet = scanOrgWallet(o.ID, balance, limit, policy)
		orgs = append(orgs, o)
	}
	if writeAggRowsErr(w, "orgs.list", rows.Err()) {
		return
	}
	if orgs == nil {
		orgs = []orgInfo{}
	}
	writeJSON(w, http.StatusOK, orgs)
}

// createOrg: POST /api/admin/orgs —— 建组织 + 空钱包（warn 策略，不限额度）。
func (h *Handler) createOrg(w http.ResponseWriter, r *http.Request) {
	var req createOrgRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !isValidOrgCode(req.Code) {
		writeError(w, http.StatusBadRequest, "invalid org code (2-64 chars, lowercase alphanumeric/-/_)")
		return
	}
	if req.Name == "" || req.TenantID == "" {
		writeError(w, http.StatusBadRequest, "tenant_id and name are required")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var exists bool
	if err := h.policyDB().QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tenants WHERE code = $1)`, req.TenantID).Scan(&exists); err != nil || !exists {
		writeError(w, http.StatusBadRequest, "tenant not found")
		return
	}

	var o orgInfo
	err := h.policyDB().QueryRow(ctx, `
		INSERT INTO organizations (tenant_id, code, name, description)
		VALUES ($1, $2, $3, $4)
		RETURNING id, tenant_id, code, name, description, enabled, created_at, updated_at
	`, req.TenantID, req.Code, req.Name, req.Description).Scan(
		&o.ID, &o.TenantID, &o.Code, &o.Name, &o.Description, &o.Enabled, &o.CreatedAt, &o.UpdatedAt)
	if err != nil {
		if strings.Contains(err.Error(), "organizations_tenant_code_unique") || strings.Contains(err.Error(), "duplicate key") {
			writeError(w, http.StatusConflict, "org code already exists in this tenant")
			return
		}
		writeInternalErr(w, "create org failed", err)
		return
	}

	// 钱包随组织自动建（balance=0、不限额度、warn）——预检的「无行=不限」语义
	// 不依赖钱包行存在，建行只为管理面有明确的展示/调整锚点。
	if _, err := h.db.Exec(ctx, `
		INSERT INTO org_credit_wallets (org_id, tenant_id, balance_credits, credit_limit_credits, credit_policy)
		VALUES ($1, $2, 0, NULL, 'warn')
		ON CONFLICT (org_id) DO NOTHING
	`, o.ID, o.TenantID); err != nil {
		writeInternalErr(w, "create org wallet failed", err)
		return
	}
	o.Wallet = &orgWallet{BalanceCredits: orgWalletInt64Ptr(0), CreditLimitCredits: nil, CreditPolicy: "warn"}
	writeJSON(w, http.StatusCreated, o)
}

func orgWalletInt64Ptr(v int64) *int64 { return &v }

// getOrg: GET /api/admin/orgs/{id} —— 详情含钱包与成员名单。
func (h *Handler) getOrg(w http.ResponseWriter, r *http.Request, id int) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var o orgInfo
	var balance int64
	var limit *int64
	var policy string
	err := h.policyDB().QueryRow(ctx, `
		SELECT o.id, o.tenant_id, o.code, o.name, o.description, o.enabled, o.created_at, o.updated_at,
		       COALESCE((SELECT COUNT(*) FROM users WHERE org_id = o.id), 0),
		       `+orgWalletSelect+`
		FROM organizations o
		LEFT JOIN org_credit_wallets w ON w.org_id = o.id
		WHERE o.id = $1
	`, id).Scan(&o.ID, &o.TenantID, &o.Code, &o.Name, &o.Description, &o.Enabled,
		&o.CreatedAt, &o.UpdatedAt, &o.MemberCount, &balance, &limit, &policy)
	if err != nil {
		writeError(w, http.StatusNotFound, "org not found")
		return
	}
	o.Wallet = scanOrgWallet(o.ID, balance, limit, policy)

	members, err := h.orgMemberRows(ctx, id)
	if err != nil {
		writeInternalErr(w, "query members failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"org": o, "members": members})
}

// listOrgMembers: GET /api/admin/orgs/{id}/members —— 成员 + 用户钱包余额。
func (h *Handler) listOrgMembers(w http.ResponseWriter, r *http.Request, id int) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var exists bool
	if err := h.policyDB().QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM organizations WHERE id = $1)`, id).Scan(&exists); err != nil || !exists {
		writeError(w, http.StatusNotFound, "org not found")
		return
	}
	members, err := h.orgMemberRows(ctx, id)
	if err != nil {
		writeInternalErr(w, "query members failed", err)
		return
	}
	writeJSON(w, http.StatusOK, members)
}

// orgMemberRows 成员行：用户基础信息 + 用户钱包（余额/限额/策略）+ 组织余额
// （同一组织成员共享 org 钱包，逐行带出省一次前端请求）。
func (h *Handler) orgMemberRows(ctx context.Context, orgID int) ([]map[string]any, error) {
	rows, err := h.policyDB().Query(ctx, `
		SELECT u.id, u.tenant_id, u.username, u.display_name, u.email, u.role, u.enabled,
		       uow.balance_credits, uow.credit_limit_credits, COALESCE(uow.credit_policy, 'warn'),
		       oow.balance_credits, oow.credit_limit_credits, COALESCE(oow.credit_policy, 'warn')
		FROM users u
		LEFT JOIN user_credit_wallets uow ON uow.user_id = u.id
		JOIN organizations o ON o.id = u.org_id
		LEFT JOIN org_credit_wallets oow ON oow.org_id = o.id
		WHERE u.org_id = $1
		ORDER BY u.id
	`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	members := make([]map[string]any, 0)
	for rows.Next() {
		var id int
		var tenantID, username, displayName, email, role string
		var enabled bool
		var uBal, oBal *int64
		var uLimit, oLimit *int64
		var uPolicy, oPolicy string
		if err := rows.Scan(&id, &tenantID, &username, &displayName, &email, &role, &enabled,
			&uBal, &uLimit, &uPolicy, &oBal, &oLimit, &oPolicy); err != nil {
			warnRowSkip("orgs.members", err)
			continue
		}
		members = append(members, map[string]any{
			"id": id, "tenant_id": tenantID, "username": username,
			"display_name": displayName, "email": email, "role": role, "enabled": enabled,
			"user_wallet": map[string]any{
				"balance_credits":     uBal,
				"credit_limit_credits": uLimit,
				"credit_policy":       uPolicy,
			},
			"org_wallet": map[string]any{
				"balance_credits":     oBal,
				"credit_limit_credits": oLimit,
				"credit_policy":       oPolicy,
			},
		})
	}
	if err := rows.Err(); err != nil {
		warnRowSkip("orgs.members", err)
	}
	return members, nil
}

// updateOrg: PATCH /api/admin/orgs/{id} —— name/description/enabled。
func (h *Handler) updateOrg(w http.ResponseWriter, r *http.Request, id int) {
	var req updateOrgRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	tag, err := h.db.Exec(ctx, `
		UPDATE organizations SET
			name = COALESCE($2, name),
			description = COALESCE($3, description),
			enabled = COALESCE($4, enabled),
			updated_at = now()
		WHERE id = $1
	`, id, req.Name, req.Description, req.Enabled)
	if err != nil {
		writeInternalErr(w, "update org failed", err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "org not found")
		return
	}
	h.getOrg(w, r, id)
}

// deleteOrg: DELETE /api/admin/orgs/{id} —— 级联（钱包 CASCADE、成员 org_id SET NULL）。
func (h *Handler) deleteOrg(w http.ResponseWriter, r *http.Request, id int) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	tag, err := h.db.Exec(ctx, `DELETE FROM organizations WHERE id = $1`, id)
	if err != nil {
		writeInternalErr(w, "delete org failed", err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "org not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": id})
}

// updateOrgWallet: PATCH /api/admin/orgs/{id}/wallet —— 余额/限额/策略调整。
//
//	credit_limit_credits 用双指针：null=设为不限，数值=设值，缺省=保持。
func (h *Handler) updateOrgWallet(w http.ResponseWriter, r *http.Request, id int) {
	var req updateOrgWalletRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.CreditPolicy != nil && *req.CreditPolicy != "warn" && *req.CreditPolicy != "block" {
		writeError(w, http.StatusBadRequest, "credit_policy must be 'warn' or 'block'")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// ★ 2026-11-28 12h 审计轮（F-1）：先做 organizations 存在性预检（GET 成员
	//   列表 :317 同款惯例）。修复前 org 不存在时 ensure-wallet 的
	//   INSERT（tenant_id 子查询为 NULL ⇒ NOT NULL / FK 违约）会以
	//   500「ensure org wallet failed」漏出——内部错误信封泄漏存储细节；
	//   更早版本则把别名 42P01 误报成 404。预检后不存在 org 稳定 404。
	var exists bool
	if err := h.policyDB().QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM organizations WHERE id = $1)`, id).Scan(&exists); err != nil {
		// 真存储故障 ≠ 行不存在：500（writeInternalErr），不伪装成 404。
		writeInternalErr(w, "org precheck failed", err)
		return
	}
	if !exists {
		writeError(w, http.StatusNotFound, "org not found")
		return
	}

	// ★ 2026-11-28 审计轮：credit_limit 三态解析（及其 400）必须发生在**任何
	//   写操作之前**——修复前非法 payload 会先把 ensure-wallet/balance/policy
	//   落库、再在校验处返回 400（部分写副作用）。user 面（users.go）顺序
	//   本就正确，两面从此同形。
	limitVal, setNull, keep, perr := parseCreditLimit(req.CreditLimitCredits)
	if perr != nil {
		writeError(w, http.StatusBadRequest, perr.Error())
		return
	}

	// 余额/策略直接 COALESCE 合并；limit 三态单独处理。
	if _, err := h.policyDB().Exec(ctx, `
		INSERT INTO org_credit_wallets (org_id, tenant_id, balance_credits, credit_limit_credits, credit_policy)
		VALUES ($1, (SELECT tenant_id FROM organizations WHERE id = $1), COALESCE($2, 0), NULL, COALESCE($3, 'warn'))
		ON CONFLICT (org_id) DO NOTHING
	`, id, req.BalanceCredits, req.CreditPolicy); err != nil {
		writeInternalErr(w, "ensure org wallet failed", err)
		return
	}

	if req.BalanceCredits != nil {
		if _, err := h.policyDB().Exec(ctx, `UPDATE org_credit_wallets SET balance_credits = $2, updated_at = now() WHERE org_id = $1`, id, *req.BalanceCredits); err != nil {
			writeInternalErr(w, "update balance failed", err)
			return
		}
	}
	if req.CreditPolicy != nil {
		if _, err := h.policyDB().Exec(ctx, `UPDATE org_credit_wallets SET credit_policy = $2, updated_at = now() WHERE org_id = $1`, id, *req.CreditPolicy); err != nil {
			writeInternalErr(w, "update policy failed", err)
			return
		}
	}
	if !keep {
		if setNull {
			if _, err := h.policyDB().Exec(ctx, `UPDATE org_credit_wallets SET credit_limit_credits = NULL, updated_at = now() WHERE org_id = $1`, id); err != nil {
				writeInternalErr(w, "clear credit limit failed", err)
				return
			}
		} else {
			if _, err := h.policyDB().Exec(ctx, `UPDATE org_credit_wallets SET credit_limit_credits = $2, updated_at = now() WHERE org_id = $1`, id, *limitVal); err != nil {
				writeInternalErr(w, "update credit limit failed", err)
				return
			}
		}
	}

	var balance int64
	var limit *int64
	var policy string
	// ★ orgWalletSelect 引用了别名 w（供 JOIN 形态复用），裸查这里必须带
	//   同名别名，否则「missing FROM-clause entry for table w」（42P01）会被
	//   下面的 err 分支误报成「org not found」（2026-11-27 211 实测）。
	if err := h.policyDB().QueryRow(ctx, `SELECT `+orgWalletSelect+` FROM org_credit_wallets w WHERE w.org_id = $1`, id).Scan(&balance, &limit, &policy); err != nil {
		writeInternalErr(w, "read org wallet failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"wallet": scanOrgWallet(id, balance, limit, policy)})
}
