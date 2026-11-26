package admin

import (
	"net/http"
	"strconv"
	"time"

	"github.com/kaixuan/llm-gateway-go/maas"
)

// 在 registerMaasBillingRoutes 中添加新路由
func (h *Handler) registerBillPaymentRoutes(mux *http.ServeMux) {
	// 账单状态管理（仅super_admin）
	mux.HandleFunc("/api/admin/maas/bills/mark-paid", h.superAdmin(h.handleMarkBillPaid))
	mux.HandleFunc("/api/admin/maas/bills/void", h.superAdmin(h.handleVoidBill))
	mux.HandleFunc("/api/admin/maas/bills/batch-mark-paid", h.superAdmin(h.handleBatchMarkBillsPaid))
	
	// 账单查询（admin权限）
	mux.HandleFunc("/api/admin/maas/bills/list", h.admin(h.handleListBills))
	mux.HandleFunc("/api/admin/maas/bills/details", h.admin(h.handleGetBillDetails))
}

// handleMarkBillPaid 标记账单为已付款
// POST /api/admin/maas/bills/mark-paid
func (h *Handler) handleMarkBillPaid(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	svc := h.maasSvc()
	if svc == nil || !svc.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "maas service disabled")
		return
	}

	var req struct {
		BillID             int64     `json:"bill_id"`
		PaymentDate        time.Time `json:"payment_date"`
		PaymentMethod      string    `json:"payment_method"`
		PaymentVoucher     string    `json:"payment_voucher"`
		PaymentAmountCents int64     `json:"payment_amount_cents"`
		Notes              string    `json:"notes,omitempty"`
	}

	if err := readJSONRequired(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}

	// 验证必填字段
	if req.BillID <= 0 {
		writeError(w, http.StatusBadRequest, "bill_id required")
		return
	}
	if req.PaymentDate.IsZero() {
		writeError(w, http.StatusBadRequest, "payment_date required")
		return
	}
	if req.PaymentMethod == "" {
		writeError(w, http.StatusBadRequest, "payment_method required")
		return
	}
	if req.PaymentAmountCents <= 0 {
		writeError(w, http.StatusBadRequest, "payment_amount_cents must be positive")
		return
	}

	// 获取操作人（从认证信息中）
	actionBy := getAdminEmail(r) // 假设有这个辅助函数
	if actionBy == "" {
		actionBy = "unknown"
	}

	// 调用服务层
	err := svc.MarkBillPaid(r.Context(), req.BillID, maas.BillPaymentInfo{
		PaymentDate:        req.PaymentDate,
		PaymentMethod:      req.PaymentMethod,
		PaymentVoucher:     req.PaymentVoucher,
		PaymentAmountCents: req.PaymentAmountCents,
		Notes:              req.Notes,
		ActionBy:           actionBy,
	})

	if err != nil {
		if err.Error() == "bill not found or already paid" {
			writeError(w, http.StatusNotFound, err.Error())
		} else {
			writeInternalErr(w, "failed to mark bill as paid", err)
		}
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"message": "Bill marked as paid successfully",
		"bill_id": req.BillID,
	})
}

// handleVoidBill 作废账单
// POST /api/admin/maas/bills/void
func (h *Handler) handleVoidBill(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	svc := h.maasSvc()
	if svc == nil || !svc.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "maas service disabled")
		return
	}

	var req struct {
		BillID int64  `json:"bill_id"`
		Reason string `json:"reason"` // dispute / adjustment / duplicate / error / other
		Notes  string `json:"notes,omitempty"`
	}

	if err := readJSONRequired(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}

	// 验证必填字段
	if req.BillID <= 0 {
		writeError(w, http.StatusBadRequest, "bill_id required")
		return
	}
	if req.Reason == "" {
		writeError(w, http.StatusBadRequest, "reason required")
		return
	}

	// 验证reason有效性
	validReasons := map[string]bool{
		"dispute":    true,
		"adjustment": true,
		"duplicate":  true,
		"error":      true,
		"other":      true,
	}
	if !validReasons[req.Reason] {
		writeError(w, http.StatusBadRequest, "invalid reason (must be: dispute, adjustment, duplicate, error, or other)")
		return
	}

	// 获取操作人
	actionBy := getAdminEmail(r)
	if actionBy == "" {
		actionBy = "unknown"
	}

	// 调用服务层
	err := svc.VoidBill(r.Context(), req.BillID, maas.BillVoidInfo{
		Reason:   req.Reason,
		Notes:    req.Notes,
		ActionBy: actionBy,
	})

	if err != nil {
		if err.Error() == "bill not found or cannot be voided (must be in issued status)" {
			writeError(w, http.StatusBadRequest, err.Error())
		} else {
			writeInternalErr(w, "failed to void bill", err)
		}
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"message": "Bill voided successfully",
		"bill_id": req.BillID,
	})
}

// handleBatchMarkBillsPaid 批量标记账单为已付款
// POST /api/admin/maas/bills/batch-mark-paid
func (h *Handler) handleBatchMarkBillsPaid(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	svc := h.maasSvc()
	if svc == nil || !svc.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "maas service disabled")
		return
	}

	var req struct {
		Bills []struct {
			BillID             int64     `json:"bill_id"`
			PaymentDate        time.Time `json:"payment_date"`
			PaymentVoucher     string    `json:"payment_voucher"`
			PaymentAmountCents int64     `json:"payment_amount_cents"`
		} `json:"bills"`
		PaymentMethod string `json:"payment_method"` // 统一付款方式
	}

	if err := readJSONRequired(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}

	if len(req.Bills) == 0 {
		writeError(w, http.StatusBadRequest, "bills array cannot be empty")
		return
	}

	if len(req.Bills) > 100 {
		writeError(w, http.StatusBadRequest, "cannot process more than 100 bills at once")
		return
	}

	if req.PaymentMethod == "" {
		writeError(w, http.StatusBadRequest, "payment_method required")
		return
	}

	// 获取操作人
	actionBy := getAdminEmail(r)
	if actionBy == "" {
		actionBy = "unknown"
	}

	// 转换为服务层期望的类型（去除 JSON 标签）
	payments := make([]struct {
		BillID             int64
		PaymentDate        time.Time
		PaymentVoucher     string
		PaymentAmountCents int64
	}, len(req.Bills))

	for i, b := range req.Bills {
		payments[i].BillID = b.BillID
		payments[i].PaymentDate = b.PaymentDate
		payments[i].PaymentVoucher = b.PaymentVoucher
		payments[i].PaymentAmountCents = b.PaymentAmountCents
	}

	// 调用服务层批量处理
	succeeded, failed, err := svc.BatchMarkBillsPaid(r.Context(), payments, req.PaymentMethod, actionBy)

	if err != nil {
		writeInternalErr(w, "batch operation failed", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":   true,
		"message":   "Batch operation completed",
		"succeeded": succeeded,
		"failed":    failed,
		"total":     len(req.Bills),
	})
}

// handleListBills 查询账单列表
// GET /api/admin/maas/bills/list?tenant_id=xxx&status=issued&overdue=true&limit=50&offset=0
func (h *Handler) handleListBills(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	svc := h.maasSvc()
	if svc == nil || !svc.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "maas service disabled")
		return
	}

	// 解析查询参数
	query := r.URL.Query()
	
	filter := maas.ListBillsFilter{
		TenantID:   query.Get("tenant_id"),
		Status:     query.Get("status"),
		PeriodFrom: query.Get("period_from"),
		PeriodTo:   query.Get("period_to"),
		Overdue:    query.Get("overdue") == "true",
	}

	// 解析分页参数
	if limitStr := query.Get("limit"); limitStr != "" {
		if limit, err := strconv.Atoi(limitStr); err == nil {
			filter.Limit = limit
		}
	}
	if offsetStr := query.Get("offset"); offsetStr != "" {
		if offset, err := strconv.Atoi(offsetStr); err == nil {
			filter.Offset = offset
		}
	}

	// 调用服务层
	bills, total, err := svc.ListBills(r.Context(), filter)
	if err != nil {
		writeInternalErr(w, "failed to query bills", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"bills":  bills,
		"total":  total,
		"limit":  filter.Limit,
		"offset": filter.Offset,
	})
}

// handleGetBillDetails 获取账单详情（包含审计日志）
// GET /api/admin/maas/bills/details?bill_id=123
func (h *Handler) handleGetBillDetails(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	svc := h.maasSvc()
	if svc == nil || !svc.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "maas service disabled")
		return
	}

	// 解析bill_id
	billIDStr := r.URL.Query().Get("bill_id")
	if billIDStr == "" {
		writeError(w, http.StatusBadRequest, "bill_id required")
		return
	}

	billID, err := strconv.ParseInt(billIDStr, 10, 64)
	if err != nil || billID <= 0 {
		writeError(w, http.StatusBadRequest, "invalid bill_id")
		return
	}

	// 调用服务层
	bill, auditLogs, err := svc.GetBillDetails(r.Context(), billID)
	if err != nil {
		if err.Error() == "bill not found" {
			writeError(w, http.StatusNotFound, err.Error())
		} else {
			writeInternalErr(w, "failed to query bill details", err)
		}
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"bill":        bill,
		"audit_logs":  auditLogs,
	})
}

// getAdminEmail 从请求中获取管理员邮箱（辅助函数）
func getAdminEmail(r *http.Request) string {
	// 从context或session中获取当前登录用户信息
	// 这里需要根据实际的认证机制实现
	// 示例：从JWT claims中获取
	
	// 临时实现：从header获取
	email := r.Header.Get("X-Admin-Email")
	if email != "" {
		return email
	}
	
	// 或从context获取
	if user, ok := r.Context().Value("admin_user").(string); ok {
		return user
	}
	
	return "unknown"
}
