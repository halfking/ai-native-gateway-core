import { describe, it, expect, vi, beforeEach } from 'vitest'
import * as api from './probeTriStateTasks'
import type { ProbeTriStateTask, ProbeTriStateResponse } from './probeTriStateTasks'

vi.mock('./client', () => ({ req: vi.fn(), ApiError: class extends Error {} }))

import { req } from './client'
const rq = vi.mocked(req)

function ok(payload: unknown): void {
  rq.mockResolvedValueOnce(payload)
}

/* ═══════════════════════════════════════════════════════════════════════════
 * 夹具：逐字照抄 ProbeTriStateTask 与 queryProbeTriStateTasks 的赋值
 * ═══════════════════════════════════════════════════════════════════════════ */

/** pending 腿（DB status='ready' ⇒ :1814-1815 改写成 pending）。 */
function pendingTask(over: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    id: 41,
    dedup_key: 'dk-41',
    credential_id: 7,
    raw_model: 'glm-5.2',
    command: 'chat',
    source: 'periodic',
    origin: 'scheduled',
    status: 'pending',
    attempt: 0,
    max_attempts: 3,
    priority: 50,
    created_at: '2026-10-07T08:00:00Z',
    updated_at: '2026-10-07T08:00:00Z',
    next_retry_at_ms: 1791369600000,
    ...over,
  }
}

/** in_flight 腿（DB status='running' ⇒ 改写成 in_flight，**不写 next_retry_at_ms**）。 */
function inFlightTask(over: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    id: 42,
    dedup_key: 'dk-42',
    credential_id: 8,
    raw_model: 'gpt-5.4',
    command: 'chat',
    source: 'admin',
    origin: 'manual',
    status: 'in_flight',
    attempt: 1,
    max_attempts: 3,
    priority: 60,
    created_at: '2026-10-07T08:01:00Z',
    updated_at: '2026-10-07T08:01:05Z',
    ...over,
  }
}

/**
 * completed 腿。`origin: 'error'` 对应 `source: 'request_failure'`（`:1745`）。
 * ★ http_status / latency_ms 演示**指针 + omitempty 保留 0**（`:1825-1832`）。
 */
function completedTask(over: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    id: 43,
    dedup_key: 'dk-43',
    credential_id: 9,
    provider_id: 3,
    provider_name: 'OpenRouter',
    provider_code: 'openrouter',
    raw_model: 'deepseek-v4-pro',
    command: 'chat',
    source: 'request_failure',
    origin: 'error',
    status: 'completed',
    outcome: 'failed',
    attempt: 3,
    max_attempts: 3,
    priority: 50,
    reason_code: 'upstream_5xx',
    http_status: 500,
    latency_ms: 1200,
    created_at: '2026-10-07T07:00:00Z',
    updated_at: '2026-10-07T07:00:09Z',
    finished_at: '2026-10-07T07:00:09Z',
    ...over,
  }
}

function response(over: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    status: 'pending',
    tasks: [pendingTask()],
    count: 1,
    ...over,
  }
}

/* ═══════════════════════════════════════════════════════════════════════════
 * 常量对齐后端字面量
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('常量对齐后端字面量', () => {
  it('★ 响应 status 是三值', () => {
    // probe_dashboard.go:1851
    expect([...api.PROBE_TASK_STATUSES]).toEqual(['pending', 'in_flight', 'completed'])
  })

  it('★ DB status 是六值（与响应三值不同构）', () => {
    // credential_probe_queue_status_check
    expect([...api.PROBE_QUEUE_DB_STATUSES]).toEqual([
      'ready', 'running', 'success', 'failed', 'expired', 'cancelled',
    ])
  })

  it('★ outcome 是四值', () => {
    expect([...api.PROBE_TASK_OUTCOMES]).toEqual([
      'success', 'failed', 'expired', 'cancelled',
    ])
  })

  it('origin 是三值', () => {
    expect([...api.PROBE_TASK_ORIGINS]).toEqual(['scheduled', 'error', 'manual'])
  })

  it('★ DB source 是四值（no_candidates 不在其中）', () => {
    expect([...api.PROBE_QUEUE_DB_SOURCES]).toEqual([
      'request_failure', 'periodic', 'external_async', 'admin',
    ])
    expect(api.PROBE_QUEUE_DB_SOURCES as readonly string[]).not.toContain('no_candidates')
  })

  it('缺省 status 与 limit', () => {
    // :1847-1848 与 :1856
    expect(api.PROBE_TASK_DEFAULT_STATUS).toBe('pending')
    expect(api.PROBE_TASK_DEFAULT_LIMIT).toBe(50)
  })

  it('limit 上界 200', () => {
    // :1755 probeCompletedWindow
    expect(api.PROBE_TASK_MAX_LIMIT).toBe(200)
  })

  it('两条 400 文案逐字对齐', () => {
    expect(api.PROBE_TASK_BAD_STATUS_MESSAGE).toBe(
      'status must be pending|in_flight|completed',
    )
    expect(api.PROBE_TASK_BAD_LIMIT_MESSAGE).toBe('limit must be 1..200')
  })

  it('503 与 500 文案', () => {
    expect(api.PROBE_DB_NOT_CONFIGURED_MESSAGE).toBe('database not configured')
    expect(api.PROBE_TASK_INTERNAL_ERROR_MESSAGE).toBe('internal server error')
  })

  it('★ 恒在十三键、条件九键', () => {
    expect(api.PROBE_TASK_ALWAYS_KEYS).toHaveLength(13)
    expect(api.PROBE_TASK_OPTIONAL_KEYS).toHaveLength(9)
    const always = new Set<string>(api.PROBE_TASK_ALWAYS_KEYS)
    expect(api.PROBE_TASK_OPTIONAL_KEYS.filter((k) => always.has(k))).toEqual([])
  })

  it('顶层三键', () => {
    expect([...api.PROBE_TRI_STATE_KEYS]).toEqual(['status', 'tasks', 'count'])
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * 解包
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('解包 · 三态队列', () => {
  it('健康载荷原样通过', () => {
    const r = api.unwrapProbeTriStateTasks(response())
    expect(r.tasks).toHaveLength(1)
    expect(r.count).toBe(1)
  })

  it('★ tasks 空数组不是 null', () => {
    // :1797 []ProbeTriStateTask{}
    const r = api.unwrapProbeTriStateTasks({ status: 'completed', tasks: [], count: 0 })
    expect(r.tasks).toEqual([])
  })

  it('顶层缺键抛错', () => {
    expect(() => api.unwrapProbeTriStateTasks({ status: 'pending', tasks: [] })).toThrow(
      /缺 1 个键（count）/,
    )
  })

  it('status 不是字符串时抛错', () => {
    expect(() => api.unwrapProbeTriStateTasks(response({ status: 1 }))).toThrow(
      /status 不是字符串/,
    )
  })

  it('★ status 是未知取值时抛错（封闭三值）', () => {
    expect(() => api.unwrapProbeTriStateTasks(response({ status: 'ready' }))).toThrow(
      /status 不是已知状态（ready）/,
    )
  })

  it('tasks 不是数组时抛错', () => {
    expect(() => api.unwrapProbeTriStateTasks(response({ tasks: null }))).toThrow(
      /tasks 不是数组/,
    )
  })

  it('count 不是数字时抛错', () => {
    expect(() => api.unwrapProbeTriStateTasks(response({ count: '1' }))).toThrow(
      /count 不是数字/,
    )
  })

  it('响应形状不是裸对象时抛错', () => {
    expect(() => api.unwrapProbeTriStateTasks(null)).toThrow(/期望裸对象，实得 null/)
    expect(() => api.unwrapProbeTriStateTasks([])).toThrow(/期望裸对象，实得 array/)
    expect(() => api.unwrapProbeTriStateTasks(undefined)).toThrow(
      /期望裸对象，实得 undefined/,
    )
  })

  it('★ 元素缺恒在键时抛错', () => {
    expect(() =>
      api.unwrapProbeTriStateTasks({ status: 'pending', tasks: [{ id: 1 }], count: 1 }),
    ).toThrow(/tasks\[0\] 缺 12 个键/)
  })

  it('元素的 status 是 DB 原值时抛错', () => {
    expect(() =>
      api.unwrapProbeTriStateTasks({
        status: 'pending',
        tasks: [pendingTask({ status: 'ready' })],
        count: 1,
      }),
    ).toThrow(/tasks\[0\] 的 status 不是已知状态（ready）/)
  })

  it('★ 元素的 origin 是未知值时抛错', () => {
    expect(() =>
      api.unwrapProbeTriStateTasks({
        status: 'pending',
        tasks: [pendingTask({ origin: 'auto' })],
        count: 1,
      }),
    ).toThrow(/tasks\[0\] 的 origin 不是已知来源（auto）/)
  })

  it('★ outcome 是未知值时抛错', () => {
    expect(() =>
      api.unwrapProbeTriStateTasks({
        status: 'completed',
        tasks: [completedTask({ outcome: 'timeout' })],
        count: 1,
      }),
    ).toThrow(/tasks\[0\] 的 outcome 不是已知结果（timeout）/)
  })

  it('★ outcome 不是字符串时也抛错', () => {
    expect(() =>
      api.unwrapProbeTriStateTasks({
        status: 'completed',
        tasks: [completedTask({ outcome: 7 })],
        count: 1,
      }),
    ).toThrow(/outcome 不是已知结果/)
  })

  it('priority 不是数字时抛错', () => {
    expect(() =>
      api.unwrapProbeTriStateTasks({
        status: 'pending',
        tasks: [pendingTask({ priority: '50' })],
        count: 1,
      }),
    ).toThrow(/tasks\[0\] 的 priority 不是数字/)
  })

  it('★ http_status 是字符串时抛错（指针键也要校类型）', () => {
    expect(() =>
      api.unwrapProbeTriStateTasks({
        status: 'completed',
        tasks: [completedTask({ http_status: '500' })],
        count: 1,
      }),
    ).toThrow(/tasks\[0\] 的 http_status 不是数字/)
  })

  it('provider_name 不是字符串时抛错', () => {
    expect(() =>
      api.unwrapProbeTriStateTasks({
        status: 'pending',
        tasks: [pendingTask({ provider_name: 3 })],
        count: 1,
      }),
    ).toThrow(/tasks\[0\] 的 provider_name 不是字符串/)
  })

  it('★ index 出现在错误消息里，可定位到具体条目', () => {
    expect(() =>
      api.unwrapProbeTriStateTasks({
        status: 'pending',
        tasks: [pendingTask(), pendingTask({ attempt: 'x' })],
        count: 2,
      }),
    ).toThrow(/tasks\[1\] 的 attempt 不是数字/)
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * 语义判定 · origin 由 source 推导
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('语义判定 · origin 由 source 推导', () => {
  it('request_failure ⇒ error', () => {
    expect(api.probeTaskOriginFromSource('request_failure')).toBe('error')
  })

  it('★ admin ⇒ manual', () => {
    expect(api.probeTaskOriginFromSource('admin')).toBe('manual')
  })

  it('external_async ⇒ manual', () => {
    expect(api.probeTaskOriginFromSource('external_async')).toBe('manual')
  })

  it('periodic ⇒ scheduled', () => {
    expect(api.probeTaskOriginFromSource('periodic')).toBe('scheduled')
  })

  it('★ 未知 source 落 default 分支 ⇒ scheduled', () => {
    expect(api.probeTaskOriginFromSource('whatever')).toBe('scheduled')
  })

  it('★ 后端给的 origin 与自算一致时不判漂移', () => {
    const t = api.unwrapProbeTriStateTasks({
      status: 'pending',
      tasks: [pendingTask({ source: 'periodic', origin: 'scheduled' })],
      count: 1,
    }).tasks[0]!
    expect(api.probeTaskOriginMismatch(t)).toBe(false)
  })

  it('★ origin 对不上时判为契约漂移', () => {
    const t = api.unwrapProbeTriStateTasks({
      status: 'pending',
      tasks: [pendingTask({ source: 'periodic', origin: 'manual' })],
      count: 1,
    }).tasks[0]!
    expect(api.probeTaskOriginMismatch(t)).toBe(true)
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * 语义判定 · status / outcome 互斥
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('语义判定 · outcome 与 status 互斥', () => {
  const one = (t: Record<string, unknown>, top = 'completed'): ProbeTriStateTask =>
    api.unwrapProbeTriStateTasks({ status: top, tasks: [t], count: 1 }).tasks[0]!

  it('★ completed 带 outcome 时一致', () => {
    expect(api.probeTaskOutcomeIsConsistent(one(completedTask({ outcome: 'success' })))).toBe(true)
  })

  it('★★ completed 缺 outcome 时不一致', () => {
    const t = one(completedTask())
    delete (t as unknown as Record<string, unknown>).outcome
    expect(api.probeTaskOutcomeIsConsistent(t)).toBe(false)
  })

  it('★★ pending 带 outcome 时不一致', () => {
    expect(
      api.probeTaskOutcomeIsConsistent(one(pendingTask({ outcome: 'success' }), 'pending')),
    ).toBe(false)
  })

  it('pending 不带 outcome 时一致', () => {
    expect(api.probeTaskOutcomeIsConsistent(one(pendingTask(), 'pending'))).toBe(true)
  })

  it('in_flight 不带 outcome 时一致', () => {
    expect(api.probeTaskOutcomeIsConsistent(one(inFlightTask(), 'in_flight'))).toBe(true)
  })
})

describe('语义判定 · next_retry_at_ms 只在 pending', () => {
  const one = (t: Record<string, unknown>, top: string): ProbeTriStateTask =>
    api.unwrapProbeTriStateTasks({ status: top, tasks: [t], count: 1 }).tasks[0]!

  it('★ pending 带退避时一致', () => {
    expect(api.probeTaskNextRetryIsConsistent(one(pendingTask(), 'pending'))).toBe(true)
  })

  it('★★ pending 缺退避时不一致', () => {
    const t = one(pendingTask(), 'pending')
    delete (t as unknown as Record<string, unknown>).next_retry_at_ms
    expect(api.probeTaskNextRetryIsConsistent(t)).toBe(false)
  })

  it('★ in_flight 不带退避时一致', () => {
    expect(api.probeTaskNextRetryIsConsistent(one(inFlightTask(), 'in_flight'))).toBe(true)
  })

  it('★★ in_flight 居然带退避时不一致', () => {
    expect(
      api.probeTaskNextRetryIsConsistent(one(inFlightTask({ next_retry_at_ms: 1 }), 'in_flight')),
    ).toBe(false)
  })

  it('pending 行取得到退避值', () => {
    expect(api.probeTaskNextRetryAtMsOrNull(one(pendingTask(), 'pending'))).toBe(1791369600000)
  })

  it('in_flight 行取不到退避值', () => {
    expect(api.probeTaskNextRetryAtMsOrNull(one(inFlightTask(), 'in_flight'))).toBeNull()
  })
})

describe('语义判定 · 指针键保留 0', () => {
  const one = (t: Record<string, unknown>): ProbeTriStateTask =>
    api.unwrapProbeTriStateTasks({ status: 'completed', tasks: [t], count: 1 }).tasks[0]!

  it('★★ http_status 为 0 时判为「测出了状态码」', () => {
    const t = one(completedTask({ http_status: 0 }))
    expect(api.probeTaskHasHttpStatus(t)).toBe(true)
    expect(t.http_status).toBe(0)
  })

  it('http_status 缺失时判为「没测出」', () => {
    const t = one(completedTask())
    delete (t as unknown as Record<string, unknown>).http_status
    expect(api.probeTaskHasHttpStatus(t)).toBe(false)
  })

  it('★★ latency_ms 为 0 时取得到 0 而不是 null', () => {
    expect(api.probeTaskLatencyMsOrNull(one(completedTask({ latency_ms: 0 })))).toBe(0)
  })

  it('latency_ms 缺失时取 null', () => {
    const t = one(completedTask())
    delete (t as unknown as Record<string, unknown>).latency_ms
    expect(api.probeTaskLatencyMsOrNull(t)).toBeNull()
  })
})

describe('语义判定 · 供应商三键', () => {
  const one = (t: Record<string, unknown>): ProbeTriStateTask =>
    api.unwrapProbeTriStateTasks({ status: 'pending', tasks: [t], count: 1 }).tasks[0]!

  it('★ 三个键全缺时判为供应商未知', () => {
    expect(api.probeTaskProviderIsUnknown(one(pendingTask()))).toBe(true)
  })

  it('★ 只有 provider_name 时不判未知', () => {
    expect(
      api.probeTaskProviderIsUnknown(one(pendingTask({ provider_name: 'OpenRouter' }))),
    ).toBe(false)
  })

  it('★ 只有 provider_id 时不判未知', () => {
    expect(api.probeTaskProviderIsUnknown(one(pendingTask({ provider_id: 3 })))).toBe(false)
  })

  it('键缺失时显示名取 null', () => {
    expect(api.probeTaskProviderNameOrNull(one(pendingTask()))).toBeNull()
  })

  it('键在时显示名原样取出', () => {
    expect(api.probeTaskProviderNameOrNull(one(pendingTask({ provider_name: 'OR' })))).toBe('OR')
  })
})

describe('语义判定 · 分页与终态', () => {
  const page = (n: number, status = 'pending'): ProbeTriStateResponse =>
    api.unwrapProbeTriStateTasks({
      status,
      tasks: Array.from({ length: n }, () => pendingTask()),
      count: n,
    })

  it('★ count 恒等于 tasks.length', () => {
    expect(page(7).count).toBe(page(7).tasks.length)
  })

  it('★ 取满 limit 时判为可能还有下一页', () => {
    expect(api.probeTaskMayHaveMore(page(50), 50)).toBe(true)
  })

  it('★ 没取满时判为已到末尾', () => {
    expect(api.probeTaskMayHaveMore(page(3), 50)).toBe(false)
  })

  it('★ 不传 limit 时按缺省 50 判断', () => {
    expect(api.probeTaskPageIsFull(page(50))).toBe(true)
    expect(api.probeTaskPageIsFull(page(49))).toBe(false)
  })

  it('重试用尽且未进终态时成立', () => {
    const t = api.unwrapProbeTriStateTasks({
      status: 'pending',
      tasks: [pendingTask({ attempt: 3, max_attempts: 3 })],
      count: 1,
    }).tasks[0]!
    expect(api.probeTaskRetriesExhausted(t)).toBe(true)
  })

  it('★ 已进终态时不算重试用尽', () => {
    const t = api.unwrapProbeTriStateTasks({
      status: 'completed',
      tasks: [completedTask({ attempt: 3, max_attempts: 3 })],
      count: 1,
    }).tasks[0]!
    expect(api.probeTaskRetriesExhausted(t)).toBe(false)
  })

  it('在途判定', () => {
    const t = api.unwrapProbeTriStateTasks({
      status: 'in_flight',
      tasks: [inFlightTask()],
      count: 1,
    }).tasks[0]!
    expect(api.probeTaskIsInFlight(t)).toBe(true)
  })

  it('终态成功判定', () => {
    const t = api.unwrapProbeTriStateTasks({
      status: 'completed',
      tasks: [completedTask({ outcome: 'success' })],
      count: 1,
    }).tasks[0]!
    expect(api.probeTaskSucceeded(t)).toBe(true)
    expect(api.probeTaskFailedOrAbandoned(t)).toBe(false)
  })

  it('终态失败判定', () => {
    const t = api.unwrapProbeTriStateTasks({
      status: 'completed',
      tasks: [completedTask({ outcome: 'expired' })],
      count: 1,
    }).tasks[0]!
    expect(api.probeTaskSucceeded(t)).toBe(false)
    expect(api.probeTaskFailedOrAbandoned(t)).toBe(true)
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * 取数
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('取数', () => {
  beforeEach(() => {
    rq.mockReset()
  })

  it('★ 不传参数时打裸路径（后端补 status=pending、limit=50）', async () => {
    ok(response())
    await api.fetchProbeTriStateTasks()
    expect(rq.mock.calls[0]![0]).toBe('GET')
    expect(rq.mock.calls[0]![1]).toBe('/api/admin/probe/tasks')
  })

  it('★ status 与 limit 按序拼进 query', async () => {
    ok(response())
    await api.fetchProbeTriStateTasks('completed', 20)
    expect(rq.mock.calls[0]![1]).toBe('/api/admin/probe/tasks?status=completed&limit=20')
  })

  it('★ 只传 status 时只带 status', async () => {
    ok(response({ status: 'in_flight', tasks: [] }))
    await api.fetchProbeTriStateTasks('in_flight')
    expect(rq.mock.calls[0]![1]).toBe('/api/admin/probe/tasks?status=in_flight')
  })

  it('★ 空串 status 也要发（后端会 400，不是回落）', async () => {
    ok(response())
    await api.fetchProbeTriStateTasks('')
    expect(rq.mock.calls[0]![1]).toBe('/api/admin/probe/tasks?status=')
  })

  it('★ limit 为 0 也要发（后端会 400，不是回落成 50）', async () => {
    ok(response())
    await api.fetchProbeTriStateTasks('pending', 0)
    expect(rq.mock.calls[0]![1]).toBe('/api/admin/probe/tasks?status=pending&limit=0')
  })

  it('★ 会拒绝 DB 原值形态的 status', async () => {
    ok(response({ status: 'ready' }))
    await expect(api.fetchProbeTriStateTasks('ready')).rejects.toThrow(
      /status 不是已知状态（ready）/,
    )
  })

  it('★ 会拒绝元素级形状错误', async () => {
    ok(response({ tasks: [pendingTask({ latency_ms: 'x' })] }))
    await expect(api.fetchProbeTriStateTasks()).rejects.toThrow(/latency_ms 不是数字/)
  })

  it('★ 会拒绝 tasks 为 null', async () => {
    ok(response({ tasks: null }))
    await expect(api.fetchProbeTriStateTasks()).rejects.toThrow(/tasks 不是数组/)
  })
})