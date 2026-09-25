package admin

// report_rollup.go —— 对账报表管理端读面 + Excel 导出 + 手动重跑
//（2026-09-25 对账报表落地轮，设计 docs/reconciliation/design-report-rollup.md §4）。
//
// 端点（全部 superAdmin——对帐数据含全租户内部计费，不随租户角色放行）：
//
//	GET  /api/admin/report-rollup/summary?start&end&view[&provider_id&tenant_id&model]
//	     区间汇总 JSON：总计 + 按供应商/租户/人员/模型/天 分组行。
//	     周/月报表 = 前端传对应区间；数据只来自 report_snapshots 日快照，
//	     不回扫 usage_facts。
//	GET  /api/admin/report-rollup/export?同上           → xlsx 双 sheet 下载
//	POST /api/admin/report-rollup/run {"date":"YYYY-MM-DD"} → 手动重跑单日聚合
//
// 快照缺失语义：区间内当日无快照行 = worker 未跑或当日无流量，summary 以
// snapshot_dates 透出覆盖日期，不视为错误（与 stats 的 STATS_NOT_MIGRATED
// 降级同思路：表缺席时返回 degraded 而非 500）。

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/reportrollup"
)

// SetReportRollupWorker 注入每日聚合 worker（手动重跑端点用）。
func (h *Handler) SetReportRollupWorker(w interface {
	RollupDateDetached(day time.Time) (reportrollup.RollupStats, error)
}) {
	h.reportRollupWorker = w
}

// handleReportRollup 路由分发（/api/admin/report-rollup/ 前缀）。
func (h *Handler) handleReportRollup(w http.ResponseWriter, r *http.Request) {
	// lite/无 DB 模式下 adminHandler 仍会创建（保 /api/auth/*），本组端点
	// 全部依赖 PG——按仓库约定在请求时 503，而非 nil pool panic
	//（对齐 bg/audit_trimmer.go 的显式 nil-pool 守卫先例，R65）。
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not available")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	switch {
	case strings.HasSuffix(r.URL.Path, "/summary"):
		h.handleReportRollupSummary(w, r)
	case strings.HasSuffix(r.URL.Path, "/export"):
		h.handleReportRollupExport(w, r)
	case strings.HasSuffix(r.URL.Path, "/run"):
		h.handleReportRollupRun(w, r)
	default:
		writeError(w, http.StatusNotFound, "unknown report-rollup resource")
	}
}

// reportRange 解析区间参数（YYYY-MM-DD 闭区间，UTC）。缺省 = 昨日往前 7 天
// （今日快照尚不存在——聚合语义是 T+1 凌晨出昨日报表）。
func reportRange(r *http.Request) (time.Time, time.Time, error) {
	yesterday := time.Now().UTC().AddDate(0, 0, -1)
	end := yesterday
	start := yesterday.AddDate(0, 0, -6)
	if raw := r.URL.Query().Get("start"); raw != "" {
		parsed, err := time.ParseInLocation("2006-01-02", raw, time.UTC)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid start (want YYYY-MM-DD): %w", err)
		}
		start = parsed
	}
	if raw := r.URL.Query().Get("end"); raw != "" {
		parsed, err := time.ParseInLocation("2006-01-02", raw, time.UTC)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid end (want YYYY-MM-DD): %w", err)
		}
		end = parsed
	}
	if end.Before(start) {
		return time.Time{}, time.Time{}, fmt.Errorf("end before start")
	}
	if end.Sub(start) > 366*24*time.Hour {
		return time.Time{}, time.Time{}, fmt.Errorf("range cannot exceed 366 days")
	}
	return start, end, nil
}

// reportViewFilter 解析 view 与可选维度过滤。
func reportViewFilter(r *http.Request) (reportrollup.View, reportrollup.RangeFilter, error) {
	view := reportrollup.View(strings.ToLower(strings.TrimSpace(r.URL.Query().Get("view"))))
	if view == "" {
		view = reportrollup.ViewProvider
	}
	if !view.Valid() {
		return "", reportrollup.RangeFilter{}, fmt.Errorf("view must be provider or internal")
	}
	var filter reportrollup.RangeFilter
	if raw := strings.TrimSpace(r.URL.Query().Get("provider_id")); raw != "" {
		pid, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return "", filter, fmt.Errorf("invalid provider_id: %w", err)
		}
		if view != reportrollup.ViewProvider {
			return "", filter, fmt.Errorf("provider_id filter is only valid with view=provider")
		}
		filter.ProviderID = &pid
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("tenant_id")); raw != "" {
		if view != reportrollup.ViewInternal {
			return "", filter, fmt.Errorf("tenant_id filter is only valid with view=internal")
		}
		filter.TenantID = raw
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("model")); raw != "" {
		filter.Model = raw
	}
	return view, filter, nil
}

// providerNames 拉供应商 id→display_name 映射（providers 缺表不致命）。
func (h *Handler) providerNames(ctx context.Context) map[int64]string {
	names := map[int64]string{}
	rows, err := h.db.Query(ctx, `SELECT id, display_name FROM providers`)
	if err != nil {
		return names
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err == nil {
			names[id] = name
		}
	}
	return names
}

func (h *Handler) handleReportRollupSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	start, end, err := reportRange(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	view, filter, err := reportViewFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	rep, err := reportrollup.BuildRangeReport(r.Context(), h.db, start, end, view, filter, h.providerNames(r.Context()))
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "report_snapshots") {
			writeJSON(w, http.StatusOK, map[string]any{"degraded": true, "error_code": "REPORT_SNAPSHOTS_NOT_MIGRATED"})
			return
		}
		slog.Error("report rollup summary failed", "error", err)
		writeError(w, http.StatusInternalServerError, "report summary failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"report": rep})
}

func (h *Handler) handleReportRollupExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	start, end, err := reportRange(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	view, filter, err := reportViewFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	rep, err := reportrollup.BuildRangeReport(r.Context(), h.db, start, end, view, filter, h.providerNames(r.Context()))
	if err != nil {
		// 与 summary 同款降级：report_snapshots 缺表（未迁移）时导出
		// 也返回 degraded JSON 而非 500（R65 对齐文件头降级语义）。
		if strings.Contains(strings.ToLower(err.Error()), "report_snapshots") {
			writeJSON(w, http.StatusOK, map[string]any{"degraded": true, "error_code": "REPORT_SNAPSHOTS_NOT_MIGRATED"})
			return
		}
		slog.Error("report rollup export query failed", "error", err)
		writeError(w, http.StatusInternalServerError, "report export query failed")
		return
	}
	xlsxBytes, err := reportrollup.BuildWorkbookBytes(rep)
	if err != nil {
		slog.Error("report rollup xlsx build failed", "error", err)
		writeError(w, http.StatusInternalServerError, "report xlsx build failed")
		return
	}
	filename := reportrollup.ExportFilename(view, start, end)
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	w.Header().Set("Content-Length", strconv.Itoa(len(xlsxBytes)))
	_, _ = w.Write(xlsxBytes)
}

func (h *Handler) handleReportRollupRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.reportRollupWorker == nil {
		writeError(w, http.StatusServiceUnavailable, "report rollup worker not wired")
		return
	}
	var req struct {
		Date string `json:"date"`
	}
	if err := readJSONRequired(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	day := time.Now().UTC().AddDate(0, 0, -1)
	if req.Date != "" {
		parsed, err := time.ParseInLocation("2006-01-02", req.Date, time.UTC)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid date (want YYYY-MM-DD)")
			return
		}
		day = parsed
	}
	// 与请求生命周期解耦：客户端断开不中断聚合（聚合走自带超时的
	// detached context，2026-09-25 审计轮修正）。
	stats, err := h.reportRollupWorker.RollupDateDetached(day)
	if err != nil {
		slog.Error("report rollup manual run failed", "date", day, "error", err)
		// R65：错误细节（可含 SQL/约束信息）只进日志，不透传响应体。
		writeError(w, http.StatusInternalServerError, "report rollup run failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"date":          day.Format("2006-01-02"),
		"rows_written":  stats.RowsWritten,
		"requests_seen": stats.RequestsSeen,
	})
}
