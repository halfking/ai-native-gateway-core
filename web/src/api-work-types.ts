// api-work-types.ts — Phase 1 work type admin API bindings

import { store, authBearer } from './store'

const BASE = ''

async function req<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = { Authorization: `Bearer ${authBearer()}` }
  if (body !== undefined) headers['Content-Type'] = 'application/json'
  const resp = await fetch(BASE + path, {
    method,
    headers,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  })
  if (!resp.ok) {
    const text = await resp.text().catch(() => '')
    throw new Error(`${resp.status} ${resp.statusText}: ${text}`)
  }
  return resp.json() as Promise<T>
}

export interface ModelRoute {
  id?: number
  canonical_name: string
  weight: number
  min_score: number
  enabled: boolean
}

export interface WorkTypeConfig {
  key: string
  label: string
  category: string
  l1_task_type: string
  default_profile: 'smart' | 'speed_first' | 'cost_first'
  tags: string[]
  prompt_keywords: string[]
  acc_task_type?: string | null
  enabled: boolean
  sort_order: number
  synced_from_acc_at?: string | null
  updated_at: string
  model_routes?: ModelRoute[]
}

export interface WorkTypeStatEntry {
  key: string
  label: string
  category: string
  l1_task_type: string
  count_24h: number
  count_direct: number
  count_l1_proxy: number
}

export interface WorkTypeStats {
  window_hours: number
  total_auto: number
  by_work_type: Record<string, WorkTypeStatEntry>
  by_l1_task: Record<string, number>
  top_models: Array<{ model: string; count: number }>
  sync_meta?: WorkTypeSyncMeta
}

export interface WorkTypeSyncMeta {
  source: string
  last_synced_at?: string | null
  enabled_count: number
  route_count: number
  acc_configured: boolean
}

export interface WorkTypeSyncResponse {
  synced: boolean
  message: string
  source?: string
  synced_at?: string
  upserted?: number
  routes?: number
  disabled?: number
  acc_count?: number
  sync_meta?: WorkTypeSyncMeta
}

export const L1_TASK_TYPES = [
  { key: 'chat', label: '通用对话' },
  { key: 'reasoning', label: '逻辑推理' },
  { key: 'code', label: '代码' },
  { key: 'agent', label: 'Agent' },
  { key: 'creative', label: '创意' },
  { key: 'long_context', label: '长文档' },
  { key: 'vision', label: '视觉' },
  { key: 'function_call', label: '函数调用' },
]

export const PROFILES = [
  { key: 'smart', label: '智能' },
  { key: 'speed_first', label: '速度' },
  { key: 'cost_first', label: '成本' },
]

export const CATEGORIES = ['通用', '研发', '营销', '采集', '多媒体', '企业']

export function listWorkTypes(includeDisabled = false): Promise<WorkTypeConfig[]> {
  const q = includeDisabled ? '?include_disabled=true' : ''
  return req<WorkTypeConfig[]>('GET', `/api/admin/work-types${q}`)
}

export function getWorkType(key: string): Promise<WorkTypeConfig> {
  return req<WorkTypeConfig>('GET', `/api/admin/work-types/${encodeURIComponent(key)}`)
}

export function createWorkType(body: Partial<WorkTypeConfig> & { key: string; label: string; category: string; l1_task_type: string }): Promise<WorkTypeConfig> {
  return req<WorkTypeConfig>('POST', '/api/admin/work-types', body)
}

export function updateWorkType(key: string, body: Partial<WorkTypeConfig>): Promise<WorkTypeConfig> {
  return req<WorkTypeConfig>('PATCH', `/api/admin/work-types/${encodeURIComponent(key)}`, body)
}

export function deleteWorkType(key: string): Promise<{ key: string; enabled: boolean; deleted: boolean }> {
  return req('DELETE', `/api/admin/work-types/${encodeURIComponent(key)}`)
}

export function putWorkTypeRoutes(key: string, routes: ModelRoute[]): Promise<{ key: string; model_routes: ModelRoute[] }> {
  return req('PUT', `/api/admin/work-types/${encodeURIComponent(key)}/routes`, routes)
}

export function getWorkTypeStats(): Promise<WorkTypeStats> {
  return req<WorkTypeStats>('GET', '/api/admin/work-types/stats')
}

export function syncWorkTypesFromACC(): Promise<WorkTypeSyncResponse> {
  return req('POST', '/api/admin/work-types/sync-from-acc')
}
