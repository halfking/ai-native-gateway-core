import { req } from './_core'
import type { UserInfo } from './_core'

// admin.ts — v6.0 audit T12 (2026-06-22)
// Super-admin surfaces: user CRUD, audit log, tenant management +
// tenant-scoped users/keys/stats. All routes under /api/admin/...
// require the super_admin role (distinct from the admin role on
// /api/admin/... resources below). The "tenant" surface also includes
// the static status enums / label maps / color maps used by the UI.

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

export function getAuthMe() {
  return req<UserInfo>('GET', '/api/auth/me')
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

export interface TenantStats {
  days: number
  total_requests: number
  total_tokens: number
  total_credits: number
  total_cost_usd: number
  unique_keys: number
  unique_models: number
  unique_apps: number
  by_model: Array<{ model: string; requests: number; tokens: number; credits: number; cost_usd: number }>
  by_application: Array<{ application_code: string; requests: number; tokens: number; credits: number; cost_usd: number }>
}

export function getTenantStats(code: string, days?: number) {
  const qs = days ? '?days=' + days : ''
  return req<TenantStats>('GET', `/api/admin/tenants/${code}/stats` + qs)
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