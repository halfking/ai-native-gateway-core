// Package admin — Session Analytics Top Sessions & Filter Options API
//
// 补全前端 Dashboard 调用但后端缺失的两个端点（审计发现）：
//   1. GET /api/admin/session-analytics/top-sessions   - 热门会话排名
//   2. GET /api/admin/session-analytics/filter-options - 过滤器可选值
//
// 这两个端点被 SessionAnalyticsDashboardView.vue 直接调用，但前序并行代理
// 未实现后端 handler，导致前端 404。本文件补全。
//
// 参考文档：
//   - docs/session-management-analytics-plan.md §4.2.4（热门会话排名）
//   - docs/session-management-analytics-plan.md §4.2.5（统一过滤器）
package admin

import (
	"context"
	"net/http"
	"strconv"
	"time"
)

// ── 响应类型 ────────────────────────────────────────────────────────

// TopSessionsResponse 热门会话排名响应
type TopSessionsResponse struct {
	Metric   string             `json:"metric"`
	Sessions []TopSessionItem   `json:"sessions"`
}

// TopSessionItem 单个热门会话
type TopSessionItem struct {
	GwSessionID     string   `json:"gw_session_id"`
	Title           *string  `json:"title"`
	TenantID        string   `json:"tenant_id"`
	RequestCount    int      `json:"request_count"`
	TotalCostUSD    float64  `json:"total_cost_usd"`
	TotalTokens     int64    `json:"total_tokens"`
	DurationSeconds int      `json:"duration_seconds"`
	AvgLatencyMs    int      `json:"avg_latency_ms"`
	HealthGrade     *string  `json:"health_grade"`
	PrimaryModel    *string  `json:"primary_model"`
}

// FilterOptionsResponse 过滤器可选值响应（模型/提供商列表，用于前端下拉填充）
type FilterOptionsResponse struct {
	Models    []string `json:"models"`
	Providers []string `json:"providers"`
}

// ── Handlers ───────────────────────────────────────────────────────

// HandleTopSessions GET /api/admin/session-analytics/top-sessions
//
// 返回按指定指标排序的 Top N 会话。前端 Dashboard 用此填充"热门会话"表格，
// 点击行可跳转到该会话的全景图。
//
// 查询参数：
//   - metric: cost(默认) | tokens | latency | duration
//   - limit:  默认 10，上限 100
//   - date_from / date_to: 可选，默认最近 7 天
//   - model / provider / compliance_status: 可选过滤
func (h *Handler) HandleTopSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "db not available")
		return
	}

	filters, err := parseAnalyticsFilters(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	metric := r.URL.Query().Get("metric")
	if metric == "" {
		metric = "cost"
	}
	limit := 10
	if l := r.URL.Query().Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 && v <= 100 {
			limit = v
		}
	}

	// 映射排序字段（白名单防 SQL 注入）
	orderCol := "total_cost_usd"
	switch metric {
	case "tokens":
		orderCol = "total_tokens"
	case "latency":
		orderCol = "avg_latency_ms"
	case "duration":
		orderCol = "duration_seconds"
	case "cost":
		orderCol = "total_cost_usd"
	default:
		writeError(w, http.StatusBadRequest, "invalid metric: must be cost|tokens|latency|duration")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	tenantID := EffectiveTenantIDAll(r)

	query := `
		SELECT session_key, tenant_id, title, request_count,
		       total_cost_usd, total_tokens, duration_seconds,
		       avg_latency_ms, health_grade, primary_model
		FROM session_summaries
		WHERE first_request_at >= $1 AND first_request_at < $2`
	args := []interface{}{filters.dateFrom, filters.dateTo}
	argIdx := 3

	if tenantID != "" {
		query += " AND tenant_id = $" + strconv.Itoa(argIdx)
		args = append(args, tenantID)
		argIdx++
	}
	if filters.model != "" {
		query += " AND $" + strconv.Itoa(argIdx) + " = ANY(models_used)"
		args = append(args, filters.model)
		argIdx++
	}
	if filters.provider != "" {
		query += " AND $" + strconv.Itoa(argIdx) + " = ANY(providers)"
		args = append(args, filters.provider)
		argIdx++
	}
	if filters.complianceStatus != "" {
		query += " AND compliance_status = $" + strconv.Itoa(argIdx)
		args = append(args, filters.complianceStatus)
		argIdx++
	}

	query += " ORDER BY " + orderCol + " DESC NULLS LAST LIMIT " + strconv.Itoa(limit)

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}
	defer rows.Close()

	sessions := []TopSessionItem{}
	for rows.Next() {
		var s TopSessionItem
		if err := rows.Scan(
			&s.GwSessionID, &s.TenantID, &s.Title, &s.RequestCount,
			&s.TotalCostUSD, &s.TotalTokens, &s.DurationSeconds,
			&s.AvgLatencyMs, &s.HealthGrade, &s.PrimaryModel,
		); err != nil {
			writeError(w, http.StatusInternalServerError, "scan failed: "+err.Error())
			return
		}
		sessions = append(sessions, s)
	}

	writeJSON(w, http.StatusOK, TopSessionsResponse{
		Metric:   metric,
		Sessions: sessions,
	})
}

// HandleFilterOptions GET /api/admin/session-analytics/filter-options
//
// 返回该租户实际使用过的模型与提供商列表，用于前端过滤器下拉框填充。
// 数据从 session_summaries 的 models_used / providers 数组聚合（DISTINCT
// UNNEST），而非硬编码——这样新模型/提供商接入后自动出现在过滤器中。
func (h *Handler) HandleFilterOptions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "db not available")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	tenantID := EffectiveTenantIDAll(r)

	// 模型列表（从近 30 天数据聚合，避免全表扫描）
	modelQuery := `
		SELECT DISTINCT model FROM (
			SELECT UNNEST(models_used) AS model
			FROM session_summaries
			WHERE last_request_at > NOW() - INTERVAL '30 days'`
	modelArgs := []interface{}{}
	if tenantID != "" {
		modelQuery += " AND tenant_id = $1"
		modelArgs = append(modelArgs, tenantID)
	}
	modelQuery += ") t WHERE model IS NOT NULL AND model != '' ORDER BY model"

	modelRows, err := h.db.Query(ctx, modelQuery, modelArgs...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query models failed: "+err.Error())
		return
	}
	defer modelRows.Close()

	models := []string{}
	for modelRows.Next() {
		var m string
		if err := modelRows.Scan(&m); err == nil {
			models = append(models, m)
		}
	}

	// 提供商列表
	providerQuery := `
		SELECT DISTINCT provider FROM (
			SELECT UNNEST(providers) AS provider
			FROM session_summaries
			WHERE last_request_at > NOW() - INTERVAL '30 days'`
	providerArgs := []interface{}{}
	if tenantID != "" {
		providerQuery += " AND tenant_id = $1"
		providerArgs = append(providerArgs, tenantID)
	}
	providerQuery += ") t WHERE provider IS NOT NULL AND provider != '' ORDER BY provider"

	providerRows, err := h.db.Query(ctx, providerQuery, providerArgs...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query providers failed: "+err.Error())
		return
	}
	defer providerRows.Close()

	providers := []string{}
	for providerRows.Next() {
		var p string
		if err := providerRows.Scan(&p); err == nil {
			providers = append(providers, p)
		}
	}

	writeJSON(w, http.StatusOK, FilterOptionsResponse{
		Models:    models,
		Providers: providers,
	})
}
