import { req, type RequestOptions } from './client'

// requestLogs.ts — /api/logs（请求日志）+ /api/logs/{id}（单条详情）。
//
// 这是运维最高频入口，移动端此前完全没有。选它的理由：节点/供应商页回答的是
// 「现在谁不健康」，请求日志回答的是「刚才那次到底发生了什么」——后者是排障起点。
//
// 鉴权：h.admin（admin/handler.go:1268）⇒ tenant_admin 可用，且后端按
// 请求行自己的 tenant_id 过滤（logs.go:503-505），不是 JOIN api_keys。
//
// ⚠️★ **时间窗会被后端静默收窄**（logs.go:476 → :1326-1341）：
//   · 非 default 租户：跨度 > **72h（3 天）** ⇒ 被收窄到「以 end 为锚、回推 72h」
//   · default 租户 / super_admin：跨度 > **366 天** ⇒ 同样收窄
// ⇒ 用户选「最近 7 天」在非 default 租户上**只会拿到 3 天，且没有任何提示**。
//   所以移动端必须**按角色把时间选择器本身**收窄，而不是让用户选了再被后端打折。
//   这条已写进 pickDefaultWindow / maxWindowHours。

/** 与桌面 web/src/api/logs.ts:11- 的 RequestLogRow 逐字段对齐。 */
export interface RequestLogRow {
  ts: string
  request_id: string
  api_key_id: number | null
  end_user_id: string | null
  client_model: string | null
  outbound_model: string | null
  credential_id: number | null
  credential_label: string | null
  provider_id: number | null
  provider_name: string | null
  provider_code: string | null
  client_profile: string | null
  request_mode: string | null
  prompt_tokens: number | null
  completion_tokens: number | null
  cache_read_tokens: number | null
  cache_write_tokens: number | null
  total_tokens: number | null
  /** ⚠️ 后端可能是 number 也可能是 string（见桌面同名字段注释），别当成纯 number 用。 */
  cost_usd: number | string | null
  cost_display: number | string | null
  cost_currency: string | null
  latency_ms: number | null
  success?: boolean
  error_kind?: string | null
  request_status?: 'in_progress' | 'success' | 'failure' | 'rate_limited' | null
  [k: string]: unknown
}

export interface RequestLogsAggregate {
  total_requests?: number
  success_requests?: number
  failure_requests?: number
  total_cost?: number | string
  total_tokens?: number
  avg_latency_ms?: number
  p95_latency_ms?: number
  [k: string]: unknown
}

export interface RequestLogsResponse {
  items: RequestLogRow[]
  /** 命中总数（COUNT），不是本页条数。 */
  count: number
  aggregate?: RequestLogsAggregate
}

/** 后端 page_size 硬上限（logs.go:486-488），超了静默 clamp 到 500。 */
export const REQUEST_LOG_MAX_PAGE_SIZE = 500

export type RequestLogStatus = 'in_progress' | 'success' | 'failure' | 'rate_limited'

export interface RequestLogParams {
  page?: number
  page_size?: number
  q?: string
  model?: string
  request_status?: RequestLogStatus
  error_kind?: string
  credential_id?: number
  provider_id?: number
  from?: string
  to?: string
}

/** 非 default 租户的最大查询跨度（后端 logs.go:1322）。 */
export const TENANT_MAX_WINDOW_HOURS = 72
/** default 租户 / super_admin 的最大跨度。 */
export const DEFAULT_MAX_WINDOW_DAYS = 366

/**
 * 按租户算出**允许的最大查询小时数**，供 UI 收窄时间选择器。
 *
 * tenant_id 为空或 'default' 走 366 天档（logs.go:1337 的判定正是这个条件）。
 * 注意这是**按租户**而非按角色 —— super_admin 访问 default 租户数据时同样受 366 天限。
 */
export function maxWindowHours(tenantId: string | null | undefined): number {
  const t = (tenantId ?? '').trim()
  if (t !== '' && t !== 'default') return TENANT_MAX_WINDOW_HOURS
  return DEFAULT_MAX_WINDOW_DAYS * 24
}

export function fetchRequestLogs(
  params?: RequestLogParams,
  options?: RequestOptions,
): Promise<RequestLogsResponse> {
  const qs = new URLSearchParams()
  if (params?.page != null) {
    const n = Math.trunc(params.page)
    // 后端 page<1 归 1（logs.go:479-481，**不** 400）⇒ 前端不发非法值即可
    if (Number.isFinite(n) && n >= 1) qs.set('page', String(n))
  }
  if (params?.page_size != null) {
    const n = Math.trunc(params.page_size)
    if (Number.isFinite(n) && n >= 1) qs.set('page_size', String(Math.min(n, REQUEST_LOG_MAX_PAGE_SIZE)))
  }
  if (params?.q) qs.set('q', params.q)
  if (params?.model) qs.set('model', params.model)
  if (params?.request_status) qs.set('request_status', params.request_status)
  if (params?.error_kind) qs.set('error_kind', params.error_kind)
  if (params?.credential_id != null) qs.set('credential_id', String(params.credential_id))
  if (params?.provider_id != null) qs.set('provider_id', String(params.provider_id))
  if (params?.from) qs.set('from', params.from)
  if (params?.to) qs.set('to', params.to)
  const s = qs.toString()
  return req<RequestLogsResponse>('GET', `/api/logs${s ? '?' + s : ''}`, undefined, options)
}

/**
 * 单条详情。⚠️ `omit_body=1` 跳过后端 body 抓取（logs.go 二阶段：hot heap → 列存月分区）。
 * 移动端首屏用 omit_body=true：先出元信息，再按需补 body —— 移动网络下 body 可能很大。
 */
export interface RequestLogDetail extends RequestLogRow {
  request_body?: string | null
  response_body?: string | null
  outbound_request_body?: string | null
  outbound_response_body?: string | null
  request_body_omitted?: boolean
  [k: string]: unknown
}

export function fetchRequestLogDetail(
  requestId: string,
  opts?: { omitBody?: boolean },
  options?: RequestOptions,
): Promise<RequestLogDetail> {
  const qs = opts?.omitBody ? '?omit_body=1' : ''
  return req<RequestLogDetail>(
    'GET',
    `/api/logs/${encodeURIComponent(requestId)}${qs}`,
    undefined,
    options,
  )
}

/**
 * 成本字段归一。后端 cost_usd / cost_display 可能是 number 也可能是 string
 * （列存路径按文本返回）。直接 `toFixed` 会在这两种形态之一上炸掉或显示成
 * "0.012300000000000002"。⇒ 统一走 Number() 且对非数值返回 null。
 *
 * ★ 空串必须**先挡掉**：`Number('')` 是 **0**（不是 NaN），所以只判
 * `Number.isFinite` 会把「没有成本数据」渲染成「$0.0000」——那是在断言一个
 * 我们没有依据的数字。判据：requestLogs.test.ts 的 `costNumber('')` 用例。
 */
export function costNumber(v: number | string | null | undefined): number | null {
  if (v == null) return null
  if (typeof v === 'string' && v.trim() === '') return null
  const n = typeof v === 'number' ? v : Number(v)
  return Number.isFinite(n) ? n : null
}
