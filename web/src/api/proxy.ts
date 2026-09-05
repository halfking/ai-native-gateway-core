import { req } from './_core'

// proxy.ts — 代理管理前端 API 客户端（对应后端 /api/proxy/* 超级管理员接口）。
// 字符串一律走 i18n，这里只负责请求与类型。错误由调用方处理（与 admin.ts 一致）。

export interface ProxySubscription {
  id: number
  name: string
  subscribe_url: string
  status: string
  node_count: number
  last_fetch_at: string | null
  last_fetch_status: string
  last_error: string
  created_at: string
  updated_at: string
}

export interface ProxyNode {
  id: number
  subscription_id: number
  name: string
  protocol: string
  server: string
  port: number
  username: string
  has_password: boolean // 绝不返回密码原文
  dialable: boolean // 是否可被 Go 直接拨号（http/https/socks5）
  location: string
  status: string
  health_check_url: string
  last_health_check_at: string | null
  last_health_check_status: string
  response_time_ms: number
  success_rate: number
  consecutive_failures: number
  created_at: string
  updated_at: string
}

export interface ProxyStatus {
  subscription_count: number
  active_subscriptions: number
  node_count: number
  by_protocol: Record<string, number>
  dialable_count: number
  unhealthy_count: number
  healthy_count: number
  selected_node: ProxyNode | null
  selection_error: string
  warning: string
}

export interface ProxyHealthCheckResult {
  ok: boolean
  status_code?: number
  response_time_ms?: number
  error?: string
}

// 后端列表端点统一返回 { items: [...], total: N } 信封（与 /api/admin/center/* 等一致），
// 这里统一解包为裸数组；返回 null / 非对象时兜底为空数组，
// 避免前端拿到 {items: ...} 对象后被当作数组迭代、读到 .id 触发 null 崩溃。
function unwrapItems<T>(body: unknown, isItem: (item: unknown) => item is T): T[] {
  const items = Array.isArray(body)
    ? body
    : body && typeof body === 'object' && Array.isArray((body as { items?: unknown }).items)
      ? (body as { items: unknown[] }).items
      : []
  return items.filter(isItem)
}

function isRecord(item: unknown): item is Record<string, unknown> {
  return !!item && typeof item === 'object'
}

function isNullableString(value: unknown): value is string | null {
  return value === null || typeof value === 'string'
}

function isProxySubscription(item: unknown): item is ProxySubscription {
  return isRecord(item)
    && typeof item.id === 'number'
    && typeof item.name === 'string'
    && typeof item.subscribe_url === 'string'
    && typeof item.status === 'string'
    && typeof item.node_count === 'number'
    && isNullableString(item.last_fetch_at)
}

function isProxyNode(item: unknown): item is ProxyNode {
  return isRecord(item)
    && typeof item.id === 'number'
    && typeof item.name === 'string'
    && typeof item.protocol === 'string'
    && typeof item.server === 'string'
    && typeof item.port === 'number'
    && typeof item.dialable === 'boolean'
    && typeof item.status === 'string'
    && typeof item.response_time_ms === 'number'
    && typeof item.consecutive_failures === 'number'
}

function listRequest<T>(path: string, isItem: (item: unknown) => item is T): Promise<T[]> {
  return req<unknown>('GET', path).then((body) => unwrapItems(body, isItem))
}

export function getProxySubscriptions(): Promise<ProxySubscription[]> {
  return listRequest('/api/proxy/subscriptions', isProxySubscription)
}

export function createProxySubscription(data: {
  name: string
  subscribe_url: string
  notes?: string
}): Promise<ProxySubscription> {
  return req<ProxySubscription>('POST', '/api/proxy/subscriptions', data)
}

export function refreshProxySubscription(id: number): Promise<{ ok: boolean; node_count: number; error?: string }> {
  return req<{ ok: boolean; node_count: number; error?: string }>('POST', `/api/proxy/subscriptions/${id}/refresh`)
}

export function deleteProxySubscription(id: number): Promise<{ ok: boolean }> {
  return req<{ ok: boolean }>('DELETE', `/api/proxy/subscriptions/${id}`)
}

export function getProxyNodes(subscriptionID?: number, dialable?: boolean): Promise<ProxyNode[]> {
  const qs = new URLSearchParams()
  if (subscriptionID !== undefined) qs.set('subscription_id', String(subscriptionID))
  if (dialable !== undefined) qs.set('dialable', dialable ? 'true' : 'false')
  const q = qs.toString()
  return listRequest(`/api/proxy/nodes${q ? `?${q}` : ''}`, isProxyNode)
}

export function createProxyNode(data: {
  subscription_id: number
  name: string
  protocol: string
  server: string
  port: number
  username?: string
  password?: string
  location?: string
  health_check_url?: string
}): Promise<ProxyNode> {
  return req<ProxyNode>('POST', '/api/proxy/nodes', data)
}

export function healthCheckProxyNode(id: number): Promise<ProxyHealthCheckResult> {
  return req<ProxyHealthCheckResult>('POST', `/api/proxy/nodes/${id}/health-check`)
}

export function deleteProxyNode(id: number): Promise<{ ok: boolean }> {
  return req<{ ok: boolean }>('DELETE', `/api/proxy/nodes/${id}`)
}

export function getProxyStatus(): Promise<ProxyStatus> {
  return req<ProxyStatus>('GET', '/api/proxy/status')
}
