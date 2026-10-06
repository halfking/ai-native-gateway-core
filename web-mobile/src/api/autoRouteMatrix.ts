import { req, type RequestOptions } from './client'

// autoRouteMatrix.ts — auto-route 分析面的「矩阵」与「流量」两个面。
//   GET /api/admin/auto-route/analytics/matrix   模型 × 任务 热力矩阵
//   GET /api/admin/auto-route/analytics/flow     任务 → 模型 → 供应商 分层流量
//
// 鉴权：两条都是 superAdmin —— RegisterAnalyticsRoutes 由
// `analyticsH.RegisterAnalyticsRoutes(mux, h.superAdmin)` 挂载
// （admin/handler.go:1430），而 matrix/flow 是它里面的 adminWrap。
//
// 与 autoRouteInsights.ts 的关系：那一份答「**单个模型**的请求漏斗」，
// 这两份答「**全体模型**的横向对比」。漏斗是单点深挖，矩阵/流是全局鸟瞰。
//
// ⚠️★ 四个坑，全部是从源码推出来的，不是猜的：
//
// (1) ★★ **矩阵单元的 0 有两种完全不同的含义，且后端给不出判据。**
//     `handleMatrix` 的 cellMap 只装「DB 里真有行」的 (row,col) 对；
//     渲染成矩形时缺的格子由 Go 侧零值填充（`cells[i][j]` 初始即 0）。
//     ⇒ 矩阵里的 0 至少可能意味着：
//       · 该模型 × 该任务组合在窗口内**根本没有记录**
//       · **真的**是 0（success_rate 0% = 全部失败；cost 0 = 没花钱）
//     两者运营含义完全相反：前者「不用管」，后者「是故障」。
//     ★ 唯一能证明「这一格有数据」的办法是**切到 count 指标**
//       （`COUNT(*)` 对任何产出组 ≥1，所以 count=0 ⟺ 无记录）。
//     ⇒ UI 不能把 0 直接渲染成一个醒目的数字，也不能直接说成「无数据」；
//       只能弱化显示 + 常驻说明，让用户自己切 count 去确认。
//       见 `cellMayBeAbsent`。
//
// (2) ★★ **p95_ms 在 7d 窗口下是近似值，且两种窗口口径不同。**
//     `useMaterializedView`（analytics_materialized.go:53-58）只在
//     `windowLabel == "7d"` 且物化视图新鲜时返回 true：
//       · 7d + MV 新鲜 → `SUM(p95_latency_ms * request_count) / SUM(request_count)`
//         = **按请求数加权的「各小时 p95 的平均」**，不是真 p95
//       · 24h（MV 恒不启用）→ `percentile_cont(0.95) WITHIN GROUP (ORDER BY latency_ms)`
//         = 对原始行的**真 p95**
//     ⇒ 同一个模型，24h 的 p95 必然 ≥ 7d 的 p95（平均 of p95s < p95 of all）。
//     ★ 响应里**没有任何字段**告诉客户端这次走的是哪条路。
//     ⇒ UI 在 p95 指标下必须**永远**标注口径，不能因为「这次可能是真 p95」
//       就只在某些时候标注。见 `P95_IS_ALWAYS_APPROXIMATE`。
//
// (3) ★ **`__specified__` 是合成键，不是真实任务类型。**
//     analytics.go:36 `const SpecifiedModelTaskKey = "__specified__"`：
//     显式指定模型（网关没改写 body）的请求，任务类型为空时用这个占位。
//     直接渲染 `__specified__` 会让运维以为有个叫这个名字的任务类型。
//     ⇒ 客户端要把它映射成可读文案。见 `taskLabel`。
//
// (4) **matrix 的 `row` 与 `cols` 语义与直觉相反。**
//     2026-06-22 做过轴向交换：**行 = 模型，列 = 任务**。
//     端点注释写得很明确（analytics.go:271-275），但「matrix」这个词
//     会让人默认行是任务。⇒ 变量名与 UI 文案都要钉死这个方向。
//
// (5) 本模块**不引入降级字面值**。matrix/flow 的 meta 里没有
//     `approximate` / `degraded` —— 它们要么真算了要么报错。
//     唯一的「非精确」是 (2) 的 p95 口径，已单列。

// ── matrix ────────────────────────────────────────────────────────────────

/** window 合法值。★ 从 autoRouteInsights 收口，避免两处各写一份字面量。 */
export { ANALYTICS_WINDOWS } from './autoRouteInsights'
import { ANALYTICS_WINDOWS } from './autoRouteInsights'

/** `row` 合法值。空串 = 不发（后端默认 `task_type`）。 */
export const MATRIX_ROWS = ['', 'task_type', 'work_type'] as const
/** `metric` 合法值。空串 = 不发（后端默认 `count`）。 */
export const MATRIX_METRICS = ['', 'count', 'success_rate', 'p95_ms', 'cost_usd'] as const

export type MatrixRowDim = (typeof MATRIX_ROWS)[number]
export type MatrixMetric = (typeof MATRIX_METRICS)[number]

export interface MatrixMeta {
  window: string
  metric: string
  row: string
  /** canonical 名 → 它归并掉的 raw 名数组。用于「这个模型有别名」的提示。 */
  row_aliases: Record<string, string[]> | null
}

export interface MatrixResponse {
  /** ★ 行 = **模型**（canonical 名），不是任务。见文件头 (4)。 */
  rows: string[] | null
  /** ★ 列 = 任务类型（或 work_type，取决于 `row` 参数）。 */
  cols: string[] | null
  /** `cells[i][j]` 对应 `rows[i]` × `cols[j]`。缺格由后端填 0。 */
  cells: number[][] | null
  meta: MatrixMeta
}

export interface MatrixParams {
  window?: '' | '24h' | '7d'
  row?: MatrixRowDim
  metric?: MatrixMetric
}

export function fetchRouteMatrix(params: MatrixParams = {}, options?: RequestOptions): Promise<MatrixResponse> {
  const qs = new URLSearchParams()
  // ★ 运行时也要拦：TypeScript 的 const 断言**编译后不存在**，
  //   `as any` 就能塞进 `metric: 'latency'`。而后端对非法 metric 是 400
  //   （analytics.go:114-117）且**不做** ToLower。
  //   ⇒ 词表外的值不发，退回后端默认（count / task_type）。
  //   这不是防御性编程的口癖，是这里唯一能挡住 400 的地方。
  if (params.window && ANALYTICS_WINDOWS.includes(params.window)) qs.set('window', params.window)
  if (params.row && MATRIX_ROWS.includes(params.row)) qs.set('row', params.row)
  if (params.metric && MATRIX_METRICS.includes(params.metric)) qs.set('metric', params.metric)
  const s = qs.toString()
  return req<MatrixResponse>('GET', `/api/admin/auto-route/analytics/matrix${s ? '?' + s : ''}`, undefined, options)
}

// ── metric 口径 ────────────────────────────────────────────────────────────

export type MatrixMetricKind = 'count' | 'ratio' | 'duration' | 'money'

const METRIC_KIND: Record<string, MatrixMetricKind> = {
  count: 'count',
  success_rate: 'ratio',
  p95_ms: 'duration',
  cost_usd: 'money',
}

export function metricKind(metric: string | null | undefined): MatrixMetricKind {
  return METRIC_KIND[(metric ?? '').toLowerCase()] ?? 'count'
}

/**
 * ★ 0 在这个指标下**有多可信**。
 *
 * - `count`：0 **必然**是「无记录」—— `COUNT(*)` 对任何产出组 ≥ 1。
 *   ⇒ 可以确定地下结论。
 * - `success_rate`：0 可能是真 0%（**全部失败**），也可能是「无记录」。
 *   ⇒ 不能下结论。
 * - `p95_ms` / `cost_usd`：同 success_rate，两种都可能是 0。
 *
 * 返回 true = 「这一格的 0 很可能只是没有记录」，UI 应弱化显示。
 */
export function cellMayBeAbsent(metric: string | null | undefined): boolean {
  return metricKind(metric) !== 'count'
}

/**
 * ★ p95 永远是近似 —— 见文件头 (2)。
 *
 * 7d 走物化视图时是「按请求数加权的各小时 p95 平均」，
 * 24h 才是对原始行的真 p95。客户端拿不到本次走的哪条路，
 * 所以只能在 p95 指标下**无条件**标注口径。
 * 若只在「7d」时标注，24h 那次就会显示成一个精确值 —— 而它虽然是真 p95，
 * 也不该和别处的 p95 直接比较。
 */
export const P95_IS_ALWAYS_APPROXIMATE = true

/** 值 → 显示串。**每种指标口径不同**，套同一个 fmtNum 就是在撒谎。 */
export function formatMatrixValue(metric: string | null | undefined, v: number | null | undefined): string {
  const n = typeof v === 'number' && Number.isFinite(v) ? v : null
  if (n === null) return '—'
  switch (metricKind(metric)) {
    case 'count':
      // 大数用千分位（不是 fmtCompact 的 1.2k —— 矩阵要能对数）
      return Math.round(n).toLocaleString('en-US')
    case 'ratio':
      // ★ 成功率是 0..1 的比例，不是百分数。×100 之前先判 0。
      return n === 0 ? '—' : `${(n * 100).toFixed(1)}%`
    case 'duration':
      return `${Math.round(n)}ms`
    case 'money':
      return `$${n.toFixed(n < 0.01 && n > 0 ? 4 : 2)}`
  }
}

// ── __specified__ 合成键 ───────────────────────────────────────────────────

/** analytics.go:36。显式指定模型的请求，任务类型为空时的占位键。 */
export const SPECIFIED_MODEL_TASK_KEY = '__specified__'

/**
 * 合成键 → 可读标签。
 *
 * ★ 直接渲染 `__specified__` 会让运维以为有个叫这个名字的真实任务类型
 *   （analytics.go:140 的 colExpr 会把它当成一列产出）。
 *   而它其实表示「请求显式指定了模型，网关没做任务分类」。
 *
 * @param raw 后端给的 col key
 * @param translate i18n 翻译函数（由视图注入，避免 api 层依赖 i18n）
 */
export function taskLabel(raw: string, translate: (key: string) => string): string {
  if (raw === SPECIFIED_MODEL_TASK_KEY) return translate('matrix.specified')
  if (raw === 'unknown') return translate('matrix.unknownTask')
  return raw
}

// ── flow ──────────────────────────────────────────────────────────────────

export interface FlowNode {
  /** 带层前缀：`task:` / `model:` / `prov:`。 */
  id: string
  label: string
  /** 0 = 任务，1 = 模型，2 = 供应商。 */
  layer: number
}

export interface FlowLink {
  source: string
  target: string
  value: number
  /** 原始任务类型，用来给链路染色。 */
  task_type: string
}

/** ★ meta 是 `map[string]string` —— 全字符串（本仓库第四次同款）。 */
export interface FlowMeta {
  window: string
  [k: string]: string
}

export interface FlowResponse {
  nodes: FlowNode[] | null
  links: FlowLink[] | null
  meta: FlowMeta
}

export interface FlowParams {
  window?: '' | '24h' | '7d'
}

export function fetchRouteFlow(params: FlowParams = {}, options?: RequestOptions): Promise<FlowResponse> {
  const qs = new URLSearchParams()
  // ★ 同 fetchRouteMatrix：窗口也是 400（analytics.go:62-71），
  //   词表外一律不发，退回后端默认 7d。
  if (params.window && ANALYTICS_WINDOWS.includes(params.window)) qs.set('window', params.window)
  const s = qs.toString()
  return req<FlowResponse>('GET', `/api/admin/auto-route/analytics/flow${s ? '?' + s : ''}`, undefined, options)
}

export const FLOW_LAYER_COUNT = 3
export const FLOW_LAYER_PREFIXES = ['task:', 'model:', 'prov:'] as const

/** 节点 ID 的层级前缀。**未知前缀一律当最后一层**（保守：新值排最后，不排中间）。 */
export function layerPrefixOf(id: string | null | undefined): string {
  const v = id ?? ''
  return FLOW_LAYER_PREFIXES.find((p) => v.startsWith(p)) ?? FLOW_LAYER_PREFIXES[FLOW_LAYER_COUNT - 1]!
}

/** 按层分组的节点。层号来自后端 `node.layer`（不是从前缀推的）。 */
export function nodesByLayer(nodes: FlowNode[] | null | undefined): FlowNode[][] {
  const out: FlowNode[][] = Array.from({ length: FLOW_LAYER_COUNT }, () => [])
  for (const n of nodes ?? []) {
    const i = n.layer >= 0 && n.layer < FLOW_LAYER_COUNT ? n.layer : FLOW_LAYER_COUNT - 1
    out[i]!.push(n)
  }
  return out
}

/** 从某节点出发的链路。id 前缀不同时是 0 条（不可跨层直连）。 */
export function linksFrom(links: FlowLink[] | null | undefined, nodeId: string): FlowLink[] {
  return (links ?? []).filter((l) => l.source === nodeId)
}
