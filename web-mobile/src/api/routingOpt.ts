import { req, type RequestOptions } from './client'

// routingOpt.ts — 路由优化器读面。
//   GET /api/admin/routing-opt/stats        总体准确率 + 样本量（窗口写死 24h）
//   GET /api/admin/routing-opt/accuracy     小时 × 任务类型 准确率桶（hours 可调）
//   GET /api/admin/routing-opt/parameters   当前激活的参数版本
//   GET /api/admin/routing-opt/metrics      5 分钟聚合表投影（hours/task_type/provider 可调）
//
// 鉴权：四条都挂 `admin(...)`（admin/handler.go:1421-1424）⇒ **admin 档**。
//
// ★ 这一族补齐的是「**优化器自己调得怎么样**」，与已上移的
//   `/overrides`（规则是什么）、`/routing-audit`（谁改的）、
//   `/funnel`（请求漏斗）、`/matrix`（热力矩阵）、`/model-task-index`
//   （按 5 分钟桶的表现）互不重叠。
//
// ⚠️★★★ 七个坑，逐条实读源码：
//
// (1) ★★★★ **准确率是「1 条人工标注算 2 条」的加权平均，不是合并准确率。**
//     `routingOptWeightedAccuracy`：
//       denom = autoTotal + 2*humanTotal
//       num   = autoCorrect + 2*humanCorrect
//       accuracy = num / denom        （num 另有 `if num > denom { num = denom }` 防御性夹取）
//     ⇒ 用户按 `(correct+correct)/(total+total)` 自己算是**另一个数**。
//     ★ 更要紧的是：响应**只给样本量、不给命中数**
//       （`auto_samples` / `human_samples` 是 COUNT，没有 correct 字段）
//       ⇒ 客户端**无法从响应验证这个加权值**，只能照抄并说明口径。
//
// (2) ★★★★ **auto 与 human 的「正确」是两种不同定义。**
//       auto ：`SUM(CASE WHEN success THEN 1 ELSE 0 END)` —— 请求**成功**了
//       human：`predicted_provider = correct_provider` —— 预测**命中人工标注**
//     ⇒ 加权平均把「成功」和「命中人工真值」揉成一个数，
//       两者口径不同，不能互相替代解读。
//
// (3) ★★★★ **量纲第 5 处：`overall_accuracy` / `accuracy` 是 0..1 比率，不是百分数。**
//     → 走 `formatRoutingOptAccuracy`（×100）。刻意与
//       `formatSuccessRatePct`（0..100）、`formatModelTaskRatePct`（0..1）
//       **不同名**，避免混淆。累计五处量纲见 §11.63。
//
// (4) ★★★ `accuracy` 端点每个桶的 `accuracy` **恒有样本**。
//     handler 写的是 `b.Accuracy, _ = routingOptWeightedAccuracy(...)` ——
//     `hasData` 返回值**被丢弃**，看起来像缺陷；但 SQL 是
//     `… GROUP BY date_trunc('hour',created_at), task_type`，**没有**
//     `generate_series`，而 `GROUP BY` 只产出非空组 ⇒ `COUNT(*) ≥ 1`
//     ⇒ `denom ≥ 1 > 0` ⇒ `hasData` 恒为 true ⇒ 丢弃它**无害**。
//     ★ 因此桶里的 `accuracy: 0` 是**真的 0%**，不是「没有数据」。
//     ★ 这是本轮**特意追到 SQL 才排除**的一个「看起来像 bug 的地方」——
//       既不该去「修」，也不该在文档里写成坑。
//     ★ 对比：`stats` 端点整窗无样本时 `hasData=false` 是**真会发生的**，
//       后端也**正确处理**了（回落 `persisted_state` 或 `none`）。
//
// (5) ★★★ `accuracy` 的 `accuracy_source` 有**三个语义完全不同的来源**：
//       `weighted_feedback` = 本窗口内按 (1) 实时算出
//       `persisted_state`   = 窗口内**没有反馈样本**，回落到**持久化状态里的旧值**
//       `none`              = 既没有反馈样本，也没有持久化状态
//     ⇒ 三个共用 `overall_accuracy` 一个字段，不看 `accuracy_source` 就分不出来。
//     ⇒ 持久化回落那一档是**旧数据**，必须标出来。
//
// (6) ★★ `hours` 是**静默回落 + 静默 clamp**，永不报错：
//       非数字 / < 1 → 24；> 720 → 720（`parseRoutingOptHours`）。
//     ★ 而 `stats` 的窗口是**写死的常量 24**，**根本没有参数**。
//     ⇒ 两个端点的窗口语义不同，不能共用一个控件。
//
// (7) ★★★ `metrics` 的 `LIMIT 2000` 在长窗口下**必然命中**，且**丢的是最旧的**。
//     SQL 注释自陈「LIMIT 是常量 2000 防护（30 天 × 288 桶/天 × 维度组合，
//     正常远小于此）」——★ 这条推理的算术**与自己的 LIMIT 矛盾**：
//     5 分钟桶 ⇒ 一天 288 个；30 天（`hours=720`）⇒ **8640 个桶，仅时间桶就超过 2000**。
//     好在 `ORDER BY time_bucket DESC` + `resp.Truncated = len(rows) >= 2000`
//     ⇒ 后端**自带截断标记**，丢的是最旧的数据。
//     ⇒ 页面必须说「只含最新的 2000 行」，并说清丢的是旧数据。
//
// ★ 另一条：`parameters` 在**没有激活版本**时返回 **404** `No active optimization state`
//   —— 那是「还没配置」，**不是**错误，要独立渲染。
// ★ 错误信封：这一族全部走 `http.Error`（**text/plain**），
//   与 `writeError`（嵌套 JSON）不同族。`api/client.ts` 的 `errorMessage`
//   能兜住纯文本（回退成原文）。
// ★ `routingOptPool() == nil` ⇒ **503** `Database not available`。
//
// ⚠️★★★★★ 第八个坑（★ 本轮**追到写入方**才确认，不是从 handler 看出来的）：
//
//   **四个端点里只有 `metrics` 读的是「物化聚合表」，另外三个都是实时读。**
//     · stats / accuracy  → `routing_feedback_log`（**实时**聚合）
//     · parameters        → `routing_optimization_state`（**实时**）
//     · metrics           → `routing_optimization_metrics`（**物化表**）
//   那张表由后台 sweep `bg/routing_metrics_aggregator.go` 从 `routing_feedback_log`
//   滚动写入（先 `DELETE … WHERE time_bucket >= $1` 再整窗 `INSERT`，靠事务级
//   advisory lock 串行化，避免同桶重复行让下游 SUM 双倍计数）。
//   ⇒ 后果：**sweep 没跑或落后时，`metrics` 会空/过期，而 `stats`/`accuracy` 却有数**
//     ⇒ 四个面板之间出现「不一致」**不是 bug**，是物化延迟。
//   ⇒ 页面据此标注 metrics 面板的数据来源是聚合表。
//
// ★ 同一处追出来的第二件事：`accuracy_rate` 的 SQL 是
//   `successful::float / NULLIF(total,0)` ⇒ **0..1 确认**，
//   但它量的是**成功率**，名字却叫 accuracy ⇒ 这是本族**第三个** accuracy 口径
//   （另两个：feedback 侧 `success` / 人工侧 `predicted=correct`，见 (2)）。
// ★ 第三件事：`human_accuracy_rate` = `(successful + 2*human_agree)/(total + 2*human_count)`，
//   且 `human_count = 0` 时写的是 **NULL**（列 CHECK 允许 NULL），不是 0、不是 1.0。
// ★ 第四件事：聚合 SQL 用 `GROUPING SETS ((), (task_type), (predicted_provider))`
//   ⇒ 同一批数据**同时**产出 global / task / provider / task_provider 四类行，
//   见 `MetricsRowDim`。

export const ROUTING_OPT_HOURS_DEFAULT = 24
export const ROUTING_OPT_HOURS_MIN = 1
export const ROUTING_OPT_HOURS_MAX = 720
/** `stats` 的窗口是**写死的常量**，不接受参数。见 (6)。 */
export const ROUTING_OPT_STATS_WINDOW_HOURS = 24
/** `metrics` 的后端硬上限（常量 2000）。见 (7)。 */
export const ROUTING_OPT_METRICS_ROW_CAP = 2000

/** 5 分钟一个桶 ⇒ 一天 288 个。用于把「会不会撞上限」讲清楚。 */
export const ROUTING_OPT_BUCKETS_PER_DAY = 288

// ── stats ──────────────────────────────────────────────────────────────────

export const ACCURACY_SOURCES = ['weighted_feedback', 'persisted_state', 'none'] as const
export type AccuracySource = (typeof ACCURACY_SOURCES)[number]

export interface RoutingOptStats {
  /** ★ 0..1 比率（不是百分数）。见 (3)。 */
  overall_accuracy: number
  /** ★ 三种语义完全不同的来源。见 (5)。 */
  accuracy_source: string
  parameter_version: number
  /** ★ 与 `human_samples` **是同一个值**（都取 `humanTotal`），不是另一项统计。 */
  human_annotations_used: number
  /** ★ 恒为 24（后端常量，不接受参数）。 */
  window_hours: number
  /** auto 反馈行数（`COUNT(*)`），**不含**命中数。 */
  auto_samples: number
  /** 人工纠正行数（`has_human_correction = TRUE`）。 */
  human_samples: number
  /** `*time.Time` + `omitempty` ⇒ **键可能整个不存在**。 */
  state_updated_at?: string
}

export function fetchRoutingOptStats(options?: RequestOptions): Promise<RoutingOptStats> {
  return req<RoutingOptStats>('GET', '/api/admin/routing-opt/stats', undefined, options)
}

/** ★ 准确率是 1:2 加权平均（人工单条算 2 条），且响应**不给命中数**。见 (1)。 */
export const ROUTING_OPT_ACCURACY_IS_WEIGHTED_2X = true
/** ★ auto 与 human 的「正确」是两种定义。见 (2)。 */
export const ROUTING_OPT_AUTO_AND_HUMAN_DIFFER = true

/**
 * 0..1 比率 → 百分数字符串。★ **必须 ×100**（量纲第 5 处）。
 *
 * 刻意不叫 `formatSuccessRatePct`（那份处理已经乘过 100 的 0..100 值），
 * 也不叫 `formatModelTaskRatePct`（那份虽是 0..1，但属于另一个族的指标）。
 * 三个函数处理三种量纲，同名是最容易埋「9000%」的地方。
 */
export function formatRoutingOptAccuracy(v: number | null | undefined): string {
  if (v === null || v === undefined || !Number.isFinite(v)) return '—'
  return (v * 100).toFixed(1) + '%'
}

/** 来源是否为「实时算出」。只有这一种可以当成当前准确率。 */
export function accuracyIsLive(source: string | null | undefined): boolean {
  return source === 'weighted_feedback'
}

/** 来源是否为「持久化状态回落」= **旧值**。见 (5)。 */
export function accuracyIsStaleFallback(source: string | null | undefined): boolean {
  return source === 'persisted_state'
}

/** 完全没有数据来源。 */
export function accuracyHasNoSource(source: string | null | undefined): boolean {
  return source === 'none'
}

/** 未知来源要如实显示，不能当「实时」。 */
export function accuracySourceKnown(source: string | null | undefined): boolean {
  return (ACCURACY_SOURCES as readonly string[]).includes(String(source ?? ''))
}

// ── accuracy ───────────────────────────────────────────────────────────────

export interface RoutingOptAccuracyBucket {
  hour: string
  task_type: string
  /** ★ 0..1 比率，且**恒有样本**（SQL 的 GROUP BY 保证）。见 (3)(4)。 */
  accuracy: number
  /** auto 样本数，**≥ 1**（GROUP BY 不产出空组）。见 (4)。 */
  samples: number
  human_samples: number
}

export interface RoutingOptAccuracyResponse {
  hours: number
  since: string
  buckets: RoutingOptAccuracyBucket[]
}

/** ★ 每个桶恒有样本 ⇒ 桶里 `accuracy: 0` 是真的 0%。见 (4)。 */
export const ROUTING_OPT_BUCKETS_ALWAYS_HAVE_SAMPLES = true

export interface RoutingOptAccuracyParams {
  hours?: number
}

export function fetchRoutingOptAccuracy(
  params: RoutingOptAccuracyParams = {},
  options?: RequestOptions,
): Promise<RoutingOptAccuracyResponse> {
  const qs = new URLSearchParams()
  // ★ 只发 1..720：非数字/<1 → 24，>720 → 720，**永不报错**（见 (6)）。
  if (typeof params.hours === 'number' && Number.isFinite(params.hours)) {
    const h = Math.trunc(params.hours)
    if (h >= ROUTING_OPT_HOURS_MIN && h <= ROUTING_OPT_HOURS_MAX) qs.set('hours', String(h))
  }
  const s = qs.toString()
  return req<RoutingOptAccuracyResponse>('GET', `/api/admin/routing-opt/accuracy${s ? '?' + s : ''}`, undefined, options)
}

// ── parameters ─────────────────────────────────────────────────────────────

export interface RoutingOptParameters {
  version: number
  /** JSONB 原样透传；列 NOT NULL 且 NULL 有 `{}` 兜底 ⇒ 一定是对象。 */
  classifier_weights: unknown
  confidence_thresholds: unknown
  recommender_weights: unknown
  exploration_rate: number
  learning_rate: number
  adaptation_window: number
  /** `*float64` ⇒ **键可能整个不存在**。 */
  overall_accuracy?: number
  activated_at: string
  created_by: string
  /** `*string` ⇒ **键可能整个不存在**（NULL 备注）。 */
  notes?: string
}

export function fetchRoutingOptParameters(options?: RequestOptions): Promise<RoutingOptParameters> {
  return req<RoutingOptParameters>('GET', '/api/admin/routing-opt/parameters', undefined, options)
}

// ── metrics ────────────────────────────────────────────────────────────────

export interface RoutingOptMetricsRow {
  time_bucket: string
  // ★ 下面两个是 GROUPING SETS 产出的**可空维度**：四类行里只有
  //   `task_provider` 两列同时有值；task/provider/global 三类的另一列都是 NULL（键缺失）。
  task_type?: string
  predicted_provider?: string
  total_requests: number
  successful_requests: number
  failed_requests: number
  /** ★ `successful::float / NULLIF(total,0)` ⇒ 0..1。**名字叫 accuracy，量纲是成功率**。 */
  accuracy_rate?: number
  avg_confidence?: number
  avg_latency_ms?: number
  avg_cost?: number
  p50_latency_ms?: number
  p95_latency_ms?: number
  p99_latency_ms?: number
  human_corrections: number
  /**
   * ★ 同样是 1:2 加权，但 `human_corrections = 0` 时写的是 **NULL**
   * （列 CHECK 允许 NULL），**不是 0、也不是 1.0** ⇒ 键可能不存在。
   */
  human_accuracy_rate?: number
}

export interface RoutingOptMetricsResponse {
  hours: number
  since: string
  rows: RoutingOptMetricsRow[]
  /** ★ 后端**自带**的截断标记（`len(rows) >= 2000`）。见 (7)。 */
  truncated: boolean
}

export interface RoutingOptMetricsParams {
  hours?: number
  taskType?: string
  provider?: string
}

export function fetchRoutingOptMetrics(
  params: RoutingOptMetricsParams = {},
  options?: RequestOptions,
): Promise<RoutingOptMetricsResponse> {
  const qs = new URLSearchParams()
  if (typeof params.hours === 'number' && Number.isFinite(params.hours)) {
    const h = Math.trunc(params.hours)
    if (h >= ROUTING_OPT_HOURS_MIN && h <= ROUTING_OPT_HOURS_MAX) qs.set('hours', String(h))
  }
  // ★ task_type / provider 都是**精确匹配**（`$n::text = '' OR col = $n`），
  //   不是子串；空串 = 不过滤 ⇒ 空白输入必须不发，否则变成「查名字叫空白」。
  const tt = (params.taskType ?? '').trim()
  if (tt !== '') qs.set('task_type', tt)
  const pv = (params.provider ?? '').trim()
  if (pv !== '') qs.set('provider', pv)
  const s = qs.toString()
  return req<RoutingOptMetricsResponse>('GET', `/api/admin/routing-opt/metrics${s ? '?' + s : ''}`, undefined, options)
}

/**
 * 长窗口下后端 LIMIT 会不会命中。
 *
 * ★ 只按**时间桶**估算（维度组合只会让它更容易命中）：
 *   `hours` × 12 个 5 分钟桶（每小时 12 个）⇒ 与 2000 比。
 */
export function metricsLikelyTruncated(hours: number): boolean {
  if (!Number.isFinite(hours) || hours < 0) return false
  const buckets = hours * 12
  return buckets >= ROUTING_OPT_METRICS_ROW_CAP
}

/**
 * `metrics` 的一行是哪种维度。
 *
 * ★★★ 后端聚合 SQL 用的是 `GROUPING SETS ((), (task_type), (predicted_provider))`
 *   （`bg/routing_metrics_aggregator.go` 的 INSERT … WITH agg），
 *   ⇒ **同一批数据会同时产出四类行**，而不是「按 task × provider 交叉」：
 *     · `task_provider` 两个维度都有 ⇒ 单个 (任务,供应商) 组合的分组行
 *     · `task`          只有 task_type      ⇒ 该任务跨供应商的汇总行
 *     · `provider`      只有 provider       ⇒ 该供应商跨任务的汇总行
 *     · `global`        两个都没有          ⇒ 整窗汇总行
 *   ⇒ 只分「有维度 / 没维度」两类会把**汇总行**当成明细行显示。
 *   ⇒ 页面必须逐类标注，否则用户会把同一批数据的汇总当成「又一条预测」。
 */
export type MetricsRowDim = 'task_provider' | 'task' | 'provider' | 'global'

/** ★ 与 `MetricsRowDim` 同源；供 `dynamicKeys.spec.ts` 拼接 i18n 键（不手抄）。 */
export const ROUTING_OPT_METRICS_DIMS = ['task_provider', 'task', 'provider', 'global'] as const

export function metricsRowDim(taskType: string | undefined, provider: string | undefined): MetricsRowDim {
  const t = taskType !== undefined && taskType !== null
  const p = provider !== undefined && provider !== null
  if (t && p) return 'task_provider'
  if (t) return 'task'
  if (p) return 'provider'
  return 'global'
}

/** 只有「两个维度都在」的行才是**单个预测组合**；其余三类都是汇总行。 */
export function metricsRowIsAggregate(taskType: string | undefined, provider: string | undefined): boolean {
  return metricsRowDim(taskType, provider) !== 'task_provider'
}