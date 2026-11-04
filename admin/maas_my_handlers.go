package admin

// W3-T1/T3（2026-11-04 运营管理与价格计费体系，总体设计 §2.5 用户级 API 面）：
// /api/maas/my/* —— 用户自助（admin JWT + owner 收窄）。
//
// owner 收窄判路面（A-11 + W3-T0 前置核实）：
//   - JWT 用户（super_admin / tenant_admin 经 JWT）→「我的」= 本人
//     （api_key_owner_user = AuthContext.Username）；super_admin 可显式传
//     ?owner_user= 查他人（管理员全租户面）。
//   - legacy admin_key（无 username）→ 不带 owner 过滤（服务账号 = 全租户）。
//
// ★★ **数据粒度口径（★ 2026-11-04 12h 审计轮订正）**：★ 本面**不是**整面都按
//   owner 收窄 —— 按 owner 收窄的只有两条：**usage** 与 **requests**
//   （它们读的是**按 owner 归属**的消费明细）。
//   而 **bills/{id}** 与 **budget** 读的是**租户级聚合**
//   （`maas_monthly_bills` / `tenant_budgets` 两张表**都没有 owner 列**
//   ⇒ 天然是租户粒度 ⇒ 无「跨用户越权」面，但**也不是「我的」**）。
//   ⇒ ★ 文件头此前只写「用户自助（admin JWT + owner 收窄）」⇒ **与实现不符**
//   ⇒ ★ 读代码的人会误以为四条路由都按 owner 收窄 ⇒ 本次订正写清。
//   ⇒ ★ 保持「路由在 /my/ 下但数据是租户粒度」这一现状（改路由名要连带重生成
//     admin-routes.txt + post-surface-ledger.tsv ⇒ 留待有独占窗口时再动）。
//
// 新路由登记面：本文件注册后重生成 scripts/audit/asset/admin-routes.txt；
// POST 端点（campaigns redeem）补 post-surface-ledger.tsv。

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/kaixuan/llm-gateway-go/maas"
)

// myOwnerScope resolves the owner filter for the /my/* surface.
//
// ★ 返回 (ownerUser, identified)：
//
//   - identified == false ⇒ **请求没有可用的身份**（GetAuthContext 为 nil）
//     ⇒ 调用方必须拒绝（401），**不得**退化成「不带 owner 过滤 = 全租户可见」。
//     ⇒ ★ 这条分支目前**不可达**（注册这批路由的 h.admin(...) = AdminMiddleware
//     成功时必定写入 AuthContext，失败时 401）⇒ 它是**防御性**分支
//     ⇒ 但它原先的默认值是**fail-open**（⇒ 全租户可见）⇒
//     ★ 改成 fail-closed：宁可拒绝，也不放大可见面。
//   - identified == true  ⇒ ownerUser 是本次查询应当收窄到的 owner；
//     ownerUser == "" 表示**不按 owner 收窄**，两种情形都是**有意**的：
//     · legacy admin_key 服务账号（JWT 但无 username）⇒ 全租户（服务账号面）；
//     · super_admin 且显式 ?owner_user= 指定了他人 ⇒ 按该用户。
//
// ★ owner 收窄判据的口径见文件头；★ 本函数**不再**返回「是否收窄到本人」那个
//
//	布尔值 —— 它在**所有**调用点都被丢弃（`ownerUser, _ :=`）⇒ 死返回值
//	⇒ 现在的第二个返回值有**真实语义**（有没有身份）⇒ 不再是死值。
func myOwnerScope(r *http.Request) (ownerUser string, identified bool) {
	auth := GetAuthContext(r)
	if auth == nil {
		return "", false
	}
	// legacy admin_key：无 username 的服务账号 ⇒ 全租户（不收窄），**有意**。
	if !auth.IsJWT || strings.TrimSpace(auth.Username) == "" {
		return "", true
	}
	// super_admin 可显式越权查他人（管理员全租户判定面）。
	if auth.Role == "super_admin" {
		if v := strings.TrimSpace(r.URL.Query().Get("owner_user")); v != "" {
			return v, true
		}
		return auth.Username, true // 默认仍看自己，但未强制
	}
	// 其余 JWT 用户：强制本人（成员仅本人，A-11）。
	return auth.Username, true
}

func (h *Handler) handleMaasMyUsage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	svc := h.maasSvc()
	if svc == nil || !svc.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	days := 7
	if v := r.URL.Query().Get("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			days = n
		}
	}
	ownerUser, identified := myOwnerScope(r)
	if !identified {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	tenantID := GetTenantID(r)
	detail, err := svc.QueryConsumptionDetail(r.Context(), tenantID, ownerUser, days)
	if err != nil {
		writeInternalErr(w, "internal error (see server logs)", err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (h *Handler) handleMaasMyRequests(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	svc := h.maasSvc()
	if svc == nil || !svc.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	q := r.URL.Query()
	days := 7
	if v := q.Get("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			days = n
		}
	}
	page, pageSize := 1, 20
	if v := q.Get("page"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			page = n
		}
	}
	if v := q.Get("page_size"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			pageSize = n
		}
	}
	ownerUser, identified := myOwnerScope(r)
	if !identified {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	pageData, err := svc.QueryMyRequests(r.Context(), GetTenantID(r), ownerUser, days, strings.TrimSpace(q.Get("model")), page, pageSize)
	if err != nil {
		writeInternalErr(w, "internal error (see server logs)", err)
		return
	}
	writeJSON(w, http.StatusOK, pageData)
}

func (h *Handler) handleMaasMyBillByID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	svc := h.maasSvc()
	if svc == nil || !svc.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	idStr := strings.TrimPrefix(r.URL.Path, "/api/maas/my/bills/")
	billID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || billID <= 0 {
		writeError(w, http.StatusBadRequest, "invalid bill id")
		return
	}
	bill, err := svc.GetMonthlyBill(r.Context(), GetTenantID(r), billID)
	if err != nil {
		// ★ 用仓内的统一查找错误出口（admin/aggregate_read_guard.go）：
		//   「查不到 ⇒ 404」「缺关系 ⇒ 503」「其它 ⇒ 500」。
		//   ★ 原先这里是 `err.Error() == "no rows in result set"` 逐字比
		//     ⇒ 而 maas 层返回的是 "bill not found" ⇒ **永不命中**
		//   ⇒ 「账单不存在」被当成内部错误 ⇒ 500 而不是 404。
		//   ⇒ 现在 maas 层用 %w 保住 errors.Is 链（maas/bills.go）⇒ 这里能判出来。
		writeLookupErr(w, "bill not found", err)
		return
	}
	writeJSON(w, http.StatusOK, bill)
}

func (h *Handler) handleMaasMyBudget(w http.ResponseWriter, r *http.Request) {
	svc := h.maasSvc()
	if svc == nil || !svc.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	tenantID := GetTenantID(r)
	switch r.Method {
	case http.MethodGet:
		budget, err := svc.GetTenantBudget(r.Context(), tenantID)
		if err != nil {
			writeInternalErr(w, "internal error (see server logs)", err)
			return
		}
		writeJSON(w, http.StatusOK, budget)
	case http.MethodPut:
		var body struct {
			MonthlyBudgetCents    *int64         `json:"monthly_budget_cents"`
			BalanceAlertThreshold *float64       `json:"balance_alert_threshold"`
			QuotaAlertThreshold   *float64       `json:"quota_alert_threshold"`
			NotifyChannels        map[string]any `json:"notify_channels"`
		}
		if err := readJSONRequired(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		cur, err := svc.GetTenantBudget(r.Context(), tenantID)
		if err != nil {
			writeInternalErr(w, "internal error (see server logs)", err)
			return
		}
		budgetCents := cur.MonthlyBudgetCents
		if body.MonthlyBudgetCents != nil {
			budgetCents = *body.MonthlyBudgetCents
		}
		balanceAlert := cur.BalanceAlertThreshold
		if body.BalanceAlertThreshold != nil {
			balanceAlert = *body.BalanceAlertThreshold
		}
		quotaAlert := cur.QuotaAlertThreshold
		if body.QuotaAlertThreshold != nil {
			quotaAlert = *body.QuotaAlertThreshold
		}
		channels := cur.NotifyChannels
		if body.NotifyChannels != nil {
			channels = body.NotifyChannels
		}
		budget, err := svc.UpsertTenantBudget(r.Context(), tenantID, budgetCents, balanceAlert, quotaAlert, channels)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, budget)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) handleMaasCampaigns(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	svc := h.maasSvc()
	if svc == nil || !svc.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	items, err := svc.ListActiveCampaigns(r.Context())
	if err != nil {
		writeInternalErr(w, "internal error (see server logs)", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// handleMaasCampaignRedeem redeems a campaign for the caller's tenant.
// POST /api/maas/campaigns/{code}/redeem — 活动额度双写（redemption +
// campaign_id ledger）在 maas.RedeemCampaign 的事务里。
func (h *Handler) handleMaasCampaignRedeem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	svc := h.maasSvc()
	if svc == nil || !svc.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/maas/campaigns/")
	path = strings.TrimSuffix(path, "/redeem")
	code := strings.TrimSpace(path)
	if code == "" || strings.Contains(code, "/") {
		writeError(w, http.StatusBadRequest, "invalid campaign code")
		return
	}
	ownerUser := ""
	if auth := GetAuthContext(r); auth != nil {
		ownerUser = auth.Username
	}
	red, err := svc.RedeemCampaign(r.Context(), code, GetTenantID(r), ownerUser)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, red)
}

// registerMaasMyRoutes wires the user self-service surface (W3).
func (h *Handler) registerMaasMyRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/maas/my/usage", h.admin(h.handleMaasMyUsage))
	mux.HandleFunc("/api/maas/my/requests", h.admin(h.handleMaasMyRequests))
	mux.HandleFunc("/api/maas/my/bills/", h.admin(h.handleMaasMyBillByID))
	mux.HandleFunc("/api/maas/my/budget", h.admin(h.handleMaasMyBudget))
	mux.HandleFunc("/api/maas/campaigns", h.admin(h.handleMaasCampaigns))
	mux.HandleFunc("/api/maas/campaigns/", h.admin(h.handleMaasCampaignRedeem))
}

var _ = maas.TenantBudget{} // keep import when handlers evolve
