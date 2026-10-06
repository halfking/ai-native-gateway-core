import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  fetchExtractionStatus,
  unwrapExtractionStatus,
  fetchTitlesBatch,
  unwrapTitlesBatch,
  sessionContextPath,
  TITLES_BATCH_PATH,
  sessionTitleMapKey,
  splitSessionTitleMapKey,
  titleMapKeyHasNul,
  normalizeTitlesBatchKeys,
  titlesBatchExceedsLimit,
  TITLES_BATCH_MAX_KEYS,
  titlesBatchPartiallyAnswered,
  titlesBatchMissingKeys,
  extractionStatusIsIndeterminate,
  extractionStatusLacksDetailFields,
  titlesBatchEmptyIsIndeterminate,
  extractionDetailIsNull,
  sessionContextSettingsMissing,
  taskIdRequiredMessage,
  taskIdIsShadowedByBatch,
  EXTRACTION_STATUS_MINIMAL_KEYS,
  EXTRACTION_STATUS_FULL_KEYS,
  type ExtractionStatus,
  type TitlesBatchResult,
} from './sessionContext'

/**
 * 会话上下文读面的契约测试（2026-10-07）。
 *
 * 后端逐条对应：
 *   admin/handler.go:1296              /api/system/... 前缀（不是 /api/admin/！），h.admin
 *   admin/session_extract.go:22-90     分派器：两条「同名不同码」错误 + titles 特判
 *   admin/session_extract.go:231-277   extraction-status：**两形态异形端点**
 *   admin/session_title.go:274-276     scopedSessionIDKey = TrimSpace
 *   admin/session_title.go:337-378     loadSessionTitlesBatch（DB 错误返空 map）
 *   admin/session_title.go:381-383     ★★ map 键含**字面 NUL**
 *   admin/session_title.go:607-655     titles/batch：POST-only、>500、静默跳过/去重/省略
 */

const fetchMock = vi.fn()

function jsonResponse(body: unknown, status = 200) {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: async () => body,
    text: async () => (typeof body === 'string' ? body : JSON.stringify(body)),
    headers: new Headers({ 'content-type': 'application/json' }),
  } as unknown as Response
}

function lastCall(): { url: string; init: unknown } {
  const c = fetchMock.mock.calls.at(-1)!
  return { url: String(c[0]), init: c[1] }
}

function lastUrl(): string {
  return lastCall().url
}

/** ★ 走一遍 JSON 序列化再回来 —— 后端发的就是 JSON，这一步不能省。 */
function wire<T>(v: T): T {
  return JSON.parse(JSON.stringify(v)) as T
}

/** ★★ A 形（`admin/session_extract.go:241-244 / 259-262`）：**只有 2 个键**。 */
function statusNotExtracted(taskId = 'task-1'): ExtractionStatus {
  return wire({ task_id: taskId, extracted: false })
}

/** ★ B 形（`:267-276`）：8 个键齐全。 */
function statusExtracted(over: Record<string, unknown> = {}): ExtractionStatus {
  return wire({
    task_id: 'task-1',
    extracted: true,
    extracted_at: '2026-10-01T00:00:00Z',
    written: 12,
    skipped_noise: 3,
    skipped_duplicate: 1,
    status: 'done',
    detail: { turns: 20 },
    ...over,
  })
}

/** ★ `{"titles": {…}}`，键形如 `task-1\u0000scoped-9`。 */
function titlesOf(pairs: Array<[string, string, string]> = [['task-1', 'scoped-9', '会话标题']]): TitlesBatchResult {
  const m: Record<string, string> = {}
  for (const [t, s, title] of pairs) m[sessionTitleMapKey(t, s)] = title
  return wire({ titles: m })
}

beforeEach(() => {
  fetchMock.mockReset()
  vi.stubGlobal('fetch', fetchMock)
})
afterEach(() => {
  vi.unstubAllGlobals()
})

describe('★★★★★★ URL 形态：前缀是 /api/system/ 而不是 /api/admin/', () => {
  it('★★★★★★ 两条端点的 URL 与方法', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(statusNotExtracted()))
    await fetchExtractionStatus('task-1')
    expect(lastUrl()).toBe('/api/system/session-context/task-1/extraction-status')

    fetchMock.mockResolvedValueOnce(jsonResponse(titlesOf()))
    await fetchTitlesBatch([{ task_id: 'task-1', scoped_session_id: 'scoped-9' }])
    expect(lastUrl()).toBe(TITLES_BATCH_PATH)
    expect(lastUrl()).toBe('/api/system/session-context/titles/batch')
  })

  it('★★★★★ taskId 也要 encode', () => {
    expect(sessionContextPath('a/b', 'extraction-status')).toBe(
      '/api/system/session-context/a%2Fb/extraction-status',
    )
  })

  it('★★★★★ 前缀写错成 /api/admin/ 会 404（这族不在 admin 命名空间下）', () => {
    expect(TITLES_BATCH_PATH.startsWith('/api/admin/')).toBe(false)
    expect(TITLES_BATCH_PATH.startsWith('/api/system/')).toBe(true)
  })
})

describe('★★★★★★★★ extraction-status 是两形态异形端点', () => {
  it('★★★★★★★ A 形**只有 2 个键**，其余键**不存在**（不是 null）', async () => {
    const got = statusNotExtracted()
    expect(Object.keys(got).sort()).toEqual([...EXTRACTION_STATUS_MINIMAL_KEYS].sort())
    expect('extracted_at' in got).toBe(false)
    expect('status' in got).toBe(false)
    expect('written' in got).toBe(false)
    expect('detail' in got).toBe(false)
    // ★★ 这条是**变异 T13 逼出来的**：`extractionStatusLacksDetailFields` 恒 false 时
    //   原来**全绿** —— 因为我只断言过 B 形返回 false，**A 形返回 true 那侧从没喂过**。
    expect(extractionStatusLacksDetailFields(got)).toBe(true)
  })

  it('★★★★★★★ B 形是 8 个键', () => {
    const got = statusExtracted()
    expect(Object.keys(got).sort()).toEqual([...EXTRACTION_STATUS_FULL_KEYS].sort())
    expect(extractionStatusLacksDetailFields(got)).toBe(false)
  })

  it('★★★★★★ 解包判据**不能**要求 B 形的键（要求了就永远收不到 A 形）', () => {
    expect(() => unwrapExtractionStatus(statusNotExtracted())).not.toThrow()
    expect(() => unwrapExtractionStatus(statusExtracted())).not.toThrow()
    // ★ 裸数组 / null / 空对象 ⇒ 抛错
    expect(() => unwrapExtractionStatus(null)).toThrow(/形状不符/)
    expect(() => unwrapExtractionStatus([])).toThrow(/形状不符/)
    expect(() => unwrapExtractionStatus({})).toThrow(/形状不符/)
  })

  it('★★★★★ `task_id` 或 `extracted` 缺失 ⇒ 抛错', () => {
    expect(() => unwrapExtractionStatus({ extracted: false })).toThrow(/形状不符/)
    expect(() => unwrapExtractionStatus({ task_id: 'x' })).toThrow(/形状不符/)
    // ★ TS 会按 `unknown` 入参收窄，这里显式 cast 才能喂「extracted 不是布尔」
    expect(() => unwrapExtractionStatus({ task_id: 'x', extracted: 'yes' } as unknown)).toThrow(/形状不符/)
  })
})

describe('★★★★★★★★ `extracted:false` 有三种成因且完全分不开', () => {
  // 后端：不属于你的租户(:240) / 表里没这行(:258) / **DB 查询出错**(:258)
  // 三者都返 200 + {task_id, extracted:false} ⇒ 客户端一个都分辨不出。
  it('★★★★★★★ 三种成因的响应**逐字节同形**', () => {
    // ★ `as const`：ExtractionStatus 是**判别联合**（extracted: false | true），
    //   普通对象字面量会把 extracted 拓宽成 boolean ⇒ 类型不匹配。
    const notYours = wire({ task_id: 'task-1', extracted: false } as const)
    const neverExtracted = wire({ task_id: 'task-1', extracted: false } as const)
    const dbDown = wire({ task_id: 'task-1', extracted: false } as const)
    expect(JSON.stringify(notYours)).toBe(JSON.stringify(neverExtracted))
    expect(JSON.stringify(neverExtracted)).toBe(JSON.stringify(dbDown))
    expect(extractionStatusIsIndeterminate(dbDown)).toBe(true)
  })

  it('★★★★★ ★ **数据库故障从不返回 500**，它长成「没抽取过」', async () => {
    // 后端 `if err != nil { writeJSON(200, {task_id, extracted:false}) }`
    // ⇒ 真实 200 + extracted:false。判据是「没有错误条」。
    fetchMock.mockResolvedValueOnce(jsonResponse({ task_id: 'task-1', extracted: false }, 200))
    const got = await fetchExtractionStatus('task-1')
    expect(got.extracted).toBe(false)
    expect(extractionStatusIsIndeterminate(got)).toBe(true)
  })

  it('★★★★★ 任务不存在**不是 404**，也是 200 + extracted:false', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ task_id: 'ghost', extracted: false }, 200))
    const got = await fetchExtractionStatus('ghost')
    expect(got.task_id).toBe('ghost')
    expect(got.extracted).toBe(false)
  })
})

describe('★★★★★★★★★ map 键含字面 NUL', () => {
  it('★★★★★★★★★ 键形如 `task-1\\u0000scoped-9`，按 taskId 直查**永远 miss**', () => {
    const key = sessionTitleMapKey('task-1', 'scoped-9')
    expect(key).toBe('task-1\u0000scoped-9')
    expect(key.length).toBe('task-1'.length + 1 + 'scoped-9'.length)
    // ★ 客户端天真地 titles[taskId] 查不到
    const titles = titlesOf()
    expect(titles.titles['task-1']).toBeUndefined()
    expect(titles.titles[key]).toBe('会话标题')
  })

  it('★★★★★★★★★ 键里确实含 NUL ⇒ 判「这个 map 是不是后端产的」', () => {
    expect(titleMapKeyHasNul(sessionTitleMapKey('a', 'b'))).toBe(true)
    expect(titleMapKeyHasNul('a')).toBe(false)
  })

  it('★★★★★★★ scoped 为空 ⇒ 键是 `task-1\\u0000`（**带尾随 NUL**）', () => {
    const key = sessionTitleMapKey('task-1', '')
    expect(key.length).toBe('task-1'.length + 1)
    expect(key.endsWith('\u0000')).toBe(true)
    expect(splitSessionTitleMapKey(key)).toEqual({ taskId: 'task-1', scopedSessionId: '' })
  })

  it('★★★★★★ scoped 会被 trim（后端 scopedSessionIDKey = TrimSpace）', () => {
    expect(sessionTitleMapKey('t', '  s  ')).toBe(sessionTitleMapKey('t', 's'))
  })

  it('★★★★★★★ 拆键能还原出两边（客户端只能这样反查）', () => {
    const key = sessionTitleMapKey('task-1', 'scoped-9')
    expect(splitSessionTitleMapKey(key)).toEqual({ taskId: 'task-1', scopedSessionId: 'scoped-9' })
  })

  it('★★★★★ 走一遍 JSON 序列化后 NUL 仍在（不是编码问题，是后端真的这么发）', () => {
    const key = JSON.parse('"task-1\\u0000scoped-9"') as string
    expect(key).toBe(sessionTitleMapKey('task-1', 'scoped-9'))
  })
})

describe('★★★★★★★ titles/batch 的四种「空」分不开', () => {
  it('★★★★★★★ 空 keys 的早返回分支：{titles:{}}', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ titles: {} }))
    const got = await fetchTitlesBatch([])
    expect(got.titles).toEqual({})
    expect(titlesBatchEmptyIsIndeterminate(got)).toBe(true)
  })

  it('★★★★★★★ DB 查询出错 ⇒ 也是 {titles:{}} + **200**（不是 500）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ titles: {} }))
    const got = await fetchTitlesBatch([{ task_id: 'task-1' }])
    expect(got.titles).toEqual({})
    expect(titlesBatchEmptyIsIndeterminate(got)).toBe(true)
  })

  it('★★★★★ `titles` 永远不为 null（后端 make 出来的）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ titles: {} }))
    const got = await fetchTitlesBatch([{ task_id: 't' }])
    expect(got.titles).not.toBeNull()
  })

  it('★★★★★★ 请求体是 `{keys:[…]}`，**空 task_id 不发** scoped 字段', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(titlesOf()))
    await fetchTitlesBatch(normalizeTitlesBatchKeys([{ task_id: '  task-1  ', scoped_session_id: ' s ' }]))
    const init = lastCall().init as { body: string }
    expect(JSON.parse(init.body)).toEqual({ keys: [{ task_id: 'task-1', scoped_session_id: 's' }] })
  })
})

describe('★★★★★★ 静默丢弃三处', () => {
  it('★★★★★★ 空 task_id 被**静默跳过**（客户端要自己先剔）', () => {
    expect(normalizeTitlesBatchKeys([{ task_id: 'a' }, { task_id: '   ' }, { task_id: '' }])).toEqual([
      { task_id: 'a' },
    ])
  })

  it('★★★★★★ 重复的 (task_id, scoped) 被**静默去重**', () => {
    const out = normalizeTitlesBatchKeys([
      { task_id: 'a' },
      { task_id: 'a' },
      { task_id: 'a', scoped_session_id: 's' },
    ])
    expect(out).toEqual([{ task_id: 'a' }, { task_id: 'a', scoped_session_id: 's' }])
  })

  it('★★★★★★★★ ★ 没有标题的键**整个消失** ⇒ 键缺失 ≠ 标题是空串', () => {
    const r = titlesOf([['task-1', 's1', '有标题']])
    // 后端「没存过标题的键直接从 map 里省略」
    expect('task-2' in r.titles).toBe(false)
    expect(r.titles[sessionTitleMapKey('task-1', 's1')]).toBe('有标题')
    // ⇒ 没有「空串标题」这种值
    expect(Object.values(r.titles)).not.toContain('')
  })

  it('★★★★★ 缺键可被逐个列出（供页面标「没存过」）', () => {
    const r = titlesOf([['task-1', 's1', 'T']])
    const missing = titlesBatchMissingKeys(r, [
      { task_id: 'task-1', scoped_session_id: 's1' },
      { task_id: 'task-2', scoped_session_id: 's1' },
    ])
    expect(missing).toEqual([sessionTitleMapKey('task-2', 's1')])
    expect(titlesBatchPartiallyAnswered(2, 1)).toBe(true)
    expect(titlesBatchPartiallyAnswered(2, 2)).toBe(false)
  })
})

describe('★★★★★ 限幅与错误态', () => {
  it('★★★★★★ 是 `> 500` 不是 `>= 500` ⇒ **正好 500 个合法**', () => {
    expect(TITLES_BATCH_MAX_KEYS).toBe(500)
    expect(titlesBatchExceedsLimit(500)).toBe(false)
    expect(titlesBatchExceedsLimit(501)).toBe(true)
    expect(titlesBatchExceedsLimit(0)).toBe(false)
  })

  it('★★★★★ 501 个 ⇒ 400 `too many keys (max 500)`', async () => {
    fetchMock.mockResolvedValueOnce(
      jsonResponse({ error: { detail: 'too many keys (max 500)' } }, 400),
    )
    await expect(fetchTitlesBatch([])).rejects.toThrow(/too many keys/)
  })

  it('★★★★★ 503 `database not configured`（两条都有）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ error: { detail: 'database not configured' } }, 503))
    await expect(fetchExtractionStatus('t')).rejects.toThrow(/database not configured/)
    expect(sessionContextSettingsMissing('database not configured')).toBe(true)
    expect(sessionContextSettingsMissing('not found')).toBe(false)
  })

  it('★★★★★ 400 `invalid body: …`', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ error: { detail: 'invalid body: unexpected EOF' } }, 400))
    await expect(fetchTitlesBatch([])).rejects.toThrow(/invalid body/)
  })

  it('★★★★★★ ★ 400 与 404 **共用** `task_id required` ⇒ 只能看状态码', async () => {
    // rest == "" ⇒ 404；TrimSpace(parts[0]) == "" ⇒ 400。同一句话。
    const msg = 'task_id required'
    expect(taskIdRequiredMessage(msg)).toBe(true)
    fetchMock.mockResolvedValueOnce(jsonResponse({ error: { detail: msg } }, 404))
    await expect(fetchExtractionStatus('t')).rejects.toThrow(new RegExp(msg))
  })

  it('★★★★★ GET titles/batch ⇒ **405**（只读语义但只收 POST）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ error: { detail: 'method not allowed' } }, 405))
    await expect(fetchTitlesBatch([])).rejects.toThrow(/method not allowed/)
  })

  it('★★★★★ 404 `unknown session-context route`', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ error: { detail: 'unknown session-context route' } }, 404))
    await expect(fetchExtractionStatus('t')).rejects.toThrow(/unknown session-context route/)
  })

  it('★★★★★★ task_id 叫 `titles` 的会话被特判截胡', () => {
    expect(taskIdIsShadowedByBatch('titles')).toBe(true)
    expect(taskIdIsShadowedByBatch('task-1')).toBe(false)
  })
})

describe('★★★ detail 列的 nullability', () => {
  it('★★★★★★ `COALESCE` 只挡 SQL NULL，挡不住 **JSON 标量 null**', () => {
    // 后端 SQL 是 COALESCE(detail, '{}'::jsonb) —— 列里存的是 JSON null 时不会被替换
    const s = statusExtracted({ detail: null })
    expect(extractionDetailIsNull(s)).toBe(true)
  })

  it('★★★★★ detail 是普通对象时不为 null', () => {
    expect(extractionDetailIsNull(statusExtracted())).toBe(false)
  })
})

describe('★★ 形状互喂', () => {
  it('★★★★★ `titles` 与 `extraction-status` 两形状互不包含 ⇒ 互喂抛错', () => {
    expect(() => unwrapTitlesBatch(statusExtracted())).toThrow(/形状不符/)
    expect(() => unwrapExtractionStatus(titlesOf())).toThrow(/形状不符/)
  })

  it('★★★★★ `{titles:{}}` / `{archives:[]}` 等兄弟形状都抛错', () => {
    expect(() => unwrapTitlesBatch({ archives: [] })).toThrow(/形状不符/)
    expect(() => unwrapTitlesBatch({ items: [] })).toThrow(/形状不符/)
    expect(() => unwrapTitlesBatch(null)).toThrow(/形状不符/)
  })

  it('★★★★★ `titles` 键存在但不是对象 ⇒ 抛错', () => {
    expect(() => unwrapTitlesBatch({ titles: [] })).toThrow(/形状不符/)
    expect(() => unwrapTitlesBatch({ titles: 'x' })).toThrow(/形状不符/)
    expect(() => unwrapTitlesBatch({})).toThrow(/形状不符/)
  })
})