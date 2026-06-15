package admin

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/kaixuan/llm-gateway-go/maas"
)

func (h *Handler) registerMaasRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/admin/maas/settings", h.superAdmin(h.handleMaasSettings))
	mux.HandleFunc("/api/admin/maas/plans", h.superAdmin(h.handleMaasPlans))
	mux.HandleFunc("/api/admin/maas/topup-packages", h.superAdmin(h.handleMaasTopupPackages))
	mux.HandleFunc("/api/admin/maas/tenants/", h.superAdmin(h.handleMaasTenantAdmin))
	mux.HandleFunc("/api/admin/maas/orders", h.superAdmin(h.handleAdminMaasOrders))
	mux.HandleFunc("/api/admin/maas/orders/", h.superAdmin(h.handleAdminMaasOrderByID))

	mux.HandleFunc("/api/maas/settings", h.admin(h.handleMaasPublicSettings))
	mux.HandleFunc("/api/maas/models", h.admin(h.handleMaasPublicModels))
	mux.HandleFunc("/api/maas/plans", h.admin(h.handleMaasPublicPlans))
	mux.HandleFunc("/api/maas/topup-packages", h.admin(h.handleMaasPublicTopup))
	mux.HandleFunc("/api/maas/wallet", h.admin(h.handleMaasWallet))
	mux.HandleFunc("/api/maas/account", h.admin(h.handleMaasAccount))
	mux.HandleFunc("/api/maas/ledger", h.admin(h.handleMaasLedger))
	mux.HandleFunc("/api/maas/orders", h.admin(h.handleMaasOrders))
	mux.HandleFunc("/api/maas/orders/", h.admin(h.handleMaasOrderByID))
	mux.HandleFunc("/api/maas/usage/summary", h.admin(h.handleMaasUsageSummary))
}

func (h *Handler) maasSvc() *maas.Service {
	if h.db == nil {
		return nil
	}
	return maas.NewService(h.db)
}

func (h *Handler) handleMaasSettings(w http.ResponseWriter, r *http.Request) {
	svc := h.maasSvc()
	if svc == nil || !svc.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	switch r.Method {
	case http.MethodGet:
		st, err := svc.GetSettings(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, st)
	case http.MethodPut:
		var body maas.Settings
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json")
			return
		}
		if body.BaseCreditsPer1M <= 0 {
			writeError(w, http.StatusBadRequest, "base_credits_per_1m must be positive")
			return
		}
		if err := svc.UpdateSettings(r.Context(), body); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) handleMaasPlans(w http.ResponseWriter, r *http.Request) {
	svc := h.maasSvc()
	if svc == nil || !svc.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	items, err := svc.ListPlans(r.Context(), false)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) handleMaasTopupPackages(w http.ResponseWriter, r *http.Request) {
	svc := h.maasSvc()
	if svc == nil || !svc.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	items, err := svc.ListTopupPackages(r.Context(), false)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) handleMaasTenantAdmin(w http.ResponseWriter, r *http.Request) {
	svc := h.maasSvc()
	if svc == nil || !svc.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/admin/maas/tenants/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) < 2 {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	tenantCode := parts[0]
	action := parts[1]
	switch action {
	case "wallet":
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		wallet, err := svc.GetWallet(r.Context(), tenantCode)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, wallet)
	case "ledger":
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		limit := 50
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				limit = n
			}
		}
		items, err := svc.ListLedger(r.Context(), tenantCode, limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items})
	case "adjust":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var body struct {
			Amount int64  `json:"amount"`
			Note   string `json:"note"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json")
			return
		}
		if err := svc.AdjustCredits(r.Context(), tenantCode, body.Amount, body.Note); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	case "grant":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var body struct {
			GrantedCredits int64  `json:"granted_credits"`
			Note           string `json:"note"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json")
			return
		}
		if err := svc.GrantCredits(r.Context(), tenantCode, body.GrantedCredits, body.Note); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	case "orders":
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		limit := 20
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				limit = n
			}
		}
		items, err := svc.ListOrders(r.Context(), tenantCode, limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items})
	default:
		writeError(w, http.StatusNotFound, "not found")
	}
}

func (h *Handler) handleMaasPublicSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	svc := h.maasSvc()
	if svc == nil || !svc.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	st, err := svc.GetSettings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Tenants see conversion knobs only, not internal cost data.
	writeJSON(w, http.StatusOK, map[string]any{
		"cents_per_credit":     st.CentsPerCredit,
		"base_credits_per_1m":  st.BaseCreditsPer1M,
		"currency_display":     st.CurrencyDisplay,
	})
}

func (h *Handler) handleMaasPublicModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	svc := h.maasSvc()
	if svc == nil || !svc.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	items, err := svc.ListPublicModels(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) handleMaasPublicPlans(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	svc := h.maasSvc()
	if svc == nil || !svc.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	items, err := svc.ListPlans(r.Context(), true)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) handleMaasPublicTopup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	svc := h.maasSvc()
	if svc == nil || !svc.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	items, err := svc.ListTopupPackages(r.Context(), true)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) handleMaasWallet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	svc := h.maasSvc()
	if svc == nil || !svc.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	tenantID := GetTenantID(r)
	wallet, err := svc.GetWallet(r.Context(), tenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, wallet)
}

func (h *Handler) handleMaasAccount(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	svc := h.maasSvc()
	if svc == nil || !svc.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	tenantID := GetTenantID(r)
	account, err := svc.GetAccount(r.Context(), tenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, account)
}

func (h *Handler) handleMaasOrders(w http.ResponseWriter, r *http.Request) {
	svc := h.maasSvc()
	if svc == nil || !svc.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	switch r.Method {
	case http.MethodGet:
		limit := 20
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				limit = n
			}
		}
		tenantID := GetTenantID(r)
		items, err := svc.ListOrders(r.Context(), tenantID, limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items})
	case http.MethodPost:
		var body maas.CreateOrderRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json")
			return
		}
		tenantID := GetTenantID(r)
		order, err := svc.CreateOrder(r.Context(), tenantID, body)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, order)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) handleMaasOrderByID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	svc := h.maasSvc()
	if svc == nil || !svc.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	idStr := strings.TrimPrefix(r.URL.Path, "/api/maas/orders/")
	id, err := strconv.ParseInt(strings.Trim(idStr, "/"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid order id")
		return
	}
	tenantID := GetTenantID(r)
	order, err := svc.GetOrder(r.Context(), id, tenantID)
	if err != nil {
		writeError(w, http.StatusNotFound, "order not found")
		return
	}
	writeJSON(w, http.StatusOK, order)
}

func (h *Handler) handleAdminMaasOrders(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	svc := h.maasSvc()
	if svc == nil || !svc.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	items, err := svc.ListOrders(r.Context(), "", limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) handleAdminMaasOrderByID(w http.ResponseWriter, r *http.Request) {
	svc := h.maasSvc()
	if svc == nil || !svc.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/admin/maas/orders/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) < 1 || parts[0] == "" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid order id")
		return
	}
	if len(parts) >= 2 && parts[1] == "confirm" {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var body struct {
			Note string `json:"note"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if err := svc.ConfirmOrder(r.Context(), id, body.Note); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	order, err := svc.GetOrder(r.Context(), id, "")
	if err != nil {
		writeError(w, http.StatusNotFound, "order not found")
		return
	}
	writeJSON(w, http.StatusOK, order)
}

func (h *Handler) handleMaasUsageSummary(w http.ResponseWriter, r *http.Request) {
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
	limit := 10
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	tenantID := GetTenantID(r)
	summary, err := svc.QueryUsageSummary(r.Context(), tenantID, days, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

func (h *Handler) handleMaasLedger(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	svc := h.maasSvc()
	if svc == nil || !svc.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	tenantID := GetTenantID(r)
	items, err := svc.ListLedger(r.Context(), tenantID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
