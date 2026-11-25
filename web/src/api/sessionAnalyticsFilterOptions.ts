import { req } from './_core'
import { rangeFromSpanDays } from '../components/ui/kxDatePresets'

export interface SessionAnalyticsFilterOptions {
  clients: string[] // top N client_id
  tasks: string[] // top N task_id
  users: Array<{ owner: string; tenant_id?: string }>
  refreshedAt: string
  startDate?: string
  endDate?: string
}

function buildQuery(params: Record<string, string | number | undefined>): string {
  const qs = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined) qs.append(k, String(v))
  }
  return qs.toString() ? '?' + qs.toString() : ''
}

/**
 * 从同一自然日范围获取客户端、任务和人员候选。
 */
export async function listSessionAnalyticsFilterOptions(params: {
  hours?: number
  limit?: number
} = {}): Promise<SessionAnalyticsFilterOptions> {
  const hours = params.hours ?? 24
  const limit = params.limit ?? 20
  const range = rangeFromSpanDays(Math.max(1, Math.min(90, Math.ceil(hours / 24))))
  const query = buildQuery({ start_date: range.start, end_date: range.end, limit })
  const [clients, tasks, users] = await Promise.all([
    req<{ clients: Array<{ client_id: string }>; refreshed_at?: string | null }>(
      'GET',
      `/api/admin/session-analytics/v2/clients${query}`,
    ),
    req<{ tasks: Array<{ task_id: string }>; refreshed_at?: string | null }>(
      'GET',
      `/api/admin/session-analytics/v2/tasks${query}`,
    ),
    req<{ users: Array<{ owner: string; tenant_id?: string }>; updated_at?: string | null }>(
      'GET',
      `/api/admin/session-analytics/v2/user-ranking${query}`,
    ),
  ])
  const timestamps = [clients.refreshed_at, tasks.refreshed_at, users.updated_at].map(v => v ? Date.parse(v) : NaN)
  return {
    clients: clients.clients.map((c) => c.client_id).filter(Boolean),
    tasks: tasks.tasks.map((t) => t.task_id).filter(Boolean),
    users: users.users.filter(u => u.owner).map(u => ({ owner: u.owner, tenant_id: u.tenant_id })),
    refreshedAt: timestamps.every(Number.isFinite) ? new Date(Math.min(...timestamps)).toISOString() : '',
    startDate: range.start,
    endDate: range.end,
  }
}
