// requestDetail.ts — 统一请求详情（unified request detail）的只读面。
//   GET /api/admin/request-detail/{request_id}?omit_body=1|true
//
// 鉴权：`admin/handler.go:1271` 用 `admin(...)` 注册 ⇒ **admin 档**。
//
// ⚠️⚠️⚠️ 这是本仓最「厚」的一个只读端点：一个 request_id 背后有**五层取数**
// 与**五种状态码**，且其中两种的含义反直觉。
//
// (1) ★★★★★★ **404 身兼两职**，且其中一职是 **fail-closed**：
//     `ErrNotFound` ⇒ 404 `request detail not found`；
//     而「**跨租户**或 **meta.tenant_id 为空**」⇒ **也是** 404
//     （handleUnifiedRequestDetail 的最后一段）。
//     注释自陈这是 2026-08-26 的 P1-29 修复：此前只对 `PersistencePersisted`
//     做了租户校验，**在途/落盘路径对知道别人 request_id 的 tenant_admin 是敞开的**。
//     ★ 空 TenantID 被当作「来源不明」而**拒绝**，理由是「早于该提交写入的
//       遗留在途 meta 没有租户记录，不能跨租户泄漏」。
//     ⇒ 客户端义务：**404 不许渲染成「这个请求不存在」**，
//       必须说明「也可能是别人的请求、或这条记录没有租户归属」。
//
// (2) ★★★★★ **413 不是 500**：`ErrBodyTooLarge`（单 body > 10MB，`MaxBodyFileSize`）
//     ⇒ 413 `request body exceeds 10MB limit`。
//     注释自陈 2026-08-29 审计跟进：映射成 500 会误导运维和客户端。
//
// (3) ★★★★ **503 `request detail store not configured`**：
//     `h.requestDetailLocator == nil` 时直接 503
//     ⇒ 这是**部署未接线**，不是「查不到」。
//
// (4) ★★★★ `omit_body` **只认两个字面值**：
//     `== "1" || == "true"`。`yes` / `TRUE` / `on` 一律**不生效**
//     ⇒ 客户端**只发 `1` 或 `true`**，绝不发别的。
//     ★ 发 `omit_body=0` 也**不会**让后端去取 body（`"0" != "1"` 且 `!= "true"`）——
//       它就是「不过滤」，不是「要 body」。
//
// (5) ★★★★ **五种 `source`**，含义完全不同：
//     `memory`（本进程在途内存）/ `file`（本地落盘）/ `live_stream`（Redis 实时流）
//     / `request_logs`（DB 审计表）/ `session_turns`（跨会话轮次表）
//     ⇒ 「从哪拿到的」决定**新鲜度与可信度**：`memory` 最实时但只在当前进程，
//       `request_logs` 是已落库的真相。页面必须显示 source。
//
// (6) ★★★★ `body_status` 是**两态**契约（available / unavailable），
//     而且口径反直觉（types.go:38-56 自陈 R73 订正）：
//     · `available`   —— request/response/outbound 三者**至少一个**带真实载荷；
//                       **空容器（`[]` / `{}`）也算载荷**。
//     · `unavailable` —— 三者**全是** SQL NULL / JSON null 字面量 / 纯空白。
//     ★★ `admin/body_status.go` 的 `columnHasPayload` 与迁移后的 SQL 探针
//       `<> 'null'::jsonb` 都把 **JSON null 判为「无载荷」**。
//       而 JSON null 恰是写路径对「载荷为空」的**常态编码**（`bodies_writer jsonTextOrNull`）。
//     ⇒ **不能**用「body_status=available 就一定有内容」来渲染。
//
// (7) ★★★ `body_status` 带 `omitempty` ⇒ **键可能整个缺失**，
//     注释明写「缺失一律当作 unknown / not computed」——
//     它**不是** available、也**不是** unavailable。
//     ★ `dropped` **有意不在**线上契约里：区分「被保留期清理掉」与「从没写过」
//       所需的保留期与 bodies_trimmer 作业都还没落地。
//
// (8) ★★★ **错误信封是 `{"error":{"detail":"…"}}`**（`admin/handler.go:1494-1498`
//     的 `writeError`）⇒ 本仓的 `error.detail` 族，`client.ts` 的 `errorMessage` 兜得住。
//
// (9) ★★★ **`request_id` 有三种合法形态**（`ValidateRequestID`，store.go:43/440-451），
//     长度 8..128：
//     ① 32 位纯 hex（大小写皆可）；② 带连字符的 uuid；③ **必须以字母开头**的带前缀形态。
//     ★ 旧正则 `^[A-Za-z0-9._-]{8,128}$` 被判定**过宽**并废弃：
//       任何 8 字符的 `abc..def` 都会被接受，而 `..` 在下游工具漏调
//       `filepath.Base` 时是**路径穿越**向量。
//     ⇒ 客户端**提交前先按同一规则本地校验**，别拿明显非法的 id 去换一个 400。
//
// (10) ★ `Bodies` 的三个字段都是 `json.RawMessage` + `omitempty`
//     ⇒ **形状不定**（对象/数组/字符串/number 皆可能），原样透传不解析。

import { req, type RequestOptions } from './client'

// ── 字面量（照抄，不是猜的） ──────────────────────────────────────────────

/** ★ `Source` 的全部 5 个值（domains/requestdetail/types.go:12-18）。 */
export const REQUEST_DETAIL_SOURCES = [
  'memory',
  'file',
  'live_stream',
  'request_logs',
  'session_turns',
] as const
export type RequestDetailSource = (typeof REQUEST_DETAIL_SOURCES)[number]

/** ★ `Persistence` 的两个值（types.go:21-24）。 */
export const REQUEST_DETAIL_PERSISTENCE = ['in_flight', 'persisted'] as const
export type RequestDetailPersistence = (typeof REQUEST_DETAIL_PERSISTENCE)[number]

/**
 * ★ `body_status` 的**两个**线上取值。
 * ★★ 键**可能缺失** ⇒ 缺失是第三种状态「未知/未计算」，不是这两个之一。
 * ★ `dropped` 有意**不在**契约里（见坑 7）。
 */
export const REQUEST_DETAIL_BODY_STATUSES = ['available', 'unavailable'] as const
export type RequestDetailBodyStatus = (typeof REQUEST_DETAIL_BODY_STATUSES)[number]

/** ★ 键缺失 / 值为空 ⇒ 未知（区别于 available 与 unavailable）。 */
export const REQUEST_DETAIL_BODY_STATUS_UNKNOWN = 'unknown' as const

/** ★ `MaxBodyFileSize` = 10MB（store.go）；超了是 **413** 不是 500。 */
export const REQUEST_DETAIL_MAX_BODY_BYTES = 10 * 1024 * 1024

// ── request_id 本地校验（与 ValidateRequestID 同规则） ────────────────────

/**
 * ★ 与 `requestdetail.ValidateRequestID`（store.go:440-451）**同规则**的本地校验。
 *
 * 三种形态（`safeRequestIDPattern`，store.go:43-45）：
 *  ① `^[0-9a-f]{32}$`（大小写皆可）
 *  ② 带连字符的 uuid
 *  ③ **必须以字母开头**，其后为「一串安全字符」或「单个分隔符 + 恰好一个安全字符」
 *
 * 长度 8..128。③ 里「分隔符不可连用」是为了拒掉 `..`（路径穿越向量）。
 */
export function isValidRequestId(id: string | null | undefined): boolean {
  if (typeof id !== 'string') return false
  const v = id.trim()
  if (v.length < 8 || v.length > 128) return false
  const hex32 = /^[0-9a-f]{32}$/i.test(v)
  const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(v)
  // ③ 前缀形态：字母开头 + 重复的「安全串 或 (分隔符 + 一个安全字符)」
  const prefixed = /^[A-Za-z](?:[A-Za-z0-9]+|[._-][A-Za-z0-9])+$/.test(v)
  return hex32 || uuid || prefixed
}

// ── 形状 ──────────────────────────────────────────────────────────────────

/**
 * ★ 指针字段一律带 `omitempty` ⇒ **键可能整个不存在**（不是「值为 null」）。
 * 另注：`Bodies` 的三个字段是 `json.RawMessage` ⇒ **形状不定**，原样透传。
 */
export interface RequestDetailMeta {
  request_id: string
  tenant_id?: string
  gw_session_id?: string
  gw_task_id?: string
  client_model?: string
  request_status?: string
  success?: boolean
  latency_ms?: number
  turn_number?: number
  /** ★ 键可能缺失 ⇒ 未知。`dropped` **不是**合法取值。见坑 6/7。 */
  body_status?: string
}

export interface RequestDetail {
  source: string
  persistence: string
  meta: RequestDetailMeta
  /** ★ `*Bodies` + omitempty ⇒ 整块可能不存在（`omit_body=1` 时尤其如此）。 */
  bodies?: {
    request_body?: unknown
    response_body?: unknown
    outbound_body?: unknown
  }
  warning?: string
}

// ── 取数 ──────────────────────────────────────────────────────────────────

export interface RequestDetailOptions {
  /** ★ 后端只认 `1` 与 `true`；其它字面值一律**不生效**。见坑 4。 */
  omitBody?: boolean
  options?: RequestOptions
}

export function fetchRequestDetail(
  requestId: string,
  opts: RequestDetailOptions = {},
): Promise<RequestDetail> {
  const id = (requestId ?? '').trim()
  if (!isValidRequestId(id)) {
    // ★ 本地就拦，别拿明显非法的 id 去换一个 400（见坑 9）。
    return Promise.reject(new Error(`request-detail: 非法 request_id「${requestId}」（三种形态之一，长度 8..128）`))
  }
  // ★ 只发 1 / true 这两个字面值；`omit_body=0` 不是「要 body」。
  const q = opts.omitBody ? '?omit_body=1' : ''
  return req<unknown>('GET', `/api/admin/request-detail/${encodeURIComponent(id)}${q}`, undefined, opts.options).then(
    unwrapRequestDetail,
  )
}

/**
 * ★ 后端 `writeJSON(w, 200, detail)`（unified_detail.go:474）传的是 `*Detail` 本身
 * ⇒ 响应**没有包装键**，顶层就是 `{source, persistence, meta, bodies?, warning?}`。
 * 形状不符**抛错**，绝不当成「没查到」返回 null。
 */
export function unwrapRequestDetail(resp: unknown): RequestDetail {
  if (resp && typeof resp === 'object' && !Array.isArray(resp)) {
    const d = resp as RequestDetail
    if (d.meta && typeof d.meta === 'object' && typeof d.meta.request_id === 'string') {
      return d
    }
  }
  const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
  throw new Error(
    `request-detail 响应形状不符：期望 {source, persistence, meta:{request_id,…}}，实得 ${actual}`,
  )
}

// ── 页面侧的判读工具 ────────────────────────────────────────────────────

/**
 * ★★ `body_status` 的**三态**判读（`unknown` 不是契约值，是「键缺失」的表述）：
 * - `'available'`   ⇒ 至少一个 body 带载荷（**空容器也算**）
 * - `'unavailable'` ⇒ 三个 body 全是 NULL / JSON null / 空白
 * - `'unknown'`     ⇒ **键缺失**（生产者未采用该契约）
 */
export function bodyStatusOf(meta: RequestDetailMeta | null | undefined): string {
  const raw = meta?.body_status
  if (typeof raw !== 'string' || raw === '') return REQUEST_DETAIL_BODY_STATUS_UNKNOWN
  return raw
}

/**
 * ★★ **不能**用「`body_status === 'available'` ⇒ 一定有内容」来渲染（见坑 6）：
 * 写路径对「载荷为空」的常态编码就是 **JSON null**，而实现与 SQL 探针都把它
 * 判为**无载荷**；反之空容器 `[]`/`{}` 又被算作**有载荷**。
 * ⇒ 这里只看「三块 body 的键是否**都**不存在」，不做内容断言。
 */
export function bodiesPresent(detail: RequestDetail | null | undefined): boolean {
  const b = detail?.bodies
  if (!b || typeof b !== 'object') return false
  return b.request_body !== undefined || b.response_body !== undefined || b.outbound_body !== undefined
}

/** ★ `source` 是否是「当前进程在途」——决定这份数据有多实时、丢了会不会再没有。 */
export function isInFlightSource(detail: RequestDetail | null | undefined): boolean {
  return detail?.source === 'memory' || detail?.source === 'live_stream'
}

/**
 * ★★ 404 的两副面孔（见坑 1）：后端**无法**区分
 * 「真没有这条记录」与「这是别人的请求 / 这条记录没有租户归属」。
 * ⇒ 返回 true 时页面**必须**说「也可能是跨租户被拒」，不能只说「不存在」。
 */
export function notFoundIsAlsoTenantDenied(err: unknown): boolean {
  const msg = (err as Error)?.message ?? ''
  return /request detail not found/i.test(msg)
}
