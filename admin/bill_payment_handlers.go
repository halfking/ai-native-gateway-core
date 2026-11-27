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
	
	// 账单查询
	// ★ 2026-11-27 12h 审计轮（W9 收口）：list/details 此前走 h.admin（任意
	//   JWT）——同一组账单数据在 /api/admin/bills 族是 superAdmin 闸（财务
	//   敏感面与 market/model-platforms 同级），弱闸路径让等权数据暴露在
	//   tenant_admin/user 之下。二收一到 superAdmin。
	mux.HandleFunc("/api/admin/maas/bills/list", h.superAdmin(h.handleListBills))
	mux.HandleFunc("/api/admin/maas/bills/details", h.superAdmin(h.handleGetBillDetails))
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

	// ★ W8 契约对齐：POST /api/admin/bills/{id}/mark-paid 的路径参数此前被忽略。
	// 路径 {id} 优先，body bill_id 后备。
	if id, err := resolveBillID(r); err == nil && id > 0 {
		req.BillID = id
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

	// ★ W8 契约对齐：POST /api/admin/bills/{id}/void 的路径参数此前被忽略。
	// 路径 {id} 优先，body bill_id 后备。
	if id, err := resolveBillID(r); err == nil && id > 0 {
		req.BillID = id
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

// resolveBillID 兼容两条路由族的账单 ID 读取（2026-11-27 12h 审计轮 W8）。
//
// 同一组 handler 挂在两个路由族上：/api/admin/bills/{id}（路径参数声明）
// 与 /api/admin/maas/bills/*（query/body 传参）——此前路径 {id} 被完全忽略，
// 走 {id} 形态不带 query 时 400，API 契约与路由声明不符。优先级：
// 路径 {id} > query bill_id >（mark-paid/void 的）JSON body。
func resolveBillID(r *http.Request) (int64, error) {
	if v := r.PathValue("id"); v != "" {
		return strconv.ParseInt(v, 10, 64)
	}
	if v := r.URL.Query().Get("bill_id"); v != "" {
		return strconv.ParseInt(v, 10, 64)
	}
	return 0, errBillIDMissing
}

var errBillIDMissing = errBillIDRequired{}

type errBillIDRequired struct{}

func (errBillIDRequired) Error() string { return "bill_id required" }

// handleGetBillDetails 获取账单详情（包含审计日志）
// GET /api/admin/bills/{id} 或 GET /api/admin/maas/bills/details?bill_id=123
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

	// 解析bill_id（路径 {id} 优先，query 后备 —— W8 契约对齐）
	billID, err := resolveBillID(r)
	if err != nil || billID <= 0 {
		writeError(w, http.StatusBadRequest, "bill_id required")
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

// getAdminEmail 从请求中获取管理员身份（辅助函数）。
//
// ★ 2026-11-27 12h 审计轮（P1-E 根修）：此前优先取客户端可伪造的
//   X-Admin-Email header（注释自承「临时实现」）⇒ mark-paid/void/batch 的
//   maas_bill_status_audit.action_by 可被任意 super_admin 请求伪造，
//   1001 迁移建的审计触发器失去可信主体。改为以 AdminMiddleware 注入的
//   AuthContext（GetAuthContext，admin/context.go:31）为准——Username 是
//   验签后的 JWT 主体；header 仅在无 AuthContext 时作为 legacy admin-key
//   请求的补充记录，且格式上带 header: 前缀以免与真实用户名混淆。
func getAdminEmail(r *http.Request) string {
	if auth := GetAuthContext(r); auth != nil && auth.Username != "" {
		return auth.Username
	}

	// legacy admin-key 通道（无 JWT 主体）：保留 header 作为来源标注，
	// 显式标记不可信来源，审计侧可据此区分。
	if email := r.Header.Get("X-Admin-Email"); email != "" {
		return "header:" + email
	}

	return "unknown"
}
