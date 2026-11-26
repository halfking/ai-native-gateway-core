package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// handleMySessions 返回当前用户自己的会话列表
// GET /api/sessions/my
//
// 权限模型：
//   - role=user: 只看自己的会话
//   - role=tenant_admin: 看本租户所有会话
//   - role=super_admin: 看全平台会话
//
// 实施日期：2026-11-26（数据权限审计轮）
func (h *Handler) handleMySessions(w http.ResponseWriter, r *http.Request) {
	// 1. 要求至少是认证用户
	if RequireUserOrHigher(w, r) {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	// 2. 获取过滤条件
	userID := EffectiveUserID(r)      // user→自己ID，admin→0
	tenantID := GetTenantID(r)         // 当前用户的租户

	// 3. 构造查询
	query := `
		SELECT 
			ss.session_key,
			ss.tenant_id,
			COALESCE(ss.title, '') as title,
			ss.created_at,
			ss.updated_at,
			ss.request_count,
			ss.total_cost_usd
		FROM session_summaries ss
		WHERE ss.tenant_id = $1
		  AND ss.deleted_at IS NULL`

	args := []interface{}{tenantID}

	// 4. 用户级过滤
	if userID != 0 {
		// 普通用户只看自己的会话
		query += ` AND EXISTS (
			SELECT 1 FROM session_dimensions sd 
			WHERE sd.session_key = ss.session_key 
			AND sd.owner_user = $2
		)`
		args = append(args, GetAuthContext(r).Username)
	}

	// 5. 排序和限制
	query += `
		ORDER BY ss.updated_at DESC
		LIMIT 50`

	// 6. 执行查询
	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		writeInternalTextErr(w, "failed to query my sessions", err)
		return
	}
	defer rows.Close()

	// 7. 扫描结果
	type MySession struct {
		SessionKey   string    `json:"session_key"`
		TenantID     string    `json:"tenant_id"`
		Title        string    `json:"title"`
		CreatedAt    time.Time `json:"created_at"`
		UpdatedAt    time.Time `json:"updated_at"`
		RequestCount int       `json:"request_count"`
		TotalCostUSD float64   `json:"total_cost_usd"`
	}

	sessions := make([]MySession, 0)
	for rows.Next() {
		var s MySession
		if err := rows.Scan(
			&s.SessionKey,
			&s.TenantID,
			&s.Title,
			&s.CreatedAt,
			&s.UpdatedAt,
			&s.RequestCount,
			&s.TotalCostUSD,
		); err != nil {
			writeInternalTextErr(w, "failed to scan session", err)
			return
		}
		sessions = append(sessions, s)
	}

	if err := rows.Err(); err != nil {
		writeInternalTextErr(w, "rows iteration error", err)
		return
	}

	// 8. 返回结果
	response := map[string]interface{}{
		"sessions": sessions,
		"total":    len(sessions),
		"user_id":  GetAuthUserID(r),
		"role":     GetAuthContext(r).Role,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// handleMyUsage 返回当前用户自己的用量统计
// GET /api/usage/my
//
// 权限模型：同 handleMySessions
//
// 实施日期：2026-11-26（数据权限审计轮）
func (h *Handler) handleMyUsage(w http.ResponseWriter, r *http.Request) {
	// 1. 要求至少是认证用户
	if RequireUserOrHigher(w, r) {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	// 2. 获取过滤条件
	userID := EffectiveUserID(r)
	tenantID := GetTenantID(r)
	auth := GetAuthContext(r)

	// 3. 构造查询
	query := `
		SELECT 
			COUNT(*) as request_count,
			COALESCE(SUM(cost_usd), 0) as total_cost,
			COALESCE(SUM(prompt_tokens), 0) as total_prompt_tokens,
			COALESCE(SUM(completion_tokens), 0) as total_completion_tokens
		FROM request_logs_with_current_month
		WHERE tenant_id = $1
		  AND ts >= NOW() - INTERVAL '30 days'`

	args := []interface{}{tenantID}

	// 4. 用户级过滤
	if userID != 0 {
		// 普通用户只看自己的用量
		// 注意：request_logs 可能没有直接的 user_id，需要通过 session 关联
		query += ` AND EXISTS (
			SELECT 1 FROM sessions s
			JOIN session_dimensions sd ON s.session_key = sd.session_key
			WHERE s.session_key = request_logs_with_current_month.session_key
			AND sd.owner_user = $2
		)`
		args = append(args, auth.Username)
	}

	// 5. 执行查询
	var usage struct {
		RequestCount         int     `json:"request_count"`
		TotalCost            float64 `json:"total_cost_usd"`
		TotalPromptTokens    int64   `json:"total_prompt_tokens"`
		TotalCompletionTokens int64  `json:"total_completion_tokens"`
	}

	err := h.db.QueryRow(ctx, query, args...).Scan(
		&usage.RequestCount,
		&usage.TotalCost,
		&usage.TotalPromptTokens,
		&usage.TotalCompletionTokens,
	)

	if err != nil {
		writeInternalTextErr(w, "failed to query my usage", err)
		return
	}

	// 6. 返回结果
	response := map[string]interface{}{
		"usage":   usage,
		"period":  "last_30_days",
		"user_id": GetAuthUserID(r),
		"role":    auth.Role,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}
