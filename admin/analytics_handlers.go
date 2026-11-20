package admin

// analytics_handlers.go — 分析详情端点处理器 (2026-11-20)
//
// 为运营看板详情分析系统提供后端支持,支持4个维度:
// - model (模型)
// - provider (供应商)
// - client (客户端)
// - tenant (租户)
//
// 端点: GET /api/admin/analytics/:dimension/:identifier
// 权限: superAdmin / admin_key (与 /admin/dashboard/board 保持一致)

import (
	"net/http"
	"strconv"
	"time"
)

// AnalyticsDetailPayload 分析详情完整数据
type AnalyticsDetailPayload struct {
	Dimension     string                 `json:"dimension"`
	Identifier    string                 `json:"identifier"`
	DisplayName   string                 `json:"display_name,omitempty"`
	TimeRange     AnalyticsTimeRange     `json:"time_range"`
	Summary       AnalyticsSummary       `json:"summary"`
	Trends        []AnalyticsTrendPoint  `json:"trends"`
	CrossAnalysis AnalyticsCrossAnalysis `json:"cross_analysis"`

	// 降级标记
	Degraded       bool     `json:"degraded,omitempty"`
	DegradedReason string   `json:"degraded_reason,omitempty"`
	DegradedTrends bool     `json:"degraded_trends,omitempty"`
	DegradedCross  []string `json:"degraded_cross,omitempty"`
}

// AnalyticsTimeRange 时间范围
type AnalyticsTimeRange struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
	Days  int       `json:"days"`
}

// AnalyticsSummary 核心指标汇总
type AnalyticsSummary struct {
	TotalRequests         int64   `json:"total_requests"`
	SuccessCount          int64   `json:"success_count"`
	SuccessRate           float64 `json:"success_rate"`
	ErrorCount            int64   `json:"error_count"`
	TotalTokens           int64   `json:"total_tokens"`
	TotalPromptTokens     int64   `json:"total_prompt_tokens,omitempty"`
	TotalCompletionTokens int64   `json:"total_completion_tokens,omitempty"`
	TotalCostUSD          float64 `json:"total_cost_usd"`
	TotalCredits          float64 `json:"total_credits"`
	AvgLatencyMS          float64 `json:"avg_latency_ms"`
	P95LatencyMS          float64 `json:"p95_latency_ms,omitempty"`
	P99LatencyMS          float64 `json:"p99_latency_ms,omitempty"`
}

// AnalyticsTrendPoint 趋势数据点
type AnalyticsTrendPoint struct {
	Bucket       string  `json:"bucket"`
	Requests     int64   `json:"requests"`
	Success      int64   `json:"success"`
	Errors       int64   `json:"errors"`
	Tokens       int64   `json:"tokens"`
	CostUSD      float64 `json:"cost_usd"`
	Credits      float64 `json:"credits"`
	AvgLatencyMS float64 `json:"avg_latency_ms"`
}

// AnalyticsCrossAnalysis 交叉分析数据
type AnalyticsCrossAnalysis struct {
	ByModel    []boardPieItem `json:"by_model,omitempty"`
	ByProvider []boardPieItem `json:"by_provider,omitempty"`
	ByClient   []boardPieItem `json:"by_client,omitempty"`
	ByTenant   []boardPieItem `json:"by_tenant,omitempty"`
	ByError    []boardPieItem `json:"by_error,omitempty"`
	ByStatus   []boardPieItem `json:"by_status,omitempty"`
}

// handleAnalyticsDetail 处理分析详情请求
//
// GET /api/admin/analytics/:dimension/:identifier
//
// 参数:
//   - dimension: model/provider/client/tenant
//   - identifier: 对象ID (URL编码)
//   - days: 时间范围(天数), 默认7
//   - start, end: 或使用开始/结束时间
//   - tenant_id: 可选租户过滤
//
// 权限: superAdmin / admin_key
func (h *Handler) handleAnalyticsDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// 从路径中获取参数
	dimension := r.PathValue("dimension")
	identifier := r.PathValue("identifier")

	// 验证维度
	if !isValidDimension(dimension) {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "invalid dimension, must be one of: model, provider, client, tenant",
		})
		return
	}

	// 解析时间范围
	tr, err := parseAnalyticsTimeRange(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "invalid time range: " + err.Error(),
		})
		return
	}

	// 可选租户过滤
	tenantID := r.URL.Query().Get("tenant_id")

	// 查询汇总数据
	summary, summaryOK := h.queryAnalyticsSummary(ctx, dimension, identifier, tr, tenantID)
	if !summaryOK {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "failed to query summary data",
		})
		return
	}

	// 查询趋势数据
	trends, trendsOK := h.queryAnalyticsTrends(ctx, dimension, identifier, tr, tenantID)
	degradedTrends := !trendsOK

	// 查询交叉分析数据
	crossAnalysis, degradedCross := h.queryAnalyticsCrossAnalysis(ctx, dimension, identifier, tr, tenantID)

	// 构建响应
	payload := AnalyticsDetailPayload{
		Dimension:   dimension,
		Identifier:  identifier,
		DisplayName: identifier, // 可以后续增强,查询实际显示名称
		TimeRange: AnalyticsTimeRange{
			Start: tr.Start,
			End:   tr.End,
			Days:  tr.Days,
		},
		Summary:        summary,
		Trends:         trends,
		CrossAnalysis:  crossAnalysis,
		DegradedTrends: degradedTrends,
		DegradedCross:  degradedCross,
	}

	writeJSON(w, http.StatusOK, payload)
}

// isValidDimension 验证维度名称
func isValidDimension(dimension string) bool {
	switch dimension {
	case "model", "provider", "client", "tenant":
		return true
	default:
		return false
	}
}

// parseAnalyticsTimeRange 解析时间范围参数
func parseAnalyticsTimeRange(r *http.Request) (boardTimeRange, error) {
	q := r.URL.Query()

	// 优先使用 start/end
	if startStr := q.Get("start"); startStr != "" {
		endStr := q.Get("end")
		if endStr == "" {
			endStr = time.Now().Format(time.RFC3339)
		}

		start, err := time.Parse(time.RFC3339, startStr)
		if err != nil {
			return boardTimeRange{}, err
		}

		end, err := time.Parse(time.RFC3339, endStr)
		if err != nil {
			return boardTimeRange{}, err
		}

		days := int(end.Sub(start).Hours() / 24)
		return boardTimeRange{
			Start: start,
			End:   end,
			Days:  days,
		}, nil
	}

	// 使用 days 参数
	daysStr := q.Get("days")
	if daysStr == "" {
		daysStr = "7"
	}

	days, err := strconv.Atoi(daysStr)
	if err != nil || days < 1 || days > 365 {
		days = 7
	}

	end := time.Now()
	start := end.Add(-time.Duration(days) * 24 * time.Hour)

	return boardTimeRange{
		Start: start,
		End:   end,
		Days:  days,
	}, nil
}
