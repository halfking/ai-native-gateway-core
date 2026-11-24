// Package dashboardapi - module_stats.go
// 模块执行统计 API：查询各模块的执行次数、耗时、缓存命中率
//
// 2026-07-13: 增加 session_module_executions_hot 表缺失时的优雅降级，
// 复用 admin/dashboarddegrade 中的 helper。
//
// 2026-11-25 真源统一轮：本端点**不换源**，而是**显式声明不可用**。
//
// ★ 为什么声明而不是换源：`session_module_executions_hot` 的唯一写方
//
//	`moduleexec.Executor` 在 cmd/gateway **从未装配**（全仓只有
//	domains/moduleexec/executor.go:18 的注释里出现过 moduleexec.NewExecutor）
//	⇒ 该表在生产恒空 ⇒ 卡片上的 0 说的不是「没有模块被执行」，而是
//	「这个维度没有生产数据源」。
//
// ★ 请求级日志真源（request_logs_with_current_month）里**没有模块维度**
//
//	⇒ 换源只能把模块列抹成 unknown（失实字段）⇒ 不如显式声明不可用。
//
// ★ 窗口：本端点也消费 start_date/end_date（同族其它端点用 parseSessionWindow），
//
//	但**滚动路径逐字保留旧的 7 天字面量**（不变量①：现役调用方结果零变化）。
package dashboardapi

import (
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/admin/dashboarddegrade"
)

// moduleStatsUnavailableReason 是本端点声明「模块执行维度不可用」时给出的原因。
//
// ★ 为什么必须显式声明（缺席比假绿诚实）：session_module_executions_hot 的
//
//	唯一写方 moduleexec.Executor 在 cmd/gateway 从未装配 ⇒ 该表在生产恒空
//	⇒ 表格里的 0 不是「没有模块被执行」，而是「这个维度没有生产数据源」。
//
// ★ 为什么不去请求级日志真源上凑：那个真源没有模块维度（模块执行是网关内部
//
//	的执行单元，不落在请求行上）⇒ 换源只能把模块列抹成 unknown（失实字段）。
const moduleStatsUnavailableReason = "模块执行维度无生产数据源：session_module_executions_hot 的唯一写方 moduleexec.Executor 在 cmd/gateway 从未装配，该表在生产恒空；此处的 0 表示「无数据源」，不是「没有模块被执行」"

// ModuleStatsHandler 模块执行统计 Handler
type ModuleStatsHandler struct {
	db *pgxpool.Pool
}

// NewModuleStatsHandler 创建 Handler
func NewModuleStatsHandler(db *pgxpool.Pool) *ModuleStatsHandler {
	return &ModuleStatsHandler{db: db}
}

// ModuleStatsItem 模块统计项
type ModuleStatsItem struct {
	ModuleName      string     `json:"module_name"`
	ModuleVersion   string     `json:"module_version"`
	TotalExecutions int        `json:"total_executions"`
	SuccessCount    int        `json:"success_count"`
	FailedCount     int        `json:"failed_count"`
	SkippedCount    int        `json:"skipped_count"`
	AvgDurationMs   float64    `json:"avg_duration_ms"`
	P95DurationMs   float64    `json:"p95_duration_ms"`
	CacheHitRate    float64    `json:"cache_hit_rate"`
	UniqueSessions  int        `json:"unique_sessions"`
	LastExecutedAt  *time.Time `json:"last_executed_at,omitempty"`
}

// ModuleStatsResponse 模块统计响应
type ModuleStatsResponse struct {
	// DataAvailable 声明该维度在生产**有没有数据源**。
	// ★ 当前恒为 false（写方从未装配）⇒ 前端显示「未接入」而不是把 0 当数据。
	DataAvailable bool `json:"data_available"`
	// DataUnavailableReason 说明为什么不可用（data_available=false 时非空）。
	DataUnavailableReason string             `json:"data_unavailable_reason"`
	Modules               []ModuleStatsItem  `json:"modules"`
	Summary               ModuleStatsSummary `json:"summary"`
	PeriodStart           time.Time          `json:"period_start"`
	PeriodEnd             time.Time          `json:"period_end"`
}

// ModuleStatsSummary 模块统计摘要
type ModuleStatsSummary struct {
	TotalModules    int     `json:"total_modules"`
	TotalExecutions int     `json:"total_executions"`
	AvgCacheHitRate float64 `json:"avg_cache_hit_rate"`
	AvgDurationMs   float64 `json:"avg_duration_ms"`
}

// HandleModuleStats 处理模块执行统计请求
//
// GET /api/admin/dashboard/module-stats
func (h *ModuleStatsHandler) HandleModuleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErrorJSON(w, http.StatusMethodNotAllowed, ErrCodeInvalidParam, "method not allowed", "")
		return
	}

	startTime := time.Now()
	apiStatus := "success"
	defer func() {
		recordAPIRequest("module-stats", apiStatus, time.Since(startTime))
	}()

	params, _, ok := prepareDashboardRequest(w, r, h.db)
	if !ok {
		return
	}
	// 2026-11-25 窗口对齐轮：本端点也消费 start_date/end_date（同族其它端点
	// 用 parseSessionWindow）；解析失败回 400（参数错不是服务端故障）。
	win, werr := parseSessionWindow(r, params)
	if werr != nil {
		writeErrorJSON(w, http.StatusBadRequest, ErrCodeInvalidParam, werr.Error(), "")
		return
	}
	ctx, cancel := GetRequestContext(r, 15*time.Second)
	defer cancel()

	where := []string{}
	args := []interface{}{}
	argIdx := 1
	if win.calendar {
		// 显式日历窗：参数化边界（end 独占 ⇒ 末日整天被包含）。
		where = append(where, fmt.Sprintf("created_at >= $%d AND created_at < $%d", argIdx, argIdx+1))
		args = append(args, win.startUTC, win.endUTC)
		argIdx += 2
	} else {
		// ★ 不变量①：滚动路径**逐字**保留旧的 7 天字面量（不是 params.Days）
		//   ⇒ 现役调用方的查询计划与结果零变化。
		where = append(where, "created_at >= NOW() - INTERVAL '7 days'")
	}
	appendExecutionScope(&where, params, &args, &argIdx, "")

	query := fmt.Sprintf(`
		SELECT
			module_name,
			MAX(module_version) as module_version,
			COUNT(*) as total_executions,
			COUNT(*) FILTER (WHERE status = 'completed') as success_count,
			COUNT(*) FILTER (WHERE status = 'failed') as failed_count,
			COUNT(*) FILTER (WHERE status = 'skipped') as skipped_count,
			COALESCE(AVG(duration_ms) FILTER (WHERE status = 'completed'), 0) as avg_duration_ms,
			COALESCE(PERCENTILE_CONT(0.95) WITHIN GROUP (ORDER BY duration_ms) FILTER (WHERE status = 'completed'), 0) as p95_duration_ms,
			COUNT(DISTINCT gw_session_id) as unique_sessions,
			MAX(completed_at) as last_executed_at
			FROM session_module_executions_hot
			WHERE %s
			GROUP BY module_name
			ORDER BY total_executions DESC
		`, joinStrings(where, " AND "))

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		if dashboarddegrade.IsMissingRelationError(err) {
			view := dashboarddegrade.Report(nil, "moduleStats", err)
			apiStatus = "degraded"
			now := time.Now()
			writeSuccessJSON(w, ModuleStatsResponse{
				DataAvailable:         false,
				DataUnavailableReason: moduleStatsUnavailableReason,
				Modules:               []ModuleStatsItem{},
				Summary:               ModuleStatsSummary{},
				PeriodStart:           now.AddDate(0, 0, -7),
				PeriodEnd:             now,
			}, &Metadata{
				GeneratedAt: now,
				TookMs:      time.Since(startTime).Milliseconds(),
				Degraded:    true,
				MissingView: view,
				Hint:        "数据视图尚未初始化，请先执行数据聚合迁移",
			})
			return
		}
		apiStatus = "error"
		writeErrorJSON(w, http.StatusInternalServerError, ErrCodeDatabaseError, "failed to query module stats", err.Error())
		return
	}
	defer rows.Close()

	modules := make([]ModuleStatsItem, 0)
	totalExecs := 0
	totalCacheRate := 0.0
	totalDuration := 0.0

	for rows.Next() {
		var item ModuleStatsItem
		if err := rows.Scan(
			&item.ModuleName, &item.ModuleVersion,
			&item.TotalExecutions, &item.SuccessCount, &item.FailedCount, &item.SkippedCount,
			&item.AvgDurationMs, &item.P95DurationMs,
			&item.UniqueSessions, &item.LastExecutedAt,
		); err != nil {
			apiStatus = "error"
			writeErrorJSON(w, http.StatusInternalServerError, ErrCodeDatabaseError, "failed to scan module stats", err.Error())
			return
		}
		// 计算缓存命中率（skipped 表示缓存命中）
		if item.TotalExecutions > 0 {
			item.CacheHitRate = float64(item.SkippedCount) * 100 / float64(item.TotalExecutions)
		}
		modules = append(modules, item)
		totalExecs += item.TotalExecutions
		totalCacheRate += item.CacheHitRate
		totalDuration += item.AvgDurationMs
	}
	// 迭代中断（连接断开/服务端错误）只会让 Next() 返回 false。不查
	// Err() 就继续算 summary，会把「读了一半」当成「读完」→ 静默 200
	// 返回偏小的 module 统计（total_execs / cache 命中率一起偏）。
	if err := rows.Err(); err != nil {
		apiStatus = "error"
		writeErrorJSON(w, http.StatusInternalServerError, ErrCodeDatabaseError, "failed to iterate module stats rows", err.Error())
		return
	}

	summary := ModuleStatsSummary{
		TotalModules:    len(modules),
		TotalExecutions: totalExecs,
	}
	if len(modules) > 0 {
		summary.AvgCacheHitRate = totalCacheRate / float64(len(modules))
		summary.AvgDurationMs = totalDuration / float64(len(modules))
	}

	now := time.Now()
	periodStart, periodEnd := now.AddDate(0, 0, -7), now
	// calendar 模式下 period 边界反映真实窗口（前端跨面对拍直接可用）。
	if win.calendar {
		periodStart, periodEnd = win.startUTC, win.endUTC
	}
	resp := ModuleStatsResponse{
		// ★ 恒声明不可用：该维度的生产写方从未装配 ⇒ 0 不是数据。
		//   （一旦写方装配，须先裁决口径再把这里置 true。）
		DataAvailable:         false,
		DataUnavailableReason: moduleStatsUnavailableReason,
		Modules:               modules,
		Summary:               summary,
		PeriodStart:           periodStart,
		PeriodEnd:             periodEnd,
	}

	metadata := &Metadata{
		GeneratedAt: now,
		TookMs:      time.Since(startTime).Milliseconds(),
	}
	writeSuccessJSON(w, resp, metadata)
}
