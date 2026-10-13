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
	if err := readJSONRequired(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.ProviderID <= 0 {
		writeError(w, http.StatusBadRequest, "provider_id is required")
		return
	}
	if req.InputTokens < 0 || req.OutputTokens < 0 || req.TotalTokens < 0 || req.TotalCost < 0 {
		writeError(w, http.StatusBadRequest, "bill tokens and cost must be nonnegative")
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
	// 缺省 month 取「当前月的月初」，不是当前时刻。
	//
	// ⚠️ 这里是曾经的真实缺陷（静默失败，非报错）：reconciliation_month 列是
	// date，读端以 `WHERE reconciliation_month = $1::date` 比较，列里存的是
	// **日历月首日**；而裸 `month := time.Now()`（如 2026-10-11 23:45）
	// 传进去与该列必然不等 ⇒ 端点返回 200 + count=0，不报错、不告警。
	//
	// ⚠️⚠️ 第二层（2026-10-13 线上实测）：上一版的修复只对齐了默认路径，
	//	**显式 `?month=YYYY-MM` 那条路径仍然是坏的**：
	//	  · 默认路径用 `time.Date(..., now.Location())`（+08）；
	//	  · 显式路径用 `time.Parse("2006-01", v)` —— 它返回 **UTC**；
	//	  · 而 PG 把 timestamptz 折成 date 时按**会话 TimeZone**，
	//	 于是同一时刻两条路径给出的 date 键不同。
	//	实测：默认路径 count=19、显式 ?month=2026-10 count=0。
	//
	//	⇒ 现在两端都走**文本日历月键**（domains/providerprofile.monthKey），
	//	  这里只需要保证「解析出哪个日历月」是对的，转换交给 SQL 的 `::date`，
	//	  不再依赖任何时区。
	var month time.Time
	now := time.Now()
	if v := r.URL.Query().Get("month"); v != "" {
		// 显式月份：只取年/月，**丢弃时刻与时区**。
		parsed, err := time.Parse("2006-01", v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "month must be in YYYY-MM format")
			return
		}
		month = time.Date(parsed.Year(), parsed.Month(), 1, 0, 0, 0, 0, time.UTC)
	} else {
		month = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
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
