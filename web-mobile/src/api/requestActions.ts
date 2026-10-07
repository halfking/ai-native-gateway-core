import { req, type RequestOptions } from './client'

/**
 * requestActions.ts — 请求动作时间线（2026-10-07，第七十四批）。
 *
 * GET /api/admin/requests/{id}/actions
 *
 * 注册在 `admin/handler.go:1026` 的 `admin(...)` ⇒ **admin 档**，tenant_admin 可用
 * ⇒ 抽屉席**不设** `requiresRole`。
 *
 * ★ 名字里有 "actions"，但**它本身是只读端点**（只有 GET、只 `LRange` 读 Redis），
 *   与本仓一律不碰的写操作端点不是一回事。
 *
 * 它是前端 `ActionTimeline` 的 **SSE 之外的 REST 回退**：刷新页面或请求已结束时，
 * 没有这个端点就没有回放（`admin/request_actions.go:16-18` 的自陈）。
 *
 * ## ★★★★★ 本族最要紧的七件事
 *
 * (1) ★★★★★ **`actions[i]` 是一个「键集不可穷举」的开放对象。**
 *      `admin/live_stream_lifecycle.go:96-125` `flattenActionEvent` 的做法是
 *      「整体序列化再反序列化成 map，然后把 `detail` 的每个键**摊平到顶层**」：
 *      ```go
 *      if detail, ok := m["detail"].(map[string]any); ok {
 *          delete(m, "detail")
 *          for k, v := range detail {
 *              if _, taken := m[k]; !taken {   // ★ 不覆盖已存在的顶层键
 *                  m[k] = v
 *              }
 *          }
 *      }
 *      ```
 *      ⇒ 顶层键 = `ActionEvent` 的固定键 ∪ **`detail` 里内容决定的任意键**。
 *      ⇒ ★★ 这是本仓**第一次**出现「载荷形状开放、键集无法穷举」的端点
 *        ⇒ 解包器**只能校验必有的那几个键**，绝不能对全部键做 `requireKeys`。
 *      ⇒ ★★ 提升时**不覆盖**已有键 ⇒ 若 `detail` 里带了 `action` / `seq` 这类名字，
 *        **会被静默丢弃**。
 *
 * (2) ★★★★★ **`detail` 这个键永远不出现**（`:108` `delete(m, "detail")`）
 *      ⇒ 客户端按 `{action, detail: {...}}` 去读会拿到 `undefined`。
 *
 * (3) ★★★★★ **`action` 是**开放字符串**，不是封闭枚举。**
 *      `admin/request_actions.go:85-94` 的 `decodeStoredAction` **只**判 `ev.Action == ""`：
 *      ```go
 *      if ev.Action == "" { return ev, false }
 *      ```
 *      它**不校验是否在 `liveactions` 的 Action 常量列表内** ⇒ 后端会放行任意非空字符串。
 *      ⇒ 解包器**只校验「非空字符串」，绝不能校验枚举** ——
 *        校验枚举会在后端新增动作时把正常响应判成异常。
 *      ★★ 对照第七十三批的 `event_type`：那个是 `if/else` 决定的三态（**封闭**），
 *        这个是 Redis 内容决定的（**开放**）⇒ 判据不能跨族照抄。
 *
 * (4) ★★★★ **`count` 可能小于「实际匹配数」，而且它**不是**分页元信息。**
 *      `:65-68` 按 `seq` 去重：
 *      ```go
 *      if _, dup := seen[ev.Seq]; dup { continue }
 *      seen[ev.Seq] = struct{}{}
 *      ```
 *      ⇒ `count` 是**去重后**的条数。
 *      ★ `seq` 跨进程重启会碰撞（注释 `:55-57` 自陈），碰撞时**保留先出现的**
 *        （LIST 头 = 最新优先扫描顺序）⇒ 保留的是**较新**的那条。
 *
 * (5) ★★★★ **这是短期回放，不是历史。**
 *      源码 `:56` 的 `LRange(ctx, RedisKey, 0, RedisMaxLen-1)` 扫**整个**有界队列，
 *      而队列是 `LPUSH` + `LTRIM 5000`（`internal/liveactions/liveactions.go:115-117`）
 *      ⇒ **更老的事件已经被 LTRIM 掉**。
 *      `:23-24` 的注释自陈：「Long-term history stays in request journey
 *      ⇒ **this endpoint must not scan Redis as the long-term solution**」。
 *
 * (6) ★★★★ **失败编码有三种，其中一种是 502。**
 *      | 情形 | 状态码 | message |
 *      |---|---|---|
 *      | `id` 为空（`:36-38`） | **400** | `missing request id` |
 *      | Redis 未装配（`:41-44`） | **503** | `live actions store not wired` |
 *      | Redis 读失败（`:52-56`） | **502** | `live actions store unavailable` |
 *      ★★ **502 Bad Gateway** 在本仓其它地方基本不用 ⇒ 客户端的「网关错误」分类
 *        必须显式容纳它，否则会被当成「未知错误」。
 *      ★ 读 Redis 的超时只有 **2 秒**（`:29` `requestActionsScanTimeout`）。
 *
 * (7) ★★★ **`credential_label` 在本端点永远不出现。**
 *      `flattenActionEvent` 支持注入 `labels`（`:115-123`），但本端点调用时传的是
 *      **`nil`**（`request_actions.go:69` `flattenActionEvent(ev, nil)`）
 *      ⇒ 那是 SSE 那条路才有的字段 ⇒ 按 SSE 契约去等它会永远等不到。
 *
 * ## 另注
 *
 * · 排序：`actionEntryLess`（`:98-107`）先比 `ts`（两边都是 string 且不等时），
 *   否则比 `seq`（`.(float64)` 断言失败静默取 0）。
 *   但 `ActionEvent` 的 `ts`/`seq` **都不带 omitempty** ⇒ 恒在
 *   ⇒ 断言不会失败 ⇒ 排序实际是可信的。
 *   ★ 真正会让顺序看起来奇怪的是**零值 `ts`**：`time.Time{}` 序列化成
 *   `"0001-01-01T00:00:00Z"`，字符串比较下**最小** ⇒ 排到最前。
 *   ★ `sort.SliceStable` + LIST 是「最新在前」⇒ 缺 `seq` 的行会保持扫描顺序。
 * · `request_id` 是**原样回显**（`:78` 用 `r.PathValue("id")`，不 trim 不规范化），
 *   且过滤是**精确比较**（`:62`）⇒ 大小写敏感、前后空格也不匹配。
 * · `actions` 恒为数组（`make([]map[string]any, 0, 16)`，`:58`）。
 * · `flattenActionEvent` 序列化失败时返回 **`map[string]any{}`**（`:100`/`:105`）
 *   ⇒ 理论上可能出现一条**空对象**，客户端要能识别而不是当成正常条目。
 * · 坏行被静默丢弃：`decodeStoredAction` 对 JSON 解析失败（`:87-89`）与
 *   `action` 为空（`:90-92`）都 `continue` ⇒ 响应里看不出丢过东西。
 */

/* ═══════════════════════════════════════════════════════════════════════════
 * 常量（逐字来自后端源码）
 * ═══════════════════════════════════════════════════════════════════════════ */

/** admin/request_actions.go:37 的 400 message。 */
export const MISSING_REQUEST_ID_MESSAGE = 'missing request id'
/** :43 的 503 message（Redis 没装配）。 */
export const LIVE_ACTIONS_NOT_WIRED_MESSAGE = 'live actions store not wired'
/** :55 的 502 message（Redis 读失败）。★ 本仓少见的 502。 */
export const LIVE_ACTIONS_UNAVAILABLE_MESSAGE = 'live actions store unavailable'

/** internal/liveactions/liveactions.go:115 `RedisKey`。 */
export const LIVE_ACTIONS_REDIS_KEY = 'llmgw:live:actions'
/** :117 `RedisMaxLen = 5000` —— `LRange` 扫的整个队列上界。 */
export const LIVE_ACTIONS_REDIS_MAX_LEN = 5000
/** admin/request_actions.go:29 `requestActionsScanTimeout = 2 * time.Second`。 */
export const LIVE_ACTIONS_SCAN_TIMEOUT_MS = 2000

/**
 * ★ 摊平后**不该出现**的键：`:108` 的 `delete(m, "detail")`
 * ⇒ `detail` 的内容被提升到顶层，`detail` 本身消失。
 */
export const ACTION_DETAIL_KEY = 'detail'

/**
 * ★ `credential_label` 只在 SSE 那条路注入（`flattenActionEvent(ev, labels)`
 *   且 `labels != nil`），而本端点传的是 `nil`（`:69`）⇒ **永远不会有**。
 */
export const ACTION_CREDENTIAL_LABEL_KEY = 'credential_label'

/** `ActionEvent` 里**恒在**的四个键（`internal/liveactions/liveactions.go:98-109` 无 omitempty）。 */
export const ACTION_ALWAYS_KEYS = ['request_id', 'seq', 'action', 'ts'] as const

/**
 * `ActionEvent` 里带 `omitempty` 的条件键（`:103-108`）。
 * ⚠️ **它们与「由 detail 摊平上来的任意键」无法区分** ——
 * 一个叫 `model` 的键可能来自 `Detail["model"]` 而非 `ActionEvent.Model`。
 */
export const ACTION_OPTIONAL_KEYS = [
  'model', 'credential_id', 'error_kind', 'retry_seq', 'retry',
] as const

export const REQUEST_ACTIONS_KEYS = ['request_id', 'actions', 'count'] as const

/**
 * `internal/liveactions/liveactions.go:48-74` 里已知的 Action 取值（**仅供展示分组**）。
 * ★★ 绝**不是**封闭枚举：后端 `decodeStoredAction` 只判 `!= ""`。
 */
export const KNOWN_ACTIONS = [
  'arrive', 'route_resolved', 'model_enqueued', 'credential_selected',
  'node_enqueued', 'node_selected', 'upstream_request', 'first_byte',
  'reply', 'node_switch', 'model_switch', 'state_change', 'no_route',
] as const

/* ═══════════════════════════════════════════════════════════════════════════
 * 类型
 * ═══════════════════════════════════════════════════════════════════════════ */

/**
 * `flattenActionEvent` 产出的摊平对象。
 *
 * ★★ 索引签名是**契约要求**，不是偷懒：`detail` 的键被提升到顶层
 *   （`live_stream_lifecycle.go:107-114`），键名由 Redis 里的内容决定
 *   ⇒ 键集**不可穷举** ⇒ 声明成固定字段列表会在遇到 `detail` 里的键时丢数据。
 */
export interface RequestActionEntry {
  /** 恒在。 */
  request_id: string
  /** 恒在。★ 去重键；跨进程重启会碰撞（`:55-57`）。 */
  seq: number
  /** 恒在。★ **开放字符串**：后端只判非空，不校验枚举。 */
  action: string
  /** 恒在。★ 零值时间是 `"0001-01-01T00:00:00Z"`，会排到最前。 */
  ts: string
  model?: string
  credential_id?: number
  error_kind?: string
  retry_seq?: number
  retry?: boolean
  /** ★ 来自 `detail` 摊平的任意键；也可能与上面几个条件键同名（此时被丢弃）。 */
  [key: string]: unknown
}

/** admin/request_actions.go:77-81。 */
export interface RequestActionsResponse {
  /** ★ 原样回显路径段（不 trim、不规范化）。 */
  request_id: string
  /** 恒为数组（含空数组）。 */
  actions: RequestActionEntry[]
  /** ★ **去重后**的条数，不是分页元信息。 */
  count: number
}

/* ═══════════════════════════════════════════════════════════════════════════
 * 取数
 * ═══════════════════════════════════════════════════════════════════════════ */

export function fetchRequestActions(
  requestId: string,
  options?: RequestOptions,
): Promise<RequestActionsResponse> {
  return req<unknown>(
    'GET',
    `/api/admin/requests/${encodeURIComponent(requestId)}/actions`,
    undefined,
    options,
  ).then(unwrapRequestActions)
}

/* ═══════════════════════════════════════════════════════════════════════════
 * 解包
 * ═══════════════════════════════════════════════════════════════════════════ */

export function unwrapRequestActions(resp: unknown): RequestActionsResponse {
  const d = requireObject(resp, '请求动作时间线')
  requireKeys(d, REQUEST_ACTIONS_KEYS, '请求动作时间线')

  if (typeof d.request_id !== 'string') {
    throw new Error('request_id 不是字符串')
  }
  // ★ count 是服务端算的 `len(actions)`（`:80`）⇒ 不等就说明载荷被换过。
  const actions = requireArray(d.actions, '请求动作时间线 actions')
  if (Number(d.count) !== actions.length) {
    throw new Error(
      `count(${String(d.count)}) 与 actions.length(${actions.length}) 不一致`,
    )
  }
  actions.forEach((row, i) => {
    if (!isPlainObject(row)) throw new Error(`actions[${i}] 不是对象`)
    // ★ 只校验 4 个恒在键。**不能**对全部键做 requireKeys ——
    //   键集因为 detail 摊平而不可穷举（见文件头第 (1) 条）。
    requireEntryShape(row, `actions[${i}]`)
  })
  return d as unknown as RequestActionsResponse
}

function requireEntryShape(obj: object, where: string): void {
  const d = obj as Record<string, unknown>
  const missing = ACTION_ALWAYS_KEYS.filter((k) => !(k in d))
  if (missing.length > 0) {
    throw new Error(`${where} 缺 ${missing.length} 个恒在键（${missing.join(', ')}）`)
  }
  // ★ `action` 只校验「非空字符串」：**不校验枚举**（后端只判 `!= ""`，见文件头第 (3) 条）。
  if (typeof d.action !== 'string' || d.action === '') {
    throw new Error(`${where} action 不是非空字符串`)
  }
  if (typeof d.seq !== 'number') throw new Error(`${where} seq 不是数字`)
  if (typeof d.ts !== 'string') throw new Error(`${where} ts 不是字符串`)
  if (typeof d.request_id !== 'string') throw new Error(`${where} request_id 不是字符串`)
}

/* ═══════════════════════════════════════════════════════════════════════════
 * 语义判定
 * ═══════════════════════════════════════════════════════════════════════════ */

/** ★ 后端原样回显路径段 ⇒ 客户端要靠它确认没串号。 */
export function requestActionsMatchesRequest(
  r: RequestActionsResponse,
  requestId: string,
): boolean {
  return r.request_id === requestId
}

/** `count` 与 `actions.length` 是否自洽（解包器已把关，这里供 UI 复核）。 */
export function requestActionsCountAgrees(r: RequestActionsResponse): boolean {
  return Number(r.count) === r.actions.length
}

/** ★ `detail` 键**不该出现**（`:108` 已 delete）。出现了就是契约漂了。 */
export function requestActionsHasDetailKey(r: RequestActionsResponse): boolean {
  return r.actions.some((a) => ACTION_DETAIL_KEY in a)
}

/** ★ `credential_label` 在本端点**永远不该出现**（labels 传的是 nil）。 */
export function requestActionsHasCredentialLabel(r: RequestActionsResponse): boolean {
  return r.actions.some((a) => ACTION_CREDENTIAL_LABEL_KEY in a)
}

/** Go `time.Time` 零值的 RFC3339 —— 零值 ts 会排到最前。 */
export const GO_ZERO_TIME = '0001-01-01T00:00:00Z'

/** ★ 这条动作的 `ts` 是 Go 零值（顺序会看起来反常）。 */
export function actionHasZeroTs(a: RequestActionEntry): boolean {
  return a.ts === GO_ZERO_TIME
}

/** ★ 时间线里是否存在零值 `ts` 的条目。 */
export function requestActionsHasZeroTs(r: RequestActionsResponse): boolean {
  return r.actions.some((a) => a.ts === GO_ZERO_TIME)
}

/** ★ `action` 是不是 `liveactions` 里的已知取值。**仅供分组，不是校验。** */
export function actionIsKnown(a: RequestActionEntry): boolean {
  return (KNOWN_ACTIONS as readonly string[]).includes(a.action)
}

/** ★★ 未知取值不是异常 —— 后端只判非空。UI 要能显示而不是拒绝。 */
export function requestActionsHasUnknownAction(r: RequestActionsResponse): boolean {
  return r.actions.some((a) => !actionIsKnown(a))
}

/**
 * ★ 摊平上来的额外键（不在四个恒在键、也不在已知条件键里）。
 * ⇒ UI 需要知道「这条动作带了额外上下文」而不是把它当未知。
 */
export function actionExtraKeys(a: RequestActionEntry): string[] {
  const known = new Set<string>([...ACTION_ALWAYS_KEYS, ...ACTION_OPTIONAL_KEYS])
  return Object.keys(a).filter((k) => !known.has(k)).sort()
}

/** ★★ 排序是否可信：`ts` 与 `seq` 都不带 omitempty ⇒ 恒在 ⇒ 通常可信。 */
export function requestActionsSortKeysPresent(r: RequestActionsResponse): boolean {
  return r.actions.every((a) => typeof a.ts === 'string' && typeof a.seq === 'number')
}

/** 客户端自己按 `(ts, seq)` 升序排 —— 零值 ts 会被排到最前，与后端一致。 */
export function requestActionsSorted(r: RequestActionsResponse): RequestActionEntry[] {
  return [...r.actions].sort((a, b) => {
    if (a.ts !== b.ts) return a.ts < b.ts ? -1 : 1
    return a.seq - b.seq
  })
}

/** ★ 升序是否成立（后端 `:73-75` 排过一次，但零值 ts 会让顺序看着反常）。 */
export function requestActionsAscending(r: RequestActionsResponse): boolean {
  for (let i = 0; i < r.actions.length - 1; i++) {
    const a = r.actions[i]!
    const b = r.actions[i + 1]!
    if (a.ts > b.ts) return false
    if (a.ts === b.ts && a.seq > b.seq) return false
  }
  return true
}

/** ★ `seq` 是否唯一 —— 不唯一就说明后端去重没生效或跨重启碰撞。 */
export function requestActionsSeqUnique(r: RequestActionsResponse): boolean {
  const seen = new Set<number>()
  for (const a of r.actions) {
    if (seen.has(a.seq)) return false
    seen.add(a.seq)
  }
  return true
}

/**
 * ★★ 这是**短期回放**不是历史：队列 `LTRIM 5000`（`liveactions.go:117`）
 *   ⇒ 更老的事件已被丢弃，响应里**没有任何字段**说明丢过。
 */
export function requestActionsIsShortLivedReplay(): boolean {
  return true
}

/** 502 的识别（Redis 读失败）—— 本仓少见的网关错误码。 */
export function requestActionsUnavailableReason(
  status: number,
  message: string,
): 'not_wired' | 'store_unavailable' | null {
  if (status === 503 && message === LIVE_ACTIONS_NOT_WIRED_MESSAGE) return 'not_wired'
  if (status === 502 && message === LIVE_ACTIONS_UNAVAILABLE_MESSAGE) return 'store_unavailable'
  return null
}

/** ★ 400 的 message 原文（供客户端区分「路径段空」）。 */
export function requestActionsIsMissingId(status: number, message: string): boolean {
  return status === 400 && message === MISSING_REQUEST_ID_MESSAGE
}

/* ── 内部工具 ────────────────────────────────────────────────────────────── */

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