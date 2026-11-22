// api-work-types.ts — Phase 1 work type admin API bindings

import { store } from './store'

const BASE = ''

async function req<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = {}
  if (store.apiKey) headers.Authorization = `Bearer ${store.apiKey}`
  if (body !== undefined) headers['Content-Type'] = 'application/json'
  const resp = await fetch(BASE + path, {
    method,
    headers,
    credentials: 'same-origin',
    body: body !== undefined ? JSON.stringify(body) : undefined,
  })
  if (!resp.ok) {
    const text = await resp.text().catch(() => '')
    throw new Error(`${resp.status} ${resp.statusText}: ${text}`)
  }
  return resp.json() as Promise<T>
}

// work_type_model_route.tier is a three-layer priority ordering. Every tier
// can contain multiple canonical models; weight orders models within a tier.
export type ModelRouteTier = 'primary' | 'secondary' | 'fallback'

export interface ModelRoute {
  id?: number
  canonical_name: string
  weight: number
  min_score: number
  enabled: boolean
  tier: ModelRouteTier
  task_quality_score: number
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

/**
 * Default values used when the backend hands us rows that pre-date the
 * tier column (e.g. before 046_task_route_tiers.sql). Newer rows always
 * carry an explicit tier.
 */
export function normalizeRouteTier(rt: Partial<ModelRoute>): ModelRouteTier {
  const t = rt.tier
  if (t === 'primary' || t === 'secondary' || t === 'fallback') return t
  return 'secondary'
}

/**
 * Bucket model routes by tier for the three-layer UI. Routes that have
 * an empty canonical_name are dropped (frontend can have a half-filled
 * draft row, but it should never be persisted). Unknown tiers fall into
 * the `secondary` bucket so legacy data still renders.
 */
export function groupRoutesByLayer(
  routes: ModelRoute[] | undefined | null,
): Record<ModelRouteTier, ModelRoute[]> {
  const groups: Record<ModelRouteTier, ModelRoute[]> = {
    primary: [],
    secondary: [],
    fallback: [],
  }
  for (const rt of routes ?? []) {
    if (!rt.canonical_name || !rt.canonical_name.trim()) continue
    const tier = normalizeRouteTier(rt)
    groups[tier].push({
      ...rt,
      tier,
      task_quality_score: typeof rt.task_quality_score === 'number' ? rt.task_quality_score : 0,
      weight: typeof rt.weight === 'number' ? rt.weight : 1,
    })
  }
  // Within a tier, sort by weight DESC so the backend's ORDER BY weight DESC
  // matches what the user sees on screen after Save.
  for (const tier of Object.keys(groups) as ModelRouteTier[]) {
    groups[tier].sort((a, b) => b.weight - a.weight)
  }
  return groups
}

export interface WorkTypeStatEntry {
  key: string
  label: string
  category: string
  l1_task_type: string
  /** 窗口内请求数（2026-11-19 起随 ?days= 窗口变化；count_24h 为兼容别名） */
  count_24h: number
  count_window?: number
  count_direct: number
  count_l1_proxy: number
}

export interface WorkTypeStats {
  /** 统计窗口标签（"24h" / "7d" / "30d"...；缺省 24h，2026-11-19 参数化） */
  window?: string
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

// 2026-07-20: L1 task types are now DB-backed.
//
//   `L1_TASK_TYPES` below is a frontend seed (canonical 8-task-type taxonomy)
//   used by useL1TaskTypes() composable as immediate paint before the backend
//   response lands. The live list comes from `listL1TaskTypes()` which queries
//   /api/admin/work-types/l1-task-types (returns canonical 8 ∪ distinct
//   l1_task_type from work_type_config).
//
//   Adding/removing entries here is a system-wide schema change — the LLM
//   classifier must learn the new categories too. Keep in sync with
//   admin/work_types.go:canonicalL1TaskTypes.

export interface L1TaskTypeMeta {
  key: string
  label: string
  icon: string
  /** Number of work_type_config rows currently using this L1 (live count). */
  count: number
}

export const L1_TASK_TYPES: Omit<L1TaskTypeMeta, 'count'>[] = [
  { key: 'chat',          label: '通用对话',  icon: '💬' },
  { key: 'reasoning',     label: '逻辑推理',  icon: '🧠' },
  { key: 'code',          label: '代码',      icon: '💻' },
  { key: 'agent',         label: 'Agent',    icon: '🤖' },
  { key: 'creative',      label: '创意',      icon: '✍️' },
  { key: 'long_context',  label: '长文档',    icon: '📚' },
  { key: 'vision',        label: '视觉',      icon: '👁️' },
  { key: 'function_call', label: '函数调用',  icon: '🔧' },
]

export interface L1TaskTypesResponse {
  items: L1TaskTypeMeta[]
}

/**
 * Fetch L1 task types from the backend.
 * Returns the union of canonical 8 + any operator-added categories from
 * work_type_config. Each item carries a live `count` of how many work
 * types currently use that L1.
 */
export function listL1TaskTypes(): Promise<L1TaskTypesResponse> {
  return req<L1TaskTypesResponse>('GET', '/api/admin/work-types/l1-task-types')
}

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

/**
 * Get work type statistics.
 *
 * `days` 统计窗口（1..90 天），缺省后端回落 24h。
 * 2026-11-19 时间范围统一轮：与全站 KxDateRangePicker 的 span 口径对齐。
 *
 * ★ 2026-11-23 批判式审计轮：同时支持**真实区间** `start`/`end`
 *   （YYYY-MM-DD，系统时区）——此前用户选的自定义区间被压成「跨度天数」，
 *   后端把 days 解释为「相对今天的滚动窗口」⇒ 界面显示的区间与数据窗口
 *   不一致（静默错误答案）。后端：admin/work_types.go parseStatsTimeRange。
 */
export function getWorkTypeStats(
  params: number | { days?: number; start?: string; end?: string } = {},
): Promise<WorkTypeStats> {
  const p = typeof params === 'number' ? { days: params } : params
  const qs = new URLSearchParams()
  if (p.start && p.end) {
    qs.set('start', p.start)
    qs.set('end', p.end)
  } else if (p.days && p.days > 0) {
    qs.set('days', String(Math.round(p.days)))
  }
  const q = qs.toString()
  return req<WorkTypeStats>('GET', `/api/admin/work-types/stats${q ? `?${q}` : ''}`)
}

export function syncWorkTypesFromACC(): Promise<WorkTypeSyncResponse> {
  return req('POST', '/api/admin/work-types/sync-from-acc')
}
