package admin

import (
	"net/http"
	"strconv"
	"time"

	"github.com/kaixuan/llm-gateway-go/maas"
)

func (h *Handler) registerMaasBillingRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/admin/maas/coupons", h.superAdmin(h.handleAdminMaasCoupons))
	mux.HandleFunc("/api/admin/maas/bills/issue", h.superAdmin(h.handleAdminMaasBillsIssue))
	mux.HandleFunc("/api/admin/maas/bills/reconcile", h.superAdmin(h.handleAdminMaasBillsReconcile))

	mux.HandleFunc("/api/maas/bills", h.admin(h.handleMaasBills))
	mux.HandleFunc("/api/maas/bills/export", h.admin(h.handleMaasBillsExport))
	mux.HandleFunc("/api/maas/coupons/activate", h.admin(h.handleMaasCouponActivate))
	mux.HandleFunc("/api/maas/coupons/redemptions", h.admin(h.handleMaasCouponRedemptions))
	mux.HandleFunc("/api/maas/invoices", h.admin(h.handleMaasInvoicesPlaceholder))
}

func (h *Handler) handleAdminMaasCoupons(w http.ResponseWriter, r *http.Request) {
	svc := h.maasSvc()
	if svc == nil || !svc.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	switch r.Method {
	case http.MethodGet:
		limit := 50
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				limit = n
			}
		}
		items, err := svc.ListCoupons(r.Context(), limit)
		if err != nil {
			writeInternalErr(w, "internal error (see server logs)", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items})
	case http.MethodPost:
		var body maas.CreateCouponRequest
		if err := readJSONRequired(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json")
			return
		}
		if body.CreatedBy == "" {
			body.CreatedBy = "admin"
		}
		c, err := svc.CreateCoupon(r.Context(), body)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, c)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) handleAdminMaasBillsIssue(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	svc := h.maasSvc()
	if svc == nil || !svc.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	var body struct {
		TenantID string `json:"tenant_id"`
		PeriodYM string `json:"period_ym"`
		All      bool   `json:"all"`
	}
	if err := readJSONRequired(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if body.All {
		issued, skipped, err := svc.IssuePreviousMonthForAllTenants(r.Context(), time.Now().UTC())
		if err != nil {
			writeInternalErr(w, "internal error (see server logs)", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"issued":  issued,
			"skipped": skipped,
			"period":  maas.PreviousPeriodYM(time.Now().UTC(), time.UTC),
		})
		return
	}
	if body.TenantID == "" || body.PeriodYM == "" {
		writeError(w, http.StatusBadRequest, "tenant_id and period_ym required")
		return
	}
	bill, err := svc.IssueMonthlyBill(r.Context(), body.TenantID, body.PeriodYM)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, bill)
}

func (h *Handler) handleAdminMaasBillsReconcile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	svc := h.maasSvc()
	if svc == nil || !svc.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	tenantID := r.URL.Query().Get("tenant_id")
	periodYM := r.URL.Query().Get("period_ym")
	if tenantID == "" || periodYM == "" {
		writeError(w, http.StatusBadRequest, "tenant_id and period_ym required")
		return
	}
	reading, err := svc.ReconcileTripartite(r.Context(), tenantID, periodYM)
	if err != nil {
		writeInternalErr(w, "internal error (see server logs)", err)
		return
	}
	writeJSON(w, http.StatusOK, reading)
}

func (h *Handler) handleMaasBills(w http.ResponseWriter, r *http.Request) {
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
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "tenant required")
		return
	}
	limit := 24
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	items, err := svc.ListMonthlyBills(r.Context(), tenantID, limit)
	if err != nil {
		writeInternalErr(w, "internal error (see server logs)", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) handleMaasBillsExport(w http.ResponseWriter, r *http.Request) {
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
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "tenant required")
		return
	}
	items, err := svc.ListMonthlyBills(r.Context(), tenantID, 120)
	if err != nil {
		writeInternalErr(w, "internal error (see server logs)", err)
		return
	}
	csv := maas.FormatBillsCSV(items)
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="bills.csv"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(csv))
}

func (h *Handler) handleMaasCouponActivate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	svc := h.maasSvc()
	if svc == nil || !svc.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	tenantID := GetTenantID(r)
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "tenant required")
		return
	}
	var body struct {
		Code string `json:"code"`
	}
	if err := readJSONRequired(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	red, err := svc.ActivateCoupon(r.Context(), tenantID, body.Code)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, red)
}

func (h *Handler) handleMaasCouponRedemptions(w http.ResponseWriter, r *http.Request) {
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
	items, err := svc.ListCouponRedemptions(r.Context(), tenantID, 20)
	if err != nil {
		writeInternalErr(w, "internal error (see server logs)", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) handleMaasInvoicesPlaceholder(w http.ResponseWriter, r *http.Request) {
	svc := h.maasSvc()
	if svc == nil || !svc.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	tenantID := GetTenantID(r)
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "tenant required")
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body struct {
		BillID *int64 `json:"bill_id"`
	}
	_ = readJSONRequired(r, &body)
	inv, err := svc.CreateInvoicePlaceholder(r.Context(), tenantID, body.BillID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, inv)
}

