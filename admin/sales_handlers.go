package admin

// sales_handlers.go —— 报价系统 MVP 的 admin API 面（2026-11-04 实施规划
// W1-T1/T2，§5 API 面）。照 registerMaasMyRoutes / h.admin 先例接线；
// 新路由登记面：本文件注册后重生成 scripts/audit/asset/admin-routes.txt；
// POST 端点补 post-surface-ledger.tsv。
//
// 角色闸（规划 §4：自营内部运营表 ⇒ 角色闸在应用层，无 RLS）：
//   - sellable 清单 / 供给登记读：h.admin（tenant_admin + admin_key + super）；
//   - 供给登记写：h.superAdmin（ops 面，照 /api/admin/maas/* 目录面先例）；
//   - 需求池 / 报价工作台动作：h.admin + owner 语义（JWT 用户 = 本人动作，
//     super 可显式 ?actor= 越权，A-11 先例 myOwnerScope）。
//
// 公开面（/api/public/quote/{token}）单独一个文件（无登录、限速面，
// 安全审查：token 128bit、hash 库存、TTL、可撤销、访问审计）。

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/sales"
)

func (h *Handler) salesSvc() *sales.Service {
	if h.db == nil {
		return nil
	}
	return sales.NewService(h.db)
}

// registerSalesRoutes wires the quotation-MVP admin surface (W1).
func (h *Handler) registerSalesRoutes(mux *http.ServeMux) {
	// 供给池（W1-T1）
	mux.HandleFunc("/api/admin/sales/sellable", h.admin(h.handleSalesSellable))
	mux.HandleFunc("/api/admin/sales/supply", h.salesSupplyHandler())
	mux.HandleFunc("/api/admin/sales/demand", h.salesDemandHandler())
	mux.HandleFunc("/api/admin/sales/demand/", h.admin(h.handleSalesDemandAction))
	// 报价工作台（W1-T2）
	mux.HandleFunc("/api/admin/sales/quotes", h.admin(h.handleSalesQuotes))
	mux.HandleFunc("/api/admin/sales/quotes/", h.admin(h.handleSalesQuoteAction))
	// 审批（super / 运营负责人，MVP 单级审批）
	mux.HandleFunc("/api/admin/sales/approvals", h.superAdmin(h.handleSalesApprovals))
}

// ── 供给池 ──────────────────────────────────────────────────────────────

func (h *Handler) handleSalesSellable(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	svc := h.salesSvc()
	if svc == nil || !svc.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	list, err := svc.ListSellable(r.Context())
	if err != nil {
		writeInternalErr(w, "internal error (see server logs)", err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// salesSupplyHandler: GET = list annotations (admin), POST = upsert (super).
func (h *Handler) salesSupplyHandler() http.HandlerFunc {
	adminFn := h.admin(h.handleSalesSupplyList)
	superFn := h.superAdmin(h.handleSalesSupplyUpsert)
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			adminFn(w, r)
		case http.MethodPost:
			superFn(w, r)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	}
}

func (h *Handler) handleSalesSupplyList(w http.ResponseWriter, r *http.Request) {
	svc := h.salesSvc()
	if svc == nil || !svc.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	items, err := svc.ListSupplyAnnotations(r.Context())
	if err != nil {
		writeInternalErr(w, "internal error (see server logs)", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) handleSalesSupplyUpsert(w http.ResponseWriter, r *http.Request) {
	svc := h.salesSvc()
	if svc == nil || !svc.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	var body struct {
		CredentialID    int64  `json:"credential_id"`
		ProviderModelID int64  `json:"provider_model_id"`
		CapacityNotes   string `json:"capacity_notes"`
		SLATier         string `json:"sla_tier"`
		ValidFrom       string `json:"valid_from"`
		ValidUntil      string `json:"valid_until"`
		VendorContact   string `json:"vendor_contact"`
		Status          string `json:"status"`
		Maintainer      string `json:"maintainer"`
		Notes           string `json:"notes"`
	}
	if err := readJSONRequired(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	actor := salesAuthUsername(r)
	ann, err := svc.UpsertSupplyAnnotation(r.Context(), sales.SupplyAnnotation{
		CredentialID:    body.CredentialID,
		ProviderModelID: body.ProviderModelID,
		CapacityNotes:   body.CapacityNotes,
		SLATier:         body.SLATier,
		ValidFrom:       body.ValidFrom,
		ValidUntil:      body.ValidUntil,
		VendorContact:   body.VendorContact,
		Status:          body.Status,
		Maintainer:      body.Maintainer,
		Notes:           body.Notes,
	}, actor)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ann)
}

// ── 需求池 ──────────────────────────────────────────────────────────────

func (h *Handler) handleSalesDemandBody(w http.ResponseWriter, r *http.Request) {
	svc := h.salesSvc()
	if svc == nil || !svc.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	switch r.Method {
	case http.MethodGet:
		cards, err := svc.ListDemandCards(r.Context(), strings.TrimSpace(r.URL.Query().Get("status")))
		if err != nil {
			writeInternalErr(w, "internal error (see server logs)", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": cards})
	case http.MethodPost:
		var body struct {
			CustomerName string     `json:"customer_name"`
			Scenario     string     `json:"scenario"`
			MonthlyUsage string     `json:"monthly_usage"`
			Budget       string     `json:"budget"`
			WindowHint   string     `json:"window_hint"`
			ExpiresAt    *time.Time `json:"expires_at"`
			Notes        string     `json:"notes"`
		}
		if err := readJSONRequired(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		card, err := svc.CreateDemandCard(r.Context(), sales.DemandCard{
			CustomerName: body.CustomerName,
			Scenario:     body.Scenario,
			MonthlyUsage: body.MonthlyUsage,
			Budget:       body.Budget,
			WindowHint:   body.WindowHint,
			ExpiresAt:    body.ExpiresAt,
			Notes:        body.Notes,
		}, salesAuthUsername(r))
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, card)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// salesDemandHandler dispatches GET/POST on the collection.
func (h *Handler) salesDemandHandler() http.HandlerFunc {
	return h.admin(h.handleSalesDemandBody)
}

// handleSalesDemandAction: POST /api/admin/sales/demand/{id}/{claim|close}.
func (h *Handler) handleSalesDemandAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	svc := h.salesSvc()
	if svc == nil || !svc.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/admin/sales/demand/")
	parts := strings.Split(rest, "/")
	if len(parts) != 2 {
		writeError(w, http.StatusBadRequest, "invalid demand action path")
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid demand card id")
		return
	}
	actor := salesAuthUsername(r)
	super := salesAuthRole(r) == "super_admin"
	switch parts[1] {
	case "claim":
		card, err := svc.ClaimDemandCard(r.Context(), id, actor)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, card)
	case "close":
		var body struct {
			Status string `json:"status"`
			Reason string `json:"reason"`
		}
		if err := readJSONRequired(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		card, err := svc.CloseDemandCard(r.Context(), id, body.Status, body.Reason, actor, super)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, card)
	default:
		writeError(w, http.StatusBadRequest, "unknown demand action (claim|close)")
	}
}

// ── 报价工作台 ──────────────────────────────────────────────────────────

func (h *Handler) handleSalesQuotes(w http.ResponseWriter, r *http.Request) {
	svc := h.salesSvc()
	if svc == nil || !svc.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	switch r.Method {
	case http.MethodGet:
		quotes, err := svc.ListQuotes(r.Context(), strings.TrimSpace(r.URL.Query().Get("status")))
		if err != nil {
			writeInternalErr(w, "internal error (see server logs)", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": quotes})
	case http.MethodPost:
		var in sales.QuoteInput
		if err := readJSONRequired(r, &in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		q, err := svc.CreateQuote(r.Context(), in, salesAuthUsername(r))
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, q)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleSalesQuoteAction dispatches /api/admin/sales/quotes/{id}[/{action}].
func (h *Handler) handleSalesQuoteAction(w http.ResponseWriter, r *http.Request) {
	svc := h.salesSvc()
	if svc == nil || !svc.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/admin/sales/quotes/")
	parts := strings.Split(rest, "/")
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid quote id")
		return
	}
	actor := salesAuthUsername(r)
	action := ""
	if len(parts) == 2 {
		action = parts[1]
	}

	if r.Method == http.MethodGet && action == "" {
		q, err := svc.GetQuote(r.Context(), id)
		if err != nil {
			writeLookupErr(w, "quote not found", err)
			return
		}
		writeJSON(w, http.StatusOK, q)
		return
	}
	// A5（2026-11-05 审计）：只读 action（versions/approvals/shares 的 GET）
	// 接受 GET——此前统一 POST-only 把三个只读分支变成不可达死代码。
	readOnlyAction := action == "versions" || action == "approvals" || action == "shares"
	if r.Method == http.MethodGet && !readOnlyAction {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	switch action {
	case "":
		var in sales.QuoteInput
		if err := readJSONRequired(r, &in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		q, err := svc.ReviseQuote(r.Context(), id, in, actor)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, q)
	case "submit":
		q, err := svc.SubmitQuote(r.Context(), id, actor)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, q)
	case "decide":
		// MVP 单级审批：super / 运营负责人（superAdmin 面在 handler 内判角色）。
		if salesAuthRole(r) != "super_admin" {
			writeError(w, http.StatusForbidden, "super_admin role required for approval decisions")
			return
		}
		var body struct {
			Decision string `json:"decision"`
			Comment  string `json:"comment"`
		}
		if err := readJSONRequired(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		q, err := svc.DecideApproval(r.Context(), id, body.Decision, body.Comment, actor)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, q)
	case "send":
		q, err := svc.SendQuote(r.Context(), id, actor)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, q)
	case "close":
		var body struct {
			Status string `json:"status"`
			Reason string `json:"reason"`
		}
		if err := readJSONRequired(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		q, err := svc.CloseQuote(r.Context(), id, body.Status, body.Reason, actor)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, q)
	case "versions":
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		versions, err := svc.ListQuoteVersions(r.Context(), id)
		if err != nil {
			writeInternalErr(w, "internal error (see server logs)", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": versions})
	case "approvals":
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		approvals, err := svc.ListApprovals(r.Context(), id)
		if err != nil {
			writeInternalErr(w, "internal error (see server logs)", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": approvals})
	case "shares":
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if r.Method == http.MethodGet {
			links, err := svc.ListShares(r.Context(), id)
			if err != nil {
				writeInternalErr(w, "internal error (see server logs)", err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"items": links})
			return
		}
		var body struct {
			ContactName  string `json:"contact_name"`
			ContactEmail string `json:"contact_email"`
			TTLHours     int    `json:"ttl_hours"`
		}
		if err := readJSONRequired(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		link, err := svc.CreateShare(r.Context(), id, body.ContactName, body.ContactEmail,
			time.Duration(body.TTLHours)*time.Hour, actor)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, link)
	default:
		writeError(w, http.StatusBadRequest, "unknown quote action")
	}
}

// ── 审批查询面（super，跨单） ────────────────────────────────────────────

func (h *Handler) handleSalesApprovals(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	svc := h.salesSvc()
	if svc == nil || !svc.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	idStr := r.URL.Query().Get("quote_id")
	if idStr == "" {
		writeError(w, http.StatusBadRequest, "quote_id is required")
		return
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid quote_id")
		return
	}
	approvals, err := svc.ListApprovals(r.Context(), id)
	if err != nil {
		writeInternalErr(w, "internal error (see server logs)", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": approvals})
}

// ── auth 小助手 ─────────────────────────────────────────────────────────

func salesAuthUsername(r *http.Request) string {
	if auth := GetAuthContext(r); auth != nil {
		return auth.Username
	}
	return ""
}

func salesAuthRole(r *http.Request) string {
	if auth := GetAuthContext(r); auth != nil {
		return auth.Role
	}
	return ""
}
