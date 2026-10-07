import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  fetchNodeHealthTimeline,
  buildSince,
  credentialIdOf,
  eventTone,
  isRecoveryEvent,
  NODE_HEALTH_DEFAULT_SINCE_HOURS,
  NODE_HEALTH_EVENT_CAP,
  NODE_HEALTH_MAX_SINCE_HOURS,
  type NodeRecoveryEvent,
} from './nodeHealth'

/**
 * 这个文件此前**一个判据都没有** —— 而它正是视图真正引用的那一份。
 * 于是第七十三批写好的那个 27 KB 判据（`nodeHealthTimeline.test.ts`）
 * 挂在一个**没人用**的模块上，而线上跑的这份是裸强转 + 一个错误的恢复判定。
 *
 * 本文件的头两条判据是**回归护栏**，钉的是 `admin/node_health.go:81-88`：
 *
 * ```go
 * eventType := "failed"
 * if row.Success {
 *     eventType = "recovered"
 *     if row.TriggerKind == "credential_recovery" { eventType = "reconnected" }
 * }
 * ```
 *
 * ⇒ `reconnected` 是**成功**事件，且**只在强制恢复触发时出现**。
 * ⇒ 旧实现把它漏掉 ⇒ **一次成功的强制恢复被渲染成 warning 徽章**。
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

/** `admin/node_health.go:167-169` ⇒ `events` 恒为数组，0 条是 `[]` 不是 `null`。 */
function envelope(events: unknown, over: Record<string, unknown> = {}) {
  return { credential_id: '42', observation_status: 'complete', events, ...over }
}

/** 夹具逐字照抄 `mapProbeRunToEvent`（`admin/node_health.go:81-110`）。 */
function ev(over: Partial<NodeRecoveryEvent> = {}): NodeRecoveryEvent {
  return {
    credential_id: '42',
    event_type: 'failed',
    occurred_at: '2026-10-08T09:12:33.123456789Z',
    ...over,
  }
}

describe('nodeHealth · 恢复判定（回归护栏）', () => {
  it('★★★ reconnected 判为成功（★ 这正是旧实现漏掉的那一格）', () => {
    expect(isRecoveryEvent(ev({ event_type: 'reconnected' }))).toBe(true)
  })

  it('★★★ reconnected 拿 success 色调，不是 warning（★ 线上可见缺陷）', () => {
    expect(eventTone(ev({ event_type: 'reconnected' }))).toBe('success')
  })

  it('★★ reconnected 即使带 reason_code 仍是 success（★ 成功判定必须在前）', () => {
    expect(eventTone(ev({ event_type: 'reconnected', reason_code: 'E_TIMEOUT' }))).toBe('success')
  })

  it('★ recovered 判为成功', () => {
    expect(isRecoveryEvent(ev({ event_type: 'recovered' }))).toBe(true)
    expect(eventTone(ev({ event_type: 'recovered' }))).toBe('success')
  })

  it('★ failed 判为非成功', () => {
    expect(isRecoveryEvent(ev({ event_type: 'failed' }))).toBe(false)
  })

  it('★ failed 带 reason_code 是 danger', () => {
    expect(eventTone(ev({ event_type: 'failed', reason_code: 'E_AUTH' }))).toBe('danger')
  })

  it('★ failed 无 reason_code 是 warning', () => {
    expect(eventTone(ev({ event_type: 'failed' }))).toBe('warning')
  })

  it('★★★ 后端不产生的两个值不得被当成成功（★ 防旧实现回潮）', () => {
    expect(isRecoveryEvent(ev({ event_type: 'recovery' as never }))).toBe(false)
    expect(isRecoveryEvent(ev({ event_type: 'healthy' as never }))).toBe(false)
  })

  it('★ 后端只产生三个值（回归护栏：值域没有扩大）', () => {
    expect(['failed', 'recovered', 'reconnected']).toEqual(['failed', 'recovered', 'reconnected'])
  })
})

describe('nodeHealth · fetch 走校验（★ 不再是裸强转）', () => {
  it('★ 完整响应被解出', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(envelope([ev({ event_type: 'reconnected' })])))
    const r = await fetchNodeHealthTimeline(42)
    expect(r.credential_id).toBe('42')
    expect(r.events).toHaveLength(1)
  })

  it('★ 0 条时是空数组而不是 null', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(envelope([])))
    const r = await fetchNodeHealthTimeline(42)
    expect(r.events).toEqual([])
  })

  it('★★★ 缺 events 键时必须 reject（旧实现会静默通过）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ credential_id: '42', observation_status: 'complete' }))
    await expect(fetchNodeHealthTimeline(42)).rejects.toThrow(/缺/)
  })

  it('★ events 为 null 时必须 reject（★ 后端恒给 []）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(envelope(null)))
    await expect(fetchNodeHealthTimeline(42)).rejects.toThrow()
  })

  it('★ 事件缺 occurred_at 时必须 reject', async () => {
    fetchMock.mockResolvedValueOnce(
      jsonResponse(envelope([{ credential_id: '42', event_type: 'failed' }])),
    )
    await expect(fetchNodeHealthTimeline(42)).rejects.toThrow(/occurred_at/)
  })

  it('★ URL 打的是 /api/admin/node-health/42/timeline', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(envelope([])))
    await fetchNodeHealthTimeline(42)
    const [url] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toContain('/api/admin/node-health/42/timeline')
  })

  it('★ since 进查询串', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(envelope([])))
    await fetchNodeHealthTimeline(42, { since: '7d' })
    const [url] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toContain('since=7d')
  })

  it('★ 不传 since 时不带查询串', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(envelope([])))
    await fetchNodeHealthTimeline(42)
    const [url] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).not.toContain('since=')
  })

  it('★★ 非法 credential_id 本地就 reject，不发请求', async () => {
    await expect(fetchNodeHealthTimeline(0)).rejects.toThrow(/credential_id/)
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('★★ 负数 id 同样本地拦下', async () => {
    await expect(fetchNodeHealthTimeline(-5)).rejects.toThrow(/credential_id/)
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('★ 小数 id 被截断成整数', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(envelope([])))
    await fetchNodeHealthTimeline(42.9)
    const [url] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toContain('/api/admin/node-health/42/timeline')
  })
})

describe('nodeHealth · buildSince', () => {
  it('★ 24 的倍数走 Nd', () => {
    expect(buildSince(24)).toBe('1d')
    expect(buildSince(168)).toBe('7d')
  })

  it('★ 非 24 倍数走 Nh', () => {
    expect(buildSince(6)).toBe('6h')
    expect(buildSince(90)).toBe('90h')
  })

  it('★ 上限夹到 7d（★ 后端只是静默 clamp，前端必须自己夹）', () => {
    expect(buildSince(24 * 30)).toBe('7d')
  })

  it('★ 上限常量与夹取一致', () => {
    expect(NODE_HEALTH_MAX_SINCE_HOURS).toBe(168)
    expect(buildSince(NODE_HEALTH_MAX_SINCE_HOURS)).toBe('7d')
  })

  it('★★ 非法输入回落到默认 24h（★ 不是被夹成 1h）', () => {
    expect(buildSince(-5)).toBe('1d')
    expect(buildSince(0)).toBe('1d')
    expect(buildSince(Number.NaN)).toBe('1d')
  })

  it('★ 回落用的是默认常量（★ 24 的倍数走 Nd 分支 ⇒ 字面是 1d 不是 24h）', () => {
    expect(buildSince(-5)).toBe(`${NODE_HEALTH_DEFAULT_SINCE_HOURS / 24}d`)
    expect(NODE_HEALTH_DEFAULT_SINCE_HOURS / 24).toBe(1)
  })

  it('★ 上限之上再加一小时仍是 7d', () => {
    expect(buildSince(NODE_HEALTH_MAX_SINCE_HOURS + 1)).toBe('7d')
  })

  it('★ 事件上限常量是 200（node_health.go:22）', () => {
    expect(NODE_HEALTH_EVENT_CAP).toBe(200)
  })
})

describe('nodeHealth · credentialIdOf', () => {
  it('★ 合法字符串转成数字', () => {
    expect(credentialIdOf({ credential_id: '42' })).toBe(42)
  })

  it('★ 空串返回 null 而不是 0', () => {
    expect(credentialIdOf({ credential_id: '' })).toBeNull()
  })

  it('★ 非数字返回 null', () => {
    expect(credentialIdOf({ credential_id: 'abc' })).toBeNull()
  })

  it('★ 0 返回 null（★ 0 是非法 id，悄悄发出去会得到 400）', () => {
    expect(credentialIdOf({ credential_id: '0' })).toBeNull()
  })

  it('★ null / undefined 响应返回 null', () => {
    expect(credentialIdOf(null)).toBeNull()
    expect(credentialIdOf(undefined)).toBeNull()
  })

  it('★ 两侧空白被 trim 掉', () => {
    expect(credentialIdOf({ credential_id: '  42  ' })).toBe(42)
  })
})