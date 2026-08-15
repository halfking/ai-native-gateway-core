// Package admin — provider_cost_reconciliation.go
//
// 供应商成本对账管理端点（M3 CO-3，doc 19 §3）：
//
//	POST /api/admin/provider-cost-reconciliation/bill   导入供应商月度账单（manual）
//	GET  /api/admin/provider-cost-reconciliation?month=2026-07  查询月度对账与 diff 率
//
// 路由仅在 provider_profile.cost_reconciliation.enabled 开启时注册
// （cmd/gateway/main.go），默认关闭。POST 导入账单后如果差异率超过阈值，
// service 层会向 provider_events 写入 cost_reconciliation_diff 告警事件。
package admin

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/providerprofile"
)

// CostReconciliationService 是对账处理器依赖的服务接口（便于测试注入 mock）。
// *providerprofile.CostReconciler 默认满足该接口。
type CostReconciliationService interface {
	ImportBill(ctx context.Context, bill providerprofile.ProviderBill) (*providerprofile.ReconciliationRecord, error)
	ListByMonth(ctx context.Context, month time.Time) ([]providerprofile.ReconciliationRecord, error)
}

// ProviderCostReconciliationHandler 供应商成本对账 API。
type ProviderCostReconciliationHandler struct {
	service CostReconciliationService
}

// NewProviderCostReconciliationHandler 构造处理器；service 为 nil 时返回 nil
// （路由不注册，与 routeIncidentHandler 的 nil 语义一致）。
func NewProviderCostReconciliationHandler(service CostReconciliationService) *ProviderCostReconciliationHandler {
	if service == nil {
		return nil
	}
	return &ProviderCostReconciliationHandler{service: service}
}

// RegisterRoutes 注册路由。wrap 应传 superAdmin 中间件；nil 时裸注册（仅测试用）。
func (h *ProviderCostReconciliationHandler) RegisterRoutes(mux *http.ServeMux, wrap func(http.HandlerFunc) http.HandlerFunc) {
	if h == nil {
		return
	}
	if wrap == nil {
		wrap = func(fn http.HandlerFunc) http.HandlerFunc { return fn }
	}
	mux.HandleFunc("/api/admin/provider-cost-reconciliation", wrap(h.handleList))
	mux.HandleFunc("/api/admin/provider-cost-reconciliation/bill", wrap(h.handleImportBill))
}

// importBillRequest POST /bill 的请求体。
type importBillRequest struct {
	ProviderID   int64   `json:"provider_id"`
	Month        string  `json:"month"` // "2026-07"，取月份首日
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	TotalTokens  int64   `json:"total_tokens"`
	TotalCost    float64 `json:"total_cost"`
	Notes        string  `json:"notes"`
}

// handleImportBill 导入供应商月度账单。
func (h *ProviderCostReconciliationHandler) handleImportBill(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req importBillRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.ProviderID <= 0 {
		writeError(w, http.StatusBadRequest, "provider_id is required")
		return
	}
	month, err := time.Parse("2006-01", req.Month)
	if err != nil {
		writeError(w, http.StatusBadRequest, "month must be in YYYY-MM format")
		return
	}

	rec, err := h.service.ImportBill(r.Context(), providerprofile.ProviderBill{
		ProviderID:   req.ProviderID,
		Month:        month,
		InputTokens:  req.InputTokens,
		OutputTokens: req.OutputTokens,
		TotalTokens:  req.TotalTokens,
		TotalCost:    req.TotalCost,
		DataSource:   "manual",
		Notes:        req.Notes,
	})
	if err != nil {
		slog.Error("provider cost reconciliation: import bill failed", "error", err, "provider_id", req.ProviderID, "month", req.Month)
		writeError(w, http.StatusInternalServerError, "import bill failed")
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

// handleList GET /api/admin/provider-cost-reconciliation?month=YYYY-MM。
// 缺省 month 时使用当前月。
func (h *ProviderCostReconciliationHandler) handleList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	month := time.Now()
	if v := r.URL.Query().Get("month"); v != "" {
		parsed, err := time.Parse("2006-01", v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "month must be in YYYY-MM format")
			return
		}
		month = parsed
	}
	items, err := h.service.ListByMonth(r.Context(), month)
	if err != nil {
		slog.Error("provider cost reconciliation: list failed", "error", err, "month", month.Format("2006-01"))
		writeError(w, http.StatusInternalServerError, "list reconciliation records failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"month": month.Format("2006-01"),
		"items": items,
		"count": len(items),
	})
}
