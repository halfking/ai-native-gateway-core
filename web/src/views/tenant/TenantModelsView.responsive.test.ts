// TenantModelsView.responsive.test.ts — H6 第九条垂直切片的门禁。
//
// 本页的形状是**第三种三态归属**（切片四~八是「桌面三态在页面里、容器只裁 compact」）：
// 桌面空态是 `v-else-if="!filtered.length"`，整个 `.card.model-card`（连标题带）
// 都被撤掉 ⇒ 容器挂在 `v-else` 分支里，**无数据时根本不挂载**，
// 于是既不传 `:empty` 也不传 `:loading`，两档共用本页自己的 `.empty`。
// 门禁要钉住这个形态，否则「给容器补一个 :empty」会变成桌面观感的静默变化。
//
// 另外两处与前几条不同：
// 1. `.table` **本页自带 `min-width: 720px`** ⇒ `table-min-width` 传 720 而不是 0。
// 2. 有 `.table-wrap`（自带 overflow-x）⇒ 必须删，否则与容器嵌套出双滚动条。
//
// 门禁清单：
// 1. 桌面零回归：8 列表头与顺序、模型名/标识/家族三行、上下文三档（K / M / 未设置）、
//    多模态徽章两支 + 模态标签、计费模式、4 个积分列的千分位与缓存回落
// 2. 搜索与多模态筛选（前端过滤 + 计数文案）
// 3. 空态：整块撤掉、无 `<table>`、无空表壳；compact 同样走本页 `.empty`
// 4. compact：出卡片不出表、字段走 format、多模态 tone 逐行、缓存回落、副标题条件挂载
// 5. 跨层契约：`:empty`/`:loading` 都不传、`table-min-width="720px"`、`.table-wrap` 已删、
//    字段政策只有一份、容器只挂在 v-else 分支
import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest'
import TenantModelsView from './TenantModelsView.vue'
import { _resetForTests } from '../../composables/useWindowClass'
import { _resetDataViewModeForTests } from '../../composables/useDataViewMode'

const source = readFileSync(resolve(process.cwd(), 'src/views/tenant/TenantModelsView.vue'), 'utf8')
const codeOnly = source
  .replace(/\/\*[\s\S]*?\*\//g, '')
  .replace(/<!--[\s\S]*?-->/g, '')
  .replace(/(^|[^:'"`\\])\/\/[^\n]*/g, '$1')

const { ctrl, mockApi } = vi.hoisted(() => {
  const ctrl = {
    fail: false,
    reset() {
      ctrl.fail = false
    },
  }
  return { ctrl, mockApi: () => ({}) }
})

/**
 * 三行覆盖三档上下文（`1_000_000` / `128_000` / `0`）与多模态两支；
 * 第 4 行 `family_display_name` 缺 ⇒ 验证副标题走 `—`；
 * 后两行的缓存价 `undefined` ⇒ 必须回落到入/出价。
 */
const MODELS = [
  {
    canonical_name: 'gpt-4o',
    display_name: 'GPT-4o',
    family: 'gpt-4',
    family_display_name: 'GPT-4 家族',
    modality: 'multimodal',
    billing_mode: 'per_token',
    context_window: 1_000_000,
    credits_per_1m_in: 12345,
    credits_per_1m_out: 24680,
    credits_per_1m_cache_in: 617,
    credits_per_1m_cache_out: 1234,
  },
  {
    canonical_name: 'claude-3-5-sonnet',
    display_name: 'Claude 3.5 Sonnet',
    family: 'claude-3',
    family_display_name: null,
    modality: 'text',
    billing_mode: 'per_request',
    context_window: 128_000,
    credits_per_1m_in: 3000,
    credits_per_1m_out: 15000,
    credits_per_1m_cache_in: undefined,
    credits_per_1m_cache_out: undefined,
  },
  {
    // ★ 显示名故意排到最前（'AAA Tiny'）：这样「按 canonical_name 排序」与
    //   「按默认候选（先 display_name）排序」给出**不同顺序** ⇒ 排序键被换掉能被抓住。
    canonical_name: 'tiny-model',
    display_name: 'AAA Tiny',
    family: 'tiny',
    family_display_name: null,
    modality: 'text',
    billing_mode: 'per_token',
    context_window: 0,
    credits_per_1m_in: 10,
    credits_per_1m_out: 20,
    credits_per_1m_cache_in: 1,
    credits_per_1m_cache_out: 2,
  },
  // ★ 落在 K 档阈值（1000）**与「阈值被挪到 2000」之间**：1500 在两种阈值下
  //   分别出 '2K' 与 '1500'。缺它时那条变异是行为等价的（台账上是一条无牙）。
  { canonical_name: 'zeta-vision', display_name: 'Zeta Vision', family: 'zeta', family_display_name: null,
    modality: 'vision', billing_mode: 'per_token', context_window: 1_500,
    credits_per_1m_in: 100, credits_per_1m_out: 200, credits_per_1m_cache_in: 10, credits_per_1m_cache_out: 20 },
  // ★ 1.5M 那种「除不尽」的上下文：一位小数 vs 0 位小数（'1.5M' vs '2M'）
  { canonical_name: 'zeta-audio', display_name: 'Zeta Audio', family: 'zeta', family_display_name: null,
    modality: 'audio', billing_mode: 'per_token', context_window: 1_500_000,
    credits_per_1m_in: 100, credits_per_1m_out: 200, credits_per_1m_cache_in: 10, credits_per_1m_cache_out: 20 },
]

vi.mock('../../api', () => ({
  getMaasModels: vi.fn(async () => {
    if (ctrl.fail) throw new Error('models boom')
    return { items: MODELS }
  }),
}))

vi.mock('../../composables/useMaasTenantContext', () => ({
  useMaasTenantContext: () => ({
    tenantLabel: 'Acme',
    isAdminTenantView: { value: false },
    pageTitle: (s: string) => s,
    maasBackLink: () => null,
  }),
}))

vi.mock('../../components/PageBackLink.vue', () => ({ default: { template: '<a />' } }))

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: {
    'zh-CN': {
      common: { all: '全部', button: { clear: '清除', retry: '重试' } },
      models: { filter: { textSearchPlaceholder: '搜索' } },
      tenantModels: {
        page: {
          title: '模型目录', refresh: '刷新', loading: '加载中…', empty: '没有匹配的模型',
          loadFailed: '加载失败', desc: '说明', vendorSectionTitle: '可用模型', modelCount: '共 {n} 个',
          filterPlaceholder: '搜索模型',
        },
        columns: {
          model: '模型', contextWindow: '上下文', multimodal: '多模态', billingMode: '计费模式',
          inPrice: '输入价', outPrice: '输出价', cacheIn: '缓存入', cacheOut: '缓存出',
        },
        multimodal: { yes: '支持', no: '不支持' },
        billing: { per_token: '按 token', per_request: '按请求' },
        modalities: { multimodal: '多模态', text: '纯文本', vision: '视觉', audio: '音频' },
        context: { notSet: '未设置' },
        filterBar: { count: '{n} / {m}' },
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
  const w = mount(TenantModelsView, { global: { plugins: [i18n] } })
  await flushPromises()
  await flushPromises()
  return w
}

function cardsOf(w: Awaited<ReturnType<typeof factory>>) {
  return w.findAll('[data-testid="card-list"] .card')
}

function fieldValue(card: ReturnType<typeof cardsOf>[number], label: string) {
  const hit = card.findAll('.card__field').find((f) => f.find('dt').text() === label)
  expect(hit, `卡片里找不到字段「${label}」，实际有：${card.findAll('.card__field').map((f) => f.find('dt').text()).join('/')}`).toBeTruthy()
  return hit!.find('dd')
}

beforeEach(() => {
  _resetDataViewModeForTests()
  ctrl.reset()
  mockWindowClass('expanded')
  localStorage.clear()
})

afterEach(() => vi.clearAllMocks())

describe('TenantModelsView：桌面模型目录表零回归', () => {
  it('8 列表头，文案与顺序不变（末 4 列带 num-col）', async () => {
    const w = await factory()
    const ths = w.findAll('.table thead th')
    expect(ths).toHaveLength(8)
    expect(ths.map((th) => th.text())).toEqual(['模型', '上下文', '多模态', '计费模式', '输入价', '输出价', '缓存入', '缓存出'])
    expect(ths.slice(4).every((th) => th.classes().includes('num-col'))).toBe(true)
  })

  it('首列三行：显示名 / 标识 / 家族（有才出）', async () => {
    const w = await factory()
    const rows = w.findAll('.table tbody tr')
    expect(rows).toHaveLength(5)
    // ★ 渲染序不是 fixture 序：本页按 `canonical_name` 排序（2026-10-03 加的
    //   `sortByName`），所以 claude → gpt-4o → tiny。第一版照抄 fixture 序 ⇒ 9 条红。
    expect(rows.map((tr) => tr.find('.model-code').text())).toEqual(['claude-3-5-sonnet', 'gpt-4o', 'tiny-model', 'zeta-audio', 'zeta-vision'])
    expect(rows[1]!.find('.model-name').text()).toBe('GPT-4o')
    expect(rows[1]!.find('.model-code').text()).toBe('gpt-4o')
    expect(rows[1]!.find('.model-family').text()).toBe('GPT-4 家族')
    // 家族为 null ⇒ 那一行不渲染 `.model-family`（桌面原本就是 v-if）
    expect(rows[0]!.find('.model-family').exists()).toBe(false)
  })

  it('上下文三档：1M / 128K / 未设置', async () => {
    const w = await factory()
    const cells = w.findAll('.table tbody tr').map((tr) => tr.findAll('td')[1]!.text())
    // 末两格是**故意**踩在阈值上的：1.5M（除不尽 ⇒ 保留一位小数）
    // 与 1500（落在 1000~2000 区间，阈值被挪就从 '2K' 变 '1500'）
    expect(cells).toEqual(['128K', '1M', '未设置', '1.5M', '2K'])
  })

  it('多模态：徽章两支 + 模态标签；计费模式走译名', async () => {
    const w = await factory()
    const rows = w.findAll('.table tbody tr')
    const badges = w.findAll('.table tbody .badge')
    expect(badges[0]!.text()).toBe('不支持') // claude（text）
    expect(badges[0]!.classes().join(' ')).toContain('badge-no')
    expect(badges[1]!.text()).toBe('支持')   // gpt-4o（multimodal）
    expect(badges[1]!.classes().join(' ')).toContain('badge-yes')
    expect(rows[1]!.find('.modality-tag').text()).toBe('多模态')
    expect(rows[1]!.findAll('td')[3]!.text()).toBe('按 token')
    expect(rows[0]!.findAll('td')[3]!.text()).toBe('按请求')
  })

  it('4 个积分列走千分位；缓存价 undefined 时回落到入/出价', async () => {
    const w = await factory()
    const rows = w.findAll('.table tbody tr')
    const nums = (i: number) => rows[i]!.findAll('td').slice(4).map((td) => td.text())
    expect(nums(1)).toEqual(['12,345', '24,680', '617', '1,234'])
    // cache_in/cache_out 为 undefined ⇒ 第 1 行（claude）显示 3,000 / 15,000
    expect(nums(0)).toEqual(['3,000', '15,000', '3,000', '15,000'])
  })

  /**
   * ★ 排序是这一页的真实产品行为（2026-10-03 加的 `sortByName`，
   *   老板要「有名称的列表按名称排序」），且**排序键必须与首列一致**
   *   （显式传 `['canonical_name']`，不用默认候选 —— 默认会先按中文显示名排）。
   *   我第一版的期望照抄了 fixture 序（gpt-4o 在前）⇒ 9 条红。红的是判据不是产品。
   */
  it('列表按 canonical_name 排序（与首列同一键，不是后端返回序）', async () => {
    const w = await factory()
    const codes = w.findAll('.table tbody .model-code').map((c) => c.text())
    expect(codes).toEqual([...codes].sort())
    // fixture 序是 gpt-4o / claude / tiny，渲染序不是它
    expect(codes[0]).not.toBe('gpt-4o')
  })

  /**
   * ★ 补这条是因为变异 M02 无牙：往 `v-else` 分支里塞一个 `.empty` 全绿。
   *   那个分支**只在有行时**渲染，所以塞进去的空态块会在桌面**有数据时**也出现 ——
   *   而当时门禁只断「无数据时的空态文案」，没断「有数据时没有空态块」。
   *   ⇒ **反向约束**：有行时容器分支里一个 `.empty` 都不许有。
   */
  it('有数据时容器分支里不出现任何空态块（反向约束）', async () => {
    const w = await factory()
    const card = w.find('.model-card')
    expect(card.exists()).toBe(true)
    expect(card.findAll('.empty')).toHaveLength(0)
    expect(card.findAll('.app-empty-state')).toHaveLength(0)
  })

  it('桌面不出卡片，也不出现连续加载尾部', async () => {
    const w = await factory()
    expect(w.find('[data-testid="card-list"]').exists()).toBe(false)
    expect(w.find('.hyper-load-more').exists()).toBe(false)
  })
})

describe('TenantModelsView：前端搜索与筛选', () => {
  it('搜索命中 model 名 / 标识 / 家族 / 模态', async () => {
    const w = await factory()
    const input = w.find('input[type="search"]')
    await input.setValue('gpt-4o')
    await flushPromises()
    expect(w.findAll('.table tbody tr')).toHaveLength(1)
    await input.setValue('claude-3')
    await flushPromises()
    expect(w.findAll('.table tbody tr')).toHaveLength(1)
    expect(w.find('.table tbody .model-code').text()).toBe('claude-3-5-sonnet')
  })

  it('多模态筛选 yes/no 两支', async () => {
    const w = await factory()
    const sel = w.find('.filter-bar__select')
    await sel.setValue('yes')
    await flushPromises()
    // multimodal / vision / audio 三支都算「支持」—— 缺 vision / audio 时这条会变 1
    const yes = w.findAll('.table tbody .model-code').map((c) => c.text())
    expect(yes).toEqual(['gpt-4o', 'zeta-audio', 'zeta-vision'])
    await sel.setValue('no')
    await flushPromises()
    expect(w.findAll('.table tbody tr')).toHaveLength(2)
  })

  it('清空按钮同时复位搜索词与多模态筛选', async () => {
    const w = await factory()
    await w.find('input[type="search"]').setValue('gpt-4o')
    await w.find('.filter-bar__select').setValue('yes')
    await flushPromises()
    expect(w.findAll('.table tbody tr')).toHaveLength(1)
    const btn = w.find('.link-btn')
    expect(btn.exists(), '有筛选时清空按钮应出现').toBe(true)
    await btn.trigger('click')
    await flushPromises()
    expect((w.find('input[type="search"]').element as HTMLInputElement).value).toBe('')
    expect((w.find('.filter-bar__select').element as HTMLSelectElement).value).toBe('all')
    expect(w.findAll('.table tbody tr')).toHaveLength(5)
  })

  it('筛选无命中：整块撤掉（无 table、无卡片），出本页 empty', async () => {
    const w = await factory()
    await w.find('input[type="search"]').setValue('zzz-no-such-model')
    await flushPromises()
    expect(w.find('table').exists()).toBe(false)
    expect(w.find('[data-testid="card-list"]').exists()).toBe(false)
    expect(w.find('.empty.card').text()).toBe('没有匹配的模型')
  })
})

describe('TenantModelsView：compact 卡片形态', () => {
  beforeEach(() => mockWindowClass('compact'))

  it('出卡片不出表；不渲染视图切换钮', async () => {
    const w = await factory()
    expect(cardsOf(w)).toHaveLength(5)
    expect(w.find('.table').exists()).toBe(false)
    expect(w.find('.responsive-data-view__switch').exists()).toBe(false)
  })

  it('卡头是显示名；副标题是家族（无家族那两张出破折号）', async () => {
    const w = await factory()
    const cards = cardsOf(w)
    expect(cards[0]!.find('.card__title').text()).toBe('Claude 3.5 Sonnet')
    expect(cards[0]!.find('.card__subtitle').text()).toContain('—')
    expect(cards[1]!.find('.card__title').text()).toBe('GPT-4o')
    expect(cards[1]!.find('.card__subtitle').text()).toContain('GPT-4 家族')
  })

  it('八个字段走 format：标识 / 上下文三档 / 缓存回落 / 积分千分位', async () => {
    const w = await factory()
    const cards = cardsOf(w)
    expect(cards[0]!.findAll('.card__field')).toHaveLength(8)
    expect(fieldValue(cards[1]!, '模型').text()).toBe('gpt-4o')
    expect(fieldValue(cards[1]!, '上下文').text()).toBe('1M')
    expect(fieldValue(cards[0]!, '上下文').text()).toBe('128K')
    expect(fieldValue(cards[2]!, '上下文').text()).toBe('未设置')
    expect(fieldValue(cards[1]!, '输入价').text()).toBe('12,345')
    // ★ 缓存价在源数据里是 undefined，卡片必须回落到入/出价而不是 0
    expect(fieldValue(cards[0]!, '缓存入').text()).toBe('3,000')
    expect(fieldValue(cards[0]!, '缓存出').text()).toBe('15,000')
  })

  it('多模态字段把「是/否 + 模态标签」合成一段，tone 逐行', async () => {
    const w = await factory()
    const cards = cardsOf(w)
    expect(fieldValue(cards[1]!, '多模态').text()).toBe('支持 · 多模态')
    expect(fieldValue(cards[1]!, '多模态').attributes('data-tone')).toBe('good')
    expect(fieldValue(cards[0]!, '多模态').text()).toBe('不支持')
    expect(fieldValue(cards[0]!, '多模态').attributes('data-tone')).toBe('neutral')
    expect(fieldValue(cards[1]!, '计费模式').text()).toBe('按 token')
  })

  it('筛选在卡片侧同样生效', async () => {
    const w = await factory()
    await w.find('.filter-bar__select').setValue('yes')
    await flushPromises()
    expect(cardsOf(w)).toHaveLength(3)
    await w.find('.filter-bar__select').setValue('no')
    await flushPromises()
    expect(cardsOf(w)).toHaveLength(2)
  })

  it('筛选无命中：两档都走本页的 .empty（容器不挂载）', async () => {
    const w = await factory()
    await w.find('input[type="search"]').setValue('zzz')
    await flushPromises()
    expect(cardsOf(w)).toHaveLength(0)
    expect(w.find('[data-testid="card-list"]').exists()).toBe(false)
    expect(w.find('.empty.card').text()).toBe('没有匹配的模型')
  })

  it('卡片里不出现 undefined / null 字面量', async () => {
    const w = await factory()
    for (const c of cardsOf(w)) {
      expect(c.text()).not.toContain('undefined')
      expect(c.text()).not.toContain('null')
    }
  })
})

describe('TenantModelsView：跨层契约', () => {
  it('第三种三态归属：容器挂在 v-else 分支（无数据时不挂载）', () => {
    // ★ 按块定位到该 div 的收尾，不用固定字符窗口：窗口不够长就把门建在函数外
    //   （切片五踩过这个坑）。这里实测窗口需要 241 字符。
    const branch = codeOnly.slice(
      codeOnly.indexOf('<div v-else class="card model-card">'),
      codeOnly.indexOf('\n    </div>', codeOnly.indexOf('<div v-else class="card model-card">')),
    )
    expect(branch, '没找到 v-else 分支 —— 下面的断言会变成查空串').toContain('<ResponsiveDataView')
    expect(branch).toContain('class="table"')
    // 桌面空态那条 .empty 仍在，且在 v-else **之前**
    expect(codeOnly).toContain('v-else-if="!filtered.length" class="empty card"')
    const emptyAt = codeOnly.indexOf('v-else-if="!filtered.length"')
    const viewAt = codeOnly.indexOf('<ResponsiveDataView')
    expect(emptyAt, '空态分支必须排在容器之前（v-else 链的顺序）').toBeGreaterThan(-1)
    expect(emptyAt).toBeLessThan(viewAt)
  })

  it('不传 :empty 也不传 :loading（容器不挂载，传了是死代码）', () => {
    expect(codeOnly).not.toMatch(/<ResponsiveDataView[\s\S]{0,400}?\s:empty=/)
    expect(codeOnly).not.toMatch(/<ResponsiveDataView[\s\S]{0,400}?\s:loading=/)
  })

  it('table-min-width 传 720px（.table 本页自带 min-width: 720px，传 0 会改小）', () => {
    expect(codeOnly).toContain('table-min-width="720px"')
    // 本页 .table 的 min-width 必须还在（不是 0 也不是被删）
    expect(codeOnly).toMatch(/\.table\s*\{[^}]*min-width:\s*720px;/)
  })

  it('.table-wrap 已删（容器自带 overflow-x，嵌套会出双滚动条）', () => {
    expect(codeOnly).not.toContain('class="table-wrap"')
  })

  it('副标题键按「确有家族」条件挂载（否则整页一行破折号）', () => {
    expect(codeOnly).toContain(':subtitle-keys="familyKeys"')
    // ★ `computed<string[]>(() =>` 在 `>` 之后是 **`((`**：外层是 computed 的调用括号，
    //   内层才是箭头函数的空参列表。第一版写成 `\(` `\)` 少了一个括号 ⇒ 永远不匹配。
    expect(codeOnly).toMatch(/const familyKeys = computed<string\[\]>\(\(\) =>\s*filtered\.value\.some\(\(m\) => m\.family_display_name\)/)
  })

  it('本页不引入连续加载（getMaasModels 一次取回整段）', () => {
    expect(codeOnly).not.toContain('HyperLoadMore')
    expect(codeOnly).not.toContain('createHyperPages')
  })

  it('表格与卡片共用同一套格式化函数（不写第二份）', () => {
    expect(codeOnly).toContain('<td>{{ fmtContext(m.context_window) }}</td>')
    expect(codeOnly).toContain('<td class="num">{{ fmtCredits(m.credits_per_1m_in) }}</td>')
    expect((codeOnly.match(/function fmtContext\(/g) ?? []).length).toBe(1)
    expect((codeOnly.match(/function fmtCredits\(/g) ?? []).length).toBe(1)
    expect((codeOnly.match(/function supportsMultimodal\(/g) ?? []).length).toBe(1)
    expect((codeOnly.match(/function modalityLabel\(/g) ?? []).length).toBe(1)
  })

  it('页面没有硬编码中文文案（全部走 i18n）', () => {
    const cjk = codeOnly.match(/[一-鿿]/g)
    expect(cjk, `出现了硬编码中文：${cjk?.join('')}`).toBeNull()
  })

  it('没有新增请求端点（仍走 api 层的 getMaasModels）', () => {
    for (const banned of ['fetch(', 'axios', 'XMLHttpRequest']) {
      expect(codeOnly, `不应直接发起请求，却出现了 ${banned}`).not.toContain(banned)
    }
    expect(codeOnly).toContain('getMaasModels')
  })
})
