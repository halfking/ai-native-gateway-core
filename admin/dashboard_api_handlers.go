package admin

import (
	"net/http"

	"github.com/kaixuan/llm-gateway-go/admin/dashboardapi"
)

// handleDashboardSessionTrend 处理会话趋势请求
//
// GET /api/admin/dashboard/session-trend
func (h *Handler) handleDashboardSessionTrend(w http.ResponseWriter, r *http.Request) {
	handler := dashboardapi.NewSessionTrendHandler(h.db)
	handler.HandleSessionTrend(w, r)
}

// handleDashboardSessionHealth 处理会话健康度请求
//
// GET /api/admin/dashboard/session-health
func (h *Handler) handleDashboardSessionHealth(w http.ResponseWriter, r *http.Request) {
	handler := dashboardapi.NewSessionHealthHandler(h.db)
	handler.HandleSessionHealth(w, r)
}

// handleDashboardSessionActive 处理活跃会话请求
//
// GET /api/admin/dashboard/session-active
func (h *Handler) handleDashboardSessionActive(w http.ResponseWriter, r *http.Request) {
	handler := dashboardapi.NewSessionActiveHandler(h.db)
	handler.HandleSessionActive(w, r)
}

// handleDashboardModuleStats 处理模块统计请求
//
// GET /api/admin/dashboard/module-stats
func (h *Handler) handleDashboardModuleStats(w http.ResponseWriter, r *http.Request) {
	handler := dashboardapi.NewModuleStatsHandler(h.db)
	handler.HandleModuleStats(w, r)
}

// handleDashboardErrors 处理错误统计请求
//
// GET /api/admin/dashboard/errors
func (h *Handler) handleDashboardErrors(w http.ResponseWriter, r *http.Request) {
	handler := dashboardapi.NewErrorsHandler(h.db)
	handler.HandleErrors(w, r)
}

// handleDashboardPerformance 处理性能指标请求
//
// GET /api/admin/dashboard/performance
func (h *Handler) handleDashboardPerformance(w http.ResponseWriter, r *http.Request) {
	handler := dashboardapi.NewPerformanceHandler(h.db)
	handler.HandlePerformance(w, r)
}
