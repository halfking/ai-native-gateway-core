import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  fetchPendingList,
  fetchPendingDetail,
  fetchPendingStats,
  pendingListFieldIsAlwaysZero,
  pendingUnixToIso,
  pendingAgeBand,
  PENDING_LIMIT_DEFAULT,
  PENDING_LIMIT_MAX,
  PENDING_STATUSES,
  PENDING_LIST_FIELDS_ALWAYS_ZERO,
  type PendingListResponse,
} from './pendingResponses'

/**
 * 待处理响应读面的契约测试（2026-10-07）。
 *
 * 五条重点：
 * 1. ★★★★ `status` 过滤是假的：底层只扫 in_progress，
 *    所以 completed/failed/垃圾值都**静默返回空**，绝不外发；
 * 2. ★★★★ 列表的 provider_id / is_stream / bytes_buffered **恒为 0/false**
 *    （`StaleEntry` 没这些字段），但 JSON 里没有 omitempty ⇒ 看起来像真值；
 * 3. ★★★ 时间是 Unix **秒**，不是 ISO；
 * 4. ★★★ limit 越界是静默回落/clamp，offset 越界返回空；
 * 5. ★★★ oldest_created_at 为 0 = 「没有条目」，不是 1970 年。
 */

const fetchMock = vi.fn()

function jsonResponse(body: unknown) {
  return {
    ok: true,
    status: 200,
    json: async () => body,
    text: async () => JSON.stringify(body),
    headers: new Headers({ 'content-type': 'application/json' }),
  } as unknown as Response
}

function lastUrl(): string {
  return String(fetchMock.mock.calls.at(-1)![0])
}

function body(): PendingListResponse {
  return { entries: [], limit: 50, offset: 0, count: 0 }
}

beforeEach(() => {
  fetchMock.mockReset()
  vi.stubGlobal('fetch', fetchMock)
  fetchMock.mockResolvedValue(jsonResponse(body()))
})
afterEach(() => {
  vi.unstubAllGlobals()
})

describe('路径与鉴权面', () => {
  it('★★★ list 走裸路径（不带任何参数）', async () => {
    await fetchPendingList()
    expect(lastUrl()).toContain('/api/admin/pending-responses')
    expect(lastUrl()).not.toContain('?')
  })

  it('★★★ stats 是**独立路径**，不是 list 的子路径', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ total: 0, by_status: {}, oldest_created_at: 0 }))
    await fetchPendingStats()
    expect(lastUrl()).toContain('/api/admin/pending-responses/stats')
  })

  it('★ detail 用 sessionID 拼路径并 URL 编码', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({}))
    await fetchPendingDetail('sess/abc 1')
    expect(lastUrl()).toContain('/api/admin/pending-responses/sess%2Fabc%201')
  })

  it('★ status 只有 in_progress 一个合法值', () => {
    expect([...PENDING_STATUSES]).toEqual(['in_progress'])
  })
})

describe('★★★★ 判据 1：status 过滤是假的，绝不外发非法值', () => {
  it('★★★★ status=completed **不发**（后端会静默返回空且不报错）', async () => {
    // @ts-expect-error 故意传非法值，验证客户端会拦下来
    await fetchPendingList({ status: 'completed' })
    expect(lastUrl()).not.toContain('status=')
  })

  it('★★★★ status=failed 同样不发', async () => {
    // @ts-expect-error 同上
    await fetchPendingList({ status: 'failed' })
    expect(lastUrl()).not.toContain('status=')
  })

  it('★★★ status=in_progress **要发出去**（证明上一条不是「一律不发」）', async () => {
    await fetchPendingList({ status: 'in_progress' })
    expect(lastUrl()).toContain('status=in_progress')
  })

  it('★ status=任意垃圾值不发', async () => {
    // @ts-expect-error 同上
    await fetchPendingList({ status: 'whatever' })
    expect(lastUrl()).not.toContain('status=')
  })
})

describe('★★★★ 判据 2：三个字段在列表端点恒为假值', () => {
  it('★★★★ 恒假字段清单就是这三个', () => {
    expect([...PENDING_LIST_FIELDS_ALWAYS_ZERO]).toEqual(['provider_id', 'is_stream', 'bytes_buffered'])
  })

  it('★★★ 每个恒假字段都判定为真，其它都假', () => {
    for (const f of PENDING_LIST_FIELDS_ALWAYS_ZERO) expect(pendingListFieldIsAlwaysZero(f)).toBe(true)
    for (const f of ['session_id', 'request_id', 'status', 'created_at', 'age_seconds', 'bytes']) {
      expect(pendingListFieldIsAlwaysZero(f)).toBe(false)
    }
  })

  it('★★ 清单里不得出现 completed_at（它有 omitempty，是「键缺失」而非「恒 0」）', () => {
    expect(pendingListFieldIsAlwaysZero('completed_at')).toBe(false)
  })
})

describe('★★★ 判据 4：分页参数只在合法区间内发', () => {
  it('★★ limit 默认 50 / 上限 500（后端 pageBounds 的常量）', () => {
    expect(PENDING_LIMIT_DEFAULT).toBe(50)
    expect(PENDING_LIMIT_MAX).toBe(500)
  })

  it('★★★ limit=0 / 负数 / NaN **不发**（后端会静默回落到 50，不报错）', async () => {
    for (const n of [0, -1, NaN]) {
      await fetchPendingList({ limit: n })
      expect(lastUrl()).not.toContain('limit=')
    }
  })

  it('★★ limit=501 **不发**（后端会静默 clamp 到 500）', async () => {
    await fetchPendingList({ limit: 501 })
    expect(lastUrl()).not.toContain('limit=')
  })

  it('★ limit=500 发得出去（证明边界是闭区间）', async () => {
    await fetchPendingList({ limit: 500 })
    expect(lastUrl()).toContain('limit=500')
  })

  it('★★ offset=0 不发；offset>0 发出去', async () => {
    await fetchPendingList({ offset: 0 })
    expect(lastUrl()).not.toContain('offset=')
    await fetchPendingList({ offset: 20 })
    expect(lastUrl()).toContain('offset=20')
  })

  it('★ offset 负数不发（后端当 0 处理）', async () => {
    await fetchPendingList({ offset: -5 })
    expect(lastUrl()).not.toContain('offset=')
  })

  it('★★ limit 小数会被截断成整数再发', async () => {
    await fetchPendingList({ limit: 20.9 })
    expect(lastUrl()).toContain('limit=20')
  })
})

describe('★★ session_id 过滤', () => {
  it('★ 空串 / 纯空白 **不发**（否则变成「查名字叫空白」）', async () => {
    await fetchPendingList({ sessionId: '' })
    expect(lastUrl()).not.toContain('session_id=')
    await fetchPendingList({ sessionId: '   ' })
    expect(lastUrl()).not.toContain('session_id=')
  })

  it('★ 前后空白被 trim 后再发', async () => {
    await fetchPendingList({ sessionId: '  gw_abc  ' })
    expect(lastUrl()).toContain('session_id=gw_abc')
  })
})

describe('★★★ 判据 3/5：时间换算与 0 哨兵', () => {
  it('★★★ Unix 秒 → ISO（×1000）', () => {
    expect(pendingUnixToIso(1791000000)).toBe(new Date(1791000000 * 1000).toISOString())
  })

  it('★★★ 0 / 负数 / null / undefined / NaN ⇒ undefined（不是 1970 年）', () => {
    expect(pendingUnixToIso(0)).toBeUndefined()
    expect(pendingUnixToIso(-1)).toBeUndefined()
    expect(pendingUnixToIso(null)).toBeUndefined()
    expect(pendingUnixToIso(undefined)).toBeUndefined()
    expect(pendingUnixToIso(NaN)).toBeUndefined()
  })

  it('★★ ISO 里的年份是真的（不是被当成毫秒）', () => {
    const iso = pendingUnixToIso(1791000000)!
    expect(iso.slice(0, 4)).toBe('2026')
  })
})

describe('★★ age_seconds 分档', () => {
  it('★★★ 四档边界：<60 / <600 / >=600', () => {
    expect(pendingAgeBand(0)).toBe('under_1m')
    expect(pendingAgeBand(59)).toBe('under_1m')
    expect(pendingAgeBand(60)).toBe('under_10m')
    expect(pendingAgeBand(599)).toBe('under_10m')
    expect(pendingAgeBand(600)).toBe('over_10m')
    expect(pendingAgeBand(999999)).toBe('over_10m')
  })

  it('★★ 负数/NaN/null ⇒ none（不是 under_1m）', () => {
    expect(pendingAgeBand(-1)).toBe('none')
    expect(pendingAgeBand(NaN)).toBe('none')
    expect(pendingAgeBand(null)).toBe('none')
    expect(pendingAgeBand(undefined)).toBe('none')
  })
})

describe('形状不符必须抛错，不得返 []', () => {
  it('★★★ list 缺 entries ⇒ 抛错，且错误文案点名「形状不符」', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ limit: 50, offset: 0, count: 0 }))
    await expect(fetchPendingList()).rejects.toThrow(/形状不符/)
  })

  it('★★★ list 返裸数组 ⇒ 抛错（不静默包成空列表）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse([]))
    await expect(fetchPendingList()).rejects.toThrow(/形状不符/)
  })

  it('★★ list 返 null ⇒ 抛错且文案说清实得 null', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(null))
    await expect(fetchPendingList()).rejects.toThrow(/实得 null/)
  })

  it('★★★ stats 缺 total ⇒ 抛错', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ by_status: {} }))
    await expect(fetchPendingStats()).rejects.toThrow(/形状不符/)
  })

  it('★★ stats 缺 by_status ⇒ 抛错', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ total: 0, oldest_created_at: 0 }))
    await expect(fetchPendingStats()).rejects.toThrow(/形状不符/)
  })

  it('★ 404 原样抛出（页面据此显示「这个会话没有待处理响应」）', async () => {
    fetchMock.mockResolvedValueOnce({
      ok: false,
      status: 404,
      json: async () => ({ error: { message: 'no pending response for this session', code: 'PENDING_NOT_FOUND' } }),
      text: async () => '',
      headers: new Headers({ 'content-type': 'application/json' }),
    } as unknown as Response)
    await expect(fetchPendingDetail('gw_none')).rejects.toBeTruthy()
  })
})