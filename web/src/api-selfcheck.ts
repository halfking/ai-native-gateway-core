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
    const err = new Error(`${resp.status} ${resp.statusText}: ${text}`) as Error & { status?: number; body?: string }
    err.status = resp.status
    err.body = text
    throw err
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
  credential_id?: number | null
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
  selection_strategy?: 'featured' | 'most_used' | 'random' | string | null
  attempted_models?: string[] | null
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

// Trigger availability — tells the UI whether POST /api/self-check/trigger can
// accept runs right now. Under the new probe mode (default since 2026-07-14)
// the legacy featured-model worker is not instantiated, so available=false
// and the UI must disable the "触发测试" / "手动触发" buttons instead of
// firing a 410/503.
export interface SelfCheckTriggerAvailability {
  available: boolean
  new_probe_mode: boolean
  reason?: string
  error_code?: string
}

export async function fetchSelfCheckTriggerAvailability(): Promise<SelfCheckTriggerAvailability> {
  return req<SelfCheckTriggerAvailability>('GET', '/api/self-check/trigger/availability')
}

// ── New probe stack (2026-07-23) ───────────────────────────────────────
// 在新探测模式（默认）下，旧 SelfCheckWorker 关闭，self_check_runs 为空。
// 自检统计改读 /api/admin/probe/* 接口（node/model_probe_runs，含延时数据）。

export interface ProbeSystemHealth {
  total_nodes: number
  healthy_nodes: number
  failing_nodes: number
  suspicious_nodes: number
  probing_nodes: number
  [k: string]: unknown
}

export async function fetchProbeSystemHealth(): Promise<ProbeSystemHealth> {
  return req<ProbeSystemHealth>('GET', '/api/admin/probe/system-health')
}

// 逐条队列任务（区别于聚合视图 v_probe_queue_snapshot），用于泳道展示
export interface ProbeQueueTaskRow {
  id: number
  credential_id: number
  provider_id: number
  provider_name: string
  raw_model: string
  /** 标准模型名（优先于 raw_model 用于展示/维度） */
  standardized_name?: string
  status: string
  attempt: number
  priority: number
  reason_code: string
  /** 探测命令；integrity_verify 来自 integrity_probe_planner */
  probe_command?: string
  source?: string
  next_run_at?: string | null
  result_latency_ms: number
  result_http_status: number
  updated_at?: string | null
}

export async function fetchProbeQueueTasks(limit = 100): Promise<{ tasks: ProbeQueueTaskRow[]; total: number }> {
  return req<{ tasks: ProbeQueueTaskRow[]; total: number }>('GET', `/api/admin/probe/queue-tasks?limit=${limit}`)
}

// 错误触发的节点自检队列（区别于 credential_probe_queue 的完整性探测）。
// NodeProbeWorker 的 7 步退避（5s→30s→60s→5m→1h→2h→6h）写在
// node_probe_state / node_probe_runs，之前自检泳道看不到这批任务。
// 2026-08-10 (fix/selfcheck-queue-and-recovery)
export interface NodeProbeTaskRow {
  credential_id: number
  provider_id: number
  provider_name: string
  provider_code: string
  raw_model: string
  standardized_name?: string
  status: string
  attempt: number
  consecutive_failures: number
  next_retry_at?: string | null
  last_direct_ok?: boolean | null
  last_gateway_ok?: boolean | null
  last_err_code?: string | null
  last_latency_ms?: number | null
  paused: boolean
  updated_at?: string | null
  source: 'node_probe'
}

export async function fetchProbeNodeTasks(limit = 120): Promise<{ tasks: NodeProbeTaskRow[]; total: number }> {
  return req<{ tasks: NodeProbeTaskRow[]; total: number }>('GET', `/api/admin/probe/node-tasks?limit=${limit}`)
}
// ── Tri-state probe queue (OBS-BE5, 25 号 §6.2 / 26 号 §4) ─────────────
// GET /api/admin/probe/tasks?status=pending|in_flight|completed&limit=
// One leg of the three-section self-check queue view. Field semantics follow
// the backend contract exactly: optional fields are absent when unimplemented
// (e.g. next_retry_at_ms only rides on pending re-arm rows) — the UI must not
// zero-fake them (13 号门禁).

export type ProbeTriStateStatus = 'pending' | 'in_flight' | 'completed'
export type ProbeOutcome = 'success' | 'failed' | 'expired' | 'cancelled'

export interface ProbeTriStateTask {
  id: number
  dedup_key: string
  credential_id: number
  provider_id?: number
  raw_model: string
  command: string
  source: string
  origin: 'scheduled' | 'error' | 'manual'
  status: ProbeTriStateStatus
  outcome?: ProbeOutcome
  attempt: number
  max_attempts: number
  priority: number
  /** 退避下一跳（unix ms）— 仅 pending 重臂行携带 */
  next_retry_at_ms?: number
  reason_code?: string
  http_status?: number
  latency_ms?: number
  created_at: string
  updated_at: string
  finished_at?: string
}

export interface ProbeTriStateLeg {
  status: ProbeTriStateStatus
  tasks: ProbeTriStateTask[]
  count: number
}

export async function fetchProbeTriStateTasks(
  status: ProbeTriStateStatus,
  limit = 50
): Promise<ProbeTriStateLeg> {
  return req<ProbeTriStateLeg>('GET', `/api/admin/probe/tasks?status=${status}&limit=${limit}`)
}
