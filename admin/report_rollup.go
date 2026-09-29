package admin

// report_rollup.go —— 对账报表管理端读面 + Excel 导出 + 手动重跑
//（2026-09-25 对账报表落地轮；2026-09-29 多维筛选轮重写读面）。
//
// 端点（全部 superAdmin——对帐数据含全租户内部计费，不随租户角色放行）：
//
//	GET  /api/admin/report-rollup/summary
//	     ?start&end&view[&detail]
//	     [&provider_id&credential_id&api_key_id&tenant_id&person&model]
//	     区间多维 JSON：总计 + 按天 + 按天×模型（图表）+ 六个维度各自的
//	     汇总；detail=daily 追加「按天 × 维度」明细行。
//	     数据只来自 report_snapshots 的最细粒度（grain）快照，不回扫
//	     usage_facts；六个维度可任意组合过滤，总计恒等于各分组之和。
//	GET  /api/admin/report-rollup/dimensions?start&end&view
//	     筛选栏候选（各维度在区间内实际出现过的取值 + 请求数），不受当前
//	     筛选影响——否则选中一个供应商后其它供应商会从下拉里消失。
//	GET  /api/admin/report-rollup/export?同上 → xlsx 四 sheet
//	     （汇总 / 按天 / 按天×模型 / 按天明细长表），永远取明细口径。
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
	case strings.HasSuffix(r.URL.Path, "/dimensions"):
		h.handleReportRollupDimensions(w, r)
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
//
// 六个维度（供应商 / 凭据 / 模型 / 租户 / 用户 / apikey）**两个视角通用**，
// 不再按视角交叉拒绝。旧实现按视角拒绝（provider_id 只在 provider 视角、
// tenant_id 只在 internal 视角）是边缘 scope 的能力限制遗留，不是业务语义——
// 对帐人同样会问「内部流量里哪些 apikey 打到了失败率高的模型」，现在 grain
// 最细粒度快照让这个问题有了正确答案。
func reportViewFilter(r *http.Request) (reportrollup.View, reportrollup.GrainFilter, error) {
	view := reportrollup.View(strings.ToLower(strings.TrimSpace(r.URL.Query().Get("view"))))
	if view == "" {
		view = reportrollup.ViewProvider
	}
	if !view.Valid() {
		return "", reportrollup.GrainFilter{}, fmt.Errorf("view must be provider or internal")
	}
	q := r.URL.Query()
	var filter reportrollup.GrainFilter
	for _, f := range []struct {
		param string
		dst   **int64
	}{
		{"provider_id", &filter.ProviderID},
		{"credential_id", &filter.CredentialID},
		{"api_key_id", &filter.APIKeyID},
	} {
		raw := strings.TrimSpace(q.Get(f.param))
		if raw == "" {
			continue
		}
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return "", filter, fmt.Errorf("invalid %s: %w", f.param, err)
		}
		*f.dst = &v
	}
	// 文本维度：原样透传（不做大小写折叠——租户键 / 用户名 / 模型名都是
	// 标识符而非自然语言，大小写折叠会把两个不同取值合并成一个）。
	filter.TenantID = strings.TrimSpace(q.Get("tenant_id"))
	filter.Person = strings.TrimSpace(q.Get("person"))
	filter.Model = strings.TrimSpace(q.Get("model"))
	return view, filter, nil
}

// reportDailyDetail 解析明细开关：明细模式额外返回「按天 × 维度」行
// （明细表格 + 明细导出的数据面）。缺省 false = 只要汇总。
func reportDailyDetail(r *http.Request) bool {
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get("detail"))) {
	case "1", "true", "yes", "daily":
		return true
	}
	return false
}

// dimensionNames 一次性拉齐三个 id 维度的展示名映射（缺表/失败均退化为
// 显示 id，不阻断报表）。
//
// api_keys 的展示名来自 key_alias（人工起的别名），为空时回落到 key_prefix。
// **不能写 name**：public.api_keys 没有 name 列，information_schema 里能查到
// 的那个 name 属于 orchestrator 下的另一张同名表——查错 schema 会以为列存在，
// 真库一跑就 42703。原写法 `SELECT id, name FROM api_keys` 恒报错，又因为
// idLabelMap 吞错误而**静默**退化成空 map，页面上 apikey 一列永远显示裸 id
// 且没有任何报错线索。
func (h *Handler) dimensionNames(ctx context.Context) reportrollup.Names {
	return reportrollup.Names{
		Providers:   h.providerNames(ctx),
		Credentials: h.idLabelMap(ctx, `SELECT id, label FROM credentials`),
		APIKeys: h.idLabelMap(ctx,
			`SELECT id, COALESCE(NULLIF(key_alias, ''), key_prefix) FROM api_keys`),
	}
}

// idLabelMap 执行一条 id→名称查询。查询失败会**记日志**再返回空 map
// （非致命，页面退化成显示 id）——但必须留痕：静默吞掉一条写错的 SQL 会让
// 整列名称恒空且没有任何线索，排查时只能靠翻代码。
func (h *Handler) idLabelMap(ctx context.Context, sqlText string) map[int64]string {
	out := map[int64]string{}
	rows, err := h.db.Query(ctx, sqlText)
	if err != nil {
		slog.Error("report rollup: load id→name map failed, falling back to raw ids",
			"error", err)
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var name *string
		if err := rows.Scan(&id, &name); err != nil {
			continue
		}
		if name != nil && *name != "" {
			out[id] = *name
		}
	}
	return out
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

// reportDegraded 判断错误是否属于「report_snapshots 缺表（未迁移）」降级。
func reportDegraded(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "report_snapshots")
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
	rep, err := reportrollup.BuildGrainReport(r.Context(), h.db, start, end, view, filter,
		h.dimensionNames(r.Context()), reportDailyDetail(r))
	if err != nil {
		if reportDegraded(err) {
			writeJSON(w, http.StatusOK, map[string]any{"degraded": true, "error_code": "REPORT_SNAPSHOTS_NOT_MIGRATED"})
			return
		}
		slog.Error("report rollup summary failed", "error", err)
		writeError(w, http.StatusInternalServerError, "report summary failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"report": rep})
}

// handleReportRollupDimensions 返回筛选栏的候选项：区间内各维度实际出现过的
// 取值 + 各自的请求数（前端按请求数降序，默认折叠小项）。
//
// 候选**不受当前筛选条件影响**（只看日期区间与视角），否则选中一个供应商后
// 其它供应商就从候选里消失，用户没法横向切换——这是筛选栏最常见的自锁坑。
func (h *Handler) handleReportRollupDimensions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	start, end, err := reportRange(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	view, _, err := reportViewFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	dims, err := reportrollup.LoadDimensionOptions(r.Context(), h.db, view, start, end)
	if err != nil {
		if reportDegraded(err) {
			writeJSON(w, http.StatusOK, map[string]any{"degraded": true, "error_code": "REPORT_SNAPSHOTS_NOT_MIGRATED"})
			return
		}
		slog.Error("report rollup dimensions failed", "error", err)
		writeError(w, http.StatusInternalServerError, "report dimensions failed")
		return
	}
	// 就地补展示名（不新增往返）。
	names := h.dimensionNames(r.Context())
	// 候选键是文本（同一维度里 provider/credential/apikey 都是数值 id，
	// 但模型/租户/用户是字符串，统一成 string 交给前端）；名称映射按
	// ParseInt 回查，查不到就留空由前端显示 key。
	fill := func(list []reportrollup.DimensionOption, names map[int64]string) {
		for i := range list {
			id, err := strconv.ParseInt(list[i].Key, 10, 64)
			if err != nil {
				continue
			}
			if n, ok := names[id]; ok {
				list[i].Name = n
			}
		}
	}
	fill(dims.Providers, names.Providers)
	fill(dims.Credentials, names.Credentials)
	fill(dims.APIKeys, names.APIKeys)
	writeJSON(w, http.StatusOK, map[string]any{"dimensions": dims})
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
	// ?group= 决定「按天×X」那张 sheet 按哪个维度拆。缺省 model（保持旧行为）。
	// 传错值直接 400 而不是静默回退到模型——交接出去的口径对不上是查不出来的
	// 那种错，只有在这里喊出来。
	group, err := reportrollup.ParseGrainGroup(r.URL.Query().Get("group"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// 导出永远取明细口径：汇总数据是明细的前端折叠，明细可再聚合而汇总
	// 不能展开——多导一层的成本远小于对账人拿到汇总却没法下钻的代价。
	rep, err := reportrollup.BuildGrainReport(r.Context(), h.db, start, end, view, filter,
		h.dimensionNames(r.Context()), true)
	if err != nil {
		// 与 summary 同款降级：report_snapshots 缺表（未迁移）时导出
		// 也返回 degraded JSON 而非 500（R65 对齐文件头降级语义）。
		if reportDegraded(err) {
			writeJSON(w, http.StatusOK, map[string]any{"degraded": true, "error_code": "REPORT_SNAPSHOTS_NOT_MIGRATED"})
			return
		}
		slog.Error("report rollup export query failed", "error", err)
		writeError(w, http.StatusInternalServerError, "report export query failed")
		return
	}
	xlsxBytes, err := reportrollup.BuildGrainWorkbookBytes(rep, group)
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
