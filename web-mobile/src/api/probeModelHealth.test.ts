import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  fetchProbeDashboard,
  fetchProbeNodeTasks,
  derivedStatsAbsent,
  realSuccessRate24hOf,
  realRequests24hOf,
  healthTone,
  healthKeyOf,
  nodeStatusTone,
  nodeStatusKeyOf,
  latencyOf,
  needsManualAction,
  NODE_TASKS_DEFAULT_LIMIT,
  QUEUE_TASKS_DEFAULT_LIMIT,
  PROBE_TASKS_MAX_LIMIT,
  type ModelHealthSummary,
  type NodeProbeTaskRow,
} from './probeModelHealth'

/**
 * 探测模型级端点的契约测试（2026-10-07）。
 *
 * 重点是**后端已经把 NULL 压成 0** 这件事：客户端拿不到区分依据，
 * 唯一可推导的判据是 `total_credentials === 0`。
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
function lastUrl(): string {
  return decodeURIComponent(String(fetchMock.mock.calls[0]![0]))
}

const MODEL: ModelHealthSummary = {
  provider_model_id: 1,
  raw_model_name: 'gpt-4o',
  outbound_model_name: 'gpt-4o-2024-11',
  protocol: 'openai',
  provider_name: 'OpenAI',
  total_credentials: 12,
  healthy_count: 10,
  suspicious_count: 1,
  failing_count: 1,
  probing_count: 0,
  healthy_percentage: 83.3,
  failing_percentage: 8.3,
  urgent_count: 0,
  suspicious_priority_count: 1,
  failing_priority_count: 1,
  watchdog_count: 0,
  avg_success_rate_7d: 0.97,
  avg_verification_hours: 6.5,
  avg_consecutive_successes: 12,
  total_real_success_24h: 300,
  total_real_failure_24h: 5,
  real_success_rate_24h: 0.9836,
  last_verified_at: '2026-10-07T10:00:00Z',
  last_real_request_at: '2026-10-07T09:50:00Z',
  next_probe_at: '2026-10-07T11:00:00Z',
  critical_nodes: 0,
  pending_probes_5min: 0,
  overall_health: 'healthy',
}

const NODE_TASK: NodeProbeTaskRow = {
  credential_id: 7,
  provider_id: 1,
  provider_name: 'OpenAI',
  provider_code: 'openai',
  raw_model: 'gpt-4o',
  standardized_name: 'gpt-4o',
  status: 'paused',
  attempt: 5,
  consecutive_failures: 5,
  last_direct_ok: false,
  last_gateway_ok: null,
  last_err_code: 'timeout',
  last_latency_ms: 1200,
  paused: true,
  updated_at: '2026-10-07T10:00:00Z',
  source: 'node_probe',
}

describe('dashboard：model 是子串匹配（ILIKE），不是精确', () => {
  it('不发 model ⇒ 完全不发（后端不过滤）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ models: [], total: 0 }))
    await fetchProbeDashboard()
    expect(lastUrl()).not.toContain('model=')
  })

  it('★ 发 model 即照发，但语义是子串（传 gpt-4 会命中 gpt-4o-mini）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ models: [MODEL], total: 1 }))
    await fetchProbeDashboard({ model: 'gpt-4' })
    expect(lastUrl()).toContain('model=gpt-4')
  })
})

describe('★★ 判据：derivedStatsAbsent —— 唯一可推导的「没数据」', () => {
  // ★ 后端 nullFloat64 对 SQL NULL 返回 0（probe_dashboard.go:2336-2341），
  //   而 JSON 字段是普通 float64 ⇒ 「0%」和「没数据」在响应里同形。
  it('★ total_credentials=0 ⇒ 派生统计必然是「没数据」', () => {
    expect(derivedStatsAbsent({ total_credentials: 0 })).toBe(true)
  })

  // ★ 「0 个里的 0%」在数学上不成立 —— 这是唯一的可证情形
  it('★ 附：total_credentials=0 时 healthy_percentage=0 不是「0% 健康」', () => {
    const m = { ...MODEL, total_credentials: 0, healthy_count: 0, healthy_percentage: 0 }
    expect(derivedStatsAbsent(m)).toBe(true)
  })

  it('total_credentials>0 ⇒ 不判为缺失（0 是真值的可能性存在）', () => {
    expect(derivedStatsAbsent({ total_credentials: 12 })).toBe(false)
  })

  it('m 缺失 ⇒ 判为缺失', () => {
    expect(derivedStatsAbsent(null)).toBe(true)
    expect(derivedStatsAbsent(undefined)).toBe(true)
  })
})

describe('★ real_success_rate_24h 三态', () => {
  it('有值 ⇒ real', () => {
    const r = realSuccessRate24hOf(MODEL)
    expect(r.kind).toBe('real')
    if (r.kind === 'real') expect(r.value).toBeCloseTo(0.9836)
  })

  // ★ Go 侧 `*float64` + omitempty ⇒ 字段整个不存在
  it('★ 字段缺失 ⇒ no_requests，不是 0%', () => {
    const m = { ...MODEL }
    delete (m as Record<string, unknown>).real_success_rate_24h
    expect(realSuccessRate24hOf(m).kind).toBe('no_requests')
  })

  it('★ 真的是 0（跑了且全失败）⇒ real 0', () => {
    const r = realSuccessRate24hOf({ ...MODEL, real_success_rate_24h: 0 })
    expect(r.kind).toBe('real')
    if (r.kind === 'real') expect(r.value).toBe(0)
  })
})

describe('★ realRequests24hOf —— 总量 0 有歧义', () => {
  it('有请求 ⇒ 返回总量', () => {
    expect(realRequests24hOf(MODEL)).toBe(305)
  })

  // ★ 两个字段都是 plain int（NULL→0）⇒ 全 0 分不清「没有请求」与「NULL」
  it('★ 总量 0 ⇒ 返回 null 而不是 0', () => {
    expect(realRequests24hOf({ ...MODEL, total_real_success_24h: 0, total_real_failure_24h: 0 })).toBeNull()
  })
})

describe('overall_health 配色与文案：词表外一律不乐观', () => {
  it('已知状态', () => {
    expect(healthTone('critical')).toBe('danger')
    expect(healthTone('warning')).toBe('warning')
    expect(healthTone('degraded')).toBe('warning')
    expect(healthTone('healthy')).toBe('success')
    expect(healthTone('unknown')).toBe('muted')
  })

  // ★ 默认给 success 会把「看不懂」显示成「健康」
  it('★ 词表外 ⇒ muted（不是 success）', () => {
    expect(healthTone('excellent')).toBe('muted')
    expect(healthTone(null)).toBe('muted')
    expect(healthKeyOf('excellent')).toBe('probeModel.health.unknown')
  })

  it('大小写不敏感', () => {
    expect(healthTone('HEALTHY')).toBe('success')
  })
})

describe('node-tasks：★ 第六种越界语义（静默回落，且两端点默认值不同）', () => {
  it('两个姊妹端点的后端默认值**不同**（100 vs 120）', () => {
    // 锁住「别把两个端点的默认值当成同一个数」
    expect(QUEUE_TASKS_DEFAULT_LIMIT).toBe(100)
    expect(NODE_TASKS_DEFAULT_LIMIT).toBe(120)
  })

  it('共用上限 200', () => {
    expect(PROBE_TASKS_MAX_LIMIT).toBe(200)
  })

  it('不传 limit ⇒ 完全不发（用后端各自的默认）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ tasks: [], total: 0 }))
    await fetchProbeNodeTasks()
    expect(lastUrl()).not.toContain('limit=')
  })

  // ★ 越界后端**静默回落**（不是 400）⇒ 前端必须自己夹，
  //   否则用户以为看到 999 条，实际是 120 条。
  it('★ 越界夹到 200', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ tasks: [], total: 0 }))
    await fetchProbeNodeTasks({ limit: 99999 })
    expect(lastUrl()).toContain('limit=200')
  })

  it('limit=0 / 负数 不发（后端会回落默认值）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ tasks: [], total: 0 }))
    await fetchProbeNodeTasks({ limit: 0 })
    expect(lastUrl()).not.toContain('limit=')
  })

  it('合法值照发', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ tasks: [], total: 0 }))
    await fetchProbeNodeTasks({ limit: 50 })
    expect(lastUrl()).toContain('limit=50')
  })
})

describe('node-tasks 状态与延时的三态', () => {
  it('已知状态配色', () => {
    expect(nodeStatusTone('paused')).toBe('danger')
    expect(nodeStatusTone('running')).toBe('warning')
    expect(nodeStatusTone('pending')).toBe('muted')
  })

  it('★ 词表外 ⇒ muted', () => {
    expect(nodeStatusTone('weird')).toBe('muted')
    expect(nodeStatusKeyOf('weird')).toBe('probeModel.nodeStatus.pending')
  })

  // ★ `last_latency_ms` 是 `*int` + omitempty
  it('★ 延时缺失 ⇒ null（不是 0ms）', () => {
    expect(latencyOf({ ...NODE_TASK, last_latency_ms: undefined })).toBeNull()
    expect(latencyOf({ ...NODE_TASK, last_latency_ms: null })).toBeNull()
  })

  it('★ 延时真的是 0 ⇒ 返回 0（与缺失区分开）', () => {
    expect(latencyOf({ ...NODE_TASK, last_latency_ms: 0 })).toBe(0)
  })
})

describe('★ needsManualAction —— 「等一等」与「等再久也不会好」', () => {
  it('paused ⇒ 需要人工处理（已达重试上限，不会自愈）', () => {
    expect(needsManualAction(NODE_TASK)).toBe(true)
  })

  it('pending / running ⇒ 不用（会自己往前走）', () => {
    expect(needsManualAction({ ...NODE_TASK, paused: false, status: 'pending' })).toBe(false)
    expect(needsManualAction({ ...NODE_TASK, paused: false, status: 'running' })).toBe(false)
  })

  it('★ status=paused 但 paused 字段为 false ⇒ 仍按 paused 字段判（后端两处都有）', () => {
    // 后端 SQL 的 status CASE 与 paused 列是同源的，但客户端只信 paused 字段
    expect(needsManualAction({ ...NODE_TASK, status: 'paused', paused: false })).toBe(false)
  })
})
