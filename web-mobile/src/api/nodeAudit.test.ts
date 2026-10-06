import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { fetchNodeAudit, unwrapNodeAudit, NODE_AUDIT_MAX_LIMIT, type NodeAuditEntry } from './nodeAudit'

/**
 * nodeAudit API 层（2026-10-06）。
 *
 * 测 limit 夹取必须**观察实际发出的 URL**，不能只断言函数被调用过 ——
 * 「调用了 fetchNodeAudit(limit=99999)」不等于「query 里是 200」。
 * 后端 audit_operations.go:95-103 的行为是：<1 或非数字直接 400，
 * >200 静默 clamp。所以前端夹取的价值是**不让非法值出门**。
 */

describe('fetchNodeAudit 的 limit 夹取', () => {
  const fetchMock = vi.fn()

  beforeEach(() => {
    fetchMock.mockReset()
    fetchMock.mockResolvedValue({
      ok: true,
      status: 200,
      statusText: 'OK',
      text: async () => JSON.stringify({ entries: [], count: 0, limit: 50 }),
    })
    vi.stubGlobal('fetch', fetchMock)
    vi.stubGlobal('window', { location: { pathname: '/m/nodes' } })
  })
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  function requestedUrl(): string {
    return String(fetchMock.mock.calls[0]?.[0] ?? '')
  }

  it('超上限被夹到 200（后端 maxAuditOperationLimit，:57）', async () => {
    await fetchNodeAudit({ limit: 99999 })
    expect(requestedUrl()).toContain('limit=200')
    expect(NODE_AUDIT_MAX_LIMIT).toBe(200)
  })

  it('小数被截断（后端 Atoi 对 "30.5" 会 400）', async () => {
    await fetchNodeAudit({ limit: 30.9 })
    expect(requestedUrl()).toContain('limit=30')
    expect(requestedUrl()).not.toContain('30.5')
  })

  it('limit < 1 时**不发** limit 参数（后端 :96-99 对 <1 直接 400）', async () => {
    await fetchNodeAudit({ limit: 0 })
    expect(requestedUrl()).not.toContain('limit=')
    await fetchNodeAudit({ limit: -5 })
    expect(requestedUrl()).not.toContain('limit=')
  })

  it('NaN / Infinity 都不发 limit（Number.isFinite 拦住，两者都会让后端 400 或无意义）', async () => {
    await fetchNodeAudit({ limit: Number.NaN })
    expect(requestedUrl()).not.toContain('limit=')
    // ★ 第一版我在这里写了「Infinity 应被夹到 200」——**那个预期是错的**：
    //   Math.trunc(Infinity) 仍是 Infinity，而守卫是 Number.isFinite(…)
    //   ⇒ Infinity 根本进不了夹取分支，limit 不发。
    //   正确行为就是「不发」：后端拿不到 limit 就用默认 50，而不是收一个无意义的数。
    await fetchNodeAudit({ limit: Number.POSITIVE_INFINITY })
    expect(requestedUrl()).not.toContain('limit=')
  })

  it('合法值原样透传', async () => {
    await fetchNodeAudit({ limit: 30, provider_id: 5 })
    const url = requestedUrl()
    expect(url).toContain('limit=30')
    expect(url).toContain('provider_id=5')
  })
})

describe('unwrapNodeAudit', () => {
  const E: NodeAuditEntry = {
    request_id: 'r1', provider_id: 5, operation: 'enable_toggle', created_at: '2026-10-06T13:00:00Z',
  }

  it('信封形态（后端 :229-233 真实返回）', () => {
    const out = unwrapNodeAudit({ entries: [E], count: 1, limit: 50 })
    expect(out.entries).toHaveLength(1)
    expect(out.limit).toBe(50)
  })

  it('裸数组容忍并补 count/limit', () => {
    const out = unwrapNodeAudit([E])
    expect(out.entries).toHaveLength(1)
    expect(out.count).toBe(1)
  })

  it.each([
    ['null', null],
    ['字符串', 'oops'],
    ['空对象', {}],
    ['entries 不是数组', { entries: null, count: 0, limit: 50 }],
  ])('%s 必须抛错，不能静默返空', (_label, bad) => {
    // ★ 反向判据：改成返空后本用例会红。那样「后端改了返回结构」
    //   会被显示成「该供应商没有操作记录」——一次契约漂移被读成「没人动过」。
    expect(() => unwrapNodeAudit(bad as never)).toThrow(/形状不符/)
  })
})
