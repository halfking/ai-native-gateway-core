// turnsListHelpers.ts — TurnsListView 纯函数：标签、分组、参数、格式化
import type { TurnsSessionGroup, TurnsSessionsParams } from '../../api/turns'

export type TurnsGroupBy = 'project' | 'owner' | 'client' | 'apikey' | 'flat'

export interface TurnsTaskGroup {
  key: string
  label: string
  sessions: TurnsSessionGroup[]
}

export interface TurnsDisplayGroup {
  key: string
  label: string
  tasks: TurnsTaskGroup[]
  sessionCount: number
  totalTurns: number
  totalTokens: number
  totalCost: number
}

export interface TurnsFilterState {
  search: string
  model: string
  provider: string
  statusCode: string
  projectId: string
  taskId: string
  client: string
  ownerUser: string
  apiKeyId: string
  status: string
  tags: string[]
  dateFrom: string
  dateTo: string
  timePreset: string
}

export const NO_PROJECT = '未分类项目'
export const NO_TASK = '未分类任务'
export const NO_OWNER = '未指定用户'
export const NO_CLIENT = '未指定客户端'
export const NO_APIKEY = '未关联 API Key'

export function taskLabel(taskId?: string): string {
  if (!taskId) return NO_TASK
  if (taskId.startsWith('auto-summary:')) return '总结生成分支'
  if (taskId === 'auto') return '标题生成分支'
  return taskId
}

export function projectLabel(projectId?: string): string {
  return projectId || NO_PROJECT
}

export function sessionTitle(session: TurnsSessionGroup): string {
  return session.title || session.topic || session.intent || `${session.session_id.slice(0, 12)}…`
}

export function sessionTopic(session: TurnsSessionGroup): string {
  return session.topic || session.intent || ''
}

export function sessionClient(session: TurnsSessionGroup): string {
  return session.application_code || session.client_id || session.client_type || ''
}

export function relationLabel(value?: string): string {
  return ({ handoff: '轮换自', auto_title: '标题分支 ←', auto_summary: '总结分支 ←' } as Record<string, string>)[value || ''] || '父会话'
}

export function statusLabel(value: string): string {
  return ({ active: '进行中', closed: '已关闭', archived: '已归档', deleted: '已删除' } as Record<string, string>)[value] || value
}

export function shortSessionId(value?: string): string {
  return value && value.length > 14 ? `${value.slice(0, 14)}…` : value || ''
}

export function summaryMeta(session: TurnsSessionGroup): string {
  return [session.summary_model, session.summary_generated_at && new Date(session.summary_generated_at).toLocaleString()]
    .filter(Boolean)
    .join(' · ')
}

export function formatMs(value: number): string {
  if (!value || value <= 0) return '—'
  if (value < 1000) return `${value}ms`
  const seconds = Math.round(value / 1000)
  return seconds < 60 ? `${seconds}s` : `${Math.floor(seconds / 60)}m ${seconds % 60}s`
}

export function formatTokens(value: number): string {
  return value >= 1000 ? `${(value / 1000).toFixed(1)}k` : String(value)
}

export function toRFC3339(value: string): string {
  if (!value) return ''
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? '' : date.toISOString()
}

export function localDatetime(date: Date): string {
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`
}

function summarize(sessions: TurnsSessionGroup[]) {
  return {
    sessionCount: sessions.length,
    totalTurns: sessions.reduce((n, s) => n + s.total_turns, 0),
    totalTokens: sessions.reduce((n, s) => n + s.total_tokens, 0),
    totalCost: sessions.reduce((n, s) => n + s.total_cost_usd, 0),
  }
}

function wrapFlat(label: string, sessions: TurnsSessionGroup[]): TurnsDisplayGroup[] {
  return [{
    key: '__all__',
    label,
    tasks: [{ key: '__all__', label: '', sessions }],
    ...summarize(sessions),
  }]
}

function groupByKey(
  items: TurnsSessionGroup[],
  keyFn: (s: TurnsSessionGroup) => string,
  labelFn: (s: TurnsSessionGroup, key: string) => string,
): TurnsDisplayGroup[] {
  const map = new Map<string, TurnsSessionGroup[]>()
  for (const session of items) {
    const key = keyFn(session)
    const list = map.get(key) || []
    list.push(session)
    map.set(key, list)
  }
  return [...map.entries()].map(([key, sessions]) => ({
    key,
    label: labelFn(sessions[0], key),
    tasks: [{ key: '__all__', label: '', sessions }],
    ...summarize(sessions),
  }))
}

export function buildDisplayGroups(items: TurnsSessionGroup[], groupBy: TurnsGroupBy): TurnsDisplayGroup[] {
  if (groupBy === 'flat') {
    return wrapFlat('全部会话（按最近更新）', items)
  }
  if (groupBy === 'owner') {
    return groupByKey(items, s => s.owner_user || '', (_s, key) => key || NO_OWNER)
  }
  if (groupBy === 'client') {
    return groupByKey(items, s => sessionClient(s) || '', (_s, key) => key || NO_CLIENT)
  }
  if (groupBy === 'apikey') {
    return groupByKey(
      items,
      s => (s.api_key_id != null ? String(s.api_key_id) : ''),
      s => s.api_key_label || (s.api_key_id != null ? `key#${s.api_key_id}` : NO_APIKEY),
    )
  }
  const map = new Map<string, TurnsDisplayGroup>()
  for (const session of items) {
    const projectKey = session.project_id || ''
    const taskKey = session.task_id || ''
    let project = map.get(projectKey)
    if (!project) {
      project = {
        key: projectKey,
        label: projectLabel(session.project_id),
        tasks: [],
        sessionCount: 0,
        totalTurns: 0,
        totalTokens: 0,
        totalCost: 0,
      }
      map.set(projectKey, project)
    }
    let task = project.tasks.find(t => t.key === taskKey)
    if (!task) {
      task = { key: taskKey, label: taskLabel(session.task_id), sessions: [] }
      project.tasks.push(task)
    }
    task.sessions.push(session)
  }
  for (const project of map.values()) {
    Object.assign(project, summarize(project.tasks.flatMap(t => t.sessions)))
  }
  return [...map.values()]
}

export function buildTurnsParams(
  state: TurnsFilterState,
  cursor?: string,
): { params: TurnsSessionsParams | null; error: string } {
  const params: TurnsSessionsParams = { limit: 20 }
  if (cursor) params.cursor = cursor
  if (state.model.trim()) params.model = state.model.trim()
  if (state.provider.trim()) params.provider = state.provider.trim()
  if (state.statusCode.trim()) {
    const code = Number(state.statusCode)
    if (!Number.isInteger(code) || code < 100 || code > 599) {
      return { params: null, error: '状态码必须是 100 到 599 的整数' }
    }
    params.status_code = code
  }
  if (state.projectId.trim()) params.project_id = state.projectId.trim()
  if (state.taskId.trim()) params.task_id = state.taskId.trim()
  if (state.search.trim()) params.search = state.search.trim()
  if (state.tags.length) params.tags = state.tags.join(',')
  if (state.client.trim()) params.client = state.client.trim()
  if (state.ownerUser.trim()) params.owner_user = state.ownerUser.trim()
  if (state.apiKeyId.trim()) params.api_key_id = state.apiKeyId.trim()
  if (state.status.trim()) params.status = state.status.trim()
  const from = toRFC3339(state.dateFrom)
  const to = toRFC3339(state.dateTo)
  if (from) params.ts_from = from
  if (to) params.ts_to = to
  return { params, error: '' }
}

export interface FilterChip {
  key: string
  label: string
}

export function activeFilterChips(state: TurnsFilterState): FilterChip[] {
  const chips: FilterChip[] = []
  if (state.search) chips.push({ key: 'search', label: `搜索：${state.search}` })
  if (state.model) chips.push({ key: 'model', label: `模型：${state.model}` })
  if (state.timePreset && state.timePreset !== 'all') {
    chips.push({ key: 'time', label: state.timePreset === 'custom' ? '自定义时间' : `时间：${state.timePreset}` })
  }
  if (state.provider) chips.push({ key: 'provider', label: `供应商：${state.provider}` })
  if (state.statusCode) chips.push({ key: 'statusCode', label: `状态码：${state.statusCode}` })
  if (state.projectId) chips.push({ key: 'projectId', label: `项目：${state.projectId}` })
  if (state.taskId) chips.push({ key: 'taskId', label: `任务：${taskLabel(state.taskId)}` })
  if (state.client) chips.push({ key: 'client', label: `客户端：${state.client}` })
  if (state.ownerUser) chips.push({ key: 'ownerUser', label: `用户：${state.ownerUser}` })
  if (state.apiKeyId) chips.push({ key: 'apiKeyId', label: `API Key：${state.apiKeyId}` })
  if (state.status) chips.push({ key: 'status', label: `会话：${statusLabel(state.status)}` })
  if (state.tags.length) chips.push({ key: 'tags', label: `标签：${state.tags.join(',')}` })
  return chips
}

export function childOpsLabel(type: string): string {
  return ({
    title: '主题抽取',
    summary: '会话总结',
    compression: '压缩',
    sensitive_word: '敏感词',
    other: '关联操作',
  } as Record<string, string>)[type] || type
}
