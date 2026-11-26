// Package dashboardapi - performance.go
// 性能指标 API：查询系统性能指标（延迟、吞吐量、资源使用）
//
// 2026-07-13: 增加 session_module_executions_hot 表缺失时的优雅降级
//
// 2026-11-25 真源统一轮：真源从 `session_module_executions_hot` 换成
// **请求级日志** `request_logs_with_current_month`（见
// request_log_facts_source.go 的背景与口径三件套）。
//
// ★ 为什么换：该表的唯一写方 `moduleexec.Executor` 在生产从未装配 ⇒ 恒 0
//
//	（把「没有数据」读成了「没有慢请求」）。
//
// ★ 口径随真源走：延迟列 duration_ms → latency_ms，窗口列 created_at → ts，
//
//	口径集合与看板同款（终态集全量 + 探测口径分面）。
package dashboardapi

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/admin/dashboarddegrade"
)

// PerformanceHandler 性能指标 Handler
type PerformanceHandler struct {
	db *pgxpool.Pool
}

// NewPerformanceHandler 创建 Handler
func NewPerformanceHandler(db *pgxpool.Pool) *PerformanceHandler {
	return &PerformanceHandler{db: db}
}

// PerformanceResponse 性能指标响应
type PerformanceResponse struct {
	// DataSource 披露本卡读的是哪一族真源（"request_logs"）——前端徽标跟随
	// 响应值，不跟随本地开关（缺席比假绿诚实）。
	DataSource string `json:"data_source"`
	// ProbeScope 披露探测口径（excluded / included / degraded_included）。
	ProbeScope  string              `json:"probe_scope"`
	Summary     PerformanceSummary  `json:"summary"`
	LatencyDist LatencyDistribution `json:"latency_distribution"`
	Throughput  []ThroughputPoint   `json:"throughput"`
	SlowQueries []SlowQueryItem     `json:"slow_queries"`
}

// PerformanceSummary 性能摘要
type PerformanceSummary struct {
	AvgLatencyMs  float64 `json:"avg_latency_ms"`
	P50LatencyMs  float64 `json:"p50_latency_ms"`
	P95LatencyMs  float64 `json:"p95_latency_ms"`
	P99LatencyMs  float64 `json:"p99_latency_ms"`
	MaxLatencyMs  float64 `json:"max_latency_ms"`
	TotalRequests int     `json:"total_requests"`
	AvgThroughput float64 `json:"avg_throughput_rps"`
}

// LatencyDistribution 延迟分布
type LatencyDistribution struct {
	Under100ms  int `json:"under_100ms"`
	Under500ms  int `json:"under_500ms"`
	Under1000ms int `json:"under_1000ms"`
	Under5000ms int `json:"under_5000ms"`
	Over5000ms  int `json:"over_5000ms"`
}

// ThroughputPoint 吞吐量点
type ThroughputPoint struct {
	Date         string  `json:"date"`
	RequestCount int     `json:"request_count"`
	AvgLatencyMs float64 `json:"avg_latency_ms"`
}

// SlowQueryItem 慢查询项
//
// ★ 2026-11-25 真源统一轮：三个字段的**语义随真源变**，JSON 名保持不变
//
//	（前端契约不动）：
//	- SessionKey ← gw_session_id（请求级日志视图**有**会话维度：canonical
//	  视图把 session_turns.session_id 投影成 gw_session_id；sys:% 会话为 NULL）。
//	- ModuleName ← 出站模型（★ 该视图没有模块维度 ⇒ 槽位改装「上游实际调用
//	  目标」，慢请求排查要的是「哪条链路慢」，不是网关内部的执行单元名）。
//	- ErrorMessage ← error_kind（★ 该视图没有 error_message 列）。
type SlowQueryItem struct {
	SessionKey   string    `json:"session_key"`
	ModuleName   string    `json:"module_name"`
	DurationMs   int       `json:"duration_ms"`
	ExecutedAt   time.Time `json:"executed_at"`
	ErrorMessage string    `json:"error_message,omitempty"`
}

// HandlePerformance 处理性能指标请求
//
// GET /api/admin/dashboard/performance
func (h *PerformanceHandler) HandlePerformance(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErrorJSON(w, http.StatusMethodNotAllowed, ErrCodeInvalidParam, "method not allowed", "")
		return
	}

	startTime := time.Now()
	apiStatus := "success"
	defer func() {
		recordAPIRequest("performance", apiStatus, time.Since(startTime))
	}()

	params, _, ok := prepareDashboardRequest(w, r, h.db)
	if !ok {
		return
	}
	// 2026-11-23 日期范围对齐轮：窗口解析与会话总览同尺（显式日历窗口优先）。
	win, werr := parseSessionWindow(r, params)
	if werr != nil {
		writeErrorJSON(w, http.StatusBadRequest, ErrCodeInvalidParam, werr.Error(), "")
		return
	}
	ctx, cancel := GetRequestContext(r, 15*time.Second)
	defer cancel()

	// 1. 性能摘要
	summary, err := h.queryPerformanceSummary(ctx, params, win)
	if err != nil {
		if requestLogFactsMissingRelation(err) {
			h.writeDegraded(w, startTime, "performance:summary", err, win)
			apiStatus = "degraded"
			return
		}
		apiStatus = "error"
		writeErrorJSON(w, http.StatusInternalServerError, ErrCodeDatabaseError, "failed to query performance summary", err.Error())
		return
	}

	// 2. 延迟分布
	latencyDist, err := h.queryLatencyDistribution(ctx, params, win)
	if err != nil {
		if requestLogFactsMissingRelation(err) {
			h.writeDegraded(w, startTime, "performance:latency", err, win)
			apiStatus = "degraded"
			return
		}
		apiStatus = "error"
		writeErrorJSON(w, http.StatusInternalServerError, ErrCodeDatabaseError, "failed to query latency distribution", err.Error())
		return
	}

	// 3. 吞吐量趋势
	throughput, err := h.queryThroughput(ctx, params, win)
	if err != nil {
		if requestLogFactsMissingRelation(err) {
			h.writeDegraded(w, startTime, "performance:throughput", err, win)
			apiStatus = "degraded"
			return
		}
		apiStatus = "error"
		writeErrorJSON(w, http.StatusInternalServerError, ErrCodeDatabaseError, "failed to query throughput", err.Error())
		return
	}

	// 4. 慢查询
	slowQueries, err := h.querySlowQueries(ctx, params, win)
	if err != nil {
		if requestLogFactsMissingRelation(err) {
			h.writeDegraded(w, startTime, "performance:slow", err, win)
			apiStatus = "degraded"
			return
		}
		apiStatus = "error"
		writeErrorJSON(w, http.StatusInternalServerError, ErrCodeDatabaseError, "failed to query slow queries", err.Error())
		return
	}

	resp := PerformanceResponse{
		DataSource:  requestLogFactsDataSource,
		ProbeScope:  probeScopeValue(win),
		Summary:     *summary,
		LatencyDist: *latencyDist,
		Throughput:  throughput,
		SlowQueries: slowQueries,
	}

	metadata := &Metadata{
		GeneratedAt: time.Now(),
		TookMs:      time.Since(startTime).Milliseconds(),
	}
	writeSuccessJSON(w, resp, metadata)
}

func (h *PerformanceHandler) queryPerformanceSummary(ctx context.Context, params QueryParams, win sessionWindow) (*PerformanceSummary, error) {
	summary := &PerformanceSummary{}
	// ★ 谓词一次成型（窗口 + 租户 + 终态 + 探测口径）。
	whereClause, args, _ := requestLogFactsWhere(win, params)

	// ★ 旧列 → 新列映射：duration_ms → latency_ms；created_at → ts（窗口列）。
	query := fmt.Sprintf(`
		SELECT
			COUNT(*) as total_requests,
			COALESCE(AVG(r.latency_ms), 0) as avg_latency,
			COALESCE(PERCENTILE_CONT(0.50) WITHIN GROUP (ORDER BY r.latency_ms), 0) as p50_latency,
			COALESCE(PERCENTILE_CONT(0.95) WITHIN GROUP (ORDER BY r.latency_ms), 0) as p95_latency,
			COALESCE(PERCENTILE_CONT(0.99) WITHIN GROUP (ORDER BY r.latency_ms), 0) as p99_latency,
			COALESCE(MAX(r.latency_ms), 0) as max_latency
		FROM request_logs_with_current_month r
		%s
	`, whereClause)

	err := h.db.QueryRow(ctx, query, args...).Scan(
		&summary.TotalRequests, &summary.AvgLatencyMs,
		&summary.P50LatencyMs, &summary.P95LatencyMs, &summary.P99LatencyMs, &summary.MaxLatencyMs,
	)
	if err != nil {
		return nil, err
	}

	// 计算平均吞吐量（请求/秒）
	if params.Days > 0 {
		summary.AvgThroughput = float64(summary.TotalRequests) / float64(params.Days*24*3600)
	}
	return summary, nil
}

func (h *PerformanceHandler) queryLatencyDistribution(ctx context.Context, params QueryParams, win sessionWindow) (*LatencyDistribution, error) {
	dist := &LatencyDistribution{}
	whereClause, args, _ := requestLogFactsWhere(win, params)

	// ★ 旧列 → 新列映射：duration_ms → latency_ms（分档边界逐字不变）。
	query := fmt.Sprintf(`
		SELECT
			COUNT(*) FILTER (WHERE r.latency_ms < 100) as under_100ms,
			COUNT(*) FILTER (WHERE r.latency_ms >= 100 AND r.latency_ms < 500) as under_500ms,
			COUNT(*) FILTER (WHERE r.latency_ms >= 500 AND r.latency_ms < 1000) as under_1000ms,
			COUNT(*) FILTER (WHERE r.latency_ms >= 1000 AND r.latency_ms < 5000) as under_5000ms,
			COUNT(*) FILTER (WHERE r.latency_ms >= 5000) as over_5000ms
		FROM request_logs_with_current_month r
		%s
	`, whereClause)

	err := h.db.QueryRow(ctx, query, args...).Scan(
		&dist.Under100ms, &dist.Under500ms, &dist.Under1000ms, &dist.Under5000ms, &dist.Over5000ms,
	)
	return dist, err
}

func (h *PerformanceHandler) queryThroughput(ctx context.Context, params QueryParams, win sessionWindow) ([]ThroughputPoint, error) {
	whereClause, args, argIdx := requestLogFactsWhere(win, params)

	// ★ 旧列 → 新列映射：created_at → ts（窗口列 / 日桶列同源）。
	dayExpr := win.dayExpr("r.ts", &args, &argIdx)

	query := `
		SELECT
			TO_CHAR(` + dayExpr + `, 'YYYY-MM-DD') as date,
			COUNT(*) as request_count,
			COALESCE(AVG(r.latency_ms), 0) as avg_latency
		FROM request_logs_with_current_month r
		` + whereClause + `
		GROUP BY ` + dayExpr + `
		ORDER BY date
	`

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]ThroughputPoint, 0)
	for rows.Next() {
		var item ThroughputPoint
		if err := rows.Scan(&item.Date, &item.RequestCount, &item.AvgLatencyMs); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func (h *PerformanceHandler) querySlowQueries(ctx context.Context, params QueryParams, win sessionWindow) ([]SlowQueryItem, error) {
	whereClause, args, _ := requestLogFactsWhere(win, params)
	// ★ 旧列 → 新列映射：duration_ms > 1000 → r.latency_ms > 1000（阈值不变）。
	whereClause += " AND r.latency_ms > 1000"

	// ★ 旧列 → 新列映射：
	//   gw_session_id → gw_session_id（★ canonical 视图**有**会话维度：它把
	//     session_turns.session_id 投影成 gw_session_id，见迁移 890 的 118 列
	//     契约 ⇒ 旧表的会话列在新真源上同名同义，逐字沿用；
	//     视图没有 session_id 这一列名，不要照抄基表 request_logs 的列名）。
	//   module_name → 出站模型（★ 该视图没有模块维度 ⇒ 槽位改装上游调用目标）。
	//   completed_at / created_at → ts（请求级日志只有一个时间轴：ts）。
	//   error_message → error_kind（★ 该视图没有 error_message 列）。
	query := fmt.Sprintf(`
		SELECT
			COALESCE(r.gw_session_id, '') as gw_session_id,
			COALESCE(NULLIF(NULLIF(TRIM(r.outbound_model), ''), '<unknown>'), NULLIF(NULLIF(TRIM(r.client_model), ''), '<unknown>'), 'unknown') as module_name,
			r.latency_ms,
			r.ts as executed_at,
			COALESCE(NULLIF(r.error_kind, ''), '') as error_message
		FROM request_logs_with_current_month r
		%s
		ORDER BY r.latency_ms DESC
		LIMIT 20
	`, whereClause)

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]SlowQueryItem, 0)
	for rows.Next() {
		var item SlowQueryItem
		if err := rows.Scan(&item.SessionKey, &item.ModuleName, &item.DurationMs, &item.ExecutedAt, &item.ErrorMessage); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

// writeDegraded 写一个"缺表降级"的 200 响应。仪表盘布局保持完整。
func (h *PerformanceHandler) writeDegraded(w http.ResponseWriter, startTime time.Time, op string, err error, win sessionWindow) {
	view := dashboarddegrade.ExtractRelationName(err)
	now := time.Now()
	writeSuccessJSON(w, PerformanceResponse{
		// ★ 降级路径**照样披露口径**：前端要能区分「这个面本来就没数据」
		//   与「真源视图缺失导致取不到数」。
		DataSource:  requestLogFactsDataSource,
		ProbeScope:  probeScopeValue(win),
		Summary:     PerformanceSummary{},
		LatencyDist: LatencyDistribution{},
		Throughput:  []ThroughputPoint{},
		SlowQueries: []SlowQueryItem{},
	}, &Metadata{
		GeneratedAt: now,
		TookMs:      time.Since(startTime).Milliseconds(),
		Degraded:    true,
		MissingView: view,
		Hint:        "数据视图尚未初始化，请先执行数据聚合迁移",
	})
}
