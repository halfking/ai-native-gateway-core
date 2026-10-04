// MaaSUsageView.responsive.test.ts — H6 第十四条垂直切片的门禁。
//
// 本页的形状：
// 1. **没有分页 API**（`getMaasLedger(limit)` 一次取回整段，`limit` 是「取多少条」的
//    下拉而不是分页器，没有 offset）⇒ 只改呈现形态，不引入连续加载。
// 2. **空态在表内**：桌面是 `<table>` **恒渲染** + `<tr v-if="!loading && 空">`。
//    这是「表壳不能空」那条规则的反身 —— 不能像切片七那样在槽内加 `v-if`
//    （加了就把桌面空态行一起删掉），compact 的空态只能来自容器的 `:empty`，
//    且**必须带 `!loading` 前置**（否则加载中凭空多一个空态）。
// 3. **`entry_type` 只有 5 个取值** ⇒ 拿它当 `titleKey` 必然撞键。
//    `title-key="id"`（后端主键）+ `titleFormat` 出类型文本。
// 4. 桌面有**两处独立的颜色**（type 徽章按 `entry_type`、金额按 `amount` 符号），
//    卡片只保留后者 ⇒ 门禁要把桌面两处都钉住，让这个取舍是**有界且可见的**。
//
// 门禁清单：
// A. 桌面零回归：6 列表头、空态行 `colspan="6"`、type 徽章三色类、delta 符号与
//    `amount-neg`/`amount-pos`、ref 四分支、note 破折号回落、桌面出表不出卡
// B. 三态边界：加载中不出空态（桌面与 compact 各一条）
// C. compact 卡片：出卡不出表、卡头是类型文本、键唯一、5 个字段、tone 逐行、
//    ref 四分支、时间/变动/余额与桌面**逐字同源**
// D. 跨层契约：无连续加载、`:empty` 带 `isCompact && !loading &&` 前置、
//    `table-min-width="0px"`、不传 `:loading`、`title-key="id"`、`#empty` 透传口
import { flushPromises, enableAutoUnmount, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import MaaSUsageView from './MaaSUsageView.vue'
import { _resetForTests } from '../../composables/useWindowClass'
import { _resetDataViewModeForTests } from '../../composables/useDataViewMode'

enableAutoUnmount(afterEach)

const SOURCE = readFileSync(resolve(process.cwd(), 'src/views/tenant/MaaSUsageView.vue'), 'utf8')
/** 剥掉全部注释（块 / 模板 / 行；行注释正则避开 `://`）。 */
const CODE_ONLY = SOURCE
  .replace(/\/\*[\s\S]*?\*\//g, '')
  .replace(/<!--[\s\S]*?-->/g, '')
  .replace(/(^|[^:'"`\\])\/\/[^\n]*/g, '$1')

const { ctrl } = vi.hoisted(() => ({
  ctrl: {
    calls: [] as Array<{ fn: string; args: unknown[] }>,
    admin: false,
    empty: false,
    /** 手动控杆：true 时让 ledger 请求永不落地 ⇒ 停在 loading 态 */
    hang: false,
    fail: false,
    reset() {
      ctrl.calls = []
      ctrl.admin = false
      ctrl.empty = false
      ctrl.hang = false
      ctrl.fail = false
    },
  },
}))

/** type 标签表 —— 复制自 `api/maas.ts` 的真值（页面的 typeLabel 就是查它）。 */
const TYPE_LABELS: Record<string, string> = {
  consume: '消耗',
  topup: '充值',
  subscribe: '订阅',
  adjust: '调整',
  refund: '退款',
}

/**
 * 6 行刻意覆盖：
 * - **consume 出现两次**（id 1 / 6）⇒ 撞键真实可触发
 * - 4 种**已登记**的 `entry_type` + 1 种**未登记**的（`legacy_reconcile`）
 *   ⇒ `typeLabel` 的 `|| entryType` 回落分支真实可达（第一版全是已登记类型，
 *   那个变异 `|| ''` 是**行为等价**的，台账上一条无牙）
 * - `amount` 三种符号：负 / 正 / **恰好 0**（桌面两个 class 都不加，卡片 tone 为 undefined）
 * - `amount` 的 ±1 边界（`fmtCredits` 的 `n > 0` 判据就在 ±1 上翻转）
 * - `ref` 四分支：两个都有 / 只有 type / 只有 id / 都没有
 * - `note` 有空串（⇒ 桌面与卡片都出破折号）
 */
const LEDGER = [
  { id: 1, entry_type: 'consume', amount: -1200, balance_after: 8800, pool: 'a', ref_type: 'request', ref_id: 'req-001', note: 'chat 请求', created_at: '2026-10-06T01:00:00.000Z' },
  { id: 2, entry_type: 'topup', amount: 10000, balance_after: 10000, pool: 'a', ref_type: 'order', ref_id: 'ord-77', note: '', created_at: '2026-10-05T01:00:00.000Z' },
  { id: 3, entry_type: 'legacy_reconcile', amount: 0, balance_after: 10000, pool: null, ref_type: null, ref_id: null, note: '对账修正', created_at: '2026-10-04T01:00:00.000Z' },
  { id: 4, entry_type: 'refund', amount: -300, balance_after: 9700, pool: 'a', ref_type: null, ref_id: 'ref-9', note: '退款', created_at: '2026-10-03T01:00:00.000Z' },
  { id: 5, entry_type: 'subscribe', amount: -1, balance_after: 9699, pool: 'a', ref_type: 'plan', ref_id: null, note: '订阅月度', created_at: '2026-10-02T01:00:00.000Z' },
  { id: 6, entry_type: 'consume', amount: -1, balance_after: 9698, pool: 'a', ref_type: null, ref_id: null, note: '', created_at: '2026-10-01T01:00:00.000Z' },
]

const SUMMARY = {
  total_credits: 1501,
  total_cost_usd: 3.75,
  total_requests: 42,
  by_model: [],
  trend: [],
}

function ledgerPayload() {
  return { items: ctrl.empty ? [] : LEDGER }
}

vi.mock('../../api', () => ({
  MAAS_LEDGER_TYPE_LABELS: {
    consume: '消耗',
    topup: '充值',
    subscribe: '订阅',
    adjust: '调整',
    refund: '退款',
  },
  getMaasLedger: vi.fn(async (limit: number) => {
    ctrl.calls.push({ fn: 'getMaasLedger', args: [limit] })
    if (ctrl.fail) throw new Error('ledger boom')
    if (ctrl.hang) return new Promise(() => {})
    return ledgerPayload()
  }),
  getAdminMaasLedger: vi.fn(async (code: string, limit: number) => {
    ctrl.calls.push({ fn: 'getAdminMaasLedger', args: [code, limit] })
    if (ctrl.fail) throw new Error('ledger boom')
    if (ctrl.hang) return new Promise(() => {})
    return ledgerPayload()
  }),
  getMaasUsageSummary: vi.fn(async (days: number, n: number) => {
    ctrl.calls.push({ fn: 'getMaasUsageSummary', args: [days, n] })
    if (ctrl.hang) return new Promise(() => {})
    return SUMMARY
  }),
  getAdminMaasUsageSummary: vi.fn(async (code: string, days: number, n: number) => {
    ctrl.calls.push({ fn: 'getAdminMaasUsageSummary', args: [code, days, n] })
    if (ctrl.hang) return new Promise(() => {})
    return SUMMARY
  }),
}))

vi.mock('../../composables/useMaasTenantContext', async () => {
  const { computed } = await import('vue')
  return {
    useMaasTenantContext: () => ({
      tenantLabel: computed(() => (ctrl.admin ? '租户 acme' : '我的租户')),
      tenantCode: computed(() => (ctrl.admin ? 'acme' : 'me')),
      isAdminTenantView: computed(() => ctrl.admin),
      pageTitle: (s: string) => s,
      maasBackLink: () => null,
    }),
  }
})

/** locale 固定，否则 `toLocaleString` 的千分位/符号取决于宿主 ICU。 */
vi.mock('../../i18n', () => ({ localeRef: { value: 'zh-CN' } }))

/** 时间格式化固定成可断言的串：这样「表格与卡片同源」验的是**同一份函数**，
 *  而不是碰巧在两个地方写出同样的格式。 */
vi.mock('../../utils/datetime', () => ({
  formatDateTime: (v: string) => `DT:${v}`,
}))

vi.mock('vue-router', () => ({
  useRoute: () => ({ params: { owner: 'alice' } }),
  useRouter: () => ({ back: vi.fn(), push: vi.fn() }),
  RouterLink: { props: ['to'], template: '<a class="router-link-stub"><slot /></a>' },
}))

vi.mock('../../components/PageBackLink.vue', () => ({
  default: { props: ['to', 'label'], template: '<span class="page-back-stub" />' },
}))
vi.mock('../../components/ui/KxDateRangePicker.vue', () => ({
  default: { props: ['modelValue', 'presets', 'maxSpanDays'], template: '<div class="kxdaterangepicker-stub" />' },
}))
vi.mock('../../components/FeeCostCell.vue', () => ({
  default: { props: ['credits', 'costUsd', 'showCost', 'inline'], template: '<span class="fee-cell-stub">{{ credits }}</span>' },
}))

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  fallbackWarn: false,
  missingWarn: false,
  messages: {
    'zh-CN': {
      common: { back: '返回', unit: { credits: '积分', requests: '次请求' } },
      tenants: {
        usage: {
          title: '我的消耗',
          adminTitle: '消耗统计',
          refresh: '刷新',
          loading: '加载中…',
          loadFailed: '加载失败',
          days7: '近 7 天',
          days30: '近 30 天',
          ledgerLimit50: '流水 50 条',
          ledgerLimit100: '流水 100 条',
          ledgerLimit200: '流水 200 条',
          statCreditsConsumed: '积分消耗',
          statCreditsConsumedHint: '近 {days} 天 · {n} 次请求',
          trendTitle: '使用趋势',
          trendCredits: '积分消耗',
          trendRequests: '请求次数',
          emptyTrend: '近 {days} 天暂无消耗记录。',
          emptyTrendBuyCredits: '购买积分',
          emptyTrendAdminHint: '可在套餐页为该租户充值。',
          byModel: '按模型排行',
          byModelHint: '积分消耗',
          emptyByModel: '暂无模型消耗数据',
          ledgerTitle: '积分流水',
          colTime: '时间',
          colType: '类型',
          colDelta: '变动',
          colBalance: '余额',
          colRef: '关联',
          colNote: '备注',
          emptyLedger: '暂无流水记录。',
          goBuyCredits: '去购买积分',
        },
        maasUsageView: { ledgerTotalLabel: '流水消耗合计', ledgerTotalHint: '最近 {limit} 条里 {count} 笔消耗' },
      },
      hyper: { dataView: { table: '表格', cards: '卡片' }, list: { empty: '默认空态文案' } },
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
        matches,
        media: q,
        onchange: null,
        addEventListener: () => {},
        removeEventListener: () => {},
        addListener: () => {},
        removeListener: () => {},
        dispatchEvent: () => false,
      }
    },
  })
}

async function factory() {
  const w = mount(MaaSUsageView, { global: { plugins: [i18n] } })
  await flushPromises()
  await flushPromises()
  return w
}

function cardsOf(w: Awaited<ReturnType<typeof factory>>) {
  return w.findAll('[data-testid="card-list"] .card')
}

function fieldValue(card: Awaited<ReturnType<typeof cardsOf>>[number], label: string) {
  const hit = card.findAll('.card__field').find((f) => f.find('dt').text() === label)
  expect(
    hit,
    `卡片里找不到字段「${label}」，实际有：${card.findAll('.card__field').map((f) => f.find('dt').text()).join('/')}`,
  ).toBeTruthy()
  return hit!.find('dd')
}

/** 按 `id` 找卡（`:key` 取的就是它）。 */
function cardById(w: Awaited<ReturnType<typeof factory>>, id: number) {
  const all = cardsOf(w)
  const idx = all.findIndex((c, i) => c.attributes('data-title') === TYPE_LABELS[LEDGER[i]!.entry_type])
  void idx
  // `data-title` 是类型文本（会重复），改用标题+首个字段值的组合定位
  const hit = all.find((c) => fieldValue(c, '时间').text() === `DT:${LEDGER[id - 1]!.created_at}`)
  expect(hit, `找不到 id=${id} 对应的卡片`).toBeTruthy()
  return hit!
}

beforeEach(() => {
  _resetDataViewModeForTests()
  ctrl.reset()
  mockWindowClass('expanded')
  localStorage.clear()
})

afterEach(() => vi.clearAllMocks())

describe('MaaSUsageView：桌面账本表零回归', () => {
  it('6 列表头，文案与顺序不变', async () => {
    const w = await factory()
    expect(w.findAll('.table-card table thead th').map((th) => th.text())).toEqual([
      '时间',
      '类型',
      '变动',
      '余额',
      '关联',
      '备注',
    ])
  })

  it('行内：时间、type 徽章三色类、delta 符号与 amount 负正类、余额千分位', async () => {
    const w = await factory()
    const rows = w.findAll('.table-card table tbody tr')
    expect(rows).toHaveLength(6)
    expect(rows[0]!.findAll('td')[0]!.text()).toBe('DT:2026-10-06T01:00:00.000Z')
    expect(rows[0]!.find('.badge').text()).toBe('消耗')
    expect(rows[0]!.find('.badge').classes()).toContain('badge-red')
    // 负数不加 '+'，千分位带负号
    expect(rows[0]!.findAll('td')[2]!.text()).toBe('-1,200')
    expect(rows[0]!.findAll('td')[2]!.classes()).toContain('amount-neg')
    // 正数带 '+'
    expect(rows[1]!.findAll('td')[2]!.text()).toBe('+10,000')
    expect(rows[1]!.findAll('td')[2]!.classes()).toContain('amount-pos')
    expect(rows[1]!.find('.badge').classes()).toContain('badge-green')
    // ±1 边界：判据是 `n > 0`，-1 不加 '+'、1 加
    expect(rows[4]!.findAll('td')[2]!.text()).toBe('-1')
    expect(rows[0]!.findAll('td')[3]!.text()).toBe('8,800')
  })

  /**
   * ★ 金额**恰好为 0** 时桌面两个 class 都不加 —— 这是一个真实的第三态，
   *   把判据从 `n < 0` 改成 `n <= 0` 就会给它染上红色。
   */
  it('金额为 0：既无 amount-neg 也无 amount-pos（第三态）', async () => {
    const w = await factory()
    const cell = w.findAll('.table-card table tbody tr')[2]!.findAll('td')[2]!
    expect(cell.text()).toBe('0')
    expect(cell.classes()).not.toContain('amount-neg')
    expect(cell.classes()).not.toContain('amount-pos')
    expect(w.findAll('.table-card table tbody tr')[2]!.find('.badge').classes()).toContain('badge-blue')
  })

  it('type 徽章五类都映射到正确的颜色类', async () => {
    const w = await factory()
    const cls = w.findAll('.table-card table tbody tr').map((r) =>
      r.findAll('.badge').map((b) => b.classes().filter((c) => c.startsWith('badge-')).join('')).join(''),
    )
    expect(cls).toEqual(['badge-red', 'badge-green', 'badge-blue', 'badge-blue', 'badge-blue', 'badge-red'])
    // 标签文本取自 MAAS_LEDGER_TYPE_LABELS，不是裸 entry_type
    expect(w.findAll('.table-card table tbody tr').map((r) => r.find('.badge').text())).toEqual([
      '消耗',
      '充值',
      // ★ 未登记类型回落成 entry_type 原文（不是空串）
      'legacy_reconcile',
      '退款',
      '订阅',
      '消耗',
    ])
  })

  it('关联列四分支：两个都有 / 只有 type / 只有 id / 都没有⇒破折号', async () => {
    const w = await factory()
    const refCells = w.findAll('.table-card table tbody tr').map((r) => r.findAll('td')[4]!.text())
    expect(refCells[0]).toBe('requestreq-001') // 两个 span，文本直接相邻
    expect(refCells[1]).toBe('orderord-77')
    expect(refCells[2]).toBe('—') // 都没有
    expect(refCells[3]).toBe('ref-9') // 只有 id
    expect(refCells[4]).toBe('plan') // 只有 type
  })

  it('备注为空出破折号，非空出原文', async () => {
    const w = await factory()
    const notes = w.findAll('.table-card table tbody tr').map((r) => r.findAll('td')[5]!.text())
    expect(notes).toEqual(['chat 请求', '—', '对账修正', '退款', '订阅月度', '—'])
  })

  /** D7 的同族：空态行的 `colspan` 必须等于真实列数。 */
  it('空态行 colspan="6"，与 6 列表头一致（off-by-one 会红）', async () => {
    ctrl.empty = true
    const w = await factory()
    const emptyCell = w.find('.table-card table tbody tr td.empty')
    expect(emptyCell.exists()).toBe(true)
    expect(emptyCell.attributes('colspan')).toBe('6')
    expect(w.findAll('.table-card table thead th')).toHaveLength(6)
  })

  it('桌面出表不出卡，也不出现连续加载尾部', async () => {
    const w = await factory()
    expect(w.find('.table-card table').exists()).toBe(true)
    expect(w.find('[data-testid="card-list"]').exists()).toBe(false)
    expect(w.find('.hyper-load-more').exists()).toBe(false)
  })

  it('首屏取数走非管理员分支；管理员视角切到 admin 接口', async () => {
    const w = await factory()
    expect(ctrl.calls.map((c) => c.fn).sort()).toEqual(['getMaasLedger', 'getMaasUsageSummary'])
    expect(ctrl.calls.find((c) => c.fn === 'getMaasLedger')!.args).toEqual([50])
    void w

    // ★ 必须**切一次**下拉：默认值本来就是 50，把 `limit.value` 换成写死的 50
    //   在首屏是完全等价的（第一版只断首屏 ⇒ 那个变异无牙）。
    await w.find('.limit-select').setValue('200')
    await flushPromises()
    expect(ctrl.calls.filter((c) => c.fn === 'getMaasLedger').at(-1)!.args).toEqual([200])

    ctrl.calls = []
    ctrl.admin = true
    const w2 = await factory()
    expect(ctrl.calls.map((c) => c.fn).sort()).toEqual(['getAdminMaasLedger', 'getAdminMaasUsageSummary'])
    expect(ctrl.calls.find((c) => c.fn === 'getAdminMaasLedger')!.args).toEqual(['acme', 50])
  })
})

describe('MaaSUsageView：加载中不得显示空态', () => {
  /** `hang` 让请求永不落地 ⇒ 页面停在 `loading=true`。 */
  it('桌面加载中：表壳在、空态行不在', async () => {
    ctrl.empty = true
    ctrl.hang = true
    const w = mount(MaaSUsageView, { global: { plugins: [i18n] } })
    await flushPromises()
    expect(w.find('.table-card table').exists()).toBe(true)
    expect(w.find('.table-card table tbody tr td.empty').exists()).toBe(false)
  })

  /**
   * ★ compact 侧同一条：容器的 `:empty` 必须带 `!loading` 前置。
   *   少了它，compact 加载中会凭空多出一个「暂无流水记录」——
   *   那正是 13 §7「失败态/加载态不显示空态」要防的。
   */
  it('compact 加载中：不出空态（:empty 的 !loading 前置）', async () => {
    mockWindowClass('compact')
    ctrl.empty = true
    ctrl.hang = true
    const w = mount(MaaSUsageView, { global: { plugins: [i18n] } })
    await flushPromises()
    // 要断的是**空态文案**不出现。`card-list` 此时确实在（0 行 ⇒ 空 `<ul>`），
    // 那与桌面「表壳在、行不在」同形，不是缺陷；把它写成「不出 card-list」
    // 会逼实现加一个桌面没有的分支。
    expect(w.find('.app-empty-state').exists()).toBe(false)
    expect(w.text()).not.toContain('暂无流水记录')
  })
})

describe('MaaSUsageView：compact 卡片形态', () => {
  beforeEach(() => mockWindowClass('compact'))

  it('出卡片不出表；6 行对 6 卡', async () => {
    const w = await factory()
    expect(cardsOf(w)).toHaveLength(6)
    expect(w.find('.table-card table').exists()).toBe(false)
  })

  it('compact 下不渲染形态切换钮', async () => {
    const w = await factory()
    expect(w.find('.responsive-data-view__switch').exists()).toBe(false)
  })

  /**
   * 卡头是**类型文本**（`typeFormat`），而 `:key` 走的是 `id`。
   * 这两条必须分别断：卡头会重复（消耗 ×2），键不能重复。
   */
  it('卡头是类型文本且可重复；键是唯一的 id（不撞键）', async () => {
    const w = await factory()
    const titles = cardsOf(w).map((c) => c.attributes('data-title'))
    expect(titles).toEqual(['消耗', '充值', 'legacy_reconcile', '退款', '订阅', '消耗'])
    // 同一个 title 出现两次而卡片仍是 6 张 ⇒ 键不是 title
    expect(titles.filter((t) => t === '消耗')).toHaveLength(2)
  })

  it('卡头文本取自 typeLabel 而非裸 entry_type（未登记的类型回落原值）', async () => {
    const w = await factory()
    expect(w.text()).not.toContain('consume')
    expect(cardsOf(w)[0]!.attributes('data-title')).toBe(TYPE_LABELS.consume)
  })

  it('5 个字段，标签与桌面列名同源；不含「类型」（走卡头，不重复）', async () => {
    const w = await factory()
    expect(cardsOf(w)[0]!.findAll('.card__field').map((f) => f.find('dt').text())).toEqual([
      '时间',
      '变动',
      '余额',
      '关联',
      '备注',
    ])
  })

  /** 表格与卡片必须共用同一份格式化函数 —— 逐字比对，不靠「看着一样」。 */
  it('时间/变动/余额与桌面逐字同源', async () => {
    const w = await factory()
    expect(fieldValue(cardById(w, 1), '时间').text()).toBe('DT:2026-10-06T01:00:00.000Z')
    expect(fieldValue(cardById(w, 1), '变动').text()).toBe('-1,200')
    expect(fieldValue(cardById(w, 2), '变动').text()).toBe('+10,000')
    expect(fieldValue(cardById(w, 1), '余额').text()).toBe('8,800')
  })

  it('tone 逐行：负⇒danger、正⇒good、0⇒无 tone（与桌面 amount 符号同一判据）', async () => {
    const w = await factory()
    const toneOf = (id: number) => fieldValue(cardById(w, id), '变动').attributes('data-tone')
    expect(toneOf(1)).toBe('danger') // -1200
    expect(toneOf(2)).toBe('good') // +10000
    // 0 ⇒ `neutral`。注意不是 undefined：`fieldTone` 对 metric/badge 做 `t ?? 'neutral'`，
    // 而 CardList 的 CSS **只给 good/warn/danger 配色**，`neutral` 落回继承色
    // ⇒ 渲染结果与桌面「不加任何 class」一致。要断的是「不会被当成 good/danger」。
    expect(toneOf(3)).toBe('neutral')
    expect(['good', 'warn', 'danger']).not.toContain(toneOf(3))
    expect(toneOf(4)).toBe('danger') // -300
  })

  it('关联字段四分支（含两个都有时的拼接），备注空出破折号', async () => {
    const w = await factory()
    expect(fieldValue(cardById(w, 1), '关联').text()).toBe('request req-001')
    expect(fieldValue(cardById(w, 3), '关联').text()).toBe('—')
    expect(fieldValue(cardById(w, 4), '关联').text()).toBe('ref-9')
    expect(fieldValue(cardById(w, 5), '关联').text()).toBe('plan')
    expect(fieldValue(cardById(w, 2), '备注').text()).toBe('—')
    expect(fieldValue(cardById(w, 1), '备注').text()).toBe('chat 请求')
  })

  /** compact 空态不能把桌面那一格里的 CTA 降级掉（`#empty` 透传口）。 */
  it('compact 空态出 EmptyState，且保留「去购买积分」入口', async () => {
    ctrl.empty = true
    const w = await factory()
    const box = w.find('.table-card .app-empty-state')
    expect(box.exists()).toBe(true)
    expect(box.text()).toContain('暂无流水记录')
    expect(box.find('a.router-link-stub').exists()).toBe(true)
    expect(box.find('a.router-link-stub').text()).toBe('去购买积分')
    expect(w.find('[data-testid="card-list"]').exists()).toBe(false)
  })

  it('管理员视角的 compact 空态不出购买入口（与桌面一致）', async () => {
    ctrl.empty = true
    ctrl.admin = true
    const w = await factory()
    const box = w.find('.table-card .app-empty-state')
    expect(box.exists()).toBe(true)
    expect(box.find('a.router-link-stub').exists()).toBe(false)
  })
})

describe('MaaSUsageView：跨层契约', () => {
  it('无分页 ⇒ 不引入连续加载', () => {
    for (const token of ['createHyperPages', 'HyperLoadMore', 'hyper-load-more', 'offset']) {
      expect(CODE_ONLY, `页面里不该出现 ${token}`).not.toContain(token)
    }
  })

  it('`:empty` 带 isCompact 与 !loading 双前置（两档都只在该出现时出现）', () => {
    const ones = CODE_ONLY.match(/:empty="[^"]*"/g) ?? []
    expect(ones).toHaveLength(1)
    expect(ones[0]).toBe(':empty="isCompact && !loading && ledger.length === 0"')
  })

  it('table-min-width="0px"（本页 .table 靠 inline width:100%，无 min-width）', () => {
    expect(CODE_ONLY).toContain('table-min-width="0px"')
    expect(CODE_ONLY).not.toMatch(/table-min-width="(?!0px)/)
  })

  it('不传 :loading（账本卡在 .table-card 里，loading 另有页面级空态与按钮）', () => {
    expect(CODE_ONLY).not.toContain(':loading=')
  })

  /** titleKey 用后端主键 `id`；用 `entry_type` 必然撞键（consume 有 2 行）。 */
  it('title-key="id" 而非 entry_type', () => {
    expect(CODE_ONLY).toContain('title-key="id"')
    expect(CODE_ONLY).not.toContain('title-key="entry_type"')
  })

  it('用了 #empty 透传口（否则 compact 空态只剩一句话，CTA 被删）', () => {
    expect(SOURCE).toContain('<template #empty>')
  })

  it('0 新增 i18n 键：只引用 tenants.usage.* / tenants.maasUsageView.* / common.* / hyper.* 的既有键', () => {
    const used = new Set([...CODE_ONLY.matchAll(/t\('([^']+)'/g)].map((m) => m[1]))
    for (const k of used) {
      expect(
        /^(tenants\.usage\.|tenants\.maasUsageView\.|common\.|hyper\.)/.test(k),
        `出现了预期词条前缀之外的键：${k}`,
      ).toBe(true)
    }
  })
})
