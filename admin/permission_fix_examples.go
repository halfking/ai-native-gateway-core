package admin

// 本文件展示如何修复缺少租户/用户隔离的端点。
// 这些是实际修复案例的模板，可以应用到其他端点。

import (
	"encoding/json"
	"net/http"

	"github.com/jackc/pgx/v5"
)

// ═══════════════════════════════════════════════════════════════════
// 示例 1: 修复列表端点的租户隔离
// ═══════════════════════════════════════════════════════════════════

// handleSessionsListBefore 是修复前的版本（❌ 缺少租户过滤）
func (h *Handler) handleSessionsListBefore(w http.ResponseWriter, r *http.Request) {
	// ❌ 问题：直接查询所有会话，tenant_admin 可以看到其他租户的数据
	query := `
		SELECT id, title, user_id, tenant_id, created_at, updated_at
		FROM sessions
		WHERE deleted_at IS NULL
		ORDER BY updated_at DESC
		LIMIT 50`

	rows, err := h.db.Query(r.Context(), query)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	defer rows.Close()

	// ... 返回结果
}

// handleSessionsListAfter 是修复后的版本（✅ 添加了租户过滤）
func (h *Handler) handleSessionsListAfter(w http.ResponseWriter, r *http.Request) {
	// ✅ 修复：添加租户过滤
	tenantFilter := EffectiveTenantIDAll(r)

	query := `
		SELECT id, title, user_id, tenant_id, created_at, updated_at
		FROM sessions
		WHERE deleted_at IS NULL`

	args := []any{}

	// super_admin 看全部，tenant_admin 只看自己租户
	if tenantFilter != "" {
		query += " AND tenant_id = $1"
		args = append(args, tenantFilter)
	}

	query += " ORDER BY updated_at DESC LIMIT 50"

	rows, err := h.db.Query(r.Context(), query, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	defer rows.Close()

	// ... 返回结果
}

// ═══════════════════════════════════════════════════════════════════
// 示例 2: 修复详情端点的归属检查
// ═══════════════════════════════════════════════════════════════════

// handleSessionDetailBefore 是修复前的版本（❌ 缺少归属检查）
func (h *Handler) handleSessionDetailBefore(w http.ResponseWriter, r *http.Request, sessionID int) {
	// ❌ 问题：只要知道 ID 就能访问任意会话，无视租户边界
	var session struct {
		ID       int    `json:"id"`
		Title    string `json:"title"`
		UserID   int    `json:"user_id"`
		TenantID string `json:"tenant_id"`
	}

	err := h.db.QueryRow(r.Context(),
		`SELECT id, title, user_id, tenant_id FROM sessions WHERE id = $1`,
		sessionID).Scan(&session.ID, &session.Title, &session.UserID, &session.TenantID)

	if err == pgx.ErrNoRows {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}

	// ❌ 直接返回，不检查权限
	json.NewEncoder(w).Encode(session)
}

// handleSessionDetailAfter 是修复后的版本（✅ 添加了归属检查）
func (h *Handler) handleSessionDetailAfter(w http.ResponseWriter, r *http.Request, sessionID int) {
	var session struct {
		ID       int    `json:"id"`
		Title    string `json:"title"`
		UserID   int    `json:"user_id"`
		TenantID string `json:"tenant_id"`
	}

	// 1. 先查询会话归属
	err := h.db.QueryRow(r.Context(),
		`SELECT id, title, user_id, tenant_id FROM sessions WHERE id = $1`,
		sessionID).Scan(&session.ID, &session.Title, &session.UserID, &session.TenantID)

	if err == pgx.ErrNoRows {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}

	// ✅ 2. 检查访问权限
	if !CanAccessUserData(r, session.UserID, session.TenantID) {
		writeError(w, http.StatusForbidden, "access denied")
		return
	}

	// ✅ 3. 权限通过后才返回数据
	json.NewEncoder(w).Encode(session)
}

// ═══════════════════════════════════════════════════════════════════
// 示例 3: 实现用户级端点（普通用户可访问）
// ═══════════════════════════════════════════════════════════════════

// handleMySessionsNew 是新增的用户级端点（✅ 普通用户可用）
func (h *Handler) handleMySessionsNew(w http.ResponseWriter, r *http.Request) {
	// ✅ 1. 要求至少是认证用户
	if RequireUserOrHigher(w, r) {
		return
	}

	// ✅ 2. 获取用户级过滤条件
	userID := EffectiveUserID(r)
	tenantID := GetTenantID(r)

	query := `
		SELECT id, title, created_at, updated_at
		FROM sessions
		WHERE tenant_id = $1
		AND deleted_at IS NULL`

	args := []any{tenantID}

	// ✅ 3. user 角色只看自己，admin 角色看本租户全部
	if userID != 0 {
		query += " AND user_id = $2"
		args = append(args, userID)
	}

	query += " ORDER BY updated_at DESC LIMIT 50"

	rows, err := h.db.Query(r.Context(), query, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	defer rows.Close()

	// ... 返回结果
}

// ═══════════════════════════════════════════════════════════════════
// 示例 4: 修复写操作端点的租户隔离
// ═══════════════════════════════════════════════════════════════════

// handleUpdateSessionBefore 是修复前的版本（❌ 缺少归属检查）
func (h *Handler) handleUpdateSessionBefore(w http.ResponseWriter, r *http.Request, sessionID int) {
	var req struct {
		Title string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}

	// ❌ 问题：直接更新，不检查会话归属
	_, err := h.db.Exec(r.Context(),
		`UPDATE sessions SET title = $1, updated_at = now() WHERE id = $2`,
		req.Title, sessionID)

	if err != nil {
		writeError(w, http.StatusInternalServerError, "update failed")
		return
	}

	w.WriteHeader(http.StatusOK)
}

// handleUpdateSessionAfter 是修复后的版本（✅ 添加了归属检查）
func (h *Handler) handleUpdateSessionAfter(w http.ResponseWriter, r *http.Request, sessionID int) {
	var req struct {
		Title string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}

	// ✅ 1. 先查询会话归属
	var session struct {
		UserID   int
		TenantID string
	}
	err := h.db.QueryRow(r.Context(),
		`SELECT user_id, tenant_id FROM sessions WHERE id = $1`,
		sessionID).Scan(&session.UserID, &session.TenantID)

	if err == pgx.ErrNoRows {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}

	// ✅ 2. 检查写权限
	if !CanAccessUserData(r, session.UserID, session.TenantID) {
		writeError(w, http.StatusForbidden, "access denied")
		return
	}

	// ✅ 3. 权限通过后才执行更新
	_, err = h.db.Exec(r.Context(),
		`UPDATE sessions SET title = $1, updated_at = now() WHERE id = $2`,
		req.Title, sessionID)

	if err != nil {
		writeError(w, http.StatusInternalServerError, "update failed")
		return
	}

	w.WriteHeader(http.StatusOK)
}

// ═══════════════════════════════════════════════════════════════════
// 示例 5: 修复删除端点的级联权限检查
// ═══════════════════════════════════════════════════════════════════

// handleDeleteSessionAfter 是修复后的版本（✅ 完整权限检查）
func (h *Handler) handleDeleteSessionAfter(w http.ResponseWriter, r *http.Request, sessionID int) {
	// ✅ 1. 写操作必须有写权限（阻止 tenant_admin 的只读访问）
	if RequireSuperAdminForWrite(w, r) {
		return
	}

	// ✅ 2. 查询会话归属
	var session struct {
		UserID   int
		TenantID string
	}
	err := h.db.QueryRow(r.Context(),
		`SELECT user_id, tenant_id FROM sessions WHERE id = $1 AND deleted_at IS NULL`,
		sessionID).Scan(&session.UserID, &session.TenantID)

	if err == pgx.ErrNoRows {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}

	// ✅ 3. 检查归属权限（super_admin 可以删除任意会话）
	if !CanAccessUserData(r, session.UserID, session.TenantID) {
		writeError(w, http.StatusForbidden, "access denied")
		return
	}

	// ✅ 4. 软删除
	_, err = h.db.Exec(r.Context(),
		`UPDATE sessions SET deleted_at = now() WHERE id = $1`,
		sessionID)

	if err != nil {
		writeError(w, http.StatusInternalServerError, "delete failed")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ═══════════════════════════════════════════════════════════════════
// 示例 6: 统计端点的租户聚合
// ═══════════════════════════════════════════════════════════════════

// handleUsageStatsAfter 是修复后的版本（✅ 租户聚合）
func (h *Handler) handleUsageStatsAfter(w http.ResponseWriter, r *http.Request) {
	tenantFilter := EffectiveTenantIDAll(r)

	query := `
		SELECT 
			tenant_id,
			count(*) as request_count,
			sum(cost_usd) as total_cost
		FROM request_logs
		WHERE created_at >= now() - interval '7 days'`

	args := []any{}

	// ✅ 租户过滤
	if tenantFilter != "" {
		query += " AND tenant_id = $1"
		args = append(args, tenantFilter)
	}

	query += " GROUP BY tenant_id"

	rows, err := h.db.Query(r.Context(), query, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	defer rows.Close()

	// ... 返回结果
}

// ═══════════════════════════════════════════════════════════════════
// 通用修复模式总结
// ═══════════════════════════════════════════════════════════════════

/*
修复检查清单：

□ 列表端点（GET /api/resource）
  1. 添加 tenantFilter := EffectiveTenantIDAll(r)
  2. 在 WHERE 子句添加 tenant_id 过滤
  3. 如果支持用户级访问，添加 userID := EffectiveUserID(r)

□ 详情端点（GET /api/resource/{id}）
  1. 查询资源归属（user_id, tenant_id）
  2. 调用 CanAccessUserData(r, userID, tenantID) 检查权限
  3. 权限不通过返回 403

□ 创建端点（POST /api/resource）
  1. 从请求体提取 tenant_id
  2. 调用 CanAccessTenant(r, tenantID) 检查权限
  3. 插入时使用经过验证的 tenant_id

□ 更新端点（PUT /api/resource/{id}）
  1. 先查询资源归属
  2. 检查权限（同详情端点）
  3. 权限通过后才执行 UPDATE

□ 删除端点（DELETE /api/resource/{id}）
  1. 先检查写权限（RequireSuperAdminForWrite）
  2. 查询资源归属
  3. 检查归属权限
  4. 权限通过后才执行 DELETE

□ 统计端点（GET /api/stats）
  1. 添加租户过滤（同列表端点）
  2. GROUP BY 时保留 tenant_id
  3. 确保聚合不跨租户泄漏

□ 用户级端点（GET /api/my/resource）
  1. 调用 RequireUserOrHigher(w, r) 确保认证
  2. 使用 EffectiveUserID(r) 获取用户过滤
  3. 同时过滤 tenant_id 和 user_id
*/
