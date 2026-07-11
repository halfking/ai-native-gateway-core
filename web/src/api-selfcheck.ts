// api-selfcheck.ts — 系统自检模块 API 客户端
// Backend: admin/self_check_handlers.go
// 对应数据库表: self_check_runs, self_check_round_results, self_check_settings

import { authBearer } from './store'

const BASE = ''

async function req<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = { 'Authorization': `Bearer ${authBearer()}` }
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

export interface SelfCheckSettings {
  enabled: boolean
  normal_interval_seconds: number
  fault_interval_seconds: number
  model_source: 'top10' | 'featured' | 'both'
  max_models: number
  max_tokens_per_run: number
  featured_model_ids: string[]
  updated_at: string
  updated_by?: string | null
}

export interface SelfCheckRun {
  id: number
  model_name: string
  started_at: string
  completed_at?: string | null
  duration_ms: number
  status: 'running' | 'success' | 'partial' | 'failed'
  rounds_total: number
  rounds_success: number
  had_tool_call: boolean
  total_tokens: number
  avg_latency_ms: number
  error_type?: string
  error_detail?: string
  upstream_tested: boolean
  upstream_result?: string
  upstream_latency_ms?: number
  upstream_error?: string
}

export interface SelfCheckRoundResult {
  id: number
  round_index: number
  is_ping: boolean
  is_tool_call: boolean
  latency_ms: number
  prompt_tokens: number
  completion_tokens: number
  total_tokens: number
  success: boolean
  http_code: number
  error_message: string
  request_body: string
  response_preview: string
  created_at: string
}

export interface SelfCheckRunDetail {
  run: SelfCheckRun
  rounds: SelfCheckRoundResult[]
}

export interface SelfCheckStatsSummary {
  total_runs: number
  success_runs: number
  partial_runs: number
  failed_runs: number
  success_rate: number
}

export interface SelfCheckModelStat {
  model_name: string
  total: number
  success: number
  partial: number
  failed: number
  success_rate: number
  avg_latency_ms: number
}

export interface SelfCheckErrorStat {
  error_type: string
  count: number
}

export interface SelfCheckTrendPoint {
  timestamp: string
  success_rate: number
  total: number
}

export interface SelfCheckStats {
  range: string
  summary: SelfCheckStatsSummary
  by_model: SelfCheckModelStat[]
  error_breakdown: SelfCheckErrorStat[]
  trend: SelfCheckTrendPoint[]
}

export interface SelfCheckModelInfo {
  model_name: string
  total: number
  success: number
  failed: number
  last_run?: string
}

// ── API functions ─────────────────────────────────────

export async function fetchSelfCheckSettings(): Promise<SelfCheckSettings> {
  return req<SelfCheckSettings>('GET', '/api/self-check/settings')
}

export async function updateSelfCheckSettings(
  updates: Partial<SelfCheckSettings>
): Promise<{ ok: boolean }> {
  return req<{ ok: boolean }>('PUT', '/api/self-check/settings/update', updates)
}

export async function fetchSelfCheckRuns(params: {
  limit?: number
  model?: string
  status?: string
} = {}): Promise<{ items: SelfCheckRun[]; total: number }> {
  const query = new URLSearchParams()
  if (params.limit !== undefined) query.set('limit', String(params.limit))
  if (params.model) query.set('model', params.model)
  if (params.status) query.set('status', params.status)
  const qs = query.toString()
  return req<{ items: SelfCheckRun[]; total: number }>(
    'GET',
    `/api/self-check/runs${qs ? '?' + qs : ''}`
  )
}

export async function fetchSelfCheckRunDetail(
  id: number
): Promise<SelfCheckRunDetail> {
  return req<SelfCheckRunDetail>('GET', `/api/self-check/runs/${id}`)
}

export async function fetchSelfCheckStats(range = '24h'): Promise<SelfCheckStats> {
  return req<SelfCheckStats>('GET', `/api/self-check/stats?range=${range}`)
}

export async function fetchSelfCheckModels(): Promise<{ models: SelfCheckModelInfo[] }> {
  return req<{ models: SelfCheckModelInfo[] }>('GET', '/api/self-check/models')
}

export async function triggerSelfCheck(model = ''): Promise<{ ok: boolean; message: string }> {
  return req<{ ok: boolean; message: string }>('POST', '/api/self-check/trigger', { model })
}