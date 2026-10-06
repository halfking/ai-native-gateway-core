import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createRouter, createMemoryHistory } from 'vue-router'
import TurnsView from './TurnsView.vue'
import { fetchTurnsSessions, fetchSessionTurns } from '@/api/turnsSessions'
import { setLocale, locale } from '@/i18n'

/**
 * TurnsView 的两条关键不变量：
 *
 * 1. **延迟未知不许显示成 0ms**。后端 `latency` 可为 null，
 *    「没测到」和「很快」在排障里是相反的指示。
 * 2. **换搜索条件必须清掉游标**。游标是后端签名过的（换会话传错 ⇒ 400
 *    "cursor mismatch"），拿旧筛选下的游标去查新条件必然 400。
 *    这条只有量「第二次请求实际带了什么」才测得到 —— 断言内部变量没用，
 *    因为那个变量本来就会被清，是**发出去的那一枪**说了算。
 */

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

vi.mock('@/api/turnsSessions', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/turnsSessions')>()
  return { ...actual, fetchTurnsSessions: vi.fn(), fetchSessionTurns: vi.fn() }
})

const sess = fetchTurnsSessions as unknown as ReturnType<typeof vi.fn>
const turnsOf = fetchSessionTurns as unknown as ReturnType<typeof vi.fn>

const ORIGIN_LOCALE = locale.value
let mountedList: Array<{ unmount(): void }> = []

async function mountView(): Promise<ReturnType<typeof mount>> {
  const pinia = createPinia()
  setActivePinia(pinia)
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/', component: { template: '<div />' } }],
  })
  await router.push('/')
  await router.isReady()
  const w = mount(TurnsView, { attachTo: document.body, global: { plugins: [pinia, router] } })
  mountedList.push(w)
  await flushPromises()
  await flushPromises()
  return w
}

beforeEach(() => {
  // ★ 显式钉 zh-CN：jsdom 的 navigator.language 是 en-US（第三次踩，见 §11.29(3)）
  setLocale('zh-CN')
  vi.clearAllMocks()
  vi.useFakeTimers({ shouldAdvanceTime: true })
  for (const w of mountedList) {
    try {
      w.unmount()
    } catch {
      /* ignore */
    }
  }
  mountedList = []
  document.body.innerHTML = ''
})
afterEach(() => {
  vi.useRealTimers()
  setLocale(ORIGIN_LOCALE)
  vi.clearAllMocks()
})

const SESSION = {
  session_id: 's-1',
  tenant_id: 'default',
  status: 'active',
  created_at: '2026-10-07T09:00:00Z',
  updated_at: '2026-10-07T10:00:00Z',
  total_turns: 2,
  total_tokens: 1234,
  total_cost_usd: '0.0123',
  failovers: 1,
  error_count: 0,
  duration_ms: 9000,
  title: '排查导出问题',
}

describe('TurnsView 延迟未知 ≠ 0ms', () => {
  // ★★ 核心：后端 latency 是 *int，NULL 表示未知。渲染成 0ms 会让
  //   「这一轮慢」被读成「这一轮很快」，方向正好反。
  it('latency 为 null ⇒ 显示「延迟未知」而不是 0ms', async () => {
    sess.mockResolvedValue({ items: [SESSION], has_more: false, next_cursor: '' })
    turnsOf.mockResolvedValue({
      session_id: 's-1',
      count: 2,
      has_more: false,
      next_cursor: '',
      turns: [
        { turn_number: 1, request_id: 'r-1', status: 'ok', latency: null, child_requests: null, body_status: 'available' },
        { turn_number: 2, request_id: 'r-2', status: 'ok', latency: 1500, child_requests: null, body_status: 'available' },
      ],
    })
    const w = await mountView()
    await w.find('.sess-card__head').trigger('click')
    await flushPromises()
    await flushPromises()

    const turns = w.findAll('.turn')
    expect(turns).toHaveLength(2)
    expect(turns[0]!.text()).toContain('延迟未知')
    // 反向锁定：绝不能出现 "0ms"
    expect(turns[0]!.text()).not.toContain('0ms')
    // 有值的那一轮正常显示
    expect(turns[1]!.text()).toContain('1500ms')
  })
})

describe('TurnsView 换搜索条件必须清游标', () => {
  /**
   * 这条判据的**形状**是被变异逼出来的，过程值得留档：
   *
   * ① 第一版断言「改搜索后第 1 页不带 cursor」——**恒真**。`loadFirst` 会把页号
   *    重置为 1，而 fetchPage 对 page<=1 按设计就不取游标。删掉 `cursors.clear()`
   *    那个变异照样全绿 ⇒ 量的是恒真条件。
   * ② 探针发现 jsdom 的 IntersectionObserver 不触发，HyperList 永远不预载
   *    ⇒ 第 2 页走不到 ⇒ 改用 defineExpose 暴露的 controller 主动 loadNext。
   * ③ 探针还照出一个**真缺陷**：游标读成 `cursors.get(page)` 而写的是
   *    `cursors.set(page, …)`，第 2 页去读还不存在的自己 ⇒ 从不发游标 ⇒
   *    后端每页都返回第 1 页，列表永远长不大。已修成 `get(page - 1)`。
   *    ★ 本条用例的真正价值就是钉住这个（变异 B 实测转红）。
   * ④ 第二版断言「改搜索后第 2 页没有 cursor」——**恒假**（正常态也红）：
   *    重置后第 1 页会把 cursors[1] 刷成新游标，第 2 页本就该带它。
   *    正确的不变式是「不是**旧查询**的游标」。
   *
   * ★ 诚实边界：删掉 `cursors.clear()` 这个变异**不会**让本条转红
   *   （实测）。因为重置后的第 1 页请求总会覆写 cursors[1]，
   *   而第 2 页只读 cursors[1] ⇒ 更高位的陈旧条目在被读到之前必被覆写。
   *   所以 `cursors.clear()` 是**防御性、当前非承重**，本测试不声称覆盖它。
   */
  it('翻到第 2 页带上一页的游标；改搜索后不得再用旧游标', async () => {
    // ★ 游标按查询区分：旧查询发 CUR-OLD，新查询发 CUR-NEW。
    //   断言的是「不是旧的」，**不是**「没有游标」——换搜索后第 2 页
    //   本来就该带新查询第 1 页返回的游标。第一版断言 cursor===undefined，
    //   那是个恒假条件（正常态也红），量错了对象。
    sess.mockImplementation(async (p: Record<string, unknown>) => ({
      items: [SESSION],
      has_more: true,
      next_cursor: p.search ? 'CUR-NEW' : 'CUR-OLD',
    }))
    const w = await mountView()
    const ctl = (w.vm as unknown as { controller: { loadNext: () => void } }).controller

    // 第一轮：翻到第 2 页，应当带 CUR-OLD
    ctl.loadNext()
    await flushPromises()
    await flushPromises()
    expect((sess.mock.calls[1]![0] as Record<string, unknown>).cursor).toBe('CUR-OLD')

    // 改搜索 ⇒ 重置到第 1 页
    await w.find('.turns__search input').setValue('导出')
    vi.advanceTimersByTime(400)
    await flushPromises()
    await flushPromises()

    // 再翻到第 2 页：必须带 CUR-NEW，**绝不能**是 CUR-OLD
    // （拿到旧游标会被后端以 400 "cursor mismatch" 拒掉 —— 游标是会话签名的）
    ctl.loadNext()
    await flushPromises()
    await flushPromises()

    const last = sess.mock.calls[sess.mock.calls.length - 1]![0] as Record<string, unknown>
    expect(last.search).toBe('导出')
    expect(last.cursor).toBe('CUR-NEW')
    expect(last.cursor).not.toBe('CUR-OLD')
  })
})

describe('TurnsView 会话卡渲染', () => {
  it('换道与失败各自有标记', async () => {
    sess.mockResolvedValue({
      // ★ 字段名是 failover_count（不是 failovers）—— 与 error_count 同族。
      //   写错字段名的表现是「标记不渲染」而不是报错。
      items: [{ ...SESSION, failover_count: 2, error_count: 1 }],
      has_more: false,
      next_cursor: '',
    })
    const w = await mountView()
    const text = w.find('.sess-card__head').text()
    expect(text).toContain('换道 2 次')
    expect(text).toContain('失败 1 次')
  })

  it('成本空串不显示成 $0.0000（costNumber 空串 ⇒ null）', async () => {
    sess.mockResolvedValue({
      items: [{ ...SESSION, total_cost_usd: '' }],
      has_more: false,
      next_cursor: '',
    })
    const w = await mountView()
    expect(w.find('.sess-card__head').text()).not.toContain('$0.0000')
  })

  it('轮次加载失败显示错误而不是空列表', async () => {
    sess.mockResolvedValue({ items: [SESSION], has_more: false, next_cursor: '' })
    turnsOf.mockRejectedValue(new Error('turns boom'))
    const w = await mountView()
    await w.find('.sess-card__head').trigger('click')
    await flushPromises()
    await flushPromises()
    expect(w.find('.sess-card__msg--err').exists()).toBe(true)
    expect(w.text()).toContain('turns boom')
  })
})
