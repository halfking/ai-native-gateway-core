// CompressionView.responsive.test.ts — H6 第五条垂直切片的门禁。
//
// 与切片四（AuditLogView）同一取舍：**桌面三态仍由本页自己出**。
// 本页的三态是 `.loading-hint` / `.empty-hint` 两个**表格外的**兄弟节点
// （`v-if` / `v-else-if` / `v-else` 链），不是表内行 —— 形态比切片四简单，
// 但结论一致：交给容器会让桌面刷新时那句「加载中…」被换成一个转圈。
//
// 门禁清单：
// 1. 桌面零回归：7 列表头 / 策略徽章译名 / token 千分位 / 节省数 0 出破折号 /
//    两条三态 div / 页码条 / 点行跳路由
// 2. **切档位只发一遍**（接入时发现的既有双发缺陷）
// 3. compact 连续加载：卡片、字段格式化、跨页去重、revision 闸门、失败可重试
// 4. 跨层契约：时间区间单一真源、rowKey 用主键、两条路径不共享 ref、无硬编码中文
import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import CompressionView from './CompressionView.vue'
import { _resetForTests } from '../composables/useWindowClass'
import { _resetDataViewModeForTests } from '../composables/useDataViewMode'

const source = readFileSync(resolve(process.cwd(), 'src/views/CompressionView.vue'), 'utf8')
const codeOnly = source
  .replace(/\/\*[\s\S]*?\*\//g, '')
  .replace(/<!--[\s\S]*?-->/g, '')
  .replace(/(^|[^:'"`\\])\/\/[^\n]*/g, '$1')

/**
 * ★ 满页 fixture 依赖组件里的 `sessionPageSize`，所以从源码取值而不是写死 50。
 *   锚点找不到就**抛错** —— 不抛的话「短页即无更多」会让尾部控件直接 exhausted，
 *   后面每条依赖「第 2 页」的用例都会静默作废（跑绿，但什么都没验）。
 */
const sizeAnchor = source.match(/const sessionPageSize = (\d+)/)
if (!sizeAnchor) {
  throw new Error('锚点 `const sessionPageSize = <数字>` 在 CompressionView.vue 里不见了 —— 本文件的满页 fixture 与它绑定，请一并改这里')
}
const PAGE_SIZE = Number(sizeAnchor[1])

const { statCalls, sessionCalls, ctrl, mockApi } = vi.hoisted(() => {
  type Session = Record<string, unknown>
  let gen = 0
  const statCalls: Array<Record<string, unknown>> = []
  const sessionCalls: Array<Record<string, unknown>> = []
  const ctrl = {
    defer: false,
    resolvers: [] as Array<() => void>,
    fullPage1: false,
    empty: false,
    fail: false,
    reset() {
      ctrl.defer = false
      ctrl.resolvers = []
      ctrl.fullPage1 = false
      ctrl.empty = false
      ctrl.fail = false
      // ★ `gen` 是闭包变量，不在 ctrl 上 —— 漏清它第 N 个用例就拿到 #g15，
      //   而 'g1' 是 'g15' 的子串，断言会两边一起错却照样绿。
      gen = 0
    },
  }

  /**
   * 三种策略，覆盖 `strategyLabel` 的两条分支：
   * 前两条在词条里有译名，第三条没有 —— 必须回落成原始码，
   * 而不是把 `compression.zzz.unknown_strategy` 这种 key 甩到界面上。
   */
  const STRATEGIES = ['delta_append', 'sliding_window_token', 'zzz.unknown_strategy']

  // i 可以大于 23，所以走 Date.UTC（模板拼串会造出 T051: 非法时间）
  const ts = (i: number): string => new Date(Date.UTC(2026, 9, 5, i % 24, 15, 30)).toISOString()

  const mk = (i: number, generation: number): Session => ({
    // 前缀带代数标记且**长度 > 12** ⇒ shortID 会截断，卡头上只剩 `g{gen}-ses…`
    gw_session_id: `g${generation}-session-${String(i).padStart(4, '0')}-padding`,
    compression_strategy: STRATEGIES[(i - 1) % STRATEGIES.length],
    request_count: 10 + i,
    first_ts: ts(i),
    last_ts: ts(i + 1),
    outbound_msg_count: i % 2 === 0 ? null : 100 + i, // 偶数 ⇒ 表格出破折号
    outbound_token_est: i % 3 === 0 ? null : 1234 * (i + 1), // ⇒ fmtNum 的 K 分支
    estimated_original_msgs: 200,
    msg_reduction: i % 2 === 0 ? 0 : 7 * i, // 0 ⇒ 出破折号；> 0 ⇒ -N
    sample_request_id: `req-${i}`,
  })

  const STATS = {
    total_requests: 1200,
    compressed_total: 900,
    compression_rate: 0.75,
    strategy_distribution: { delta_append: 500, sliding_window_token: 400 },
    total_outbound_tokens: 123456,
    estimated_original_tokens: 200000,
    estimated_tokens_saved: 76544,
    hourly_series: [
      { hour: ts(1), total: 10, compressed: 8, rate: 0.8 },
      { hour: ts(2), total: 20, compressed: 12, rate: 0.6 },
    ],
  }

  return {
    statCalls,
    sessionCalls,
    ctrl,
    mockApi: () => ({
      getCompressionStats: vi.fn(async (params: Record<string, unknown> = {}) => {
        statCalls.push(params)
        // ★ 统计**不**参与 defer：defer 只留给列表。
        //   否则 compact 下 `loading` 恒为 true，刷新钮的忙碌态就分不清
        //   「统计在取」与「列表在取」—— 那条断言会变成恒真。
        return STATS
      }),
      getCompressionSessions: vi.fn(async (params: Record<string, unknown> = {}) => {
        sessionCalls.push(params)
        const page = Number(params.page ?? 1)
        const generation = ++gen
        if (ctrl.fail) throw new Error('compression boom')
        let items: Session[]
        if (ctrl.empty) items = []
        else if (ctrl.fullPage1) {
          items =
            page === 1
              ? Array.from({ length: PAGE_SIZE }, (_, k) => mk(k + 1, generation))
              : [mk(PAGE_SIZE + 1, generation), mk(PAGE_SIZE + 2, generation)]
        } else {
          // ★ 每一页都返回 3 行（不按 page 变化）。第一版写成 `page === 1 ? [...] : []`，
          //   于是「点下一页」之后表格整张消失（空态分支接管）—— 红的不是产品，是 fixture。
          //   跨页去重另有 `fullPage1` 满页 fixture 专门覆盖。
          items = [mk(1, generation), mk(2, generation), mk(3, generation)]
        }
        if (ctrl.defer) await new Promise<void>((res) => ctrl.resolvers.push(res))
        return { items, count: ctrl.empty ? 0 : 120 }
      }),
    }),
  }
})

vi.mock('../api', () => mockApi())
vi.mock('../api/settings', () => ({
  getSetting: vi.fn(async () => ({ value: 'x', spec: { default: 'x' } })),
}))

const push = vi.hoisted(() => ({ fn: vi.fn() }))
vi.mock('vue-router', () => ({ useRouter: () => ({ push: push.fn, replace: vi.fn() }) }))

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: {
    'zh-CN': {
      common: { button: { filter: '筛选' } },
      settings: { compression: { enumLabels: { off: '关闭', smart: '智能' } } },
      hyper: {
        dataView: { table: '表格', cards: '卡片' },
        list: {
          empty: '暂无记录', allLoaded: '已全部加载 {count} 条', loadFailed: '加载失败，点击重试',
          retry: '重试', loadMore: '继续加载', loadingMore: '加载中…', refreshing: '正在刷新…',
        },
      },
      compression: {
        loading: '加载中…',
        refresh: '刷新',
        // ★ 故意只给前两条：zzz.unknown_strategy 必须回落成原始码
        delta_append: '增量拼接',
        sliding_window_token: '滑动窗口(Token)',
        timeBucketHour: '每小时',
        timeBucket6Hour: '每 6 小时',
        timeBucketDay: '每天',
        charts: { noData: '暂无数据' },
        pagination: { previous: '上一页', next: '下一页', pageInfo: '第 {current} / {total} 页' },
        table: {
          title: '会话压缩详情', count: '共 {n} 条',
          sessionId: '会话ID', strategy: '策略', requests: '请求数',
          compressedMsgs: '压缩消息数', compressedTokens: '压缩Token数',
          msgSaved: '消息节省', lastTime: '最后时间',
          empty: '所选时间段内没有压缩会话记录',
        },
      },
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

async function factory() {
  const w = mount(CompressionView, { global: { plugins: [i18n] } })
  await flushPromises()
  await flushPromises()
  return w
}

/**
 * ★ 必须限定在 `[data-testid="card-list"]` 里取卡片 —— 本页模板里若有
 *   `class="card ..."` 的外壳（`session-card` 就带 card 类），裸 `.card` 会
 *   把外壳一起选中，第一轮在切片四就是这么错的（详见该文件同名辅助函数）。
 */
function cardsOf(w: Awaited<ReturnType<typeof factory>>) {
  return w.findAll('[data-testid="card-list"] .card')
}

/** 取第 `index` 张卡里、`<dt>` 文案为 `label` 的字段值。作用域必须是那一张卡。 */
function fieldValue(w: Awaited<ReturnType<typeof factory>>, index: number, label: string) {
  const card = cardsOf(w)[index]
  expect(card, `没有第 ${index} 张卡`).toBeTruthy()
  const hit = card!.findAll('.card__field').find((f) => f.find('dt').text() === label)
  expect(hit, `卡片里找不到字段「${label}」，实际有：${card!.findAll('.card__field').map((f) => f.find('dt').text()).join('/')}`).toBeTruthy()
  return hit!.find('dd').text()
}

beforeEach(() => {
  _resetDataViewModeForTests()
  statCalls.length = 0
  sessionCalls.length = 0
  push.fn.mockClear()
  ctrl.reset()
  mockWindowClass('expanded')
})

afterEach(() => vi.clearAllMocks())

describe('CompressionView：桌面页码路径零回归', () => {
  it('7 列表头，文案与顺序不变', async () => {
    const w = await factory()
    const ths = w.findAll('table thead th')
    expect(ths).toHaveLength(7)
    expect(ths.map((th) => th.text())).toEqual([
      '会话ID', '策略', '请求数', '压缩消息数', '压缩Token数', '消息节省', '最后时间',
    ])
  })

  it('行内策略徽章走译名；无词条时回落原始码', async () => {
    const w = await factory()
    const badges = w.findAll('table tbody .strategy-badge')
    expect(badges).toHaveLength(3)
    expect(badges[0].text()).toBe('增量拼接')
    expect(badges[1].text()).toBe('滑动窗口(Token)')
    expect(badges[2].text()).toBe('zzz.unknown_strategy')
  })

  it('token 走 fmtNum 的 K 分支；null 出破折号', async () => {
    const w = await factory()
    const cells = w.findAll('table tbody tr').map((tr) => tr.findAll('td')[4].text())
    expect(cells[0]).toBe('2.5K') // 1234 * (1+1) = 2468
    expect(cells[2]).toBe('—') // i=3 命中 i%3===0 的 null 分支
  })

  it('消息节省：只有 > 0 才出负号，0 出破折号（不是 0）', async () => {
    const w = await factory()
    const cells = w.findAll('table tbody tr').map((tr) => tr.findAll('td')[5].text())
    // fixture：msg_reduction = i%2===0 ? 0 : 7*i
    expect(cells[0]).toBe('-7') // i=1 奇数 ⇒ 7
    expect(cells[1]).toBe('—') // i=2 偶数 ⇒ 0
    expect(cells[2]).toBe('-21') // i=3 奇数 ⇒ 21
  })

  it('行内时间走本地化，不出现裸 ISO 串', async () => {
    const w = await factory()
    const cell = w.findAll('table tbody tr')[0].findAll('td')[6].text()
    expect(cell).not.toMatch(/\d{4}-\d{2}-\d{2}T/)
    expect(cell).toMatch(/\d{1,2}[/:]\d{2}/)
  })

  /**
   * ★ 桌面 loading 仍是那句文字，**表格根本不出现**（`v-else` 未进），
   *   且容器没有接手（`.responsive-data-view__state` / `.app-empty-state` 都不该在）。
   */
  it('loading 时仍是 .loading-hint 一行文字，容器不接手', async () => {
    ctrl.defer = true
    const w = await factory()
    expect(w.find('.loading-hint').text()).toContain('加载中')
    expect(w.find('table').exists(), 'loading 时不该出表格').toBe(false)
    expect(w.find('.responsive-data-view__state').exists()).toBe(false)
    expect(w.find('.app-empty-state').exists()).toBe(false)
  })

  it('empty 时仍是 .empty-hint，不是 EmptyState', async () => {
    ctrl.empty = true
    const w = await factory()
    expect(w.find('.empty-hint').text()).toContain('所选时间段内没有压缩会话记录')
    expect(w.find('.app-empty-state').exists()).toBe(false)
    expect(w.find('.count-badge').text()).toContain('共 0 条')
    expect(w.find('.pagination').exists(), 'count <= 每页条数时不出页码条').toBe(false)
  })

  it('count 徽章显示服务端 count', async () => {
    const w = await factory()
    expect(w.find('.count-badge').text()).toContain('共 120 条')
  })

  it('首屏只请求第 1 页，且带 timeRangeParams 的 hours', async () => {
    const w = await factory()
    expect(sessionCalls).toHaveLength(1)
    expect(sessionCalls[0].page).toBe(1)
    expect(sessionCalls[0].page_size).toBe(PAGE_SIZE)
    expect(sessionCalls[0].hours).toBe(24)
    // 统计与列表共用同一份时间区间真源 ⇒ 两边的 hours 一定相等
    expect(statCalls).toHaveLength(1)
    expect(statCalls[0].hours).toBe(sessionCalls[0].hours)
  })

  it('出页码条（count > 每页条数），点下一页按 page=2 重取', async () => {
    const w = await factory()
    expect(w.find('.pagination').exists()).toBe(true)
    const next = w.findAll('.pagination button')[1]
    await next.trigger('click')
    await flushPromises()
    expect(sessionCalls[1].page).toBe(2)
    expect(w.find('table thead').exists()).toBe(true)
  })

  /**
   * ★ 接入时发现的既有缺陷：`switchTab` 自己调一次 `loadAll`，
   *   `watch(activeTab, loadAll)` 又调一次 ⇒ 每次切档位发**两遍**统计 + 两遍列表。
   *   修法：删掉 `switchTab` 里的那次调用（watch 覆盖），不删 watch。
   */
  it('切档位只发一遍统计 + 一遍列表（不是两遍）', async () => {
    const w = await factory()
    expect(statCalls).toHaveLength(1)
    expect(sessionCalls).toHaveLength(1)
    await w.findAll('.tab-btn')[1].trigger('click')
    await flushPromises()
    await flushPromises()
    expect(statCalls, '切档位发了两遍统计').toHaveLength(2)
    expect(sessionCalls, '切档位发了两遍列表').toHaveLength(2)
    // 且第二遍的页码从 1 重来
    expect(sessionCalls[1].page).toBe(1)
  })

  /**
   * ★ 页码复位的**行为**证明。
   *   第一版只断言「切档位后那次请求 page === 1」—— 但那时页码本来就还是 1
   *   （从没翻过页），所以把 `reload()` 里的 `sessionPage.value = 1` 删掉也照样绿
   *   （变异 M29 无牙）。必须**先翻到第 2 页**再切档位，差异才显现。
   */
  it('先翻到第 2 页再切档位：请求回到 page=1 且区间跟着换', async () => {
    const w = await factory()
    await w.findAll('.pagination button')[1].trigger('click')
    await flushPromises()
    expect(sessionCalls[1].page).toBe(2)

    await w.findAll('.tab-btn')[1].trigger('click')
    await flushPromises()
    await flushPromises()
    expect(sessionCalls[2].page, '切档位后页码应复位到 1').toBe(1)
    expect(sessionCalls[2].hours).toBe(168)
  })

  it('切档位后时间区间跟着换（7d ⇒ hours=168）', async () => {
    const w = await factory()
    await w.findAll('.tab-btn')[1].trigger('click')
    await flushPromises()
    expect(sessionCalls[1].hours).toBe(168)
  })

  it('点行跳到该会话的请求日志', async () => {
    const w = await factory()
    await w.find('table tbody tr.session-row').trigger('click')
    expect(push.fn).toHaveBeenCalledTimes(1)
    const arg = push.fn.mock.calls[0][0]
    expect(arg.path).toBe('/request-logs')
    expect(arg.query.gw_session_id).toMatch(/^g1-session-0001/)
  })

  it('桌面端不出连续加载尾部，也不出卡片', async () => {
    const w = await factory()
    expect(w.find('.hyper-load-more').exists()).toBe(false)
    expect(w.find('[data-testid="card-list"]').exists()).toBe(false)
  })
})

describe('CompressionView：compact 连续加载路径', () => {
  beforeEach(() => mockWindowClass('compact'))

  it('出卡片不出表格；不渲染页码条', async () => {
    const w = await factory()
    expect(w.find('[data-testid="card-list"]').exists()).toBe(true)
    expect(w.find('table').exists()).toBe(false)
    expect(w.find('.pagination').exists()).toBe(false)
    expect(w.find('.hyper-load-more').exists()).toBe(true)
  })

  /**
   * ★ 卡头必须是**截断**后的 id。
   *   `gw_session_id` 是完整长串，直接当标题会把整张卡撑爆；
   *   截断规则与表格单元格共用 `shortID`（8 字符 + …），不做第二份。
   */
  it('卡头是截断后的会话 id，不是完整长串', async () => {
    const w = await factory()
    const title = w.findAll('.card__title')[0].text()
    // shortID = slice(0,8) + '…' ⇒ 'g1-session-' 的前 8 个字符是 'g1-sessi'
    expect(title).toBe('g1-sessi…')
    expect(title).toMatch(/g1(?![\d])/)
    expect(title.length).toBeLessThan(12)
  })

  it('策略字段走 format：有译名的出译名，无词条的回落原始码', async () => {
    const w = await factory()
    expect(fieldValue(w, 0, '策略')).toBe('增量拼接')
    expect(fieldValue(w, 1, '策略')).toBe('滑动窗口(Token)')
    expect(fieldValue(w, 2, '策略')).toBe('zzz.unknown_strategy')
  })

  it('请求数是 metric 型右对齐，且是原始数字（没走 format 也不该变 undefined）', async () => {
    const w = await factory()
    const card = cardsOf(w)[0]
    const dd = card.findAll('.card__field').find((f) => f.find('dt').text() === '请求数')!.find('dd')
    expect(dd.text()).toBe('11')
    expect(dd.attributes('data-align')).toBe('end')
  })

  it('压缩 Token 走 fmtNum 的 K 分支；null 出破折号', async () => {
    const w = await factory()
    expect(fieldValue(w, 0, '压缩Token数')).toBe('2.5K')
    expect(fieldValue(w, 2, '压缩Token数')).toBe('—')
  })

  it('消息节省：> 0 出负号，0 出破折号', async () => {
    const w = await factory()
    expect(fieldValue(w, 0, '消息节省')).toBe('-7') // i=1 奇数 ⇒ 7*1
    expect(fieldValue(w, 1, '消息节省')).toBe('—') // i=2 偶数 ⇒ 0
  })

  it('卡片里不出现 undefined / null 字面量', async () => {
    const w = await factory()
    for (const c of cardsOf(w)) {
      expect(c.text()).not.toContain('undefined')
      expect(c.text()).not.toContain('null')
    }
  })

  it('最后时间字段走本地化，不出现裸 ISO 串', async () => {
    const w = await factory()
    const time = fieldValue(w, 0, '最后时间')
    expect(time).not.toMatch(/\d{4}-\d{2}-\d{2}T/)
    expect(time).toMatch(/\d{1,2}[/:]\d{2}/)
  })

  it('count 徽章在 compact 下取连续加载带回来的服务端 count', async () => {
    const w = await factory()
    // 页码 ref `sessionsCount` 在 compact 下从不被写 —— 若接线漏了，这里会是「共 0 条」
    expect(w.find('.count-badge').text()).toContain('共 120 条')
  })

  it('empty 时出 EmptyState，且不再有 .empty-hint', async () => {
    ctrl.empty = true
    const w = await factory()
    expect(w.find('.app-empty-state').exists()).toBe(true)
    expect(w.find('.app-empty-state').text()).toContain('所选时间段内没有压缩会话记录')
    expect(w.find('.empty-hint').exists()).toBe(false)
  })

  /**
   * ★ rowKey 的**行为**证明。满页 fixture 是必要的：否则「短页即无更多」
   *   会让尾部控件直接 exhausted。第 2 页用 id 951+/952+（与第 1 页下标相同、
   *   主键不同），所以只有「按主键去重」的实现才拿得到 PAGE_SIZE + 2 张卡。
   */
  it('跨页不去重：第 2 页两行不被第 1 页同下标行吃掉', async () => {
    ctrl.fullPage1 = true
    const w = await factory()
    expect(cardsOf(w)).toHaveLength(PAGE_SIZE)
    await w.find('.hyper-load-more__btn--manual').trigger('click')
    await flushPromises()
    await flushPromises()
    expect(cardsOf(w)).toHaveLength(PAGE_SIZE + 2)
  })

  /**
   * ★ revision 闸门的**行为**证明。
   *   `loadFirst()` 遇到第 1 页**在途**会提前 return（单飞）且**不**提升 revision ——
   *   没有显式 `invalidate()` 时，切档位会被当成重复调用加入旧请求，
   *   **新档位永远发不出去**（calls 停在 1，界面停在旧结果）。
   */
  it('compact 下切档位会作废在途的第 1 页：既发出新请求，旧结果也不落地', async () => {
    ctrl.defer = true
    const w = mount(CompressionView, { global: { plugins: [i18n] } })
    await flushPromises()
    expect(sessionCalls).toHaveLength(1)

    await w.findAll('.tab-btn')[1].trigger('click')
    await flushPromises()
    expect(sessionCalls, '没有 invalidate 时第二次切档位会被单飞吞掉').toHaveLength(2)
    expect(sessionCalls[1].hours).toBe(168)

    for (const r of ctrl.resolvers.splice(0)) r()
    await flushPromises()
    await flushPromises()
    const list = w.find('[data-testid="card-list"]').text()
    expect(list, '旧请求（gen 1）的结果不应落地').not.toMatch(/g1(?![\d])/)
    expect(list, '新请求（gen 2）的结果应落地').toMatch(/g2(?![\d])/)
  })

  it('统计与图表不分档：compact 下切档位照样重发统计', async () => {
    const w = await factory()
    expect(statCalls).toHaveLength(1)
    await w.findAll('.tab-btn')[2].trigger('click')
    await flushPromises()
    expect(statCalls).toHaveLength(2)
    expect(statCalls[1].hours).toBe(720)
  })

  it('请求失败时出可重试状态，且不出现 undefined', async () => {
    ctrl.fail = true
    const w = await factory()
    // 失败必须被 createHyperPages 收得到才会转 failed；
    // fetchPage 里若写了 try/catch 吞掉，状态会永远停在「加载中」
    expect(w.find('.hyper-load-more').attributes('data-state')).toBe('failed')
    expect(w.find('.hyper-load-more__btn').exists()).toBe(true)
    for (const c of cardsOf(w)) expect(c.text()).not.toContain('undefined')
  })

  it('会话 ID 字段也走 shortID 截断（不是完整长串）', async () => {
    const w = await factory()
    expect(fieldValue(w, 0, '会话ID')).toBe('g1-sessi…')
    expect(fieldValue(w, 0, '会话ID')).not.toContain('padding')
  })

  /**
   * ★ 刷新钮的忙碌态必须同时看**两条**路径的在途信号。
   *   本文件的统计 mock 故意不 defer（见 `vi.hoisted` 里的注释），
   *   所以这里 `loading` 恒为 false —— 若把 `refreshBusy` 退回 `loading`，
   *   手机上取第 1 页时刷新钮就永远可点、也永远不显示「加载中…」。
   */
  it('compact 下取第 1 页时刷新钮禁用且显示「加载中…」', async () => {
    ctrl.defer = true
    const w = await factory()
    expect(statCalls).toHaveLength(1)
    const btn = w.find('.refresh-btn')
    expect(btn.attributes('disabled')).toBeDefined()
    expect(btn.text()).toContain('加载中')
  })

  it('compact 下点卡片跳到该会话的请求日志', async () => {
    const w = await factory()
    await cardsOf(w)[0].find('.card__head').trigger('click')
    expect(push.fn).toHaveBeenCalledTimes(1)
    expect(push.fn.mock.calls[0][0].query.gw_session_id).toMatch(/^g1-session-0001/)
  })
})

describe('CompressionView：跨层契约', () => {
  it('时间区间只有一个真源（统计 / 页码列表 / 连续加载三处共用）', () => {
    expect((codeOnly.match(/function timeRangeParams\(/g) ?? []).length).toBe(1)
    // 定义 1 + loadStats 1 + loadSessions 1 + fetchPage 1 = 4
    expect((codeOnly.match(/timeRangeParams\(\)/g) ?? []).length).toBeGreaterThanOrEqual(4)
  })

  /**
   * ★ 判别信号是**形态**（`\bpage\b` 任意位置），不是「行首的 `page:`」。
   *   第一版只查 `^\s*page:/m` + `page_size` —— 写成
   *   `(params as ...).page = 1` 就注得进去，门照样绿。
   *
   * ★★ 定位也必须是**按块**，不能是固定字符窗口：第二版用 `slice(start, start+400)`，
   *   而注入的那行正好落在 400 字窗口之外 ⇒ 变异 M06 全绿。
   *   教训与「量具坐标系」同族：用固定窗口切函数体，等于把门建在函数外面。
   */
  it('timeRangeParams 不含 page / page_size（那是加载方式，不是筛选条件）', () => {
    const start = codeOnly.indexOf('function timeRangeParams(')
    expect(start, '没找到 timeRangeParams —— 下面的断言会变成「查了个空串」').toBeGreaterThan(-1)
    const body = codeOnly.slice(start, codeOnly.indexOf('\n}', start))
    expect(body).not.toMatch(/\bpage\b/)
    expect(body).not.toMatch(/page_size/)
  })

  it('rowKey 用后端主键 gw_session_id，不是数组下标', () => {
    expect(codeOnly).toContain('rowKey: (s) => s.gw_session_id')
    expect(codeOnly).not.toMatch(/rowKey:\s*\(.*\)\s*=>\s*i\b/)
  })

  it('页码与连续加载各自独立，不共享同一个 ref', () => {
    expect(codeOnly).toContain('const sessions = ref<CompressionSessionItem[]>([])')
    expect(codeOnly).toContain('const continuous = createHyperPages<CompressionSessionItem>(')
    // `(?!=)` 是必需的：`compactBusy` 里写的是 `===`，不带负向先行会永远红
    expect(codeOnly).not.toMatch(/continuous\.(rows|hasMore|state)\.value\s*=(?!=)/)
  })

  it('页码条与连续加载尾部不同时出现（屏幕上不能有两个「加载更多」语义）', () => {
    expect(codeOnly).toContain('!isCompact.value && sessionsCount.value > sessionPageSize')
    expect(codeOnly).toMatch(/<HyperLoadMore[\s\S]{0,60}v-if="isCompact"/)
  })

  it('reload() 统一分派两条路径，且 compact 下先作废再重取', () => {
    expect(codeOnly).toMatch(
      /async function reload\(\)[\s\S]{0,400}continuous\.invalidate\(\)[\s\S]{0,80}continuous\.loadFirst\(\)/,
    )
  })

  it('连续加载的 fetchPage 不吞异常（否则尾部控件永远停在「加载中」）', () => {
    const start = codeOnly.indexOf('fetchPage: async')
    const body = codeOnly.slice(start, start + 500)
    expect(body).not.toMatch(/catch\s*\{/)
    expect(body).toContain('getCompressionSessions')
  })

  it('switchTab 不再自己调 reload（否则切档位发两遍）', () => {
    const start = codeOnly.indexOf('function switchTab(')
    expect(start, '没找到 switchTab').toBeGreaterThan(-1)
    // 按块定位到函数结尾，不用固定窗口（见上面那条门注）
    const body = codeOnly.slice(start, codeOnly.indexOf('\n}', start))
    expect(body).not.toMatch(/reload\(\)/)
    expect(body).not.toMatch(/loadSessions\(\)/)
    // watch 仍必须存在，否则切档位就完全不刷新了
    expect(codeOnly).toMatch(/watch\(activeTab,\s*\(\)\s*=>\s*\{\s*void reload\(\)\s*\}\)/)
  })

  /**
   * ★ 容器只裁 compact 的三态（桌面那两个 div 仍在自己手里）。
   *   摘掉前置 = 桌面刷新时那句「加载中…」被换成转圈、空态文案被换掉。
   */
  it('容器的 loading/empty 只在 compact 生效（桌面三态仍归本页）', () => {
    expect(codeOnly).toMatch(/:loading="isCompact\s*&&\s*compactBusy"/)
    expect(codeOnly).toMatch(/:empty="isCompact\s*&&\s*!compactBusy\s*&&\s*rows\.length\s*===\s*0"/)
  })

  it('桌面三态两个 div 仍在模板里，且都被 isCompact 前置排除', () => {
    expect(codeOnly).toMatch(/class="loading-hint"/)
    expect(codeOnly).toMatch(/v-if="sessionsLoading\s*&&\s*!isCompact"/)
    expect(codeOnly).toMatch(/v-else-if="!isCompact\s*&&\s*!sessions\.length"/)
  })

  it('删掉本页的 .table-wrap（避免与容器自带的 overflow-x 嵌套出双滚动条）', () => {
    expect(codeOnly).not.toContain('table-wrap')
  })

  it('table-min-width 显式传 0px，保持本页原有的列宽策略', () => {
    expect(codeOnly).toContain('table-min-width="0px"')
  })

  it('卡头走 titleFormat（否则完整长串会话 id 会把卡片撑爆）', () => {
    expect(codeOnly).toContain(':title-format="cardTitle"')
    expect(codeOnly).toContain('title-key="gw_session_id"')
    expect(codeOnly).toMatch(/function cardTitle\(row: Record<string, unknown>\): string \{\s*return shortID\(/)
  })

  it('没有新增请求端点（仍走 api 层的 getCompressionSessions / getCompressionStats）', () => {
    for (const banned of ['fetch(', 'axios', 'XMLHttpRequest']) {
      expect(codeOnly, `不应直接发起请求，却出现了 ${banned}`).not.toContain(banned)
    }
    expect(codeOnly).toContain('getCompressionSessions')
    expect(codeOnly).toContain('getCompressionStats')
  })

  it('页面没有硬编码中文文案（全部走 i18n）', () => {
    const cjk = codeOnly.match(/[一-鿿]/g)
    expect(cjk, `模板里出现了硬编码中文：${cjk?.join('')}`).toBeNull()
  })
})
