import { req } from './_core'
import type { UserInfo } from './_core'
import { fetchSessionOverview, type SessionOverviewData } from './dashboard'
import type { DegradationMarker } from '../composables/useDegradationMarker'

// admin.ts — v6.0 audit T12 (2026-06-22)
// Super-admin surfaces: user CRUD, audit log, tenant management +
// tenant-scoped users/keys/stats. All routes under /api/admin/...
// require the super_admin role (distinct from the admin role on
// /api/admin/... resources below). The "tenant" surface also includes
// the static status enums / label maps / color maps used by the UI.

// ── Dashboard Session Stats (2026-07-07) ──

export type SessionOverviewResponse = SessionOverviewData

export async function getSessionOverview(days = 7, tenantId?: string): Promise<SessionOverviewResponse> {
  const { data } = await fetchSessionOverview({ days, tenant_id: tenantId })
  return data
}

// ── Session Analytics - Client/Task Dimension (P2, 2026-07-07) ──

export interface ClientAnalyticsSummary {
  client_id: string
  session_count: number
  active_sessions_24h: number
  total_requests: number
  total_cost_usd: number | null
  avg_cost_per_session: number | null
  usd_unmeasured_requests?: number
  pending_since?: string | null
  /** Average health score (0-100 integer range, nullable) */
  avg_health_score?: number
  health_distribution: {
    a: number
    b: number
    c: number
    d: number
    f: number
  }
  total_success: number
  total_errors: number
  avg_latency_ms?: number
  first_seen_at: string
  last_seen_at: string
  models_used: string[]
}

export interface ClientAnalyticsDetail extends ClientAnalyticsSummary {
  related_tasks: Array<{
    task_id: string
    session_count: number
    total_cost_usd: number | null
    /** Average health score (0-100 integer range, nullable) */
    avg_health?: number
    last_activity: string
  }>
  daily_cost_trend: Array<{
    date: string
    cost: number | null
    sessions: number
  }>
  recent_sessions: Array<{
    session_id: string
    request_count: number
    cost_usd: number | null
    health_score?: number
    health_grade?: string
    created_at: string | null
  }>
}

export interface TaskAnalyticsSummary {
  task_id: string
  session_count: number
  active_sessions_24h: number
  total_requests: number
  total_cost_usd: number | null
  avg_cost_per_session: number | null
  usd_unmeasured_requests?: number
  pending_since?: string | null
  /** Average health score (0-100 integer range, nullable) */
  avg_health_score?: number
  health_distribution: {
    a: number
    b: number
    c: number
    d: number
    f: number
  }
  total_success: number
  total_errors: number
  avg_latency_ms?: number
  first_seen_at: string
  last_seen_at: string
  models_used: string[]
  clients_used: string[]
}

export interface TaskAnalyticsDetail extends TaskAnalyticsSummary {
  related_clients: Array<{
    client_id: string
    session_count: number
    total_cost_usd: number | null
    /** Average health score (0-100 integer range, nullable) */
    avg_health?: number
    last_activity: string
  }>
  daily_cost_trend: Array<{
    date: string
    cost: number | null
    sessions: number
  }>
  recent_sessions: Array<{
    session_id: string
    request_count: number
    cost_usd: number | null
    health_score?: number
    health_grade?: string
    created_at: string | null
  }>
}

export function getClientAnalyticsList(params?: {
  limit?: number
  offset?: number
  order_by?: 'cost' | 'sessions' | 'health'
  tenant_id?: string
}) {
  const q = new URLSearchParams()
  if (params?.limit) q.set('limit', String(params.limit))
  if (params?.offset) q.set('offset', String(params.offset))
  if (params?.order_by) q.set('order_by', params.order_by)
  if (params?.tenant_id) q.set('tenant_id', params.tenant_id)
  return req<{
    clients: ClientAnalyticsSummary[]
    total: number
    limit: number
    offset: number
    refreshed_at: string | null
  }>('GET', `/api/admin/session-analytics/v2/clients?${q}`)
}

type DimensionWindow = { start: string; end: string; exact?: boolean }

function sessionDimensionParams(days: number, tenantId?: string, range?: DimensionWindow, includeProbes = false) {
  const params = new URLSearchParams()
  if (!range) params.set('days', String(days))
  else if (range.exact) {
    params.set('start_time', range.start)
    params.set('end_time', range.end)
  } else {
    params.set('start_date', range.start)
    params.set('end_date', range.end)
  }
  if (tenantId) params.set('tenant_id', tenantId)
  if (includeProbes) params.set('include_probes', 'true')
  return params
}

export function getClientAnalyticsDetail(clientId: string, days = 30, tenantId?: string, range?: DimensionWindow, includeProbes = false) {
  const params = sessionDimensionParams(days, tenantId, range, includeProbes)
  return req<ClientAnalyticsDetail>('GET', `/api/admin/session-analytics/v2/clients/${encodeURIComponent(clientId)}?${params}`)
}

export function getTaskAnalyticsList(params?: {
  limit?: number
  offset?: number
  order_by?: 'cost' | 'sessions' | 'health'
  tenant_id?: string
}) {
  const q = new URLSearchParams()
  if (params?.limit) q.set('limit', String(params.limit))
  if (params?.offset) q.set('offset', String(params.offset))
  if (params?.order_by) q.set('order_by', params.order_by)
  if (params?.tenant_id) q.set('tenant_id', params.tenant_id)
  return req<{
    tasks: TaskAnalyticsSummary[]
    total: number
    limit: number
    offset: number
    refreshed_at: string | null
  }>('GET', `/api/admin/session-analytics/v2/tasks?${q}`)
}

export function getTaskAnalyticsDetail(taskId: string, days = 30, tenantId?: string, range?: DimensionWindow, includeProbes = false) {
  const params = sessionDimensionParams(days, tenantId, range, includeProbes)
  return req<TaskAnalyticsDetail>('GET', `/api/admin/session-analytics/v2/tasks/${encodeURIComponent(taskId)}?${params}`)
}

// ── User Profile / 用户画像 (2026-07-07) ──

export interface UserProfileSummary {
  owner_user: string
  tenant_id?: string
  session_count: number
  total_requests: number
  total_cost_usd: number | null
  avg_cost_per_session: number | null
  avg_health_score?: number
  total_success: number
  total_errors: number
  first_seen_at: string | null
  last_seen_at: string | null
  end_user_count: number
  models_used?: string[]
  // 用户画像列表的选定窗口用量由 usage_meta 标明数据来源。
  window_usage?: UserWindowUsage
}

// UserWindowUsage 员工在 [window_start, window_end) 的用量聚合（与后端
// admin.UserWindowUsage 同形）。数字出处随列表响应的 usage_meta 回传。
export interface UserWindowUsage {
  request_count: number
  input_tokens: number
  output_tokens: number
  total_tokens: number
  internal_credits: number
  external_cost_usd: number | null
  cost_rows_outside_usd: number
  session_count: number
  model_count: number
}

// UserUsageMeta 数字出处块：帮助抽屉与列头提示引用，不在前端硬编码口径。
export interface UserUsageMeta {
  source_view: string
  window_start: string
  window_end: string
}

// UserUsageMatrixRow 员工×模型一行（usage-matrix 端点）。
export interface UserUsageMatrixRow {
  owner_user: string
  tenant_id?: string
  model_name: string
  provider: string
  request_count: number
  input_tokens: number
  output_tokens: number
  total_tokens: number
  internal_credits: number
  external_cost_usd: number | null
  cost_rows_outside_usd: number
  first_used_at: string
  last_used_at: string
}

export interface UserProfileDetail extends UserProfileSummary {
  daily_cost_trend: Array<{ date: string; cost: number | null; sessions: number }>
  top_tasks: Array<{ task_id: string; session_count: number; total_cost: number | null; avg_health?: number }>
  top_end_users: Array<{ end_user_id: string; session_count: number; total_cost_usd: number | null; avg_health?: number; last_activity: string }>
  recent_sessions: Array<{ session_id: string; request_count: number; cost_usd: number | null; health_score?: number; health_grade?: string; created_at: string | null }>
  health_distribution: { a: number; b: number; c: number; d: number; f: number }
  pending_since?: string | null
  usd_unmeasured_requests?: number
  usage_meta?: UserUsageMeta
}

export function getUserProfileList(params?: { limit?: number; offset?: number; search?: string; date_from?: string; date_to?: string }) {
  const q = new URLSearchParams()
  if (params?.limit) q.set('limit', String(params.limit))
  if (params?.offset) q.set('offset', String(params.offset))
  if (params?.search) q.set('search', params.search)
  if (params?.date_from) q.set('date_from', params.date_from)
  if (params?.date_to) q.set('date_to', params.date_to)
  // degraded / degraded_reason（2026-10-03）：后端在「schema 落后于代码」时
  // 返回 200 + 空列表（session_owners 属可选聚合表，本地库根本没建）。
  // 修复前该响应用户看到的和「真的没有用户」一模一样 —— 页面无从区分。
  return req<{
    users: UserProfileSummary[]
    total: number
    limit: number
    offset: number
    // 2026-11-20 数字出处轮：真实用量窗口 + 出处块（缺省时后端默认近 30 天）。
    usage_window?: { start: string; end: string }
    usage_meta?: UserUsageMeta
  } & DegradationMarker>(
    'GET',
    `/api/admin/session-analytics/v2/users?${q}`,
  )
}

// 员工项目的请求数、使用量、USD 和积分来自同一请求范围。
export interface UserProjectRow {
  project_id: string
  session_count: number
  task_count: number
  request_count: number
  total_tokens: number
  total_cost_usd: number | null
  total_credits_charged: number
  last_activity_at: string
}

export type UserProfileReadScope = {
  tenant_id?: string
  start_date?: string
  end_date?: string
  start_time?: string
  end_time?: string
}

function userProfileParams(days: number, scope?: UserProfileReadScope) {
  const params = new URLSearchParams({ days: String(days) })
  for (const [name, value] of Object.entries(scope ?? {})) {
    if (value) params.set(name, value)
  }
  return params
}

// getUserProjects 员工→项目下钻（2026-11-20 逐层下钻轮 / L1 收口）。
export function getUserProjects(
  owner: string,
  days = 30,
  scope?: UserProfileReadScope,
) {
  const params = userProfileParams(days, scope)
  return req<{ projects: UserProjectRow[]; owner_user: string; tenant_id: string; usage_meta?: UserUsageMeta } & DegradationMarker>(
    'GET',
    `/api/admin/session-analytics/v2/users/${encodeURIComponent(owner)}/projects?${params}`,
  )
}

// 员工与模型用量矩阵。owner 缺省返回当前权限范围。
export function getUserUsageMatrix(params?: {
  start_date?: string
  end_date?: string
  owner?: string
  tenant_id?: string
  limit?: number
}): Promise<{
  rows: UserUsageMatrixRow[]
  start_date: string
  end_date: string
  usage_meta?: UserUsageMeta
} & DegradationMarker> {
  const q = new URLSearchParams()
  if (params?.start_date) q.set('start_date', params.start_date)
  if (params?.end_date) q.set('end_date', params.end_date)
  if (params?.owner) q.set('owner', params.owner)
  if (params?.tenant_id) q.set('tenant_id', params.tenant_id)
  if (params?.limit) q.set('limit', String(params.limit))
  return req<{
    rows: UserUsageMatrixRow[]
    start_date: string
    end_date: string
    usage_meta?: UserUsageMeta
  } & DegradationMarker>('GET', `/api/admin/session-analytics/v2/users/usage-matrix?${q}`)
}

export function getUserProfile(owner: string, days = 30, scope?: UserProfileReadScope) {
  const params = userProfileParams(days, scope)
  return req<UserProfileDetail>('GET', `/api/admin/session-analytics/v2/users/${encodeURIComponent(owner)}?${params}`)
}

// ── User Model Usage (2026-11-19) ──

export type UserModelUsage = UserUsageMatrixRow

export async function getUserModelUsage(
  owner: string,
  startDate?: string,
  endDate?: string,
  tenantId?: string,
) {
  const { rows, ...meta } = await getUserUsageMatrix({ owner, tenant_id: tenantId, start_date: startDate, end_date: endDate, limit: 200 })
  return { ...meta, models: rows }
}

// ── User Management ──

export interface UserListItem {
  id: number
  tenant_id: string
  username: string
  display_name: string
  email: string
  role: string
  enabled: boolean
  last_login_at: string | null
  created_at: string
}

export function getUsers() {
  return req<UserListItem[]>('GET', '/api/users')
}

export function createUser(data: {
  username: string
  password: string
  tenant_id?: string
  display_name?: string
  email?: string
  role?: string
}) {
  return req<UserListItem>('POST', '/api/users', data)
}

export function updateUser(id: number, data: {
  display_name?: string
  email?: string
  role?: string
  enabled?: boolean
  password?: string
}) {
  return req<UserListItem>('PUT', `/api/users/${id}`, data)
}

export function deleteUser(id: number) {
  return req<{ status: string }>('DELETE', `/api/users/${id}`)
}

export function resetUserPassword(id: number, password: string) {
  return req<{ status: string }>('PUT', `/api/users/${id}/password`, { password })
}

// AuthMeResponse carries a fresh access_token **plus the user object nested
// under `user`** — that is what the server actually sends
// (admin/users.go:683 `{"access_token":…,"expires_at":…,"user":{…}}`).
// It also carries a fresh HttpOnly session cookie.
//
// 2026-10-23 修正：本接口此前声明为 `extends UserInfo`（用户字段在**顶层**），
// 而后端从来没有这样发过。该错误类型直接诱导了 App.vue 的水合判据去读
// `raw.id` ⇒ 对真实响应恒为 undefined ⇒ 每次冷启都 clearJwt()。
// 判身份请统一走 `store.userInfoFromAuthMe()`（同时认嵌套与降级扁平两种形态），
// 不要在这里按 UserInfo 直接展开。
export interface AuthMeResponse {
  access_token?: string
  expires_at?: string
  user?: UserInfo
}

export function getAuthMe() {
  // 降级路径（JWT 签发失败，admin/users.go:678）返回**顶层** userInfo；
  // 两种形态都在联合类型里，由 store.userInfoFromAuthMe 归一。
  return req<AuthMeResponse | UserInfo>('GET', '/api/auth/me')
}

export function changeMyPassword(old_password: string, new_password: string) {
  return req<{ status: string }>('PUT', '/api/auth/change-password', { old_password, new_password })
}

// ── Audit Logs (super_admin only) ──────────────────────────────────────────

export interface AuditLogEntry {
  id: number
  ts: string
  actor: string
  action: string
  target_type?: string
  target_id?: number
  before_json?: any
  after_json?: any
}

export function getAuditLogs(params: {
  page?: number
  size?: number
  actor?: string
  action?: string
  from?: string  // RFC3339
  to?: string
} = {}) {
  const q = new URLSearchParams()
  if (params.page) q.set('page', String(params.page))
  if (params.size) q.set('size', String(params.size))
  if (params.actor) q.set('actor', params.actor)
  if (params.action) q.set('action', params.action)
  if (params.from) q.set('from', params.from)
  if (params.to) q.set('to', params.to)
  const qs = q.toString()
  return req<{ total: number; page: number; size: number; entries: AuditLogEntry[] }>(
    'GET', '/api/admin/audit-logs' + (qs ? '?' + qs : '')
  )
}

// ── Tenant Management (super_admin only) ─────────────────────────────────

export interface Tenant {
  code: string
  name: string
  status: string  // active | trial | suspended | expired | disabled
  description: string
  contact_email: string
  created_at: string
  updated_at: string
  user_count?: number
  api_key_count?: number
  requests_7d?: number
  tokens_7d?: number
  credits_7d?: number
  cost_7d_usd?: number
  total_requests?: number
}

export interface CreateTenantResponse extends Tenant {
  default_admin?: TenantUser
  initial_password?: string
}

export function getTenantsAdmin(status?: string) {
  const qs = status ? '?status=' + status : ''
  return req<Tenant[]>('GET', '/api/admin/tenants' + qs)
}

export function getTenant(code: string) {
  return req<Tenant>('GET', `/api/admin/tenants/${code}`)
}

export function createTenant(data: {
  code: string
  name: string
  status?: string
  description?: string
  contact_email?: string
}) {
  return req<CreateTenantResponse>('POST', '/api/admin/tenants', data)
}

export function updateTenant(code: string, data: {
  name?: string
  status?: string
  description?: string
  contact_email?: string
}) {
  return req<Tenant>('PATCH', `/api/admin/tenants/${code}`, data)
}

export interface TenantUser {
  id: number
  tenant_id: string
  username: string
  display_name: string
  email: string
  role: string
  enabled: boolean
  last_login_at: string | null
  created_at: string
}

export function getTenantUsers(code: string) {
  return req<TenantUser[]>('GET', `/api/admin/tenants/${code}/users`)
}

export interface TenantKey {
  id: number
  tenant_id: string
  key_prefix: string
  key_alias: string
  owner_user: string
  enabled: boolean
  status: string
  application_id: number
  application_code: string
  total_requests: number
  total_cost_usd: number
  expires_at: string | null
  created_at: string
}

export function getTenantKeys(code: string) {
  return req<TenantKey[]>('GET', `/api/admin/tenants/${code}/keys`)
}

export interface TenantDailyStat {
  date: string // YYYY-MM-DD
  requests: number
  success: number
  errors: number
  tokens: number
  credits: number
  cost_usd: number
}

export interface TenantStats {
  days: number
  total_requests: number
  total_tokens: number
  total_credits: number
  total_cost_usd: number
  unique_keys: number
  unique_models: number
  unique_apps: number
  input_tokens: number
  output_tokens: number
  cache_read_tokens: number
  cache_write_tokens: number
  avg_latency_ms: number
  cost_rows_outside_usd?: number
  updated_at?: string | null
  window_end?: string
  by_model: Array<{ model: string; requests: number; tokens: number; credits: number; cost_usd: number }>
  by_application: Array<{ application_code: string; requests: number; tokens: number; credits: number; cost_usd: number }>
  // 2026-09-30 统计 UI 优化轮：按天时序（无流量日已由后端 generate_series 补零）。
  daily: TenantDailyStat[]
}

export function getTenantStats(code: string, days?: number) {
  const qs = days ? '?days=' + days : ''
  return req<TenantStats>('GET', `/api/admin/tenants/v2/${encodeURIComponent(code)}/stats` + qs)
}

// ── 2026-09-30 统计 UI 优化轮：/users 用户级用量统计 ────────────────
// 读面 usage_facts × api_keys.owner_user（与对账快照同源），详见
// admin/user_usage_stats.go 头注。

export interface UserUsageSummaryItem {
  username: string
  tenant_id: string
  requests: number
  tokens: number
  credits: number
  last_active_at: string | null
}

export interface UserUsageSummary {
  days: number
  items: UserUsageSummaryItem[]
  degraded?: boolean
}

export function getUserUsageSummary(days = 30) {
  return req<UserUsageSummary>('GET', `/api/admin/users/v2/usage-summary?days=${days}`)
}

export interface UserStatsKpi {
  requests: number
  tokens: number
  credits: number
  errors: number
  error_rate: number
  latency_p95_ms: number | null
}

export interface UserStatsBucket {
  id?: number // API Key ID (仅 top_keys 有值，供跳转)
  name: string
  requests: number
  tokens: number
  credits: number
  cost_usd: number
}

export interface UserStatsRecentRequest {
  ts: string
  model: string
  first_chunk_ms: number | null
  total_ms: number | null
  credits: number
  status: string
}

export interface UserStats {
  user_id: number
  username: string
  days: number
  kpi: UserStatsKpi | null
  daily: TenantDailyStat[]
  top_models: UserStatsBucket[]
  top_apps: UserStatsBucket[]
  top_keys: UserStatsBucket[]
  key_count: number
  recent_requests: UserStatsRecentRequest[]
  degraded?: boolean
}

export function getUserStats(userId: number, days = 30) {
  return req<UserStats>('GET', `/api/admin/users/v2/${userId}/stats?days=${days}`)
}

export const TENANT_STATUSES = ['active', 'trial', 'suspended', 'expired', 'disabled'] as const
export const TENANT_STATUS_LABELS: Record<string, string> = {
  active: '正常',
  trial: '试用',
  suspended: '暂停',
  expired: '过期',
  disabled: '已禁用',
}
export const TENANT_STATUS_COLORS: Record<string, string> = {
  active: 'badge-green',
  trial: 'badge-blue',
  suspended: 'badge-yellow',
  expired: 'badge-gray',
  disabled: 'badge-red',
}
