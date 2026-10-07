import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  fetchTurnsFilterOptions,
  unwrapTurnsFilterOptions,
  apiKeyFilterOptions,
  turnsFilterOptionsIsEmpty,
  turnsFilterOptionsHasAnyValue,
  turnsFilterOptionIsTruncated,
  statusCodeFilterValue,
  TURNS_FILTER_OPTIONS_PATH,
  TURNS_FILTER_OPTION_KEYS,
  TURNS_FILTER_OPTION_LIMIT,
  TURNS_TURN_DIMENSION_WINDOW_DAYS,
  TURNS_CLIENT_VALUE_SOURCES,
  type TurnsFilterOptions,
} from './turnsFilterOptions'

/**
 * 轮次列表页筛选框可选值的契约测试（2026-10-08，第九十六批）。
 *
 * 后端：`admin/turns_filter_options.go:49-130`（handler）+ `:37-46`（响应结构体）
 * + `:136-154`（三段查询拼装）+ `:157-181`（runFilterOptionQueries），
 * 由 `admin/handler.go:1277` 用 **`admin(...)`** 挂载（★ **admin 档**）。
 *
 * 重点是源文件头写明的十二件事 (1)…(12)。带 ★ 的自校验判据都能被变异打掉。
 * ⚠️ 全部用例标题**字面量**写（不用 `it.each`）—— 变异 harness 靠标题取锚点。
 * ⚠️ 所有「逐个都要检查」的循环都刻意用**字面量数组**，不用被测常量。
 * ⚠️ 夹具键序照抄结构体声明顺序（`turns_filter_options.go:38-45`）。
 * ⚠️ 正好 20 项的夹具用**字面量 20** 生成，不引用 `TURNS_FILTER_OPTION_LIMIT`。
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
// 夹具：键序照抄 `TurnsFilterOptionsResponse`（turns_filter_options.go:38-45）
// ═══════════════════════════════════════════════════════════════════════════

/** ★★ 完整的一份：每个维度都有值。 */
function populated(): Record<string, unknown> {
  return {
    projects: ['proj-alpha', 'proj-beta'],
    tasks: ['task-摘要', 'task-代码审查'],
    owners: ['alice@example.com', 'bob@example.com'],
    // ★ (9) client_id ∪ application_code 混在一起
    clients: ['web-ui', 'cli-agent'],
    tags: ['nightly', 'hotfix'],
    models: ['gpt-4o', 'claude-sonnet-4'],
    providers: ['openai', 'anthropic'],
    // ★★★ (7) 元素是 integer::text 的数字串
    status_codes: ['200', '0', '404'],
  }
}

/** ★★ (2) 8 个维度全空 —— `*targets[i] = []string{}` 让空是 `[]` 不是 `null`。 */
function allEmpty(): Record<string, unknown> {
  return {
    projects: [],
    tasks: [],
    owners: [],
    clients: [],
    tags: [],
    models: [],
    providers: [],
    status_codes: [],
  }
}

/**
 * ★★ 正好 20 项的维度 —— (6) 的**区分格**：`>=` 与 `>` 只在正好 20 时不同，
 *   而 `LIMIT 20` 完全能返回正好 20。★ 用字面量 20，不引用被测常量。
 */
function twentyValues(): string[] {
  return Array.from({ length: 20 }, (_, i) => `proj-${String(i).padStart(2, '0')}`)
}

// ═══════════════════════════════════════════════════════════════════════════
// 顶层形状
// ═══════════════════════════════════════════════════════════════════════════

describe('轮次筛选项 · 顶层形状', () => {
  it('顶层是数组时抛错（turns_filter_options.go:129 writeJSON 传结构体）', () => {
    expect(() => unwrapTurnsFilterOptions([])).toThrow(/响应形状不符：期望裸对象，实得 array/)
  })

  it('顶层是 null 时抛错', () => {
    expect(() => unwrapTurnsFilterOptions(null)).toThrow(/响应形状不符：期望裸对象，实得 null/)
  })

  it('顶层是字符串时抛错', () => {
    expect(() => unwrapTurnsFilterOptions('ok')).toThrow(/响应形状不符：期望裸对象，实得 string/)
  })

  it('顶层是数字 42 时抛错并报 number', () => {
    expect(() => unwrapTurnsFilterOptions(42)).toThrow(/响应形状不符：期望裸对象，实得 number/)
  })

  // ★★ 下面两条是「falsy 但不是 null」那一格：`v === null` 换成 `!v` 时只有它们分得开。
  it('顶层是 0 时报 number 而不是 null', () => {
    expect(() => unwrapTurnsFilterOptions(0)).toThrow(/响应形状不符：期望裸对象，实得 number/)
  })

  it('顶层是空串时报 string 而不是 null', () => {
    expect(() => unwrapTurnsFilterOptions('')).toThrow(/响应形状不符：期望裸对象，实得 string/)
  })

  it('8 个维度全空时通过（turns_filter_options.go:166 赋空切片）', () => {
    const o = unwrapTurnsFilterOptions(allEmpty())
    expect(o.models).toEqual([])
    expect(o.status_codes).toEqual([])
  })

  it('完整的一份通过（turns_filter_options.go:38-45）', () => {
    const o = unwrapTurnsFilterOptions(populated())
    expect(o.projects).toEqual(['proj-alpha', 'proj-beta'])
    expect(o.status_codes).toEqual(['200', '0', '404'])
  })

  it('路径是 /api/admin/turns/sessions/filter-options（handler.go:1277）', () => {
    expect(TURNS_FILTER_OPTIONS_PATH).toBe('/api/admin/turns/sessions/filter-options')
  })

  it('响应是 8 个键（turns_filter_options.go:38-45，★ 桌面多一个 api_keys）', () => {
    expect(TURNS_FILTER_OPTION_KEYS).toHaveLength(8)
    expect(TURNS_FILTER_OPTION_KEYS).toEqual([
      'projects',
      'tasks',
      'owners',
      'clients',
      'tags',
      'models',
      'providers',
      'status_codes',
    ])
  })

  it('每个维度的上限是 20（turns_filter_options.go:33）', () => {
    expect(TURNS_FILTER_OPTION_LIMIT).toBe(20)
  })

  it('30 天窗口只覆盖 session_turns 那三个维度（turns_filter_options.go:150-153）', () => {
    expect(TURNS_TURN_DIMENSION_WINDOW_DAYS).toBe(30)
  })

  it('clients 维度有两个来源列（turns_filter_options.go:97 与 :100）', () => {
    expect(TURNS_CLIENT_VALUE_SOURCES).toEqual(['client_id', 'application_code'])
  })
})

// ═══════════════════════════════════════════════════════════════════════════
// 顶层缺键：8 个维度逐个点名
// ═══════════════════════════════════════════════════════════════════════════

describe('轮次筛选项 · 顶层缺键', () => {
  it('缺 projects 时抛错并点名 projects', () => {
    expect(() => unwrapTurnsFilterOptions(del(populated(), 'projects'))).toThrow(
      /轮次筛选项 缺 1 个键（projects）/,
    )
  })

  it('缺 tasks 时抛错并点名 tasks', () => {
    expect(() => unwrapTurnsFilterOptions(del(populated(), 'tasks'))).toThrow(
      /轮次筛选项 缺 1 个键（tasks）/,
    )
  })

  it('缺 owners 时抛错并点名 owners', () => {
    expect(() => unwrapTurnsFilterOptions(del(populated(), 'owners'))).toThrow(
      /轮次筛选项 缺 1 个键（owners）/,
    )
  })

  it('缺 clients 时抛错并点名 clients', () => {
    expect(() => unwrapTurnsFilterOptions(del(populated(), 'clients'))).toThrow(
      /轮次筛选项 缺 1 个键（clients）/,
    )
  })

  it('缺 tags 时抛错并点名 tags', () => {
    expect(() => unwrapTurnsFilterOptions(del(populated(), 'tags'))).toThrow(/轮次筛选项 缺 1 个键（tags）/)
  })

  it('缺 models 时抛错并点名 models', () => {
    expect(() => unwrapTurnsFilterOptions(del(populated(), 'models'))).toThrow(
      /轮次筛选项 缺 1 个键（models）/,
    )
  })

  it('缺 providers 时抛错并点名 providers', () => {
    expect(() => unwrapTurnsFilterOptions(del(populated(), 'providers'))).toThrow(
      /轮次筛选项 缺 1 个键（providers）/,
    )
  })

  it('缺 status_codes 时抛错并点名 status_codes', () => {
    expect(() => unwrapTurnsFilterOptions(del(populated(), 'status_codes'))).toThrow(
      /轮次筛选项 缺 1 个键（status_codes）/,
    )
  })

  it('同时缺两个维度时一次点名两个', () => {
    const bad = del(del(populated(), 'tags'), 'models')
    expect(() => unwrapTurnsFilterOptions(bad)).toThrow(/轮次筛选项 缺 2 个键（tags, models）/)
  })

  it('空对象时一次点名 8 个键', () => {
    expect(() => unwrapTurnsFilterOptions({})).toThrow(/轮次筛选项 缺 8 个键/)
  })
})

// ═══════════════════════════════════════════════════════════════════════════
// 维度类型：不是数组
// ═══════════════════════════════════════════════════════════════════════════

describe('轮次筛选项 · 维度不是数组', () => {
  it('projects 是 null 时抛错并点名 projects', () => {
    expect(() => unwrapTurnsFilterOptions({ ...populated(), projects: null })).toThrow(
      /轮次筛选项 的 projects 不是数组（实得 null）/,
    )
  })

  it('tasks 是字符串时抛错并点名 tasks', () => {
    expect(() => unwrapTurnsFilterOptions({ ...populated(), tasks: 'task-1' })).toThrow(
      /轮次筛选项 的 tasks 不是数组（实得 string）/,
    )
  })

  it('owners 是对象时抛错并点名 owners', () => {
    expect(() => unwrapTurnsFilterOptions({ ...populated(), owners: { a: 1 } })).toThrow(
      /轮次筛选项 的 owners 不是数组（实得 object）/,
    )
  })

  it('★ clients 是 0 时抛错并报 number（falsy 但不是 null 那一格）', () => {
    expect(() => unwrapTurnsFilterOptions({ ...populated(), clients: 0 })).toThrow(
      /轮次筛选项 的 clients 不是数组（实得 number）/,
    )
  })

  it('★ clients 是空串时抛错并报 string', () => {
    expect(() => unwrapTurnsFilterOptions({ ...populated(), clients: '' })).toThrow(
      /轮次筛选项 的 clients 不是数组（实得 string）/,
    )
  })

  it('tags 是布尔时抛错并点名 tags', () => {
    expect(() => unwrapTurnsFilterOptions({ ...populated(), tags: true })).toThrow(
      /轮次筛选项 的 tags 不是数组（实得 boolean）/,
    )
  })

  it('models 是对象时抛错并点名 models', () => {
    expect(() => unwrapTurnsFilterOptions({ ...populated(), models: { gpt: 1 } })).toThrow(
      /轮次筛选项 的 models 不是数组（实得 object）/,
    )
  })

  it('providers 是空对象时抛错并点名 providers', () => {
    expect(() => unwrapTurnsFilterOptions({ ...populated(), providers: {} })).toThrow(
      /轮次筛选项 的 providers 不是数组（实得 object）/,
    )
  })

  it('status_codes 是 0 时抛错并点名 status_codes', () => {
    expect(() => unwrapTurnsFilterOptions({ ...populated(), status_codes: 0 })).toThrow(
      /轮次筛选项 的 status_codes 不是数组（实得 number）/,
    )
  })
})

// ═══════════════════════════════════════════════════════════════════════════
// 维度元素类型
// ═══════════════════════════════════════════════════════════════════════════

describe('轮次筛选项 · 维度元素类型', () => {
  it('projects 的第 0 项是数字时抛错并点名第 0 项', () => {
    expect(() => unwrapTurnsFilterOptions({ ...populated(), projects: [1] })).toThrow(
      /轮次筛选项 的 projects 的第 0 项不是字符串/,
    )
  })

  it('tasks 的第 2 项是 null 时抛错并点名第 2 项', () => {
    expect(() => unwrapTurnsFilterOptions({ ...populated(), tasks: ['a', 'b', null] })).toThrow(
      /轮次筛选项 的 tasks 的第 2 项不是字符串/,
    )
  })

  it('owners 的第 0 项是数组时抛错并点名第 0 项', () => {
    expect(() => unwrapTurnsFilterOptions({ ...populated(), owners: [[]] })).toThrow(
      /轮次筛选项 的 owners 的第 0 项不是字符串/,
    )
  })

  it('clients 的第 1 项是对象时抛错并点名第 1 项', () => {
    expect(() => unwrapTurnsFilterOptions({ ...populated(), clients: ['web-ui', {}] })).toThrow(
      /轮次筛选项 的 clients 的第 1 项不是字符串/,
    )
  })

  it('tags 的第 0 项是布尔时抛错并点名第 0 项', () => {
    expect(() => unwrapTurnsFilterOptions({ ...populated(), tags: [false] })).toThrow(
      /轮次筛选项 的 tags 的第 0 项不是字符串/,
    )
  })

  it('models 的第 0 项是对象时抛错并点名第 0 项', () => {
    expect(() => unwrapTurnsFilterOptions({ ...populated(), models: [{ name: 'gpt' }] })).toThrow(
      /轮次筛选项 的 models 的第 0 项不是字符串/,
    )
  })

  it('providers 的第 1 项是 undefined 时抛错并点名第 1 项', () => {
    expect(() => unwrapTurnsFilterOptions({ ...populated(), providers: ['openai', undefined] })).toThrow(
      /轮次筛选项 的 providers 的第 1 项不是字符串/,
    )
  })

  // ★★★ (7) 这一格就是整份契约的要害：后端给的是字符串 "200"。
  it('★ status_codes 的第 0 项是数字 200 时抛错（turns_filter_options.go:122 ::text）', () => {
    expect(() => unwrapTurnsFilterOptions({ ...populated(), status_codes: [200] })).toThrow(
      /轮次筛选项 的 status_codes 的第 0 项不是字符串/,
    )
  })

  it('status_codes 的第 1 项是 null 时抛错并点名第 1 项', () => {
    expect(() => unwrapTurnsFilterOptions({ ...populated(), status_codes: ['200', null] })).toThrow(
      /轮次筛选项 的 status_codes 的第 1 项不是字符串/,
    )
  })
})

// ═══════════════════════════════════════════════════════════════════════════
// fetch
// ═══════════════════════════════════════════════════════════════════════════

describe('轮次筛选项 · fetch', () => {
  it('发 GET 到 /api/admin/turns/sessions/filter-options 且不带查询串', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(populated()))
    await fetchTurnsFilterOptions()
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toContain('/api/admin/turns/sessions/filter-options')
    expect(url).not.toContain('?')
    expect(init.method).toBe('GET')
  })

  it('★ 不带任何参数 ⇒ 租户作用域就是调用者自己那一个（turns.ts:205 亦然）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(allEmpty()))
    await fetchTurnsFilterOptions()
    const [url] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).not.toContain('tenant')
  })

  it('响应里的维度被原样解出', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(populated()))
    const o = await fetchTurnsFilterOptions()
    expect(o.clients).toEqual(['web-ui', 'cli-agent'])
    expect(o.owners).toHaveLength(2)
  })

  it('响应形状不符时 Promise reject', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse('nope'))
    await expect(fetchTurnsFilterOptions()).rejects.toThrow(/响应形状不符/)
  })

  it('维度缺键时 Promise reject', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(del(populated(), 'models')))
    await expect(fetchTurnsFilterOptions()).rejects.toThrow(/缺 1 个键（models）/)
  })

  // ★ 这条是「fetch 只做顶层 requireKeys、放过维度类型」那条变异的唯一区分点：
  //   顶层形状与键都齐全，只有 projects 是对象 ⇒ 只有校验维度类型才拦得住。
  it('维度类型错时 Promise reject（键齐全、只有 projects 是对象）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ ...populated(), projects: { a: 1 } }))
    await expect(fetchTurnsFilterOptions()).rejects.toThrow(/的 projects 不是数组（实得 object）/)
  })

  it('维度元素类型错时 Promise reject（status_codes 里放了数字）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ ...populated(), status_codes: [200] }))
    await expect(fetchTurnsFilterOptions()).rejects.toThrow(/status_codes 的第 0 项不是字符串/)
  })
})

// ═══════════════════════════════════════════════════════════════════════════
// (1) 桌面那个幽灵键 api_keys
// ═══════════════════════════════════════════════════════════════════════════

describe('轮次筛选项 · 桌面幽灵键 api_keys (1)', () => {
  it('★ 响应里没有 api_keys 时取到空数组（turns_filter_options.go:37-46 只有 8 键）', () => {
    const o = unwrapTurnsFilterOptions(populated())
    expect('api_keys' in (o as unknown as Record<string, unknown>)).toBe(false)
    expect(apiKeyFilterOptions(o)).toEqual([])
  })

  it('api_keys 是 null 时取到空数组', () => {
    const o = unwrapTurnsFilterOptions({ ...populated(), api_keys: null })
    expect(apiKeyFilterOptions(o)).toEqual([])
  })

  it('api_keys 是字符串时取到空数组', () => {
    const o = unwrapTurnsFilterOptions({ ...populated(), api_keys: 'k-1' })
    expect(apiKeyFilterOptions(o)).toEqual([])
  })

  it('api_keys 是对象时取到空数组', () => {
    const o = unwrapTurnsFilterOptions({ ...populated(), api_keys: { id: 1 } })
    expect(apiKeyFilterOptions(o)).toEqual([])
  })

  // ★★★ 这条是「只判 undefined、不判是不是数组」那条变异的区分格：
  //   字符串能被 for...of 迭代（逐字符被 continue 掉 ⇒ 仍是 []），
  //   **数字不能迭代** ⇒ 不做数组检查的实现会在这里抛 TypeError 而不是给 []。
  it('api_keys 是数字 0 时取到空数组而不是抛迭代异常', () => {
    const o = unwrapTurnsFilterOptions({ ...populated(), api_keys: 0 })
    expect(apiKeyFilterOptions(o)).toEqual([])
  })

  it('api_keys 是布尔 true 时取到空数组', () => {
    const o = unwrapTurnsFilterOptions({ ...populated(), api_keys: true })
    expect(apiKeyFilterOptions(o)).toEqual([])
  })

  it('api_keys 是空数组时取到空数组', () => {
    const o = unwrapTurnsFilterOptions({ ...populated(), api_keys: [] })
    expect(apiKeyFilterOptions(o)).toEqual([])
  })

  it('api_keys 元素合法时被取出（★ 后端不可达，仅固定容错行为）', () => {
    const o = unwrapTurnsFilterOptions({
      ...populated(),
      api_keys: [
        { id: 1, label: 'key-one' },
        { id: 2, label: 'key-two' },
      ],
    })
    expect(apiKeyFilterOptions(o)).toEqual([
      { id: 1, label: 'key-one' },
      { id: 2, label: 'key-two' },
    ])
  })

  it('api_keys 里混入非对象元素时被跳过', () => {
    const o = unwrapTurnsFilterOptions({ ...populated(), api_keys: [{ id: 1, label: 'ok' }, 'bad', null] })
    expect(apiKeyFilterOptions(o)).toEqual([{ id: 1, label: 'ok' }])
  })

  it('api_keys 里 id 是字符串时被跳过', () => {
    const o = unwrapTurnsFilterOptions({ ...populated(), api_keys: [{ id: '1', label: 'ok' }] })
    expect(apiKeyFilterOptions(o)).toEqual([])
  })

  it('api_keys 里 label 是数字时被跳过', () => {
    const o = unwrapTurnsFilterOptions({ ...populated(), api_keys: [{ id: 1, label: 7 }] })
    expect(apiKeyFilterOptions(o)).toEqual([])
  })

  it('api_keys 里数组元素被跳过（数组也是 object，必须显式排除）', () => {
    const o = unwrapTurnsFilterOptions({ ...populated(), api_keys: [[{ id: 1, label: 'ok' }]] })
    expect(apiKeyFilterOptions(o)).toEqual([])
  })

  // ★★★ 上面那条**只验得出元素不是对象**；这一条才逼出 `Array.isArray(item)` 那一行：
  //   数组上挂了 id/label 属性后，`typeof item === 'object'` 与两个字段检查**全部通过**
  //   ⇒ 只看 `typeof item` 的实现会把它当成合法选项塞进结果里。
  it('api_keys 里带 id 与 label 属性的数组元素也被跳过', () => {
    const arrayWithProps = Object.assign([1, 2], { id: 1, label: 'ok' })
    const o = unwrapTurnsFilterOptions({ ...populated(), api_keys: [arrayWithProps] })
    expect(apiKeyFilterOptions(o)).toEqual([])
  })

  it('api_keys 里全部元素非法时取到空数组', () => {
    const o = unwrapTurnsFilterOptions({ ...populated(), api_keys: [{ id: null, label: null }, 3, 'x'] })
    expect(apiKeyFilterOptions(o)).toEqual([])
  })
})

// ═══════════════════════════════════════════════════════════════════════════
// (2) 空与有值
// ═══════════════════════════════════════════════════════════════════════════

describe('轮次筛选项 · 空与非空 (2)', () => {
  it('8 个维度全空时判定为空', () => {
    expect(turnsFilterOptionsIsEmpty(unwrapTurnsFilterOptions(allEmpty()))).toBe(true)
  })

  it('全空时没有任何值', () => {
    expect(turnsFilterOptionsHasAnyValue(unwrapTurnsFilterOptions(allEmpty()))).toBe(false)
  })

  it('有值时判定为非空', () => {
    expect(turnsFilterOptionsIsEmpty(unwrapTurnsFilterOptions(populated()))).toBe(false)
  })

  it('有值时确实取到值', () => {
    expect(turnsFilterOptionsHasAnyValue(unwrapTurnsFilterOptions(populated()))).toBe(true)
  })

  it('★ 只有 status_codes 非空时也算有值（不能只看前几个维度）', () => {
    const onlyStatus = { ...allEmpty(), status_codes: ['200'] }
    expect(turnsFilterOptionsHasAnyValue(unwrapTurnsFilterOptions(onlyStatus))).toBe(true)
  })

  it('★ 只有 providers 非空时也算有值', () => {
    const onlyProvider = { ...allEmpty(), providers: ['openai'] }
    expect(turnsFilterOptionsIsEmpty(unwrapTurnsFilterOptions(onlyProvider))).toBe(false)
  })

  it('★ 只有 tags 非空时也算有值（无 30 天窗口的那个维度）', () => {
    const onlyTags = { ...allEmpty(), tags: ['nightly'] }
    expect(turnsFilterOptionsHasAnyValue(unwrapTurnsFilterOptions(onlyTags))).toBe(true)
  })

  it('★ 只有 tags 非空时判定为非空（无 30 天窗口的那个维度，不能在空判定里被漏掉）', () => {
    const onlyTags = { ...allEmpty(), tags: ['nightly'] }
    expect(turnsFilterOptionsIsEmpty(unwrapTurnsFilterOptions(onlyTags))).toBe(false)
  })

  // ★ 这条同时是**类型级**判据：维度类型必须是 `string[]`
  //   （turns_filter_options.go:38-45 的元素全是 string，且空时是 `[]` 不是 `null`）。
  //   写成 `(string | null)[]` 或 `readonly string[]` 都会让 `vue-tsc` 报错 —— 那就是它的牙。
  it('解包结果的维度类型是 string[]，状态码下标直接可用', () => {
    const o: TurnsFilterOptions = unwrapTurnsFilterOptions(populated())
    const projects: string[] = o.projects
    const codes: string[] = o.status_codes
    expect(projects).toHaveLength(2)
    // ★ (7) 因为元素类型是 string，所以可以直接喂给转换函数、不必先断言成 string
    expect(statusCodeFilterValue(codes[0] as string)).toBe(200)
  })
})

// ═══════════════════════════════════════════════════════════════════════════
// (6) 截断：区分格在正好 20
// ═══════════════════════════════════════════════════════════════════════════

describe('轮次筛选项 · 截断判定 (6)', () => {
  it('★ 19 项时判定为未截断', () => {
    expect(turnsFilterOptionIsTruncated(twentyValues().slice(0, 19))).toBe(false)
  })

  it('★ 正好 20 项时判定为截断（≥ 与 > 只在这里不同）', () => {
    expect(turnsFilterOptionIsTruncated(twentyValues())).toBe(true)
  })

  it('21 项时判定为截断', () => {
    expect(turnsFilterOptionIsTruncated([...twentyValues(), 'proj-20'])).toBe(true)
  })

  it('空数组时判定为未截断', () => {
    expect(turnsFilterOptionIsTruncated([])).toBe(false)
  })

  it('1 项时判定为未截断', () => {
    expect(turnsFilterOptionIsTruncated(['proj-00'])).toBe(false)
  })

  it('★ 正好 20 项的模型维度也判定为截断', () => {
    const o = unwrapTurnsFilterOptions({ ...allEmpty(), models: twentyValues() })
    expect(turnsFilterOptionIsTruncated(o.models)).toBe(true)
  })

  it('★ 正好 20 项的状态码维度也判定为截断', () => {
    const o = unwrapTurnsFilterOptions({ ...allEmpty(), status_codes: twentyValues() })
    expect(turnsFilterOptionIsTruncated(o.status_codes)).toBe(true)
  })

  it('★ 18 项时判定为未截断', () => {
    expect(turnsFilterOptionIsTruncated(twentyValues().slice(0, 18))).toBe(false)
  })
})

// ═══════════════════════════════════════════════════════════════════════════
// (7) status_code 的类型落差
// ═══════════════════════════════════════════════════════════════════════════

describe('轮次筛选项 · status_code 转换 (7)', () => {
  it('"200" 转成 200', () => {
    expect(statusCodeFilterValue('200')).toBe(200)
  })

  it('"404" 转成 404', () => {
    expect(statusCodeFilterValue('404')).toBe(404)
  })

  it('★ "0" 转成 0 而不是 null（0 是合法值，别被真值判断吞掉）', () => {
    expect(statusCodeFilterValue('0')).toBe(0)
  })

  it('★ 空串转成 null 而不是 0（Number("") 是 0，必须显式挡掉）', () => {
    expect(statusCodeFilterValue('')).toBeNull()
  })

  it('★ 纯空白串转成 null（Number("   ") 也是 0）', () => {
    expect(statusCodeFilterValue('   ')).toBeNull()
  })

  it('" 200 " 去空白后转成 200', () => {
    expect(statusCodeFilterValue(' 200 ')).toBe(200)
  })

  it('"-1" 转成 -1', () => {
    expect(statusCodeFilterValue('-1')).toBe(-1)
  })

  it('"abc" 转成 null', () => {
    expect(statusCodeFilterValue('abc')).toBeNull()
  })

  it('★ "200abc" 转成 null（parseInt 会截成 200，那是把坏值变成好值）', () => {
    expect(statusCodeFilterValue('200abc')).toBeNull()
  })

  it('★ "1e3" 转成 null（parseInt 会给 1，Number 会给 1000，都不是合法的 ::text 形态）', () => {
    expect(statusCodeFilterValue('1e3')).toBeNull()
  })

  it('"200.5" 转成 null（integer::text 产不出小数）', () => {
    expect(statusCodeFilterValue('200.5')).toBeNull()
  })

  it('"Infinity" 转成 null', () => {
    expect(statusCodeFilterValue('Infinity')).toBeNull()
  })

  it('"NaN" 转成 null', () => {
    expect(statusCodeFilterValue('NaN')).toBeNull()
  })

  it('★ "0x10" 转成 null（Number 会给 16，十六进制不是状态码）', () => {
    expect(statusCodeFilterValue('0x10')).toBeNull()
  })

  it('★ "+200" 转成 null（integer::text 不会产出带加号的形式）', () => {
    expect(statusCodeFilterValue('+200')).toBeNull()
  })

  it('★ "-" 转成 null（只有符号没有数字）', () => {
    expect(statusCodeFilterValue('-')).toBeNull()
  })

  // ★ 这里刻意用 toBe（Object.is 语义）：`-0` 与 `0` 不是同一个值，
  //   而 `Number('-0')` 确实给出 `-0` ⇒ 断言必须写成 `-0` 才诚实。
  it('★ "-0" 被接受并给出 -0（符号保留；序列化成查询串时是 "0"，无害）', () => {
    expect(statusCodeFilterValue('-0')).toBe(-0)
  })

  it('★ "0" 给出 0 而不是 -0', () => {
    expect(statusCodeFilterValue('0')).toBe(0)
  })
})