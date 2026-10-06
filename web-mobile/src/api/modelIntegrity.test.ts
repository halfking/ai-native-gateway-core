import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  fetchModelIntegrityEvents,
  fetchModelIntegritySummary,
  resolveModelIntegrityEvent,
  severityTone,
  MODEL_INTEGRITY_MAX_LIMIT,
} from './modelIntegrity'

/**
 * modelIntegrity API 层（2026-10-06）。
 *
 * 判据一律**观察实际 URL**，不只看「函数被调用过」——
 * 「调用了 fetchModelIntegrityEvents(limit=99999)」不等于「query 里是 500」。
 * 后端 model_integrity.go:94-100 的行为是：<1 回落默认 100（**不** 400），
 * >500 静默 clamp。所以前端夹取的价值是「不发无意义/非法的值」。
 */

describe('fetchModelIntegrityEvents 参数夹取', () => {
  const fetchMock = vi.fn()
  beforeEach(() => {
    fetchMock.mockReset()
    fetchMock.mockResolvedValue({
      ok: true, status: 200, statusText: 'OK',
      text: async () => JSON.stringify({ events: [], count: 0, limit: 20, offset: 0 }),
    })
    vi.stubGlobal('fetch', fetchMock)
    vi.stubGlobal('window', { location: { pathname: '/m/integrity' } })
  })
  afterEach(() => vi.unstubAllGlobals())

  const url = () => String(fetchMock.mock.calls[0]?.[0] ?? '')

  it('上限与后端一致（500）', () => {
    expect(MODEL_INTEGRITY_MAX_LIMIT).toBe(500)
  })

  it('超上限夹到 500', async () => {
    await fetchModelIntegrityEvents({ limit: 99999 })
    expect(url()).toContain('limit=500')
  })

  it('小数截断为整数（后端 queryInt/Atoi 对 "20.5" 不会命中该值）', async () => {
    await fetchModelIntegrityEvents({ limit: 20.7 })
    expect(url()).toContain('limit=20')
  })

  it('limit < 1 不发（后端 :95-97 回落默认 100，不是报错）', async () => {
    await fetchModelIntegrityEvents({ limit: 0 })
    expect(url()).not.toContain('limit=')
  })

  it('offset < 0 不发（后端 :102-104 归 0）', async () => {
    await fetchModelIntegrityEvents({ offset: -3 })
    expect(url()).not.toContain('offset=')
    fetchMock.mockClear()
    await fetchModelIntegrityEvents({ offset: 0 })
    // offset=0 是合法值，但 `if (params.offset != null)` 判定后 Math.trunc(0)=0
    // ⇒ 0 >= 0 成立，会发出 offset=0。后端把 0 归 0，语义一致。
    expect(url()).toContain('offset=0')
  })

  it('过滤器按 camelCase → snake_case 正确落 query', async () => {
    await fetchModelIntegrityEvents({
      provider: 'anthropic', model: 'claude-sonnet-4-6',
      anomaly_type: 'fingerprint_drift', severity: 'high', unresolved_only: true,
    })
    const u = url()
    expect(u).toContain('provider=anthropic')
    expect(u).toContain('model=claude-sonnet-4-6')
    expect(u).toContain('anomaly_type=fingerprint_drift')
    expect(u).toContain('severity=high')
    expect(u).toContain('unresolved_only=true')
  })

  it('falsy 值不进 query（空串/undefined 不该产生裸参数）', async () => {
    await fetchModelIntegrityEvents({ provider: '', model: undefined, unresolved_only: false })
    const u = url()
    expect(u).not.toContain('provider=')
    expect(u).not.toContain('model=')
    expect(u).not.toContain('unresolved_only=')
  })
})

describe('fetchModelIntegritySummary', () => {
  const fetchMock = vi.fn()
  beforeEach(() => {
    fetchMock.mockReset()
    fetchMock.mockResolvedValue({
      ok: true, status: 200, statusText: 'OK',
      text: async () => JSON.stringify({ summaries: [], count: 0, hours: 24 }),
    })
    vi.stubGlobal('fetch', fetchMock)
    vi.stubGlobal('window', { location: { pathname: '/m/integrity' } })
  })
  afterEach(() => vi.unstubAllGlobals())

  it('默认 hours=24', async () => {
    await fetchModelIntegritySummary()
    expect(String(fetchMock.mock.calls[0]?.[0])).toContain('hours=24')
  })

  it('hours 非法回落 24（不发负数/NaN）', async () => {
    fetchMock.mockClear()
    await fetchModelIntegritySummary(Number.NaN)
    expect(String(fetchMock.mock.calls[0]?.[0])).toContain('hours=24')
    fetchMock.mockClear()
    await fetchModelIntegritySummary(0)
    expect(String(fetchMock.mock.calls[0]?.[0])).toContain('hours=24')
  })
})

describe('resolveModelIntegrityEvent', () => {
  it('非法 id 直接 reject，不发请求（后端 :276-279 会 400）', async () => {
    await expect(resolveModelIntegrityEvent(0, 'x')).rejects.toThrow(/invalid integrity id/)
    await expect(resolveModelIntegrityEvent(-1, 'x')).rejects.toThrow(/invalid integrity id/)
    await expect(resolveModelIntegrityEvent(1.5, 'x')).rejects.toThrow(/invalid integrity id/)
  })
})

describe('severityTone', () => {
  it('critical/high → danger', () => {
    expect(severityTone('critical')).toBe('danger')
    expect(severityTone('high')).toBe('danger')
  })
  it('medium → warning', () => {
    expect(severityTone('medium')).toBe('warning')
  })
  it('low 与未知值 → muted（不臆造优先级）', () => {
    expect(severityTone('low')).toBe('muted')
    expect(severityTone('brand_new')).toBe('muted')
  })
})
