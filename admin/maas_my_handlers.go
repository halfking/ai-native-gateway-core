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
// 新路由登记面：本文件注册后重生成 scripts/audit/asset/admin-routes.txt；
// POST 端点（campaigns redeem）补 post-surface-ledger.tsv。

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/kaixuan/llm-gateway-go/maas"
)

// myOwnerScope resolves the owner filter for the /my/* surface: the
// username to narrow to, or "" for an unfiltered (full-tenant) view.
func myOwnerScope(r *http.Request) string {
	auth := GetAuthContext(r)
	if auth == nil {
		// 未认证态：不应到达 —— /my/* 全部挂在 h.admin()（AdminMiddleware）
		// 之后，无身份请求在中间件就被 401。防御性兜底保持与 legacy 同口径
		// （不收窄），但语义是「没有身份」，不是「服务账号」。
		return ""
	}
	// legacy admin_key（认证了但非 JWT / 无 username）：服务账号 ⇒
	// 全租户视图（不收窄）。
	if !auth.IsJWT || strings.TrimSpace(auth.Username) == "" {
		return ""
	}
	// super_admin 可显式越权查他人（管理员全租户判定面）。
	if auth.Role == "super_admin" {
		if v := strings.TrimSpace(r.URL.Query().Get("owner_user")); v != "" {
			return v
		}
		return auth.Username // 默认仍看自己，但未强制
	}
	// 其余 JWT 用户：强制本人（成员仅本人，A-11）。
	return auth.Username
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
	ownerUser := myOwnerScope(r)
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
	ownerUser := myOwnerScope(r)
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
		if err.Error() == "no rows in result set" {
			writeError(w, http.StatusNotFound, "bill not found")
			return
		}
		writeInternalErr(w, "internal error (see server logs)", err)
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
