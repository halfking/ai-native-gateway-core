import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  fetchProbeQueueTasks,
  fetchProviderLatency,
  taskStatusTone,
  taskLabel,
  PROBE_TASKS_MAX_LIMIT,
  type ProbeQueueTasksResponse,
  type ProviderLatencyResponse,
} from './probeOps'

/**
 * 探测面契约测试（2026-10-07）。
 * 重点：queue-tasks 的 limit 越界是**静默保持 100**（本仓库第 4 种越界语义）、
 *       provider-latency **不接受任何参数**、未知任务状态不得显示成 success。
 */

const fetchMock = vi.fn()
beforeEach(() => {
  fetchMock.mockReset()
  vi.stubGlobal('fetch', fetchMock)
})
afterEach(() => {
  vi.unstubAllGlobals()
})

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } })
}

const TASKS: ProbeQueueTasksResponse = { tasks: [], total: 0 }
const LAT: ProviderLatencyResponse = { entries: [], total: 0 }

describe('probe queue-tasks 的 limit 越界是「静默保持默认 100」', () => {
  it('正常值照发', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(TASKS))
    await fetchProbeQueueTasks({ limit: 50 })
    expect(String(fetchMock.mock.calls[0]![0])).toContain('limit=50')
  })

  // 后端是 `if n > 0 && n <= 200 { limit = n }` —— 不满足时**不赋值**，
  // 于是 limit 停在 100。不是 400、也不是 clamp 到 200。
  it('超 200 被前端夹到 200（后端本会静默保持 100）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(TASKS))
    await fetchProbeQueueTasks({ limit: 9999 })
    expect(String(fetchMock.mock.calls[0]![0])).toContain(`limit=${PROBE_TASKS_MAX_LIMIT}`)
  })

  it('limit=0 不发（发了也是白费，后端照样回落 100）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(TASKS))
    await fetchProbeQueueTasks({ limit: 0 })
    expect(String(fetchMock.mock.calls[0]![0])).not.toContain('limit=')
  })

  it('URL 路径不带多余参数', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(TASKS))
    await fetchProbeQueueTasks()
    const url = String(fetchMock.mock.calls[0]![0])
    expect(url.startsWith('/api/admin/probe/queue-tasks')).toBe(true)
  })
})

describe('provider-latency 不接受任何参数', () => {
  // ★ 后端写死 `direct_ok = TRUE AND direct_latency_ms > 0`、
  //   `now() - 1 hour` 窗口、`LIMIT 500`，没有任何 Query 读取。
  //   写成一个无参函数，让「想传参」的冲动在类型层面被挡掉 ——
  //   发出一个被忽略的查询串会让用户以为过滤生效了。
  it('URL 上不带任何查询串', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(LAT))
    await fetchProviderLatency()
    const url = String(fetchMock.mock.calls[0]![0])
    expect(url).toBe('/api/admin/probe/provider-latency')
    expect(url).not.toContain('?')
  })
})

describe('taskStatusTone —— 未知状态不得显示成 success', () => {
  it('已知状态映射正确', () => {
    expect(taskStatusTone('done')).toBe('success')
    expect(taskStatusTone('success')).toBe('success')
    expect(taskStatusTone('failed')).toBe('danger')
    expect(taskStatusTone('running')).toBe('warning')
    expect(taskStatusTone('retry')).toBe('warning')
    expect(taskStatusTone('pending')).toBe('muted')
  })

  it('大小写不敏感', () => {
    expect(taskStatusTone('DONE')).toBe('success')
    expect(taskStatusTone('Failed')).toBe('danger')
  })

  // ★ 后端这个结构体上**没有**状态词表（对比 heatmap 的 node_status 有），
  //   所以值可能是我们没见过的。查表写法天然是 `?? 'success'`，
  //   默认 success 会把「看不懂的状态」显示成正常。
  it('★ 词表外状态 ⇒ muted（不是 success）', () => {
    expect(taskStatusTone('weird_new_state')).toBe('muted')
    expect(taskStatusTone('')).toBe('muted')
    expect(taskStatusTone(null)).toBe('muted')
    expect(taskStatusTone(undefined)).toBe('muted')
  })
})

describe('taskLabel —— 逐级回落，不产出空白或 undefined', () => {
  it('优先 standardized_name', () => {
    expect(taskLabel({ standardized_name: 'claude-sonnet-4-6', raw_model: 'claude-sonnet-4.6', credential_id: 3 })).toBe(
      'claude-sonnet-4-6',
    )
  })
  it('退回 raw_model', () => {
    expect(taskLabel({ standardized_name: '', raw_model: 'gpt-4o', credential_id: 3 })).toBe('gpt-4o')
  })
  // 反向锁定：全空时不能渲染成空 div 或字面量 undefined
  it('两者都空时回落到 #credential_id', () => {
    expect(taskLabel({ standardized_name: '  ', raw_model: '', credential_id: 7 })).toBe('#7')
  })
})
