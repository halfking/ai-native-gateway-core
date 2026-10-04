// ConnectionRegistryView.responsive.test.ts — H6 第十二条垂直切片的门禁。
//
// 形状：**两张表**（live / closed，各 5 列）、**无分页 API**、两张卡片刻意长得不一样
// （live 是「最后一帧多久前」相对时间，closed 是「注册于」绝对时间）。
//
// 三态归属是**第一种形态的变体**：每节 `<div v-if="filteredX.length">表格</div>`
// + `<div v-else class="cr-empty">`，空态时**整块表格被撤掉**换空文案；
// 另有首屏骨架屏 `.cr-skeleton`。⇒ 容器挂在同一个 `v-if` 上，两档共用页面自己的三态，
// **不传 `:empty` / `:loading`**。
//
// ★ 本页有**两个真实定时器**（15s 轮询 + 1s tick 喂相对时间），所以：
//   ① 必须 `enableAutoUnmount(afterEach)` —— 不卸载就是 §2.1「活实例泄漏」那条病灶；
//   ② 相对时间断言必须**冻时间** —— `elapsedOf` 依赖 `nowMs`，而 `nowMs` 由 1s tick
//      跟着 `Date.now()` 走。切片三埋过一个同类时间炸弹（08:41 绿 / 08:59 红）。
//   ③ 假定时器下**不用 `flushPromises()`**：VTU 的实现是 `setTimeout(resolve, 0)`，
//      被假定时器接管后不会自己响 ⇒ 会挂死。改用 `advanceTimersByTimeAsync(0)` 排微任务。
import { mount, enableAutoUnmount, RouterLinkStub } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest'
import ConnectionRegistryView from './ConnectionRegistryView.vue'
import { _resetForTests } from '../composables/useWindowClass'
import { _resetDataViewModeForTests } from '../composables/useDataViewMode'
import { fmtDateTime24h } from '../i18n/useFormat'

const source = readFileSync(resolve(process.cwd(), 'src/views/ConnectionRegistryView.vue'), 'utf8')
const codeOnly = source
  .replace(/\/\*[\s\S]*?\*\//g, '')
  .replace(/<!--[\s\S]*?-->/g, '')
  .replace(/(^|[^:'"`\\])\/\/[^\n]*/g, '$1')

/** 冻在一个固定时刻：`elapsedOf` 的秒/分/小时档位才可断言。 */
const NOW = new Date('2026-10-05T12:00:00.000Z').getTime()
const iso = (offsetSec: number) => new Date(NOW - offsetSec * 1000).toISOString()

const { ctrl, fetchConnectionRegistry, fetchConnectionByRequestId, pushMock } = vi.hoisted(() => {
  const ctrl = { fail: false, hidden: false }
  const fetchConnectionRegistry = vi.fn()
  const fetchConnectionByRequestId = vi.fn()
  const pushMock = vi.fn()
  return { ctrl, fetchConnectionRegistry, fetchConnectionByRequestId, pushMock }
})

/**
 * 三行覆盖相对时间三档（秒 / 分 / 小时），外加两行专门覆盖缺值与 0 帧：
 * - `last_frame_at` 距今 5s / 90s / 7200s ⇒ '5s' / '1m' / '2h'
 * - `frames_written` 缺值 ⇒ 出 `0`（与桌面同一个口径，刻意不为破折号）
 * - `protocol` / `client_type` / `close_reason` 缺值 ⇒ 破折号
 */
const LIVE = [
  { request_id: 'live-a', protocol: 'sse', client_type: 'curl', tenant_id: 'default', registered_at: iso(3600), last_frame_at: iso(5), frames_written: 12 },
  { request_id: 'live-b', protocol: 'ws', client_type: 'node', tenant_id: 'default', registered_at: iso(3600), last_frame_at: iso(90), frames_written: 3 },
  // ★ live-c 同时缺 protocol / client_type / frames_written ——
  //   第一版它有 protocol，于是 `c.protocol || '—'` 这条回落**从未被测到**，
  //   变异 M23（拿掉回落）全绿。fixture 必须真的落到那个分支上。
  { request_id: 'live-c', tenant_id: 'default', registered_at: iso(3600), last_frame_at: iso(7200) },
  // ★ live-d 距今 900s：它落在 600_000 ~ 3_600_000 之间 ⇒
  //   「分钟档阈值从 1h 挪到 10min」这条变异才会被抓到。
  //   第一版三行只有 5s / 90s / 7200s，90s 在 600_000 内侧 ⇒ 那条变异无牙（fixture 缺口）。
  { request_id: 'live-d', protocol: 'ws', client_type: 'python', tenant_id: 'other', registered_at: iso(3600), last_frame_at: iso(900) },
]

const CLOSED = [
  { request_id: 'closed-a', protocol: 'sse', close_reason: 'client_closed', registered_at: iso(7200), frames_written: 40, closed: true },
  { request_id: 'closed-b', protocol: 'ws', registered_at: iso(5), closed: true },
]

/**
 * ★ 这两个 mock **必须就是 hoisted 里的 vi.fn 本身**，不能在工厂里另写一个普通函数：
 *   另写的话组件调的是普通函数，hoisted 的 vi.fn 一次都不会被调用 ⇒
 *   计数恒为 0、`mockImplementationOnce` 也不生效，
 *   「首屏骨架」与「15s 轮询」两条用例就都在测一个**没接上的装置**。
 *   （第一版就是这么写的，4 条红里 3 条是这一个根因。）
 */
vi.mock('../api/connection-registry', () => ({
  fetchConnectionRegistry,
  fetchConnectionByRequestId,
}))

/**
 * ★ 每次都返回**新副本**，不能把 fixture 数组按引用交出去。
 *   组件的 `searchByRequestId` 会**原地改** `live.value[idx] = snap` ——
 *   而 `live.value` 就是 mock 返回的那个数组 ⇒ 一次查询就把模块级 fixture 改掉了。
 *   症状极难认：后面某条**毫不相干**的用例突然红（我这边是「筛 node 得 0 行」，
 *   因为前一条用例把它改成了 'go'），看起来像产品的过滤逻辑坏了。
 */
const freshLive = () => LIVE.map((c) => ({ ...c }))
const freshClosed = () => CLOSED.map((c) => ({ ...c }))

fetchConnectionRegistry.mockImplementation(async () => {
  if (ctrl.fail) throw new Error('registry boom')
  return { live: freshLive(), closed: freshClosed(), capacity: 200, live_count: LIVE.length }
})

vi.mock('vue-router', () => ({ useRouter: () => ({ push: pushMock }) }))

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: {
    'zh-CN': {
      connectionRegistry: {
        title: '流式连接注册表',
        subtitle: '说明文案',
        searchPlaceholder: '按 request_id 查询',
        search: '查询',
        openJourney: '打开请求旅程',
        filterPlaceholder: '过滤（id / 协议 / 客户端 / 租户）',
        liveCount: '在线 {count} / 上限 {capacity}',
        apiDegraded: '接口降级',
        historyNote: '历史为环形缓冲',
        sections: { live: '在线连接', closed: '近期关闭' },
        columns: {
          requestId: 'Request ID', protocol: '协议', client: '客户端', frames: '帧数',
          lastFrame: '最后一帧', closeReason: '关闭原因', registeredAt: '注册于',
        },
        empty: { live: '当前没有在线连接。', closed: '近期没有关闭的连接。' },
      },
      hyper: { dataView: { table: '表格', cards: '卡片' }, list: { empty: '暂无数据' } },
    },
  },
})

function mockWindowClass(cls: 'compact' | 'expanded'): void {
  _resetForTests()
  const lo = cls === 'compact' ? 0 : 1280
  Object.defineProperty(window, 'matchMedia', {
    writable: true,
    configurable: true,
    value: (q: string) => {
      const min = q.match(/min-width:\s*([\d.]+)px/)
      const max = q.match(/max-width:\s*([\d.]+)px/)
      const matches = min ? lo >= parseFloat(min[1]) : max ? lo <= parseFloat(max[1]) : false
      return {
        matches, media: q, onchange: null,
        addEventListener: () => {}, removeEventListener: () => {},
        addListener: () => {}, removeListener: () => {}, dispatchEvent: () => false,
      }
    },
  })
}

/** 排微任务但**不动假时间**（假定时器下 `flushPromises()` 会挂死，见文件头注释）。 */
async function settle(times = 4) {
  for (let i = 0; i < times; i++) await vi.advanceTimersByTimeAsync(0)
}

async function factory() {
  const w = mount(ConnectionRegistryView, { global: { plugins: [i18n], stubs: { RouterLink: RouterLinkStub } } })
  // ★ 轮询到**真实条件**：骨架屏消失 = `refreshFromApi` 的 finally 已跑 = 首屏落定。
  //   第一版写的是固定 `settle(4)`，在本组内跑就红、单独跑就绿 ——
  //   正是规范 §2 那条「固定次数的 flushPromises 不可靠」的反模式（本页首屏要过
  //   一条 await 才落定，组内多跑几条时微任务轮次不够）。
  for (let i = 0; i < 50 && w.find('[data-testid="cr-skeleton"]').exists(); i++) {
    await vi.advanceTimersByTimeAsync(0)
  }
  // 把「条件确实成立」提成**排在依赖断言之前**的断言：没到位时它先喊
  expect(w.find('[data-testid="cr-skeleton"]').exists(), '首屏没落定 —— 后面的断言都没有意义').toBe(false)
  await settle(2)
  return w
}

function cardsIn(w: Awaited<ReturnType<typeof factory>>, testid: 'cr-live' | 'cr-closed') {
  const sec = w.find(`[data-testid="${testid}"]`)
  expect(sec.exists(), `找不到分区 ${testid}`).toBe(true)
  return sec.findAll('[data-testid="card-list"] .card')
}

function fieldValue(card: ReturnType<ReturnType<typeof cardsIn>[number]['findAll']>[number], label: string) {
  const hit = card.findAll('.card__field').find((f) => f.find('dt').text() === label)
  expect(
    hit,
    `卡片里找不到字段「${label}」，实际有：${card.findAll('.card__field').map((f) => f.find('dt').text()).join('/')}`,
  ).toBeTruthy()
  return hit!.find('dd')
}

beforeEach(() => {
  _resetDataViewModeForTests()
  ctrl.fail = false
  ctrl.hidden = false
  pushMock.mockClear()
  fetchConnectionRegistry.mockClear()
  fetchConnectionRegistry.mockImplementation(async () => {
    if (ctrl.fail) throw new Error('registry boom')
    return { live: freshLive(), closed: freshClosed(), capacity: 200, live_count: LIVE.length }
  })
  fetchConnectionByRequestId.mockReset()
  fetchConnectionByRequestId.mockImplementation(async (id: string) => ({ request_id: id, protocol: 'sse', closed: true }))
  mockWindowClass('expanded')
  localStorage.clear()
  // ★ 冻时间：elapsedOf 依赖 nowMs，而 nowMs 由 1s tick 跟着 Date.now() 走
  vi.useFakeTimers()
  vi.setSystemTime(NOW)
  Object.defineProperty(document, 'hidden', { writable: true, configurable: true, value: false })
})

// 本页挂载即排两个真实 interval（15s 轮询 + 1s tick）。不卸载就是活实例泄漏。
enableAutoUnmount(afterEach)
afterEach(() => {
  vi.useRealTimers()
  vi.clearAllMocks()
})

describe('ConnectionRegistryView：桌面两张连接表零回归', () => {
  it('live 表 5 列表头与顺序；closed 表 5 列但第 3 列不同', async () => {
    const w = await factory()
    const liveTh = w.findAll('[data-testid="cr-live"] thead th').map((th) => th.text())
    const closedTh = w.findAll('[data-testid="cr-closed"] thead th').map((th) => th.text())
    expect(liveTh).toEqual(['Request ID', '协议', '客户端', '帧数', '最后一帧'])
    expect(closedTh).toEqual(['Request ID', '协议', '关闭原因', '帧数', '注册于'])
    // 两张表的「最后一帧 / 注册于」是刻意不同的列，不能被统一成同一个
    expect(liveTh[4]).not.toBe(closedTh[4])
  })

  it('两节各有自己的计数与分区标题', async () => {
    const w = await factory()
    expect(w.find('[data-testid="cr-live"] .cr-count').text()).toBe('4')
    expect(w.find('[data-testid="cr-closed"] .cr-count').text()).toBe('2')
    expect(w.find('[data-testid="cr-live"] h3').text()).toBe('在线连接')
    expect(w.find('[data-testid="cr-closed"] h3').text()).toBe('近期关闭')
  })

  it('request_id 是可点按钮，跳请求旅程（带 name + params）', async () => {
    const w = await factory()
    await w.findAll('[data-testid="cr-live-row"] .cr-link')[0]!.trigger('click')
    expect(pushMock).toHaveBeenCalledWith({ name: 'request-journey-detail', params: { requestId: 'live-a' } })
    pushMock.mockClear()
    await w.findAll('[data-testid="cr-closed-row"] .cr-link')[0]!.trigger('click')
    expect(pushMock).toHaveBeenCalledWith({ name: 'request-journey-detail', params: { requestId: 'closed-a' } })
  })

  it('协议 / 客户端 / 关闭原因缺值出破折号', async () => {
    const w = await factory()
    const live = w.findAll('[data-testid="cr-live-row"]')
    expect(live[2]!.findAll('td')[1]!.text()).toBe('—')          // ★ protocol 缺（fixture 已补）
    expect(live[2]!.findAll('td')[2]!.text()).toBe('—')          // client_type 缺
    expect(live[0]!.findAll('td')[1]!.text()).toBe('sse')
    const closed = w.findAll('[data-testid="cr-closed-row"]')
    expect(closed[0]!.findAll('td')[2]!.text()).toBe('client_closed')
    expect(closed[1]!.findAll('td')[2]!.text()).toBe('—')        // close_reason 缺
  })

  /**
   * ★ 相对时间三档：5s / 90s / 7200s ⇒ '5s' / '1m' / '2h'。
   *   这条在**冻时间**下才成立 —— 不冻就是切片三那个时间炸弹。
   */
  it('最后一帧是相对时间，三档（秒 / 分 / 时）', async () => {
    const w = await factory()
    const cells = w.findAll('[data-testid="cr-live-row"]').map((tr) => tr.findAll('td')[4]!.text())
    expect(cells).toEqual(['5s', '1m', '2h', '15m'])
  })

  it('★ 帧数缺值出 0 而不是破折号（与桌面同一口径，刻意不为 null→0 修）', async () => {
    const w = await factory()
    const cells = w.findAll('[data-testid="cr-live-row"]').map((tr) => tr.findAll('td')[3]!.text())
    // live-c 没有 frames_written
    expect(cells).toEqual(['12', '3', '0', '0'])
  })

  it('关闭表的「注册于」走 fmtDateTime24h', async () => {
    const w = await factory()
    const cells = w.findAll('[data-testid="cr-closed-row"]').map((tr) => tr.findAll('td')[4]!.text())
    expect(cells[0]).toBe(fmtDateTime24h(iso(7200)))
    expect(cells[0]).not.toBe('—')
  })

  it('搜索行：输入框 + 查询钮（空输入禁用）+ 打开旅程钮（空输入禁用）', async () => {
    const w = await factory()
    expect(w.find('[data-testid="cr-search-input"]').exists()).toBe(true)
    expect(w.find('[data-testid="cr-search-btn"]').attributes('disabled')).toBeDefined()
    expect(w.find('[data-testid="cr-journey-btn"]').attributes('disabled')).toBeDefined()
    await w.find('[data-testid="cr-search-input"]').setValue('live-d')
    expect(w.find('[data-testid="cr-search-btn"]').attributes('disabled')).toBeUndefined()
    expect(w.find('[data-testid="cr-journey-btn"]').attributes('disabled')).toBeUndefined()
  })

  it('打开旅程钮把输入框里的 id 推给路由', async () => {
    const w = await factory()
    await w.find('[data-testid="cr-search-input"]').setValue('req-xyz')
    await w.find('[data-testid="cr-journey-btn"]').trigger('click')
    expect(pushMock).toHaveBeenCalledWith({ name: 'request-journey-detail', params: { requestId: 'req-xyz' } })
  })

  it('状态行：在线数 / 上限；接口降级时多出一枚红色 chip', async () => {
    const w = await factory()
    expect(w.find('[data-testid="cr-capacity"]').text()).toBe('在线 4 / 上限 200')
    expect(w.find('[data-testid="api-degraded"]').exists()).toBe(false)
    ctrl.fail = true
    await vi.advanceTimersByTimeAsync(15000)
    await settle()
    expect(w.find('[data-testid="api-degraded"]').exists()).toBe(true)
  })

  it('桌面不出卡片', async () => {
    const w = await factory()
    expect(w.find('[data-testid="card-list"]').exists()).toBe(false)
  })
})

describe('ConnectionRegistryView：查询与过滤', () => {
  it('按 request_id 查询：命中已存在则原地替换', async () => {
    const w = await factory()
    fetchConnectionByRequestId.mockResolvedValue({ request_id: 'live-b', protocol: 'ws', client_type: 'go', closed: false, last_frame_at: iso(5) })
    await w.find('[data-testid="cr-search-input"]').setValue('live-b')
    await w.find('[data-testid="cr-search-btn"]').trigger('click')
    await settle()
    expect(fetchConnectionByRequestId).toHaveBeenCalledWith('live-b')
    // 替换后仍只有 4 行（不是 5 行 —— 原地替换不是追加）
    expect(w.findAll('[data-testid="cr-live-row"]')).toHaveLength(1)
    expect(w.find('[data-testid="cr-live-row"]').findAll('td')[2]!.text()).toBe('go')
  })

  it('查询命中 closed 段时进 closed 而不是 live', async () => {
    const w = await factory()
    fetchConnectionByRequestId.mockResolvedValue({ request_id: 'zzz', protocol: 'ws', closed: true })
    await w.find('[data-testid="cr-search-input"]').setValue('zzz')
    await w.find('[data-testid="cr-search-btn"]').trigger('click')
    await settle()
    // 查询会把 filterText 设成该 id ⇒ 只剩这一行，且落在 closed
    expect(w.find('[data-testid="cr-live"] .cr-empty').exists()).toBe(true)
    expect(w.findAll('[data-testid="cr-closed-row"]')).toHaveLength(1)
  })

  it('查询失败：错误文案出在搜索行下方', async () => {
    const w = await factory()
    fetchConnectionByRequestId.mockRejectedValue(new Error('not found'))
    await w.find('[data-testid="cr-search-input"]').setValue('nope')
    await w.find('[data-testid="cr-search-btn"]').trigger('click')
    await settle()
    expect(w.find('[data-testid="cr-search-error"]').text()).toContain('not found')
  })

  it('过滤命中 id / 协议 / 客户端 / 租户四个维度', async () => {
    const w = await factory()
    const liveIds = () => w.findAll('[data-testid="cr-live-row"] .cr-link').map((b) => b.text())
    const filt = w.find('[data-testid="cr-filter-input"]')
    await filt.setValue('ws')
    await settle()
    expect(liveIds()).toEqual(['live-b', 'live-d'])
    await filt.setValue('python')
    await settle()
    expect(liveIds()).toEqual(['live-d'])
    await filt.setValue('other')
    await settle()
    expect(liveIds()).toEqual(['live-d'])
    await filt.setValue('live-a')
    await settle()
    expect(liveIds()).toEqual(['live-a'])
  })

  it('两节共用同一个过滤词（live 空了 closed 也可能空）', async () => {
    const w = await factory()
    await w.find('[data-testid="cr-filter-input"]').setValue('node')
    await settle()
    expect(w.findAll('[data-testid="cr-live-row"]')).toHaveLength(1)
    expect(w.find('[data-testid="cr-closed"] .cr-empty').exists()).toBe(true)
    // ★ 两节的计数都必须**跟着过滤走**。第一版只断了 closed 那节 ⇒
    //   「把 live 节的 filteredLive.length 换成 live.length」是行为等价之外的可见错误，
    //   却因为没人断它而无牙。
    expect(w.find('[data-testid="cr-live"] .cr-count').text()).toBe('1')
    expect(w.find('[data-testid="cr-closed"] .cr-count').text()).toBe('0')
  })
})

describe('ConnectionRegistryView：三态（骨架屏 / 空态 / 页面隐藏）', () => {
  it('首屏：出 3 张骨架卡，两节都不渲染', async () => {
    // 让首屏请求永不落数据 ⇒ firstLoading 一直为真 ⇒ 出骨架屏
    fetchConnectionRegistry.mockImplementationOnce(() => new Promise(() => {}))
    const w = mount(ConnectionRegistryView, { global: { plugins: [i18n] } })
    await settle(2)
    expect(w.find('[data-testid="cr-skeleton"]').exists()).toBe(true)
    expect(w.findAll('.cr-skeleton-card')).toHaveLength(3)
    expect(w.find('[data-testid="cr-live"]').exists()).toBe(false)
  })

  it('某节筛空：出该节的 .cr-empty，另一节照常', async () => {
    const w = await factory()
    await w.find('[data-testid="cr-filter-input"]').setValue('only-closed-matches')
    await settle()
    expect(w.find('[data-testid="cr-live"] .cr-empty').text()).toBe('当前没有在线连接。')
    expect(w.find('[data-testid="cr-live"] .cr-table').exists()).toBe(false)
    expect(w.find('[data-testid="cr-closed"] .cr-empty').text()).toBe('近期没有关闭的连接。')
    expect(w.findAll('[data-testid="cr-closed-row"]')).toHaveLength(0)
  })

  it('页面隐藏：整块内容不渲染（但搜索行与状态行还在）', async () => {
    const w = await factory()
    Object.defineProperty(document, 'hidden', { writable: true, configurable: true, value: true })
    document.dispatchEvent(new Event('visibilitychange'))
    await settle()
    expect(w.find('[data-testid="cr-live"]').exists()).toBe(false)
    expect(w.find('[data-testid="cr-skeleton"]').exists()).toBe(false)
    expect(w.find('[data-testid="cr-search-row"]').exists()).toBe(true)
    expect(w.find('[data-testid="cr-capacity"]').exists()).toBe(true)
  })
})

describe('ConnectionRegistryView：轮询与 tick', () => {
  it('15s 后重新取数一次', async () => {
    const w = await factory()
    const before = fetchConnectionRegistry.mock.calls.length
    await vi.advanceTimersByTimeAsync(15000)
    await settle()
    expect(fetchConnectionRegistry.mock.calls.length).toBe(before + 1)
    void w
  })

  it('★ 页面隐藏时不发请求', async () => {
    await factory()
    Object.defineProperty(document, 'hidden', { writable: true, configurable: true, value: true })
    const before = fetchConnectionRegistry.mock.calls.length
    await vi.advanceTimersByTimeAsync(15000)
    await settle()
    expect(fetchConnectionRegistry.mock.calls.length, '页面隐藏仍在轮询').toBe(before)
  })

  it('1s tick 让相对时间往前走（相对时间是活的，不是快照）', async () => {
    const w = await factory()
    expect(w.findAll('[data-testid="cr-live-row"]')[0]!.findAll('td')[4]!.text()).toBe('5s')
    await vi.advanceTimersByTimeAsync(2000)
    await settle()
    expect(w.findAll('[data-testid="cr-live-row"]')[0]!.findAll('td')[4]!.text()).toBe('7s')
  })

  it('卸载后两个定时器都停（不再取数、相对时间不再走）', async () => {
    const w = await factory()
    w.unmount()
    const before = fetchConnectionRegistry.mock.calls.length
    await vi.advanceTimersByTimeAsync(30000)
    await settle()
    expect(fetchConnectionRegistry.mock.calls.length, '卸载后仍在轮询').toBe(before)
  })
})

describe('ConnectionRegistryView：compact 卡片形态', () => {
  beforeEach(() => mockWindowClass('compact'))

  it('两节各出卡片不出表；不渲染切换钮', async () => {
    const w = await factory()
    expect(cardsIn(w, 'cr-live')).toHaveLength(4)
    expect(cardsIn(w, 'cr-closed')).toHaveLength(2)
    expect(w.find('.cr-table').exists()).toBe(false)
    expect(w.find('.responsive-data-view__switch').exists()).toBe(false)
  })

  it('卡头就是 request_id（键与脸同源，不需要 titleFormat）', async () => {
    const w = await factory()
    const live = cardsIn(w, 'cr-live')
    expect(live.map((c) => c.find('.card__title').text())).toEqual(['live-a', 'live-b', 'live-c', 'live-d'])
    const closed = cardsIn(w, 'cr-closed')
    expect(closed.map((c) => c.find('.card__title').text())).toEqual(['closed-a', 'closed-b'])
  })

  it('live 卡 4 个字段：协议 / 客户端 / 帧数 / 最后一帧（相对时间复用同一个 elapsedOf）', async () => {
    const w = await factory()
    const live = cardsIn(w, 'cr-live')
    expect(live[0]!.findAll('.card__field')).toHaveLength(4)
    expect(live[0]!.findAll('.card__field').map((f) => f.find('dt').text())).toEqual(['协议', '客户端', '帧数', '最后一帧'])
    expect(fieldValue(live[0]!, '最后一帧').text()).toBe('5s')
    expect(fieldValue(live[2]!, '最后一帧').text()).toBe('2h')
    expect(fieldValue(live[2]!, '客户端').text()).toBe('—')
    expect(fieldValue(live[2]!, '协议').text()).toBe('—')
  })

  it('★ 帧数缺值在卡片上也是 0（与桌面同口径，不做第二份）', async () => {
    const w = await factory()
    const live = cardsIn(w, 'cr-live')
    expect(fieldValue(live[2]!, '帧数').text()).toBe('0')
    expect(fieldValue(live[0]!, '帧数').text()).toBe('12')
  })

  it('closed 卡 4 个字段，第 3 列换成关闭原因，末列是注册于而非最后一帧', async () => {
    const w = await factory()
    const closed = cardsIn(w, 'cr-closed')
    expect(closed[0]!.findAll('.card__field').map((f) => f.find('dt').text())).toEqual(['协议', '关闭原因', '帧数', '注册于'])
    expect(fieldValue(closed[0]!, '关闭原因').text()).toBe('client_closed')
    expect(fieldValue(closed[1]!, '关闭原因').text()).toBe('—')
    expect(fieldValue(closed[0]!, '注册于').text()).toBe(fmtDateTime24h(iso(7200)))
  })

  it('每张卡有且只有一个动作：打开请求旅程', async () => {
    const w = await factory()
    for (const c of [...cardsIn(w, 'cr-live'), ...cardsIn(w, 'cr-closed')]) {
      const acts = c.findAll('.card__actions button')
      expect(acts).toHaveLength(1)
      expect(acts[0]!.text()).toBe('打开请求旅程')
    }
  })

  it('点卡片上的动作跳对应那条旅程', async () => {
    const w = await factory()
    await cardsIn(w, 'cr-live')[2]!.find('.card__actions button').trigger('click')
    expect(pushMock).toHaveBeenCalledWith({ name: 'request-journey-detail', params: { requestId: 'live-c' } })
    pushMock.mockClear()
    await cardsIn(w, 'cr-closed')[1]!.find('.card__actions button').trigger('click')
    expect(pushMock).toHaveBeenCalledWith({ name: 'request-journey-detail', params: { requestId: 'closed-b' } })
  })

  it('过滤在两张卡片侧同样生效', async () => {
    const w = await factory()
    await w.find('[data-testid="cr-filter-input"]').setValue('ws')
    await settle()
    expect(cardsIn(w, 'cr-live')).toHaveLength(2)
    expect(cardsIn(w, 'cr-closed')).toHaveLength(1)
  })

  it('某节筛空：两档都走该节的 .cr-empty（容器不挂载）', async () => {
    const w = await factory()
    await w.find('[data-testid="cr-filter-input"]').setValue('node')
    await settle()
    // ★ 'node' 命中的是 **live-b 的 client_type** ⇒ live 还有 1 张，空的是 closed
    expect(cardsIn(w, 'cr-live')).toHaveLength(1)
    expect(cardsIn(w, 'cr-closed')).toHaveLength(0)
    // 空的那一节：容器**不挂载**（连 card-list 都没有），只有页面自己的 .cr-empty
    expect(w.find('[data-testid="cr-closed"] [data-testid="card-list"]').exists()).toBe(false)
    expect(w.find('[data-testid="cr-closed"] .cr-empty').text()).toBe('近期没有关闭的连接。')
  })

  it('两节都筛空：两个容器都不挂载，各出自己的 .cr-empty', async () => {
    const w = await factory()
    await w.find('[data-testid="cr-filter-input"]').setValue('zzz-no-such')
    await settle()
    expect(cardsIn(w, 'cr-live')).toHaveLength(0)
    expect(cardsIn(w, 'cr-closed')).toHaveLength(0)
    expect(w.find('[data-testid="cr-live"] .cr-empty').text()).toBe('当前没有在线连接。')
    expect(w.find('[data-testid="cr-closed"] .cr-empty').text()).toBe('近期没有关闭的连接。')
    expect(w.find('[data-testid="card-list"]').exists()).toBe(false)
  })

  it('卡片里不出现 undefined / null 字面量', async () => {
    const w = await factory()
    for (const c of [...cardsIn(w, 'cr-live'), ...cardsIn(w, 'cr-closed')]) {
      expect(c.text()).not.toContain('undefined')
      expect(c.text()).not.toContain('null')
    }
  })
})

describe('ConnectionRegistryView：跨层契约', () => {
  it('容器挂在 v-if 上（空态时整块撤掉）⇒ 不传 :empty / :loading', () => {
    expect((codeOnly.match(/<ResponsiveDataView/g) ?? []).length).toBe(2)
    expect(codeOnly).toContain('v-if="filteredLive.length"')
    expect(codeOnly).toContain('v-if="filteredClosed.length"')
    expect(codeOnly).not.toMatch(/<ResponsiveDataView[\s\S]{0,400}?\s:empty=/)
    expect(codeOnly).not.toMatch(/<ResponsiveDataView[\s\S]{0,400}?\s:loading=/)
  })

  it('.cr-table-wrap 已删（容器自带 overflow-x，嵌套会出双滚动条）', () => {
    expect(codeOnly).not.toContain('cr-table-wrap')
  })

  it('★ 卡片动作的触控下限 ≥48px（变异 M18 无牙后补的）', () => {
    // 桌面 `.cr-link` 是行内小字；按钮化后不抬到 48px 就不满足 Android 控件基线。
    expect(codeOnly).toMatch(/\.cr-action\s*\{[^}]*min-height:\s*48px;/)
  })

  it('table-min-width 传 0px（.cr-table 本页没有 min-width，传默认 720 会凭空出横滚）', () => {
    expect((codeOnly.match(/table-min-width="0px"/g) ?? []).length).toBe(2)
    expect(codeOnly).toMatch(/\.cr-table\s*\{[^}]*\}/)
    expect(codeOnly).not.toMatch(/\.cr-table\s*\{[^}]*min-width/)
  })

  it('title-key 是 request_id（且两张表都靠它保证 :key 唯一）', () => {
    expect((codeOnly.match(/title-key="request_id"/g) ?? []).length).toBe(2)
    // 不需要 titleFormat：键与脸同源
    expect(codeOnly).not.toContain(':title-format=')
  })

  it('本页不引入连续加载（接口一次返回 live + closed 两段）', () => {
    expect(codeOnly).not.toContain('HyperLoadMore')
    expect(codeOnly).not.toContain('createHyperPages')
  })

  it('相对时间与绝对时间各只有一份实现（表格与卡片共用）', () => {
    expect((codeOnly.match(/function elapsedOf\(/g) ?? []).length).toBe(1)
    expect((codeOnly.match(/elapsedOf\(/g) ?? []).length).toBe(3)      // 定义 + 表格 + 卡片字段
    expect(codeOnly).toContain('format: (v) => elapsedOf(v == null ? undefined : String(v))')
    expect(codeOnly).toContain('format: (v) => fmtDateTime24h(v == null ? undefined : String(v))')
    // import 语句里没有 `fmtDateTime24h(`（那是具名导入），所以**调用点只有 2 处**
    expect((codeOnly.match(/fmtDateTime24h\(/g) ?? []).length).toBe(2)
  })

  it('轮询与 tick 都有 onUnmounted 清理（不卸载就是活实例泄漏）', () => {
    const seg = codeOnly.slice(codeOnly.indexOf('onUnmounted('), codeOnly.indexOf('</script>'))
    expect(seg).toContain('stopPoll()')
    expect(seg).toContain('stopTick()')
    expect(seg).toContain("removeEventListener('visibilitychange'")
  })

  it('页面没有硬编码中文文案（全部走 i18n）', () => {
    const cjk = codeOnly.match(/[一-鿿]/g)
    expect(cjk, `出现了硬编码中文：${cjk?.join('')}`).toBeNull()
  })

  it('没有新增请求端点（仍走 api 层的两个 fetch）', () => {
    for (const banned of ['fetch(', 'axios', 'XMLHttpRequest']) {
      expect(codeOnly, `不应直接发起请求，却出现了 ${banned}`).not.toContain(banned)
    }
    expect(codeOnly).toContain('fetchConnectionRegistry')
    expect(codeOnly).toContain('fetchConnectionByRequestId')
  })
})
