package admin

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/diagnostics"
)

// maxDiagnosticLimit 是诊断查询单次返回条数上限。
//
// 审计轮 D15：limit 原先只校验 > 0，客户端可传 limit=1000000000
// 把整张诊断表拉进网关进程内存。诊断表是 append-only 的请求日志，
// 单会话量级可达数万条 ⇒ 必须有硬上限。
const maxDiagnosticLimit = 500

// clampDiagnosticLimit 把客户端给的 limit 夹到 [1, maxDiagnosticLimit]。
func clampDiagnosticLimit(limit int) int {
	if limit < 1 {
		return 1
	}
	if limit > maxDiagnosticLimit {
		return maxDiagnosticLimit
	}
	return limit
}

// DiagnosticHandler 诊断日志HTTP处理器
type DiagnosticHandler struct {
	logger diagnostics.Logger
}

// NewDiagnosticHandler 创建诊断处理器
func NewDiagnosticHandler(logger diagnostics.Logger) *DiagnosticHandler {
	return &DiagnosticHandler{logger: logger}
}

// ServeHTTP 实现http.Handler接口
// 路由规则:
//
//	GET /internal/diagnostics/request/{request_id}
//	GET /internal/diagnostics/session/{session_id}?limit=100
//	GET /internal/diagnostics/failures?start_time=xxx&end_time=xxx&limit=50
//	GET /internal/diagnostics/query?tenant_id=xxx&model=xxx&status_code=500&limit=100
func (h *DiagnosticHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 只支持GET
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/internal/diagnostics/")
	parts := strings.SplitN(path, "/", 2)

	switch parts[0] {
	case "request":
		if len(parts) < 2 || parts[1] == "" {
			http.Error(w, "request_id is required", http.StatusBadRequest)
			return
		}
		h.GetRequest(w, r, parts[1])
	case "session":
		if len(parts) < 2 || parts[1] == "" {
			http.Error(w, "session_id is required", http.StatusBadRequest)
			return
		}
		h.GetSessionRequests(w, r, parts[1])
	case "failures":
		h.GetFailures(w, r)
	case "query":
		h.Query(w, r)
	default:
		http.Error(w, "Not found", http.StatusNotFound)
	}
}

// GetRequest 获取指定请求的诊断日志
func (h *DiagnosticHandler) GetRequest(w http.ResponseWriter, r *http.Request, requestID string) {
	log, err := h.logger.Get(r.Context(), requestID)
	if err != nil {
		// 审计轮 D2/D13：诊断查询失败不得回显内部错误串
		// （pgx 错误含表名/列名/约束名）。守卫只扫 500 状态行，
		// 这里的 404 同样要收口。
		slog.Error("diagnostic_handler: get request diagnostics failed", "request_id", requestID, "error", err)
		http.Error(w, "request diagnostics not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(log)
}

// GetSessionRequests 获取指定会话的所有请求诊断日志
func (h *DiagnosticHandler) GetSessionRequests(w http.ResponseWriter, r *http.Request, sessionID string) {
	limitStr := r.URL.Query().Get("limit")
	limit := 100
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
			limit = l
		}
	}
	limit = clampDiagnosticLimit(limit)

	logs, err := h.logger.ListBySession(r.Context(), sessionID, limit)
	if err != nil {
		writeInternalTextErr(w, "internal error (see server logs)", err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"session_id": sessionID,
		"count":      len(logs),
		"requests":   logs,
	})
}

// GetFailures 获取失败的请求诊断日志
func (h *DiagnosticHandler) GetFailures(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	// 解析时间范围
	now := time.Now()
	startTime := now.Add(-24 * time.Hour) // 默认最近24小时
	endTime := now

	if startTimeStr := query.Get("start_time"); startTimeStr != "" {
		if t, err := time.Parse(time.RFC3339, startTimeStr); err == nil {
			startTime = t
		}
	}

	if endTimeStr := query.Get("end_time"); endTimeStr != "" {
		if t, err := time.Parse(time.RFC3339, endTimeStr); err == nil {
			endTime = t
		}
	}

	limitStr := query.Get("limit")
	limit := 50
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
			limit = l
		}
	}
	limit = clampDiagnosticLimit(limit)

	logs, err := h.logger.ListFailed(r.Context(), startTime, endTime, limit)
	if err != nil {
		writeInternalTextErr(w, "internal error (see server logs)", err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"start_time": startTime,
		"end_time":   endTime,
		"count":      len(logs),
		"failures":   logs,
	})
}

// Query 高级查询诊断日志
func (h *DiagnosticHandler) Query(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	filters := diagnostics.QueryFilters{}

	// 解析查询参数
	now := time.Now()
	filters.StartTime = now.Add(-24 * time.Hour) // 默认最近24小时
	filters.EndTime = now

	if startTimeStr := query.Get("start_time"); startTimeStr != "" {
		if t, err := time.Parse(time.RFC3339, startTimeStr); err == nil {
			filters.StartTime = t
		}
	}

	if endTimeStr := query.Get("end_time"); endTimeStr != "" {
		if t, err := time.Parse(time.RFC3339, endTimeStr); err == nil {
			filters.EndTime = t
		}
	}

	if tenantID := query.Get("tenant_id"); tenantID != "" {
		filters.TenantID = tenantID
	}

	if model := query.Get("model"); model != "" {
		filters.Model = model
	}

	if statusCodeStr := query.Get("status_code"); statusCodeStr != "" {
		if sc, err := strconv.Atoi(statusCodeStr); err == nil {
			filters.StatusCode = sc
		}
	}

	if successOnlyStr := query.Get("success_only"); successOnlyStr != "" {
		if successOnly, err := strconv.ParseBool(successOnlyStr); err == nil {
			filters.SuccessOnly = &successOnly
		}
	}

	limitStr := query.Get("limit")
	limit := 100
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
			limit = l
		}
	}
	filters.Limit = clampDiagnosticLimit(limit)

	offsetStr := query.Get("offset")
	if offsetStr != "" {
		if o, err := strconv.Atoi(offsetStr); err == nil && o >= 0 {
			filters.Offset = o
		}
	}

	logs, err := h.logger.QueryByFilters(r.Context(), filters)
	if err != nil {
		writeInternalTextErr(w, "internal error (see server logs)", err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"count":   len(logs),
		"results": logs,
		"filters": filters,
	})
}
