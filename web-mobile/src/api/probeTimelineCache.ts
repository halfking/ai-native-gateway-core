import { req, type RequestOptions } from './client'

// probeTimelineCache.ts — probe 线的最后两个只读端点。
//   GET  /api/admin/probe/availability-timeline   模型级可用性时间线
//   GET  /api/admin/probe/cache-state             Redis 可用性缓存快照
//
// 鉴权：都在 `RegisterProbeDashboardRoutes(mux, wrapAdmin)`
//（admin/probe_dashboard.go:1900-1909）⇒ **admin 档**，不设 requiresRole。
//
// ─────────────────────────────────────────────────────────────────────────
// ⚠️⚠️ 两个端点各有一个**静默截断**，而响应里**没有任何字段说「被截断了」**。
//    这是本文件存在的第一理由。
//
// (1) ★★ `availability-timeline` 的 `LIMIT 500` 是**写死在 SQL 里的**：
//     `... ORDER BY raw_model_name, hour_bucket DESC LIMIT 500`（:1154）。
//     视图 `v_model_availability_timeline` 本身只保留**近 24 小时**
//     （`WHERE created_at >= now() - interval '24 hours'`）且按小时聚合
//     ⇒ 24 行/模型 ⇒ 500 行 ≈ **20 个模型**。
//     响应里只有 `timeline` 与 `total`，**没有** `truncated` 之类。
//     ⇒ 唯一可推导的信号是 `total === 500`（撞到上限）。
//        「显示 500 条」会被读成「全部」，而真相是「只显示了前 500 条」。
//        见 `timelineAtCap`。
//
// (2) ★★ `cache-state` 的枚举被 `ScanKeys` 截断在 **4096 个 key**
//     （bg/model_availability_reader.go:150-153，注释明写
//     「Cap admin enumeration to keep the endpoint cheap」）。
//     handler 的注释还提到 per-model / per-credential 的 256 上限。
//     同样**没有**截断标记。⇒ 同样只能靠 `count === 4096` 推导。
//
// ─────────────────────────────────────────────────────────────────────────
// ⚠️ 两个「同名不同义」的量纲陷阱（本仓库已攒到第 3 次）：
//
// (3) ★★★ `success_rate` 在**这个视图里已经乘过 100**。
//     `v_model_availability_timeline` 的定义（baseline SQL :18772）：
//       round(count(ok) * 100.0 / count(*), 2) AS success_rate
//     ⇒ 值域 **0..100**，直接加 '%' 即可。
//     ★ 而**同一次会话**里的 `probe/dashboard` 的 `avg_success_rate_7d`
//       是 0..1（`ModelHealthSummary` 从 DB 直取），**必须 ×100**。
//       两个视图都是「成功率」，量纲相反，抄错就是差 100 倍。
//     ⇒ 这里显式提供 `formatSuccessRatePct`，把「已经是百分数」这件事
//       写进函数名，避免调用方再乘一次。
//
// (4) ★ `model` 参数在两个端点**语义不同**：
//     · availability-timeline：`WHERE raw_model_name = $1` ⇒ **精确匹配**
//     · probe/dashboard     ：`raw_model_name ILIKE '%q%'` ⇒ **子串匹配**
//     （:1149-1152 vs :675-679）
//     ⇒ 同一个输入框在两页上行为不同，必须各自说明，否则用户会以为
//       「在 dashboard 里能搜到，在这里搜不到」是数据问题。
//
// (5) ★ `outbound_model_name` 在这个视图里**恒等于** `raw_model_name`
//     （baseline SQL :18767 `raw_model_name AS outbound_model_name`）。
//     ⇒ 不显示「raw → outbound」的别名箭头，那会显示成
//       「gpt-4o → gpt-4o」这种废话。
//
// (6) ★ `avg_latency_ms` 是 `avg(latency_ms) FILTER (WHERE status='ok')`
//     ⇒ 某一小时**没有成功探测**时它是 SQL NULL；Go 侧是 `*float64` +
//     omitempty ⇒ **JSON 里没有这个键**。
//     ⇒ 「没有成功的探测」不是「延时 0ms」。
//
// (7) ★ `timeline` 是 **nil slice**（`var timeline []TimelinePoint`），
//     空时编码成 **`null`** 而不是 `[]` —— 这正是 `tuning/proposals`
//     端点专门修过的问题（那里加了 `results := make([]..., 0)`）。
//     ⇒ `.length` / `.map` 会直接崩，必须兜。
//
// (8) ★★ `cache-state` 的 `format=prom|prometheus` 返回的是
//     **text/plain 的 Prometheus 文本格式**，不是 JSON
//     （`writeCacheStateProm`，:2044 起，`Content-Type: text/plain; version=0.0.4`）。
//     ⇒ **本模块永远不发 `format`**。同一个 URL 换个参数就换了序列化格式，
//       按 JSON 解析会得到一坨乱码而不是一个错误。
//
// (9) ★ `cache-state` 的 `credential_id` 是 `strconv.Atoi` 后
//     `if err == nil && v > 0` 才采纳 ⇒ **非法值 / 0 / 负数一律静默忽略**，
//     等价于「不过滤」，返回**全量**而不是 400。
//     ⇒ 这是本仓库的**第七种**越界语义。前端要么不发、要么发正整数，
//        并在发之前自己判；不能发一个非法值然后以为「后端会告诉我错了」。
//
// (10) ★ `cache-state` 的 503 有**两个**不同来源，含义完全不同：
//     · `availability reader not wired`（:1973）⇒ 这个部署没接 Redis 读取器
//     · `redis client unavailable`（:2011）⇒ 接了但客户端类型不对
//     ⇒ 两者都是「**读不到**」，与「读到了但是空」必须分开显示。
//        页面绝不能把 503 显示成「缓存里什么都没有」。

// ── availability-timeline ─────────────────────────────────────────────────

export interface AvailabilityPoint {
  raw_model_name: string
  /** ★ 恒等于 raw_model_name（视图里就是同一个表达式的别名）。见文件头 (5)。 */
  outbound_model_name: string
  /** 按小时聚合（date_trunc('hour', created_at)）。 */
  hour_bucket: string
  total_probes: number
  successful_probes: number
  failed_probes: number
  /** ★ 视图里**已经乘过 100**，值域 0..100。见文件头 (3)。 */
  success_rate: number
  /** ★ omitempty 指针：字段缺失 = 那一小时没有成功探测，不是 0ms。见文件头 (6)。 */
  avg_latency_ms?: number | null
  probed_credentials: number
  successful_credentials: number
  failed_credentials: number
}

export interface AvailabilityTimelineResponse {
  /** ★ 空时是 `null`（nil slice），不是 `[]`。见文件头 (7)。 */
  timeline: AvailabilityPoint[] | null
  total?: number | null
}

/** SQL 里写死的上限（:1154）。24 行/模型 ⇒ ≈20 个模型。 */
export const AVAILABILITY_TIMELINE_ROW_CAP = 500

export interface AvailabilityTimelineParams {
  /** ★ **精确匹配**（`raw_model_name = $1`），不是子串。见文件头 (4)。 */
  model?: string
}

export function fetchAvailabilityTimeline(
  params: AvailabilityTimelineParams = {},
  options?: RequestOptions,
): Promise<AvailabilityTimelineResponse> {
  const qs = new URLSearchParams()
  if (params.model) qs.set('model', params.model)
  const s = qs.toString()
  return req<AvailabilityTimelineResponse>(
    'GET',
    `/api/admin/probe/availability-timeline${s ? '?' + s : ''}`,
    undefined,
    options,
  )
}

/**
 * ★★ 结果是否**撞到了 500 行上限**（可能被静默截断）。
 *
 * 后端不返回任何截断标记 ⇒ 只能靠「`total` 恰好等于写死的 LIMIT」推导。
 * 这是**可证**的：真值 ≤ cap，撞上 cap 只能说「至少这么多」，
 * 不能说「就这么多」。
 * ⇒ UI 必须说「已达上限，**可能**被截断」，不能说「共 N 条，全部如下」。
 */
export function timelineAtCap(resp: AvailabilityTimelineResponse | null | undefined): boolean {
  return (resp?.total ?? 0) >= AVAILABILITY_TIMELINE_ROW_CAP
}

/**
 * ★ 成功率（0..100）→ 显示串。
 *
 * 函数名里的 `Pct` 是刻意的：这个值**已经是百分数**，
 * 再乘一次 100 就是 100 倍错误。调用方不该自己写 `.toFixed(1) + '%'`。
 */
export function formatSuccessRatePct(v: number | null | undefined): string {
  if (v === null || v === undefined || !Number.isFinite(v)) return '—'
  return `${Number(v).toFixed(1)}%`
}

/**
 * ★ 延时：字段缺失 = 那一小时**没有成功探测**，不是 0ms。
 * 返回 `null` 让调用方显示「无成功探测」。
 */
export function avgLatencyOf(p: AvailabilityPoint | null | undefined): number | null {
  const v = p?.avg_latency_ms
  return v === undefined || v === null || !Number.isFinite(v) ? null : v
}

/**
 * 把时间线按模型分组。
 *
 * ★ SQL 是 `ORDER BY raw_model_name, hour_bucket DESC`（模型升序、小时降序）
 *   ⇒ 组内已经是最新的在前，**不要再排一次**。
 */
export function groupByModel(points: AvailabilityPoint[] | null | undefined): Array<{
  model: string
  points: AvailabilityPoint[]
}> {
  const map = new Map<string, AvailabilityPoint[]>()
  for (const p of points ?? []) {
    const arr = map.get(p.raw_model_name)
    if (arr) arr.push(p)
    else map.set(p.raw_model_name, [p])
  }
  return [...map.entries()].map(([model, pts]) => ({ model, points: pts }))
}

// ── cache-state ───────────────────────────────────────────────────────────

export interface CacheStateEntry {
  credential_id: number
  /** ⚠️ Go 字段名是 `RawModel`，**JSON 键是 `raw_model_name`** —— 同类不一致的又一例。 */
  raw_model_name: string
  /** healthy / healthy_confirmed / available / failing / broken_confirmed / unavailable / suspicious / probing */
  state: string
  /** 1 = 缓存里的这个凭据可参与路由。 */
  available: boolean
  last_status: string
  consecutive_successes: number
  consecutive_failures: number
  updated_at?: string | null
  next_retry_at?: string | null
  source: string
}

export interface CacheStateResponse {
  reader: string
  key_prefix: string
  /** 后端回显的解析结果；非法输入时会是 0（= 不过滤）。 */
  credential_id: number
  model: string
  count: number
  /** 后端显式初始化为 `[]`（:2029-2031 的 nil 检查），**不是** null。 */
  entries: CacheStateEntry[] | null
}

/** `ScanKeys` 的硬上限（bg/model_availability_reader.go:152）。 */
export const CACHE_STATE_KEY_CAP = 4096

export interface CacheStateParams {
  /** 非法值 / ≤0 会被后端**静默忽略**（= 不过滤，返回全量）。见文件头 (9)。 */
  credentialId?: number
  model?: string
}

export function fetchCacheState(params: CacheStateParams = {}, options?: RequestOptions): Promise<CacheStateResponse> {
  const qs = new URLSearchParams()
  // ★ 运行时守卫：只在是正整数时发。发 0 / 负数 / NaN 会被后端静默忽略，
  //   客户端会以为「筛了这个凭据」，实际拿到的是**全量**。
  if (params.credentialId != null && Number.isFinite(params.credentialId) && params.credentialId > 0) {
    qs.set('credential_id', String(Math.trunc(params.credentialId)))
  }
  if (params.model) qs.set('model', params.model)
  // ★ 永远不发 `format` —— prom 会把同一个 URL 变成 text/plain。见文件头 (8)。
  const s = qs.toString()
  return req<CacheStateResponse>('GET', `/api/admin/probe/cache-state${s ? '?' + s : ''}`, undefined, options)
}

/**
 * ★★ 是否撞到 4096 key 上限（可能被静默截断）。
 * 与 `timelineAtCap` 同理：后端不给截断标记，只能靠撞上限推导。
 */
export function cacheStateAtCap(resp: CacheStateResponse | null | undefined): boolean {
  return (resp?.count ?? 0) >= CACHE_STATE_KEY_CAP
}

/**
 * ★ 503 的两种来源都表示「**读不到**」，与「读到了但是空」是两件事。
 *
 * · `availability reader not wired`（:1973）
 * · `redis client unavailable`（:2011）
 *
 * ⇒ 返回可区分的分类，供 UI 决定是显示「未接线」还是显示普通错误。
 * 页面**绝不能**把 503 显示成「缓存里什么都没有」。
 */
export type CacheStateFailure =
  | { kind: 'not_wired' }
  | { kind: 'redis_unavailable' }
  | { kind: 'other'; message: string }

export function classifyCacheStateError(err: unknown): CacheStateFailure {
  const status = (err as { status?: number })?.status
  const msg = (err instanceof Error ? err.message : String(err)) || ''
  if (status === 503) {
    if (/not wired/i.test(msg)) return { kind: 'not_wired' }
    if (/redis client unavailable/i.test(msg)) return { kind: 'redis_unavailable' }
    return { kind: 'other', message: msg }
  }
  return { kind: 'other', message: msg }
}

const CACHE_STATE_TONE: Record<string, 'success' | 'warning' | 'danger' | 'muted'> = {
  healthy: 'success',
  healthy_confirmed: 'success',
  available: 'success',
  suspicious: 'warning',
  probing: 'warning',
  failing: 'danger',
  broken_confirmed: 'danger',
  unavailable: 'danger',
}

/**
 * 缓存状态 → 配色。★ 词表外一律 `muted`。
 * 而 `available` 是**独立的布尔字段**，与 `state` 分开看：
 * 一个 `state=healthy` 但 `available=false` 的条目是自相矛盾的
 * （能路由却不健康，或反之）⇒ 页面要能把这两种读数并排显示。
 */
export function cacheStateTone(e: CacheStateEntry | null | undefined): 'success' | 'warning' | 'danger' | 'muted' {
  return CACHE_STATE_TONE[(e?.state ?? '').toLowerCase()] ?? 'muted'
}

/** 词表外的 state 不得给 success —— 同 proposalStatusTone / healthTone 的纪律。 */
export function cacheStateKeyOf(e: CacheStateEntry | null | undefined): string {
  const v = (e?.state ?? '').toLowerCase()
  return [
    'healthy',
    'healthy_confirmed',
    'available',
    'suspicious',
    'probing',
    'failing',
    'broken_confirmed',
    'unavailable',
  ].includes(v)
    ? 'cache.state.' + v
    : 'cache.state.unknown'
}

/**
 * ★ `state` 与 `available` 是否自相矛盾。
 *
 * 后端把两者分别存（`state` 来自探测判定，`available` 是「是否参与路由」）。
 * 一个 `state=healthy*` 却 `available=false` 的条目意味着
 * 「探测说它好，但路由不认它」—— 那正是「模型明明健康却没被选中」的根因。
 * ⇒ 页面必须把这种行标出来，否则用户看到 healthy 就以为没问题。
 */
export function cacheStateContradiction(e: CacheStateEntry | null | undefined): boolean {
  if (!e) return false
  const s = (e.state ?? '').toLowerCase()
  const looksHealthy = s === 'healthy' || s === 'healthy_confirmed' || s === 'available'
  return looksHealthy && e.available === false
}
