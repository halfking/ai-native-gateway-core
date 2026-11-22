// api-teams.ts — W2-Q2 Team hierarchy management API bindings
//
// ★ 复用共享客户端 `src/api/_core.ts` 的 `req`（与 `src/api/keys.ts` 同一写法）：
//   · 鉴权/401 处理只有一份真值 —— _core 的 headers() 走 authBearer()，
//     **不能只读 store.apiKey**（那会让所有 JWT 登录的请求都 401，见 _core.ts 顶部注释）；
//   · 401 走「会话探针」裁决（401 ≠ 会话过期），本地这份实现没有这层 ⇒ 会误踢好会话；
//   · 同源并发闸门（MAX_SAME_ORIGIN_CONCURRENCY）也只在 _core 里。
// ⇒ 本文件不再自带 fetch 封装。

import { req } from './api/_core'

// ============================================================================
// Type Definitions
// ============================================================================

export type UserTeamRole = 'owner' | 'admin' | 'member'
export type EnforcementMode = 'warn' | 'hard'

export interface Team {
  id: number
  name: string
  display_name: string | null
  owner_id: number
  tenant_id: string
  description: string | null
  created_at: string
  updated_at: string
}

export interface TeamMember {
  id: number
  user_id: number
  team_id: number
  role: UserTeamRole
  joined_at: string
}

export interface TeamBudget {
  id: number
  team_id: number
  monthly_limit_credits: number
  enforcement_mode: EnforcementMode
  created_at: string
  updated_at: string
}

export interface TeamBudgetStats {
  team_id: number
  enforcement_mode: string
  monthly_limit_credits: number
  spent_credits: number
  spent_cents: number
  over: boolean
}

// ============================================================================
// Request/Response Types
// ============================================================================

export interface CreateTeamRequest {
  name: string
  display_name?: string | null
  description?: string | null
}

export interface UpdateTeamRequest {
  display_name?: string | null
  description?: string | null
}

export interface AddMemberRequest {
  team_id: number
  user_id: number
  role: UserTeamRole
}

export interface RemoveMemberRequest {
  team_id: number
  user_id: number
}

export interface UpsertBudgetRequest {
  team_id: number
  monthly_limit_credits: number
  enforcement_mode: EnforcementMode
}

export interface ListTeamsResponse {
  teams: Team[]
}

// ============================================================================
// Team CRUD Operations
// ============================================================================

/**
 * Create a new team
 * POST /api/admin/teams
 *
 * ★ 参数名不能叫 `req`：那会**遮蔽**共享客户端的 `req` 函数
 *   （函数体内的 `req<Team>(...)` 会解析成这个对象 ⇒ TS2349「not callable」）。
 */
export async function createTeam(payload: CreateTeamRequest): Promise<Team> {
  return await req<Team>('POST', '/api/admin/teams', payload)
}

/**
 * List all teams owned by the current user
 * GET /api/admin/teams
 */
export async function listTeams(): Promise<ListTeamsResponse> {
  return await req<ListTeamsResponse>('GET', '/api/admin/teams')
}

/**
 * Get team details by ID
 * GET /api/admin/teams/{id}
 */
export async function getTeam(teamId: number): Promise<Team> {
  return await req<Team>('GET', `/api/admin/teams/${teamId}`)
}

/**
 * Update team details
 * PUT /api/admin/teams/{id}
 */
export async function updateTeam(teamId: number, updates: UpdateTeamRequest): Promise<Team> {
  return await req<Team>('PUT', `/api/admin/teams/${teamId}`, updates)
}

/**
 * Delete a team
 * DELETE /api/admin/teams/{id}
 */
export async function deleteTeam(teamId: number): Promise<void> {
  await req<void>('DELETE', `/api/admin/teams/${teamId}`)
}

// ============================================================================
// Member Management
// ============================================================================

/**
 * Add a member to a team
 * POST /api/admin/teams/members/add
 */
export async function addTeamMember(request: AddMemberRequest): Promise<TeamMember> {
  return await req<TeamMember>('POST', '/api/admin/teams/members/add', request)
}

/**
 * Remove a member from a team
 * POST /api/admin/teams/members/remove
 */
export async function removeTeamMember(request: RemoveMemberRequest): Promise<void> {
  await req<void>('POST', '/api/admin/teams/members/remove', request)
}

// ============================================================================
// Budget Management
// ============================================================================

/**
 * Get team budget configuration
 * GET /api/admin/teams/budget?team_id={id}
 */
export async function getTeamBudget(teamId: number): Promise<TeamBudget | null> {
  const response = await req<{ budget: TeamBudget | null }>(
    'GET',
    `/api/admin/teams/budget?team_id=${teamId}`
  )
  return response.budget
}

/**
 * Create or update team budget
 * PUT /api/admin/teams/budget
 */
export async function upsertTeamBudget(budget: UpsertBudgetRequest): Promise<TeamBudget> {
  return await req<TeamBudget>('PUT', '/api/admin/teams/budget', budget)
}

/**
 * Get team budget usage statistics
 * GET /api/admin/teams/budget/stats?team_id={id}
 */
export async function getTeamBudgetStats(teamId: number): Promise<TeamBudgetStats> {
  return await req<TeamBudgetStats>('GET', `/api/admin/teams/budget/stats?team_id=${teamId}`)
}

// ============================================================================
// Utility Functions
// ============================================================================

/**
 * Check if a budget is over the limit
 */
export function isBudgetOverLimit(stats: TeamBudgetStats): boolean {
  return stats.over
}

/**
 * Calculate remaining budget in credits
 */
export function getRemainingCredits(stats: TeamBudgetStats): number {
  return Math.max(0, stats.monthly_limit_credits - stats.spent_credits)
}

/**
 * Get budget usage percentage
 */
export function getBudgetUsagePercentage(stats: TeamBudgetStats): number {
  if (stats.monthly_limit_credits <= 0) return 0
  return (stats.spent_credits / stats.monthly_limit_credits) * 100
}
