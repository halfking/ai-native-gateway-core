package dashboardapi

// session_overview_alignment.go —— /dashboard 会话统计与 /admin/session-analytics
// 的口径对齐（2026-11-21 统计口径对齐轮）。
//
// # 对齐的三层
//
// ① 会话宇宙对齐：session_summaries 直读腿默认套上与 981/994 物化视图**逐字
//    同一**的会话宇宙（sessions 内连接 + session_dim 左连接 + owner 双源键非空
//    + is_probe + session_analytics_is_probe 三维判定；片段取自
//    domains/analytics.SessionUniverseJoin/SessionUniverseWhere，迁移侧由契约
//    门对拍）。迁移前会话统计里 ~80% 是探测/自检会话（981 实测），这就是
//    「看板与会话分析对不上」的第一大项。include_probes=1 回到旧全量宇宙。
// ② 对齐指标行：aligned 块读 user_session_aggregates + model_usage_aggregates
//    物化视图（sessionanalytics.AlignedOverviewSnapshot 单一真源导出）——与
//    /admin/session-analytics 总览页**同一实现** ⇒ 两面数字构造相等，不是
//    「再算一遍碰巧相等」。即时查询面（面板自己的腿）与定时缓存面（物化
//    视图，15 分钟一刷）并存在同一响应里，由 probe_scope / window_mode /
//    aligned.window 显式披露（数据关系表单同步登记）。
// ③ 日历窗口对齐：aligned 块的窗口是**上海日历日**（与物化视图日桶同尺，
//    domains/analytics.ShanghaiCalendarRangeUtc）。days 预设自动折算成
//    [上海今天-(N-1), 上海今天]，session-analytics 的 7 天预设同一折算 ⇒
//    同一预设两边可复现同一数字；显式 start_date/end_date 直接透传。
//
// # 降级链（与 board 的形状一致：缺席比假绿诚实）
//
//   - 物化视图缺失/未迁移（42P01）⇒ aligned 块缺席 + aligned_source=unavailable，
//     面板自己的腿照常返回（新鲜值，只是没有对齐行）。
//   - 宇宙 join 所需的 sessions/session_dim/判定函数缺失 ⇒ 该腿自动降级为
//     全量宇宙重试 + probe_scope=degraded_included（ degradation 可见）。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/kaixuan/llm-gateway-go/admin/dashboarddegrade"
	"github.com/kaixuan/llm-gateway-go/domains/analytics"
)

// AlignedBlock 是响应里的「对齐指标行」。字段与 /admin/session-analytics
// overview 的 summary + success/cache rate 同源同语义（单位纪律：internal
// 美元 / external 积分，禁止相加展示——framework §3.2）。
type AlignedBlock struct {
	// aligned 窗口（上海日历日，YYYY-MM-DD），显式回传让前端可以照抄到
	// session-analytics 的 URL 上做跨面对拍。
	StartDate string `json:"start_date"`
	EndDate   string `json:"end_date"`

	TotalSessions      int64   `json:"total_sessions"`
	AvgTokens          float64 `json:"avg_tokens"`
	AvgTurns           float64 `json:"avg_turns"`
	AvgDurationSeconds float64 `json:"avg_duration_seconds"`
	ActiveUsers        int64   `json:"active_users"`
	ActiveProjects     int64   `json:"active_projects"`
	ActiveTasks        int64   `json:"active_tasks"`
	InternalCostUSD    float64 `json:"internal_cost_usd"`
	ExternalCredits    float64 `json:"external_credits"`
	SuccessRatePct     float64 `json:"success_rate_pct"`
	CacheRatePct       float64 `json:"cache_rate_pct"`
}

// ★ 2026-11-23 日期范围对齐轮：sessionWindow（窗口 + 探测口径）与
// parseSessionWindow 已移到 dashboard_window.go —— 窗口解析与 SQL 谓词片段
// 现在是**仪表盘族共享**能力（会话总览/会话趋势/健康度/性能/错误五族同尺），
// 不再是会话总览一家的私有解析。

// shanghaiPresetRange 把 days 预设折算成上海日历日窗口 [today-(N-1), today]。
// 与 session-analytics 总览页的 7d/30d 预设同一折算（前端 KxDateRangePicker
// 预设也按系统日历=上海）⇒ 同一预设两边窗口一致。
func shanghaiPresetRange(days int) (string, string) {
	today := analytics.ShanghaiCalendarDayStart(time.Now())
	start := today.AddDate(0, 0, -(days - 1))
	return start.Format("2006-01-02"), today.Format("2006-01-02")
}

// sqlDBOnce 把 pgxpool 适配成 database/sql 句柄（sessionanalytics 包的查询
// 走 database/sql）。进程内只建一次；底层连接池与 pgx 共享。
var (
	sqlDBOnce   sync.Once
	sqlDBHandle *sql.DB
)

func sqlDBFromPool(pool *pgxpool.Pool) *sql.DB {
	sqlDBOnce.Do(func() {
		if pool != nil {
			sqlDBHandle = stdlib.OpenDBFromPool(pool)
		}
	})
	return sqlDBHandle
}

// queryAlignedSnapshot 读对齐指标行（domains/analytics 单一真源——与
// /admin/session-analytics 总览页同一份 SQL）。失败（物化视图缺失等）返回
// 错误由调用方降级——对齐块缺席不是错误，是「缓存面还没就绪」。
func (h *SessionOverviewHandler) queryAlignedSnapshot(ctx context.Context, tenantID string, startDate, endDate string) (*AlignedBlock, error) {
	db := sqlDBFromPool(h.db)
	if db == nil {
		return nil, fmt.Errorf("database unavailable")
	}
	var tenantPtr *string
	if tenantID != "" {
		t := tenantID
		tenantPtr = &t
	}
	summary, err := analytics.QueryOverviewSummary(ctx, db, tenantPtr, startDate, endDate)
	if err != nil {
		return nil, err
	}
	rates, err := analytics.QueryModelStats(ctx, db, tenantPtr, startDate, endDate)
	if err != nil {
		return nil, err
	}
	return &AlignedBlock{
		StartDate:          startDate,
		EndDate:            endDate,
		TotalSessions:      summary.TotalSessions,
		AvgTokens:          summary.AvgTokens,
		AvgTurns:           summary.AvgTurns,
		AvgDurationSeconds: summary.AvgDurationSeconds,
		ActiveUsers:        summary.ActiveUsers,
		ActiveProjects:     summary.ActiveProjects,
		ActiveTasks:        summary.ActiveTasks,
		InternalCostUSD:    summary.InternalCostUSD,
		ExternalCredits:    summary.ExternalCredits,
		SuccessRatePct:     rates.SuccessRatePct,
		CacheRatePct:       rates.CacheRatePct,
	}, nil
}

// universeJoin 返回会话宇宙连接子句（排除探测时）；includeProbes 时返回空
// （调用方保持旧 FROM 形状）。
func universeJoin(includeProbes bool) string {
	if includeProbes {
		return ""
	}
	return analytics.SessionUniverseJoin("ss", "s", "sd")
}

// universeWhere 返回会话宇宙 WHERE 合取项（排除探测时）。
func universeWhere(includeProbes bool) string {
	if includeProbes {
		return ""
	}
	return analytics.SessionUniverseWhere("s", "sd")
}

// isUniverseUnavailableErr 判定「宇宙 join 的依赖缺失」（sessions/session_dim
// 表不存在 42P01，或 session_analytics_is_probe 函数不存在 42883——未迁移
// 环境）。这类失败走全量宇宙降级重试，不是 500。
// qualifySs 把裸列名的窗口/租户谓词换成 ss. 限定形态——用于「FROM
// session_summaries ss + 额外 JOIN」的分支（裸列与 dim 表的 first_request_at/
// tenant_id 同名 ⇒ 42702 ambiguous）。子查询绑小形态（单表）保持裸列。
func qualifySs(where string) string {
	return strings.ReplaceAll(
		strings.ReplaceAll(where, "tenant_id =", "ss.tenant_id ="),
		"first_request_at", "ss.first_request_at")
}

func isUniverseUnavailableErr(err error) bool {
	if err == nil {
		return false
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "42P01": // undefined_table
			rel := pgErr.TableName
			if rel == "" {
				rel = dashboarddegrade.ExtractRelationName(err)
			}
			return rel == "sessions" || rel == "session_dim" ||
				strings.Contains(pgErr.Message, "session_analytics_is_probe")
		case "42883": // undefined_function
			return strings.Contains(pgErr.Message, "session_analytics_is_probe")
		}
		return false
	}
	// 非 pgx 结构化错误（如 database/sql 包装）兜底按消息识别。
	return strings.Contains(err.Error(), "session_analytics_is_probe") ||
		dashboarddegrade.IsMissingRelationError(err) && dashboarddegrade.ExtractRelationName(err) != ""
}
