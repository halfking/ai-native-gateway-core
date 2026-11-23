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
	"sync/atomic"
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

	// TurnsAlignment 对拍 session_summaries.request_count 合计与业务 turns 行数。
	// degraded=true 时前端必须显式提示，不得当假绿 KPI。
	TurnsAlignment *TurnsAlignment `json:"turns_alignment,omitempty"`

	// ── 2026-11-21 统计口径对齐轮新增 ──
	// ProbeScope 披露当前会话宇宙口径：excluded=已排除探测（缺省，与
	// /admin/session-analytics 同宇宙）；included=include_probes=1 全量宇宙
	// （历史行为）；degraded_included=宇宙依赖缺失（sessions/session_dim/
	// 判定函数未迁移）自动降级为全量——降级可见，不是静默。
	ProbeScope string `json:"probe_scope"`
	// WindowMode 披露窗口语义：rolling_days=NOW()-N 天（面板自有腿的历史
	// 语义）；calendar=上海日历日 [start,end)（与物化视图日桶同尺）。
	WindowMode string `json:"window_mode"`
	// Aligned 是与 /admin/session-analytics 总览页同一实现（domains/analytics
	// 单一真源）的对齐指标行；物化视图缺失时缺席（aligned_source=unavailable）。
	Aligned *AlignedBlock `json:"aligned,omitempty"`
	// AlignedSource 披露对齐块来源：user_session_aggregates=物化视图快照
	// （15 分钟一刷）；unavailable=缓存面未就绪（对齐行缺席）。
	AlignedSource string `json:"aligned_source,omitempty"`

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

	// 2026-11-21 统计口径对齐轮：窗口与探测口径解析（见 session_overview_alignment.go
	// 的三层对齐说明）。日期对非法 → 400（参数错不是服务端故障）。
	degradedFlag := &atomic.Bool{}
	win, werr := parseSessionWindow(r, params)
	if werr != nil {
		writeErrorJSON(w, http.StatusBadRequest, ErrCodeInvalidParam, werr.Error(), "")
		return
	}
	win.degraded = degradedFlag
	// 对齐指标行的窗口：显式 start_date/end_date 透传；days 预设折算成上海
	// 日历日（与会话分析总览页预设同一折算 ⇒ 同一预设两边窗口一致）。
	alignedStart, alignedEnd := win.startDate, win.endDate
	if alignedStart == "" {
		alignedStart, alignedEnd = shanghaiPresetRange(params.Days)
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
	// 2026-11-02：再加 turns 对拍腿（读 session_turns 族，不扫 session_summaries）⇒ 7 段。
	// 2026-11-21：再加对齐指标行腿（读物化视图 domains/analytics 单一真源，
	// soft-fail：缓存面未就绪时对齐块缺席）⇒ 8 段。
	// **墙钟仍由两条 join 腿（top clients / top tasks）决定** —— 它们各自要把
	// 90 万行的 session_dim 整表哈希，SQL 层候选已被实测否证（见审计档）。
	// 错误语义保持：哪段死都带段名报 500（dashboardStatsStageError，
	// 类型定义在包级，见 SessionOverviewHandler 前）；turns 对拍 soft-fail。
	var (
		aggregates   *sessionSummaryAggregates
		prevCost     float64
		modelUsage   []ModelUsageItem
		topClients   []ClientRankItem
		topTasks     []TaskRankItem
		costTrend    []CostTrendPoint
		sessTrend    []SessionTrendPoint
		turnsCount   int64
		turnsCountOK bool
		aligned      *AlignedBlock
		alignedErr   error
	)
	g, gctx := errgroup.WithContext(ctx)
	// 2026-11-21 线上实证（211 三跑）：五条 session_summaries 腿**串行**执行。
	//
	// 为什么放弃并行：acc-pg17 的 /dev/shm 只有 64M（docker 默认）。宇宙连接的
	// LEFT JOIN session_dim（~90 万行）每份哈希要吃数 MB 共享内存段；并行 2 腿
	// （改造前只有 topClients/topTasks join dim）没事，本轮 6 腿全 join ⇒ 并发
	// 共享内存需求 > 64M ⇒ SQLSTATE 53100「could not resize shared memory」
	// ⇒ 整个端点 500。子查询绑小（4b8658b7）只减小 probe 侧，build 侧（dim）
	// 不变 ⇒ 串行化后任一时刻至多一份哈希在途（与 994 物化视图刷新器同形，
	// 刷新器串行跑同一 join 一直健康）。串行的墙钟 = 五腿之和（窗口绑小后
	// 每腿秒级，30s ctx 预算内；端点带 60s 客户端缓存）。
	// 错误语义保持：每腿失败带段名（dashboardStatsStageError），第一错即返回。
	g.Go(func() error {
		var err error
		if aggregates, err = h.querySessionSummaryAggregates(gctx, params, win); err != nil {
			return dashboardStatsStageError{"session summary aggregates", err}
		}
		// 上一周期成本：窗口与「成本统计」腿不同（2×days ~ days），无法并进同一次扫描。
		// 原实现在成本腿内部顺带查，**错误被忽略**（不阻断整段）⇒ 这里保持同一语义。
		prevCost = h.queryPrevPeriodCost(gctx, params, win)
		if modelUsage, err = h.queryModelUsage(gctx, params, win); err != nil {
			return dashboardStatsStageError{"model usage", err}
		}
		if topClients, err = h.queryTopClients(gctx, params, win, 5); err != nil {
			return dashboardStatsStageError{"top clients", err}
		}
		if topTasks, err = h.queryTopTasks(gctx, params, win, 5); err != nil {
			return dashboardStatsStageError{"top tasks", err}
		}
		if costTrend, sessTrend, err = h.querySessionTrends(gctx, params, win); err != nil {
			return dashboardStatsStageError{"session trends", err}
		}
		return nil
	})
	// 对齐指标行腿（2026-11-21）：soft-fail —— 物化视图缺失/超时都不拖垮面板，
	// aligned 块缺席 + aligned_source=unavailable（缺席比假绿诚实）。
	g.Go(func() error {
		a, err := h.queryAlignedSnapshot(gctx, params.TenantID, alignedStart, alignedEnd)
		if err != nil {
			alignedErr = err
			return nil
		}
		aligned = a
		return nil
	})
	// Turns 对拍腿：失败不拖垮整段 overview（KPI 仍可用），但前端会看到
	// turns_alignment 缺席 —— 比假绿「对齐」更诚实。
	g.Go(func() error {
		n, err := h.queryBusinessTurnsCount(gctx, params)
		if err != nil {
			return nil
		}
		turnsCount = n
		turnsCountOK = true
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

	var alignment *TurnsAlignment
	if turnsCountOK && costStats != nil {
		a := evaluateTurnsAlignment(costStats.TotalRequests, turnsCount)
		alignment = &a
	}

	// 构建响应
	now := time.Now()
	periodEnd := now
	periodStart := now.AddDate(0, 0, -params.Days)
	// 2026-11-21 对齐轮：calendar 模式下 period 边界反映真实窗口（上海日历
	// 折 UTC 界），前端跨面对拍直接可用。
	if win.calendar {
		periodStart, periodEnd = win.startUTC, win.endUTC
	}
	// probe_scope：degraded_included（宇宙依赖缺失自动降级）> included（显式
	// 全量）> excluded（缺省，与会话分析同宇宙）。
	probeScope := "excluded"
	if degradedFlag.Load() {
		probeScope = "degraded_included"
	} else if win.includeProbes {
		probeScope = "included"
	}
	windowMode := "rolling_days"
	if win.calendar {
		windowMode = "calendar"
	}

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
		TurnsAlignment:     alignment,
		ProbeScope:         probeScope,
		WindowMode:         windowMode,
		Aligned:            aligned,
		GeneratedAt:        now,
		PeriodStart:        periodStart,
		PeriodEnd:          periodEnd,
	}
	if aligned != nil {
		resp.AlignedSource = "user_session_aggregates"
	} else if alignedErr != nil {
		resp.AlignedSource = "unavailable"
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
//
// 2026-11-21 对齐轮：缺省套上与 981/994 物化视图同一的会话宇宙（sessions +
// session_dim join + owner 非空 + is_probe + session_analytics_is_probe；片段
// 取自 domains/analytics，迁移侧有契约门对拍）⇒ 面板 KPI 与会话分析同一宇宙。
// include_probes=1 回到旧全量宇宙（历史行为）。宇宙依赖缺失（未迁移环境）⇒
// 全量宇宙降级重试 + 标记 degraded（响应 probe_scope=degraded_included）。
func (h *SessionOverviewHandler) querySessionSummaryAggregates(ctx context.Context, params QueryParams, win sessionWindow) (*sessionSummaryAggregates, error) {
	buildQuery := func(withUniverse bool) (string, []interface{}) {
		where := []string{}
		args := []interface{}{}
		argIdx := 1

		if params.TenantID != "" {
			where = append(where, fmt.Sprintf("tenant_id = $%d", argIdx))
			args = append(args, params.TenantID)
			argIdx++
		}
		where = append(where, win.clause("first_request_at", &args, &argIdx))
		// ★ 2026-11-21 线上实证（211 首跑 500：SQLSTATE 53100 /dev/shm 耗尽）：
		// 宇宙 join 直接挂在全表 ss（86 万行）上时，errgroup 多腿并发各建一份
		// 大哈希 ⇒ PG 并行共享内存超限。⇒ 把窗口/租户谓词**下推进子查询**先绑小
		// ss，再做宇宙连接（连接的 build 侧 = 绑小后的 ss ⇒ 共享内存有界）。
		// 与 994 物化视图行集逐行等价（谓词是合取项，连接键不变）。
		// 内层只放 ss.* 谓词；宇宙合取项引用 s./sd. 别名，必须留在**外层**。
		innerWhere := joinStrings(where, " AND ")
		outerWhere := ""
		var fromClause string
		var cte string
		if withUniverse {
			if u := universeWhere(win.includeProbes); u != "" {
				outerWhere = " WHERE " + u
			}
			var join string
			cte, join = universeJoinFor(win)
			fromClause = "FROM (SELECT * FROM session_summaries WHERE " + innerWhere + ") ss\n" + join + outerWhere
		} else {
			fromClause = "FROM session_summaries ss\nWHERE " + innerWhere
		}

		query := cte + `
		SELECT
			COUNT(*) as total,
			COUNT(*) FILTER (WHERE ss.last_request_at >= NOW() - INTERVAL '24 hours') as active,
			COUNT(*) FILTER (WHERE ss.first_request_at >= NOW() - INTERVAL '24 hours') as new_24h,
			COUNT(*) FILTER (WHERE ss.last_request_at < NOW() - INTERVAL '24 hours' AND ss.first_request_at < NOW() - INTERVAL '24 hours') as closed_24h,
			COUNT(*) FILTER (WHERE ss.health_grade = 'A') as health_a,
			COUNT(*) FILTER (WHERE ss.health_grade = 'B') as health_b,
			COUNT(*) FILTER (WHERE ss.health_grade = 'C') as health_c,
			COUNT(*) FILTER (WHERE ss.health_grade = 'D') as health_d,
			COUNT(*) FILTER (WHERE ss.health_grade = 'F') as health_f,
			COALESCE(AVG(ss.health_score), 0) as avg_score,
			COUNT(*) FILTER (WHERE ss.compliance_status = 'compliant' OR ss.compliance_status IS NULL) as compliant,
			COUNT(*) FILTER (WHERE ss.compliance_status = 'warning') as warning,
			COUNT(*) FILTER (WHERE ss.compliance_status = 'violation') as violation,
			COUNT(*) FILTER (WHERE ss.prompt_injection_detected = true) as prompt_injection,
			COUNT(*) FILTER (WHERE ss.pii_detected = true) as pii,
			COUNT(*) FILTER (WHERE ss.toxic_output_detected = true) as toxic,
			COALESCE(SUM(ss.total_cost_usd), 0) as total_cost,
			COALESCE(AVG(ss.total_cost_usd), 0) as avg_cost,
			COALESCE(MAX(ss.total_cost_usd), 0) as max_cost,
			COALESCE(SUM(ss.input_cost_usd), 0) as input_cost,
			COALESCE(SUM(ss.output_cost_usd), 0) as output_cost,
			COALESCE(SUM(ss.request_count), 0) as total_requests,
			COALESCE(AVG(ss.avg_latency_ms), 0) as avg_latency
		` + fromClause
		return query, args
	}

	// 缺省带宇宙；依赖缺失 → 全量降级重试（与 board 降级链同形状）。
	includeProbes := win.includeProbes
	query, args := buildQuery(!includeProbes)
	agg, err := h.scanSessionSummaryAggregates(ctx, query, args)
	if err != nil && isUniverseUnavailableErr(err) {
		win.markDegraded()
		query, args = buildQuery(false)
		agg, err = h.scanSessionSummaryAggregates(ctx, query, args)
	}
	return agg, err
}

// scanSessionSummaryAggregates 执行聚合扫描（列序 = 上面 SELECT 的固定顺序）。
func (h *SessionOverviewHandler) scanSessionSummaryAggregates(ctx context.Context, query string, args []interface{}) (*sessionSummaryAggregates, error) {
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
//
// 2026-11-21 对齐轮：随 querySessionSummaryAggregates 同步套会话宇宙（缺省）；
// calendar 模式下「上一周期」= [start-窗口长, start)（与 days 滚动窗的
// 2×days~days 语义等长对齐）。降级语义同主腿。
func (h *SessionOverviewHandler) queryPrevPeriodCost(ctx context.Context, params QueryParams, win sessionWindow) float64 {
	buildQuery := func(withUniverse bool) (string, []interface{}) {
		where := []string{}
		args := []interface{}{}
		argIdx := 1

		if params.TenantID != "" {
			where = append(where, fmt.Sprintf("tenant_id = $%d", argIdx))
			args = append(args, params.TenantID)
			argIdx++
		}
		// 上一周期窗口：rolling 模式逐字沿用 2×days~days；calendar 模式
		// = [start-窗口长, start)（与当前窗口等长）。
		where = append(where, win.prevClause("first_request_at", &args, &argIdx))
		// 与主腿同法：窗口/租户下推进子查询绑小 ss；宇宙合取留外层（53100 修复）。
		innerWhere := joinStrings(where, " AND ")
		var fromClause string
		var cte string
		if withUniverse {
			outerWhere := ""
			if u := universeWhere(win.includeProbes); u != "" {
				outerWhere = " WHERE " + u
			}
			var join string
			cte, join = universeJoinFor(win)
			fromClause = "FROM (SELECT * FROM session_summaries WHERE " + innerWhere + ") ss\n" + join + outerWhere
		} else {
			fromClause = "FROM session_summaries ss\nWHERE " + innerWhere
		}
		query := cte + `
		SELECT COALESCE(SUM(ss.total_cost_usd), 0)
		` + fromClause
		return query, args
	}

	includeProbes := win.includeProbes
	query, args := buildQuery(!includeProbes)
	var prevTotalCost float64
	if err := h.db.QueryRow(ctx, query, args...).Scan(&prevTotalCost); err != nil && isUniverseUnavailableErr(err) {
		win.markDegraded()
		query, args = buildQuery(false)
		_ = h.db.QueryRow(ctx, query, args...).Scan(&prevTotalCost)
	}
	return prevTotalCost
}

// 2026-11-21 对齐轮：子查询随主腿套会话宇宙（缺省排除探测）——模型榜里
// self-check/deployment-gate 的模型不再霸榜，与 session-analytics top_models
// （request 级视图）口径方向一致。降级语义同主腿。
func (h *SessionOverviewHandler) queryModelUsage(ctx context.Context, params QueryParams, win sessionWindow) ([]ModelUsageItem, error) {
	buildQuery := func(withUniverse bool) (string, []interface{}) {
		where := []string{}
		args := []interface{}{}
		argIdx := 1

		if params.TenantID != "" {
			where = append(where, fmt.Sprintf("tenant_id = $%d", argIdx))
			args = append(args, params.TenantID)
			argIdx++
		}
		where = append(where, win.clause("first_request_at", &args, &argIdx))
		// 与主腿同法：ss 先绑小再做宇宙连接（53100 修复）；宇宙合取留外层。
		innerWhere := joinStrings(where, " AND ")
		var innerFrom string
		var cte string
		if withUniverse {
			outerWhere := ""
			if u := universeWhere(win.includeProbes); u != "" {
				outerWhere = " WHERE " + u
			}
			var join string
			cte, join = universeJoinFor(win)
			innerFrom = "FROM (SELECT * FROM session_summaries WHERE " + innerWhere + ") ss\n" + join + outerWhere
		} else {
			innerFrom = "FROM session_summaries ss\nWHERE " + innerWhere
		}

		query := cte + `
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
				unnest(ss.models_used) as model,
				ss.request_count,
				ss.total_cost_usd,
				ss.avg_latency_ms,
				ss.success_count
			` + innerFrom + `
		) t
		GROUP BY model
		ORDER BY total_cost DESC
		LIMIT 10
	`
		return query, args
	}

	includeProbes := win.includeProbes
	query, args := buildQuery(!includeProbes)
	rows, err := h.db.Query(ctx, query, args...)
	if err != nil && isUniverseUnavailableErr(err) {
		win.markDegraded()
		query, args = buildQuery(false)
		rows, err = h.db.Query(ctx, query, args...)
	}
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

func (h *SessionOverviewHandler) queryTopClients(ctx context.Context, params QueryParams, win sessionWindow, limit int) ([]ClientRankItem, error) {
	// 2026-11-21 对齐轮：缺省把「session_dim 回退连接」升级为会话宇宙连接
	// （sessions 内连接 + session_dim 带 tenant 左连接 + owner/is_probe 判定，
	// 片段取自 domains/analytics）⇒ 客户端榜与面板其余腿同宇宙。降级链保持
	// 既有形状：宇宙依赖缺 → 全量宇宙；session_dim 缺 → 无 dim 回退。
	buildQuery := func(withUniverse, withDim bool) (string, []interface{}) {
		where := []string{}
		args := []interface{}{}
		argIdx := 1

		where = append(where, win.clause("first_request_at", &args, &argIdx))
		if params.TenantID != "" {
			where = append(where, fmt.Sprintf("tenant_id = $%d", argIdx))
			args = append(args, params.TenantID)
			argIdx++
		}
		// 与主腿同法：ss 先绑小再做宇宙连接（53100 修复）；宇宙合取留外层。
		innerWhere := joinStrings(where, " AND ")
		var fromClause string
		var cte string
		if withUniverse {
			outerWhere := ""
			if u := universeWhere(win.includeProbes); u != "" {
				outerWhere = " WHERE " + u
			}
			var join string
			cte, join = universeJoinFor(win)
			fromClause = "FROM (SELECT * FROM session_summaries WHERE " + innerWhere + ") ss\n" + join + outerWhere
		} else if withDim {
			fromClause = "FROM session_summaries ss\n\t\tLEFT JOIN session_dim sd ON sd.gw_session_id = ss.session_key\nWHERE " + qualifySs(innerWhere)
		} else {
			fromClause = "FROM session_summaries ss\nWHERE " + innerWhere
		}

		var clientExpr string
		if withDim {
			clientExpr = "COALESCE(NULLIF(TRIM(sd.client_id), ''), NULLIF(ss.client_models[1], ''), 'unknown')"
		} else {
			clientExpr = "COALESCE(NULLIF(ss.client_models[1], ''), 'unknown')"
		}

		query := fmt.Sprintf(cte+`
		SELECT
			`+clientExpr+` as client_id,
			COUNT(*) as session_count,
			COALESCE(SUM(ss.total_cost_usd), 0) as total_cost,
			MAX(ss.last_request_at) as last_activity
		`+fromClause+`
		GROUP BY `+clientExpr+`
		ORDER BY total_cost DESC
		LIMIT $%d
	`, argIdx)
		args = append(args, limit)
		return query, args
	}

	universe := !win.includeProbes
	query, args := buildQuery(universe, true)
	rows, err := h.db.Query(ctx, query, args...)
	if err != nil && isUniverseUnavailableErr(err) {
		win.markDegraded()
		universe = false
		query, args = buildQuery(false, true)
		rows, err = h.db.Query(ctx, query, args...)
	}
	if err != nil && isMissingSessionDim(err) {
		query, args = buildQuery(false, false)
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

func (h *SessionOverviewHandler) queryTopTasks(ctx context.Context, params QueryParams, win sessionWindow, limit int) ([]TaskRankItem, error) {
	// 2026-11-21 对齐轮：与 queryTopClients 同形升级（宇宙连接 + 降级链）。
	buildQuery := func(withUniverse, withDim bool) (string, []interface{}) {
		where := []string{}
		args := []interface{}{}
		argIdx := 1

		where = append(where, win.clause("first_request_at", &args, &argIdx))
		if params.TenantID != "" {
			where = append(where, fmt.Sprintf("tenant_id = $%d", argIdx))
			args = append(args, params.TenantID)
			argIdx++
		}
		// 与主腿同法：ss 先绑小再做宇宙连接（53100 修复）；宇宙合取留外层。
		innerWhere := joinStrings(where, " AND ")
		var fromClause string
		var cte string
		if withUniverse {
			outerWhere := ""
			if u := universeWhere(win.includeProbes); u != "" {
				outerWhere = " WHERE " + u
			}
			var join string
			cte, join = universeJoinFor(win)
			fromClause = "FROM (SELECT * FROM session_summaries WHERE " + innerWhere + ") ss\n" + join + outerWhere
		} else if withDim {
			fromClause = "FROM session_summaries ss\n\t\tLEFT JOIN session_dim sd ON sd.gw_session_id = ss.session_key\nWHERE " + qualifySs(innerWhere)
		} else {
			fromClause = "FROM session_summaries ss\nWHERE " + innerWhere
		}
		var taskExpr string
		if withDim {
			taskExpr = "COALESCE(NULLIF(TRIM(sd.task_id), ''), 'unknown')"
		} else {
			taskExpr = "'unknown'"
		}

		query := fmt.Sprintf(cte+`
		SELECT
			`+taskExpr+` as task_id,
			COUNT(*) as session_count,
			COALESCE(SUM(ss.total_cost_usd), 0) as total_cost,
			MAX(ss.last_request_at) as last_activity
		`+fromClause+`
		GROUP BY `+taskExpr+`
		ORDER BY session_count DESC
		LIMIT $%d
	`, argIdx)
		args = append(args, limit)
		return query, args
	}

	universe := !win.includeProbes
	query, args := buildQuery(universe, true)
	rows, err := h.db.Query(ctx, query, args...)
	if err != nil && isUniverseUnavailableErr(err) {
		win.markDegraded()
		universe = false
		query, args = buildQuery(false, true)
		rows, err = h.db.Query(ctx, query, args...)
	}
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
func (h *SessionOverviewHandler) querySessionTrends(ctx context.Context, params QueryParams, win sessionWindow) ([]CostTrendPoint, []SessionTrendPoint, error) {
	// 2026-11-21 对齐轮：随主腿套会话宇宙（缺省排除探测）⇒ 趋势与 KPI 同宇宙。
	buildQuery := func(withUniverse bool) (string, []interface{}) {
		where := []string{}
		args := []interface{}{}
		argIdx := 1

		if params.TenantID != "" {
			where = append(where, fmt.Sprintf("tenant_id = $%d", argIdx))
			args = append(args, params.TenantID)
			argIdx++
		}
		// 窗口参数的**首个**占位符（供下面 FILTER 复用同一组边界参数）。
		winArg := argIdx
		where = append(where, win.clause("first_request_at", &args, &argIdx))
		// 与主腿同法：ss 先绑小再做宇宙连接（53100 修复）；宇宙合取留外层。
		innerWhere := joinStrings(where, " AND ")
		var fromClause string
		var cte string
		if withUniverse {
			outerWhere := ""
			if u := universeWhere(win.includeProbes); u != "" {
				outerWhere = " WHERE " + u
			}
			var join string
			cte, join = universeJoinFor(win)
			fromClause = "FROM (SELECT * FROM session_summaries WHERE " + innerWhere + ") ss\n" + join + outerWhere
		} else {
			fromClause = "FROM session_summaries ss\nWHERE " + innerWhere
		}

		// 日桶表达式（趋势图横轴）：calendar 模式按**系统时区**日历日分桶
		// （PG 会话时区未钉死 ⇒ 直接 DATE(ts) 会让横轴整体前移一天）；
		// rolling 模式逐字沿用 DATE(ss.first_request_at)。SELECT 与 GROUP BY
		// 复用**同一个表达式字符串**（同一占位符）⇒ 参数只追加一次。
		dayExpr := win.dayExpr("ss.first_request_at", &args, &argIdx)
		// 「新会话」FILTER 与 WHERE 同窗口 ⇒ 恒真（保留原语义：桶内全是新会话）。
		newFilter := win.clauseAt("ss.first_request_at", winArg)

		// DATE(...) is OID 1082 binary; pgx v5 refuses *string ⇒ cast to text.
		query := cte + `
		SELECT
			` + dayExpr + `::text as date,
			COALESCE(SUM(ss.total_cost_usd), 0) as cost,
			COUNT(*) as sessions,
			COALESCE(SUM(ss.request_count), 0) as requests,
			COUNT(*) FILTER (WHERE ` + newFilter + `) as new_sessions,
			COUNT(*) FILTER (WHERE ss.last_request_at >= NOW() - INTERVAL '24 hours') as active_count,
			COUNT(*) FILTER (WHERE ss.last_request_at < NOW() - INTERVAL '24 hours' AND ss.first_request_at < NOW() - INTERVAL '24 hours') as closed_count
		` + fromClause + `
		GROUP BY ` + dayExpr + `
		ORDER BY date
	`
		return query, args
	}

	universe := !win.includeProbes
	query, args := buildQuery(universe)
	rows, err := h.db.Query(ctx, query, args...)
	if err != nil && isUniverseUnavailableErr(err) {
		win.markDegraded()
		query, args = buildQuery(false)
		rows, err = h.db.Query(ctx, query, args...)
	}
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
