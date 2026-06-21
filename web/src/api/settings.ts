import { req } from './_core'

// settings.ts — v6.0 audit T12 (2026-06-22)
// Platform-wide Settings (settings_kv table) and tenant-level overrides
// (tenant_settings_kv). Settings are typed (enum / int / float / bool /
// string / url / duration) and the spec carries a danger_level so the
// admin UI can warn before applying a "level 3" change.
//
// All writes go through audit (settings_audit); the history endpoint
// surfaces the audit log per key.

export type SettingType = 'enum' | 'int' | 'float' | 'bool' | 'string' | 'url' | 'duration'
export type SettingScope = 'platform' | 'tenant'

export interface SettingSpec {
  key: string
  env_name: string
  type: SettingType
  scope: SettingScope
  category: string
  default: any
  options?: string[]
  min?: number
  max?: number
  description: string
  danger_level: 0 | 1 | 2 | 3
  hot_reload: boolean
  observability?: string
}

export interface SettingItem extends SettingSpec {
  value: any
  source: 'db' | 'env' | 'default' | ''
}

export interface SettingAuditEntry {
  id: number
  setting_key: string
  tenant_id?: string
  action: 'update' | 'rollback' | 'delete'
  old_value?: any
  new_value?: any
  operator_user: string
  operator_role: string
  client_ip?: string
  created_at: string
}

export function listSettings(params: { category?: string } = {}) {
  const qs = new URLSearchParams()
  if (params.category) qs.set('category', params.category)
  const s = qs.toString()
  return req<{ items: SettingItem[] }>('GET', `/api/admin/settings${s ? '?' + s : ''}`)
}

export function getSetting(key: string) {
  return req<{ spec: SettingSpec; value: any; source: string }>('GET', `/api/admin/settings/${key}`)
}

export function updateSetting(key: string, body: { value: any }) {
  return req<{ status: string; old_value?: any; new_value: any; applied_at: string }>(
    'PUT', `/api/admin/settings/${key}`, body)
}

export function rollbackSetting(key: string) {
  return req<{ status: string; rolled_back_to: any }>('POST', `/api/admin/settings/${key}/rollback`)
}

export function getSettingHistory(key: string) {
  return req<{ items: SettingAuditEntry[] }>('GET', `/api/admin/settings/${key}/history`)
}

export function updateTenantSetting(tenantID: string, key: string, body: { value: any }) {
  return req<{ status: string; new_value: any }>(
    'PUT', `/api/admin/tenant-settings/${encodeURIComponent(tenantID)}/${key}`, body)
}