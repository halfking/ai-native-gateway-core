import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  fetchSessionAnalyticsFilterOptions,
  unwrapSessionAnalyticsFilterOptions,
  filterOptionList,
  sessionAnalyticsFilterOptionsIsEmpty,
  sessionAnalyticsFilterOptionsHasAnyValue,
  filterOptionValuesAreSortedUnique,
  SESSION_ANALYTICS_FILTER_OPTIONS_PATH,
  SESSION_ANALYTICS_FILTER_OPTION_KEYS,
  SESSION_ANALYTICS_FILTER_WINDOW_DAYS,
  type SessionAnalyticsFilterOptions,
} from './sessionAnalyticsFilterOptions'

/**
 * 会话分析页模型/提供商筛选项的契约测试（2026-10-08，第九十七批）。
 *
 * 后端：`admin/session_analytics_top.go:190-272`（handler）+ `:47-50`（响应结构体）
 * + `admin/session_tenant.go:126-131`（effectiveScopeTenant）
 * + `admin/session_analytics_handler.go:728-729`（分发器 case）
 * + `cmd/gateway/main.go:7186`（前缀 catch-all 注册）+ `main_admin_wrappers.go:42-64`（双模式鉴权）。
 *
 * ★ 本批的价值在于**与批 96 的同叶名兄弟端点的六处相反**，逐条钉在用例里。
 * ⚠️ 全部用例标题**字面量**写（不用 `it.each`）—— 变异 harness 靠标题取锚点。
 * ⚠️ 所有「逐个都要检查」的循环都刻意用**字面量数组**，不用被测常量。
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

/** 删键造「键缺」—— ★ 必须用 `delete`，`undefined` 会造出显式 undefined 键。 */
function del(obj: Record<string, unknown>, key: string): Record<string, unknown> {
  const c = { ...obj }
  delete c[key]
  return c
}

// ═══════════════════════════════════════════════════════════════════════════
// 夹具
// ═══════════════════════════════════════════════════════════════════════════

/** ★★ 有数据的一份：键序照抄 `FilterOptionsResponse`（session_analytics_top.go:48-49）。 */
function populated(): Record<string, unknown> {
  return {
    models: ['claude-sonnet-4', 'gpt-4o', 'qwen3-max'],
    providers: ['anthropic', 'openai', 'qwen'],
  }
}

/**
 * ★★★★★ **两个键都是裸 `null`** —— 这就是 (2) 的核心形状：
 * `var models []string` 从 nil 开始、只被 `append`（`:232-234`、`:243-245`）⇒ 零行仍是 nil。
 */
function bothNull(): Record<string, unknown> {
  return { models: null, providers: null }
}

/** ★★★ (3) **半空形状**：有模型但没有提供商 ⇒ 两个键的编码可以不同。 */
function modelsOnly(): Record<string, unknown> {
  return { models: ['gpt-4o'], providers: null }
}

/** ★★★ (3) 反过来的半空形状。 */
function providersOnly(): Record<string, unknown> {
  return { models: null, providers: ['openai'] }
}

/** ★★ 「查到了、一行都没匹配」⇒ 走的是 `[]` 而不是 `null`。 */
function bothEmptyArrays(): Record<string, unknown> {
  return { models: [], providers: [] }
}

// ═══════════════════════════════════════════════════════════════════════════
// 常量与路径
// ═══════════════════════════════════════════════════════════════════════════

describe('会话筛选项 · 常量与路径', () => {
  it('路径是 /api/admin/session-analytics/filter-options（main.go:7186 前缀 catch-all）', () => {
    expect(SESSION_ANALYTICS_FILTER_OPTIONS_PATH).toBe('/api/admin/session-analytics/filter-options')
  })

  it('响应只有 2 个键（session_analytics_top.go:48-49）', () => {
    expect(SESSION_ANALYTICS_FILTER_OPTION_KEYS).toHaveLength(2)
    expect(SESSION_ANALYTICS_FILTER_OPTION_KEYS).toEqual(['models', 'providers'])
  })

  it('窗口是 30 天且这次头注与代码相符（session_analytics_top.go:210 223）', () => {
    expect(SESSION_ANALYTICS_FILTER_WINDOW_DAYS).toBe(30)
  })

  // ★ 与批 96 的对照钉在这里：兄弟端点是 8 键、且 5 个维度没有时间窗。
  it('★ 与兄弟端点不同：这里只有 2 个维度，两个都在同一个 30 天窗口内', () => {
    expect(SESSION_ANALYTICS_FILTER_OPTION_KEYS).toHaveLength(2)
    expect(SESSION_ANALYTICS_FILTER_WINDOW_DAYS).toBe(30)
  })
})

// ═══════════════════════════════════════════════════════════════════════════
// 顶层形状
// ═══════════════════════════════════════════════════════════════════════════

describe('会话筛选项 · 顶层形状', () => {
  it('顶层是数组时抛错（分发器 case 只在 len(parts)==1 时命中）', () => {
    expect(() => unwrapSessionAnalyticsFilterOptions([])).toThrow(/响应形状不符：期望裸对象，实得 array/)
  })

  it('顶层是 null 时抛错', () => {
    expect(() => unwrapSessionAnalyticsFilterOptions(null)).toThrow(/响应形状不符：期望裸对象，实得 null/)
  })

  it('顶层是字符串时抛错', () => {
    expect(() => unwrapSessionAnalyticsFilterOptions('ok')).toThrow(/响应形状不符：期望裸对象，实得 string/)
  })

  it('顶层是数字 42 时抛错并报 number', () => {
    expect(() => unwrapSessionAnalyticsFilterOptions(42)).toThrow(/响应形状不符：期望裸对象，实得 number/)
  })

  // ★ 下面两条是「falsy 但不是 null」那一格：`v === null` 换成 `!v` 时只有它们分得开。
  it('顶层是 0 时报 number 而不是 null', () => {
    expect(() => unwrapSessionAnalyticsFilterOptions(0)).toThrow(/响应形状不符：期望裸对象，实得 number/)
  })

  it('顶层是空串时报 string 而不是 null', () => {
    expect(() => unwrapSessionAnalyticsFilterOptions('')).toThrow(/响应形状不符：期望裸对象，实得 string/)
  })

  it('有数据的一份通过（session_analytics_top.go:268-271）', () => {
    const o = unwrapSessionAnalyticsFilterOptions(populated())
    expect(o.models).toEqual(['claude-sonnet-4', 'gpt-4o', 'qwen3-max'])
    expect(o.providers).toEqual(['anthropic', 'openai', 'qwen'])
  })

  // ★★ 这一条是本批的中心：null 必须放行，否则「网关还没跑过流量」会整页报错。
  it('★★ 两个键都是裸 null 时通过（session_analytics_top.go:232-234 零行即 nil）', () => {
    const o = unwrapSessionAnalyticsFilterOptions(bothNull())
    expect(o.models).toBeNull()
    expect(o.providers).toBeNull()
  })

  it('两个键都是空数组时通过（与 null 是不同的两态）', () => {
    const o = unwrapSessionAnalyticsFilterOptions(bothEmptyArrays())
    expect(o.models).toEqual([])
    expect(o.providers).toEqual([])
  })

  it('★ 半空形状 models 非空 providers 为 null 时通过', () => {
    const o = unwrapSessionAnalyticsFilterOptions(modelsOnly())
    expect(o.models).toEqual(['gpt-4o'])
    expect(o.providers).toBeNull()
  })

  it('★ 半空形状 models 为 null providers 非空时通过', () => {
    const o = unwrapSessionAnalyticsFilterOptions(providersOnly())
    expect(o.models).toBeNull()
    expect(o.providers).toEqual(['openai'])
  })
})

// ═══════════════════════════════════════════════════════════════════════════
// 顶层缺键
// ═══════════════════════════════════════════════════════════════════════════

describe('会话筛选项 · 顶层缺键', () => {
  it('缺 models 时抛错并点名 models', () => {
    expect(() => unwrapSessionAnalyticsFilterOptions(del(populated(), 'models'))).toThrow(
      /会话筛选项 缺 1 个键（models）/,
    )
  })

  it('缺 providers 时抛错并点名 providers', () => {
    expect(() => unwrapSessionAnalyticsFilterOptions(del(populated(), 'providers'))).toThrow(
      /会话筛选项 缺 1 个键（providers）/,
    )
  })

  it('两个键都缺时一次点名两个', () => {
    expect(() => unwrapSessionAnalyticsFilterOptions({})).toThrow(/会话筛选项 缺 2 个键（models, providers）/)
  })
})

// ═══════════════════════════════════════════════════════════════════════════
// 槽位类型：null / 数组 / 其余
// ═══════════════════════════════════════════════════════════════════════════

describe('会话筛选项 · 槽位类型', () => {
  it('models 是字符串时抛错并点名 models', () => {
    expect(() => unwrapSessionAnalyticsFilterOptions({ ...populated(), models: 'gpt-4o' })).toThrow(
      /会话筛选项 的 models 不是数组也不是 null（实得 string）/,
    )
  })

  it('★ models 是 0 时抛错并报 number（falsy 但不是 null 那一格）', () => {
    expect(() => unwrapSessionAnalyticsFilterOptions({ ...populated(), models: 0 })).toThrow(
      /会话筛选项 的 models 不是数组也不是 null（实得 number）/,
    )
  })

  it('★ models 是空串时抛错并报 string', () => {
    expect(() => unwrapSessionAnalyticsFilterOptions({ ...populated(), models: '' })).toThrow(
      /会话筛选项 的 models 不是数组也不是 null（实得 string）/,
    )
  })

  it('models 是对象时抛错并点名 models', () => {
    expect(() => unwrapSessionAnalyticsFilterOptions({ ...populated(), models: { a: 1 } })).toThrow(
      /会话筛选项 的 models 不是数组也不是 null（实得 object）/,
    )
  })

  it('models 是布尔时抛错并点名 models', () => {
    expect(() => unwrapSessionAnalyticsFilterOptions({ ...populated(), models: false })).toThrow(
      /会话筛选项 的 models 不是数组也不是 null（实得 boolean）/,
    )
  })

  it('providers 是字符串时抛错并点名 providers', () => {
    expect(() => unwrapSessionAnalyticsFilterOptions({ ...populated(), providers: 'openai' })).toThrow(
      /会话筛选项 的 providers 不是数组也不是 null（实得 string）/,
    )
  })

  it('providers 是数字时抛错并点名 providers', () => {
    expect(() => unwrapSessionAnalyticsFilterOptions({ ...populated(), providers: 1 })).toThrow(
      /会话筛选项 的 providers 不是数组也不是 null（实得 number）/,
    )
  })

  it('providers 是对象时抛错并点名 providers', () => {
    expect(() => unwrapSessionAnalyticsFilterOptions({ ...populated(), providers: {} })).toThrow(
      /会话筛选项 的 providers 不是数组也不是 null（实得 object）/,
    )
  })

  // ★★ 与批 96 的对照：兄弟端点只有「数组」一态（它恒发 []），
  //    这里多出 null 一态 ⇒ 两个解包器不能共用。
  it('★ models 是 null 时不抛错（多出来的第三态）', () => {
    expect(() => unwrapSessionAnalyticsFilterOptions(modelsOnly())).not.toThrow()
  })

  it('★ providers 是 null 时不抛错（多出来的第三态）', () => {
    expect(() => unwrapSessionAnalyticsFilterOptions(providersOnly())).not.toThrow()
  })
})

// ═══════════════════════════════════════════════════════════════════════════
// 槽位元素类型
// ═══════════════════════════════════════════════════════════════════════════

describe('会话筛选项 · 槽位元素类型', () => {
  it('models 的第 0 项是数字时抛错并点名第 0 项', () => {
    expect(() => unwrapSessionAnalyticsFilterOptions({ ...populated(), models: [1] })).toThrow(
      /会话筛选项 的 models 的第 0 项不是字符串/,
    )
  })

  it('models 的第 2 项是 null 时抛错并点名第 2 项', () => {
    expect(() => unwrapSessionAnalyticsFilterOptions({ ...populated(), models: ['a', 'b', null] })).toThrow(
      /会话筛选项 的 models 的第 2 项不是字符串/,
    )
  })

  it('models 的第 0 项是数组时抛错并点名第 0 项', () => {
    expect(() => unwrapSessionAnalyticsFilterOptions({ ...populated(), models: [[]] })).toThrow(
      /会话筛选项 的 models 的第 0 项不是字符串/,
    )
  })

  it('models 的第 1 项是 undefined 时抛错并点名第 1 项', () => {
    expect(() => unwrapSessionAnalyticsFilterOptions({ ...populated(), models: ['a', undefined] })).toThrow(
      /会话筛选项 的 models 的第 1 项不是字符串/,
    )
  })

  it('providers 的第 0 项是对象时抛错并点名第 0 项', () => {
    expect(() => unwrapSessionAnalyticsFilterOptions({ ...populated(), providers: [{}] })).toThrow(
      /会话筛选项 的 providers 的第 0 项不是字符串/,
    )
  })

  it('providers 的第 1 项是布尔时抛错并点名第 1 项', () => {
    expect(() => unwrapSessionAnalyticsFilterOptions({ ...populated(), providers: ['openai', true] })).toThrow(
      /会话筛选项 的 providers 的第 1 项不是字符串/,
    )
  })
})

// ═══════════════════════════════════════════════════════════════════════════
// fetch
// ═══════════════════════════════════════════════════════════════════════════

describe('会话筛选项 · fetch', () => {
  it('发 GET 到 /api/admin/session-analytics/filter-options 且不带查询串', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(populated()))
    await fetchSessionAnalyticsFilterOptions()
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toContain('/api/admin/session-analytics/filter-options')
    expect(url).not.toContain('?')
    expect(init.method).toBe('GET')
  })

  // ★★ (5) 这个端点**没有** ?tenant= 收窄手段 ⇒ 请求里出现 tenant 就是错的。
  it('★ 请求里不带 tenant：这个端点无法把 super_admin 收窄到单个租户', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(populated()))
    await fetchSessionAnalyticsFilterOptions()
    const [url] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).not.toContain('tenant')
  })

  it('★ 两键皆 null 时 resolve 而不是 reject', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(bothNull()))
    const o = await fetchSessionAnalyticsFilterOptions()
    expect(o.models).toBeNull()
  })

  it('响应形状不符时 Promise reject', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse('nope'))
    await expect(fetchSessionAnalyticsFilterOptions()).rejects.toThrow(/响应形状不符/)
  })

  it('键缺时 Promise reject', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(del(populated(), 'providers')))
    await expect(fetchSessionAnalyticsFilterOptions()).rejects.toThrow(/缺 1 个键（providers）/)
  })

  it('★ 槽位是对象时 Promise reject（键齐全、只有 models 类型错）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ ...populated(), models: { a: 1 } }))
    await expect(fetchSessionAnalyticsFilterOptions()).rejects.toThrow(/models 不是数组也不是 null/)
  })

  it('★ 槽位元素是数字时 Promise reject', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ ...populated(), providers: [200] }))
    await expect(fetchSessionAnalyticsFilterOptions()).rejects.toThrow(/providers 的第 0 项不是字符串/)
  })
})

// ═══════════════════════════════════════════════════════════════════════════
// (2) null 归一
// ═══════════════════════════════════════════════════════════════════════════

describe('会话筛选项 · null 归一 (2)', () => {
  it('null 归一成空数组', () => {
    expect(filterOptionList(null)).toEqual([])
  })

  it('空数组原样返回', () => {
    expect(filterOptionList([])).toEqual([])
  })

  it('有值的数组原样返回', () => {
    expect(filterOptionList(['gpt-4o', 'qwen3-max'])).toEqual(['gpt-4o', 'qwen3-max'])
  })

  // ★★★ 下面三条是「漏掉 ?? 」那条变异的区分点：不归一时它们会抛 TypeError。
  it('★ 两键皆 null 时空判定为 true', () => {
    expect(sessionAnalyticsFilterOptionsIsEmpty(unwrapSessionAnalyticsFilterOptions(bothNull()))).toBe(true)
  })

  it('★ 两键皆空数组时空判定为 true', () => {
    expect(sessionAnalyticsFilterOptionsIsEmpty(unwrapSessionAnalyticsFilterOptions(bothEmptyArrays()))).toBe(
      true,
    )
  })

  it('★ 两键皆 null 时没有任何值', () => {
    expect(
      sessionAnalyticsFilterOptionsHasAnyValue(unwrapSessionAnalyticsFilterOptions(bothNull())),
    ).toBe(false)
  })

  it('★ 只有 models 非空时空判定为 false（半空形状）', () => {
    expect(sessionAnalyticsFilterOptionsIsEmpty(unwrapSessionAnalyticsFilterOptions(modelsOnly()))).toBe(
      false,
    )
  })

  it('★ 只有 providers 非空时空判定为 false（半空形状）', () => {
    expect(
      sessionAnalyticsFilterOptionsIsEmpty(unwrapSessionAnalyticsFilterOptions(providersOnly())),
    ).toBe(false)
  })

  it('★ 半空形状时确实取到值', () => {
    expect(
      sessionAnalyticsFilterOptionsHasAnyValue(unwrapSessionAnalyticsFilterOptions(modelsOnly())),
    ).toBe(true)
  })

  it('半空形状时 providers 那一侧归一后仍是空数组', () => {
    const o = unwrapSessionAnalyticsFilterOptions(modelsOnly())
    expect(filterOptionList(o.providers)).toEqual([])
  })

  it('完整的一份时空判定为 false', () => {
    expect(sessionAnalyticsFilterOptionsIsEmpty(unwrapSessionAnalyticsFilterOptions(populated()))).toBe(false)
  })

  it('完整的一份时确实取到值', () => {
    expect(
      sessionAnalyticsFilterOptionsHasAnyValue(unwrapSessionAnalyticsFilterOptions(populated())),
    ).toBe(true)
  })

  // ★ 这条同时是**类型级**判据：两个键的类型必须是 `string[] | null`，
  //   写成 `string[]` 就会在移动端最常见的「还没跑过流量」场景里崩掉。
  it('解包结果的键类型是 string[] | null（★ 不是 string[]）', () => {
    const o: SessionAnalyticsFilterOptions = unwrapSessionAnalyticsFilterOptions(bothNull())
    const models: string[] | null = o.models
    expect(models).toBeNull()
    expect(filterOptionList(models)).toEqual([])
  })
})

// ═══════════════════════════════════════════════════════════════════════════
// (9) 顺序与去重：这一侧是可观测的
// ═══════════════════════════════════════════════════════════════════════════

describe('会话筛选项 · 升序与去重 (9)', () => {
  it('按值升序的列表判定为真（session_analytics_top.go:216 ORDER BY model）', () => {
    expect(filterOptionValuesAreSortedUnique(['claude-sonnet-4', 'gpt-4o', 'qwen3-max'])).toBe(true)
  })

  it('乱序的列表判定为假', () => {
    expect(filterOptionValuesAreSortedUnique(['qwen3-max', 'claude-sonnet-4', 'gpt-4o'])).toBe(false)
  })

  it('★ 完全逆序判定为假', () => {
    expect(filterOptionValuesAreSortedUnique(['c', 'b', 'a'])).toBe(false)
  })

  it('★ 有重复项时判定为假（DISTINCT 挡住的那种形状）', () => {
    expect(filterOptionValuesAreSortedUnique(['a', 'a', 'b'])).toBe(false)
  })

  it('★ 三项全同时判定为假', () => {
    expect(filterOptionValuesAreSortedUnique(['a', 'a', 'a'])).toBe(false)
  })

  it('相邻重复也算重复（两两比较而不是与首项比）', () => {
    expect(filterOptionValuesAreSortedUnique(['a', 'b', 'b', 'c'])).toBe(false)
  })

  it('空列表判定为真（真空真，不是漏判）', () => {
    expect(filterOptionValuesAreSortedUnique([])).toBe(true)
  })

  it('单项列表判定为真', () => {
    expect(filterOptionValuesAreSortedUnique(['gpt-4o'])).toBe(true)
  })

  it('★ 空串排在最前时判定为真（ASCII 序，空串最小）', () => {
    expect(filterOptionValuesAreSortedUnique(['', 'a', 'b'])).toBe(true)
  })

  it('★ 含空串的完整响应仍然通过升序判定', () => {
    const o = unwrapSessionAnalyticsFilterOptions({ models: ['', 'a', 'b'], providers: [''] })
    expect(filterOptionValuesAreSortedUnique(filterOptionList(o.models))).toBe(true)
    expect(filterOptionValuesAreSortedUnique(filterOptionList(o.providers))).toBe(true)
  })

  it('★ 含空串的完整响应里，空判定仍然是 false（空串是值不是空列表）', () => {
    const o = unwrapSessionAnalyticsFilterOptions({ models: [''], providers: null })
    expect(sessionAnalyticsFilterOptionsIsEmpty(o)).toBe(false)
  })

  it('★ 大小写混排按 ASCII 序判定（Q 在 a 之前）', () => {
    expect(filterOptionValuesAreSortedUnique(['Q', 'a'])).toBe(true)
  })

  it('★ 大小写反序判定为假', () => {
    expect(filterOptionValuesAreSortedUnique(['a', 'Q'])).toBe(false)
  })
})