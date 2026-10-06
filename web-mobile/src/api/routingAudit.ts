import { req, type RequestOptions } from './client'

// routingAudit.ts — 路由**覆盖规则**的变更审计（谁、什么时候、改了什么）。
//   GET /api/admin/routing/overrides/audit
//
// ⚠️★ 这是排障线里**唯一的 superAdmin 档**：handler.go:1381
//   `autoH.RegisterAutoRouteRoutes(mux, h.superAdmin)`，auth.go:353-357 对
//   `claims.Role != "super_admin"` 直接 403。
//   ⇒ 移动端导航必须 `requiresRole: 'super_admin'`，否则等于给 tenant_admin 一个
//   必然失败的入口（17 §11.1 对凭据操作区定的规矩）。
//
// 它回答的是「这条请求为什么没按默认路由走」——除非有人（通常是运维自己）
// 悄悄加了一条 override。所以它和 routing-log 互补：
//   routing-log = 运行时**发生了什么**（探测/切换/熔断）
//   routing-audit = 配置面**谁改了什么**（insert / update / delete）
//
// ⚠️ 参数越界是 **400，不是 clamp**：
//   days  1..90（默认 7），越界 400（routing_overrides.go:351-358）
//   limit 1..1000（默认 200），越界 400（:360-367）
// ★ 与本仓库另一处同类参数语义相反：admin/audit_operations.go:95-103 的
//   limit 越界是**静默 clamp**。两处不能互相照抄。
//   ⇒ 前端一律在发出前夹住，别让用户吃 400。

export type RoutingAuditAction = 'insert' | 'update' | 'delete'

export const ROUTING_AUDIT_MIN_DAYS = 1
export const ROUTING_AUDIT_MAX_DAYS = 90
export const ROUTING_AUDIT_DEFAULT_DAYS = 7
export const ROUTING_AUDIT_MIN_LIMIT = 1
export const ROUTING_AUDIT_MAX_LIMIT = 1000
export const ROUTING_AUDIT_DEFAULT_LIMIT = 200

/**
 * 审计条目（routing_overrides.go:323-336）。
 *
 * ⚠️ 除 id / ts / action 外**全部是指针 + omitempty** ⇒ 可能是「键不存在」
 *   或「值为 null」，渲染时都要当「没有」处理。
 */
export interface RoutingAuditEntry {
  id: number
  ts: string
  action: RoutingAuditAction | (string & {})
  override_id?: number
  task_type?: string
  profile?: string
  mode?: string
  model_chosen?: string
  reason?: string
  expires_at?: string
  old_expires_at?: string
  actor?: string
  [k: string]: unknown
}

/**
 * 回显的过滤条件。
 *
 * ★ 四个值**全是 string** —— 包括 `days`（后端 `strconv.Itoa(days)`），
 *   routing_overrides.go:424-429 的类型就是 `map[string]string`。
 *   当成 number 用会得到 `undefined`。
 */
export interface RoutingAuditFilterEcho {
  action: string
  actor: string
  override_id: string
  days: string
}

export interface RoutingAuditResponse {
  entries: RoutingAuditEntry[]
  count: number
  filter: RoutingAuditFilterEcho
}

export interface RoutingAuditParams {
  action?: RoutingAuditAction
  /** 精确等值匹配，非模糊。 */
  actor?: string
  override_id?: number
  days?: number
  limit?: number
}

export const ROUTING_AUDIT_ACTIONS: readonly RoutingAuditAction[] = ['insert', 'update', 'delete'] as const

export function fetchRoutingAudit(
  params?: RoutingAuditParams,
  options?: RequestOptions,
): Promise<RoutingAuditResponse> {
  const qs = new URLSearchParams()
  if (params?.action) qs.set('action', params.action)
  if (params?.actor) qs.set('actor', params.actor)
  if (params?.override_id != null && Number.isFinite(params.override_id)) {
    // ★ 后端对解析失败是**静默忽略该过滤条件**（:377-381）而不是报错。
    //   所以发一个非数字 override_id 会让用户以为在按 ID 过滤、其实没过滤。
    //   这里只发合法数字。
    qs.set('override_id', String(Math.trunc(params.override_id)))
  }
  if (params?.days != null) {
    const n = Math.trunc(params.days)
    const clamped = Math.max(ROUTING_AUDIT_MIN_DAYS, Math.min(n, ROUTING_AUDIT_MAX_DAYS))
    qs.set('days', String(Number.isFinite(clamped) ? clamped : ROUTING_AUDIT_DEFAULT_DAYS))
  }
  if (params?.limit != null) {
    const n = Math.trunc(params.limit)
    const clamped = Math.max(ROUTING_AUDIT_MIN_LIMIT, Math.min(n, ROUTING_AUDIT_MAX_LIMIT))
    qs.set('limit', String(Number.isFinite(clamped) ? clamped : ROUTING_AUDIT_DEFAULT_LIMIT))
  }
  const s = qs.toString()
  return req<RoutingAuditResponse>('GET', `/api/admin/routing/overrides/audit${s ? '?' + s : ''}`, undefined, options)
}

/**
 * 变更的人类可读摘要。
 *
 * ★ 多数字段是 omitempty，缺就**不要**拼出「从 到 」这种带空格的残句。
 *   返回 null 让调用方走「只显示动作 + 时间」的降级呈现，
 *   而不是显示一句读不通的话。
 */
export function describeAuditChange(e: RoutingAuditEntry): string | null {
  const parts: string[] = []
  if (e.task_type) parts.push(e.task_type)
  if (e.profile) parts.push(e.profile)
  if (e.mode) parts.push(e.mode)
  if (e.model_chosen) parts.push(e.model_chosen)
  if (parts.length === 0) return null
  return parts.join(' · ')
}

export const AUDIT_ACTION_TONE: Record<string, 'success' | 'warning' | 'danger'> = {
  insert: 'success',
  update: 'warning',
  delete: 'danger',
}
