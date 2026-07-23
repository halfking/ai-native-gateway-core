// web/src/api/api-system-monitor.ts — 系统监测模块 API 客户端
//
// 设计依据: docs/会话优化v2/32-系统监测模块设计.md §6
// 端点全部从 admin/systemmonitor_handlers.go 接入。
import { req } from './_core'
import { authBearer } from '../store'

// ── 类型定义 ─────────────────────────────────────────────────

export type TaskType =
  | 'direct_ping'
  | 'gateway_ping'
  | 'chat_minimal'
  | 'chat_tool'
  | 'chat_stream'
  | 'http_ping'

export type Automaticity = 'mandatory' | 'automatic'

export type TaskStatus =
  | 'ready'
  | 'running'
  | 'success'
  | 'failed'
  | 'timeout'
  | 'network_error'
  | 'expired'
  | 'skipped'

export interface SystemMonitorStats {
  queue_size: number
  running_size: number
  in_fallback: boolean
  monitor_concurrency: number
  snapshot_at: string
  completed_total_1h: number
  failed_total_1h: number
  skipped_total_1h: number
  total_tokens_1h: number
}

export interface SystemMonitorRun {
  id: number
  task_id: number
  task_type: TaskType | string
  automaticity: Automaticity | string
  credential_id: number
  raw_model: string
  source: string
  worker_id: string
  status: TaskStatus | string
  attempt: number
  http_status: number | null
  latency_ms: number | null
  err_code: string
  skip_reason: string
  started_at: string
  finished_at: string
  recent_request_id: string
}

export interface SystemMonitorSubmitRequest {
  credential_id: number
  provider_id?: number
  raw_model: string
  task_type?: TaskType
  automaticity?: Automaticity
  max_attempts?: number
}

export interface SystemMonitorBulkResponse {
  triggered: number
  failed: number
  total: number
  paired?: boolean
  credential_id?: number
  provider_id?: number
  model?: string
}

// ── 端点 ────────────────────────────────────────────────────

// POST /api/admin/system-monitor/submit
export function submitSystemMonitorTask(req_body: SystemMonitorSubmitRequest) {
  return req<{ task_id: number; task_type: string; automaticity: string; submitted_at: string }>(
    'POST', '/api/admin/system-monitor/submit', req_body
  )
}

// POST /api/admin/system-monitor/start-all
export function startAllSystemMonitorTasks() {
  return req<SystemMonitorBulkResponse>(
    'POST', '/api/admin/system-monitor/start-all', {}
  )
}

// POST /api/admin/system-monitor/stop-all
export function stopAllSystemMonitorTasks() {
  return req<{ queue_size: number; stopped: number; in_fallback: boolean; note: string }>(
    'POST', '/api/admin/system-monitor/stop-all', {}
  )
}

// POST /api/admin/system-monitor/by-credential/{id}
export function startByCredential(credID: number) {
  return req<SystemMonitorBulkResponse>(
    'POST', `/api/admin/system-monitor/by-credential/${credID}`, {}
  )
}

// POST /api/admin/system-monitor/by-provider/{id}
//   每条 binding 生成 direct_ping + http_ping（设计 §4.4 1:1 配对）
export function startByProvider(providerID: number) {
  return req<SystemMonitorBulkResponse>(
    'POST', `/api/admin/system-monitor/by-provider/${providerID}`, {}
  )
}

// POST /api/admin/system-monitor/by-model/{name}
export function startByModel(modelName: string) {
  const enc = encodeURIComponent(modelName)
  return req<SystemMonitorBulkResponse>(
    'POST', `/api/admin/system-monitor/by-model/${enc}`, {}
  )
}

// GET /api/admin/system-monitor/stats — 5s 轮询
export function fetchSystemMonitorStats() {
  return req<SystemMonitorStats>(
    'GET', '/api/admin/system-monitor/stats', {}
  )
}

// GET /api/admin/system-monitor/recent-runs?limit=50
export function fetchSystemMonitorRecentRuns(limit = 50) {
  return req<{ runs: SystemMonitorRun[]; total: number; limit: number }>(
    'GET', `/api/admin/system-monitor/recent-runs?limit=${encodeURIComponent(String(limit))}`
  )
}

// PATCH /api/admin/system-monitor/concurrency
export function updateSystemMonitorConcurrency(monitor_concurrency: number) {
  return req<{ monitor_concurrency: number; updated_at: string }>(
    'PATCH', '/api/admin/system-monitor/concurrency', { monitor_concurrency }
  )
}

// SSE 流：通过原生 EventSource 直连，使用 cookie 鉴权。
//
//   path = '/api/admin/system-monitor/stream'
//   envelope = { type, ts, task?, stats?, skip_reason?, recent_request_id? }
//
// 设计 §5.1 — 与 /api/admin/live-stream 物理隔离。
//
// EventSource 不支持自定义 header，所以 token 走 ?token= 查询串。
// 即便如此，llmgw_session cookie 仍通过 credentials=include 一并发出。
export function openSystemMonitorStream(
  onEvent: (env: SystemMonitorEvent) => void,
  onError?: (e: Event) => void,
): () => void {
  const base = ''
  const token = authBearer()
  const url = new URL(base + '/api/admin/system-monitor/stream', window.location.origin)
  if (token) url.searchParams.set('token', token)
  const es = new EventSource(url.toString(), { withCredentials: true })

  const handler = (raw: MessageEvent) => {
    let env: SystemMonitorEvent
    try {
      env = JSON.parse(raw.data) as SystemMonitorEvent
    } catch {
      return
    }
    onEvent(env)
  }
  // 后端按 type 字段命名 event（如 'submitted' / 'completed'）。
  // 我们监听所有 7 种状态 + heartbeat，并把 error 转给 onError。
  const eventNames = ['submitted', 'claimed', 'started', 'completed', 'skipped', 'failed', 'timeout', 'network_error', 'queue_full', 'heartbeat']
  for (const name of eventNames) {
    es.addEventListener(name, handler as EventListener)
  }
  es.onerror = (e) => {
    if (onError) onError(e)
  }

  return () => {
    for (const name of eventNames) {
      es.removeEventListener(name, handler as EventListener)
    }
    es.close()
  }
}

export interface SystemMonitorEvent {
  type:
    | 'submitted'
    | 'claimed'
    | 'started'
    | 'completed'
    | 'skipped'
    | 'failed'
    | 'timeout'
    | 'network_error'
    | 'queue_full'
    | 'heartbeat'
  ts: string
  task?: SystemMonitorTaskSummary
  stats?: SystemMonitorStatsPayload
  skip_reason?: string
  recent_request_id?: string
  host?: string
}

export interface SystemMonitorTaskSummary {
  id: number
  task_type: TaskType | string
  automaticity: Automaticity | string
  status: TaskStatus | string
  attempt: number
  max_attempts: number
  credential_id: number
  provider_id: number
  raw_model: string
  source: string
  worker_id?: string
  http_status?: number
  latency_ms?: number
  err_code?: string
  total_tokens?: number
}

export interface SystemMonitorStatsPayload {
  queue_size: number
  running_size: number
  monitor_concurrency: number
  skipped_total_1h: number
  completed_total_1h: number
  failed_total_1h: number
  total_tokens_1h: number
}
// ── Phase 3: Migration Metrics ────────────────────────────

export interface MigrationMetrics {
  window_days: number
  total_tasks: number
  system_monitor_tasks: number
  legacy_tasks: number
  coverage_percent: number
  by_source: Record<string, number>
  by_task_type: Record<string, number>
  collected_at: string
}

export interface MigrationMetricsResponse {
  metrics: MigrationMetrics
  ready_for_migration: boolean
  migration_message: string
}

export function fetchMigrationMetrics(windowDays = 7) {
  return req<MigrationMetricsResponse>(
    'GET',
    `/api/admin/system-monitor/migration-metrics?window_days=${encodeURIComponent(String(windowDays))}`
  )
}
