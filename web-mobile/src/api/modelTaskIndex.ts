import { req, type RequestOptions } from './client'
import { taskLabel } from './autoRouteMatrix'

// modelTaskIndex.ts — 自动路由的「模型 × 任务」表现索引。
//   GET /api/admin/auto-route/analytics/model-task-index
//
// 鉴权：**superAdmin**。同 `matrix` / `flow` —— RegisterAnalyticsRoutes 由
// `analyticsH.RegisterAnalyticsRoutes(mux, h.superAdmin)` 挂载
// （admin/handler.go:1430），本端点是它里面的第三个 adminWrap。
//
// 与同族两份的关系：autoRouteInsights 答「**单个模型**的请求漏斗」，
// autoRouteMatrix 答「模型 × 任务的横向热力」，本份答「**按 5 分钟桶
// 滚动的**模型 × 任务表现排行」，带成本与 p95。
//
// ⚠️★★★ 七个坑，全部逐条实读源码，不是猜的：
//
// (1) ★★★ **只看自动路由的请求。**
//     生产者 `bg/auto_index_refresher.go` 的 rollup SQL 里有
//     `WHERE ... AND rl.is_auto_request = TRUE`。
//     ⇒ 人工/直连的流量**完全不进这张表**。
//     ⇒ 排行榜天然偏小众模型，必须说清口径，否则运维会以为
//       「这个模型请求量这么低」是全站事实。
//
// (2) ★★ **只返回最新一个 5 分钟桶。**
//     handler 先 `SELECT MAX(bucket) FROM model_task_index`，
//     再 `WHERE mti.bucket = $1`（admin/analytics.go:621-660）。
//     ⇒ 这**不是**趋势图，也不是窗口聚合；
//       桶窗口是 `rl.ts >= NOW() - 5min AND rl.ts < $1`（滚动 5 分钟），
//       而刷新器每 5 分钟跑一次 ⇒ 数据可能**滞后约 5~10 分钟**。
//     ⇒ 页面必须常驻显示「数据截至 <bucket>」。
//
// (3) ★★★ **量纲陷阱第 4 处，而且这个 0.9 是个陷阱中的陷阱。**
//     列类型 `success_rate numeric(5,4)`
//     （deploy/sql/schemas/baseline/01-schema.sql:10270+），
//     生产者是 `AVG(CASE WHEN rl.success THEN 1.0 ELSE 0.0 END)`
//     ⇒ **0..1 的比率**，显示成百分比要 ×100。
//     ⚠️ 同一份刷新器里另一个表用 `0.9::numeric(5,4)` 作
//       **冷启动默认**（auto_index_refresher.go:625），注释里也写
//       `success_rate=0.9` 是「默认分」。
//       ⇒ **0.9 在这个家族里既是「90% 成功率」又是「默认值」**
//         （不同表）。本表它是真实比率，但正因为长得像默认值，
//         下一个人很容易在这里加一条「0.9 视为无数据」的特判 —— 那是错的。
//     ⇒ 一律走 `formatModelTaskRatePct`（名字里带 ModelTask 就是为了
//       和 timeline 那份 0..100 的 `formatSuccessRatePct` 区分开）。

// (4) ★★★ **`items` 的键是**条件写入**的，响应是稀疏对象。**
//     handler 用 `map[string]interface{}` 逐个 `if x != nil { entry[...] = ... }`
//     （admin/analytics.go:684-722）。而表里：
//       · `canonical_id  integer NOT NULL`    ⇒ 该键恒存在
//       · `sample_count integer NOT NULL`    ⇒ 该键恒存在（且 COUNT(*) ⇒ ≥1）
//       · `canonical_name` 来自 LEFT JOIN models_canonical ⇒ **匹配不到就没有这个键**
//       · `success_rate` / `avg_latency_ms` / `p95_latency_ms`
//         / `avg_cost_per_1k_usd` / `primary_credential_id` 列可空 ⇒ **键可能整个不存在**
//     ⇒ 类型里这些字段一律可选；渲染时 `undefined` 走「—」而不是 0。

// (5) ★★★ **三个数值列的「0」是生产者兜底值，不是真实测量。**
//     rollup SQL：
//       `COALESCE(AVG(rl.latency_ms), 0)::int`               → **0 = 没有延时数据**
//       `COALESCE(percentile_cont(0.95) ..., 1000)::int`     → **1000 = 没有延时数据**
//       `CASE WHEN SUM(rl.total_tokens) > 0 THEN ... ELSE 0 END` → **0 = 没有 token/成本数据**
//     ⇒ 三者含义完全不同：`avg_latency_ms = 0` 是「没量到」，
//       `p95_latency_ms = 1000` 是「没量到」、`avg_cost_per_1k_usd = 0` 是「没花钱数据」。
//     ★ 后端**给不出判据**（列可空，但被 COALESCE 吃掉了）。
//     ⇒ UI 只能弱化显示 + 常驻说明，不能把 0 渲染成「0ms」/「免费」。

// (6) ★★ **空表是一个独立状态，不是「没有数据」。**
//     `SELECT MAX(bucket)` 为 NULL 时返回
//     `{ bucket: null, items: [], warning: "model_task_index is empty; awaiting first bg worker refresh" }`
//     （admin/analytics.go:643-649）。
//     ⇒ 这说明**后台刷新器还没首刷**，不是「这段时间没有自动路由流量」。
//     ⇒ 必须独立渲染，不能落进空态。见 `isAwaitingFirstRefresh`。

// (7) ★ `top` 默认 20，越界**静默回落**到 20（不是 400）：
//     `if v, err := strconv.Atoi(...); err == nil && v > 0 && v <= 500 { top = v }`
//     ⇒ 非法值、不传、传 0、传 501，四种情况都得到 20。
//     ★ 与同族 `window`/`metric`（**400**）语义相反，别照抄。
//     ⇒ 客户端只发 1..500 的整数；不发就是后端默认 20。
//
// ★ `task_type` 过滤：`strings.TrimSpace` 但**不** ToLower，
//   SQL 是 `mti.task_type = $1`（**大小写敏感**精确匹配）。
//   ⇒ 客户端只 trim，不擅自改大小写 —— 改成小写反而查不到。

/** 后端默认 top（越界时也回落到这里）。 */
export const MODEL_TASK_INDEX_TOP_DEFAULT = 20
/** 后端接受的最大 top（`v <= 500`）。 */
export const MODEL_TASK_INDEX_TOP_MAX = 500

export interface ModelTaskIndexItem {
  /** 表列 NOT NULL ⇒ 恒存在。 */
  canonical_id: number
  /** LEFT JOIN models_canonical 未命中 ⇒ **整个键不存在**（不是空串）。 */
  canonical_name?: string
  /** 表列 NOT NULL。★ 合成键 `__specified__` 需经 `taskLabel` 翻译。 */
  task_type: string
  /** 表列 NOT NULL；`COUNT(*)` ⇒ **≥ 1**，不会出现 0。 */
  sample_count: number
  /** 0..1 比率（**不是**百分数）。见 (3)。 */
  success_rate?: number
  /** **0 是生产者兜底值**（`COALESCE(..., 0)`），不是「真的 0ms」。见 (5)。 */
  avg_latency_ms?: number
  /** **1000 是生产者兜底值**（`COALESCE(..., 1000)`）。见 (5)。 */
  p95_latency_ms?: number
  /** **0 表示没有 token/成本数据**，不是「免费」。见 (5)。 */
  avg_cost_per_1k_usd?: number
  primary_credential_id?: number
  updated_at: string
}

export interface ModelTaskIndexResponse {
  /** 最新桶的时间戳。★ `null` = 表空 = **尚未首刷**，不是「没有数据」。 */
  bucket: string | null
  items: ModelTaskIndexItem[]
  /** 仅在「尚未首刷」分支出现。 */
  warning?: string
}

export interface ModelTaskIndexParams {
  taskType?: string
  top?: number
}

export function fetchModelTaskIndex(
  params: ModelTaskIndexParams = {},
  options?: RequestOptions,
): Promise<ModelTaskIndexResponse> {
  const qs = new URLSearchParams()
  // ★ 后端**不** ToLower（analytics.go:627 只有 TrimSpace），
  //   SQL 是 `= $1` 大小写敏感 ⇒ 擅自改小写会让本来能命中的查询落空。
  const tt = (params.taskType ?? '').trim()
  if (tt !== '') qs.set('task_type', tt)
  // ★ 只发 1..500 的整数：越界不 400，而是**静默变成 20**（见 (7)），
  //   客户端发了非法值却拿到 20 条，会误以为是自己要的条数。
  if (typeof params.top === 'number' && Number.isFinite(params.top)) {
    const t = Math.trunc(params.top)
    if (t >= 1 && t <= MODEL_TASK_INDEX_TOP_MAX) qs.set('top', String(t))
  }
  const s = qs.toString()
  return req<ModelTaskIndexResponse>('GET', `/api/admin/auto-route/analytics/model-task-index${s ? '?' + s : ''}`, undefined, options)
}

// ── 状态判定 ───────────────────────────────────────────────────────────────

/** ★ `bucket === null` = 后台刷新器**尚未首刷**（不是「这段时间没有流量」）。 */
export function isAwaitingFirstRefresh(resp: ModelTaskIndexResponse | null | undefined): boolean {
  return !!resp && resp.bucket === null
}

/**
 * 是否可能被 `top` 截断。
 *
 * ★ 与 timeline（写死 500）/ cache-state（ScanKeys 4096）不同：
 *   这两个是**后端写死的**上限，客户端不知道实际发了多少 ⇒ 只能说「可能被截断」。
 *   而 `top` 是**客户端自己发的**，所以 `items.length === top` 是**精确**信号。
 *
 * @param requestedTop 客户端实际发出的 top；没发就按后端默认 20 算。
 */
export function modelTaskIndexTruncated(
  resp: ModelTaskIndexResponse | null | undefined,
  requestedTop?: number,
): boolean {
  if (!resp) return false
  const n = resp.items?.length ?? 0
  const eff =
    typeof requestedTop === 'number' && Number.isFinite(requestedTop) &&
      Math.trunc(requestedTop) >= 1 && Math.trunc(requestedTop) <= MODEL_TASK_INDEX_TOP_MAX
      ? Math.trunc(requestedTop)
      : MODEL_TASK_INDEX_TOP_DEFAULT
  return n >= eff
}

// ── 数值呈现 ───────────────────────────────────────────────────────────────

/**
 * `success_rate`（0..1）→ 百分数字符串。★ **必须 ×100**。
 *
 * 刻意不叫 `formatSuccessRatePct`：那份（api/probeTimelineCache.ts）处理的是
 * **已经乘过 100** 的 0..100 值。两个同名函数处理**相反量纲**，
 * 是最容易埋「9000%」的地方。详见文件头 (3)。
 */
export function formatModelTaskRatePct(v: number | null | undefined): string {
  if (v === null || v === undefined || !Number.isFinite(v)) return '—'
  return (v * 100).toFixed(1) + '%'
}

/** `avg_latency_ms`：★ 0 是**生产者兜底值**，不是真的 0ms。 */
export const AVG_LATENCY_PRODUCER_DEFAULT = 0
/** `p95_latency_ms`：★ 1000 是**生产者兜底值**。 */
export const P95_LATENCY_PRODUCER_DEFAULT = 1000
/** `avg_cost_per_1k_usd`：★ 0 表示**没有 token/成本数据**，不是免费。 */
export const COST_NO_TOKEN_DATA = 0

export function avgLatencyMayBeDefault(v: number | null | undefined): boolean {
  return v === AVG_LATENCY_PRODUCER_DEFAULT
}
export function p95LatencyMayBeDefault(v: number | null | undefined): boolean {
  return v === P95_LATENCY_PRODUCER_DEFAULT
}
export function costMayBeNoData(v: number | null | undefined): boolean {
  return v === COST_NO_TOKEN_DATA
}

/** 毫秒展示；缺失（键不存在）走 `—`，不显示 0。 */
export function formatMs(v: number | null | undefined): string {
  if (v === null || v === undefined || !Number.isFinite(v)) return '—'
  // ★ **不加千分位**：全站延时口径是 `1200ms`（timeline 的 `avgLatency: '{ms}ms'`），
  //   这里若用 `Intl.NumberFormat` 会渲染成 `1,200ms`，同一应用里两种口径。
  //   样本数那类「计数值」才走 fmtInt（带分隔符），延时不是计数值。
  return String(Math.round(v)) + 'ms'
}

/** 美元 / 每千 token。★ 后端字段名 `avg_cost_per_1k_usd` 已是「每 1k」。 */
export function formatCostPer1k(v: number | null | undefined): string {
  if (v === null || v === undefined || !Number.isFinite(v)) return '—'
  return '$' + v.toFixed(4) + '/1k'
}

/** 稳定 key：同一模型同一任务同一桶只应有一行。 */
export function modelTaskIndexKeyOf(it: ModelTaskIndexItem | null | undefined): string {
  if (!it) return '∅'
  return `${it.canonical_id}|${it.task_type}`
}

export { taskLabel }