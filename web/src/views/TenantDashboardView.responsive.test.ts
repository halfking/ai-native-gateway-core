// TenantDashboardView.responsive.test.ts — H6 第六条垂直切片的门禁。
//
// 本页与前五条**结构不同**：它没有分页主列表。真候选是**明细下钻表**
// （点模型柱 / 点某天 → 请求明细）。那张表是全页唯一真正需要卡片的列表，
// 而且它暴露了一个**既有功能缺口**：`getRequestLogs(page: 1, page_size: 50)`
// 之后再无任何翻页入口 ⇒ 超过 50 条的明细后面**根本看不到**。
// compact 侧接入连续加载顺带补上这个缺口；桌面侧**维持现状**（补页码条属新增 UI，
// 不在本次零回归范围内，已登记）。
//
// 门禁清单：
// 1. 桌面零回归：5 列表头 / 模型列 client_model 优先 / 状态徽章三支 + 配色 /
//    积分千分位 / request_id 截断 / toggle 清空 / 两条三态 .empty div
// 2. compact 连续加载：卡片、字段格式化、跨页去重、revision 闸门、失败可重试、
//    **>50 条能继续加载**（本切片的核心价值）
// 3. 本页原有 4 例降级模式门禁不被打破；降级文案已从硬编码中文改为 i18n
import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import TenantDashboardView from './TenantDashboardView.vue'
import { _resetForTests } from '../composables/useWindowClass'
import { _resetDataViewModeForTests } from '../composables/useDataViewMode'

const source = readFileSync(resolve(process.cwd(), 'src/views/TenantDashboardView.vue'), 'utf8')
const codeOnly = source
  .replace(/\/\*[\s\S]*?\*\//g, '')
  .replace(/<!--[\s\S]*?-->/g, '')
  .replace(/(^|[^:'"`\\])\/\/[^\n]*/g, '$1')

/**
 * ★ 满页 fixture 依赖组件里的 `DETAIL_PAGE_SIZE`，从源码取值而不是写死 50。
 *   锚点找不到就抛错 —— 不抛的话「短页即无更多」会让尾部控件直接 exhausted，
 *   后面每条依赖「第 2 页」的用例都会静默作废（跑绿，但什么都没验）。
 */
const sizeAnchor = source.match(/const DETAIL_PAGE_SIZE = (\d+)/)
if (!sizeAnchor) {
  throw new Error('锚点 `const DETAIL_PAGE_SIZE = <数字>` 在 TenantDashboardView.vue 里不见了 —— 本文件的满页 fixture 与它绑定，请一并改这里')
}
const PAGE_SIZE = Number(sizeAnchor[1])

/**
 * ★ 派生 + 钉死，两条一起要：只派生的话，改 `DETAIL_PAGE_SIZE` 会让 fixture 跟着变，
 *   满页用例照样全绿 —— 页大小这个**契约**（桌面只取 50 条，超出的看不到）
 *   就没有任何东西守着了。这条断的是值，不是名字。
 */
describe('TenantDashboardView：页大小契约', () => {
  it('DETAIL_PAGE_SIZE 必须是 50（派生出来的值本身要被钉住）', () => {
    expect(PAGE_SIZE).toBe(50)
  })
})

const { logCalls, ctrl, mockApi, routerPush } = vi.hoisted(() => {
  type LogRow = Record<string, unknown>
  let gen = 0
  const logCalls: Array<Record<string, unknown>> = []
  const ctrl = {
    defer: false,
    resolvers: [] as Array<() => void>,
    /** 满页：否则「短页即无更多」让尾部控件直接 exhausted，点不到。 */
    fullPage1: false,
    empty: false,
    fail: false,
    reset() {
      ctrl.defer = false
      ctrl.resolvers = []
      ctrl.fullPage1 = false
      ctrl.empty = false
      ctrl.fail = false
      // ★ `gen` 是闭包变量，不在 ctrl 上 —— 漏清它第 N 个用例就拿到 g15，
      //   而 'g1' 是 'g15' 的子串，断言会两边一起错却照样绿。
      gen = 0
    },
  }

  // i 可以大于 23，所以走 Date.UTC（模板拼串会造出 T051: 非法时间）
  const ts = (i: number): string => new Date(Date.UTC(2026, 9, 5, i % 24, 15, 30)).toISOString()

  /**
   * 三种状态，覆盖 `statusText` 的三支：
   * rate_limited / 成功 / 失败。另加一支 client_model 与 outbound_model 都为 null
   * ⇒ 模型列必须出破折号而不是 'null'。
   */
  const STATUSES = ['rate_limited', 'success', 'failure'] as const

  /**
   * ★ null 的位置必须是 i=2 / i=3，不是取模。
   *   默认 fixture 只有 3 行，而 `i % 4 === 0` 要到第 4 行、`i % 5 === 0` 要到
   *   第 5 行才命中 —— 那两条用例就会在**一行 null 都没有**的页面上断言
   *   「null 出破折号」，红的不是产品，是**这条断言什么都没验**（第一版就是这么红的：
   *   findAll(...)[3] 是 undefined）。改成落在默认页内的固定下标，两个分支才真的可达。
   */
  const mk = (i: number, generation: number): LogRow => {
    const status = STATUSES[(i - 1) % STATUSES.length]
    return {
      // 前缀带代数标记，截断后（8 位）仍能看到是哪一代
      request_id: `g${generation}-req-${String(i).padStart(4, '0')}-suffix`,
      ts: ts(i),
      client_model: i === 2 ? null : `client-model-${i}`,
      outbound_model: `outbound-model-${i}`,
      success: status === 'success',
      request_status: status,
      credits_charged: i === 3 ? null : 12345 * (i + 1),
    }
  }

  const SUMMARY = {
    days: 7,
    tenant_id: 'default',
    total_requests: 4321,
    total_credits: 987654,
    by_model: [
      { model: 'zeta-model', requests: 300, credits: 9000 },
      { model: 'alpha-model', requests: 100, credits: 1000 },
      { model: 'mid-model', requests: 200, credits: 5000 },
    ],
    trend: [
      { date: '2026-10-01', credits: 100, requests: 10 },
      { date: '2026-10-02', credits: 200, requests: 20 },
    ],
  }

  const WALLET = {
    total_available: 5000,
    quota_remaining: 4000,
    granted_balance: 1000,
    purchased_balance: 3000,
    subscription: {
      plan_name: 'Pro',
      period_start: '2026-10-01T00:00:00Z',
      period_end: '2026-11-01T00:00:00Z',
    },
  }

  return {
    logCalls,
    ctrl,
    routerPush: vi.fn(),
    mockApi: () => ({
      getMaasUsageSummary: vi.fn(async () => SUMMARY),
      getMaasWallet: vi.fn(async () => WALLET),
      getRequestLogs: vi.fn(async (params: Record<string, unknown> = {}) => {
        logCalls.push(params)
        const page = Number(params.page ?? 1)
        const generation = ++gen
        if (ctrl.fail) throw new Error('logs boom')
        let items: LogRow[]
        if (ctrl.empty) items = []
        else if (ctrl.fullPage1) {
          // ★ 必须按**请求里的 page_size** 出行，不能按源码里的 PAGE_SIZE 常量。
          //   否则「连续加载的 pageSize 改成 25、桌面还是 50」这种分页错位在 fixture
          //   层面根本看不出来（后端是按 page_size 给行的，fixture 不认就是自己骗自己）。
          const want = Number(params.page_size ?? PAGE_SIZE)
          items =
            page === 1
              ? Array.from({ length: want }, (_, k) => mk(k + 1, generation))
              : [mk(want + 1, generation), mk(want + 2, generation)]
        } else {
          // ★ 每一页都返回 3 行（不按 page 变化）：跨页去重另有满页 fixture 覆盖。
          //   第一版写成 `page === 1 ? [...] : []`，于是「点下一页」之后表格整张消失
          //   （空态分支接管）—— 红的不是产品，是 fixture。
          items = [mk(1, generation), mk(2, generation), mk(3, generation)]
        }
        if (ctrl.defer) await new Promise<void>((res) => ctrl.resolvers.push(res))
        return { items, count: ctrl.empty ? 0 : 137 }
      }),
    }),
  }
})

vi.mock('../api', () => mockApi())
vi.mock('../store', () => ({ getCurrentTenantId: () => 'default' }))
vi.mock('../utils/openRequestDetailPage', () => ({ openRequestDetailPage: vi.fn() }))
type RouterTo = string | { path?: string; query?: Record<string, unknown> }

/**
 * ★ `RouterLink` 必须**真的渲染 href**。第一版 mock 成 `<a><slot /></a>` 空壳，
 *   于是「并是指向请求日志的链接」这半句承诺从未被验过 —— `attributes('href')`
 *   是 undefined，断「query 还在不在」更是一片空白（表现为断了自己的手）。
 */
vi.mock('vue-router', () => ({
  useRouter: () => ({ push: routerPush }),
  RouterLink: {
    props: ['to'],
    computed: {
      // `this` 必须显式标注：`props: ['to']` 不带类型，vue-tsc 会把它推成 `{ href(): string }`
      href(this: { to: RouterTo | undefined }): string {
        const t = this.to
        if (typeof t === 'string') return t
        const q = t?.query
          ? Object.entries(t.query)
              .map(([k, v]) => `${k}=${encodeURIComponent(String(v))}`)
              .join('&')
          : ''
        return q ? `${t?.path ?? ''}?${q}` : (t?.path ?? '')
      },
    },
    template: '<a :href="href"><slot /></a>',
  },
}))
// 实时流是另一个功能（SSE + acquire/release），本切片不测它 —— 整个 stub 掉，
// 否则它自己的连接/定时器会让「隔离绿 / 全量红」这类判断彻底不可信。
vi.mock('../components/LiveRequestStreamV2.vue', () => ({ default: { template: '<div class="live-stub" />' } }))

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: {
    'zh-CN': {
      common: { button: { retry: '重试' } },
      requests: { list: { filter: { resultRateLimited: '限流' } } },
      hyper: {
        dataView: { table: '表格', cards: '卡片' },
        list: {
          empty: '暂无记录', allLoaded: '已全部加载 {count} 条', loadFailed: '加载失败，点击重试',
          retry: '重试', loadMore: '继续加载', loadingMore: '加载中…', refreshing: '正在刷新…',
        },
      },
      dashboard: {
        tabs: { liveStream: '实时流', sessionStats: '会话统计' },
        stat: { successRate: '成功率', avgLatency: '平均延迟', models: '模型数', activeInDays: '近 {days} 天 {n} 个' },
      },
      tenants: {
        dashboard: {
          title: '租户看板', tenantLabel: '租户 {id}', refresh: '刷新', loadFailed: '加载失败',
          detailLoadFailed: '明细加载失败',
          range: { today: '今天', last7d: '近 7 天', last30d: '近 30 天' },
          subscriptionTitle: '订阅', goPricing: '查看定价', goPricingLink: '去定价 ',
          labelPlan: '套餐', labelPeriod: '周期', labelQuotaRemaining: '剩余额度', labelExpiresAt: '到期',
          creditsUnit: '积分', noSubscription: '暂无订阅。', noSubscriptionHint: '查看定价了解套餐。',
          statCreditsConsumed: '积分消耗', statCreditsConsumedSub: '近 {n} 天',
          statRequests: '请求次数', statAvailable: '可用额度', statAvailableSub: '订阅 {a} · 信用 {b} · 充值 {c}',
          recentDaysSub: '近 {n} 天',
          chartModelTitle: '模型请求排行', chartModelHint: '点击柱子查看明细', chartModelEmpty: '暂无', chartModelUnit: '次',
          chartTrendTitle: '使用趋势', chartTrendHint: '点击查看', chartTrendEmpty: '暂无',
          chartTrendCredits: '积分消耗', chartTrendRequests: '请求次数',
          chartTrendCreditsTip: '{date}: {n}', chartTrendRequestsTip: '{date}: {n}',
          tableModelUsage: '各模型用量', tableModelUsageHint: '说明',
          tableColModel: '模型', tableColRequests: '请求次数', tableColCredits: '消耗积分',
          emptyTable: '暂无数据',
          detailTitleModel: '模型「{model}」请求明细', detailTitleDay: '{day} 请求明细',
          detailLoading: '加载明细…', detailEmpty: '该筛选条件下暂无请求记录',
          detailColTime: '时间', detailColModel: '模型', detailColStatus: '状态',
          detailColCredits: '积分', detailColRequestId: '请求 ID',
          statusOk: '成功', statusFail: '失败',
          detailFooterLogs: '查看全部请求日志 →', detailFooterUsage: '我的消耗 →',
          onboarding: '暂无调用数据。前往', onboardingModels: '标准模型', onboardingModelsHint: ' 配置。',
          onboardingKeys: '密钥', onboardingKeysHint: ' 添加。',
          degradedFallback: '数据视图 {view} 尚未初始化，请先执行数据聚合迁移',
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
  const w = mount(TenantDashboardView, { global: { plugins: [i18n] } })
  await flushPromises()
  await flushPromises()
  return w
}

/** 打开明细下钻（点第一个模型柱），并等明细落地。 */
async function openDetail(w: Awaited<ReturnType<typeof factory>>) {
  await w.findAll('.bar-row')[0].trigger('click')
  await flushPromises()
  await flushPromises()
}

/** ★ 必须限定在 `[data-testid="card-list"]` 里取卡片 —— 本页模板里有
 *  `class="card chart-card"` / `class="card detail-card"` 的外壳，裸 `.card` 会选中它们。 */
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
  logCalls.length = 0
  routerPush.mockClear()
  ctrl.reset()
  mockWindowClass('expanded')
  localStorage.clear()
})

afterEach(() => vi.clearAllMocks())

describe('TenantDashboardView：桌面明细表零回归', () => {
  it('5 列表头，文案与顺序不变', async () => {
    const w = await factory()
    await openDetail(w)
    const ths = w.findAll('.detail-table thead th')
    expect(ths).toHaveLength(5)
    expect(ths.map((th) => th.text())).toEqual(['时间', '模型', '状态', '积分', '请求 ID'])
  })

  it('行内：时间本地化、无裸 ISO；模型列 client_model 优先于 outbound_model', async () => {
    const w = await factory()
    await openDetail(w)
    const row = w.find('.detail-table tbody tr')
    const time = row.findAll('td')[0].text()
    expect(time).not.toMatch(/\d{4}-\d{2}-\d{2}T/)
    expect(time).toMatch(/\d{1,2}[/:]\d{2}/)
    // mk(1) 的 i%4 !== 0 ⇒ client_model 有值，应压过 outbound_model
    expect(row.findAll('td')[1].text()).toBe('client-model-1')
  })

  it('模型列在 client_model 为 null 时回落到 outbound_model', async () => {
    const w = await factory()
    await openDetail(w)
    // 第 2 行 i=2 ⇒ client_model=null（见 mk 的注释：null 必须落在默认 3 行页内）
    const cell = w.findAll('.detail-table tbody tr')[1].findAll('td')[1]
    expect(cell.text()).toBe('outbound-model-2')
  })

  it('状态徽章三支都有译文（rate_limited / 成功 / 失败）', async () => {
    const w = await factory()
    await openDetail(w)
    const badges = w.findAll('.detail-table tbody .badge')
    expect(badges).toHaveLength(3)
    expect(badges[0].text()).toBe('限流')
    expect(badges[1].text()).toBe('成功')
    expect(badges[2].text()).toBe('失败')
  })

  it('状态徽章配色三支：rate_limited 琥珀、成功绿、失败红', async () => {
    const w = await factory()
    await openDetail(w)
    const classes = w.findAll('.detail-table tbody .badge').map((b) => b.classes().join(' '))
    expect(classes[0]).toContain('badge-amber')
    expect(classes[1]).toContain('badge-green')
    expect(classes[2]).toContain('badge-red')
  })

  /**
   * ★ 反向约束：不能只断「第 3 格是破折号」，还要断**只有**它破折号。
   *   第一版是 `expect(cells[0]).not.toBe('0')` 打在一行有值的数字上 —— 恒真，
   *   「null 出破折号」这条承诺一行都没验（默认页三条全是数字，见 mk 的注释）。
   */
  it('积分走千分位；null 出破折号（不是 0，也不是空）', async () => {
    const w = await factory()
    await openDetail(w)
    const cells = w.findAll('.detail-table tbody tr').map((tr) => tr.findAll('td')[3].text())
    expect(cells[0]).toMatch(/\d{1,3}(,\d{3})+/)
    expect(cells[1]).toMatch(/\d{1,3}(,\d{3})+/)
    // mk(3) 的 credits_charged=null（i===3）
    expect(cells[2]).toBe('—')
    expect(cells[2]).not.toBe('0')
    // 只有一行破折号：多出破折号说明 null 判定被放宽成「空串也算无值」
    expect(cells.filter((c) => c === '—')).toHaveLength(1)
  })

  it('请求 ID 截断到 8 位 + 省略号，并是指向请求日志的链接', async () => {
    const w = await factory()
    await openDetail(w)
    const link = w.find('.detail-table tbody tr a')
    // `slice(0, 8)` 是 8 个字符：'g1-req-0001-suffix' → 'g1-req-0'（第一版写成 7 个字符的
    // `g1-req`，红的是判据不是产品）。逐字断而不是断长度：长度断了就接不住前缀回归。
    expect(link.text()).toBe('g1-req-0…')
    expect(link.attributes('href')).toBe('/request-logs?q=g1-req-0001-suffix')
  })

  it('桌面仍只取第 1 页（不加页码条、不改既有行为）', async () => {
    const w = await factory()
    await openDetail(w)
    expect(logCalls).toHaveLength(1)
    expect(logCalls[0].page).toBe(1)
    expect(logCalls[0].page_size).toBe(PAGE_SIZE)
    expect(logCalls[0].model).toBe('zeta-model')
    expect(w.find('.pagination').exists()).toBe(false)
  })

  it('点同一根柱子再点 = 收起，明细行清空', async () => {
    const w = await factory()
    await openDetail(w)
    expect(w.findAll('.detail-table tbody tr').length).toBe(3)
    await w.findAll('.bar-row')[0].trigger('click')
    await flushPromises()
    expect(w.findAll('.detail-table tbody tr')).toHaveLength(0)
  })

  it('桌面端不出卡片，也不出连续加载尾部', async () => {
    const w = await factory()
    await openDetail(w)
    expect(w.find('[data-testid="card-list"]').exists()).toBe(false)
    expect(w.find('.hyper-load-more').exists()).toBe(false)
  })

  it('降级横幅文案走 i18n（不再是硬编码中文模板串）', async () => {
    const w = await factory()
    // degraded=false ⇒ 横幅不出现；这里验的是源码里没有那段中文兜底
    expect(w.find('[data-testid="tenant-dashboard-degraded-hint"]').exists()).toBe(false)
    expect(codeOnly).toContain("t('tenants.dashboard.degradedFallback', { view })")
  })
})

describe('TenantDashboardView：compact 连续加载路径', () => {
  beforeEach(() => mockWindowClass('compact'))

  it('出卡片不出明细表；出连续加载尾部', async () => {
    const w = await factory()
    await openDetail(w)
    expect(w.find('[data-testid="card-list"]').exists()).toBe(true)
    expect(w.find('.detail-table').exists()).toBe(false)
    expect(w.find('.hyper-load-more').exists()).toBe(true)
  })

  it('卡头是截断后的请求 ID（与表格那一格同一套规则）', async () => {
    const w = await factory()
    await openDetail(w)
    const title = w.findAll('.card__title')[0].text()
    expect(title).toBe('g1-req-0…')
  })

  /**
   * ★ 表格与卡片共用 `modelCellText` / `creditsDisplay` 的**行为**证明。
   *   只断「各有一份实现」（跨层契约那组）断不出「它们渲染出同一个值」——
   *   一份被改成 `String(row[key])` 的话计数仍然各是 1。
   */
  it('null 分支在卡片侧与表格侧同值（回落模型 / 破折号积分）', async () => {
    const w = await factory()
    await openDetail(w)
    expect(fieldValue(w, 0, '模型')).toBe('client-model-1')
    expect(fieldValue(w, 1, '模型')).toBe('outbound-model-2')
    expect(fieldValue(w, 2, '积分')).toBe('—')
  })

  it('字段走 format：时间本地化、模型回落、状态译名、积分千分位', async () => {
    const w = await factory()
    await openDetail(w)
    expect(fieldValue(w, 0, '时间')).not.toMatch(/\d{4}-\d{2}-\d{2}T/)
    expect(fieldValue(w, 0, '模型')).toBe('client-model-1')
    expect(fieldValue(w, 0, '状态')).toBe('限流')
    expect(fieldValue(w, 0, '积分')).toMatch(/\d{1,3}(,\d{3})+/)
  })

  it('卡片里不出现 undefined / null 字面量', async () => {
    const w = await factory()
    await openDetail(w)
    for (const c of cardsOf(w)) {
      expect(c.text()).not.toContain('undefined')
      expect(c.text()).not.toContain('null')
    }
  })

  /**
   * ★ 本切片的核心价值：桌面路径只取第 1 页 50 条且**没有任何翻页入口**，
   *   compact 走连续加载 ⇒ 超过 50 条能看到后面。
   */
  it('>50 条能继续加载（桌面拿不到的后半段在 compact 拿得到）', async () => {
    ctrl.fullPage1 = true
    const w = await factory()
    await openDetail(w)
    expect(cardsOf(w)).toHaveLength(PAGE_SIZE)
    await w.find('.hyper-load-more__btn--manual').trigger('click')
    await flushPromises()
    await flushPromises()
    expect(cardsOf(w)).toHaveLength(PAGE_SIZE + 2)
  })

  /**
   * ★ 跨页去重的**行为**证明。满页 fixture 是必要的：否则「短页即无更多」
   *   让尾部控件直接 exhausted。第 2 页的 id 与第 1 页下标相同、主键不同，
   *   所以只有「按主键去重」的实现才拿得到 PAGE_SIZE + 2 张卡。
   */
  it('跨页不去重：第 2 页两行不被第 1 页同下标行吃掉', async () => {
    ctrl.fullPage1 = true
    const w = await factory()
    await openDetail(w)
    await w.find('.hyper-load-more__btn--manual').trigger('click')
    await flushPromises()
    await flushPromises()
    expect(cardsOf(w)).toHaveLength(PAGE_SIZE + 2)
  })

  /**
   * ★ revision 闸门的**行为**证明。换一根柱子 = 换筛选条件；
   *   没有显式 `invalidate()` 时会被单飞吞掉，新条件**永远发不出去**。
   */
  it('compact 下换下钻目标会作废在途的第 1 页：既发出新请求，旧结果也不落地', async () => {
    ctrl.defer = true
    const w = await factory()
    await w.findAll('.bar-row')[0].trigger('click')
    await flushPromises()
    expect(logCalls).toHaveLength(1)

    await w.findAll('.bar-row')[1].trigger('click')
    await flushPromises()
    expect(logCalls, '没有 invalidate 时第二次点柱会被单飞吞掉').toHaveLength(2)
    expect(logCalls[1].model).toBe('alpha-model')

    for (const r of ctrl.resolvers.splice(0)) r()
    await flushPromises()
    await flushPromises()
    const list = w.find('[data-testid="card-list"]').text()
    expect(list, '旧请求（gen 1）的结果不应落地').not.toMatch(/g1(?![\d])/)
    expect(list, '新请求（gen 2）的结果应落地').toMatch(/g2(?![\d])/)
  })

  it('失败时尾部控件转成可重试，且不出现 undefined', async () => {
    ctrl.fail = true
    const w = await factory()
    await w.findAll('.bar-row')[0].trigger('click')
    await flushPromises()
    expect(w.find('.hyper-load-more').attributes('data-state')).toBe('failed')
    expect(w.find('.hyper-load-more__btn').exists()).toBe(true)
    for (const c of cardsOf(w)) expect(c.text()).not.toContain('undefined')
  })

  it('empty 时出 EmptyState，而不是本页的 .empty 文案 div', async () => {
    ctrl.empty = true
    const w = await factory()
    await openDetail(w)
    expect(w.find('.app-empty-state').exists()).toBe(true)
    expect(w.find('.app-empty-state').text()).toContain('该筛选条件下暂无请求记录')
  })

  it('点卡片跳到该请求的请求日志（与表格那一格同一意图）', async () => {
    const w = await factory()
    await openDetail(w)
    await cardsOf(w)[0].find('.card__head').trigger('click')
    expect(routerPush).toHaveBeenCalledTimes(1)
    const arg = routerPush.mock.calls[0][0]
    expect(arg.path).toBe('/request-logs')
    expect(arg.query.q).toMatch(/^g1-req-0001/)
  })

  it('点趋势某天：筛选条件从 model 换成 from/to 区间', async () => {
    const w = await factory()
    await openDetail(w)
    logCalls.length = 0
    await w.findAll('.trend-col')[1].trigger('click')
    await flushPromises()
    expect(logCalls).toHaveLength(1)
    expect(logCalls[0].model).toBeUndefined()
    expect(logCalls[0].from).toBe('2026-10-02T00:00:00.000Z')
    expect(logCalls[0].to).toBe('2026-10-03T00:00:00.000Z')
  })

  /**
   * ★ 收起下钻时**两条路径**的行都要清。
   *   只清 `detailRows` 的话，compact 会把上一段明细留在屏幕上（它读的是
   *   `detailPages.rows`）—— 症状是「收起后卡片还在，且显示的是上一次的模型」。
   */
  it('收起下钻时 compact 的累积行也清掉（不留上一段明细）', async () => {
    const w = await factory()
    await openDetail(w)
    expect(cardsOf(w)).toHaveLength(3)
    await w.findAll('.bar-row')[0].trigger('click')
    await flushPromises()
    expect(cardsOf(w)).toHaveLength(0)
  })
})

describe('TenantDashboardView：跨层契约', () => {
  it('下钻筛选只有一个真源（两个入口共用 detailQuery）', () => {
    expect((codeOnly.match(/function detailQuery\(/g) ?? []).length).toBe(1)
    // 定义 1 + loadDetail 1 + fetchPage 1 = 3
    expect((codeOnly.match(/detailQuery\(\)/g) ?? []).length).toBeGreaterThanOrEqual(3)
  })

  /**
   * ★ 「调用了 detailQuery」不等于「筛选条件只有 detailQuery」。
   *   变异 M16 就是在展开后面**再补一个 model** —— 调用数仍然是 3，真源仍然只有一份
   *   声明，于是全绿。可它是**两份真源**：真源改了（换筛选维度）它不跟着改。
   *   ⇒ 这里断的是**形态**：两个取数处的参数对象只能由 `展开真源 + 两个分页键` 构成。
   */
  it('两个取数处的参数对象都只由 detailQuery 展开 + 分页参数构成（不得内联筛选键）', () => {
    const sites = codeOnly.match(/getRequestLogs\(\{[^}]*\}\)/g) ?? []
    expect(sites, `取数处应为 2 处（loadDetail / fetchPage），实际 ${sites.length}`).toHaveLength(2)
    for (const s of sites) {
      expect(s).toMatch(/^getRequestLogs\(\{ \.\.\.detailQuery\(\), page: [A-Za-z0-9_.]+, page_size: DETAIL_PAGE_SIZE \}\)$/)
    }
  })

  it('detailQuery 不含 page / page_size（按块定位，不用固定窗口）', () => {
    const start = codeOnly.indexOf('function detailQuery(')
    expect(start, '没找到 detailQuery —— 下面的断言会变成「查了个空串」').toBeGreaterThan(-1)
    const body = codeOnly.slice(start, codeOnly.indexOf('\n}', start))
    expect(body).not.toMatch(/\bpage\b/)
    expect(body).not.toMatch(/page_size/)
  })

  it('rowKey 用后端主键 request_id，不是数组下标', () => {
    expect(codeOnly).toContain('rowKey: (r) => r.request_id')
    expect(codeOnly).not.toMatch(/rowKey:\s*\(.*\)\s*=>\s*i\b/)
  })

  it('连续加载的 pageSize 与取数处的 page_size 同源（否则 hasMore 按错单位算）', () => {
    // ★ `createHyperPages` 的短页判据是 `result.rows.length < opts.pageSize`
    //   —— 它用的是**配置里那个数**，不是 fetchPage 发出去的那个数。两者一旦不一致：
    //   配置写 100 而请求发 50 ⇒ 第 1 页回来 50 行就被判成「短页」⇒ hasMore=false
    //   ⇒ 本切片要修的「超过 50 条看不到」在 compact 下又回来了，而且**静默**。
    expect(codeOnly).toMatch(/createHyperPages<RequestLogRow>\(\{\s*pageSize: DETAIL_PAGE_SIZE,/)
    expect((codeOnly.match(/pageSize: DETAIL_PAGE_SIZE/g) ?? []).length).toBe(1)
  })

  it('页码与连续加载各自独立，不共享同一个 ref', () => {
    expect(codeOnly).toContain('const detailRows = ref<RequestLogRow[]>([])')
    expect(codeOnly).toContain('const detailPages = createHyperPages<RequestLogRow>(')
    // `(?!=)` 是必需的：`detailBusy` 里写的是 `===`，不带负向先行会永远红
    expect(codeOnly).not.toMatch(/detailPages\.(rows|hasMore|state)\.value\s*=(?!=)/)
  })

  it('reloadDetail 统一分派两条路径，且 compact 下先作废再重取', () => {
    expect(codeOnly).toMatch(
      /async function reloadDetail\(\)[\s\S]{0,300}detailPages\.invalidate\(\)[\s\S]{0,80}detailPages\.loadFirst\(\)/,
    )
  })

  it('连续加载的 fetchPage 不吞异常（否则尾部控件永远停在「加载中」）', () => {
    const start = codeOnly.indexOf('fetchPage: async')
    expect(start, '没找到 fetchPage').toBeGreaterThan(-1)
    const body = codeOnly.slice(start, start + 500)
    expect(body).not.toMatch(/catch\s*\{/)
    expect(body).toContain('getRequestLogs')
  })

  it('容器只裁 compact 的三态（桌面那两个 .empty div 仍在本页）', () => {
    expect(codeOnly).toMatch(/:loading="isCompact\s*&&\s*detailBusy"/)
    expect(codeOnly).toMatch(/:empty="isCompact\s*&&\s*!detailBusy\s*&&\s*detailRowsForView\.length\s*===\s*0"/)
    expect(codeOnly).toMatch(/v-if="detailLoading\s*&&\s*!isCompact"/)
    expect(codeOnly).toMatch(/v-else-if="!isCompact\s*&&\s*!detailRows\.length"/)
  })

  it('HyperLoadMore 与页码条不同时出现；本页桌面无页码条', () => {
    expect(codeOnly).toMatch(/<HyperLoadMore[\s\S]{0,60}v-if="isCompact"/)
    expect(codeOnly).not.toMatch(/class="pagination"/)
  })

  it('table-min-width 显式传 0px（本页原本没有表级 min-width）', () => {
    // ★ 断「恰好一处」而不是 `toContain`：源码注释里也写着这串字，
    //   而剥注释后 `toContain` 只能防「整段没了」，防不住「多了一张表也这么写」。
    expect((codeOnly.match(/table-min-width="0px"/g) ?? []).length).toBe(1)
    // 属性数必须与容器数相等：新增第二张表却忘了传，属性数不变 ⇒ 门不会红
    const views = (codeOnly.match(/<ResponsiveDataView\b/g) ?? []).length
    expect(views).toBe(1)
    expect((codeOnly.match(/table-min-width=/g) ?? []).length).toBe(views)
  })

  it('卡头走 titleFormat（否则完整长串 request_id 会把卡片撑爆）', () => {
    expect(codeOnly).toContain(':title-format="detailCardTitle"')
    expect(codeOnly).toContain('title-key="request_id"')
    expect(codeOnly).toMatch(/function detailCardTitle\(row: Record<string, unknown>\): string/)
  })

  it('表格与卡片共用同一套字段政策（不写第二份）', () => {
    // 模型列 / 状态文本 / 徽章配色三处，表格与卡片都读同一份函数
    expect(codeOnly).toMatch(/<td><code>\{\{ modelCellText\(r\) \}\}<\/code><\/td>/)
    expect(codeOnly).toMatch(/\{\{ statusText\(r\) \}\}/)
    expect(codeOnly).toMatch(/:class="statusBadgeClass\(r\)"/)
    // 组合起来：这三处各只能有一份实现
    expect((codeOnly.match(/function modelCellText\(/g) ?? []).length).toBe(1)
    expect((codeOnly.match(/function statusText\(/g) ?? []).length).toBe(1)
    expect((codeOnly.match(/function statusBadgeClass\(/g) ?? []).length).toBe(1)
  })

  it('收起下钻走 clearDetailRows（两条路径同步清）', () => {
    expect((codeOnly.match(/function clearDetailRows\(/g) ?? []).length).toBe(1)
    expect(codeOnly).toMatch(/function clearDetailRows\(\)[\s\S]{0,220}detailPages\._reset\(\)/)
    // 两个入口 + load() 都走它，而不是各自写 detailRows.value = []
    expect((codeOnly.match(/clearDetailRows\(\)/g) ?? []).length).toBeGreaterThanOrEqual(4)
  })

  it('没有新增请求端点（仍走 api 层的 getRequestLogs / getMaasUsageSummary）', () => {
    for (const banned of ['fetch(', 'axios', 'XMLHttpRequest']) {
      expect(codeOnly, `不应直接发起请求，却出现了 ${banned}`).not.toContain(banned)
    }
    expect(codeOnly).toContain('getRequestLogs')
    expect(codeOnly).toContain('getMaasUsageSummary')
  })

  it('页面没有硬编码中文文案（全部走 i18n）', () => {
    const cjk = codeOnly.match(/[一-鿿]/g)
    expect(cjk, `模板里出现了硬编码中文：${cjk?.join('')}`).toBeNull()
  })
})
