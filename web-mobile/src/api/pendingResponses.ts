import { req, type RequestOptions } from './client'

// pendingResponses.ts — 待处理响应（pending response cache）的运维读面。
//   GET /api/admin/pending-responses            列表（分页 + 过滤）
//   GET /api/admin/pending-responses/stats      聚合计数
//   GET /api/admin/pending-responses/{sessionID} 单条详情
//
// 鉴权：三条都挂 `admin(...)`（admin/handler.go:1364-1366）⇒ **admin 档**。
// ★ `/stats` 注册在 `.../pending-responses/` 子路由**之前**（:1364 vs :1366），
//   否则会被子路由当成 sessionID 吞掉。这个顺序是有意为之，不是巧合。
//
// ★ 写操作 `DELETE /api/admin/pending-responses/{sessionID}` **不在本文件**。
//
// ⚠️⚠️⚠️⚠️ 这一族最大的坑：**列表端点把三个字段硬编码成 0/false，
//   而 JSON 里没有 omitempty，所以它们看起来像真值。**
//
// (1) ★★★★ `status` 过滤**是假的**（`admin/pending_handlers.go:166-188`）。
//     底层的 `pending.Store.ListStaleInProgress`
//     （`pending/pending.go:311-372`）在 `:355` 有：
//         if r == nil || r.Status != StatusInProgress { continue }
//     ⇒ 它**只**返回 `in_progress`。而 adapter 里
//         `Status: "in_progress"`（`pending_handlers.go:95`）是**写死的字面量**，
//         不是从条目读出来的。
//     ⇒ `?status=completed` / `?status=failed` / `?status=任意垃圾`
//       都**永远返回空数组**，而且**不报错**。
//     ⇒ 页面因此**不提供** completed/failed 选项 —— 提供了用户就会得出
//       「没有已完成的挂起响应」这种错误结论。
//
// (2) ★★★★ 列表里的 `provider_id` / `is_stream` / `bytes_buffered`
//     **恒为 0 / false / 0**。
//     `StaleEntry`（`pending/pending.go:283-288`）**只有四个字段**：
//     `SessionID / RequestID / CreatedAt / TenantID` —— 根本没有这三个可填。
//     而 `listEntry`（:52-63）给它们的 JSON tag **没有 `omitempty`**
//     ⇒ 响应里**一定会出现** `"provider_id":0, "is_stream":false, "bytes_buffered":0`。
//     ★ **`provider_id: 0` 不是「供应商 0」，是「列表端点不返回这个」。**
//     只有 `GET /{sessionID}` 详情（:236-248）才真的填这三个字段。
//     ⇒ 页面必须明说这一点，否则用户会把 0 当成真值。
//
// (3) ★★★ 时间是 **Unix 秒（int64）**，不是 ISO 字符串。
//     `created_at` / `completed_at` / `oldest_created_at` 全是秒。
//     ★ 这与本仓库其它端点（ISO 字符串）不同，直接丢给 `relativeTime()` 会
//       `Date.parse("1791…")` 失败后**原样回显那个数字**。
//     ⇒ 一律先过 `pendingUnixToIso`。
//
// (4) ★★★ `stats.by_status` **永远只有一个键** `in_progress`，且恒等于 `total`
//     （`pending_handlers.go:331-336` 里写死 `byStatus["in_progress"]++`）。
//     ⇒ 展示它没有任何信息量，页面直接说「全是进行中」即可。
//
// (5) ★★★ `stats.oldest_created_at` 在**没有条目时是 0**
//     （循环不进，初值 0 原样输出）⇒ 0 是「没有条目」，**不是 1970 年**。
//     且它的口径是「in_progress 里最老的那条」，**不含** completed/failed。
//
// (6) ★★ 分页是「先全取、再内存切」。`limit` clamp [1,500]，
//     **非数字/≤0 → 回落 50**，永不报错（`pageBounds`，:124-141）。
//     ★ `offset` 越界**不报错**，返回空数组
//       （`offset>len` ⇒ `offset=len`，`end` 也被 clamp 到 len ⇒ 切出空片）。
//     ★★ 顺带订正一个**很容易推断错**的点：`ListStaleInProgress(…, 1000)`
//       里的 `1000` **不是条数上限**，它是 Redis `SCAN` 的 `COUNT` **提示**；
//       该函数循环到 `cursor==0` 为止（`pending/pending.go:325-369`），
//       函数注释也写明「COUNT is a hint, not a guarantee」。
//       ⇒ 列表**不是**被截断到 1000 条，`count` 可以当「本租户挂起总数」看
//         （但仍只是**进行中**的总数，见坑 1）。
//
// (7) ★★ `TenantID` 是 `json:"-"` ⇒ **响应不返回租户**，
//     但过滤确实做了：superAdmin 看全量，tenant_admin 只看自己租户
//     （:184）。所以「列表变少」对 tenant_admin 是**正常的**。
//
// (8) ★ 详情 404 与「跨租户不可见」**共用** `PENDING_NOT_FOUND`
//     （:228-232：`!found || 租户不符` 一起返回 404）
//     ⇒ 这是**刻意不泄漏存在性**，不是 bug；但页面不能说成「服务出错了」。
//
// (9) ★ 错误信封走 `writeErrorJSON`（:391-399）⇒ **嵌套 JSON**
//     `{"error":{"message":…,"code":…}}`，与 routing-opt 那一族的
//     `http.Error`（text/plain）**不同族**。
//     503 `PENDING_STORE_UNAVAILABLE` / 503 `PENDING_STORE_ERROR` /
//     404 `PENDING_NOT_FOUND` / 405 `METHOD_NOT_ALLOWED`。

/** ★ 后端**只**扫这一种状态；其余状态筛了也是空。见坑 1。 */
export const PENDING_STATUSES = ['in_progress'] as const
export type PendingStatus = (typeof PENDING_STATUSES)[number]

/** ★ `pageBounds` 的默认值与上限（:119-123）。越界是**静默回落/clamp**，不是 400。 */
export const PENDING_LIMIT_DEFAULT = 50
export const PENDING_LIMIT_MIN = 1
export const PENDING_LIMIT_MAX = 500

// ── list ───────────────────────────────────────────────────────────────────

export interface PendingListEntry {
  session_id: string
  request_id: string
  /** ★ 恒为 `'in_progress'`（字面量写死）。见坑 1。 */
  status: string
  /** ★★ 列表端点**恒为 0**（`StaleEntry` 没这个字段）。见坑 2。 */
  provider_id: number
  /** ★★ 列表端点**恒为 false**。见坑 2。 */
  is_stream: boolean
  /** ★ Unix **秒**，不是 ISO。见坑 3。 */
  created_at: number
  /** ★ `omitempty` ⇒ 为 0 时**键整个不存在**。见坑 2。 */
  completed_at?: number
  /** ★★ 列表端点**恒为 0**。见坑 2。 */
  bytes_buffered: number
  /** ★ Unix **秒**差，由 `time.Now().Unix() - CreatedAt` 现算。 */
  age_seconds: number
}

/** ★ 列表端点恒为假的三个字段——页面据此决定「不显示这几列」。 */
export const PENDING_LIST_FIELDS_ALWAYS_ZERO = ['provider_id', 'is_stream', 'bytes_buffered'] as const

export function pendingListFieldIsAlwaysZero(field: string): boolean {
  return (PENDING_LIST_FIELDS_ALWAYS_ZERO as readonly string[]).includes(field)
}

export interface PendingListResponse {
  entries: PendingListEntry[]
  limit: number
  /** ★ 已回显**修正后**的 offset（越界时等于总条数）。见坑 6。 */
  offset: number
  /** ★ 过滤后的条数 —— **只含进行中**，别当成「全部挂起」。见坑 1。 */
  count: number
}

export interface PendingListParams {
  /** ★ 只接受 `'in_progress'`；其它值会被**静默丢弃**，不发。见坑 1。 */
  status?: PendingStatus
  /** 精确匹配 sessionID（后端 `e.SessionID != sessionFilter`）。 */
  sessionId?: string
  limit?: number
  offset?: number
}

export function fetchPendingList(params: PendingListParams = {}, options?: RequestOptions): Promise<PendingListResponse> {
  const qs = new URLSearchParams()
  // ★ 只发合法的 status：发 `completed` 会得到空数组且不报错（见坑 1），
  //   与其让用户以为「没有已完成的」，不如不发。
  if (params.status && (PENDING_STATUSES as readonly string[]).includes(params.status)) {
    qs.set('status', params.status)
  }
  const sid = (params.sessionId ?? '').trim()
  if (sid !== '') qs.set('session_id', sid)
  // ★ limit 只在 1..500 内发：越界会被后端**静默 clamp**（不是报错）。
  if (typeof params.limit === 'number' && Number.isFinite(params.limit)) {
    const n = Math.trunc(params.limit)
    if (n >= PENDING_LIMIT_MIN && n <= PENDING_LIMIT_MAX) qs.set('limit', String(n))
  }
  if (typeof params.offset === 'number' && Number.isFinite(params.offset)) {
    const n = Math.trunc(params.offset)
    if (n > 0) qs.set('offset', String(n))
  }
  const s = qs.toString()
  return req<unknown>('GET', `/api/admin/pending-responses${s ? '?' + s : ''}`, undefined, options).then(unwrapPendingList)
}

/**
 * ★ 形状不符**抛错**，不静默返 `[]`。
 *
 * 理由与 `nodeAudit.ts` 的 `unwrapNodeAudit` 同源（不抽通用解包器）：
 * 静默返 `[]` 会让「解包失败」与「真的一个挂起响应都没有」在页面上长得一模一样，
 * 而这两件事要采取的行动完全相反。
 */
export function unwrapPendingList(resp: unknown): PendingListResponse {
  if (resp && typeof resp === 'object' && Array.isArray((resp as PendingListResponse).entries)) {
    return resp as PendingListResponse
  }
  const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
  throw new Error(`pending-responses 响应形状不符：期望 {entries:[…], count, limit, offset}，实得 ${actual}`)
}

// ── detail ─────────────────────────────────────────────────────────────────

export interface PendingDetail {
  session_id: string
  request_id: string
  /** ★ 与列表不同：这里**真的**从条目读，可能不是 in_progress。 */
  status: string
  provider_id: number
  /** ★ 列表里恒为 0 的字段，在详情里是真值。 */
  credential_id: number
  is_stream: boolean
  created_at: number
  completed_at: number
  bytes_buffered: number
  age_seconds: number
  error_message: string
}

export function fetchPendingDetail(sessionId: string, options?: RequestOptions): Promise<PendingDetail> {
  return req<PendingDetail>('GET', `/api/admin/pending-responses/${encodeURIComponent(sessionId)}`, undefined, options)
}

// ── stats ──────────────────────────────────────────────────────────────────

export interface PendingStats {
  total: number
  /** ★★ 永远只有 `in_progress` 一个键，且恒等于 `total`。见坑 4。 */
  by_status: Record<string, number>
  /** ★ 空列表时是 **0**（不是「1970 年」）。见坑 5。 */
  oldest_created_at: number
}

export function fetchPendingStats(options?: RequestOptions): Promise<PendingStats> {
  return req<unknown>('GET', '/api/admin/pending-responses/stats', undefined, options).then(unwrapPendingStats)
}

/** ★ 同 `unwrapPendingList`：形状不符抛错，不让 `by_status` 变成 undefined 还当 0。 */
export function unwrapPendingStats(resp: unknown): PendingStats {
  if (
    resp &&
    typeof resp === 'object' &&
    typeof (resp as PendingStats).total === 'number' &&
    (resp as PendingStats).by_status &&
    typeof (resp as PendingStats).by_status === 'object'
  ) {
    return resp as PendingStats
  }
  const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
  throw new Error(`pending-responses/stats 响应形状不符：期望 {total, by_status, oldest_created_at}，实得 ${actual}`)
}

// ── 时间换算 ───────────────────────────────────────────────────────────────

/**
 * Unix **秒** → ISO 字符串（`relativeTime` 这类工具只认 ISO）。
 *
 * ★ 0 / 负数 / 非有限一律返回 `undefined`：
 *   `oldest_created_at` 在没有条目时是 0（见坑 5），
 *   那不是 1970-01-01，页面必须当「没有」。
 */
export function pendingUnixToIso(sec: number | null | undefined): string | undefined {
  if (sec === null || sec === undefined || !Number.isFinite(sec) || sec <= 0) return undefined
  return new Date(sec * 1000).toISOString()
}

/** 排障面板要回答的是「有没有卡住的请求」，所以按秒数分成四档。 */
export type PendingAgeBand = 'none' | 'under_1m' | 'under_10m' | 'over_10m'

/** ★ 与 `PendingAgeBand` 同源；供 `dynamicKeys.spec.ts` 拼接 i18n 键（不手抄）。 */
export const PENDING_AGE_BANDS = ['none', 'under_1m', 'under_10m', 'over_10m'] as const

/**
 * `age_seconds` → 分档。分档边界写在这里，页面只认返回值，不自己算。
 */
export function pendingAgeBand(ageSeconds: number | null | undefined): PendingAgeBand {
  if (ageSeconds === null || ageSeconds === undefined || !Number.isFinite(ageSeconds) || ageSeconds < 0) return 'none'
  if (ageSeconds < 60) return 'under_1m'
  if (ageSeconds < 600) return 'under_10m'
  return 'over_10m'
}