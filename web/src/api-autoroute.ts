// api-autoroute.ts — v2.0 auto-route admin API bindings.
// Backend endpoints: admin/auto_route.go RegisterAutoRouteRoutes

import { store } from './store'

const BASE = ''

async function req<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = { 'Authorization': `Bearer ${store.apiKey}` }
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

// ── Types ─────────────────────────────────────────────

export interface AutoRouteIndexEntry {
  credential_id: number
  raw_model: string
  canonical_id?: number
  canonical_name?: string
  billing_mode?: string
  unit_price_in_per_1m?: number
  unit_price_out_per_1m?: number
  context_window?: number
  success_rate?: number
  p95_latency_ms?: number
  active_sessions?: number
  concurrency_limit?: number
  pressure_ratio?: number
  score_smart?: number
  score_speed_first?: number
  score_cost_first?: number
  updated_at?: string
  bucket?: string
}

export interface AutoRouteDecision {
  ts: string
  request_id: string
  task_type?: string
  auto_profile?: string
  auto_confidence?: number
  client_model?: string
  outbound_model?: string
  credential_id?: number
  api_key_id?: number
  success: boolean
  latency_ms?: number
  auto_decision?: {
    task_type: string
    confidence: number
    classifier: string
    profile: string
    chosen_model: string
    chosen_raw_model?: string
    chosen_credential_id: number
    candidates_top3: Array<{
      model: string
      composite_score: number
      price_score?: number
      speed_score?: number
      stability_score?: number
      match_score?: number
      pressure_score?: number
      context_fit?: number
    }>
  }
}

export interface AutoRouteAudit {
  total_auto_requests: number
  success_rate: number
  task_distribution: Record<string, number>
  profile_distribution: Record<string, number>
  top_chosen_models: Array<{ model: string; count: number }>
}

export interface CustomerCostRow {
  api_key_id: number
  key_alias?: string
  cost_usd_1h?: number
  cost_usd_24h?: number
  cost_usd_7d?: number
  total_auto_requests?: number
  total_auto_success?: number
  active_concurrent?: number
  avg_pressure_1h?: number
  best_score_smart?: number
  best_score_speed_first?: number
  best_score_cost_first?: number
  last_request_at?: string
}

export interface ModelCostRow {
  raw_model: string
  canonical_id?: number
  total_cost_usd?: number
  total_tokens?: number
  avg_cost_per_1m_usd?: number
  success_rate?: number
  avg_latency_ms?: number
  total_requests?: number
  unique_api_keys?: number
}

export interface ProfileWeights {
  Price: number
  Speed: number
  Stability: number
  Match: number
  Pressure: number
  ContextFit: number
}

export const DEFAULT_PROFILE_WEIGHTS: Record<string, ProfileWeights> = {
  smart:       { Price: 25, Speed: 25, Stability: 20, Match: 25, Pressure: 10, ContextFit: 15 },
  speed_first: { Price: 10, Speed: 50, Stability: 20, Match: 15, Pressure: 5,  ContextFit: 10 },
  cost_first:  { Price: 50, Speed: 10, Stability: 15, Match: 20, Pressure: 5,  ContextFit: 10 },
}

export const TASK_TYPES = [
  { key: 'chat',          label: '通用对话',  icon: '💬' },
  { key: 'reasoning',     label: '逻辑推理',  icon: '🧠' },
  { key: 'code',          label: '代码生成',  icon: '💻' },
  { key: 'agent',         label: 'Agent',    icon: '🤖' },
  { key: 'creative',      label: '创意写作',  icon: '✍️' },
  { key: 'long_context',  label: '长文档',    icon: '📚' },
  { key: 'vision',        label: '图像理解',  icon: '👁️' },
  { key: 'function_call', label: '函数调用',  icon: '🔧' },
]

export const TASK_TAGS: Record<string, string[]> = {
  reasoning:     ['reasoning', 'math', 'logic'],
  code:          ['code', 'programming'],
  agent:         ['agent', 'tool_use', 'function_call'],
  creative:      ['creative', 'writing'],
  long_context:  ['long_context', '128k', '200k', '512k', '1m'],
  vision:        ['vision', 'multimodal'],
  function_call: ['function_call', 'tool_use'],
  chat:          [],
}

// ── API functions ─────────────────────────────────────

export function getAutoRouteIndex(top = 20): Promise<AutoRouteIndexEntry[]> {
  return req<AutoRouteIndexEntry[]>('GET', `/api/admin/auto-route/index?top=${top}`)
}

export function getAutoRouteDecisions(limit = 20, task?: string, profile?: string): Promise<AutoRouteDecision[]> {
  let q = `/api/admin/auto-route/decisions?limit=${limit}`
  if (task) q += `&task=${encodeURIComponent(task)}`
  if (profile) q += `&profile=${encodeURIComponent(profile)}`
  return req<AutoRouteDecision[]>('GET', q)
}

export function getAutoRouteAudit(): Promise<AutoRouteAudit> {
  return req<AutoRouteAudit>('GET', '/api/admin/auto-route/audit')
}

export function getCustomerCost(top = 10): Promise<CustomerCostRow[]> {
  return req<CustomerCostRow[]>('GET', `/api/admin/auto-route/cost/customer?top=${top}`)
}

export function getModelCost(top = 10): Promise<ModelCostRow[]> {
  return req<ModelCostRow[]>('GET', `/api/admin/auto-route/cost/model?top=${top}`)
}

export function refreshAutoRouteIndex(): Promise<{ refreshed: boolean; refreshed_at: string }> {
  return req('POST', '/api/admin/auto-route/refresh')
}

// ── Analytics (Phase 2a) ──────────────────────────────

export type AnalyticsMetric = 'count' | 'success_rate' | 'p95_ms' | 'cost_usd'
export type AnalyticsWindow = '24h' | '7d'

export interface AnalyticsMatrix {
  rows: string[]
  cols: string[]
  cells: number[][]
  meta: { window: AnalyticsWindow; metric: AnalyticsMetric; row?: string }
}

export interface AnalyticsFlowNode {
  id: string
  label: string
  layer: number
}

export interface AnalyticsFlowLink {
  source: string
  target: string
  value: number
}

export interface AnalyticsFlow {
  nodes: AnalyticsFlowNode[]
  links: AnalyticsFlowLink[]
  meta?: { window: AnalyticsWindow }
}

export interface ModelTaskIndexItem {
  canonical_id?: number
  canonical_name?: string
  task_type: string
  sample_count?: number
  success_rate?: number
  avg_latency_ms?: number
  p95_latency_ms?: number
  avg_cost_per_1k_usd?: number
  primary_credential_id?: number
  updated_at?: string
}

export interface ModelTaskIndexResponse {
  bucket: string | null
  items: ModelTaskIndexItem[]
  warning?: string
}

export function getAnalyticsMatrix(
  window: AnalyticsWindow = '7d',
  metric: AnalyticsMetric = 'count',
): Promise<AnalyticsMatrix> {
  const q = new URLSearchParams({ window, metric, row: 'task_type' })
  return req<AnalyticsMatrix>('GET', `/api/admin/auto-route/analytics/matrix?${q}`)
}

export function getAnalyticsFlow(window: AnalyticsWindow = '7d'): Promise<AnalyticsFlow> {
  return req<AnalyticsFlow>('GET', `/api/admin/auto-route/analytics/flow?window=${window}`)
}

export function getModelTaskIndex(task_type?: string, top = 20): Promise<ModelTaskIndexResponse> {
  let q = `/api/admin/auto-route/analytics/model-task-index?top=${top}`
  if (task_type) q += `&task_type=${encodeURIComponent(task_type)}`
  return req<ModelTaskIndexResponse>('GET', q)
}

export async function simulateAutoRoute(prompt: string, profile: string, hint?: string): Promise<{
  status: number
  decision?: Record<string, unknown>
  body?: string
  error?: string
}> {
  try {
    const headers: Record<string, string> = {
      'Authorization': `Bearer ${store.apiKey}`,
      'Content-Type': 'application/json',
      'X-Gw-Auto-Profile': profile,
    }
    if (hint) headers['X-Gw-Task-Hint'] = hint
    const resp = await fetch('/v1/chat/completions', {
      method: 'POST',
      headers,
      body: JSON.stringify({
        model: 'auto',
        messages: [{ role: 'user', content: prompt }],
        max_tokens: 10,
        stream: false,
      }),
    })
    const decisionHeader = resp.headers.get('X-Gw-Auto-Decision') || ''
    let decision: Record<string, unknown> | undefined
    if (decisionHeader) {
      try { decision = JSON.parse(decisionHeader) } catch { /* ignore */ }
    }
    const body = await resp.text()
    return { status: resp.status, decision, body }
  } catch (e) {
    return { status: 0, error: String(e) }
  }
}