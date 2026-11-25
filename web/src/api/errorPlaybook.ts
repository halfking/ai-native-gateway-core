// errorPlaybook.ts — 错误处理手册 admin API client（2026-11-21）
//
// Wraps the endpoints served by `admin/error_playbook_handler.go`
// (/api/admin/error-playbook/*). Every call goes through `req<T>()` from _core
// so the unified 401 adjudication + JSON error parsing come for free.
//
// Contract notes verified against the Go handlers (2026-11-21 live on 211):
//   · `entries` / `executions` lists ride inside `{..., total}` envelopes;
//     treat the array fields as possibly-null anyway (Go slice → `null`).
//   · Business failures come back as 422 with a PLAIN-TEXT body
//     (`error_type is required`, `api plan route ... is not registered ...`).
//     `req<T>()` surfaces that text on `ApiError.detail` — show it verbatim,
//     it is written to be user-facing (fail-closed gating copy).
//   · PUT /entries/{id} only replaces title/description/root_cause when the
//     plan is being replaced at the same time — and the Go handler then
//     defaults MISSING text fields to "" ⇒ always send the three text fields
//     together with the plan. Flags-only saves must omit `plan`.
//   · Execution `steps` is a JSONB array of StepLog; snapshots live per step.

import { req } from './_core'

const BASE = '/api/admin/error-playbook'

// ─────────────────────────────────────────────────────────────
// 类型（与 domains/errorplaybook/playbook.go 的 json tag 一一对应）
// ─────────────────────────────────────────────────────────────

export type PlanKind = 'script' | 'api' | 'prompt'

/** script 方案的单个动作步骤（白名单闭集见 ActionRegistry）。 */
export interface PlanStep {
  action: string
  params?: Record<string, unknown> | null
  note?: string
}

/** 归一化后的预定执行方案（plan_payload 列的形状）。 */
export interface PlaybookPlan {
  kind: PlanKind
  steps?: PlanStep[] | null
  method?: string
  path?: string
  body?: unknown
  timeout_ms?: number
  prompt?: string
  expect?: '' | 'analysis' | 'proposal'
}

export type EntrySource = 'seed' | 'manual' | 'pi_analysis' | 'refinement'

/** 手册条目（error_playbook_entries 一行）。 */
export interface PlaybookEntry {
  id: number
  error_type: string
  match_pattern: string
  title: string
  description: string
  root_cause: string
  plan_kind: PlanKind
  plan: PlaybookPlan
  confidence: number
  source: EntrySource
  source_queue_id: number
  enabled: boolean
  auto_execute: boolean
  hit_count: number
  execution_count: number
  success_count: number
  failure_count: number
  last_hit_at: string | null
  last_executed_at: string | null
  last_status: string
  refinement_count: number
  created_by: string
  created_at: string
  updated_at: string
}

export type ExecutionStatus =
  | 'succeeded'
  | 'failed'
  | 'skipped'
  | 'awaiting_approval'
  | 'no_action'

/** 执行留痕里的单步记录（steps JSONB 数组的元素）。 */
export interface StepLog {
  step: number
  action: string
  status: 'ok' | 'failed' | 'skipped'
  result?: string
  snapshot?: unknown
  error?: string
}

/** 执行留痕（error_playbook_executions 一行）。 */
export interface PlaybookExecution {
  id: number
  entry_id: number
  error_type: string
  match_pattern: string
  trigger_source: 'standard' | 'auto_analyze' | 'manual' | 'refinement'
  context?: unknown
  status: ExecutionStatus
  steps?: StepLog[] | null
  error_message?: string
  duration_ms: number
  executed_by: string
  created_at: string
}

export interface PlaybookStats {
  engine_wired: boolean
  llm_wired: boolean
  provisioned: boolean
  total_entries?: number
  enabled_entries?: number
  auto_execute_entries?: number
  entries_by_source?: Record<string, number> | null
  entries_by_plan_kind?: Record<string, number> | null
  executions?: { total?: number; last_24h?: Record<string, number> | null } | null
  pending_unknown?: Record<string, number> | null
  last_run_at?: string
}

// ─────────────────────────────────────────────────────────────
// 请求/响应体
// ─────────────────────────────────────────────────────────────

export interface EntryListFilter {
  enabled?: boolean
  source?: string
  error_type?: string
  q?: string
  limit?: number
  offset?: number
}

export interface EntryListResult {
  entries: PlaybookEntry[] | null
  total: number
}

/** 手工录入/方案替换的请求体（plan 已在客户端组装成归一化形状）。 */
export interface EntryWriteBody {
  error_type?: string
  match_pattern?: string
  title?: string
  description?: string
  root_cause?: string
  plan_kind?: PlanKind
  plan?: unknown
  enabled?: boolean
  auto_execute?: boolean
}

export interface ExecutionListFilter {
  entry_id?: number
  status?: string
  error_type?: string
  limit?: number
  offset?: number
  recommendations?: 'only'
}

export interface ExecutionListResult {
  executions: PlaybookExecution[] | null
  total: number
}

export interface AnalyzeBody {
  error_type: string
  message?: string
  samples?: string[]
  occurrences?: number
}

export interface ExecuteResult {
  execution: PlaybookExecution
  error?: string
}

export interface RevertResult {
  reverted?: Array<{ step: number; action: string; result: string }> | null
  error?: string
}

// ─────────────────────────────────────────────────────────────
// 端点封装
// ─────────────────────────────────────────────────────────────

function qs(params: Record<string, string | number | boolean | undefined>): string {
  const parts: string[] = []
  for (const [k, v] of Object.entries(params)) {
    if (v === undefined || v === '') continue
    parts.push(`${encodeURIComponent(k)}=${encodeURIComponent(String(v))}`)
  }
  return parts.length ? `?${parts.join('&')}` : ''
}

export async function listEntries(filter: EntryListFilter): Promise<EntryListResult> {
  return req<EntryListResult>(
    'GET',
    `${BASE}/entries${qs({
      enabled: filter.enabled,
      source: filter.source,
      error_type: filter.error_type,
      q: filter.q,
      limit: filter.limit,
      offset: filter.offset,
    })}`,
  )
}

export async function createEntry(body: EntryWriteBody): Promise<PlaybookEntry> {
  return req<PlaybookEntry>('POST', `${BASE}/entries`, body)
}

export async function getEntry(id: number): Promise<PlaybookEntry> {
  return req<PlaybookEntry>('GET', `${BASE}/entries/${id}`)
}

export async function updateEntry(id: number, body: EntryWriteBody): Promise<PlaybookEntry> {
  return req<PlaybookEntry>('PUT', `${BASE}/entries/${id}`, body)
}

export async function deleteEntry(id: number): Promise<void> {
  await req<{ deleted: boolean }>('DELETE', `${BASE}/entries/${id}`)
}

export async function executeEntry(id: number, message?: string): Promise<ExecuteResult> {
  return req<ExecuteResult>('POST', `${BASE}/entries/${id}/execute`, { message: message ?? '' })
}

export async function listExecutions(
  filter: ExecutionListFilter,
): Promise<ExecutionListResult> {
  return req<ExecutionListResult>(
    'GET',
    `${BASE}/executions${qs({
      entry_id: filter.entry_id,
      recommendations: filter.recommendations,
      status: filter.status,
      error_type: filter.error_type,
      limit: filter.limit,
      offset: filter.offset,
    })}`,
  )
}

export async function getExecution(id: number): Promise<PlaybookExecution> {
  return req<PlaybookExecution>('GET', `${BASE}/executions/${id}`)
}

export async function revertExecution(id: number): Promise<RevertResult> {
  return req<RevertResult>('POST', `${BASE}/executions/${id}/revert`)
}

export async function analyzeUnknown(body: AnalyzeBody): Promise<{ message: string }> {
  return req<{ message: string }>('POST', `${BASE}/analyze`, body)
}

export async function getStats(): Promise<PlaybookStats> {
  return req<PlaybookStats>('GET', `${BASE}/stats`)
}
