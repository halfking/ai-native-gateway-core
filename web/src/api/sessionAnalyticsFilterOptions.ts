import { req } from './_core'

// sessionAnalyticsFilterOptions.ts — 批 158a（2026-10-17）
// P1 sessionAnalyticsFilterOptions 孤儿接前端：聚合 session-analytics
// 6 端点（clients / tasks / users）的客户端/任务/用户 id 作为 filter options。
// 后端无新端点（6 端点已存在，29 跳验证全部有 12-18 命中租户隔离）。
//
// 与批 116 modelIQ 同形态：API helper + View + i18n + router + 测试。

export interface SessionAnalyticsFilterOptions {
  clients: string[] // top N client_id
  tasks: string[] // top N task_id
  users: string[] // top N user/owner
  refreshedAt: string // 最近一次聚合时间
}

function buildQuery(params: Record<string, string | number | undefined>): string {
  const qs = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined) qs.append(k, String(v))
  }
  return qs.toString() ? '?' + qs.toString() : ''
}

/**
 * 聚合 session-analytics 6 端点数据，取 client_id / task_id / owner 列表
 * 作为 SessionAnalyticsFilterOptionsView 的下拉框选项。
 */
export async function listSessionAnalyticsFilterOptions(params: {
  hours?: number
  limit?: number
} = {}): Promise<SessionAnalyticsFilterOptions> {
  const hours = params.hours ?? 24
  const limit = params.limit ?? 20
  const clientsQs = buildQuery({ hours, limit })
  const tasksQs = buildQuery({ hours, limit })
  const usersQs = buildQuery({ hours, limit })
  // 三端点并发请求
  const [clients, tasks, users] = await Promise.all([
    req<{ clients: Array<{ client_id: string }> }>(
      'GET',
      `/api/admin/session-analytics/clients${clientsQs}`,
    ),
    req<{ tasks: Array<{ task_id: string }> }>(
      'GET',
      `/api/admin/session-analytics/tasks${tasksQs}`,
    ),
    req<{ users: Array<{ owner: string }> }>(
      'GET',
      `/api/admin/session-analytics/users${usersQs}`,
    ),
  ])
  return {
    clients: clients.clients.map((c) => c.client_id).filter(Boolean),
    tasks: tasks.tasks.map((t) => t.task_id).filter(Boolean),
    users: users.users.map((u) => u.owner).filter(Boolean),
    refreshedAt: new Date().toISOString(),
  }
}
