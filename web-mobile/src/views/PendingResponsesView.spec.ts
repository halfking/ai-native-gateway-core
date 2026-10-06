import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import PendingResponsesView from './PendingResponsesView.vue'
import { fetchPendingList, fetchPendingStats, fetchPendingDetail } from '@/api/pendingResponses'
import { setLocale, locale } from '@/i18n'

/**
 * PendingResponsesView 的不变量（2026-10-07）。
 *
 * 1. ★★★★ **不提供** completed/failed 状态筛选，并解释原因；
 * 2. ★★★★ 列表**不显示** provider_id / is_stream / bytes_buffered，
 *    并说明这三项要进详情才看得到（它们在列表里恒为 0/false）；
 * 3. ★★★ 详情 404 ⇒ 「查不到」，**不是**错误；
 * 4. ★★★ `oldest_created_at = 0` ⇒ 「没有条目」，不是 1970 年；
 * 5. ★★★ Unix 秒经 `pendingUnixToIso` 转 ISO；换不出时间时显示「时间未知」
 *    而不是 `relativeTime` 的「从未」；
 * 6. ★★★ 列表与详情**各自独立**失败，一个失败不清空另一个；
 * 7. ★★ limit 切换重置 offset；分页按钮按本地总数推，不越界。
 */

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

vi.mock('@/api/pendingResponses', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/pendingResponses')>()
  return { ...actual, fetchPendingList: vi.fn(), fetchPendingStats: vi.fn(), fetchPendingDetail: vi.fn() }
})

const listMock = fetchPendingList as unknown as ReturnType<typeof vi.fn>
const statsMock = fetchPendingStats as unknown as ReturnType<typeof vi.fn>
const detailMock = fetchPendingDetail as unknown as ReturnType<typeof vi.fn>

const ORIGIN_LOCALE = locale.value
let mountedList: Array<{ unmount(): void }> = []

async function mountView(): Promise<ReturnType<typeof mount>> {
  const pinia = createPinia()
  setActivePinia(pinia)
  const w = mount(PendingResponsesView, { attachTo: document.body, global: { plugins: [pinia] } })
  mountedList.push(w)
  await flushPromises()
  await flushPromises()
  return w
}

/** 2026-09-20 附近的 Unix 秒（相对当前时间的量级足够断言用）。 */
const T0 = Math.floor(Date.parse('2026-09-20T12:00:00Z') / 1000)

function entry(over: Record<string, unknown> = {}) {
  return {
    session_id: 'gw_sess_a',
    request_id: '11111111-2222-3333-4444-555555555555',
    status: 'in_progress',
    // ★ 这三个在真实后端里就是恒 0/false，fixture 照实写
    provider_id: 0,
    is_stream: false,
    created_at: T0,
    bytes_buffered: 0,
    age_seconds: 30,
    ...over,
  }
}

function listResp(over: Record<string, unknown> = {}) {
  return { entries: [entry()], limit: 50, offset: 0, count: 1, ...over }
}

function statsResp(over: Record<string, unknown> = {}) {
  return { total: 1, by_status: { in_progress: 1 }, oldest_created_at: T0, ...over }
}

function detailResp(over: Record<string, unknown> = {}) {
  return {
    session_id: 'gw_sess_a',
    request_id: '11111111-2222-3333-4444-555555555555',
    status: 'in_progress',
    provider_id: 7,
    credential_id: 12,
    is_stream: true,
    created_at: T0,
    completed_at: 0,
    bytes_buffered: 4096,
    age_seconds: 30,
    error_message: '',
    ...over,
  }
}

function httpErr(status: number, msg: string): Error {
  return Object.assign(new Error(msg), { status })
}

beforeEach(() => {
  setLocale('zh-CN')
  vi.clearAllMocks()
  listMock.mockResolvedValue(listResp())
  statsMock.mockResolvedValue(statsResp())
  detailMock.mockResolvedValue(detailResp())
  document.body.innerHTML = ''
})
afterEach(() => {
  for (const w of mountedList) {
    try {
      w.unmount()
    } catch {
      /* ignore */
    }
  }
  mountedList = []
  document.body.innerHTML = ''
  setLocale(ORIGIN_LOCALE as 'zh-CN' | 'en-US')
})

describe('入口与两个端点', () => {
  it('★★★ 挂载即请求 list 与 stats（互不依赖）', async () => {
    await mountView()
    expect(listMock).toHaveBeenCalledTimes(1)
    expect(statsMock).toHaveBeenCalledTimes(1)
    expect(listMock.mock.calls[0]![0]).toEqual({ limit: 50, offset: 0, sessionId: undefined })
  })

  it('★★ 概览显示 total（= 进行中条数）', async () => {
    statsMock.mockResolvedValue(statsResp({ total: 42, by_status: { in_progress: 42 } }))
    const w = await mountView()
    expect(w.text()).toContain('42')
  })
})

describe('★★★★ 判据 1：状态筛选是假的', () => {
  it('★★★★ 页面**没有任何**状态筛选控件（判据落在控件上，不是文案上）', async () => {
    const w = await mountView()
    // ★ 说明文案**必然**要提到「已完成 / 已失败」才能解释为什么不给筛选项，
    //   所以判据不能写成「文本里不能出现这两个词」—— 那会把好文案判红。
    //   真正的判据是：**没有任何可交互控件**能选中它们。
    expect(w.findAll('select')).toHaveLength(0)
    expect(w.findAll('input[type="radio"]')).toHaveLength(0)
    expect(w.findAll('input[type="checkbox"]')).toHaveLength(0)
    // 唯一的 chip 组是 limit
    const chipTexts = w.findAll('.pd__chip').map((c) => c.text())
    expect(chipTexts).toEqual(['前 50', '前 100', '前 500'])
    for (const txt of chipTexts) expect(txt).not.toContain('完成')
  })

  it('★★★ 从头到尾没有带 status 字段的请求（API 层与视图层双保险）', async () => {
    const w = await mountView()
    for (const c of listMock.mock.calls) expect(c[0]).not.toHaveProperty('status')
    // 再点一遍 limit / 提交筛选，确认后续请求也不带
    await w.findAll('.pd__chip').find((c) => c.text() === '前 100')!.trigger('click')
    await flushPromises()
    for (const c of listMock.mock.calls) expect(c[0]).not.toHaveProperty('status')
  })

  it('★★★ 明说「只统计进行中」且不等于全部待处理数', async () => {
    const w = await mountView()
    expect(w.text()).toContain('这个数字只统计「进行中」的请求')
    expect(w.text()).toContain('所以它不等于全部待处理响应数')
  })

  it('★★★ 明说「选了也只会拿到空列表」', async () => {
    const w = await mountView()
    expect(w.text()).toContain('页面不提供状态筛选')
    expect(w.text()).toContain('只会拿到空列表')
  })

  it('★★ by_status 恒等于 total ⇒ 不单独渲染一张状态表', async () => {
    const w = await mountView()
    // 恰好出现一次「只统计进行中」，证明只有一句说明而非按状态分列
    const hits = w.text().split('这个数字只统计「进行中」的请求').length - 1
    expect(hits).toBe(1)
  })
})

describe('★★★★ 判据 2：列表的三个假字段不得当真值显示', () => {
  it('★★★★ 明说列表没有供应商/凭据/流式/缓冲字节', async () => {
    const w = await mountView()
    expect(w.text()).toContain('列表里没有供应商、凭据、流式和已缓冲字节数')
    expect(w.text()).toContain('进去看详情才有')
  })

  it('★★★ 列表区**不出现** 供应商 ID / 凭据 ID 两行（证明不是「说明没写、数字照显示」）', async () => {
    const w = await mountView()
    expect(w.text()).not.toContain('供应商 ID')
    expect(w.text()).not.toContain('凭据 ID')
  })

  it('★★ 详情里才出现真实 provider_id / credential_id', async () => {
    detailMock.mockResolvedValue(detailResp({ provider_id: 7, credential_id: 12 }))
    const w = await mountView()
    await w.find('.pd__rowbtn').trigger('click')
    await flushPromises()
    expect(w.text()).toContain('供应商 ID')
    expect(w.text()).toContain('凭据 ID')
    expect(w.text()).toContain('真实值')
  })

  it('★★ 明说详情是真值、列表同批字段是固定 0', async () => {
    const w = await mountView()
    await w.find('.pd__rowbtn').trigger('click')
    await flushPromises()
    expect(w.text()).toContain('是固定 0，没有参考意义')
  })
})

describe('★★★ 判据 3：详情 404 是「查不到」，不是错误', () => {
  it('★★★ 404 ⇒ 显示「查不到」且**不**显示红色错误', async () => {
    detailMock.mockRejectedValueOnce(httpErr(404, 'no pending response for this session'))
    const w = await mountView()
    await w.find('.pd__rowbtn').trigger('click')
    await flushPromises()
    expect(w.text()).toContain('查不到这个会话的待处理响应')
    expect(w.text()).not.toContain('no pending response for this session')
  })

  it('★ 404 的说明覆盖「已被清理」与「不在租户内」两种可能', async () => {
    detailMock.mockRejectedValueOnce(httpErr(404, 'x'))
    const w = await mountView()
    await w.find('.pd__rowbtn').trigger('click')
    await flushPromises()
    expect(w.text()).toContain('已经完成或被清理了')
    expect(w.text()).toContain('不在你的租户范围内')
  })

  it('★★★ 非 404 失败 ⇒ 显示错误文案', async () => {
    detailMock.mockRejectedValueOnce(httpErr(503, 'pending store error: boom'))
    const w = await mountView()
    await w.find('.pd__rowbtn').trigger('click')
    await flushPromises()
    expect(w.text()).toContain('pending store error: boom')
    expect(w.text()).not.toContain('查不到这个会话的待处理响应')
  })

  it('★ 没选行时提示「先选一条」', async () => {
    const w = await mountView()
    expect(w.text()).toContain('从上面选一条看详情')
  })
})

describe('★★★ 判据 4/5：时间口径', () => {
  it('★★★ oldest_created_at = 0 ⇒ 「没有条目」，不是 1970 年', async () => {
    statsMock.mockResolvedValue(statsResp({ total: 0, by_status: {}, oldest_created_at: 0 }))
    const w = await mountView()
    expect(w.text()).toContain('没有条目')
    expect(w.text()).not.toContain('1970')
  })

  it('★ 有条目时最老一条显示相对时间', async () => {
    const w = await mountView()
    expect(w.text()).toMatch(/前|刚才|小时/)
  })

  it('★★★ created_at = 0 ⇒ 显示「时间未知」，**不是**「从未」', async () => {
    listMock.mockResolvedValue(listResp({ entries: [entry({ created_at: 0 })] }))
    const w = await mountView()
    expect(w.text()).toContain('时间未知')
    expect(w.text()).not.toContain('从未')
  })

  it('★ 年龄分档渲染：30 秒 ⇒ 1 分钟内', async () => {
    const w = await mountView()
    expect(w.text()).toContain('1 分钟内')
  })

  it('★★★ 超过 10 分钟 ⇒ 显示「超过 10 分钟」', async () => {
    listMock.mockResolvedValue(listResp({ entries: [entry({ age_seconds: 1200 })] }))
    const w = await mountView()
    expect(w.text()).toContain('超过 10 分钟')
    expect(w.text()).not.toContain('1 分钟内')
  })
})

describe('★★★ 判据 6：两个端点各自独立失败', () => {
  it('★★★ stats 失败 ⇒ 列表照常显示', async () => {
    statsMock.mockRejectedValueOnce(new Error('stats boom'))
    const w = await mountView()
    expect(w.text()).toContain('stats boom')
    expect(w.text()).toContain('gw_sess_a')
  })

  it('★★★ list 失败 ⇒ 概览照常显示', async () => {
    listMock.mockRejectedValueOnce(new Error('list boom'))
    const w = await mountView()
    expect(w.text()).toContain('list boom')
    expect(w.text()).toContain('本次返回')
  })

  it('★★ list 失败时显示空态文案之外还有错误（不静默变「没有」）', async () => {
    listMock.mockRejectedValueOnce(new Error('list boom'))
    const w = await mountView()
    expect(w.text()).not.toContain('没有进行中的待处理响应')
  })
})

describe('★★ 判据 7：分页与 limit', () => {
  it('★★★ 切 limit ⇒ offset 归零重查', async () => {
    const w = await mountView()
    await w.findAll('.pd__chip').find((c) => c.text() === '前 100')!.trigger('click')
    await flushPromises()
    expect(listMock.mock.calls[1]![0]).toEqual({ limit: 100, offset: 0, sessionId: undefined })
  })

  it('★ 只有一页时「下一页」禁用', async () => {
    const w = await mountView()
    const next = w.findAll('.pd__btn').find((b) => b.text() === '下一页')!
    expect(next.attributes('disabled')).toBeDefined()
  })

  it('★★★ count 超出当前页 ⇒ 「下一页」可用并带 offset 重查', async () => {
    listMock.mockResolvedValue(listResp({ entries: [entry()], count: 120, offset: 0 }))
    const w = await mountView()
    const next = w.findAll('.pd__btn').find((b) => b.text() === '下一页')!
    expect(next.attributes('disabled')).toBeUndefined()
    await next.trigger('click')
    await flushPromises()
    expect(listMock.mock.calls[1]![0]).toEqual({ limit: 50, offset: 50, sessionId: undefined })
  })

  it('★★ 页码信息说清「第几条–第几条 / 共几条」', async () => {
    listMock.mockResolvedValue(listResp({ entries: [entry()], count: 120 }))
    const w = await mountView()
    expect(w.text()).toContain('第 1-1 条，共 120 条')
  })

  it('★★★ 后端回显的 offset 被采纳（越界修正后不再乱翻页）', async () => {
    listMock.mockResolvedValue(listResp({ entries: [], count: 3, offset: 3 }))
    const w = await mountView()
    const prev = w.findAll('.pd__btn').find((b) => b.text() === '上一页')!
    expect(prev.attributes('disabled')).toBeUndefined()
    await prev.trigger('click')
    await flushPromises()
    // offset 3 - limit 50 ⇒ 负数被夹到 0
    expect(listMock.mock.calls[1]![0]).toEqual({ limit: 50, offset: 0, sessionId: undefined })
  })

  it('★★ 填 session_id 提交 ⇒ 带过滤条件重查且 offset 归零', async () => {
    const w = await mountView()
    await w.find('input').setValue('  gw_sess_x  ')
    await w.find('form').trigger('submit')
    await flushPromises()
    expect(listMock.mock.calls[1]![0]).toEqual({ limit: 50, offset: 0, sessionId: 'gw_sess_x' })
  })

  it('★★ 「清空筛选」是真的清空（不是「取消」）', async () => {
    const w = await mountView()
    await w.find('input').setValue('gw_sess_x')
    await w.find('form').trigger('submit')
    await flushPromises()
    await w.findAll('.pd__btn').find((b) => b.text() === '清空筛选')!.trigger('click')
    await flushPromises()
    expect((w.find('input').element as HTMLInputElement).value).toBe('')
    expect(listMock.mock.calls[2]![0]).toEqual({ limit: 50, offset: 0, sessionId: undefined })
  })
})

describe('租户作用域说明', () => {
  it('★★ 明说列表按租户过滤、且接口不返回租户字段', async () => {
    const w = await mountView()
    expect(w.text()).toContain('列表按租户过滤')
    expect(w.text()).toContain('接口不返回租户字段')
  })
})