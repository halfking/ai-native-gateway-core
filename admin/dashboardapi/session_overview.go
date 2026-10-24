// Package dashboardapi - session_overview.go
// 会话总览 API：提供首页 Dashboard 所需的完整会话数据
package dashboardapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"
)

// dashboardStatsStageError carries which parallel stats stage failed so the
// error answer keeps the per-stage detail of the pre-parallel serial pipeline
// (same shape as admin.statsStageError in tenants.go).
type dashboardStatsStageError struct {
	stage string
	err   error
}

func (e dashboardStatsStageError) Error() string { return e.stage + ": " + e.err.Error() }

// SessionOverviewHandler 会话总览 Handler
type SessionOverviewHandler struct {
	db       *pgxpool.Pool
	executor interface{} // 可选：moduleexec.Executor（用于缓存）
}

// NewSessionOverviewHandler 创建 Handler
func NewSessionOverviewHandler(db *pgxpool.Pool, executor interface{}) *SessionOverviewHandler {
	return &SessionOverviewHandler{
		db:       db,
		executor: executor,
	}
}

// SessionOverviewResponse 会话总览响应数据
type SessionOverviewResponse struct {
	// 核心指标
	TotalSessions     int `json:"total_sessions"`
	ActiveSessions    int `json:"active_sessions"`
	NewSessions24h    int `json:"new_sessions_24h"`
	ClosedSessions24h int `json:"closed_sessions_24h"`

	// 健康度分布
	HealthDistribution HealthDistribution `json:"health_distribution"`

	// 合规状态
	ComplianceStats ComplianceStats `json:"compliance_stats"`

	// 成本统计
	CostStats CostStats `json:"cost_stats"`

	// 模型使用
	ModelUsage []ModelUsageItem `json:"model_usage"`

	// Top 排行
	TopClients []ClientRankItem `json:"top_clients"`
	TopTasks   []TaskRankItem   `json:"top_tasks"`

	// 趋势数据
	CostTrend    []CostTrendPoint    `json:"cost_trend"`
	SessionTrend []SessionTrendPoint `json:"session_trend"`

	// 时间戳
	GeneratedAt time.Time `json:"generated_at"`
	PeriodStart time.Time `json:"period_start"`
	PeriodEnd   time.Time `json:"period_end"`
}

// HealthDistribution 健康度分布
type HealthDistribution struct {
	Total int `json:"total"`
	A     int `json:"a"` // 90-100
	B     int `json:"b"` // 75-89
	C     int `json:"c"` // 60-74
	D     int `json:"d"` // 40-59
	F     int `json:"f"` // 0-39

	// 百分比（自动计算）
	APercent float64 `json:"a_percent"`
	BPercent float64 `json:"b_percent"`
	CPercent float64 `json:"c_percent"`
	DPercent float64 `json:"d_percent"`
	FPercent float64 `json:"f_percent"`

	AvgScore float64 `json:"avg_score"`
}

// ComplianceStats 合规统计
type ComplianceStats struct {
	Total           int     `json:"total"`
	Compliant       int     `json:"compliant"`
	Warning         int     `json:"warning"`
	Violation       int     `json:"violation"`
	PromptInjection int     `json:"prompt_injection_detected"`
	PIIDetected     int     `json:"pii_detected"`
	ToxicOutput     int     `json:"toxic_output_detected"`
	ComplianceRate  float64 `json:"compliance_rate"`
}

// CostStats 成本统计（与 days 窗口对齐的会话汇总）
type CostStats struct {
	TotalCostUSD      float64 `json:"total_cost_usd"`
	AvgCostPerSession float64 `json:"avg_cost_per_session"`
	AvgCostPerRequest float64 `json:"avg_cost_per_request"`
	MaxCostSession    float64 `json:"max_cost_session"`
	InputCostUSD      float64 `json:"input_cost_usd"`
	OutputCostUSD     float64 `json:"output_cost_usd"`
	CostGrowthPct     float64 `json:"cost_growth_pct"` // 相比上一周期
	TotalRequests     int64   `json:"total_requests"`  // SUM(request_count) in period
	AvgLatencyMs      float64 `json:"avg_latency_ms"`  // AVG(avg_latency_ms) in period
}

// ModelUsageItem 模型使用项
type ModelUsageItem struct {
	Model        string  `json:"model"`
	SessionCount int     `json:"session_count"`
	RequestCount int     `json:"request_count"`
	TotalCost    float64 `json:"total_cost"`
	AvgLatencyMs float64 `json:"avg_latency_ms"`
	SuccessRate  float64 `json:"success_rate"`
}

// ClientRankItem 客户端排行
type ClientRankItem struct {
	ClientID     string    `json:"client_id"`
	SessionCount int       `json:"session_count"`
	TotalCost    float64   `json:"total_cost"`
	AvgHealth    *int      `json:"avg_health,omitempty"`
	LastActivity time.Time `json:"last_activity"`
}

// TaskRankItem 任务排行
type TaskRankItem struct {
	TaskID       string    `json:"task_id"`
	SessionCount int       `json:"session_count"`
	TotalCost    float64   `json:"total_cost"`
	AvgHealth    *int      `json:"avg_health,omitempty"`
	LastActivity time.Time `json:"last_activity"`
}

// CostTrendPoint 成本趋势点
type CostTrendPoint struct {
	Date     string  `json:"date"` // YYYY-MM-DD
	Cost     float64 `json:"cost"`
	Sessions int     `json:"sessions"`
	Requests int     `json:"requests"`
}

// SessionTrendPoint 会话趋势点
type SessionTrendPoint struct {
	Date        string `json:"date"`
	NewSessions int    `json:"new_sessions"`
	ActiveCount int    `json:"active_count"`
	ClosedCount int    `json:"closed_count"`
}

// HandleSessionOverview 处理会话总览请求
//
// GET /api/admin/dashboard/session-overview
//
// 查询参数：
//   - days: 时间范围（1/7/30/90，默认 7）
//   - tenant_id: 租户 ID（可选，超级管理员可用）
//   - refresh: 是否强制刷新缓存（可选）
func (h *SessionOverviewHandler) HandleSessionOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErrorJSON(w, http.StatusMethodNotAllowed, ErrCodeInvalidParam, "method not allowed", "")
		return
	}

	startTime := time.Now()
	apiStatus := "success"
	defer func() {
		recordAPIRequest("session-overview", apiStatus, time.Since(startTime))
	}()
	params, _, ok := prepareDashboardRequest(w, r, h.db)
	if !ok {
		return
	}

	ctx, cancel := GetRequestContext(r, 30*time.Second)
	defer cancel()

	// ── 六段聚合并行（2026-10-27 巡检轮）──
	// 2026-10-15 可靠性轮：9 段串行共用 30s ctx，session_summaries 73 万行 seq scan，
	// 每段 0.6~2.5s ⇒ 串行 4~12s，负载高时 30s 顶穿报 500 ⇒ 改成 errgroup 并行，墙钟 = max(段)。
	// 2026-10-27 巡检轮（实测）：session_summaries 是 **86 万行普通表**（无分区、无
	// first_request_at 索引），每条腿都是一次 Parallel Seq Scan（50,603 buffers）；
	// 9 段并发 ⇒ 27 个并行 worker 抢 max_worker_processes=8 的槽 ⇒ 互相抢 CPU。
	// 合并口径不变的四条标量腿（总会话/健康/合规/成本 → 1 次扫描）与两条趋势腿
	// （成本趋势/会话趋势 → 1 次扫描）⇒ 9 段降到 6 段，session_summaries 扫描 6→3 次。
	// **墙钟仍由两条 join 腿（top clients / top tasks）决定** —— 它们各自要把
	// 90 万行的 session_dim 整表哈希，SQL 层候选已被实测否证（见审计档）。
	// 错误语义保持：哪段死都带段名报 500（dashboardStatsStageError，
	// 类型定义在包级，见 SessionOverviewHandler 前）。
	var (
		aggregates *sessionSummaryAggregates
		prevCost   float64
		modelUsage []ModelUsageItem
		topClients []ClientRankItem
		topTasks   []TaskRankItem
		costTrend  []CostTrendPoint
		sessTrend  []SessionTrendPoint
	)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		var err error
		if aggregates, err = h.querySessionSummaryAggregates(gctx, params); err != nil {
			return dashboardStatsStageError{"session summary aggregates", err}
		}
		return nil
	})
	g.Go(func() error {
		// 上一周期成本：窗口与「成本统计」腿不同（2×days ~ days），无法并进同一次扫描。
		// 原实现在成本腿内部顺带查，**错误被忽略**（不阻断整段）⇒ 这里保持同一语义。
		prevCost = h.queryPrevPeriodCost(gctx, params)
		return nil
	})
	g.Go(func() error {
		var err error
		if modelUsage, err = h.queryModelUsage(gctx, params); err != nil {
			return dashboardStatsStageError{"model usage", err}
		}
		return nil
	})
	g.Go(func() error {
		var err error
		if topClients, err = h.queryTopClients(gctx, params, 5); err != nil {
			return dashboardStatsStageError{"top clients", err}
		}
		return nil
	})
	g.Go(func() error {
		var err error
		if topTasks, err = h.queryTopTasks(gctx, params, 5); err != nil {
			return dashboardStatsStageError{"top tasks", err}
		}
		return nil
	})
	g.Go(func() error {
		var err error
		costTrend, sessTrend, err = h.querySessionTrends(gctx, params)
		if err != nil {
			return dashboardStatsStageError{"session trends", err}
		}
		return nil
	})
	if err := g.Wait(); err != nil {
		apiStatus = "error"
		var se dashboardStatsStageError
		if errors.As(err, &se) {
			writeErrorJSON(w, http.StatusInternalServerError, ErrCodeDatabaseError, "failed to query "+se.stage, se.err.Error())
			return
		}
		writeErrorJSON(w, http.StatusInternalServerError, ErrCodeDatabaseError, "failed to query session overview", err.Error())
		return
	}
	totalStats := aggregates.total
	healthDist := aggregates.health
	compliance := aggregates.compliance
	costStats := aggregates.cost
	// 成本增长率：与原实现同口径（上一周期成本为 0 时不计算）
	if prevCost > 0 {
		costStats.CostGrowthPct = (costStats.TotalCostUSD - prevCost) * 100 / prevCost
	}
	sessionTrend := sessTrend

	// 构建响应
	now := time.Now()
	periodEnd := now
	periodStart := now.AddDate(0, 0, -params.Days)

	resp := SessionOverviewResponse{
		TotalSessions:      totalStats.Total,
		ActiveSessions:     totalStats.Active,
		NewSessions24h:     totalStats.New24h,
		ClosedSessions24h:  totalStats.Closed24h,
		HealthDistribution: *healthDist,
		ComplianceStats:    *compliance,
		CostStats:          *costStats,
		ModelUsage:         modelUsage,
		TopClients:         topClients,
		TopTasks:           topTasks,
		CostTrend:          costTrend,
		SessionTrend:       sessionTrend,
		GeneratedAt:        now,
		PeriodStart:        periodStart,
		PeriodEnd:          periodEnd,
	}

	metadata := &Metadata{
		GeneratedAt: now,
		TookMs:      time.Since(startTime).Milliseconds(),
		CacheHit:    false,
	}

	// 写入响应
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "private, max-age=60") // 客户端缓存 60 秒
	w.WriteHeader(http.StatusOK)
	writeSuccessJSON(w, resp, metadata)
}

// ────────────────────────────────────────────────────────────────
// 内部数据结构
// ────────────────────────────────────────────────────────────────

type totalStatsInternal struct {
	Total     int
	Active    int
	New24h    int
	Closed24h int
}

// sessionSummaryAggregates 是一次「session_summaries 扫描」里同时聚合出来的四组标量。
//
// 2026-10-27 巡检轮引入：原先「总会话数 / 健康分布 / 合规统计 / 成本统计」是 4 条
// 独立 SELECT，四条的 WHERE 子句逐字相同（可选 tenant_id + first_request_at 窗口），
// 聚合表达式也逐条相同来源 ⇒ **合并后结果与旧实现逐值相同**。
type sessionSummaryAggregates struct {
	total      *totalStatsInternal
	health     *HealthDistribution
	compliance *ComplianceStats
	cost       *CostStats
}

// querySessionSummaryAggregates 把四条标量腿合并成一次扫描。
//
// 为什么值得合并（2026-10-27 生产实测）：
//   - session_summaries 是 **86 万行的普通表**（非分区，且**没有 first_request_at 索引**），
//     每条腿都是一次 Parallel Seq Scan（50,603 shared hit / 760,110 行被过滤丢弃）。
//   - 加 first_request_at 索引被**实测否证**（两条腿反而更慢，见审计档）⇒ 杠杆不在索引，
//     在「同一张表被反复扫」。
//   - 端点原本 9 段并发 ⇒ 27 个并行 worker 抢 max_worker_processes=8 的槽 ⇒ 互相抢 CPU。
//     合并后这段扫描从 4 次降到 1 次。
//
// 口径等价性：WHERE 与每条聚合表达式都从原实现逐字搬来；健康分布与合规统计各自的
// COUNT(*) 与「总会话数」的 COUNT(*) 同源（同表同窗）⇒ 直接复用同一个 total。
func (h *SessionOverviewHandler) querySessionSummaryAggregates(ctx context.Context, params QueryParams) (*sessionSummaryAggregates, error) {
	where := []string{}
	args := []interface{}{}
	argIdx := 1

	if params.TenantID != "" {
		where = append(where, fmt.Sprintf("tenant_id = $%d", argIdx))
		args = append(args, params.TenantID)
		argIdx++
	}
	where = append(where, fmt.Sprintf("first_request_at >= NOW() - INTERVAL '%d days'", params.Days))

	whereClause := "WHERE " + joinStrings(where, " AND ")

	query := fmt.Sprintf(`
		SELECT
			COUNT(*) as total,
			COUNT(*) FILTER (WHERE last_request_at >= NOW() - INTERVAL '24 hours') as active,
			COUNT(*) FILTER (WHERE first_request_at >= NOW() - INTERVAL '24 hours') as new_24h,
			COUNT(*) FILTER (WHERE last_request_at < NOW() - INTERVAL '24 hours' AND first_request_at < NOW() - INTERVAL '24 hours') as closed_24h,
			COUNT(*) FILTER (WHERE health_grade = 'A') as health_a,
			COUNT(*) FILTER (WHERE health_grade = 'B') as health_b,
			COUNT(*) FILTER (WHERE health_grade = 'C') as health_c,
			COUNT(*) FILTER (WHERE health_grade = 'D') as health_d,
			COUNT(*) FILTER (WHERE health_grade = 'F') as health_f,
			COALESCE(AVG(health_score), 0) as avg_score,
			COUNT(*) FILTER (WHERE compliance_status = 'compliant' OR compliance_status IS NULL) as compliant,
			COUNT(*) FILTER (WHERE compliance_status = 'warning') as warning,
			COUNT(*) FILTER (WHERE compliance_status = 'violation') as violation,
			COUNT(*) FILTER (WHERE prompt_injection_detected = true) as prompt_injection,
			COUNT(*) FILTER (WHERE pii_detected = true) as pii,
			COUNT(*) FILTER (WHERE toxic_output_detected = true) as toxic,
			COALESCE(SUM(total_cost_usd), 0) as total_cost,
			COALESCE(AVG(total_cost_usd), 0) as avg_cost,
			COALESCE(MAX(total_cost_usd), 0) as max_cost,
			COALESCE(SUM(input_cost_usd), 0) as input_cost,
			COALESCE(SUM(output_cost_usd), 0) as output_cost,
			COALESCE(SUM(request_count), 0) as total_requests,
			COALESCE(AVG(avg_latency_ms), 0) as avg_latency
		FROM session_summaries
		%s
	`, whereClause)

	agg := &sessionSummaryAggregates{
		total:      &totalStatsInternal{},
		health:     &HealthDistribution{},
		compliance: &ComplianceStats{},
		cost:       &CostStats{},
	}
	err := h.db.QueryRow(ctx, query, args...).Scan(
		&agg.total.Total,
		&agg.total.Active,
		&agg.total.New24h,
		&agg.total.Closed24h,
		&agg.health.A, &agg.health.B, &agg.health.C, &agg.health.D, &agg.health.F,
		&agg.health.AvgScore,
		&agg.compliance.Compliant,
		&agg.compliance.Warning,
		&agg.compliance.Violation,
		&agg.compliance.PromptInjection,
		&agg.compliance.PIIDetected,
		&agg.compliance.ToxicOutput,
		&agg.cost.TotalCostUSD,
		&agg.cost.AvgCostPerSession,
		&agg.cost.MaxCostSession,
		&agg.cost.InputCostUSD,
		&agg.cost.OutputCostUSD,
		&agg.cost.TotalRequests,
		&agg.cost.AvgLatencyMs,
	)
	if err != nil {
		return nil, err
	}

	// 健康分布与合规统计各自原本带一个 COUNT(*)：同表同窗 ⇒ 与「总会话数」同值。
	agg.health.Total = agg.total.Total
	agg.compliance.Total = agg.total.Total

	// 计算百分比
	if agg.health.Total > 0 {
		agg.health.APercent = float64(agg.health.A) * 100 / float64(agg.health.Total)
		agg.health.BPercent = float64(agg.health.B) * 100 / float64(agg.health.Total)
		agg.health.CPercent = float64(agg.health.C) * 100 / float64(agg.health.Total)
		agg.health.DPercent = float64(agg.health.D) * 100 / float64(agg.health.Total)
		agg.health.FPercent = float64(agg.health.F) * 100 / float64(agg.health.Total)
	}
	if agg.compliance.Total > 0 {
		agg.compliance.ComplianceRate = float64(agg.compliance.Compliant) * 100 / float64(agg.compliance.Total)
	}
	if agg.cost.TotalRequests > 0 {
		agg.cost.AvgCostPerRequest = agg.cost.TotalCostUSD / float64(agg.cost.TotalRequests)
	}

	return agg, nil
}

// queryPrevPeriodCost 取「上一周期」的成本合计（成本增长率的分母）。
//
// 与当前周期窗口不同（2×days ~ days）⇒ 无法并进同一次扫描，保持独立一条。
// 错误按原实现语义**忽略**（原代码 `_ = h.db.QueryRow(...).Scan(&prevTotalCost)`），
// 失败时返回 0 ⇒ 调用方不计算增长率。
func (h *SessionOverviewHandler) queryPrevPeriodCost(ctx context.Context, params QueryParams) float64 {
	where := []string{}
	args := []interface{}{}
	argIdx := 1

	if params.TenantID != "" {
		where = append(where, fmt.Sprintf("tenant_id = $%d", argIdx))
		args = append(args, params.TenantID)
		argIdx++
	}
	where = append(where, fmt.Sprintf("first_request_at >= NOW() - INTERVAL '%d days' AND first_request_at < NOW() - INTERVAL '%d days'", params.Days*2, params.Days))
	whereClause := "WHERE " + joinStrings(where, " AND ")

	query := fmt.Sprintf(`
		SELECT COALESCE(SUM(total_cost_usd), 0)
		FROM session_summaries
		%s
	`, whereClause)

	var prevTotalCost float64
	_ = h.db.QueryRow(ctx, query, args...).Scan(&prevTotalCost)
	return prevTotalCost
}

func (h *SessionOverviewHandler) queryModelUsage(ctx context.Context, params QueryParams) ([]ModelUsageItem, error) {
	where := []string{}
	args := []interface{}{}
	argIdx := 1

	if params.TenantID != "" {
		where = append(where, fmt.Sprintf("tenant_id = $%d", argIdx))
		args = append(args, params.TenantID)
		argIdx++
	}

	where = append(where, fmt.Sprintf("first_request_at >= NOW() - INTERVAL '%d days'", params.Days))
	whereClause := "WHERE " + joinStrings(where, " AND ")

	query := fmt.Sprintf(`
		SELECT
			model,
			COUNT(*) as session_count,
			COALESCE(SUM(request_count), 0) as request_count,
			COALESCE(SUM(total_cost_usd), 0) as total_cost,
			COALESCE(AVG(avg_latency_ms), 0) as avg_latency,
			CASE WHEN SUM(request_count) > 0 
			     THEN SUM(success_count)::FLOAT / SUM(request_count) 
			     ELSE 0 END as success_rate
		FROM (
			SELECT 
				unnest(models_used) as model,
				tenant_id,
				first_request_at,
				request_count,
				total_cost_usd,
				avg_latency_ms,
				success_count
			FROM session_summaries
		) t
		%s
		GROUP BY model
		ORDER BY total_cost DESC
		LIMIT 10
	`, whereClause)

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]ModelUsageItem, 0)
	for rows.Next() {
		var item ModelUsageItem
		if err := rows.Scan(
			&item.Model,
			&item.SessionCount,
			&item.RequestCount,
			&item.TotalCost,
			&item.AvgLatencyMs,
			&item.SuccessRate,
		); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	// 迭代中断（连接断开/服务端错误）只会让 Next() 返回 false，不查
	// Err() 就把「读到一半断了」当成「读完」→ 静默 200 截断 Top-N 榜。
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return items, nil
}

func (h *SessionOverviewHandler) queryTopClients(ctx context.Context, params QueryParams, limit int) ([]ClientRankItem, error) {
	where := []string{}
	args := []interface{}{}
	argIdx := 1

	where = append(where, fmt.Sprintf("ss.first_request_at >= NOW() - INTERVAL '%d days'", params.Days))
	if params.TenantID != "" {
		where = append(where, fmt.Sprintf("ss.tenant_id = $%d", argIdx))
		args = append(args, params.TenantID)
		argIdx++
	}
	whereClause := "WHERE " + joinStrings(where, " AND ")

	query := fmt.Sprintf(`
		SELECT
			COALESCE(NULLIF(TRIM(sd.client_id), ''), NULLIF(ss.client_models[1], ''), 'unknown') as client_id,
			COUNT(*) as session_count,
			COALESCE(SUM(ss.total_cost_usd), 0) as total_cost,
			MAX(ss.last_request_at) as last_activity
		FROM session_summaries ss
		LEFT JOIN session_dim sd ON sd.gw_session_id = ss.session_key
		%s
		GROUP BY COALESCE(NULLIF(TRIM(sd.client_id), ''), NULLIF(ss.client_models[1], ''), 'unknown')
		ORDER BY total_cost DESC
		LIMIT $%d
	`, whereClause, argIdx)
	args = append(args, limit)

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil && isMissingSessionDim(err) {
		query = fmt.Sprintf(`
			SELECT
				COALESCE(NULLIF(ss.client_models[1], ''), 'unknown') as client_id,
				COUNT(*) as session_count,
				COALESCE(SUM(ss.total_cost_usd), 0) as total_cost,
				MAX(ss.last_request_at) as last_activity
			FROM session_summaries ss
			%s
			GROUP BY COALESCE(NULLIF(ss.client_models[1], ''), 'unknown')
			ORDER BY total_cost DESC
			LIMIT $%d
		`, whereClause, argIdx)
		rows, err = h.db.Query(ctx, query, args...)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]ClientRankItem, 0)
	for rows.Next() {
		var item ClientRankItem
		if err := rows.Scan(&item.ClientID, &item.SessionCount, &item.TotalCost, &item.LastActivity); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func (h *SessionOverviewHandler) queryTopTasks(ctx context.Context, params QueryParams, limit int) ([]TaskRankItem, error) {
	where := []string{}
	args := []interface{}{}
	argIdx := 1

	where = append(where, fmt.Sprintf("ss.first_request_at >= NOW() - INTERVAL '%d days'", params.Days))
	if params.TenantID != "" {
		where = append(where, fmt.Sprintf("ss.tenant_id = $%d", argIdx))
		args = append(args, params.TenantID)
		argIdx++
	}
	whereClause := "WHERE " + joinStrings(where, " AND ")

	query := fmt.Sprintf(`
		SELECT
			COALESCE(NULLIF(TRIM(sd.task_id), ''), 'unknown') as task_id,
			COUNT(*) as session_count,
			COALESCE(SUM(ss.total_cost_usd), 0) as total_cost,
			MAX(ss.last_request_at) as last_activity
		FROM session_summaries ss
		LEFT JOIN session_dim sd ON sd.gw_session_id = ss.session_key
		%s
		GROUP BY COALESCE(NULLIF(TRIM(sd.task_id), ''), 'unknown')
		ORDER BY session_count DESC
		LIMIT $%d
	`, whereClause, argIdx)
	args = append(args, limit)

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil && isMissingSessionDim(err) {
		return []TaskRankItem{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]TaskRankItem, 0)
	for rows.Next() {
		var item TaskRankItem
		if err := rows.Scan(&item.TaskID, &item.SessionCount, &item.TotalCost, &item.LastActivity); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

// querySessionTrends 把「成本趋势」与「会话趋势」两条腿合并成一次扫描。
//
// 2026-10-27 巡检轮引入：两条腿的 WHERE 相同、GROUP BY 相同（DATE(first_request_at)）、
// ORDER BY 相同 ⇒ **一次扫描同时产出两个切片，行数与顺序逐行一致**。
// 聚合表达式逐条从原实现搬来（含原会话趋势里那个恒真的 FILTER，它在原 WHERE 下
// 必然为真，但保留原样以确保结果与旧实现逐值相同）。
func (h *SessionOverviewHandler) querySessionTrends(ctx context.Context, params QueryParams) ([]CostTrendPoint, []SessionTrendPoint, error) {
	where := []string{}
	args := []interface{}{}
	argIdx := 1

	if params.TenantID != "" {
		where = append(where, fmt.Sprintf("tenant_id = $%d", argIdx))
		args = append(args, params.TenantID)
		argIdx++
	}
	where = append(where, fmt.Sprintf("first_request_at >= NOW() - INTERVAL '%d days'", params.Days))
	whereClause := "WHERE " + joinStrings(where, " AND ")

	query := fmt.Sprintf(`
		SELECT
			DATE(first_request_at) as date,
			COALESCE(SUM(total_cost_usd), 0) as cost,
			COUNT(*) as sessions,
			COALESCE(SUM(request_count), 0) as requests,
			COUNT(*) FILTER (WHERE first_request_at >= NOW() - INTERVAL '%d days') as new_sessions,
			COUNT(*) FILTER (WHERE last_request_at >= NOW() - INTERVAL '24 hours') as active_count,
			COUNT(*) FILTER (WHERE last_request_at < NOW() - INTERVAL '24 hours' AND first_request_at < NOW() - INTERVAL '24 hours') as closed_count
		FROM session_summaries
		%s
		GROUP BY DATE(first_request_at)
		ORDER BY date
	`, params.Days, whereClause)

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	costItems := make([]CostTrendPoint, 0)
	sessItems := make([]SessionTrendPoint, 0)
	for rows.Next() {
		var date string
		var cost CostTrendPoint
		var sess SessionTrendPoint
		if err := rows.Scan(
			&date,
			&cost.Cost, &cost.Sessions, &cost.Requests,
			&sess.NewSessions, &sess.ActiveCount, &sess.ClosedCount,
		); err != nil {
			return nil, nil, err
		}
		cost.Date = date
		sess.Date = date
		costItems = append(costItems, cost)
		sessItems = append(sessItems, sess)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return costItems, sessItems, nil
}

// 注：joinStrings、writeSuccessJSON、writeErrorJSON 已定义在 types.go
