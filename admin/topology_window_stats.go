package admin

// topology_window_stats.go — providers-today / models-today 的短窗口口径。
//
//	GET /api/admin/topology/providers-today?window=5m|1h
//	GET /api/admin/topology/models-today?window=5m|1h
//
// 2026-10-28（live 页头部统计轮）：owner 要求头部统计可选「当天 / 1 小时 /
// 5 分钟」。日桶表 stats_usage_daily 是纯日粒度（无 hour 列）且 rollup 每小时
// 重建 —— 短窗口唯一如实的来源是 request_logs 直查：
//   - request_logs 有 idx_request_logs_ts (ts DESC)，短窗口聚合走索引；
//   - 供应商归并 credentials.provider_id ← rl.provider_id、模型归并
//     models_canonical ← rl.canonical_id，与 live-stream SSE 兜底查询
//     （live_stream_sse.go catch-up）完全同款 —— 不引入第二套归并口径；
//   - 状态判定复用 SSE 同一表达式（request_status 优先、success 布尔兜底）。
//
// 为什么绝不能从实时流（SSE）聚合短窗口：Redis 快照每泳道只保留
// LiveStreamLaneVisibleLimit=100 条、前端 MAX_VISIBLE ≤200 —— 高峰期一分钟的
// 流量就能填满缓冲，从它聚合出的「1 小时」会系统性偏低，把低估说成统计。
//
// 两条窗口 SQL 的 SELECT 列形状与各自 day SQL 逐列一致（含结尾
// NULL::timestamptz 占位 watermark）⇒ handler 共用同一段行处理 / totals
// 累加 / share 计算，不为窗口口径另写一份循环。窗口口径没有 rollup 滞后，
// watermark 恒空、rollup_cadence 记 "realtime"，响应加 window 字段回显。

import (
	"net/http"
	"strings"
	"time"
)

// topologyWindowParam 解析 ?window=：只认 5m / 1h，其余（含缺省与乱写）
// 一律走 day 日桶路径 —— 响应的 window 字段回显实际口径，前端不会把
// 日桶数据误标成短窗口。
func topologyWindowParam(r *http.Request) (time.Duration, bool) {
	switch strings.TrimSpace(r.URL.Query().Get("window")) {
	case "5m":
		return 5 * time.Minute, true
	case "1h":
		return time.Hour, true
	default:
		return 0, false
	}
}

// topologyWindowLabel 把窗口时长转成响应/前端使用的回显标签。
func topologyWindowLabel(d time.Duration) string {
	if d == time.Hour {
		return "1h"
	}
	return "5m"
}

// requestLogsStatusExpr 与 live-stream SSE catch-up 查询逐字同款：
// request_status 优先，空串回落到 success 布尔列，再回落 in_progress。
// 短窗口统计与实时流面板对同一条请求的成败判定必须一致，否则
// 「右栏 3 失败、头部统计 2 失败」这类对不上账会直接被用户看到。
//
// ★ 下面的 SQL 把这份表达式**逐字内联**（不是 Go 常量拼接）：canonical
// 读点登记簿的扫描器只认 SQL 字面量文本、不解析 Go 标识符拼接，拼接写法
// 会让 rl.request_status / rl.success 从读点清单里隐身（实测抓到）。
// 「内联文本 == 本常量」由 TestTopologyWindowSQLContractPins 钉住 ——
// 改表达式必须两处同步，钉子会在只改一处时变红。
const requestLogsStatusExpr = `COALESCE(NULLIF(rl.request_status, ''), ` +
	`CASE WHEN rl.success THEN 'success' WHEN rl.success = FALSE THEN 'failure' ELSE 'in_progress' END)`

// providersWindowSQL：短窗口供应商聚合。列形状与 providersTodaySQL 一致
// （watermark 恒 NULL —— 直查没有 rollup 滞后）。LEFT JOIN providers 保留
// provider_id=0 未知桶（客户端侧被拒/未归因的请求不丢流量，只标未知），
// 与 day 口径同一语义。
const providersWindowSQL = `
WITH base AS (
	SELECT COALESCE(p.id, 0)                       AS provider_id,
	       COALESCE(p.catalog_code, '')            AS catalog_code,
	       COALESCE(NULLIF(p.display_name, ''), '') AS display_name,
	       COALESCE(p.enabled, FALSE)              AS enabled,
	       COALESCE(NULLIF(rl.request_status, ''), CASE WHEN rl.success THEN 'success' WHEN rl.success = FALSE THEN 'failure' ELSE 'in_progress' END) AS status,
	       -- ★ status 表达式 = requestLogsStatusExpr（逐字内联，见该常量注释）
	       rl.prompt_tokens, rl.completion_tokens, rl.total_tokens,
	       rl.cache_read_tokens, rl.cache_write_tokens,
	       rl.credits_charged, rl.cost_usd
	FROM request_logs_with_current_month rl
	LEFT JOIN credentials c ON c.id = rl.credential_id
	LEFT JOIN providers p ON p.id = COALESCE(c.provider_id, rl.provider_id)
	WHERE rl.ts >= $1::timestamptz
	  AND ($2::text = '' OR rl.tenant_id = $2)
)
SELECT provider_id, catalog_code, display_name, enabled,
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
       COALESCE(SUM(cost_usd),0)::float8           AS cost_usd,
       NULL::timestamptz                           AS watermark
FROM base
GROUP BY provider_id, catalog_code, display_name, enabled
ORDER BY requests DESC, provider_id
LIMIT $3`

// modelsWindowSQL：短窗口模型聚合（canonical_id 上归并，同一标准模型跨
// 供应商/别名合并一行 —— 与 modelsTodaySQL 同一归并粒度；raw_model_name
// 取窗口内出现过的原始请求模型名，canonical_id=0 的 passthrough 行回落
// 原始名）。列形状与 modelsTodaySQL 一致。
// ★ 2026-10-29 复审轮修：与 modelsTodaySQL 同款 passthrough 拆名——
// canonical_id=0 的不同 raw 名是不同模型（图 modelKeyOf 按原始名分节点），
// 单键 GROUP BY 会把窗口内所有 passthrough 并成一行、名字 MAX 任选。
const modelsWindowSQL = `
WITH base AS (
	SELECT COALESCE(mc.id, 0)                        AS canonical_id,
	       COALESCE(NULLIF(mc.canonical_name, ''), '') AS canonical_name,
	       COALESCE(NULLIF(rl.client_model, ''), '')   AS raw_model_name,
	       COALESCE(NULLIF(rl.request_status, ''), CASE WHEN rl.success THEN 'success' WHEN rl.success = FALSE THEN 'failure' ELSE 'in_progress' END) AS status,
	       -- ★ status 表达式 = requestLogsStatusExpr（逐字内联，见该常量注释）
	       rl.prompt_tokens, rl.completion_tokens, rl.total_tokens,
	       rl.cache_read_tokens, rl.cache_write_tokens,
	       rl.credits_charged, rl.cost_usd
	FROM request_logs_with_current_month rl
	LEFT JOIN models_canonical mc ON mc.id = rl.canonical_id
	WHERE rl.ts >= $1::timestamptz
	  AND ($2::text = '' OR rl.tenant_id = $2)
)
SELECT canonical_id,
       COALESCE(MAX(canonical_name), '')                    AS canonical_name,
       COALESCE(MAX(NULLIF(raw_model_name, '')), '')        AS raw_model_name,
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
       COALESCE(SUM(cost_usd),0)::float8           AS cost_usd,
       NULL::timestamptz                           AS watermark
FROM base
GROUP BY canonical_id,
         CASE WHEN canonical_id = 0 THEN COALESCE(raw_model_name, '') ELSE '' END
ORDER BY requests DESC, canonical_id, raw_model_name
LIMIT $3`
