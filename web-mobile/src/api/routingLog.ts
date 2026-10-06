import { req, type RequestOptions } from './client'

// routingLog.ts — 路由/探测/状态变更的**事件流水**。
//   GET /api/credentials/routing-log
//
// 为什么是 `/api/credentials/` 而不是 `/api/admin/`：这条路由不在 admin 命名空间下，
// 它注册在 monitor handler 里（admin/credential_monitor.go:171），经
// handler.go:1453 `monitorH.RegisterMonitorRoutes(mux, h.admin)` 挂载。
// ★ 写成 `/api/admin/credentials/routing-log` 会 404 —— 而且 404 与「时间窗超限」
//   返回的 400 长得很像，都是 `{"error":"..."}` 文本，容易误判成「这个功能没数据」。
//
// 鉴权：h.admin。文件注释明写「允许 tenant_admin 访问凭据监控页面」⇒ tenant_admin 可用。
//
// 本模块的两个硬约束（都会以 400 形式打断，不是静默）：
//   · 时间窗 **> 7d 直接 400**（credential_routing_log.go:235-237），
//     所以时间选择器的**选项本身**必须按 7 天封顶，不能让用户选了再吃 400。
//   · `kind` / `result` 非法值 400（:96-110）—— 用字面量类型在编译期挡住。
//
// 本端点**没有降级标记**：DB 没配返回 503 "database not configured"，
// 查询失败返回 500 "routing log query failed"（都不外泄 SQL 细节）。
// ⇒ 这里不存在「降级显示成没数据」的风险，但也别自己造一个。

export type RoutingLogKind = 'all' | 'routing' | 'probe' | 'state_change'
export type RoutingLogResult = 'all' | 'success' | 'failed'

/** 后端硬上限：窗口 > 7d 直接 400，不是 clamp。UI 用它封顶选项。 */
export const ROUTING_LOG_MAX_WINDOW_HOURS = 7 * 24
export const ROUTING_LOG_MAX_LIMIT = 500
export const ROUTING_LOG_DEFAULT_LIMIT = 100

/**
 * 流水条目（RoutingLogEntry, credential_routing_log.go:23-45）。
 *
 * ⚠️ 大量字段是**指针 + omitempty** ⇒ JSON 里可能是「键不存在」或「值是 null」，
 *   两者在 TS 里都表现为 undefined / null，渲染时都要当「没有」处理。
 */
export interface RoutingLogEntry {
  ts: string
  kind: string
  /** 只在 kind=state_change 时出现。 */
  change?: 'recovered' | 'broke' | 'online' | 'offline' | (string & {})
  model: string
  credential_id?: number
  credential_label: string
  provider_name: string
  success?: boolean
  status: string
  latency_ms?: number
  error_code?: string
  error_message?: string
  request_id?: string
  tier?: number
  source: string
  actor?: string
  http_status?: number
  sticky?: boolean
  outbound_model?: string
  detail?: string
  [k: string]: unknown
}

export interface RoutingLogMeta {
  time_start: string
  time_end: string
  kind: string
  model: string
  result: string
  limit: number
  offset: number
  duration_ms: number
  [k: string]: unknown
}

/** 响应信封（credential_routing_log.go:48-52）。 */
export interface RoutingLogResponse {
  meta: RoutingLogMeta
  entries: RoutingLogEntry[]
  total: number
}

export interface RoutingLogParams {
  /** RFC3339。格式错 400；end < start 400；跨度 > 7d 400。 */
  time_start?: string
  time_end?: string
  kind?: RoutingLogKind
  result?: RoutingLogResult
  /** 模型名子串，大小写不敏感。空 = 不过滤。 */
  model?: string
  credential_id?: number
  limit?: number
  offset?: number
}

export const ROUTING_LOG_KINDS: readonly RoutingLogKind[] = ['all', 'routing', 'probe', 'state_change'] as const
export const ROUTING_LOG_RESULTS: readonly RoutingLogResult[] = ['all', 'success', 'failed'] as const

export function fetchRoutingLog(
  params?: RoutingLogParams,
  options?: RequestOptions,
): Promise<RoutingLogResponse> {
  const qs = new URLSearchParams()
  if (params?.time_start) qs.set('time_start', params.time_start)
  if (params?.time_end) qs.set('time_end', params.time_end)
  if (params?.kind) qs.set('kind', params.kind)
  if (params?.result) qs.set('result', params.result)
  if (params?.model) qs.set('model', params.model)
  if (params?.credential_id != null) qs.set('credential_id', String(params.credential_id))
  if (params?.limit != null) {
    // 后端 >500 是**静默 clamp**（:118-123），<=0 回落 100。前端自己封顶，
    // 免得 meta.limit 回显 500 而用户以为发了 1000。
    const n = Math.trunc(params.limit)
    if (Number.isFinite(n) && n > 0) qs.set('limit', String(Math.min(n, ROUTING_LOG_MAX_LIMIT)))
  }
  if (params?.offset != null) {
    const n = Math.trunc(params.offset)
    if (Number.isFinite(n) && n > 0) qs.set('offset', String(n))
  }
  const s = qs.toString()
  return req<RoutingLogResponse>('GET', `/api/credentials/routing-log${s ? '?' + s : ''}`, undefined, options)
}

/**
 * 把「当前窗口 + limit + offset」算成请求参数。
 *
 * ⚠️ 时间窗在**发请求前**夹到 7 天内（RoutingLogMaxWindowHours）。这里必须夹，
 * 因为超窗是 400 硬失败而不是降级 —— 让用户点了才报错，等于把后端约束暴露给用户。
 */
export function buildRoutingLogWindow(hours: number, now = Date.now()): { time_start: string; time_end: string } {
  const h = Math.max(1, Math.min(hours, ROUTING_LOG_MAX_WINDOW_HOURS))
  return {
    time_end: new Date(now).toISOString(),
    time_start: new Date(now - h * 3600_000).toISOString(),
  }
}
