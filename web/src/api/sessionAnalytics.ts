// sessionAnalytics.ts — Session Analytics admin API client (2026-11-20)
//
// Wraps the 7 endpoints served by the Go package `admin/session_analytics`
// (admin/session_analytics/handlers.go). Every call goes through `req<T>()`
// from _core so the unified 401 adjudication + JSON error parsing come for free.
//
// Contract notes that the UI must respect (verified against the Go handlers):
//   · `start_date` / `end_date` are REQUIRED and must be YYYY-MM-DD.
//   · Every list field is a Go slice. When a query returns no rows the slice
//     stays nil ⇒ the server serializes it as `null`, NOT `[]`. Callers must
//     therefore treat every array field as possibly-null.
//   · `last_active_at` / `last_used_at` are RFC3339 strings, and are the
//     EMPTY STRING (not null) when the row has no timestamp.
//   · The freshness gate answers 503 with `code=analytics_view_stale` when the
//     materialized views are older than the budget. That is a "data is being
//     refreshed" state, NOT a generic failure — see `isViewStaleError`.

import { ApiError, req, type RequestOptions } from './_core'

const BASE = '/api/admin/session-analytics'

/** Inclusive date window shared by every endpoint of this API. */
export interface SessionAnalyticsRange {
  /** YYYY-MM-DD */
  start_date: string
  /** YYYY-MM-DD */
  end_date: string
}

/** Optional tenant scope; only super admins may pass one. */
export interface SessionAnalyticsScope {
  tenant_id?: string
}

export type SortOrder = 'asc' | 'desc'

export interface OverviewSummary {
  total_sessions: number
  total_sessions_growth: number | null
  avg_tokens: number | null
  avg_context_size: number | null
  avg_duration_seconds: number | null
  avg_turns: number | null
  request_count: number
  total_cost_usd: number | null
  total_cost_usd_growth: number | null
  credits_charged: number
  usd_unmeasured_requests: number
  active_users: number
  active_users_growth: number | null
  active_projects: number
  active_tasks: number
  overall_success_rate: number | null
  overall_cache_rate: number | null
}

export interface OverviewTopModel {
  model: string
  count: number
}

export interface OverviewTrendPoint {
  date: string
  count: number
}

export interface SessionCostTrendPoint {
  date: string
  total_cost_usd: number | null
  credits_charged: number
}

export interface OverviewTokenTrendPoint {
  date: string
  total: number
}

export interface OverviewTrends {
  sessions: OverviewTrendPoint[] | null
  cost: SessionCostTrendPoint[] | null
  tokens: OverviewTokenTrendPoint[] | null
}

export interface OverviewTopUser {
  owner: string
  display_name: string
  email: string
  session_count: number
  total_tokens: number
  total_cost: number | null
  credits_charged?: number
}

export interface OverviewTopProject {
  project: string
  session_count: number
  user_count: number
  total_cost: number | null
}

export interface SessionAnalyticsOverview {
  summary: OverviewSummary
  top_models: OverviewTopModel[] | null
  trends: OverviewTrends
  top_users: OverviewTopUser[] | null
  top_projects: OverviewTopProject[] | null
}

export interface UserRankingRow {
  rank: number
  owner: string
  tenant_id?: string
  display_name: string
  email: string
  session_count: number
  project_count: number
  task_count: number
  total_tokens: number
  total_cost: number | null
  credits_charged?: number
  /** RFC3339, or "" when the row has no activity timestamp. */
  last_active_at: string
  latest_task: string | null
}

export interface UserRankingResponse {
  total: number
  users: UserRankingRow[] | null
  pending_since?: string | null
}

export interface UserDetailInfo {
  owner: string
  display_name: string
  email: string
  tenant_id: string
}

export interface UserDetailSummary {
  session_count: number
  task_count: number
  total_cost: number | null
  credits_charged?: number
  usd_unmeasured_requests?: number
  rank: number | null
  rank_change: number | null
}

export interface UserTrendPoint {
  date: string
  count: number
}

export interface UserRankTrendPoint {
  date: string
  rank: number
}

export interface UserDetailTrends {
  sessions: UserTrendPoint[] | null
  tasks: UserTrendPoint[] | null
  cost: SessionCostTrendPoint[] | null
  rank: UserRankTrendPoint[] | null
}

export interface UserDetailResponse {
  user: UserDetailInfo
  summary: UserDetailSummary
  trends: UserDetailTrends
  pending_since?: string | null
}

export interface UserProjectRow {
  project: string
  session_count: number
  task_count: number
  total_tokens: number
  total_cost: number | null
  credits_charged?: number
  last_active_at: string
}

export interface UserProjectsResponse {
  projects: UserProjectRow[] | null
}

export interface ProjectTaskRow {
  task: string
  session_count: number
  request_count: number
  total_tokens: number
  total_cost: number | null
  credits_charged?: number
  last_active_at: string
}

export interface ProjectTasksResponse {
  tasks: ProjectTaskRow[] | null
}

export interface AgentRankingRow {
  rank: number
  agent_name: string
  user_count: number
  session_count: number
  task_count: number
  total_tokens: number
  total_cost: number | null
  credits_charged?: number
  usd_unmeasured_requests?: number
  avg_duration_seconds: number | null
  last_used_at: string
}

export interface AgentRankingResponse {
  agents: AgentRankingRow[] | null
  pending_since?: string | null
}

export interface ModelRankingRow {
  rank: number
  model: string
  provider: string
  request_count: number
  success_count: number
  success_rate: number
  total_tokens: number
  cache_hit_count: number
  cache_rate: number
  avg_duration_ms: number | null
  total_cost: number | null
  credits_charged?: number
  usd_unmeasured_requests?: number
  user_count: number
  last_used_at: string
}

export interface ModelRankingResponse {
  models: ModelRankingRow[] | null
  pending_since?: string | null
}

/* ── provider ranking (2026-11-23 审计轮新增) ───────────────────────────── */

/**
 * 供应商排行行。
 *
 * ★ 为什么不能在前端聚合：物化视图 `model_usage_aggregates` 的 `model` 列是
 *   **纯模型名**（不含 `provider/` 前缀），前端 `split('/')[0]` 会把整个模型名
 *   当成 provider（如 `claude-sonnet-4-5`）；而 overview 的 `top_models` 又只有
 *   3 条 ⇒ 前端聚合既得不到全量 Top5、也会给出错误的 provider 名。
 *   ⇒ provider 维度由后端 SQL 直接聚合（见 admin/session_analytics/queries.go）。
 */
export interface ProviderRankingRow {
  rank: number
  provider: string
  model_count: number
  request_count: number
  success_count: number
  success_rate: number
  total_tokens: number
  total_cost: number | null
  credits_charged?: number
  usd_unmeasured_requests?: number
  user_count: number
  last_used_at: string
}

export interface ProviderRankingResponse {
  providers: ProviderRankingRow[] | null
  pending_since?: string | null
}

/* ── task ranking (2026-11-23 审计轮新增) ───────────────────────────────── */

/**
 * 全局任务排行行。
 *
 * ★ 口径：物化视图 `project_task_aggregates`（会话级，与 user_session_aggregates
 *   同源）。`task` 空串 = 「无具体任务」，后端已过滤。
 */
export interface TaskRankingRow {
  rank: number
  task: string
  owner_count: number
  session_count: number
  turn_count: number
  total_tokens: number
  total_cost: number | null
  credits_charged?: number
  usd_unmeasured_requests?: number
  last_active_at: string
}

export interface TaskRankingResponse {
  tasks: TaskRankingRow[] | null
  pending_since?: string | null
}

/* ── query option bags ─────────────────────────────────────────────────── */

export interface OverviewQuery extends SessionAnalyticsRange, SessionAnalyticsScope {}

export type UserRankingSort = 'sessions' | 'tokens' | 'cost' | 'last_active'

export interface UserRankingQuery extends SessionAnalyticsRange, SessionAnalyticsScope {
  project?: string
  sort_by?: UserRankingSort
  order?: SortOrder
  limit?: number
  offset?: number
}

export interface UserDetailQuery extends SessionAnalyticsRange, SessionAnalyticsScope {}

export interface UserProjectsQuery extends SessionAnalyticsRange, SessionAnalyticsScope {
  sort_by?: 'sessions' | 'cost' | 'last_active'
  order?: SortOrder
}

export interface ProjectTasksQuery extends SessionAnalyticsRange, SessionAnalyticsScope {
  sort_by?: 'sessions' | 'cost' | 'last_active'
}

export type AgentRankingSort = 'sessions' | 'users' | 'cost' | 'last_used'

export interface AgentRankingQuery extends SessionAnalyticsRange, SessionAnalyticsScope {
  sort_by?: AgentRankingSort
  order?: SortOrder
}

export type ModelRankingSort =
  | 'requests'
  | 'tokens'
  | 'cost'
  | 'success_rate'
  | 'cache_rate'
  | 'last_used'

export interface ModelRankingQuery extends SessionAnalyticsRange, SessionAnalyticsScope {
  sort_by?: ModelRankingSort
  order?: SortOrder
}

/** 供应商排行排序字段（白名单与 model-ranking 不同：多 models/users）。 */
export type ProviderRankingSort =
  | 'requests'
  | 'tokens'
  | 'cost'
  | 'models'
  | 'users'
  | 'last_used'

export interface ProviderRankingQuery extends SessionAnalyticsRange, SessionAnalyticsScope {
  sort_by?: ProviderRankingSort
  order?: SortOrder
}

/** 任务排行排序字段。 */
export type TaskRankingSort =
  | 'sessions'
  | 'turns'
  | 'tokens'
  | 'cost'
  | 'users'
  | 'last_active'

export interface TaskRankingQuery extends SessionAnalyticsRange, SessionAnalyticsScope {
  sort_by?: TaskRankingSort
  order?: SortOrder
}

/* ── helpers ───────────────────────────────────────────────────────────── */

function query(params: Record<string, string | number | undefined>): string {
  const sp = new URLSearchParams()
  for (const [key, value] of Object.entries(params)) {
    if (value === undefined || value === '') continue
    sp.set(key, String(value))
  }
  const s = sp.toString()
  return s ? `?${s}` : ''
}

/**
 * The server answers 503 + `code=analytics_view_stale` when the aggregation
 * materialized views are outside the freshness budget. Callers should render a
 * "data is refreshing" state rather than a generic error, because the server
 * deliberately refuses to answer with stale numbers.
 */
export function isViewStaleError(e: unknown): boolean {
  return e instanceof ApiError && e.status === 503 && e.code === 'analytics_view_stale'
}

/** The view is missing entirely (migration 981 not applied on this database). */
export function isViewMissingError(e: unknown): boolean {
  return e instanceof ApiError && e.code === 'analytics_view_missing'
}

/**
 * YYYY-MM-DD for `days` days ending today (inclusive), in local time.
 * The server compares these as plain date strings.
 */
export function defaultDateRange(days = 7): SessionAnalyticsRange {
  const end = new Date()
  const start = new Date(end)
  start.setDate(start.getDate() - (days - 1))
  return {
    start_date: toDateParam(start),
    end_date: toDateParam(end),
  }
}

/** Formats a Date as the YYYY-MM-DD string the API expects. */
export function toDateParam(d: Date): string {
  const y = d.getFullYear()
  const m = String(d.getMonth() + 1).padStart(2, '0')
  const day = String(d.getDate()).padStart(2, '0')
  return `${y}-${m}-${day}`
}

/* ── endpoints ─────────────────────────────────────────────────────────── */

/** GET /api/admin/session-analytics/v2/overview */
export function getOverview(
  params: OverviewQuery,
  options?: RequestOptions,
): Promise<SessionAnalyticsOverview> {
  return req<SessionAnalyticsOverview>(
    'GET',
    `${BASE}/v2/overview${query({
      tenant_id: params.tenant_id,
      start_date: params.start_date,
      end_date: params.end_date,
    })}`,
    undefined,
    options,
  )
}

/** GET /api/admin/session-analytics/v2/user-ranking */
export function getUserRanking(
  params: UserRankingQuery,
  options?: RequestOptions,
): Promise<UserRankingResponse> {
  return req<UserRankingResponse>(
    'GET',
    `${BASE}/v2/user-ranking${query({
      tenant_id: params.tenant_id,
      start_date: params.start_date,
      end_date: params.end_date,
      project: params.project,
      sort_by: params.sort_by,
      order: params.order,
      limit: params.limit,
      offset: params.offset,
    })}`,
    undefined,
    options,
  )
}

/**
 * GET /api/admin/session-analytics/v2/user-ranking/{owner}
 *
 * The owner is a path segment: it MUST be percent-encoded, otherwise an owner
 * containing "/" or "#" would silently address a different endpoint.
 */
export function getUserDetail(
  owner: string,
  params: UserDetailQuery,
  options?: RequestOptions,
): Promise<UserDetailResponse> {
  return req<UserDetailResponse>(
    'GET',
    `${BASE}/v2/user-ranking/${encodeURIComponent(owner)}${query({
      tenant_id: params.tenant_id,
      start_date: params.start_date,
      end_date: params.end_date,
    })}`,
    undefined,
    options,
  )
}

/** GET /api/admin/session-analytics/v2/user-ranking/{owner}/projects */
export function getUserProjects(
  owner: string,
  params: UserProjectsQuery,
  options?: RequestOptions,
): Promise<UserProjectsResponse> {
  return req<UserProjectsResponse>(
    'GET',
    `${BASE}/v2/user-ranking/${encodeURIComponent(owner)}/projects${query({
      tenant_id: params.tenant_id,
      start_date: params.start_date,
      end_date: params.end_date,
      sort_by: params.sort_by,
      order: params.order,
    })}`,
    undefined,
    options,
  )
}

/** GET /api/admin/session-analytics/v2/user-ranking/{owner}/projects/{project}/tasks */
export function getProjectTasks(
  owner: string,
  project: string,
  params: ProjectTasksQuery,
  options?: RequestOptions,
): Promise<ProjectTasksResponse> {
  return req<ProjectTasksResponse>(
    'GET',
    `${BASE}/v2/user-ranking/${encodeURIComponent(owner)}/projects/${encodeURIComponent(
      project,
    )}/tasks${query({
      tenant_id: params.tenant_id,
      start_date: params.start_date,
      end_date: params.end_date,
      sort_by: params.sort_by,
    })}`,
    undefined,
    options,
  )
}

/** GET /api/admin/session-analytics/v2/agent-ranking */
export function getAgentRanking(
  params: AgentRankingQuery,
  options?: RequestOptions,
): Promise<AgentRankingResponse> {
  return req<AgentRankingResponse>(
    'GET',
    `${BASE}/v2/agent-ranking${query({
      tenant_id: params.tenant_id,
      start_date: params.start_date,
      end_date: params.end_date,
      sort_by: params.sort_by,
      order: params.order,
    })}`,
    undefined,
    options,
  )
}

/** GET /api/admin/session-analytics/v2/model-ranking */
export function getModelRanking(
  params: ModelRankingQuery,
  options?: RequestOptions,
): Promise<ModelRankingResponse> {
  return req<ModelRankingResponse>(
    'GET',
    `${BASE}/v2/model-ranking${query({
      tenant_id: params.tenant_id,
      start_date: params.start_date,
      end_date: params.end_date,
      sort_by: params.sort_by,
      order: params.order,
    })}`,
    undefined,
    options,
  )
}

// ============= 用户模型用量 (2026-11-22) =============

/** GET /api/admin/session-analytics/v2/provider-ranking */
export function getProviderRanking(
  params: ProviderRankingQuery,
  options?: RequestOptions,
): Promise<ProviderRankingResponse> {
  return req<ProviderRankingResponse>(
    'GET',
    `${BASE}/v2/provider-ranking${query({
      tenant_id: params.tenant_id,
      start_date: params.start_date,
      end_date: params.end_date,
      sort_by: params.sort_by,
      order: params.order,
    })}`,
    undefined,
    options,
  )
}

/** GET /api/admin/session-analytics/v2/task-ranking */
export function getTaskRanking(
  params: TaskRankingQuery,
  options?: RequestOptions,
): Promise<TaskRankingResponse> {
  return req<TaskRankingResponse>(
    'GET',
    `${BASE}/v2/task-ranking${query({
      tenant_id: params.tenant_id,
      start_date: params.start_date,
      end_date: params.end_date,
      sort_by: params.sort_by,
      order: params.order,
    })}`,
    undefined,
    options,
  )
}
export interface UserModelUsage {
  model: string
  request_count: number
  total_tokens: number
  total_cost: number | null
  credits_charged?: number
  success_rate: number
  cache_hit_rate: number
  avg_response_time_ms: number | null
  last_used_at: string
}

export interface UserModelUsageResponse {
  models: UserModelUsage[]
}

export interface UserModelUsageQuery extends SessionAnalyticsRange, SessionAnalyticsScope {}

/** GET /api/admin/session-analytics/v2/user-ranking/:owner/models */
export function getUserModelUsage(
  owner: string,
  params: UserModelUsageQuery,
  options?: RequestOptions,
): Promise<UserModelUsageResponse> {
  return req<UserModelUsageResponse>(
    'GET',
    `${BASE}/v2/user-ranking/${encodeURIComponent(owner)}/models${query({
      tenant_id: params.tenant_id,
      start_date: params.start_date,
      end_date: params.end_date,
    })}`,
    undefined,
    options,
  )
}

// ============= 用户API Key用量 (2026-11-22) =============

export interface UserAPIKeyUsage {
  key_id: number
  key_alias: string
  request_count: number
  total_tokens: number
  total_cost: number | null
  credits_charged?: number
  last_used_at: string
  project_count: number
  top_project: string
}

export interface UserAPIKeyUsageResponse {
  api_keys: UserAPIKeyUsage[]
}

export interface UserAPIKeyUsageQuery extends SessionAnalyticsRange, SessionAnalyticsScope {}

/** GET /api/admin/session-analytics/v2/user-ranking/:owner/api-keys */
export function getUserAPIKeyUsage(
  owner: string,
  params: UserAPIKeyUsageQuery,
  options?: RequestOptions,
): Promise<UserAPIKeyUsageResponse> {
  return req<UserAPIKeyUsageResponse>(
    'GET',
    `${BASE}/v2/user-ranking/${encodeURIComponent(owner)}/api-keys${query({
      tenant_id: params.tenant_id,
      start_date: params.start_date,
      end_date: params.end_date,
    })}`,
    undefined,
    options,
  )
}
