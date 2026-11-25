import { req, type RequestOptions } from './client'

// errorPlaybook.ts — 错误处理手册管理面（移动端只读形态）。
//
// 后端：admin/error_playbook_handler.go 的 RegisterRoutes（fail-closed：
//   store/pool/secret 任一缺失 ⇒ 端点**不注册** ⇒ 本页会拿到 404 ——
//   这是「手册未开」的形态，不是请求失败；视图按 404 披露）。
//   注册点为 cmd/gateway/main.go 中的 `pbHandler.RegisterRoutes(mux, "/api/admin/error-playbook")`
//   （`errorPlaybookStoreGlobal != nil` 才装配）。
//
// ★ 权限档：wrap = AdminMiddleware（admin/error_playbook_handler.go:72-74，
//   只认证不检 role）⇒ 后端 tenant_admin 可读。但条目/执行**没有任何租户维度**
//   （error_playbook_entries / executions 表无 tenant_id 列，
//   `sql/migrations/startup/995_error_playbook.sql` 建表可见）⇒
//   平台级全局事实 ⇒ 抽屉席按本仓第三类「过度限制」设 super_admin
//   （与 ops-overview / platform-settings 同理由；PC 端菜单同档 super+hideForTenant）。
//
// ★★ 本模块**只接三个 GET**：/entries（列表）、/executions（列表）、/stats。
//   同族的写端点本页一律不碰：
//   · POST /entries、PUT/DELETE /entries/{id}、POST /entries/{id}/execute
//     —— 手册 CRUD 与「真的执行一次方案」（script/api/prompt 都有真实副作用）；
//   · POST /executions/{id}/revert —— 按快照回滚（写）；
//   · POST /analyze —— 真的跑一次 PI 分析（LLM 调用 + 落库）。

const BASE = '/api/admin/error-playbook'

/** plan_kind 的闭合集（`sql/migrations/startup/995_error_playbook.sql:70` CHECK 约束）。 */
export const PLAYBOOK_PLAN_KINDS = ['script', 'api', 'prompt'] as const
export type PlanKind = (typeof PLAYBOOK_PLAN_KINDS)[number]

/** source 的闭合集（同迁移 :76 CHECK 约束）。 */
export const PLAYBOOK_SOURCES = ['seed', 'manual', 'pi_analysis', 'refinement'] as const
export type EntrySource = (typeof PLAYBOOK_SOURCES)[number]

/** 执行状态的闭合集（domains/errorplaybook/engine.go 写入侧）。 */
export const EXECUTION_STATUSES = ['running', 'success', 'failed', 'skipped'] as const
export type ExecutionStatus = (typeof EXECUTION_STATUSES)[number]

/** 触发来源的闭合集（同迁移 :125 CHECK 约束）。 */
export const TRIGGER_SOURCES = ['standard', 'auto_analyze', 'manual', 'refinement'] as const
export type TriggerSource = (typeof TRIGGER_SOURCES)[number]

export interface PlaybookEntry {
  id: number
  error_type: string
  /** "" = 类型级条目（不按消息内容匹配）。 */
  match_pattern: string
  title: string
  description: string
  root_cause: string
  plan_kind: PlanKind
  /** confidence ∈ [0,1]（schema CHECK）。 */
  confidence: number
  source: EntrySource
  /** 0 = 无来源队列。 */
  source_queue_id: number
  enabled: boolean
  auto_execute: boolean
  hit_count: number
  execution_count: number
  success_count: number
  failure_count: number
  /** Go 零值时间可能以 "0001-01-01T00:00:00Z" 形态出现 ⇒ 视图按「从未」披露。 */
  last_hit_at: string | null
  last_executed_at: string | null
  last_status: string
  refinement_count: number
  created_by: string
  created_at: string
  updated_at: string
}

export interface PlaybookExecution {
  id: number
  /** 0 = 无（未知错误分析等）。 */
  entry_id: number
  error_type: string
  match_pattern: string
  trigger_source: TriggerSource
  status: ExecutionStatus
  /** steps JSONB 数组的 JSON 字符串；本页只数条数与状态，不展开渲染。 */
  steps?: unknown
  error_message?: string
  duration_ms: number
  executed_by: string
  created_at: string
}

export interface EntryListResult {
  /** 存储层 `continue` 丢行 ⇒ 本页的列表可能少于 total（文件头同款披露）。 */
  entries: PlaybookEntry[] | null
  total: number
}

export interface ExecutionListResult {
  executions: PlaybookExecution[] | null
  total: number
}

export interface PlaybookStats {
  /** 手册自动化引擎是否装配（engine 可为 nil ⇒ 手册管理仍可用）。 */
  engine_wired: boolean
  llm_wired: boolean
  total_entries?: number
  enabled_entries?: number
  auto_execute_entries?: number
  entries_by_source?: Record<string, number>
  entries_by_plan_kind?: Record<string, number>
  executions?: { last_24h: Record<string, number>; total: number }
  pending_unknown?: number
  last_run_at?: string
  provisioned: boolean
}

export interface EntryListFilter {
  enabled?: boolean
  source?: string
  error_type?: string
  q?: string
  limit?: number
  offset?: number
}

export interface ExecutionListFilter {
  entry_id?: number
  status?: string
  error_type?: string
  limit?: number
  offset?: number
}

function qs(params: Record<string, string | number | boolean | undefined>): string {
  const parts: string[] = []
  for (const [k, v] of Object.entries(params)) {
    if (v === undefined || v === '') continue
    parts.push(`${encodeURIComponent(k)}=${encodeURIComponent(String(v))}`)
  }
  return parts.length ? `?${parts.join('&')}` : ''
}

export async function listEntries(
  filter: EntryListFilter,
  options?: RequestOptions,
): Promise<EntryListResult> {
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
    undefined,
    options,
  )
}

export async function listExecutions(
  filter: ExecutionListFilter,
  options?: RequestOptions,
): Promise<ExecutionListResult> {
  return req<ExecutionListResult>(
    'GET',
    `${BASE}/executions${qs({
      entry_id: filter.entry_id,
      status: filter.status,
      error_type: filter.error_type,
      limit: filter.limit,
      offset: filter.offset,
    })}`,
    undefined,
    options,
  )
}

export async function getStats(options?: RequestOptions): Promise<PlaybookStats> {
  return req<PlaybookStats>('GET', `${BASE}/stats`, undefined, options)
}
