package admin

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

var errForbidden = errors.New("forbidden")

// channelGate 是 W4.3 渠道商权限门：验证当前登录用户是否拥有指定渠道，
// 以及该渠道是否拥有指定客户。fail-closed：权限链任一环节断裂 → 403。
type channelGate struct {
	db *pgxpool.Pool
}

// verifyChannelOwnership 检查 user_id 是否归属于 channel_id（通过 channel_users 表）。
func (g *channelGate) verifyChannelOwnership(ctx context.Context, userID int, channelID int64) error {
	var exists bool
	err := g.db.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM channel_users WHERE user_id = $1 AND channel_id = $2
		)
	`, userID, channelID).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		return errForbidden
	}
	return nil
}

// verifyCustomerOwnership 检查 customer_id 是否归属于 channel_id（通过 customers 表）。
func (g *channelGate) verifyCustomerOwnership(ctx context.Context, channelID int64, customerID int64) error {
	var exists bool
	err := g.db.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM customers WHERE id = $1 AND channel_id = $2
		)
	`, customerID, channelID).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		return errForbidden
	}
	return nil
}

// listChannelCustomers (GET /api/channels/{id}/customers) 返回渠道名下的客户列表。
// 权限：channel 用户只能看自己归属的渠道，super_admin 可查任意渠道。
func (h *Handler) listChannelCustomers(w http.ResponseWriter, r *http.Request) {
	channelID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid channel_id", http.StatusBadRequest)
		return
	}

	auth := GetAuthContext(r)
	if auth == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// 权限检查：channel 用户必须归属该渠道，super_admin 放行
	if auth.Role == "channel" {
		gate := &channelGate{db: h.db}
		if err := gate.verifyChannelOwnership(r.Context(), auth.UserID, channelID); err != nil {
			if errors.Is(err, errForbidden) {
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}
			http.Error(w, "Internal error", http.StatusInternalServerError)
			return
		}
	} else if auth.Role != "super_admin" {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	// 查询客户列表
	rows, err := h.db.Query(r.Context(), `
		SELECT id, name, code, enabled, created_at
		FROM customers
		WHERE channel_id = $1
		ORDER BY created_at DESC
	`, channelID)
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type customer struct {
		ID        int64     `json:"id"`
		Name      string    `json:"name"`
		Code      string    `json:"code"`
		Enabled   bool      `json:"enabled"`
		CreatedAt time.Time `json:"created_at"`
	}
	var customers []customer
	for rows.Next() {
		var c customer
		if err := rows.Scan(&c.ID, &c.Name, &c.Code, &c.Enabled, &c.CreatedAt); err != nil {
			http.Error(w, "Scan error", http.StatusInternalServerError)
			return
		}
		customers = append(customers, c)
	}

	writeJSON(w, http.StatusOK, customers)
}

// getChannelCustomerStats (GET /api/channels/{id}/customers/{customer_id}/stats)
// 返回客户的会话统计（会话总数、活跃用户、月度成本）。
// 权限：channel 用户需验证 channel→customer 归属链，super_admin 放行。
func (h *Handler) getChannelCustomerStats(w http.ResponseWriter, r *http.Request) {
	channelID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid channel_id", http.StatusBadRequest)
		return
	}
	customerID, err := strconv.ParseInt(r.PathValue("customer_id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid customer_id", http.StatusBadRequest)
		return
	}

	auth := GetAuthContext(r)
	if auth == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	gate := &channelGate{db: h.db}
	// 权限检查：channel 用户需验证完整归属链
	if auth.Role == "channel" {
		if err := gate.verifyChannelOwnership(r.Context(), auth.UserID, channelID); err != nil {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		if err := gate.verifyCustomerOwnership(r.Context(), channelID, customerID); err != nil {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
	} else if auth.Role != "super_admin" {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	// 统计查询：从 request_logs 聚合（407迁移已有 customer_id）
	// ★ 2026-11-26 审计轮代上游收口：★ 读点从【已退役的 v1 底表】改指到【890 之后仍存在的
	// canonical 视图】，★ 时间列从 created_at 改成视图里真实存在的 ts ——
	// ① v1 底表 public.request_logs 被迁移 890 **物理删除**（★ 真库实测：154 生产库没有这张表、
	//    只有视图 request_logs_with_current_month ⇒ 原写法运行时 **42P01** ★★）；
	// ② canonical 视图 request_logs_with_current_month 的数据源是会话族两臂
	//    （FROM session_turns ∪ session_turns_hot）⇒ 它才是 890 之后的存活源；
	// ③ 该视图的列是 ts（★ 没有 created_at ★）⇒ 时间过滤改用 ts（★ 实测该视图列集：
	//    ts / customer_id / cost_usd / identity_hash 齐备 ★★）。
	type stats struct {
		TotalSessions int     `json:"total_sessions"`
		ActiveUsers   int     `json:"active_users"`
		MonthlyCost   float64 `json:"monthly_cost_usd"`
	}
	var s stats
	err = h.db.QueryRow(r.Context(), `
		SELECT
			COUNT(DISTINCT COALESCE(identity_hash, '')),
			COUNT(DISTINCT COALESCE(end_user_id, '')),
			COALESCE(SUM(cost_usd), 0)
		FROM request_logs_with_current_month
		WHERE customer_id = $1
		  AND ts >= NOW() - INTERVAL '30 days'
	`, customerID).Scan(&s.TotalSessions, &s.ActiveUsers, &s.MonthlyCost)
	if err != nil {
		http.Error(w, "Query error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, s)
}

// getChannelStats (GET /api/channels/{id}/stats) 返回渠道整体统计（所有客户汇总）。
func (h *Handler) getChannelStats(w http.ResponseWriter, r *http.Request) {
	channelID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid channel_id", http.StatusBadRequest)
		return
	}

	auth := GetAuthContext(r)
	if auth == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	if auth.Role == "channel" {
		gate := &channelGate{db: h.db}
		if err := gate.verifyChannelOwnership(r.Context(), auth.UserID, channelID); err != nil {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
	} else if auth.Role != "super_admin" {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	// 聚合渠道下所有客户的统计
	type stats struct {
		TotalCustomers int     `json:"total_customers"`
		TotalSessions  int     `json:"total_sessions"`
		MonthlyCost    float64 `json:"monthly_cost_usd"`
	}
	var s stats
	err = h.db.QueryRow(r.Context(), `
		WITH channel_customers AS (
			SELECT id FROM customers WHERE channel_id = $1
		)
		SELECT
			(SELECT COUNT(*) FROM channel_customers),
			COUNT(DISTINCT COALESCE(rl.identity_hash, '')),
			COALESCE(SUM(rl.cost_usd), 0)
		FROM request_logs_with_current_month rl
		WHERE rl.customer_id IN (SELECT id FROM channel_customers)
		  AND rl.ts >= NOW() - INTERVAL '30 days'
	`, channelID).Scan(&s.TotalCustomers, &s.TotalSessions, &s.MonthlyCost)
	if err != nil {
		http.Error(w, "Query error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, s)
}

// listChannelCustomerSessions (GET /api/channels/{id}/customers/{customer_id}/sessions)
// 返回客户的会话列表（从 request_logs/session_dim 联查，简化版）。
func (h *Handler) listChannelCustomerSessions(w http.ResponseWriter, r *http.Request) {
	channelID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid channel_id", http.StatusBadRequest)
		return
	}
	customerID, err := strconv.ParseInt(r.PathValue("customer_id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid customer_id", http.StatusBadRequest)
		return
	}

	auth := GetAuthContext(r)
	if auth == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	gate := &channelGate{db: h.db}
	if auth.Role == "channel" {
		if err := gate.verifyChannelOwnership(r.Context(), auth.UserID, channelID); err != nil {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		if err := gate.verifyCustomerOwnership(r.Context(), channelID, customerID); err != nil {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
	} else if auth.Role != "super_admin" {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	// 查询会话列表（简化版：从 request_logs_with_current_month 取最近会话，分页默认50）
	// ★ 2026-11-26 审计轮代上游收口：★ 同上（v1 底表 request_logs 已被 890 物理删除 ⇒ 改指
	// canonical 视图 request_logs_with_current_month；★ 时间列 created_at → ts ★）。
	limit := 50
	rows, err := h.db.Query(r.Context(), `
		SELECT DISTINCT ON (identity_hash)
			identity_hash,
			MAX(ts) as last_activity,
			COUNT(*) as request_count,
			SUM(cost_usd) as total_cost
		FROM request_logs_with_current_month
		WHERE customer_id = $1
		  AND ts >= NOW() - INTERVAL '30 days'
		GROUP BY identity_hash
		ORDER BY identity_hash, last_activity DESC
		LIMIT $2
	`, customerID, limit)
	if err != nil {
		http.Error(w, "Query error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type session struct {
		IdentityHash string    `json:"identity_hash"`
		LastActivity time.Time `json:"last_activity"`
		RequestCount int       `json:"request_count"`
		TotalCost    float64   `json:"total_cost_usd"`
	}
	var sessions []session
	for rows.Next() {
		var s session
		if err := rows.Scan(&s.IdentityHash, &s.LastActivity, &s.RequestCount, &s.TotalCost); err != nil {
			http.Error(w, "Scan error", http.StatusInternalServerError)
			return
		}
		sessions = append(sessions, s)
	}

	writeJSON(w, http.StatusOK, sessions)
}
