package admin

// pricing_suggestion.go —— 定价建议的 admin 端点（2026-11-17 定价对比完善轮）。
//
// 需求原文（owner 2026-11-17）：
//   「在进行销售定价时，可以使用复制某个 MaaS 的价格或平均价格，或最低价格等
//     策略，快速地更新本地的销售价格……也可以将本地模型的价格与采集的
//     maas 中的模型价格进行对比，根据策略给出建议方案，可以一键执行。
//     简化价格的操作。并且要有价格变更的历史，用于审计分析。」
//
// 三个端点：
//   POST /api/admin/market/pricing-suggestions  预览建议（**不写任何表**）
//   POST /api/admin/market/pricing-apply        一键执行（写零售价 + 留痕）
//   GET  /api/admin/market/pricing-batches      批次留痕（审计分析）
//
// ★ 红线（与采集侧红线同源、但语义不同，必须分清）：
//   · 行情价**不得自动**回流计费面 ⇒ 本文件没有定时任务、没有后台自动执行；
//   · 回流只发生在运营**显式点击「一键执行」**时 ⇒ confirm=true + superAdmin；
//   · 写入的是**零售价**（model_credit_rates 的 manual 覆盖），不碰供应商
//     实付价、不碰路由权重；
//   · 每次写入经 upsertModelRateSource ⇒ model_price_versions **自动留期次**
//     （source=market_suggestion + note 带依据与批次 id）
//     ⇒ 价格变更历史是**唯一**的一份（期次账 = 模型级、批次表 = 批次级）。
//
// ★ 操作者身份：取自**登录态**（superAdmin 中间件解析出的用户名/租户），
//   **不信前端传参** ⇒ 审计字段不可伪造。

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/kaixuan/llm-gateway-go/maas"
)

// pricingSuggestionOperator 从请求头取操作者标识（审计用）。
// ★ 沿用仓内既有范式（provider_credential.go / session_approval.go 同款）：
//
//	X-Admin-User 头由 admin 中间件从登录态注入。
//
// ★ 取不到就退化成 "unknown" —— **不猜身份**，但身份缺失本身要能被审计看见
//
//	（而不是悄悄记成某个默认账号）。
func pricingSuggestionOperator(r *http.Request) string {
	if v := strings.TrimSpace(r.Header.Get("X-Admin-User")); v != "" {
		return v
	}
	if v := strings.TrimSpace(r.Header.Get("X-Auth-User")); v != "" {
		return v
	}
	return "unknown"
}

// handlePricingSuggestions POST /api/admin/market/pricing-suggestions
// —— 预览定价建议（纯读；不写任何表）。
func (h *Handler) handlePricingSuggestions(w http.ResponseWriter, r *http.Request) {
	svc := h.maasSvc()
	if svc == nil || !svc.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req maas.PricingSuggestionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	req.Operator = pricingSuggestionOperator(r)
	res, err := svc.SuggestPricing(r.Context(), req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handlePricingApply POST /api/admin/market/pricing-apply
// —— 一键执行：把建议价写进本地零售价（期次账自动留痕 + 批次留痕）。
func (h *Handler) handlePricingApply(w http.ResponseWriter, r *http.Request) {
	svc := h.maasSvc()
	if svc == nil || !svc.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req maas.PricingSuggestionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if !req.Confirm {
		// ★ 二次确认闸：前端「一键执行」必须显式带 confirm=true
		//   ⇒ 误触/被构造的请求不会改价。
		writeError(w, http.StatusBadRequest, "执行定价建议需要显式确认（confirm=true）")
		return
	}
	req.Operator = pricingSuggestionOperator(r)
	res, err := svc.ApplyPricingSuggestions(r.Context(), req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handlePricingSuggestionBatches GET /api/admin/market/pricing-batches?limit=
// —— 批次留痕（审计分析）。
func (h *Handler) handlePricingSuggestionBatches(w http.ResponseWriter, r *http.Request) {
	svc := h.maasSvc()
	if svc == nil || !svc.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := svc.ListPricingSuggestionBatches(r.Context(), limit)
	if err != nil {
		writeInternalErr(w, "internal error (see server logs)", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
