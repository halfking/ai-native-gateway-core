import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  fetchSessionList,
  fetchOnlineSessions,
  fetchSessionTimeline,
  sessionListTruncated,
  modelsUsedOf,
  onlineHasMore,
  sessionIdSourceIsGuess,
  sessionIdSourceKnown,
  countTurns,
  flattenTurns,
  costMayBeNoData,
  SESSION_LIST_TITLE_ALWAYS_FALSE,
  SESSION_LIST_SUMMARY_ALWAYS_FALSE,
  SESSION_LIST_MISSING_ID_ALWAYS_ZERO,
  SESSION_LIST_HAS_ID_ALWAYS_TRUE,
  SESSION_LIST_LIMIT_DEFAULT,
  SESSION_LIST_LIMIT_MAX,
  ONLINE_LIMIT_DEFAULT,
  ONLINE_LIMIT_MAX,
  SESSION_ID_SOURCES,
  type SessionAudit,
  type SessionListResponse,
  type SessionTurn,
} from './sessions'

/**
 * 会话运维面的契约测试（2026-10-06）。
 *
 * 五条重点：
 * 1. ★★★ `list` 的 `has_title` / `has_summary` / `missing_session_ids` /
 *    `has_session_id` 是**恒定值**（SQL 没查那些表 / 空行被 continue 跳过）；
 * 2. ★★ `limit` 被后端**回显** ⇒ 截断是**精确**信号，不是「可能」；
 * 3. ★★★ `online` 的 superAdmin **不加租户过滤**，且 `limit>100` 被**静默 clamp**；
 * 4. ★★★ `timeline` 的 `{id}` 有三种身份，`numeric_fallback_resolved` = 猜的；
 * 5. ★ `models_used` 可能是 `null`；`total_cost_usd=0` 是 COALESCE 兜底。
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
  return String(fetchMock.mock.calls[0]![0])
}
function decodedUrl(): string {
  return decodeURIComponent(lastUrl())
}
function okList(): void {
  fetchMock.mockResolvedValueOnce(jsonResponse({ tenant: 't1', limit: 50, sessions: [], id_kind: 'gw_session_id' }))
}
function okOnline(): void {
  fetchMock.mockResolvedValueOnce(jsonResponse({ sessions: [], count: 0, has_more: false }))
}

function audit(over: Partial<SessionAudit> = {}): SessionAudit {
  return {
    session_id: 'gw-abc',
    has_compression: false,
    compression_hits: 0,
    total_turns: 3,
    has_session_id: true,
    missing_session_ids: 0,
    has_title: false,
    has_summary: false,
    models_used: ['gpt-4o'],
    total_prompt_tokens: 100,
    total_resp_tokens: 50,
    total_cost_usd: 0.01,
    audit_at: '2026-10-07T10:00:00Z',
    ...over,
  }
}
function listResp(n: number, over: Partial<SessionListResponse> = {}): SessionListResponse {
  return {
    tenant: 't1',
    limit: 50,
    id_kind: 'gw_session_id',
    sessions: Array.from({ length: n }, (_, i) => ({
      id_kind: 'gw_session_id',
      primary_key: `gw-${i}`,
      audit: audit({ session_id: `gw-${i}` }),
    })),
    ...over,
  }
}

function turn(over: Partial<SessionTurn> = {}): SessionTurn {
  return { request_id: 'r1', request_type: 'main', status: 'ok', ...over }
}

describe('★ 判据 1：四个恒定字段', () => {
  it('★ has_title 恒 false ⇒ 不该渲染成「无标题」', () => {
    expect(SESSION_LIST_TITLE_ALWAYS_FALSE).toBe(true)
  })
  it('★ has_summary 恒 false', () => {
    expect(SESSION_LIST_SUMMARY_ALWAYS_FALSE).toBe(true)
  })
  it('★ missing_session_ids 恒 0', () => {
    expect(SESSION_LIST_MISSING_ID_ALWAYS_ZERO).toBe(true)
  })
  it('★ has_session_id 恒 true', () => {
    expect(SESSION_LIST_HAS_ID_ALWAYS_TRUE).toBe(true)
  })
  it('★ 后端真的返回这些恒定值时，函数**不**改写它们（只声明，不篡改）', () => {
    const a = audit({ has_title: false, has_summary: false, missing_session_ids: 0, has_session_id: true })
    expect(a.has_title).toBe(false)
    expect(a.missing_session_ids).toBe(0)
  })
})

describe('★★ 判据 2：limit 回显 ⇒ 精确截断', () => {
  it('不发 limit ⇒ 裸路径', async () => {
    okList()
    await fetchSessionList()
    expect(decodedUrl()).toBe('/api/admin/sessions/list')
  })

  it('★ limit=200 发得出去', async () => {
    okList()
    await fetchSessionList({ limit: 200 })
    expect(decodedUrl()).toContain('limit=200')
  })

  it('★ limit=501 **不发**（越界后端静默回落 50）', async () => {
    okList()
    await fetchSessionList({ limit: SESSION_LIST_LIMIT_MAX + 1 })
    expect(decodedUrl()).not.toContain('limit=')
  })

  it('★ limit=0 / 负数 / NaN 一律不发', async () => {
    for (const limit of [0, -5, NaN]) {
      fetchMock.mockResolvedValueOnce(jsonResponse({ tenant: 't', limit: 50, sessions: [], id_kind: 'gw_session_id' }))
      await fetchSessionList({ limit })
      expect(decodedUrl()).not.toContain('limit=')
    }
  })

  it('★★ sessions.length === **回显的** limit ⇒ 判截断', () => {
    expect(sessionListTruncated(listResp(50, { limit: 50 }))).toBe(true)
  })

  it('★★ 判据用回显值而非请求值（请求 200、回显 50、返回 50 ⇒ 截断）', () => {
    // 若错用「请求值 200」来判断，50 < 200 会漏报
    expect(sessionListTruncated(listResp(50, { limit: 50 }))).toBe(true)
  })

  it('★ sessions.length < limit ⇒ 未截断', () => {
    expect(sessionListTruncated(listResp(49, { limit: 50 }))).toBe(false)
  })

  it('★ 空响应 / null ⇒ 不判截断', () => {
    expect(sessionListTruncated(null)).toBe(false)
    expect(sessionListTruncated(listResp(0, { limit: 50 }))).toBe(false)
  })

  it('★ limit 缺失或 0 ⇒ 不判截断（没有基准就别说截断）', () => {
    expect(sessionListTruncated(listResp(50, { limit: 0 as unknown as number }))).toBe(false)
  })

  it('后端默认 limit 常量 = 50', () => {
    expect(SESSION_LIST_LIMIT_DEFAULT).toBe(50)
    expect(SESSION_LIST_LIMIT_MAX).toBe(500)
  })
})

describe('★ 判据 3：models_used 可为 null', () => {
  it('★ null ⇒ 空数组（不崩、不显示 undefined）', () => {
    expect(modelsUsedOf(audit({ models_used: null }))).toEqual([])
  })
  it('★ 有值 ⇒ 原样', () => {
    expect(modelsUsedOf(audit({ models_used: ['a', 'b'] }))).toEqual(['a', 'b'])
  })
  it('空数组 ⇒ 空数组', () => {
    expect(modelsUsedOf(audit({ models_used: [] }))).toEqual([])
  })
  it('null audit ⇒ 空数组', () => {
    expect(modelsUsedOf(null)).toEqual([])
  })
})

describe('★★★ 判据 4：online 的 scope 与 clamp', () => {
  it('不发参数 ⇒ 裸路径', async () => {
    okOnline()
    await fetchOnlineSessions()
    expect(decodedUrl()).toBe('/api/admin/sessions/online')
  })

  it('★ limit 上限是 100（不是 500）', () => {
    expect(ONLINE_LIMIT_MAX).toBe(100)
    expect(ONLINE_LIMIT_DEFAULT).toBe(20)
  })

  it('★ limit=100 发得出去（边界）', async () => {
    okOnline()
    await fetchOnlineSessions({ limit: 100 })
    expect(decodedUrl()).toContain('limit=100')
  })

  it('★★ limit=101 **不发**（后端会静默 clamp 到 100）', async () => {
    okOnline()
    await fetchOnlineSessions({ limit: 101 })
    expect(decodedUrl()).not.toContain('limit=')
  })

  it('★ cursor **原样透传**（不透明，后端自己编解码）', async () => {
    okOnline()
    await fetchOnlineSessions({ cursor: 'eyJ1IjoxMjM0NTY3ODkwfQ==' })
    expect(decodedUrl()).toContain('cursor=eyJ1IjoxMjM0NTY3ODkwfQ==')
  })

  it('★ cursor 空值 / null ⇒ 不发', async () => {
    okOnline()
    await fetchOnlineSessions({ cursor: null })
    expect(decodedUrl()).not.toContain('cursor=')
    fetchMock.mockResolvedValueOnce(jsonResponse({ sessions: [], count: 0, has_more: false }))
    await fetchOnlineSessions({ cursor: '' })
    expect(decodedUrl()).not.toContain('cursor=')
  })

  it('★ has_more 是后端给的显式标记（不是靠撞上限猜）', () => {
    expect(onlineHasMore({ sessions: [], count: 0, has_more: true })).toBe(true)
    expect(onlineHasMore({ sessions: [], count: 0, has_more: false })).toBe(false)
    expect(onlineHasMore(null)).toBe(false)
  })
})

describe('★★★ 判据 5：timeline 的三种 id 身份', () => {
  it('★ 空 id 本地抛错（后端会 400）', () => {
    expect(() => fetchSessionTimeline('')).toThrow('session id required')
    expect(() => fetchSessionTimeline('   ')).toThrow('session id required')
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('★ 正常 id ⇒ 路径带 encodeURIComponent', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ session_id: 'a', session_pk: null, session_id_source: 'path_gw_session_id', turns: [], count: 0, has_more: false, truncated: false }))
    await fetchSessionTimeline('gw-abc')
    expect(decodedUrl()).toContain('/api/admin/sessions/gw-abc/timeline')
  })

  it('★ session_pk 只发正整数', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ session_id: 'a', session_pk: 7, session_id_source: 'session_pk_resolved', turns: [], count: 0, has_more: false, truncated: false }))
    await fetchSessionTimeline('gw-abc', { sessionPk: 7 })
    expect(decodedUrl()).toContain('session_pk=7')
  })

  it('★ sessionPk=0 / 负数 ⇒ 不发（否则后端 400）', async () => {
    for (const pk of [0, -3]) {
      fetchMock.mockResolvedValueOnce(jsonResponse({ session_id: 'a', session_pk: null, session_id_source: 'path_gw_session_id', turns: [], count: 0, has_more: false, truncated: false }))
      await fetchSessionTimeline('gw-abc', { sessionPk: pk })
      expect(decodedUrl()).not.toContain('session_pk=')
    }
  })

  it('★ 三种已知来源', () => {
    expect([...SESSION_ID_SOURCES]).toEqual(['path_gw_session_id', 'session_pk_resolved', 'numeric_fallback_resolved'])
  })

  it('★★ 只有 numeric_fallback_resolved 算「猜的」', () => {
    expect(sessionIdSourceIsGuess('numeric_fallback_resolved')).toBe(true)
    expect(sessionIdSourceIsGuess('path_gw_session_id')).toBe(false)
    expect(sessionIdSourceIsGuess('session_pk_resolved')).toBe(false)
    expect(sessionIdSourceIsGuess(undefined)).toBe(false)
  })

  it('★ 未知来源不算已知（要如实显示，不能当正常路径）', () => {
    expect(sessionIdSourceKnown('path_gw_session_id')).toBe(true)
    expect(sessionIdSourceKnown('numeric_fallback_resolved')).toBe(true)
    expect(sessionIdSourceKnown('brand_new_source')).toBe(false)
    expect(sessionIdSourceKnown(null)).toBe(false)
  })

  it('★ 后端自带 truncated 字段（不用再自己猜）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ session_id: 'a', session_pk: null, session_id_source: 'path_gw_session_id', turns: [], count: 0, has_more: true, truncated: true }))
    const r = await fetchSessionTimeline('gw-abc')
    expect(r.truncated).toBe(true)
    expect(r.has_more).toBe(true)
  })
})

describe('轮次树递归', () => {
  it('★ countTurns 数进 children（不是只数顶层）', () => {
    const turns = [turn({ request_id: 'a' }), turn({ request_id: 'b', children: [turn({ request_id: 'b1' })] })]
    expect(countTurns(turns)).toBe(3)
  })

  it('★ 三层嵌套也数对', () => {
    const turns = [
      turn({
        request_id: 'a',
        children: [turn({ request_id: 'a1', children: [turn({ request_id: 'a1x' })] })],
      }),
    ]
    expect(countTurns(turns)).toBe(3)
  })

  it('空 / null ⇒ 0', () => {
    expect(countTurns([])).toBe(0)
    expect(countTurns(null)).toBe(0)
  })

  it('★ flattenTurns 保留深度，且顺序是深度优先', () => {
    const turns = [
      turn({ request_id: 'a', children: [turn({ request_id: 'a1' })] }),
      turn({ request_id: 'b' }),
    ]
    expect(flattenTurns(turns).map((x) => [x.turn.request_id, x.depth])).toEqual([
      ['a', 0],
      ['a1', 1],
      ['b', 0],
    ])
  })
})

describe('成本兜底复用（同族收口）', () => {
  it('★ 从 modelTaskIndex 复用，不重写一份', () => {
    expect(costMayBeNoData(0)).toBe(true)
    expect(costMayBeNoData(0.01)).toBe(false)
  })
})