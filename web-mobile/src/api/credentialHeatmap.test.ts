import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  fetchCredentialHeatmap,
  buildHeatmapWindow,
  allowedGranularities,
  finestAllowedGranularity,
  nodeStateTone,
  bucketRate,
  HEATMAP_MAX_WINDOW_HOURS,
  HEATMAP_MAX_BUCKETS,
  type HeatmapResponse,
} from './credentialHeatmap'
import {
  fetchNodeHealthTimeline,
  buildSince,
  credentialIdOf,
  eventTone,
  NODE_HEALTH_MAX_SINCE_HOURS,
  NODE_HEALTH_DEFAULT_SINCE_HOURS,
} from './nodeHealth'

/**
 * 凭据监控线两个端点的契约测试（2026-10-07）。
 * 重点：热力图四个 400 约束、粒度与窗口的**桶数联动**、
 *       node-health 的字符串 credential_id 与静默 clamp。
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

const NOW = Date.parse('2026-10-07T12:00:00Z')

describe('heatmap 时间窗必填且封顶 7d', () => {
  it('两个时间参数都发（后端缺一个就是 400，不是用默认窗口）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ meta: {}, credentials: [] } as unknown as HeatmapResponse))
    const w = buildHeatmapWindow(24, NOW)
    await fetchCredentialHeatmap({ time_start: w.time_start, time_end: w.time_end, granularity: '15m' })
    // ★ URLSearchParams 会把 ':' 编成 %3A —— 断言必须按**编码后**的形态写，
    //   否则这条会在「参数没发」和「断言写错」之间分不清。
    const url = decodeURIComponent(String(fetchMock.mock.calls[0]![0]))
    expect(url).toContain('time_start=2026-10-06T12:00:00.000Z')
    expect(url).toContain('time_end=2026-10-07T12:00:00.000Z')
    expect(url).toContain('granularity=15m')
  })

  it('★ 超 7 天的窗口在发出前被夹到 7 天（后端是 400 硬失败）', () => {
    const w = buildHeatmapWindow(30 * 24, NOW)
    const spanH = (Date.parse(w.time_end) - Date.parse(w.time_start)) / 3600_000
    expect(spanH).toBe(HEATMAP_MAX_WINDOW_HOURS)
  })

  it('exclude_self_test 缺省不发（后端默认已是 true，裸发会把自检算进服务质量）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ meta: {}, credentials: [] }))
    const w = buildHeatmapWindow(1, NOW)
    await fetchCredentialHeatmap({ time_start: w.time_start, time_end: w.time_end })
    expect(String(fetchMock.mock.calls[0]![0])).not.toContain('exclude_self_test')
  })

  it('显式要自检时才发 false', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ meta: {}, credentials: [] }))
    const w = buildHeatmapWindow(1, NOW)
    await fetchCredentialHeatmap({ time_start: w.time_start, time_end: w.time_end, exclude_self_test: false })
    expect(String(fetchMock.mock.calls[0]![0])).toContain('exclude_self_test=false')
  })

  it('credential_ids / models 逗号分隔', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ meta: {}, credentials: [] }))
    const w = buildHeatmapWindow(1, NOW)
    await fetchCredentialHeatmap({ time_start: w.time_start, time_end: w.time_end, credential_ids: [3, 7], models: ['gpt-4o'] })
    const url = String(fetchMock.mock.calls[0]![0])
    expect(url).toContain('credential_ids=3%2C7')
    expect(url).toContain('models=gpt-4o')
  })
})

describe('★ 粒度受桶数上限约束（7d + 1m 必然 400）', () => {
  // 后端 :153-157 桶数 > 5000 直接 400。7d = 604800s：
  //   1m → 10080 ✗    5m → 2016 ✓    15m → 672 ✓
  it('1h 窗口下 1m 可用（60 桶）', () => {
    expect(allowedGranularities(1)).toContain('1m')
  })

  it('★ 7 天窗口下只有 1m 不可用（10080 桶 > 5000）；5m 是 2016，可用', () => {
    const ok = allowedGranularities(HEATMAP_MAX_WINDOW_HOURS)
    expect(ok).not.toContain('1m')
    expect(ok).toContain('5m')
  })

  it('7 天窗口下最细可用粒度是 5m', () => {
    expect(finestAllowedGranularity(HEATMAP_MAX_WINDOW_HOURS)).toBe('5m')
  })

  it('桶数判据与后端上限一致（而不是我拍的一个数）', () => {
    // 找一条边界：刚好 ≤5000 与刚好 >5000
    const okAt = (h: number, g: '1m' | '5m' | '15m' | '1h' | '1d') =>
      Math.floor((h * 3600) / ({ '1m': 60, '5m': 300, '15m': 900, '1h': 3600, '1d': 86400 }[g])) <= HEATMAP_MAX_BUCKETS
    // 83h × 1m = 4980 ✓ ；84h × 1m = 5040 ✗
    expect(okAt(83, '1m')).toBe(true)
    expect(okAt(84, '1m')).toBe(false)
  })
})

describe('nodeStateTone —— 词表外的状态不当成成功', () => {
  it('词表内各状态映射正确', () => {
    expect(nodeStateTone('healthy_confirmed')).toBe('success')
    expect(nodeStateTone('broken_confirmed')).toBe('danger')
    expect(nodeStateTone('suspicious')).toBe('warning')
    expect(nodeStateTone('probing')).toBe('warning')
    expect(nodeStateTone('unknown')).toBe('muted')
    expect(nodeStateTone('unprobed')).toBe('muted')
  })

  // ★ 未在词表内的值一律 muted。默认给 success 会把「看不懂的状态」显示成健康。
  it('未知状态 ⇒ muted（不是 success）', () => {
    expect(nodeStateTone('something_new')).toBe('muted')
    expect(nodeStateTone(null)).toBe('muted')
    expect(nodeStateTone(undefined)).toBe('muted')
  })
})

describe('bucketRate —— 无样本 ≠ 成功率 0%', () => {
  it('有样本时返回后端给的 success_rate', () => {
    expect(bucketRate({ total_requests: 10, success_count: 7, success_rate: 0.7 })).toBeCloseTo(0.7)
  })

  // ★「没有样本」与「全部失败」是两件事。直接 success/total 得 NaN，
  // 用 || 0 兜底则把两者混为一谈。
  it('total_requests=0 ⇒ null 而不是 0', () => {
    expect(bucketRate({ total_requests: 0, success_count: 0, success_rate: 0 })).toBeNull()
  })
  it('缺对象 ⇒ null', () => {
    expect(bucketRate(null)).toBeNull()
    expect(bucketRate(undefined)).toBeNull()
  })
})

describe('node-health：★ 响应里的 credential_id 是字符串', () => {
  it('URL 用数字 id', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ credential_id: '7', observation_status: 'complete', events: [] }))
    await fetchNodeHealthTimeline(7, { since: '24h' })
    const url = String(fetchMock.mock.calls[0]![0])
    expect(url).toContain('/api/admin/node-health/7/timeline')
    expect(url).toContain('since=24h')
  })

  it('credentialIdOf 把字符串解回数字', () => {
    expect(credentialIdOf({ credential_id: '7' })).toBe(7)
  })

  // ★ 返回 null 而不是 0 —— 0 拼进 URL 会得到 400 "invalid credential_id"。
  it('空 / 非法 id ⇒ null 而不是 0', () => {
    expect(credentialIdOf({ credential_id: '' })).toBeNull()
    expect(credentialIdOf({ credential_id: 'abc' })).toBeNull()
    expect(credentialIdOf({ credential_id: '-3' })).toBeNull()
    expect(credentialIdOf(null)).toBeNull()
  })
})

describe('buildSince —— ★ 与热力图相反：这里是静默 clamp，必须前端夹', () => {
  it('24 的倍数走 Nd 分支', () => {
    expect(buildSince(24)).toBe('1d')
    expect(buildSince(168)).toBe('7d')
  })
  it('非 24 倍数走 Go duration 分支', () => {
    expect(buildSince(6)).toBe('6h')
    expect(buildSince(90)).toBe('90h')
  })
  it('★ 超 7 天被夹到 7d（后端也会静默 clamp，但那会让界面显示的范围与实取不一致）', () => {
    expect(buildSince(30 * 24)).toBe('7d')
    expect(Number(buildSince(30 * 24).slice(0, -1))).toBe(NODE_HEALTH_MAX_SINCE_HOURS / 24)
  })
  it('0 / 负数 / NaN ⇒ 默认 24h', () => {
    expect(buildSince(0)).toBe('1d')
    expect(buildSince(-5)).toBe('1d')
    expect(buildSince(Number.NaN)).toBe('1d')
    expect(NODE_HEALTH_DEFAULT_SINCE_HOURS).toBe(24)
  })
})

describe('eventTone', () => {
  it('恢复类事件是 success', () => {
    expect(eventTone({ credential_id: '1', event_type: 'recovered', occurred_at: '' })).toBe('success')
  })
  it('带 reason_code 是 danger', () => {
    expect(
      eventTone({ credential_id: '1', event_type: 'broke', occurred_at: '', reason_code: 'E_UPSTREAM' }),
    ).toBe('danger')
  })
  it('其余是 warning', () => {
    expect(eventTone({ credential_id: '1', event_type: 'probing', occurred_at: '' })).toBe('warning')
  })
})
