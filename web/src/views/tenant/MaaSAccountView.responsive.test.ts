// MaaSAccountView.responsive.test.ts — H6 第七条垂直切片的门禁。
//
// 本页与前六条的结构差别有两处，都写进门禁里：
//
// 1. **没有分页 API。** 两张表（recent_orders / recent_ledger）随
//    `getMaasAccount()` 一次取回 ⇒ 本切片只改**呈现形态**，不引入连续加载。
//    门禁用「页面里不存在任何连续加载控件」把这条钉住 —— 将来谁想顺手
//    加一个 `HyperLoadMore`，会发现**它翻不到第 2 页**。
// 2. **桌面空态时整张表都不渲染**（`v-if` 在 `<table>` 上）。把它提到容器外，
//    空态下容器仍会渲染一个带边框的空表壳 —— 那是桌面观感的静默变化。
//    ⇒ `#table` 槽内**必须**保留 `v-if`，并配一条反向约束。
//
// 门禁清单：
// 1. 桌面零回归：两张表的表头数与文案、7 列/6 列结构、金额 ¥ 两位小数、
//    积分千分位、状态徽章四支配色、时间本地化、台账正负号、null 出破折号
// 2. 桌面空态：表**不渲染**、本页 `.empty` 出文案、**没有空表壳**
// 3. compact：出卡片不出表、字段走 format、状态色**逐行**、仅待支付有去支付链接
// 4. 跨层契约：`:empty` 带 isCompact 前置、不传 `:loading`、`table-min-width="0px"`、
//    字段政策只有一份、`#actions` 直通口
import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest'
import MaaSAccountView from './MaaSAccountView.vue'
import { _resetForTests } from '../../composables/useWindowClass'
import { _resetDataViewModeForTests } from '../../composables/useDataViewMode'

const source = readFileSync(resolve(process.cwd(), 'src/views/tenant/MaaSAccountView.vue'), 'utf8')
const codeOnly = source
  .replace(/\/\*[\s\S]*?\*\//g, '')
  .replace(/<!--[\s\S]*?-->/g, '')
  .replace(/(^|[^:'"`\\])\/\/[^\n]*/g, '$1')

const { ctrl, mockApi, routerPush } = vi.hoisted(() => {
  const ctrl = {
    orders: [] as unknown[],
    ledger: [] as unknown[],
    emptyOrders: false,
    emptyLedger: false,
    fail: false,
    reset() {
      ctrl.orders = []
      ctrl.ledger = []
      ctrl.emptyOrders = false
      ctrl.emptyLedger = false
      ctrl.fail = false
    },
  }
  return { ctrl, routerPush: vi.fn(), mockApi: () => ({}) }
})

// 三种订单状态可同页共存 —— 用来验「状态色是逐行的」，不是整列表一个色。
const ORDERS = [
  {
    id: 1,
    order_no: 'ORD-0001',
    order_type: 'subscribe',
    status: 'pending',
    amount_cents: 9900,
    credits: 12000,
    created_at: '2026-10-05T01:15:30.000Z',
  },
  {
    id: 2,
    order_no: 'ORD-0002',
    order_type: 'topup',
    status: 'paid',
    amount_cents: 5000,
    credits: 6000,
    created_at: '2026-10-05T02:15:30.000Z',
  },
  {
    id: 3,
    order_no: 'ORD-0003',
    order_type: 'topup',
    status: 'cancelled',
    amount_cents: 100,
    credits: 0,
    created_at: '2026-10-05T03:15:30.000Z',
  },
]

const LEDGER = [
  {
    id: 11,
    entry_type: 'consume',
    amount: -1500,
    balance_after: 8500,
    pool: 'granted',
    note: '请求结算',
    created_at: '2026-10-05T04:15:30.000Z',
  },
  {
    id: 12,
    entry_type: 'topup',
    amount: 10000,
    balance_after: 18500,
    // ★ pool 为 null ⇒ 必须出破折号而不是 'null'
    pool: null,
    // ★ note 为空串 ⇒ 破折号
    note: '',
    created_at: '2026-10-05T05:15:30.000Z',
  },
]

vi.mock('../../api', () => {
  const payload = () => ({
    wallet: {
      total_available: 10000,
      quota_remaining: 4000,
      granted_balance: 3000,
      purchased_balance: 3000,
      subscription: null,
    },
    recent_ledger: ctrl.emptyLedger ? [] : LEDGER,
    recent_orders: ctrl.emptyOrders ? [] : ORDERS,
  })
  return {
    getMaasAccount: vi.fn(async () => {
      if (ctrl.fail) throw new Error('account boom')
      ctrl.orders.push(1)
      return payload()
    }),
    getAdminMaasAccount: vi.fn(async () => payload()),
    MAAS_LEDGER_TYPE_LABELS: { consume: '消耗', topup: '充值', subscribe: '订阅', adjust: '调整', refund: '退款' },
    MAAS_POOL_LABELS: { subscription_quota: '订阅额度', granted: '信用积分', purchased: '充值积分' },
    MAAS_ORDER_STATUS_LABELS: { pending: '待支付', paid: '已支付', cancelled: '已取消', expired: '已过期' },
  }
})

vi.mock('../../composables/useMaasTenantContext', () => ({
  useMaasTenantContext: () => ({
    tenantLabel: 'Acme',
    tenantCode: 'acme',
    isAdminTenantView: { value: false },
    pageTitle: (s: string) => s,
    maasBackLink: () => null,
  }),
}))

// RouterLink 必须**真出 href**（切片六同款教训：空壳 mock 会让「是链接」半句零覆盖）
vi.mock('vue-router', () => ({
  useRouter: () => ({ push: routerPush }),
  useRoute: () => ({ query: {} }),
  RouterLink: {
    props: ['to'],
    computed: {
      href(this: { to: string | { path?: string } }): string {
        return typeof this.to === 'string' ? this.to : (this.to?.path ?? '')
      },
    },
    template: '<a :href="href"><slot /></a>',
  },
}))

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: {
    'zh-CN': {
      common: { back: '返回' },
      tenants: {
        account: {
          title: '我的账户', adminTitle: '租户账户', refresh: '刷新', loading: '加载中…', loadFailed: '加载失败',
          buyCredits: '充值', recentOrders: '最近订单', recentLedger: '流水',
          emptyOrders: '暂无订单', emptyLedger: '暂无流水', emptyAccount: '暂无账户',
          orderNo: '订单号', orderType: '类型', orderAmount: '金额', orderCredits: '积分',
          orderStatus: '状态', orderTime: '时间', orderPayLink: '去支付',
          orderTypeSubscribe: '订阅', orderTypeTopup: '充值',
          ledgerTime: '时间', ledgerType: '类型', ledgerPool: '账户', ledgerDelta: '变动',
          ledgerBalance: '余额', ledgerNote: '备注', consumptionStats: '消耗统计', goBuy: '去购买',
          walletQuotaRemaining: '剩余订阅额度', walletGrantedBalance: '赠送积分',
          walletPurchasedBalance: '充值积分', walletTotalAvailable: '可用总额',
        },
      },
      hyper: { dataView: { table: '表格', cards: '卡片' } },
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
  const w = mount(MaaSAccountView, { global: { plugins: [i18n] } })
  await flushPromises()
  await flushPromises()
  return w
}

/** 两张表的容器序号：0 = 订单，1 = 流水。 */
function listsOf(w: Awaited<ReturnType<typeof factory>>) {
  return w.findAll('.responsive-data-view')
}

/** 必须限定在 `[data-testid="card-list"]` 里取卡片（页面里还有 `.wallet-card card`）。 */
function cardsIn(w: Awaited<ReturnType<typeof factory>>, which: 0 | 1) {
  return w.findAll('[data-testid="card-list"]')[which]!.findAll('.card')
}

function fieldValue(card: ReturnType<typeof cardsIn>[number], label: string) {
  const hit = card.findAll('.card__field').find((f) => f.find('dt').text() === label)
  expect(hit, `卡片里找不到字段「${label}」，实际有：${card.findAll('.card__field').map((f) => f.find('dt').text()).join('/')}`).toBeTruthy()
  return hit!.find('dd')
}

beforeEach(() => {
  _resetDataViewModeForTests()
  routerPush.mockClear()
  ctrl.reset()
  mockWindowClass('expanded')
  localStorage.clear()
})

afterEach(() => vi.clearAllMocks())

describe('MaaSAccountView：桌面两张表零回归', () => {
  it('订单表 7 个 th（末位是空 th，操作列），文案与顺序不变', async () => {
    const w = await factory()
    const ths = listsOf(w)[0]!.findAll('thead th')
    expect(ths).toHaveLength(7)
    expect(ths.map((th) => th.text())).toEqual(['订单号', '类型', '金额', '积分', '状态', '时间', ''])
  })

  it('流水表 6 个 th，文案与顺序不变', async () => {
    const w = await factory()
    const ths = listsOf(w)[1]!.findAll('thead th')
    expect(ths).toHaveLength(6)
    expect(ths.map((th) => th.text())).toEqual(['时间', '类型', '账户', '变动', '余额', '备注'])
  })

  it('订单行：金额 ¥ 两位小数、积分千分位、状态徽章四支、时间本地化无裸 ISO', async () => {
    const w = await factory()
    const rows = listsOf(w)[0]!.findAll('tbody tr')
    expect(rows).toHaveLength(3)
    expect(rows[0]!.findAll('td')[2]!.text()).toBe('¥99.00')
    expect(rows[0]!.findAll('td')[3]!.text()).toMatch(/\d{1,3}(,\d{3})/)
    expect(rows[0]!.findAll('td')[0]!.text()).toBe('ORD-0001')
    // 状态：待支付 / 已支付 / 已取消 —— 文案与配色两支都要
    const badges = w.findAll('.responsive-data-view .badge')
    expect(badges.map((b) => b.text())).toEqual(['待支付', '已支付', '已取消'])
    expect(badges[0]!.classes().join(' ')).toContain('badge-yellow')
    expect(badges[1]!.classes().join(' ')).toContain('badge-green')
    expect(badges[2]!.classes().join(' ')).toContain('badge-gray')
    const time = rows[0]!.findAll('td')[5]!.text()
    expect(time).not.toMatch(/\d{4}-\d{2}-\d{2}T/)
    expect(time).toMatch(/\d{1,2}[/:]\d{2}/)
  })

  it('流水行：正数带 + 号、负数不带、pool 为 null 与空 note 都出破折号', async () => {
    const w = await factory()
    const rows = listsOf(w)[1]!.findAll('tbody tr')
    expect(rows).toHaveLength(2)
    expect(rows[0]!.findAll('td')[3]!.text()).toBe('-1,500')
    expect(rows[1]!.findAll('td')[3]!.text()).toBe('+10,000')
    expect(rows[0]!.findAll('td')[2]!.text()).toBe('信用积分')
    expect(rows[1]!.findAll('td')[2]!.text()).toBe('—')
    expect(rows[1]!.findAll('td')[5]!.text()).toBe('—')
    // 反向约束：只有那两格是破折号，别的一律不是
    const all = rows.flatMap((tr) => tr.findAll('td').map((td) => td.text()))
    expect(all.filter((x) => x === '—')).toHaveLength(2)
  })

  it('仅待支付订单出「去支付」链接，其余两行没有', async () => {
    const w = await factory()
    const rows = listsOf(w)[0]!.findAll('tbody tr')
    expect(rows[0]!.find('a').attributes('href')).toBe('/tenant/orders/1')
    expect(rows[1]!.findAll('a')).toHaveLength(0)
    expect(rows[2]!.findAll('a')).toHaveLength(0)
  })

  it('桌面不出卡片，也不出连续加载尾部（本页没有分页 API）', async () => {
    const w = await factory()
    expect(w.find('[data-testid="card-list"]').exists()).toBe(false)
    expect(w.find('.hyper-load-more').exists()).toBe(false)
  })
})

describe('MaaSAccountView：桌面空态不得变成「空表壳」', () => {
  it('订单为空：表不渲染、出本页文案', async () => {
    ctrl.emptyOrders = true
    const w = await factory()
    expect(listsOf(w)[0]!.find('table').exists()).toBe(false)
    expect(w.text()).toContain('暂无订单')
  })

  it('流水为空：表不渲染、出本页文案', async () => {
    ctrl.emptyLedger = true
    const w = await factory()
    expect(listsOf(w)[1]!.find('table').exists()).toBe(false)
    expect(w.text()).toContain('暂无流水')
  })

  it('两张表都空：DOM 里一个 <table> 都不剩（空表壳 = 桌面观感静默变化）', async () => {
    ctrl.emptyOrders = true
    ctrl.emptyLedger = true
    const w = await factory()
    expect(w.findAll('table')).toHaveLength(0)
    // 钱包四张卡仍在（空的是这两张表，不是整页）
    expect(w.findAll('.wallet-card')).toHaveLength(4)
  })
})

describe('MaaSAccountView：compact 卡片形态', () => {
  beforeEach(() => mockWindowClass('compact'))

  it('出两张卡片列表、不出表格', async () => {
    const w = await factory()
    expect(w.findAll('[data-testid="card-list"]')).toHaveLength(2)
    expect(w.find('table').exists()).toBe(false)
  })

  it('不渲染视图切换钮（用户无从切到横滚表格）', async () => {
    const w = await factory()
    expect(w.find('.responsive-data-view__switch').exists()).toBe(false)
  })

  it('订单卡头是订单号；金额 / 积分 / 状态 / 时间走 format', async () => {
    const w = await factory()
    const cards = cardsIn(w, 0)
    expect(cards).toHaveLength(3)
    expect(cards[0]!.find('.card__title').text()).toBe('ORD-0001')
    expect(fieldValue(cards[0]!, '金额').text()).toBe('¥99.00')
    expect(fieldValue(cards[0]!, '积分').text()).toMatch(/\d{1,3}(,\d{3})/)
    expect(fieldValue(cards[0]!, '状态').text()).toBe('待支付')
    expect(fieldValue(cards[0]!, '时间').text()).not.toMatch(/\d{4}-\d{2}-\d{2}T/)
  })

  it('积分 0 也出 0（不是破折号）—— 0 积分是合法值，不是无值', async () => {
    const w = await factory()
    // `fmtCredits` 走 toLocaleString ⇒ 0 就是 '0'。两个小数是 `fmtPrice`（金额）的事，
    // 这里第一版写成 '0.00'，红的是判据不是产品。
    expect(fieldValue(cardsIn(w, 0)[2]!, '积分').text()).toBe('0')
  })

  /** ★ 状态色必须**逐行**：字段级常量会把整列表按第一行上色。 */
  it('状态色逐行求值（待支付 warn / 已支付 good / 已取消 neutral）', async () => {
    const w = await factory()
    const tones = cardsIn(w, 0).map((c) => fieldValue(c, '状态').attributes('data-tone'))
    expect(tones).toEqual(['warn', 'good', 'neutral'])
  })

  it('流水卡头是类型译名（不是机器码 entry_type），字段带正负号与破折号', async () => {
    const w = await factory()
    const cards = cardsIn(w, 1)
    expect(cards[0]!.find('.card__title').text()).toBe('消耗')
    expect(fieldValue(cards[0]!, '变动').text()).toBe('-1,500')
    expect(fieldValue(cards[1]!, '变动').text()).toBe('+10,000')
    expect(fieldValue(cards[1]!, '账户').text()).toBe('—')
    expect(fieldValue(cards[1]!, '备注').text()).toBe('—')
  })

  /**
   * ★ 补这条是因为变异 M14 抓到了缺口：流水的**时间**字段此前**无人断言**
   *   （订单卡断言了时间、流水卡没有）⇒ 把它退回 `String(v)` 全绿。
   *   与切片三那次「枚举与金额被断言、日期被漏」是同一个漏法。
   *   断**形态**不取值：`fmtTime` 取的是应用自己的 `localeRef`（默认 en-US），
   *   与测试 i18n 是两套，所以只断「不是裸 ISO」。
   */
  it('流水卡片的时间走 fmtTime（形态断言：不出现裸 ISO 串）', async () => {
    const w = await factory()
    for (const c of cardsIn(w, 1)) {
      const time = fieldValue(c, '时间').text()
      expect(time).not.toMatch(/\d{4}-\d{2}-\d{2}T/)
      expect(time, '不应退化成空或 undefined').not.toBe('—')
    }
  })

  /** 桌面那两个金额列是 `style="text-align:right"`；卡片侧靠 `align: 'end'` 保持同一惯例。 */
  it('变动额与余额在卡片里右对齐，时间列保持默认 start（与桌面 text-align:right 同一惯例）', async () => {
    const w = await factory()
    const card = cardsIn(w, 1)[0]!
    expect(fieldValue(card, '变动').attributes('data-align')).toBe('end')
    expect(fieldValue(card, '余额').attributes('data-align')).toBe('end')
    // `data-align` 恒有值（`f.align ?? 'start'`），第一版写成 toBeUndefined，红的是判据
    expect(fieldValue(card, '时间').attributes('data-align')).toBe('start')
  })

  it('仅待支付订单出「去支付」动作（与表格那一格同一意图）', async () => {
    const w = await factory()
    const cards = cardsIn(w, 0)
    expect(cards[0]!.find('.card__actions a').attributes('href')).toBe('/tenant/orders/1')
    expect(cards[1]!.findAll('.card__actions a')).toHaveLength(0)
    expect(cards[2]!.findAll('.card__actions a')).toHaveLength(0)
  })

  /**
   * ★ 无动作的卡片，动作容器**没有元素子节点** —— 这条保证容器高度为 0、
   *   不吃 `margin-top`（CardList 已把间距改挂 `.card__actions > *`）。
   *
   *   判据断的是**元素子节点数**，不是 `:empty`：空的插槽片段会在 DOM 里留下
   *   两个 `nodeValue === ""` 的 `#text` 节点（实测），`:empty` 原理上就不成立。
   */
  it('无动作卡片的动作容器没有元素子节点（否则底部白吃 10px 间距）', async () => {
    const w = await factory()
    for (const c of [cardsIn(w, 0)[1]!, cardsIn(w, 0)[2]!]) {
      const box = c.find('.card__actions')
      expect(box.exists()).toBe(true)
      expect(box.element.children.length, `实际元素子节点：${[...box.element.children].map((n) => n.nodeName).join(',') || '（无）'}`).toBe(0)
    }
  })

  it('空态出 EmptyState，而不是本页的 .empty 文案 div', async () => {
    ctrl.emptyOrders = true
    const w = await factory()
    const states = w.findAll('.app-empty-state')
    expect(states.length).toBeGreaterThan(0)
    expect(states[0]!.text()).toContain('暂无订单')
  })

  it('卡片里不出现 undefined / null 字面量', async () => {
    ctrl.emptyLedger = true
    const w = await factory()
    for (const list of w.findAll('[data-testid="card-list"]')) {
      for (const c of list.findAll('.card')) {
        expect(c.text()).not.toContain('undefined')
        expect(c.text()).not.toContain('null')
      }
    }
  })
})

describe('MaaSAccountView：跨层契约', () => {
  it('两个容器的 :empty 都带 isCompact 前置（桌面空态仍由本页出）', () => {
    const n = (codeOnly.match(/:empty="isCompact && account\.recent_\w+\.length === 0"/g) ?? []).length
    expect(n, `应恰好 2 处（订单 / 流水），实际 ${n}`).toBe(2)
    // 本页那两个 .empty div 必须带 !isCompact，否则 compact 下与 EmptyState 重复出现
    expect((codeOnly.match(/v-if="!isCompact && !account\.recent_\w+\.length"/g) ?? []).length).toBe(2)
  })

  it('#table 槽内的 v-if 必须留在 <table> 上（不能提到容器外）', () => {
    // 出现 2 次：两张表各一次
    expect((codeOnly.match(/<table v-if="account\.recent_\w+\.length" class="table">/g) ?? []).length).toBe(2)
  })

  it('不给容器传 :loading（两张表没有独立加载态，传了刷新时 compact 会闪转圈）', () => {
    expect(codeOnly).not.toMatch(/<ResponsiveDataView[\s\S]{0,400}?\s:loading=/)
  })

  it('本页不引入连续加载（没有分页 API，加了也翻不到第 2 页）', () => {
    expect(codeOnly).not.toContain('HyperLoadMore')
    expect(codeOnly).not.toContain('createHyperPages')
  })

  it('table-min-width 显式传 0px（本页 .table 是全局类，无表级 min-width）', () => {
    // 恰好两处，与 ResponsiveDataView 的个数相等（新增第三张表却忘了传，门会红）
    expect((codeOnly.match(/table-min-width="0px"/g) ?? []).length).toBe(2)
    const views = (codeOnly.match(/<ResponsiveDataView\b/g) ?? []).length
    expect(views).toBe(2)
    expect((codeOnly.match(/table-min-width=/g) ?? []).length).toBe(views)
  })

  it('表格与卡片共用同一套格式化函数（不写第二份）', () => {
    // 表格那一格仍是 `¥{{ fmtPrice(...) }}` / `{{ e.amount > 0 ? '+' : '' }}{{ fmtCredits(...) }}`
    expect(codeOnly).toContain('<td>¥{{ fmtPrice(o.amount_cents) }}</td>')
    expect(codeOnly).toContain("{{ e.amount > 0 ? '+' : '' }}{{ fmtCredits(e.amount) }}")
    // 卡片刻意走 orderAmountText / ledgerAmountText（模板里算不出一份真源）
    expect((codeOnly.match(/function orderAmountText\(/g) ?? []).length).toBe(1)
    expect((codeOnly.match(/function ledgerAmountText\(/g) ?? []).length).toBe(1)
    // 状态四支 class 与四支 tone 必须同源
    expect((codeOnly.match(/function orderStatusClass\(/g) ?? []).length).toBe(1)
    expect((codeOnly.match(/function orderStatusTone\(/g) ?? []).length).toBe(1)
  })

  it('台账卡头走 titleFormat（entry_type 是机器码，不能直接当标题）', () => {
    expect(codeOnly).toContain(':title-format="ledgerCardTitle"')
    expect(codeOnly).toContain('title-key="entry_type"')
  })

  it('页面没有硬编码中文文案（全部走 i18n）', () => {
    const cjk = codeOnly.match(/[一-鿿]/g)
    expect(cjk, `模板/脚本里出现了硬编码中文：${cjk?.join('')}`).toBeNull()
  })

  it('没有新增请求端点（仍走 api 层的 getMaasAccount）', () => {
    for (const banned of ['fetch(', 'axios', 'XMLHttpRequest']) {
      expect(codeOnly, `不应直接发起请求，却出现了 ${banned}`).not.toContain(banned)
    }
    expect(codeOnly).toContain('getMaasAccount')
  })
})
