import { describe, expect, it } from 'vitest'
import type { TurnsSessionGroup } from '../../api/turns'
import {
  activeFilterChips,
  buildDisplayGroups,
  buildTurnsParams,
  NO_APIKEY,
  NO_OWNER,
  NO_TASK,
  taskLabel,
  type TurnsFilterState,
} from './turnsListHelpers'

function session(over: Partial<TurnsSessionGroup> = {}): TurnsSessionGroup {
  return {
    session_id: 'sess-1',
    tenant_id: 't1',
    status: 'active',
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-02T00:00:00Z',
    total_turns: 1,
    total_tokens: 100,
    total_cost_usd: 0.01,
    models_used: [],
    failover_count: 0,
    error_count: 0,
    duration_ms: 1000,
    compression: { applied_count: 0, tokens_saved: 0, strategies: [] },
    turns: [],
    user_tags: [],
    ...over,
  }
}

describe('taskLabel', () => {
  it('maps auto-summary prefix to 总结生成分支', () => {
    expect(taskLabel('auto-summary:abc')).toBe('总结生成分支')
  })

  it('maps auto to 标题生成分支', () => {
    expect(taskLabel('auto')).toBe('标题生成分支')
  })

  it('returns task id or NO_TASK', () => {
    expect(taskLabel('my-task')).toBe('my-task')
    expect(taskLabel()).toBe(NO_TASK)
  })
})

describe('buildDisplayGroups', () => {
  const items = [
    session({ session_id: 'a', project_id: 'p1', task_id: 't1', owner_user: 'u1', api_key_id: 1, api_key_label: 'key-a' }),
    session({ session_id: 'b', project_id: 'p1', task_id: 't2', owner_user: 'u2', api_key_id: 2, api_key_label: 'key-b' }),
    session({ session_id: 'c', owner_user: '', api_key_id: undefined }),
  ]

  it('flat mode wraps all sessions', () => {
    const groups = buildDisplayGroups(items, 'flat')
    expect(groups).toHaveLength(1)
    expect(groups[0].tasks[0].sessions).toHaveLength(3)
  })

  it('project mode nests by project and task', () => {
    const groups = buildDisplayGroups(items, 'project')
    const p1 = groups.find(g => g.key === 'p1')
    expect(p1).toBeTruthy()
    expect(p1!.tasks.length).toBeGreaterThanOrEqual(2)
  })

  it('owner mode groups by owner_user', () => {
    const groups = buildDisplayGroups(items, 'owner')
    expect(groups.find(g => g.key === 'u1')?.label).toBe('u1')
    expect(groups.find(g => g.key === '')?.label).toBe(NO_OWNER)
  })

  it('apikey mode groups by api_key_id', () => {
    const groups = buildDisplayGroups(items, 'apikey')
    expect(groups.find(g => g.key === '1')?.label).toBe('key-a')
    expect(groups.find(g => g.key === '')?.label).toBe(NO_APIKEY)
  })
})

describe('buildTurnsParams', () => {
  it('includes api_key_id when set', () => {
    const state: TurnsFilterState = {
      search: '', model: '', provider: '', statusCode: '',
      projectId: '', taskId: '', client: '', ownerUser: '',
      apiKeyId: '42', status: 'active', tags: [],
      dateFrom: '', dateTo: '', timePreset: 'all',
    }
    const { params, error } = buildTurnsParams(state)
    expect(error).toBe('')
    expect(params?.api_key_id).toBe('42')
    expect(params?.status).toBe('active')
  })

  it('rejects invalid status code', () => {
    const state: TurnsFilterState = {
      search: '', model: '', provider: '', statusCode: '999',
      projectId: '', taskId: '', client: '', ownerUser: '',
      apiKeyId: '', status: '', tags: [],
      dateFrom: '', dateTo: '', timePreset: 'all',
    }
    const { params, error } = buildTurnsParams(state)
    expect(params).toBeNull()
    expect(error).toContain('状态码')
  })
})

describe('activeFilterChips', () => {
  it('builds chips for active filters including api_key', () => {
    const chips = activeFilterChips({
      search: 'hello', model: 'gpt', provider: '', statusCode: '',
      projectId: '', taskId: 'auto', client: '', ownerUser: '',
      apiKeyId: '7', status: 'closed', tags: ['a'],
      dateFrom: '', dateTo: '', timePreset: 'all',
    })
    expect(chips.some(c => c.key === 'apiKeyId' && c.label.includes('7'))).toBe(true)
    expect(chips.some(c => c.key === 'taskId' && c.label.includes('标题生成分支'))).toBe(true)
    expect(chips.some(c => c.key === 'status' && c.label.includes('已关闭'))).toBe(true)
  })
})
