import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  fetchInjectionStats,
  fetchInjectionDetections,
  fetchAttackVectors,
  fetchInjectionRules,
  fetchInjectionEngines,
  fetchSeverityMatrix,
  fetchCanaryTokens,
  unwrapInjectionStats,
  unwrapInjectionDetections,
  unwrapAttackVectors,
  unwrapInjectionRules,
  unwrapInjectionEngines,
  unwrapSeverityMatrix,
  unwrapCanaryTokens,
  parseInjectionRiskLevel,
  vectorPageLooksFull,
  INJECTION_CATEGORIES,
  INJECTION_ACTIONS,
  INJECTION_SEVERITY_LEVELS,
  INJECTION_PAGE_SIZE_DEFAULT,
  INJECTION_PAGE_SIZE_MAX,
  type InjectionDetectionsResponse,
} from './promptInjection'

/**
 * 提示词注入读面的契约测试（2026-10-07）。
 *
 * 六条重点：
 * 1. ★★★★ `enabled` / `blocked` 的真值判定是 `== "true"`
 *    ⇒ 只发 true/false 字面量，发 `1` 会筛出**相反**结果；
 * 2. ★★★★ 三个列表端点分页形状**各不相同**，其中 rules/engines/canary-tokens
 *    **根本不接受分页**（`count` 是本次返回条数，不是总数）；
 * 3. ★★★★ `page_size` 越界是**回落默认 20**，**不是** clamp 到 100；
 * 4. ★★★★ `attack-vectors` 响应**没有 total**；
 * 5. ★★★★ `stats` 没统计行时返回**全 0 对象**（不是 404）⇒ 全 0 是合法响应；
 * 6. ★★★★★ `risk_level` 是 **1..10 的数字字符串**，不是等级名。
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

function detResp(over: Record<string, unknown> = {}): InjectionDetectionsResponse {
  return { detections: [], page: 1, page_size: 20, total: 0, ...over }
}

beforeEach(() => {
  fetchMock.mockReset()
  vi.stubGlobal('fetch', fetchMock)
  fetchMock.mockResolvedValue(jsonResponse({}))
})
afterEach(() => {
  vi.unstubAllGlobals()
})

describe('路径', () => {
  it('★★★ 八个只读端点各自独立（逐个显式声明夹具，不用 helper 猜）', async () => {
    // ★ 用一个「按调用次序排队」的显式夹具：`Object.keys().find(url.includes)`
    //   那种写法在路径互为前缀时会把夹具配错。
    const queue: unknown[] = [
      { total_detections: 0 },
      detResp(),
      { vectors: [], page: 1, page_size: 20 },
      { rules: [], count: 0 },
      { engines: [], count: 0 },
      { matrix: [] },
      { tokens: [], count: 0 },
    ]
    for (const body of queue) fetchMock.mockResolvedValueOnce(jsonResponse(body))

    await fetchInjectionStats()
    await fetchInjectionDetections()
    await fetchAttackVectors()
    await fetchInjectionRules()
    await fetchInjectionEngines()
    await fetchSeverityMatrix()
    await fetchCanaryTokens()

    const urls = fetchMock.mock.calls.map((c) => String(c[0]))
    for (const u of [
      '/api/admin/prompt-injection/stats',
      '/api/admin/prompt-injection/detections?',
      '/api/admin/prompt-injection/attack-vectors?',
      '/api/admin/prompt-injection/rules',
      '/api/admin/prompt-injection/engines',
      '/api/admin/prompt-injection/severity-matrix',
      '/api/admin/prompt-injection/canary-tokens',
    ]) {
      expect(urls.some((x) => x.startsWith(u))).toBe(true)
    }
  })
})

describe('★★★★ 判据 1：布尔筛选只发 true/false 字面量', () => {
  it('★★★★ blocked=true ⇒ 发 `blocked=true`（不是 1）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(detResp()))
    await fetchInjectionDetections({ blocked: true })
    expect(lastUrl()).toContain('blocked=true')
    expect(lastUrl()).not.toContain('blocked=1')
  })

  it('★★★★ blocked=false ⇒ 也发 `false`（后端据此筛「未阻断」）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(detResp()))
    await fetchInjectionDetections({ blocked: false })
    expect(lastUrl()).toContain('blocked=false')
  })

  it('★★★ 不传 blocked ⇒ 不发（后端不过滤）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(detResp()))
    await fetchInjectionDetections({})
    expect(lastUrl()).not.toContain('blocked=')
  })

  it('★★★★ enabled 同理：rules 只发 true/false', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ rules: [], count: 0 }))
    await fetchInjectionRules({ enabled: true })
    expect(lastUrl()).toContain('enabled=true')
    await fetchInjectionRules({ enabled: false })
    expect(lastUrl()).toContain('enabled=false')
    await fetchInjectionRules({})
    expect(lastUrl()).not.toContain('enabled=')
  })
})

describe('★★★★ 判据 2/3：三种分页形状', () => {
  it('★★★★ rules **不发**任何分页参数', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ rules: [], count: 0 }))
    await fetchInjectionRules({})
    expect(lastUrl()).not.toContain('page')
    expect(lastUrl()).not.toContain('limit')
  })

  it('★★★★ engines / canary-tokens 走裸路径', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ engines: [], count: 0 }))
    await fetchInjectionEngines()
    expect(lastUrl()).toBe('/api/admin/prompt-injection/engines')
    fetchMock.mockResolvedValue(jsonResponse({ tokens: [], count: 0 }))
    await fetchCanaryTokens()
    expect(lastUrl()).toBe('/api/admin/prompt-injection/canary-tokens')
  })

  it('★★★★ detections 用 `page` + `page_size`（**不是** limit/offset）', async () => {
    fetchMock.mockResolvedValue(jsonResponse(detResp()))
    await fetchInjectionDetections({ page: 3, pageSize: 50 })
    expect(lastUrl()).toContain('page=3')
    expect(lastUrl()).toContain('page_size=50')
    expect(lastUrl()).not.toContain('limit=')
    expect(lastUrl()).not.toContain('offset=')
  })

  it('★★★ page_size 越界在**客户端**就回落默认 20（不发 101）', async () => {
    fetchMock.mockResolvedValue(jsonResponse(detResp()))
    await fetchInjectionDetections({ pageSize: 101 })
    expect(lastUrl()).toContain('page_size=20')
    expect(lastUrl()).not.toContain('page_size=101')
  })

  it('★★★ page_size ≤ 0 / NaN 同样回落 20', async () => {
    for (const n of [0, -5, NaN]) {
      fetchMock.mockResolvedValue(jsonResponse(detResp()))
      await fetchInjectionDetections({ pageSize: n })
      expect(lastUrl()).toContain('page_size=20')
    }
  })

  it('★★★ page < 1 回落 1（不是 0）', async () => {
    fetchMock.mockResolvedValue(jsonResponse(detResp()))
    await fetchInjectionDetections({ page: 0 })
    expect(lastUrl()).toContain('page=1')
  })

  it('★★ page_size=100 是闭区间上界，发得出去', async () => {
    fetchMock.mockResolvedValue(jsonResponse(detResp()))
    await fetchInjectionDetections({ pageSize: 100 })
    expect(lastUrl()).toContain('page_size=100')
  })

  it('★ 常量：默认 20 / 上界 100', () => {
    expect(INJECTION_PAGE_SIZE_DEFAULT).toBe(20)
    expect(INJECTION_PAGE_SIZE_MAX).toBe(100)
  })
})

describe('★★★★ 判据 4：attack-vectors 没有 total', () => {
  it('★★★★ 响应形状只有 vectors/page/page_size', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ vectors: [], page: 1, page_size: 20 }))
    const r = await fetchAttackVectors()
    expect(r).not.toHaveProperty('total')
    expect(r.vectors).toEqual([])
  })

  it('★★★ 「这页排满」是**近似**判断，不是精确的「还有更多」', () => {
    expect(vectorPageLooksFull(20, 20)).toBe(true)
    expect(vectorPageLooksFull(20, 5)).toBe(false)
    expect(vectorPageLooksFull(20, 0)).toBe(false) // 空页不算「排满」
  })

  it('★★★ 「排满」阈值跟的是**生效**的 page_size，不是请求里那个', () => {
    // ★ 越界的 101 会被回落到 20，服务端也就只给 20 条 ⇒ 这一页确实排满了。
    //   若阈值错用「请求值 101」，就会把「排满」判成 false ⇒ 下一页按钮被错误禁用。
    expect(vectorPageLooksFull(101, 20)).toBe(true)
    // 反向：请求 100（生效 100），只回 20 条 ⇒ 没排满
    expect(vectorPageLooksFull(100, 20)).toBe(false)
    expect(vectorPageLooksFull(100, 100)).toBe(true)
  })
})

describe('★★★★ 判据 5：stats 全 0 是合法响应', () => {
  it('★★★★ 十四项全 0 的对象必须被接受（ErrNoRows 分支）', async () => {
    const allZero = {
      total_detections: 0,
      blocked_count: 0,
      critical_count: 0,
      high_count: 0,
      medium_count: 0,
      low_count: 0,
      approval_count: 0,
      replaced_count: 0,
      terminated_count: 0,
      canary_leak_count: 0,
      avg_score: 0,
      max_score: 0,
      avg_llm_confidence: 0,
      affected_sessions: 0,
    }
    fetchMock.mockResolvedValueOnce(jsonResponse(allZero))
    const s = await fetchInjectionStats()
    expect(s.total_detections).toBe(0)
    expect(s.avg_score).toBe(0)
  })

  it('★★ 缺 total_detections 才抛错', () => {
    expect(() => unwrapInjectionStats({ blocked_count: 1 })).toThrow(/形状不符/)
    expect(() => unwrapInjectionStats(null)).toThrow(/实得 null/)
  })
})

describe('★★★★★ 判据 6：risk_level 是 1..10 的数字字符串', () => {
  it('★★★★★ "7" ⇒ 7（不是等级名）', () => {
    expect(parseInjectionRiskLevel('7')).toBe(7)
    expect(parseInjectionRiskLevel('1')).toBe(1)
    expect(parseInjectionRiskLevel('10')).toBe(10)
  })

  it('★★★★★ 等级名（low/high…）⇒ null（不硬套分档）', () => {
    expect(parseInjectionRiskLevel('high')).toBeNull()
    expect(parseInjectionRiskLevel('critical')).toBeNull()
  })

  it('★★★ 越界 / 非数字 / 非字符串 ⇒ null', () => {
    expect(parseInjectionRiskLevel('0')).toBeNull()
    expect(parseInjectionRiskLevel('11')).toBeNull()
    expect(parseInjectionRiskLevel('7.5')).toBeNull()
    expect(parseInjectionRiskLevel('abc')).toBeNull()
    expect(parseInjectionRiskLevel(null)).toBeNull()
    expect(parseInjectionRiskLevel(undefined)).toBeNull()
    expect(parseInjectionRiskLevel(7 as never)).toBeNull()
  })

  it('★ 带空白的 " 7 " 也能解析', () => {
    expect(parseInjectionRiskLevel(' 7 ')).toBe(7)
  })
})

describe('enum 常量照抄 schema', () => {
  it('★★★ injection_category 是 15 个值，且顺序与 schema 一致', () => {
    expect(INJECTION_CATEGORIES.length).toBe(15)
    expect([...INJECTION_CATEGORIES][0]).toBe('role_hijack')
    expect([...INJECTION_CATEGORIES][14]).toBe('tool_abuse')
  })

  it('★★★ injection_action 是 11 个值', () => {
    expect(INJECTION_ACTIONS.length).toBe(11)
    expect([...INJECTION_ACTIONS]).toContain('quarantine')
    expect([...INJECTION_ACTIONS]).toContain('block')
  })

  it('★★ severity_level 只有 low/medium/high/critical 四个（DB CHECK）', () => {
    expect([...INJECTION_SEVERITY_LEVELS]).toEqual(['low', 'medium', 'high', 'critical'])
  })
})

describe('筛选参数（各端点语义不同）', () => {
  it('★★ rules 的 search 是子串（不转义、不加 %）', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ rules: [], count: 0 }))
    await fetchInjectionRules({ search: '  role  ' })
    expect(lastUrl()).toContain('search=role')
    // ★ 客户端不自己加 %，后端 SQL 自己拼 ILIKE '%q%'
    expect(lastUrl()).not.toContain('%25')
  })

  it('★★ rules 的 type / category 空白不发', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ rules: [], count: 0 }))
    await fetchInjectionRules({ type: '  ', category: '' })
    expect(lastUrl()).not.toContain('=')
  })

  it('★★ detections 的四个筛选项空白不发', async () => {
    fetchMock.mockResolvedValue(jsonResponse(detResp()))
    await fetchInjectionDetections({ riskLevel: '  ', action: '', sessionKey: ' ', category: '  ' })
    const u = lastUrl()
    expect(u).not.toContain('risk_level=')
    expect(u).not.toContain('action=')
    expect(u).not.toContain('session_key=')
    expect(u).not.toContain('category=')
  })

  it('★ detections 的 category 是**数组包含**语义，名字与 rules 同但语义不同', async () => {
    fetchMock.mockResolvedValue(jsonResponse(detResp()))
    await fetchInjectionDetections({ category: 'jailbreak' })
    expect(lastUrl()).toContain('category=jailbreak')
  })
})

describe('形状不符抛错（500 不许退化成空清单）', () => {
  it('★★★ detections 缺 detections ⇒ 抛错', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ page: 1 }))
    await expect(fetchInjectionDetections()).rejects.toThrow(/形状不符/)
    // ★ 顺带把 unwrap 接进判据（而不是删 import）：同一形状直接调一次
    expect(() => unwrapInjectionDetections({ page: 1 })).toThrow(/形状不符/)
  })

  it('★★★ rules 缺 rules ⇒ 抛错', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ count: 0 }))
    await expect(fetchInjectionRules()).rejects.toThrow(/形状不符/)
  })

  it('★★★ engines 缺 engines ⇒ 抛错', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ count: 0 }))
    await expect(fetchInjectionEngines()).rejects.toThrow(/形状不符/)
  })

  it('★★★ canary-tokens 缺 tokens ⇒ 抛错', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ count: 0 }))
    await expect(fetchCanaryTokens()).rejects.toThrow(/形状不符/)
  })

  it('★★★ severity-matrix 缺 matrix ⇒ 抛错', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({}))
    await expect(fetchSeverityMatrix()).rejects.toThrow(/形状不符/)
  })

  it('★★★ attack-vectors 500 ⇒ 抛错原文（不显示成「没有攻击向量」）', async () => {
    fetchMock.mockResolvedValueOnce({
      ok: false,
      status: 500,
      json: async () => ({ error: 'Failed to list vectors' }),
      text: async () => JSON.stringify({ error: 'Failed to list vectors' }),
      headers: new Headers({ 'content-type': 'application/json' }),
    } as unknown as Response)
    await expect(fetchAttackVectors()).rejects.toThrow(/Failed to list vectors/)
  })

  it('★ unwrap 可单测且文案说清实得类型', () => {
    expect(() => unwrapAttackVectors(null)).toThrow(/实得 null/)
    expect(() => unwrapInjectionRules('x')).toThrow(/实得 string/)
    expect(() => unwrapInjectionEngines([])).toThrow(/实得 array/)
    expect(() => unwrapSeverityMatrix(null)).toThrow(/形状不符/)
    expect(() => unwrapCanaryTokens('x')).toThrow(/形状不符/)
  })

  it('★ 合法的空清单必须被接受（真的空，不是错）', () => {
    expect(unwrapInjectionRules({ rules: [], count: 0 }).rules).toEqual([])
    expect(unwrapAttackVectors({ vectors: [], page: 1, page_size: 20 }).vectors).toEqual([])
  })
})