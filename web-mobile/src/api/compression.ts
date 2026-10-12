import { req, type RequestOptions } from './client'

/**
 * compression.ts — 压缩可观测面的两条**只读**端点（2026-10-08，第六十八批）。
 *
 * GET /api/admin/compression/stats     （handler.go:954，admin 档）
 * GET /api/admin/compression/sessions  （handler.go:955，admin 档）
 *
 * ## 与既有页面的关系（不重叠）
 * `api/dataLifecycleStats.ts`（第六十六批）里的 `growth_trend[].compression_rate`
 * 是**逐日**的压缩率（0-100 百分数），而本页 `compression_rate` 是**窗口内整体**
 * 的比例（**0-1**）⇒ **单位相反**，两者不能直接比较。
 * data-flow 页答「记录怎么分布」，本页答「压缩策略实际压了多少、会话被压成什么样」。
 *
 * ## ★★★★★ 本族最要紧的五件事
 *
 * (1) ★★★★★ **`count` 查询失败时返回的是 200 + `{items:[], count:0}`，不是 500**
 *      （compression_sessions.go:108-112）。
 *      ⇒ `count: 0` **无法区分**「真的没有会话」与「count 查询失败」，
 *      而后者连主查询都没跑。
 *      ⇒ 同时注意：主查询失败走的是 500（:155-160），**两条失败路径不一致**。
 *
 * (2) ★★★★ **`hours` 只在 `from` 与 `to` 都缺省时才参与时间窗计算**
 *      （:89-109 / :64-85）：
 *      ```go
 *      if fromStr != "" { from = parse(fromStr) } else { to = now; from = to - hours }
 *      if toStr   != "" { to   = parse(toStr)   } else if fromStr != "" { to = now }
 *      ```
 *      ⇒ **只要传了 `from` 或 `to` 任一，`hours` 就被完全忽略。**
 *      这是本族最容易踩的坑：前端「顺手」带上 `hours` + `from`，
 *      `hours` 会无声失效，而界面上还显示着它。
 *
 * (3) ★★★★ **`compressed_total` 是组级口径，不是行级口径**：
 *      ```go
 *      result.TotalRequests += cnt
 *      if withOutbound > 0 { result.CompressedTotal += cnt }   // ← 整组都算
 *      ```
 *      （compression_stats.go:173-176）按 strategy 分组，
 *      `with_outbound = COUNT(rb.outbound_body)` 是**组内**计数
 *      ⇒ 只要组内有任意一行有 outbound_body，**整组 cnt 都进 CompressedTotal**。
 *      ⇒ `compression_rate = CompressedTotal / TotalRequests`（:187，**0-1 比例**）
 *      的分子**不是**「被压缩的行数」，而是「至少有一行被压缩的策略组的行数之和」。
 *
 * (4) ★★★★ **本族两个端点的 nil 指针编码相反**：
 *      - `compressionSessionItem` 的四个 `*int`
 *        （`outbound_msg_count` / `outbound_token_est` /
 *          `estimated_original_msgs` / `msg_reduction`）**没有 omitempty**
 *        ⇒ 键**恒存在**，值为 **`null`**。
 *      - `compressionStats` 的七个 token 字段（`token_band_*` /
 *        `total_outbound_tokens` / `estimated_*` / `summary_mode_rows`）
 *        **有 omitempty 且是 `*int64`** ⇒ 值不大于阈值时**键直接不存在**。
 *      ⇒ 「键缺失」与「值为 null」在本族是**两种不同的失败语义**，不能互相套用。
 *
 * (5) ★★★ **租户隔离之外还有第二重口径差异：`($3 OR rl.success)`**
 *      （stats:154/band:226/bucket:275、sessions:89）
 *      `$3 = !tenantFilter` ⇒ **只有非 tenant_admin 才忽略 `success`**
 *      ⇒ **tenant_admin 只看成功请求**，而 super_admin 的数字含失败请求。
 *      ⇒ 同一族两条端点、两种角色，统计口径**不可直接对比**。
 *
 * 另：租户过滤走 `tenantLogsClause`（session_tenant.go:16-26），
 * 它**只对非 default 租户的 tenant_admin 注入** `AND tenant_id = $N`
 * ⇒ **default 租户的 tenant_admin 不被隔离**（看得到全部租户的行）。
 * 这与 data-lifecycle 的 `IsTenantAdmin` + 字符串拼 `tenant_id`（:57-63）
 * **口径不同** —— 那条连 default 租户也过滤。两族不可类推。
 */

/* ═══════════════════════════════════════════════════════════════════════════
 * 公共：时间窗参数
 * ═══════════════════════════════════════════════════════════════════════════ */

/** compression_stats.go:74 / compression_sessions.go:40 的缺省。 */
export const HOURS_DEFAULT = 24
/** :75-77 钳位下界（不是回落，是 clamp）。 */
export const HOURS_MIN = 1
/** :78-80 钳位上界。 */
export const HOURS_MAX = 720

/** `queryInt`（handler.go:1534-1544）：空串或 `Atoi` 失败 ⇒ 回落缺省。 */
export function hoursParseEffective(raw: string | undefined): number {
  if (raw === undefined || raw === '') return HOURS_DEFAULT
  const n = Number(raw)
  if (!Number.isInteger(n)) return HOURS_DEFAULT
  return n
}

/** ★ 后端是 **clamp 到 [1,720]**，不是回落缺省（与 approvals 的 page 回落口径不同）。 */
export function hoursEffective(hours: number): number {
  if (hours < HOURS_MIN) return HOURS_MIN
  if (hours > HOURS_MAX) return HOURS_MAX
  return hours
}

export interface TimeWindowQuery {
  /** `hours`：缺省 24，钳位 [1,720]。★ 传了 from/to 就会被忽略。 */
  hours?: number
  /** RFC3339；**格式错 ⇒ 400**（不是静默忽略）。 */
  from?: string
  /** RFC3339；**格式错 ⇒ 400**。 */
  to?: string
}

/**
 * ★★★ 后端**真正**用哪种口径决定时间窗（stats:89-109、sessions:64-85）。
 *
 * - `'hours'`：from/to 都不传 ⇒ `[now-hours, now]`，`hours` 生效。
 * - `'explicit'`：传了 from 或 to 任一 ⇒ 用显式端点，**`hours` 被完全忽略**；
 *   只传 from 时另一端取 `now`。
 *
 * ⇒ UI 在传了 hours 又传 from 时**必须**去掉 hours，否则会显示一个没生效的值。
 */
export function timeWindowMode(q: TimeWindowQuery | undefined): 'hours' | 'explicit' {
  if (!q) return 'hours'
  if (q.from !== undefined && q.from !== '') return 'explicit'
  if (q.to !== undefined && q.to !== '') return 'explicit'
  return 'hours'
}

/** 组查询串：★ 只在 `timeWindowMode === 'hours'` 时才发 `hours`。 */
export function timeWindowQuery(q: TimeWindowQuery | undefined): string {
  const parts: string[] = []
  const mode = timeWindowMode(q)
  if (q?.from) parts.push(`from=${encodeURIComponent(q.from)}`)
  if (q?.to) parts.push(`to=${encodeURIComponent(q.to)}`)
  if (mode === 'hours') {
    const h = q?.hours !== undefined ? hoursEffective(q.hours) : HOURS_DEFAULT
    parts.push(`hours=${h}`)
  }
  return parts.length > 0 ? `?${parts.join('&')}` : ''
}

/* ═══════════════════════════════════════════════════════════════════════════
 * A. GET /api/admin/compression/stats
 * ═══════════════════════════════════════════════════════════════════════════ */

/**
 * compression_stats.go:16-21。
 * ★ `rate` 是 **0-1 的比例**（:294 `compressed/total`），
 * 与 data-lifecycle 的 `percent_of_total`（0-100）**单位相反**。
 * ★ `hour` 的粒度**随时间窗变化**，见 `seriesGranularityOf`。
 */
export interface HourBucket {
  hour: string
  total: number
  compressed: number
  rate: number
}

/**
 * compression_stats.go:113-130（匿名 struct）。
 *
 * 必检：五个。
 * ★ 后七个是 `*int64` + **omitempty** ⇒ 值不达阈值时**键不存在**。
 */
export interface CompressionStatsResponse {
  total_requests: number
  compressed_total: number
  /** ★ 0-1 比例（不是百分数）。分母 `total_requests` 为 0 时留 0。 */
  compression_rate: number
  /** ★ 空值恒为 `{}`（:131 `make`），不是 null。键是 strategy，值是行数。 */
  strategy_distribution: Record<string, number>
  /** ★ omitempty。SQL 侧 `COALESCE(token_band,'')`，三档分别命中才出现。 */
  token_band_below?: number
  token_band_preliminary?: number
  token_band_forced?: number
  /** ★ omitempty。只在窗口内 `SUM(outbound_token_est) > 0` 时出现（:189-191）。 */
  total_outbound_tokens?: number
  /** ★ omitempty。只在 >0 时出现（:202-203）。**该查询失败是静默的**（:196-200）。 */
  estimated_original_tokens?: number
  /**
   * ★★ omitempty，且**只在 `estimated_original_tokens > total_outbound_tokens` 时出现**
   * （:204-206）⇒ 「没有节省」与「算不出来」都表现为**键缺失**，
   * 客户端**不能**把缺失说成「没节省」。
   */
  estimated_tokens_saved?: number
  /** ★ omitempty（:212-214，注释明说为了与 pre-P2-C1 响应字节一致）。 */
  summary_mode_rows?: number
  /** ★ 空值恒为 `[]`（:132 `make`）；bucket 查询失败时也是 `[]`（:279-280 静默）。 */
  hourly_series: HourBucket[]
}

export const COMPRESSION_STATS_KEYS = [
  'total_requests', 'compressed_total', 'compression_rate',
  'strategy_distribution', 'hourly_series',
] as const
export const HOUR_BUCKET_KEYS = ['hour', 'total', 'compressed', 'rate'] as const

/** ★ 这七个键**可能不存在**，UI 不能当必填。 */
export const COMPRESSION_STATS_OPTIONAL_KEYS = [
  'token_band_below', 'token_band_preliminary', 'token_band_forced',
  'total_outbound_tokens', 'estimated_original_tokens',
  'estimated_tokens_saved', 'summary_mode_rows',
] as const

export type TokenBand = 'below' | 'preliminary' | 'forced'

export function fetchCompressionStats(
  q?: TimeWindowQuery,
  options?: RequestOptions,
): Promise<CompressionStatsResponse> {
  return req<unknown>('GET', `/api/admin/compression/stats${timeWindowQuery(q)}`, undefined, options)
    .then(unwrapCompressionStats)
}

export function unwrapCompressionStats(resp: unknown): CompressionStatsResponse {
  const d = requireObject(resp, '压缩统计')
  requireKeys(d, COMPRESSION_STATS_KEYS, '压缩统计')
  const dist = d.strategy_distribution
  if (!isPlainObject(dist)) throw new Error('strategy_distribution 不是对象')
  for (const [k, v] of Object.entries(dist)) {
    if (typeof v !== 'number') throw new Error(`strategy_distribution["${k}"] 不是数字`)
  }
  const series = requireArray(d.hourly_series, 'hourly_series')
  series.forEach((row, i) => {
    if (!isPlainObject(row)) throw new Error(`hourly_series[${i}] 不是对象`)
    requireKeys(row, HOUR_BUCKET_KEYS, `hourly_series[${i}]`)
  })
  return d as unknown as CompressionStatsResponse
}

/** ★ 分母为 0 时那个 0 无意义，不能讲成「压缩率 0%」。 */
export function compressionRateMeaningless(m: CompressionStatsResponse): boolean {
  return m.total_requests === 0
}

/**
 * ★★ `strategy_distribution` 各组之和必须等于 `total_requests`
 * （每行进唯一一组，:173/:177）。
 * 对不上说明策略名撞了键（`COALESCE(NULLIF(,''),'none')` 把空串与 NULL 归到 `'none'`）。
 */
export function strategyCountsDisagree(m: CompressionStatsResponse): boolean {
  const sum = Object.values(m.strategy_distribution).reduce((a, b) => a + b, 0)
  return sum !== Number(m.total_requests)
}

/**
 * ★★★ 三档 token band 的计数。
 *
 * 后端只在对应 band **出现过**时设指针（:240-250）⇒ 某一档没出现就是**键缺失**，
 * 而缺失与「这一档是 0 条」**不可分**（`COUNT(*)` 不会为 0 的 band 返回行，
 * 会被 GROUP BY 产出一行 band='' 但 switch 里没有 case ''）。
 * ⇒ 返回 `null` 表示「查不到」，**不是 0**。
 */
export function tokenBandCount(m: CompressionStatsResponse, band: TokenBand): number | null {
  const v = m[`token_band_${band}`]
  return typeof v === 'number' ? v : null
}

/** ★ 键缺失 ⇒ 窗口内 token 合计为 0 或没记录，**不是**「没有 outbound token」。 */
export function outboundTokensMissing(m: CompressionStatsResponse): boolean {
  return !('total_outbound_tokens' in m)
}

/**
 * ★★★ 键缺失有**两种**可能，且客户端分不出来：
 *   (a) 估算查询失败（`:196-200` 只有 `slog.Warn`，**静默**）；
 *   (b) 估算值为 0（:202 `if estimatedOrig > 0`）。
 * ⇒ 措辞只能是「查不到」，不能是「为 0」。
 */
export function estimatedOrigMissing(m: CompressionStatsResponse): boolean {
  return !('estimated_original_tokens' in m)
}

/**
 * ★★★★ 「键缺失」**不能**说成「没节省」。
 *
 * 后端只在 `estimated_original_tokens > total_outbound_tokens` 时才设该指针（:204-206）
 * ⇒ 节省为 0、节省为负、以及估算查询失败，**三种情况都是键缺失**。
 * ⇒ UI 只能说「未给出节省估算」。
 */
export function tokensSavedMissing(m: CompressionStatsResponse): boolean {
  return !('estimated_tokens_saved' in m)
}

/** ★ 键缺失同理：要么 0，要么该查询静默失败。 */
export function summaryModeRowsMissing(m: CompressionStatsResponse): boolean {
  return !('summary_mode_rows' in m)
}

/**
 * ★★★ 序列粒度**随时间窗变化**（stats:257-266），字段名 `hourly_series` 会骗人：
 * - `rangeHours <= 48`  ⇒ **每小时**
 * - `rangeHours <= 168` ⇒ **每 6 小时**（`date_trunc('day') + 6h*(hour/6)`）
 * - 否则               ⇒ **每天**
 */
export type SeriesGranularity = 'hour' | '6h' | 'day'

export function seriesGranularityOf(rangeHours: number): SeriesGranularity {
  if (rangeHours <= 48) return 'hour'
  if (rangeHours <= 168) return '6h'
  return 'day'
}

/** 序列为空时**不能**断言「没有流量」—— bucket 查询失败也是 `[]`（:279-280）。 */
export function hourlySeriesMayBeFailed(m: CompressionStatsResponse): boolean {
  return m.hourly_series.length === 0
}

/** ★ 任一桶的 `compressed > total` ⇒ 计数矛盾（口径出问题）。 */
export function bucketCountsDisagree(m: CompressionStatsResponse): boolean {
  return m.hourly_series.some((b) => Number(b.compressed) > Number(b.total))
}

/* ═══════════════════════════════════════════════════════════════════════════
 * B. GET /api/admin/comcompression/sessions
 * ═══════════════════════════════════════════════════════════════════════════ */

/**
 * compression_sessions.go:16-27。
 *
 * ★ 四个 `*int` 字段**没有 omitempty** ⇒ 键恒存在，**值可为 `null`**。
 * ★ `estimated_original_msgs` 由 SQL 侧 `COALESCE(latest.orig_msg_count, 0)`
 *   兜底（:125）⇒ **恒非 null**；
 *   而 `outbound_msg_count` 直接 `MAX(rl.outbound_msg_count)` 扫进指针（:133/:170）
 *   ⇒ DB 里该列为 NULL 时**可为 null**。
 * ⇒ 同名字段族里一个恒有值一个可能为空，**不能照名字推断**。
 */
export interface CompressionSessionItem {
  gw_session_id: string
  /**
   * ★ `MAX(rl.compression_strategy)`（:129）——**字典序最大**的那一条，
   * 不是首个也不是最新。会话中途换策略时读到的是巧合值。
   */
  compression_strategy: string
  request_count: number
  /** Go `time.Time` 直编 ⇒ RFC3339 **纳秒**级。 */
  first_ts: string
  /** 同上，纳秒级。 */
  last_ts: string
  /** ★ 可为 `null`。 */
  outbound_msg_count: number | null
  /** ★ 可为 `null`。 */
  outbound_token_est: number | null
  /** ★ **恒非 null**（SQL 侧 COALESCE 到 0）。 */
  estimated_original_msgs: number | null
  /**
   * ★ 可为 `null`：**只在 `outbound_msg_count` 与 `estimated_original_msgs`
   *   都非 null 时才计算**（:183），否则指针保持 nil。
   * ★ 负差被**夹到 0**（:185-187）⇒ **永不出现负数**。
   */
  msg_reduction: number | null
  /** ★ `MAX(rl.request_id)`（:135）——字典序最大的 id，不是最新那次。 */
  sample_request_id: string
}

/**
 * compression_sessions.go:29-32。
 *
 * ★★ `count` 是**真实总数**（`COUNT(DISTINCT gw_session_id)`，:106-107），
 * 与 approvals 的 `total`（本页条数）**正好相反** ⇒ 这里可以拿 count 翻页。
 * ★ 但 count 与 items 仍有**两处**对不上（见 `countExceedsListable`）。
 */
export interface CompressionSessionsResponse {
  items: CompressionSessionItem[]
  count: number
}

export const COMPRESSION_SESSIONS_KEYS = ['items', 'count'] as const
/** ★ 这四个键**恒存在**，但值可为 `null`。 */
export const COMPRESSION_SESSION_NULLABLE_KEYS = [
  'outbound_msg_count', 'outbound_token_est',
  'estimated_original_msgs', 'msg_reduction',
] as const
export const COMPRESSION_SESSION_KEYS = [
  'gw_session_id', 'compression_strategy', 'request_count',
  'first_ts', 'last_ts', 'outbound_msg_count', 'outbound_token_est',
  'estimated_original_msgs', 'msg_reduction', 'sample_request_id',
] as const

/** compression_sessions.go:51-55。`page < 1` ⇒ 回落 1。 */
export const SESSIONS_PAGE_DEFAULT = 1
/** ★ `page_size` 不在 [1,200] ⇒ **静默回落 50**（不是 clamp）。 */
export const SESSIONS_PAGE_SIZE_DEFAULT = 50
export const SESSIONS_PAGE_SIZE_MAX = 200

export interface CompressionSessionsQuery extends TimeWindowQuery {
  page?: number
  pageSize?: number
  /** ★ 精确 `=` 匹配（:100），**不校验合法性** ⇒ 非法值返回空列表而非 400。 */
  strategy?: string
}

export function sessionsPageEffective(page: number | undefined): number {
  if (page === undefined || !Number.isInteger(page)) return SESSIONS_PAGE_DEFAULT
  return page < 1 ? SESSIONS_PAGE_DEFAULT : page
}

/** ★ 越界是**回落 50**，与 stats 的 `hours` clamp [1,720] 是两种口径。 */
export function sessionsPageSizeEffective(pageSize: number | undefined): number {
  if (pageSize === undefined || !Number.isInteger(pageSize)) return SESSIONS_PAGE_SIZE_DEFAULT
  if (pageSize < 1 || pageSize > SESSIONS_PAGE_SIZE_MAX) return SESSIONS_PAGE_SIZE_DEFAULT
  return pageSize
}

export function fetchCompressionSessions(
  q?: CompressionSessionsQuery,
  options?: RequestOptions,
): Promise<CompressionSessionsResponse> {
  const parts: string[] = []
  const base = timeWindowQuery(q)
  if (base.startsWith('?')) parts.push(base.slice(1))
  if (q?.page !== undefined) parts.push(`page=${sessionsPageEffective(q.page)}`)
  if (q?.pageSize !== undefined) parts.push(`page_size=${sessionsPageSizeEffective(q.pageSize)}`)
  if (q?.strategy) parts.push(`strategy=${encodeURIComponent(q.strategy)}`)
  const suffix = parts.length > 0 ? `?${parts.join('&')}` : ''
  return req<unknown>('GET', `/api/admin/compression/sessions${suffix}`, undefined, options)
    .then(unwrapCompressionSessions)
}

export function unwrapCompressionSessions(resp: unknown): CompressionSessionsResponse {
  const d = requireObject(resp, '压缩会话')
  requireKeys(d, COMPRESSION_SESSIONS_KEYS, '压缩会话')
  const items = requireArray(d.items, '压缩会话 items')
  items.forEach((row, i) => {
    if (!isPlainObject(row)) throw new Error(`items[${i}] 不是对象`)
    requireKeys(row, COMPRESSION_SESSION_KEYS, `items[${i}]`)
    // ★ 键恒存在但值可为 null ⇒ 这里逐个校类型，不校非空
    for (const key of COMPRESSION_SESSION_NULLABLE_KEYS) {
      const v = row[key]
      if (v !== null && typeof v !== 'number') {
        throw new Error(`items[${i}] ${key} 既不是数字也不是 null`)
      }
    }
  })
  return d as unknown as CompressionSessionsResponse
}

/**
 * ★★★★ `count: 0` 是**二义的**。
 *
 * count 查询失败时后端返回 **200 + `{items:[], count:0}`**（:108-112），
 * 与「真的一个会话都没有」的响应**完全一样**，而且失败时**主查询根本没跑**。
 * ⇒ 客户端分不出来 ⇒ UI 只能说「没有可展示的记录」。
 */
export function sessionsEmptyIsAmbiguous(r: CompressionSessionsResponse): boolean {
  return r.count === 0 && r.items.length === 0
}

/**
 * ★★★ `count` 可能**大于**本页能列出的会话总数，两处成因：
 * - 空 `gw_session_id` 的行被 `if item.GwSessionID != ""`（:190）**静默丢弃**，
 *   而 `COUNT(DISTINCT)` **把空串也算进去**；
 * - 逐行 `Scan` 失败走 `continue`（:177-180），count 不受扫描失败影响。
 * ⇒ `count` 与 `items.length` 对不上是**可预期**的，不能当成 bug 报。
 */
export function countExceedsListable(r: CompressionSessionsResponse): boolean {
  return Number(r.count) > r.items.length
}

/**
 * ★ 真正的总数（与 approvals 的 `total` 相反）⇒ 可以据此翻页。
 *
 * ⚠️ 必须把**请求用的** pageSize 传进来：后端对越界值是**回落 50**，
 *   而「本页取满」的阈值要用**实际生效**的条数，否则自定义 20 时
 *   会拿 50 当阈值、本页明明取满了也判成「还有下一页」。
 */
export function sessionsHasNextPage(
  r: CompressionSessionsResponse,
  requestedPageSize?: number,
): boolean {
  return r.items.length >= sessionsPageSizeEffective(requestedPageSize)
}

/** ★ `msg_reduction` 为 `null` ⇒ 缺原始估算或缺 outbound 计数（:183）。 */
export function sessionReductionUnknown(i: CompressionSessionItem): boolean {
  return i.msg_reduction === null || i.msg_reduction === undefined
}

/** ★ `outbound_msg_count` 为 `null` ⇒ 该列在 DB 里是 NULL。 */
export function sessionOutboundMsgUnknown(i: CompressionSessionItem): boolean {
  return i.outbound_msg_count === null || i.outbound_msg_count === undefined
}

/**
 * ★★ `msg_reduction === 0` 但估算值**大于** outbound 计数
 * ⇒ 说明差值为负、被后端**夹到 0**（:185-187）
 * ⇒ 也就是说「压完反而变多了」这件事**被抹平了**，界面上只能看到 0。
 */
export function sessionReductionClamped(i: CompressionSessionItem): boolean {
  if (sessionReductionUnknown(i)) return false
  if (i.estimated_original_msgs === null) return false
  if (i.outbound_msg_count === null) return false
  // ⚠️ 方向别写反：后端 `red = orig - outbound`（:184），
  //   **outbound 比 orig 还多**时差值为负、被夹到 0（:185-187）。
  return i.msg_reduction === 0 && i.outbound_msg_count > i.estimated_original_msgs
}

/** ── 内部工具 ──────────────────────────────────────────────────────────── */

function isPlainObject(v: unknown): v is Record<string, unknown> {
  return !!v && typeof v === 'object' && !Array.isArray(v)
}

function requireObject(resp: unknown, where: string): Record<string, unknown> {
  if (!isPlainObject(resp)) {
    const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
    throw new Error(`${where} 响应形状不符：期望裸对象，实得 ${actual}`)
  }
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

function requireArray(v: unknown, where: string): unknown[] {
  if (!Array.isArray(v)) throw new Error(`${where} 不是数组`)
  return v
}