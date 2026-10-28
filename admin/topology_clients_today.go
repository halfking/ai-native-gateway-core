package admin

// topology_clients_today.go — 实时拓扑页「客户端区间统计」只读投影。
//
//	GET /api/admin/topology/clients-today?window=5m|1h|day
//
// 2026-10-29（live 页单一统计轮）：头部统计面板扩成 clients+providers+models
// 三组、删除下方 SSE 三块汇总 —— 客户端组由本端点供数。
//
// ★ 为什么客户端没有日桶口径：stats 事件链（usage_facts → stats_usage_daily）
// 不携带客户端身份列（Event 里的 agent_name/client_profile/identity_hash 在
// 落 facts 时被丢弃），扩日桶维度要动分区表 DDL + rollup 引擎 —— 不为本轮
// 引入这条风险面。⇒ 客户端组**恒**直查 request_logs（与窗口口径同源同款）：
//   - day = ts >= UTC 日桶起点（与 day_utc 同界）；
//   - 5m / 1h = ts >= now()-interval（topologyWindowParam 同款解析）；
//   - ★ day 模式**以 rollup 水位封顶**（ts <= MAX(source_max_occurred_at)）：
//     providers/models 组的 day 数字来自 rollup（≤1h 滞后、止于水位），客户端
//     若不封顶就会比它们多出最后一小时的量 —— 同一面板里三个组对不上账。
//     rollup 尚无当日行（水位 NULL）时不封顶：那一小时里 providers/models
//     本来就是 0，属「日桶未就绪」的已知边缘，自愈。
// 客户端取键与前端 clientKeyOf 逐字同款：agent_name → client_profile →
// client_protocol → identity_hash 前 8 位 → 'unknown'。
//
// 租户隔离 / 降级 / 截断语义与 providers-today 家族一致（statsTenantScope /
// degraded 信封 / limit 上限 + truncated 标注）。

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// topologyClientsTodayRow 是一行客户端聚合（DB 行 → API 行）。
type topologyClientsTodayRow struct {
	ClientKey        string
	Requests         int64
	Success          int64
	Failure          int64
	RateLimited      int64
	PromptTokens     int64
	CompletionTokens int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	TotalTokens      int64
	CreditsCharged   int64
	CostUSD          float64
}

type topologyClientTodayItem struct {
	// client_key 是稳定身份键（与拓扑图 clientKeyOf 同口径），显示名由前端
	// clientDisplayName 映射（品牌名与语言无关，不进 locale）。
	ClientKey        string  `json:"client_key"`
	Requests         int64   `json:"requests"`
	Success          int64   `json:"success"`
	Failure          int64   `json:"failure"`
	RateLimited      int64   `json:"rate_limited"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	CacheReadTokens  int64   `json:"cache_read_tokens"`
	CacheWriteTokens int64   `json:"cache_write_tokens"`
	TotalTokens      int64   `json:"total_tokens"`
	CreditsCharged   int64   `json:"credits_charged"`
	CostUSD          float64 `json:"cost_usd"`
	Share            float64 `json:"share"` // 占区间总请求的比重（0..1）
}

type topologyClientsTodayResponse struct {
	DayUTC string `json:"day_utc"`
	// WindowStartUTC 只在窗口口径有语义（day 模式的水位封顶见 source_high_watermark）。
	WindowStartUTC *time.Time `json:"window_start_utc,omitempty"`
	TenantScope    string     `json:"tenant_scope,omitempty"`
	GeneratedAt    string     `json:"generated_at"`
	RollupCadence  string     `json:"rollup_cadence,omitempty"`
	// 窗口口径回显：?window=5m|1h 时为 "5m"/"1h"；day 模式不输出（家族契约）。
	Window string `json:"window,omitempty"`
	// SourceHighWatermark = day 模式实际套用的 rollup 水位封顶（对齐 providers/
	// models 组的日桶滞后）；窗口口径 / 水位 NULL（日桶未就绪）时不输出。
	SourceHighWatermark *time.Time                `json:"source_high_watermark,omitempty"`
	Items               []topologyClientTodayItem `json:"items"`
	Truncated           bool                      `json:"truncated"`
	TotalRequests       int64                     `json:"total_requests"`
	Degraded            bool                      `json:"degraded"`
	ErrorCode           string                    `json:"error_code,omitempty"`
}

// clientsTodayLimit 与 providersTodayLimit 同理：客户端键是几十量级（身份哈希
// 会多些），500 是「足够全 + 挡异常膨胀」的双保险；截断显式标注。
const clientsTodayLimit = 500

// clientsTodaySQL：客户端聚合。列形状与窗口家族同款；状态表达式与
// providersWindowSQL/modelsWindowSQL 逐字同款（同一把尺）。参数：
// $1 = 区间下界、$2 = 租户（” = 全量）、$3 = 水位封顶（NULL = 不封顶）、$4 = limit。
const clientsTodaySQL = `
WITH base AS (
	SELECT COALESCE(NULLIF(rl.agent_name, ''), NULLIF(rl.client_profile, ''),
	                NULLIF(rl.client_protocol, ''),
	                LEFT(NULLIF(rl.identity_hash, ''), 8), 'unknown') AS client_key,
	       COALESCE(NULLIF(rl.request_status, ''), CASE WHEN rl.success THEN 'success' WHEN rl.success = FALSE THEN 'failure' ELSE 'in_progress' END) AS status,
	       rl.prompt_tokens, rl.completion_tokens, rl.total_tokens,
	       rl.cache_read_tokens, rl.cache_write_tokens,
	       rl.credits_charged, rl.cost_usd
	FROM request_logs_with_current_month rl
	WHERE rl.ts >= $1::timestamptz
	  AND ($3::timestamptz IS NULL OR rl.ts <= $3::timestamptz)
	  AND ($2::text = '' OR rl.tenant_id = $2)
)
SELECT client_key,
       COUNT(*)::bigint                                AS requests,
       COUNT(*) FILTER (WHERE status = 'success')::bigint      AS success,
       COUNT(*) FILTER (WHERE status = 'failure')::bigint      AS failure,
       COUNT(*) FILTER (WHERE status = 'rate_limited')::bigint AS rate_limited,
       COALESCE(SUM(prompt_tokens),0)::bigint      AS prompt_tokens,
       COALESCE(SUM(completion_tokens),0)::bigint  AS completion_tokens,
       COALESCE(SUM(cache_read_tokens),0)::bigint  AS cache_read_tokens,
       COALESCE(SUM(cache_write_tokens),0)::bigint AS cache_write_tokens,
       COALESCE(SUM(total_tokens),0)::bigint       AS total_tokens,
       COALESCE(SUM(credits_charged),0)::bigint    AS credits_charged,
       COALESCE(SUM(cost_usd),0)::float8           AS cost_usd
FROM base
GROUP BY client_key
ORDER BY requests DESC, client_key
LIMIT $4`

// rollupWatermarkForDay 取当日 rollup 的数据水位（MAX(source_max_occurred_at)）
// —— 客户端 day 查询的水位封顶。日桶表缺失/无行返回 NULL（不封顶）。
func rollupWatermarkForDay(querier topologyStatsDB, ctx context.Context, day time.Time) *time.Time {
	var wm pgtype.Timestamptz
	err := querier.QueryRow(ctx, `SELECT MAX(source_max_occurred_at) FROM stats_usage_daily WHERE day_utc = $1::date`, day).Scan(&wm)
	if err != nil || !wm.Valid {
		return nil
	}
	ts := wm.Time
	return &ts
}

// handleTopologyClientsToday 服务实时拓扑头部统计面板的「客户端组」。
func (h *Handler) handleTopologyClientsToday(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	tenant := statsTenantScope(r)
	day := todayUTCDay(time.Now())
	win, isWindow := topologyWindowParam(r)

	querier := h.topologyStatsQuerier()
	if querier == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"degraded":   true,
			"error_code": "TOPOLOGY_STATS_UNAVAILABLE",
			"items":      []topologyClientTodayItem{},
		})
		return
	}

	limit := clientsTodayLimit
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 && n <= clientsTodayLimit {
		limit = n
	}

	// 区间下界 + 水位封顶：day = UTC 日起点 + rollup 水位；窗口 = now()-interval、
	// 不封顶（三个组同为实时直查，天然对齐）。★ 上界解引用成 any 再进参数：
	// SQL 侧 `$3::timestamptz IS NULL` 吃的是 NULL 语义，typed-nil 指针只会在
	// 量具（pgxmock DeepEqual）与驱动编码之间制造两种真相。
	lower := day
	var upper *time.Time
	var upperArg any
	if isWindow {
		lower = time.Now().UTC().Add(-win)
	} else if wm := rollupWatermarkForDay(querier, r.Context(), day); wm != nil {
		upper = wm
		upperArg = *wm
	}

	rows, err := querier.Query(r.Context(), clientsTodaySQL, lower, strings.TrimSpace(tenant), upperArg, limit)
	if err != nil {
		// request_logs 直查失败与家族口径一致：degraded 信封（前端渲染
		// 「统计不可用」），不把故障说成 0 流量。
		writeJSON(w, http.StatusOK, topologyClientsTodayResponse{
			DayUTC:      day.Format("2006-01-02"),
			GeneratedAt: time.Now().UTC().Format(time.RFC3339),
			Items:       []topologyClientTodayItem{},
			Degraded:    true,
			ErrorCode:   "QUERY_FAILED",
		})
		return
	}
	defer rows.Close()

	items := make([]topologyClientTodayItem, 0, 16)
	var totalRequests int64
	for rows.Next() {
		var row topologyClientsTodayRow
		if err := rows.Scan(
			&row.ClientKey, &row.Requests, &row.Success, &row.Failure, &row.RateLimited,
			&row.PromptTokens, &row.CompletionTokens,
			&row.CacheReadTokens, &row.CacheWriteTokens, &row.TotalTokens,
			&row.CreditsCharged, &row.CostUSD,
		); err != nil {
			warnRowSkip("topology.clients_today", err)
			continue
		}
		items = append(items, topologyClientTodayItem{
			ClientKey:        row.ClientKey,
			Requests:         row.Requests,
			Success:          row.Success,
			Failure:          row.Failure,
			RateLimited:      row.RateLimited,
			PromptTokens:     row.PromptTokens,
			CompletionTokens: row.CompletionTokens,
			CacheReadTokens:  row.CacheReadTokens,
			CacheWriteTokens: row.CacheWriteTokens,
			TotalTokens:      row.TotalTokens,
			CreditsCharged:   row.CreditsCharged,
			CostUSD:          row.CostUSD,
		})
		totalRequests += row.Requests
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "topology clients-today rows failed")
		return
	}

	// share 用「已返回行」的请求数做分母：截断发生时它退化成「可见部分占比」，
	// 语义仍然自洽（与 providers-today 同款）。
	for i := range items {
		if totalRequests > 0 {
			items[i].Share = float64(items[i].Requests) / float64(totalRequests)
		}
	}

	resp := topologyClientsTodayResponse{
		DayUTC:        day.Format("2006-01-02"),
		TenantScope:   tenant,
		GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
		RollupCadence: "realtime",
		Items:         items,
		Truncated:     len(items) >= limit,
		TotalRequests: totalRequests,
	}
	if isWindow {
		windowStart := lower
		resp.Window = topologyWindowLabel(win)
		resp.WindowStartUTC = &windowStart
	} else {
		resp.SourceHighWatermark = upper
	}
	writeJSON(w, http.StatusOK, resp)
}
