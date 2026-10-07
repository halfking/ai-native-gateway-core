import { req, type RequestOptions } from './client'

/**
 * annotations.ts — 人工标注工作台的三条只读端点（2026-10-08，第六十一批）。
 *
 * GET /api/admin/annotations/samples              （handler.go:1386，admin 档）
 * GET /api/admin/annotations/first-turn-samples   （handler.go:1389，admin 档）
 * GET /api/admin/annotations/stats                （handler.go:1390，admin 档）
 *
 * ⚠️ 三条走 `json.NewEncoder(w).Encode(resp)` ⇒ **裸 JSON，无信封**。
 * 与 `api/dashboard.ts`（`{success,data,metadata}`）不是一个家族。
 *
 * ## 为什么不碰写操作
 * `POST /annotations`（handleCreateAnnotation）、`POST /annotations/batch`
 * （handleBatchAnnotate）、`DELETE /annotations/{request_id}`
 * （handleDeleteAnnotation）三条**会改标注事实**且互相不可逆
 * （删一条标注会改变该样本的 accuracy 统计口径），本批不碰。
 *
 * ## ★★★★ 本族最刺眼的一处：零标注时 stats 整条 500
 *
 * `GetOverallStats`（annotation/stats.go:31-59）是一条**无聚合子句**的
 * `SELECT ... FROM annotation_stats`（单行汇总表）。
 * ⇒ **表里没有行时返回 `pgx.ErrNoRows`**
 * ⇒ handler.go:1136-1139 直接 `writeInternalTextErr` ⇒ **HTTP 500**。
 *
 * 且四个块（overall / by_provider / by_annotator / by_reason）是**串联早退**的：
 * 任一块出错整条端点就 500，客户端拿不到「部分可用」。
 * ⇒ 客户端**不能**把 500 读成「统计为零」——
 *   「一条标注都没有」与「数据库查不通」在响应上无法区分，
 *   唯一区别是 500 与 200，而 200 那条路上整体可能是 `null`。
 */

/* ═══════════════════════════════════════════════════════════════════════════
 * A. GET /api/admin/annotations/stats
 * ═══════════════════════════════════════════════════════════════════════════ */

/** annotation/types.go:60-68，逐字照抄。 */
export interface AnnotationStats {
  total_annotations: number
  correct_count: number
  incorrect_count: number
  accuracy_percent: number
  num_annotators: number
  /** ★ `*time.Time` ⇒ 没有标注时是 `null`，不是零值时间。 */
  first_annotation_at: string | null
  last_annotation_at: string | null
}

/** annotation/types.go:71-78。 */
export interface ProviderAccuracy {
  provider: string
  total_predictions: number
  correct_predictions: number
  incorrect_predictions: number
  accuracy_percent: number
  avg_confidence: number
}

/** annotation/types.go:81-91。★ 这两个是 `time.Time`（非指针）⇒ 恒有值。 */
export interface AnnotatorStats {
  annotator: string
  total_annotations: number
  correct_count: number
  incorrect_count: number
  accuracy_percent: number
  first_annotation_at: string
  last_annotation_at: string
  hours_span: number
}

/**
 * annotation/types.go:94-98。
 * ★★ 字段名 `Count` / `Percent` 的 JSON 键是 **`count` / `percentage`**
 *    （不是 `percent`）⇒ 按字段名去读 JSON 会读到 undefined。
 */
export interface ReasonDistribution {
  reason: string
  count: number
  percentage: number
}

/** admin/annotation_handler.go:130-135，四个键**全部无 omitempty**。 */
export interface AnnotationStatsResponse {
  /**
   * ★★ **指针字段**：Go 侧 `*annotation.AnnotationStats`。
   * 正常路径下 `GetOverallStats` 成功即非 nil，
   * 但**客户端必须容忍 `null`** —— 它是本族唯一可能为 null 的块。
   */
  overall: AnnotationStats | null
  by_provider: ProviderAccuracy[]
  by_annotator: AnnotatorStats[]
  by_reason: ReasonDistribution[]
}

export const ANNOTATION_STATS_KEYS = ['overall', 'by_provider', 'by_annotator', 'by_reason'] as const
export const ANNOTATION_STATS_OVERALL_KEYS = [
  'total_annotations', 'correct_count', 'incorrect_count', 'accuracy_percent',
  'num_annotators', 'first_annotation_at', 'last_annotation_at',
] as const
export const PROVIDER_ACCURACY_KEYS = [
  'provider', 'total_predictions', 'correct_predictions', 'incorrect_predictions',
  'accuracy_percent', 'avg_confidence',
] as const
export const ANNOTATOR_STATS_KEYS = [
  'annotator', 'total_annotations', 'correct_count', 'incorrect_count',
  'accuracy_percent', 'first_annotation_at', 'last_annotation_at', 'hours_span',
] as const
export const REASON_DISTRIBUTION_KEYS = ['reason', 'count', 'percentage'] as const

export function fetchAnnotationStats(options?: RequestOptions): Promise<AnnotationStatsResponse> {
  return req<unknown>('GET', '/api/admin/annotations/stats', undefined, options)
    .then(unwrapAnnotationStats)
}

/** ★ `overall === null` 时**不**要求它的子键 —— 它整个块都没有。 */
export function unwrapAnnotationStats(resp: unknown): AnnotationStatsResponse {
  const d = requireObject(resp, '标注统计')
  requireKeys(d, ANNOTATION_STATS_KEYS, '标注统计')
  requireArray(d.by_provider, 'by_provider')
  requireArray(d.by_annotator, 'by_annotator')
  requireArray(d.by_reason, 'by_reason')

  if (d.overall !== null && d.overall !== undefined) {
    if (typeof d.overall !== 'object' || Array.isArray(d.overall)) {
      throw new Error(`标注统计 overall 不是对象（实得 ${Array.isArray(d.overall) ? 'array' : typeof d.overall}）`)
    }
    requireKeys(d.overall, ANNOTATION_STATS_OVERALL_KEYS, '标注统计 overall')
  }
  d.by_provider.forEach((row, i) => {
    if (!isPlainObject(row)) throw new Error(`标注统计 by_provider[${i}] 不是对象`)
    requireKeys(row, PROVIDER_ACCURACY_KEYS, `标注统计 by_provider[${i}]`)
  })
  d.by_annotator.forEach((row, i) => {
    if (!isPlainObject(row)) throw new Error(`标注统计 by_annotator[${i}] 不是对象`)
    requireKeys(row, ANNOTATOR_STATS_KEYS, `标注统计 by_annotator[${i}]`)
  })
  d.by_reason.forEach((row, i) => {
    if (!isPlainObject(row)) throw new Error(`标注统计 by_reason[${i}] 不是对象`)
    requireKeys(row, REASON_DISTRIBUTION_KEYS, `标注统计 by_reason[${i}]`)
  })
  return d as unknown as AnnotationStatsResponse
}

/**
 * ★★ `accuracy_percent` 的 0 与「一条标注都没有」在值上不可分
 * （`GetOverallStats` 从单行汇总表读，没有 COALESCE，0 就是真 0）。
 * ⇒ 判据只能看 `total_annotations`：为 0 时那个百分数**无意义**。
 */
export function annotationAccuracyMeaningless(s: AnnotationStats): boolean {
  return s.total_annotations === 0
}

/** ★ 正确+错误对不上总数 ⇒ 计数器自相矛盾，必须单独报警。 */
export function annotationCountsContradict(s: AnnotationStats): boolean {
  return s.correct_count + s.incorrect_count > s.total_annotations
}

/** ★ `overall === null` ⇒ 整个统计块不可用（不是「零标注」）。 */
export function annotationOverallUnavailable(r: AnnotationStatsResponse): boolean {
  return r.overall === null
}

/** ★ 三个分布块全空 ⇒ 没有任何可聚合的标注（与 by_provider 空不是一回事）。 */
export function annotationAllDistributionsEmpty(r: AnnotationStatsResponse): boolean {
  return r.by_provider.length === 0 && r.by_annotator.length === 0 && r.by_reason.length === 0
}

/** ★ 逐 provider 校验：correct + incorrect 不得超过 total。 */
export function providerAccuracyContradicts(p: ProviderAccuracy): boolean {
  return p.correct_predictions + p.incorrect_predictions > p.total_predictions
}

/* ═══════════════════════════════════════════════════════════════════════════
 * B. GET /api/admin/annotations/samples
 * ═══════════════════════════════════════════════════════════════════════════ */

/** admin/annotation_handler.go:54-62。★ `strategy` 带 omitempty ⇒ 条件键。 */
export interface SamplesResponse {
  samples: AnnotationSample[]
  total: number
  /**
   * ★★ **条件键**：`resp.Strategy = strategy` 只在 `strategy != "recent"` 时写
   * （annotation_handler.go:225-227），而 recent 是默认值
   * ⇒ 键缺失 = recent，**不是**「策略未知」。
   */
  strategy?: string
}

/** 子结构字段本层不逐字钉（querySamples 拼装，形状随 SELECT 列走），
 *  但**元素必须是对象**这件事要钉 —— 否则数组里混进 null 时模板会炸。 */
export type AnnotationSample = Record<string, unknown>

export const SAMPLES_KEYS = ['samples', 'total'] as const

export const SAMPLES_STRATEGY_RECENT = 'recent'
export const SAMPLES_STRATEGY_DISAGREEMENT = 'disagreement'
export const SAMPLES_STRATEGY_STRATIFIED = 'stratified'
export const SAMPLES_STRATEGIES = [
  SAMPLES_STRATEGY_RECENT,
  SAMPLES_STRATEGY_DISAGREEMENT,
  SAMPLES_STRATEGY_STRATIFIED,
] as const
export type SamplesStrategy = (typeof SAMPLES_STRATEGIES)[number]

export const SAMPLES_PAGE_DEFAULT = 1
export const SAMPLES_SIZE_DEFAULT = 50
export const SAMPLES_SIZE_MAX = 200
export const SAMPLES_PER_STRATA_DEFAULT = 5
export const SAMPLES_PER_STRATA_MAX = 20

export interface SamplesQuery {
  page?: number
  size?: number
  startDate?: string
  endDate?: string
  minConfidence?: number
  /**
   * ★ 后端是 `if maxConfidence == 0 { maxConfidence = 1.0 }`
   *   （annotation_handler.go:168-170）⇒ 填 0 会被改成 1.0。
   */
  maxConfidence?: number
  /** ★ 条件参数：`query.Has("annotated")` 才解析，`== "true"` 为真。 */
  annotated?: boolean
  annotator?: string
  strategy?: SamplesStrategy
  /** ★ 仅 stratified 生效；越界静默钳位到 [1,20]。 */
  perStrata?: number
}

/**
 * ★★ **本族最容易被类推错的地方**：三个参数三种越界行为，
 * 与 dashboard 的「静默回落 7」、drill 的「clamp [1,90]」都**不同**。
 *
 *   page        <1 → 1（静默，handler.go:153-155）
 *   size        <1 → 50；>200 → 200（**两个方向的静默钳位**，:156-162）
 *   per_strata  <1 → 5；>20 → 20（钳位，:240-245）
 *   strategy    非法值 → **400**（唯一一个会报错的，:198-200）
 */
export function samplesPageEffective(page: number | undefined): number {
  if (page === undefined || !Number.isInteger(page)) return SAMPLES_PAGE_DEFAULT
  return page < SAMPLES_PAGE_DEFAULT ? SAMPLES_PAGE_DEFAULT : page
}

export function samplesSizeEffective(size: number | undefined): number {
  if (size === undefined || !Number.isInteger(size)) return SAMPLES_SIZE_DEFAULT
  if (size < 1) return SAMPLES_SIZE_DEFAULT
  return size > SAMPLES_SIZE_MAX ? SAMPLES_SIZE_MAX : size
}

export function samplesPerStrataEffective(n: number | undefined): number {
  if (n === undefined || !Number.isInteger(n)) return SAMPLES_PER_STRATA_DEFAULT
  if (n < 1) return SAMPLES_PER_STRATA_DEFAULT
  return n > SAMPLES_PER_STRATA_MAX ? SAMPLES_PER_STRATA_MAX : n
}

/** ★ 非法策略会 400 ⇒ 前端**不该发**，发之前先过这道。 */
export function samplesStrategyValid(s: string | null | undefined): boolean {
  if (s === undefined) return true
  if (s === null) return false
  return (SAMPLES_STRATEGIES as readonly string[]).includes(s)
}

function samplesSuffix(q: SamplesQuery | undefined): string {
  const qs = new URLSearchParams()
  if (!q) return ''
  if (q.page !== undefined) qs.set('page', String(q.page))
  if (q.size !== undefined) qs.set('size', String(q.size))
  if (q.startDate !== undefined) qs.set('start_date', q.startDate)
  if (q.endDate !== undefined) qs.set('end_date', q.endDate)
  if (q.minConfidence !== undefined) qs.set('min_confidence', String(q.minConfidence))
  if (q.maxConfidence !== undefined) qs.set('max_confidence', String(q.maxConfidence))
  // ★ 后端用 `query.Has("annotated")` 判断是否筛选 ⇒ 必须发字面量
  if (q.annotated !== undefined) qs.set('annotated', q.annotated ? 'true' : 'false')
  if (q.annotator !== undefined) qs.set('annotator', q.annotator)
  if (q.strategy !== undefined) qs.set('strategy', q.strategy)
  if (q.perStrata !== undefined) qs.set('per_strata', String(q.perStrata))
  return qs.toString() ? `?${qs}` : ''
}

export function fetchSamples(
  q?: SamplesQuery,
  options?: RequestOptions,
): Promise<SamplesResponse> {
  if (q?.strategy !== undefined && !samplesStrategyValid(q.strategy)) {
    // ★ 前端先拦一道：后端会 400（annotation_handler.go:198-200），
    //   而一个 400 的往返对用户毫无价值。
    return Promise.reject(new Error(`采样策略非法：${String(q.strategy)}`))
  }
  return req<unknown>('GET', `/api/admin/annotations/samples${samplesSuffix(q)}`, undefined, options)
    .then(unwrapSamples)
}

export function unwrapSamples(resp: unknown): SamplesResponse {
  const d = requireObject(resp, '标注样本')
  requireKeys(d, SAMPLES_KEYS, '标注样本')
  requireArray(d.samples, '标注样本 samples')
  d.samples.forEach((row, i) => {
    if (!isPlainObject(row)) throw new Error(`标注样本 samples[${i}] 不是对象`)
  })
  if (d.strategy !== undefined && typeof d.strategy !== 'string') {
    throw new Error('标注样本 strategy 不是字符串')
  }
  return d as unknown as SamplesResponse
}

/** ★ 键缺失 = recent（默认口径），不是「策略未知」。 */
export function samplesStrategyEffective(r: SamplesResponse): string {
  return r.strategy ?? SAMPLES_STRATEGY_RECENT
}

/** ★ 本页取满 + total 还有更多 ⇒ 该显示翻页。 */
export function samplesHasNextPage(r: SamplesResponse, size: number): boolean {
  const pageSize = samplesSizeEffective(size)
  return r.samples.length >= pageSize && r.total > r.samples.length
}

/* ═══════════════════════════════════════════════════════════════════════════
 * C. GET /api/admin/annotations/first-turn-samples
 * ═══════════════════════════════════════════════════════════════════════════ */

/** admin/annotation_handler.go:124-127。★ **没有** strategy 键。 */
export interface FirstTurnSamplesResponse {
  samples: AnnotationSample[]
  total: number
}

export const FIRST_TURN_KEYS = ['samples', 'total'] as const

export interface FirstTurnQuery {
  page?: number
  size?: number
  /**
   * ★★ 格式是 **YYYY-MM-DD**（`time.Parse("2006-01-02", …)`），
   * 格式错 **400**（resolveFirstTurnDateRange，handler.go:635-637）。
   * ★ 缺省不是「不限」而是**今天（UTC）**（:628-633）—— 与 samples 的
   *   缺省不限窗口语义相反。
   */
  startDate?: string
  endDate?: string
}

export const FIRST_TURN_DATE_PATTERN = /^\d{4}-\d{2}-\d{2}$/

export function firstTurnDateValid(s: string | null | undefined): boolean {
  if (s === undefined) return true
  if (s === null) return false
  return FIRST_TURN_DATE_PATTERN.test(s)
}

function firstTurnSuffix(q: FirstTurnQuery | undefined): string {
  const qs = new URLSearchParams()
  if (!q) return ''
  if (q.page !== undefined) qs.set('page', String(q.page))
  if (q.size !== undefined) qs.set('size', String(q.size))
  if (q.startDate !== undefined) qs.set('start_date', q.startDate)
  if (q.endDate !== undefined) qs.set('end_date', q.endDate)
  return qs.toString() ? `?${qs}` : ''
}

export function fetchFirstTurnSamples(
  q?: FirstTurnQuery,
  options?: RequestOptions,
): Promise<FirstTurnSamplesResponse> {
  if (!firstTurnDateValid(q?.startDate) || !firstTurnDateValid(q?.endDate)) {
    return Promise.reject(new Error('日期格式必须是 YYYY-MM-DD，否则后端返回 400'))
  }
  return req<unknown>(
    'GET',
    `/api/admin/annotations/first-turn-samples${firstTurnSuffix(q)}`,
    undefined,
    options,
  ).then(unwrapFirstTurnSamples)
}

export function unwrapFirstTurnSamples(resp: unknown): FirstTurnSamplesResponse {
  const d = requireObject(resp, '首轮样本')
  requireKeys(d, FIRST_TURN_KEYS, '首轮样本')
  requireArray(d.samples, '首轮样本 samples')
  d.samples.forEach((row, i) => {
    if (!isPlainObject(row)) throw new Error(`首轮样本 samples[${i}] 不是对象`)
  })
  // ★ 反向检测：samples 那个端点有 strategy 键；这里**没有**。
  //   拿到多出来的键说明串了端点，形状对但语义错。
  if ('strategy' in d) {
    throw new Error('首轮样本 拿到的是 /annotations/samples 的形状，本端点没有 strategy 键')
  }
  return d as unknown as FirstTurnSamplesResponse
}

/* ── 内部工具（各端点契约不同，不抽通用解包器） ────────────────────────── */

function isPlainObject(v: unknown): v is Record<string, unknown> {
  return !!v && typeof v === 'object' && !Array.isArray(v)
}

function requireObject(resp: unknown, where: string): Record<string, unknown> {
  if (!isPlainObject(resp)) {
    const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
    throw new Error(`${where} 响应形状不符：期望裸对象，实得 ${actual}`)
  }
  // ★ 反向检测：dashboardapi 家族是信封形状，跨家族误用会在这里被挡住
  if ('success' in resp && 'timestamp' in resp) {
    throw new Error(`${where} 拿到的是 dashboardapi 信封形状，本族应为裸 JSON`)
  }
  return resp
}

function requireKeys(obj: object, keys: readonly string[], where: string): void {
  const d = obj as Record<string, unknown>
  const missing = keys.filter((k) => !(k in d))
  if (missing.length > 0) {
    throw new Error(`${where} 缺 ${missing.length} 个键（${missing.join(', ')}）`)
  }
}

function requireArray(v: unknown, where: string): asserts v is unknown[] {
  if (!Array.isArray(v)) throw new Error(`${where} 不是数组`)
}
