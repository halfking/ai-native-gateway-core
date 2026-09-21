package admin

// request_anomalies.go — 请求侧异常（reqprobe）管理 API（2026-09-21）。
//
// 与 response_format_anomalies（PG 表）并列的第二个数据面：上游 4xx 中
// "请求参数被拒 / 请求形态不匹配"的探测记录，存储是 Redis（Full）或内存
// （lite），与部署模式无关地暴露同一 API：
//
//	GET  /api/admin/request-anomalies            列表（day/provider/model/
//	                                             trigger/unresolved_only 过滤）
//	GET  /api/admin/request-anomalies/count      导航徽标计数
//	POST /api/admin/request-anomalies/{id}/resolve
//	POST /api/admin/request-anomalies/batch-resolve  按 ids 或按过滤条件批量解决
//
// store 未接线（请求路径不该走到，但防御）返回 503。

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/kaixuan/llm-gateway-go/internal/reqprobe"
)

// SetRequestAnomalyStore 注入 reqprobe 存储门面。nil 安全（路由保留，
// 请求时 503）。启动时由 cmd/gateway 按 Full(Redis)/lite(内存) 模式选择。
func (h *Handler) SetRequestAnomalyStore(c *reqprobe.Coordinator) {
	h.requestAnomalies = c
}

func (h *Handler) handleRequestAnomalies(w http.ResponseWriter, r *http.Request) {
	if h.requestAnomalies == nil || h.requestAnomalies.Store() == nil {
		writeError(w, http.StatusServiceUnavailable, "request anomaly store not configured")
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	limit := queryInt(r, "limit", 50)
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	offset := queryInt(r, "offset", 0)
	if offset < 0 {
		offset = 0
	}
	f := reqprobe.Filter{
		Day:            strings.TrimSpace(queryString(r, "day")),
		ProviderCode:   strings.TrimSpace(queryString(r, "provider")),
		Model:          strings.TrimSpace(queryString(r, "model")),
		Trigger:        strings.TrimSpace(queryString(r, "trigger")),
		UnresolvedOnly: queryBool(r, "unresolved_only"),
		Limit:          limit,
		Offset:         offset,
	}

	items, total, err := h.requestAnomalies.Store().List(r.Context(), f)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list query failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"anomalies": items,
		"count":     total,
		"limit":     limit,
		"offset":    offset,
	})
}

func (h *Handler) handleRequestAnomalyCount(w http.ResponseWriter, r *http.Request) {
	if h.requestAnomalies == nil || h.requestAnomalies.Store() == nil {
		writeError(w, http.StatusServiceUnavailable, "request anomaly store not configured")
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	counts, err := h.requestAnomalies.Store().Counts(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "count query failed")
		return
	}
	writeJSON(w, http.StatusOK, counts)
}

func (h *Handler) handleRequestAnomalyResolve(w http.ResponseWriter, r *http.Request, idRaw string) {
	if h.requestAnomalies == nil || h.requestAnomalies.Store() == nil {
		writeError(w, http.StatusServiceUnavailable, "request anomaly store not configured")
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	id, err := strconv.ParseInt(idRaw, 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid anomaly id")
		return
	}
	var body struct {
		ResolutionNotes string `json:"resolution_notes"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	n, err := h.requestAnomalies.Store().Resolve(r.Context(), []int64{id}, body.ResolutionNotes)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "resolve failed")
		return
	}
	if n == 0 {
		writeError(w, http.StatusNotFound, "anomaly not found or already resolved")
		return
	}
	h.requestAnomalies.InvalidateLearned()
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "request anomaly marked as resolved"})
}

func (h *Handler) handleRequestAnomalyBatchResolve(w http.ResponseWriter, r *http.Request) {
	if h.requestAnomalies == nil || h.requestAnomalies.Store() == nil {
		writeError(w, http.StatusServiceUnavailable, "request anomaly store not configured")
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body struct {
		IDs             []int64 `json:"ids"`
		Day             string  `json:"day"`
		Provider        string  `json:"provider"`
		Model           string  `json:"model"`
		Trigger         string  `json:"trigger"`
		AllUnresolved   bool    `json:"all_unresolved"`
		ResolutionNotes string  `json:"resolution_notes"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	var n int
	var err error
	switch {
	case len(body.IDs) > 0:
		n, err = h.requestAnomalies.Store().Resolve(r.Context(), body.IDs, body.ResolutionNotes)
	case body.AllUnresolved:
		// 按当前过滤条件批量解决（day/provider/model/trigger 可组合）。
		f := reqprobe.Filter{
			Day:            strings.TrimSpace(body.Day),
			ProviderCode:   strings.TrimSpace(body.Provider),
			Model:          strings.TrimSpace(body.Model),
			Trigger:        strings.TrimSpace(body.Trigger),
			UnresolvedOnly: true,
		}
		n, err = h.requestAnomalies.Store().ResolveFilter(r.Context(), f, body.ResolutionNotes)
	default:
		writeError(w, http.StatusBadRequest, "ids or all_unresolved required")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "batch resolve failed")
		return
	}
	h.requestAnomalies.InvalidateLearned()
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "resolved": n})
}

// handleRequestAnomalySubrouter 分发 /api/admin/request-anomalies/* 子路径。
func (h *Handler) handleRequestAnomalySubrouter(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/admin/request-anomalies/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	switch {
	case len(parts) == 1 && parts[0] == "count":
		h.handleRequestAnomalyCount(w, r)
	case len(parts) == 1 && parts[0] == "batch-resolve":
		h.handleRequestAnomalyBatchResolve(w, r)
	case len(parts) == 2 && parts[1] == "resolve":
		h.handleRequestAnomalyResolve(w, r, parts[0])
	default:
		http.NotFound(w, r)
	}
}
