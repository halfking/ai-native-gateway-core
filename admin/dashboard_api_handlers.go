package admin

import (
	"net/http"
	"strconv"
	"time"

	"github.com/kaixuan/llm-gateway-go/admin/dashboardapi"
)

type dashboardResponseWriter struct {
	http.ResponseWriter
	statusCode  int
	wroteHeader bool
}

func (w *dashboardResponseWriter) WriteHeader(statusCode int) {
	if w.wroteHeader {
		return
	}
	w.statusCode = statusCode
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *dashboardResponseWriter) Write(body []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

func (h *Handler) serveDashboardAPI(w http.ResponseWriter, r *http.Request, next http.HandlerFunc) {
	startedAt := time.Now()
	wrapped := &dashboardResponseWriter{ResponseWriter: w, statusCode: http.StatusOK}
	request := dashboardAPIRequest(r)
	next(wrapped, request)

	if h.dashboardEventRecorder == nil {
		return
	}
	auth := GetAuthContext(r)
	if auth == nil {
		return
	}
	h.dashboardEventRecorder.RecordAccess(
		auth.TenantID,
		strconv.Itoa(auth.UserID),
		auth.Role,
		"",
		r.URL.Path,
		r.Method,
		wrapped.statusCode,
		time.Since(startedAt),
		false,
	)
}

func dashboardAPIRequest(r *http.Request) *http.Request {
	auth := GetAuthContext(r)
	if auth == nil {
		return r
	}
	info := dashboardapi.AuthInfo{
		UserID:       strconv.Itoa(auth.UserID),
		TenantID:     auth.TenantID,
		Username:     auth.Username,
		UserRole:     auth.Role,
		IsSuperAdmin: auth.Role == "super_admin",
		IsJWT:        auth.IsJWT,
	}
	return r.WithContext(dashboardapi.SetAuthInfoToContext(r.Context(), info))
}

func (h *Handler) handleDashboardSessionOverviewAudited(w http.ResponseWriter, r *http.Request) {
	h.serveDashboardAPI(w, r, h.handleDashboardSessionOverview)
}

// handleDashboardSessionTrend 处理会话趋势请求
//
// GET /api/admin/dashboard/session-trend
func (h *Handler) handleDashboardSessionTrend(w http.ResponseWriter, r *http.Request) {
	handler := dashboardapi.NewSessionTrendHandler(h.db)
	h.serveDashboardAPI(w, r, handler.HandleSessionTrend)
}

// handleDashboardSessionHealth 处理会话健康度请求
//
// GET /api/admin/dashboard/session-health
func (h *Handler) handleDashboardSessionHealth(w http.ResponseWriter, r *http.Request) {
	handler := dashboardapi.NewSessionHealthHandler(h.db)
	h.serveDashboardAPI(w, r, handler.HandleSessionHealth)
}

// handleDashboardSessionActive 处理活跃会话请求
//
// GET /api/admin/dashboard/session-active
func (h *Handler) handleDashboardSessionActive(w http.ResponseWriter, r *http.Request) {
	handler := dashboardapi.NewSessionActiveHandler(h.db)
	h.serveDashboardAPI(w, r, handler.HandleSessionActive)
}

// handleDashboardModuleStats 处理模块统计请求
//
// GET /api/admin/dashboard/module-stats
func (h *Handler) handleDashboardModuleStats(w http.ResponseWriter, r *http.Request) {
	handler := dashboardapi.NewModuleStatsHandler(h.db)
	h.serveDashboardAPI(w, r, handler.HandleModuleStats)
}

// handleDashboardErrors 处理错误统计请求
//
// GET /api/admin/dashboard/errors
func (h *Handler) handleDashboardErrors(w http.ResponseWriter, r *http.Request) {
	handler := dashboardapi.NewErrorsHandler(h.db)
	h.serveDashboardAPI(w, r, handler.HandleErrors)
}

// handleDashboardPerformance 处理性能指标请求
//
// GET /api/admin/dashboard/performance
func (h *Handler) handleDashboardPerformance(w http.ResponseWriter, r *http.Request) {
	handler := dashboardapi.NewPerformanceHandler(h.db)
	h.serveDashboardAPI(w, r, handler.HandlePerformance)
}
