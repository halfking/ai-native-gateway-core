import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  isDegraded,
  isIngress,
  classifyJourneyDetail,
  fetchJourneyQueues,
  fetchJourneyDetail,
  type JourneyDetailResponse,
  type RequestSnapshot,
  type IngressSnapshot,
} from './requestJourney'

/**
 * requestJourney 契约测试（2026-10-07）。
 *
 * 这个端点族的坑全部是**静默**的：写错了不报错，只是把「看不到」显示成
 * 「没有」。所以判据不测「函数返回了什么」，测**降级与空是否被分开**。
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
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'content-type': 'application/json' },
  })
}

describe('isDegraded —— 降级字面值是 observation_degraded，不是 degraded', () => {
  it('observation_degraded 判为降级', () => {
    expect(isDegraded({ observation_status: 'observation_degraded' })).toBe(true)
  })

  // ★ 这条是本模块最贵的坑：仓库其它面（usage / board）用的是裸 `degraded`，
  //   照抄过来会**永远为 false**，降级被显示成「真的没有请求」。
  it('裸 degraded 不算降级 —— 那是本仓库其它面的字段名，用在这里恒为假', () => {
    expect(isDegraded({ observation_status: 'degraded' } as never)).toBe(false)
  })

  it('正常状态与缺字段都不算降级', () => {
    expect(isDegraded({ observation_status: 'healthy' })).toBe(false)
    expect(isDegraded({ observation_status: '' })).toBe(false)
    expect(isDegraded(null)).toBe(false)
    expect(isDegraded(undefined)).toBe(false)
  })
})

describe('isIngress —— scope=all 时快照元素换成瘦类型', () => {
  const rich: RequestSnapshot = {
    request_id: 'r1',
    current_stage: 'model',
    updated_at: '2026-10-07T00:00:00Z',
  }
  const thin: IngressSnapshot = {
    request_id: 'r2',
    gateway_instance_id: 'gw-1',
    protocol: 'anthropic',
    path_class: 'messages',
    arrived_at: '2026-10-07T00:00:00Z',
    updated_at: '2026-10-07T00:01:00Z',
    status: 'in_flight',
  }

  it('自带 protocol 的是 Ingress（瘦）', () => {
    expect(isIngress(thin)).toBe(true)
  })
  it('只有 current_stage 的是 RequestSnapshot（胖）', () => {
    expect(isIngress(rich)).toBe(false)
  })
})

describe('classifyJourneyDetail —— 三态不能二元化', () => {
  const JOURNEY = {
    tenant_id: 'default',
    gateway_instance_id: 'gw-1',
    request_id: 'r1',
    observation_status: 'healthy',
    started_at: '2026-10-07T00:00:00Z',
    updated_at: '2026-10-07T00:01:00Z',
    events: [],
  }

  it('有 journey ⇒ ok', () => {
    const r: JourneyDetailResponse = { observation_status: 'healthy', journey: JOURNEY }
    const s = classifyJourneyDetail(r)
    expect(s.kind).toBe('ok')
    if (s.kind === 'ok') {
      expect(s.journey.request_id).toBe('r1')
      expect(s.degraded).toBe(false)
      expect(s.divergence).toEqual([])
    }
  })

  // ★★ request_journey.go:265-268：只有 journey==nil **且**非降级才 404。
  //   ⇒ 降级且无数据时后端给的是 200 + 没有 journey。若判成 not_found，
  //   UI 会告诉用户「这个请求不存在」—— 那是一个我们没有依据的结论。
  it('降级 + 无 journey ⇒ observation_degraded，不是 not_found', () => {
    const r: JourneyDetailResponse = { observation_status: 'observation_degraded' }
    const s = classifyJourneyDetail(r)
    expect(s.kind).toBe('observation_degraded')
  })

  it('非降级 + 无 journey ⇒ not_found（后端契约被打破时的兜底）', () => {
    const s = classifyJourneyDetail({ observation_status: 'healthy' })
    expect(s.kind).toBe('not_found')
  })

  it('有 journey 但观测面降级 ⇒ 仍是 ok，degraded=true（数据来自部分源）', () => {
    const r: JourneyDetailResponse = {
      observation_status: 'observation_degraded',
      journey: { ...JOURNEY, observation_status: 'observation_degraded' },
    }
    const s = classifyJourneyDetail(r)
    expect(s.kind).toBe('ok')
    if (s.kind === 'ok') expect(s.degraded).toBe(true)
  })

  it('divergence 非数组时归一为空数组（不把 undefined 传进模板）', () => {
    const r: JourneyDetailResponse = { observation_status: 'healthy', journey: JOURNEY, divergence: null as never }
    const s = classifyJourneyDetail(r)
    if (s.kind === 'ok') expect(s.divergence).toEqual([])
  })
})

describe('URL 构造', () => {
  it('queues 带上 view / lifecycle_state', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ view: 'total', observation_status: 'healthy' }))
    await fetchJourneyQueues({ view: 'models', lifecycle_state: 'in_flight' })
    const url = String(fetchMock.mock.calls[0]![0])
    expect(url).toContain('/api/admin/request-journeys/queues?')
    expect(url).toContain('view=models')
    expect(url).toContain('lifecycle_state=in_flight')
  })

  it('detail 的 request_id 必须转义（后端对含 / 的 id 返回 400）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ observation_status: 'healthy', journey: null }))
    await fetchJourneyDetail('req/../admin')
    const url = String(fetchMock.mock.calls[0]![0])
    expect(url).toContain('/api/admin/request-journeys/req%2F..%2Fadmin')
    expect(url).not.toContain('req/../admin')
  })
})
