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
	// 2026-11-26 E2E 轮订正：改用 sessions 分区表真 schema（生产实测）——
	//   · session_summaries 没有 deleted_at 列（42703，部署后首条 E2E 即暴露），
	//     软删除语义由 session_status CHECK（active/completed/abandoned）承担；
	//   · owner_user 落在 sessions 表本体（不是 session_dimensions——该表不存在）；
	//   · 会话标题列是 topic（title 在 summaries 表）。
	query := `
		SELECT
			s.session_id,
			s.tenant_id,
			COALESCE(s.topic, '') as title,
			s.created_at,
			s.updated_at,
			s.total_turns,
			s.total_cost_usd
		FROM sessions s
		WHERE s.tenant_id = $1
		  AND s.agent_role = 'main'`

	args := []interface{}{tenantID}

	// 4. 用户级过滤
	if userID != 0 {
		// 普通用户只看自己的会话（owner_user 记录的是用户名）
		query += ` AND s.owner_user = $2`
		args = append(args, GetAuthContext(r).Username)
	}

	// 5. 排序和限制
	query += `
		ORDER BY s.updated_at DESC
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
		// ★ W21 订正（2026-11-27 12h 审计轮）：SELECT 的 s.total_turns 此前扫进
		//   RequestCount / json:"request_count"——名义是请求数、承载的是轮次数，
		//   前端直显语义漂移。改回真实名义 TotalTurns / json:"total_turns"
		//   （前端 MySessionsView prop 同步改）。
		TotalTurns   int       `json:"total_turns"`
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
			&s.TotalTurns,
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
	// 2026-11-26 E2E 轮订正（生产实测绘得真列名）：request_metrics 的
	//   时间列是 occurred_at（不是 ts）、成本列是 cost_amount+cost_currency
	//   （不是 cost_usd）、归属列是 owner_user（表本体，不是关联查询）。
	//   owner_user 空值率高（历史行未回填）⇒ 用户过滤天然只命中自己的行。
	query := `
		SELECT
			COUNT(*) as request_count,
			COALESCE(SUM(cost_amount), 0) as total_cost,
			COALESCE(SUM(prompt_tokens), 0) as total_prompt_tokens,
			COALESCE(SUM(completion_tokens), 0) as total_completion_tokens
		FROM request_metrics
		WHERE tenant_id = $1
		  AND cost_currency = 'USD'
		  AND occurred_at >= NOW() - INTERVAL '30 days'`

	args := []interface{}{tenantID}

	// 4. 用户级过滤
	if userID != 0 {
		// 普通用户只看自己的用量：request_metrics.owner_user 记录用户名
		query += ` AND owner_user = $2`
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
	// ★ W21 订正：cost_currency='USD' 谓词下 total_cost_usd 静默排除非美元行
	//   （欠计非泄露）——响应带 currency_scope 显式标注口径，防"总额=全币种"
	//   误读。
	response := map[string]interface{}{
		"usage":          usage,
		"period":         "last_30_days",
		"user_id":        GetAuthUserID(r),
		"role":           auth.Role,
		"currency_scope": "USD",
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}
