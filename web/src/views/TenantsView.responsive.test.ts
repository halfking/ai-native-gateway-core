// TenantsView.responsive.test.ts — H6 第一条垂直切片的门禁。
//
// 存在理由：门禁证明的是「状态机正确」，不是「用户在手机上能看到它工作」。
// 本文件是**第一个让 compact 卡片形态挂在真实业务页上的证据**。
//
// 三条不可省的断言：
// 1. **桌面表格结构未变**（桌面零回归红线）—— 9 列表头与行内元素逐字核对。
// 2. **两种形态读同一份 `tenants` 数组** —— 切形态不重新打接口。
// 3. **卡片字段走 `format` 钩子** —— 否则千分位/本地化日期/状态译名全丢。
import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import TenantsView from './TenantsView.vue'
import CardList from '../components/ui/CardList.vue'
import { _resetForTests } from '../composables/useWindowClass'
import { _resetDataViewModeForTests } from '../composables/useDataViewMode'

const source = readFileSync(resolve(process.cwd(), 'src/views/TenantsView.vue'), 'utf8')
const codeOnly = source
  .replace(/\/\*[\s\S]*?\*\//g, '')
  .replace(/<!--[\s\S]*?-->/g, '')
  .replace(/(^|[^:'"`\\])\/\/[^\n]*/g, '$1')

const TENANTS = [
  { code: 'acme', name: 'Acme 集团', status: 'active', user_count: 1234, api_key_count: 56, total_requests: 987654, contact_email: 'ops@acme.example', created_at: '2026-01-02T03:04:05Z' },
  { code: 'beta', name: 'Beta 科技', status: 'suspended', user_count: 7, api_key_count: 0, total_requests: 42, contact_email: null, created_at: '' },
]

// 只 mock 一次 api 模块。mock 两次会互相覆盖，行为取决于加载顺序 ——
// 那是「测试自己骗自己」的经典形态。
/** 可变返回值：空态用例需要把列表换成空数组。 */
let apiRows: unknown[] = TENANTS

vi.mock('../api', () => ({
  getTenantsAdmin: vi.fn(async () => apiRows),
  TENANT_STATUSES: ['active', 'suspended'],
  TENANT_STATUS_COLORS: { active: 'badge-green', suspended: 'badge-yellow' },
}))

vi.mock('../store', () => ({ isPlatformOpsView: () => true }))
vi.mock('../utils/sortByName', () => ({ sortByName: (rows: unknown[]) => rows }))
vi.mock('../utils/datetime', () => ({ formatDateTime: (s: string) => (s ? `DT(${s})` : '-') }))
vi.mock('../composables/useTenantStatusLabel', () => ({
  useTenantStatusLabel: () => ({ tenantStatusLabel: (s: string) => `STATUS(${s})` }),
}))
vi.mock('./TenantCreateDialog.vue', () => ({ default: { template: '<div />' } }))
vi.mock('../components/FeeCostCell.vue', () => ({ default: { template: '<span class="fee" />' } }))

const MESSAGES = {
  'zh-CN': {
    tenants: {
      list: {
        title: '租户', createBtn: '新建租户', statusLabel: '状态', allStatuses: '全部',
        colName: '名称', colCode: '编码', colStatus: '状态', colUsers: '用户数',
        colKeys: '密钥数', colCost7d: '7 天费用', colRequests: '请求数',
        colContact: '联系邮箱', colCreated: '创建时间',
        loading: '加载中…', empty: '暂无租户', loadFailed: '加载失败',
      },
    },
    hyper: { dataView: { table: '表格', cards: '卡片' }, list: { empty: '暂无记录' } },
  },
}
const i18n = createI18n({ legacy: false, locale: 'zh-CN', messages: MESSAGES })

function mockWindowClass(cls: 'compact' | 'expanded'): void {
  _resetForTests()
  const lo = cls === 'compact' ? 0 : 1280
  Object.defineProperty(window, 'matchMedia', {
    writable: true,
    configurable: true,
    value: (query: string) => {
      const min = query.match(/min-width:\s*([\d.]+)px/)
      const max = query.match(/max-width:\s*([\d.]+)px/)
      const matches = min ? lo >= parseFloat(min[1]) : max ? lo <= parseFloat(max[1]) : false
      return { matches, media: query, onchange: null, addEventListener: () => {}, removeEventListener: () => {}, addListener: () => {}, removeListener: () => {}, dispatchEvent: () => false }
    },
  })
}

const router = { push: vi.fn() }
vi.mock('vue-router', () => ({ useRouter: () => router }))

/**
 * `load()` 是 onMounted 里的异步调用，mount 返回时 `loading` 仍为 true，
 * 表格/卡片都还没渲染。**必须 flushPromises**，否则断言全部落在空 DOM 上。
 */
async function factory() {
  const w = mount(TenantsView, { global: { plugins: [i18n] } })
  await flushPromises()
  return w
}

beforeEach(() => {
  apiRows = TENANTS
  _resetDataViewModeForTests()
  mockWindowClass('expanded')
  router.push.mockClear()
})

afterEach(() => vi.clearAllMocks())

describe('TenantsView：桌面表格零回归', () => {
  it('渲染 9 列表头，顺序与列名不变', async () => {
    const w = await factory()
    const ths = w.findAll('table thead th')
    expect(ths).toHaveLength(9)
    expect(ths.map((th) => th.text())).toEqual([
      '名称', '编码', '状态', '用户数', '密钥数', '7 天费用', '请求数', '联系邮箱', '创建时间',
    ])
  })

  it('行内仍是 name/code/状态徽章/三个数字/FeeCostCell/邮箱/时间', async () => {
    const w = await factory()
    const row = w.find('table tbody tr.tenant-row')
    expect(row.exists()).toBe(true)
    expect(row.find('strong').text()).toBe('Acme 集团')
    expect(row.find('code').text()).toBe('acme')
    expect(row.find('.badge').text()).toBe('STATUS(active)')
    expect(row.find('.fee').exists()).toBe(true)
    // 数字仍走 toLocaleString（1234 → "1,234"）
    expect(row.text()).toContain('1,234')
    expect(row.text()).toContain('987,654')
  })

  it('行仍可点且仍是键盘可达（tabindex + enter）', async () => {
    const w = await factory()
    const row = w.find('table tbody tr.tenant-row')
    expect(row.attributes('tabindex')).toBe('0')
    await row.trigger('click')
    expect(router.push).toHaveBeenCalledWith('/tenants/acme')
  })

  it('表格仍被包在横向滚动容器里，且 min-width 为 760px', async () => {
    const w = await factory()
    const wrap = w.find('.responsive-data-view__table')
    expect(wrap.exists()).toBe(true)
    const root = w.find('.responsive-data-view')
    expect((root.attributes('style') ?? '').replace(/\s/g, '')).toContain('--rdv-table-min-width:760px')
  })

  it('桌面端渲染表格，不渲染卡片', async () => {
    const w = await factory()
    expect(w.find('table').exists()).toBe(true)
    expect(w.find('[data-testid="card-list"]').exists()).toBe(false)
  })

  it('桌面空态也走 EmptyState（不再有表格内的空行）', async () => {
    apiRows = []
    const w = await factory()
    expect(w.find('.app-empty-state').exists()).toBe(true)
    expect(w.find('table tbody tr').exists()).toBe(false)
  })
})

describe('TenantsView：compact 卡片形态', () => {
  beforeEach(() => mockWindowClass('compact'))

  it('渲染卡片，且不渲染表格', async () => {
    const w = await factory()
    expect(w.find('[data-testid="card-list"]').exists()).toBe(true)
    expect(w.find('table').exists()).toBe(false)
  })

  it('compact 下不渲染视图切换钮（用户无从切到横滚表格）', async () => {
    const w = await factory()
    expect(w.find('.responsive-data-view__switch').exists()).toBe(false)
  })

  it('卡片主标题是租户名、副标题是 code', async () => {
    const w = await factory()
    const card = w.findAll('.card')[0]
    expect(card.find('.card__title').text()).toBe('Acme 集团')
    expect(card.find('.card__subtitle').text()).toContain('acme')
  })

  it('状态走 format 钩子出译名，而不是原始 status 值', async () => {
    const w = await factory()
    // 取「状态」那一格的值断言，而不是在整卡文本里找子串 ——
    // 译名 `STATUS(active)` 本身含 "active"，用 not.toContain 会永远失败。
    const cell = w
      .findAll('.card')[0]
      .findAll('.card__field')
      .find((f) => f.find('dt').text() === '状态')
    expect(cell).toBeDefined()
    expect(cell!.find('dd').text()).toBe('STATUS(active)')
  })

  it('数字走 format 出千分位', async () => {
    const w = await factory()
    const card = w.findAll('.card')[0]
    expect(card.text()).toContain('1,234')
    expect(card.text()).toContain('987,654')
  })

  it('日期走 format 出本地化结果', async () => {
    const w = await factory()
    expect(w.findAll('.card')[0].text()).toContain('DT(2026-01-02T03:04:05Z)')
  })

  it('空值字段显示为破折号而不是 "null"/"undefined"', async () => {
    const w = await factory()
    const second = w.findAll('.card')[1]
    expect(second.text()).toContain('—')
    expect(second.text()).not.toContain('null')
    expect(second.text()).not.toContain('undefined')
  })

  it('卡片头部可点，进入的是同一个详情路由', async () => {
    const w = await factory()
    // 点击绑在 .card__head 上（clickable 时是 <button>），不是整个 <li>
    const head = w.findAll('.card')[0].find('.card__head')
    expect(head.element.tagName).toBe('BUTTON')
    await head.trigger('click')
    expect(router.push).toHaveBeenCalledWith('/tenants/acme')
  })

  it('空态出共享 EmptyState，两种形态一致', async () => {
    apiRows = []
    const w = await factory()
    expect(w.find('.app-empty-state').exists()).toBe(true)
    expect(w.find('.app-empty-state').text()).toBe('暂无租户')
    expect(w.find('table').exists()).toBe(false)
    expect(w.find('[data-testid="card-list"]').exists()).toBe(false)
  })
})

/**
 * CardList 的通用契约。放在这里而不是新建 CardList.spec，是因为
 * T8/T9 两条变异暴露了覆盖缺口：`primaryField` 的格式化与
 * `text()` 的空值兜底在 TenantsView 的数据下**根本走不到**
 * —— 页面只用了 `fields` + `subtitleKeys`，且 subtitle 的 key 非空。
 */
describe('CardList：格式化钩子与空值兜底（通用契约）', () => {
  it('primaryField 也走 format —— 此前它只用 String(row[key])，金额会丢币种与千分位', () => {
    const w = mount(CardList, {
      props: {
        rows: [{ name: 'A', amount: 1234567.5 }],
        titleKey: 'name',
        primaryField: { key: 'amount', label: '费用', type: 'metric', align: 'end', format: (v) => `$${(v as number).toLocaleString()}` },
      },
    })
    expect(w.find('.card__primary-value').text()).toBe('$1,234,567.5')
  })

  it('primaryField 未给 format 时退回原始值', () => {
    const w = mount(CardList, {
      props: { rows: [{ name: 'A', amount: 42 }], titleKey: 'name', primaryField: { key: 'amount', label: '费用' } },
    })
    expect(w.find('.card__primary-value').text()).toBe('42')
  })

  it('subtitle/meta 的空值兜底成破折号，不显示 "null"', () => {
    const w = mount(CardList, {
      props: { rows: [{ name: 'A', code: null, tag: undefined }], titleKey: 'name', subtitleKeys: ['code'], metaKeys: ['tag'] },
    })
    const card = w.find('.card')
    expect(card.text()).toContain('—')
    expect(card.text()).not.toContain('null')
    expect(card.text()).not.toContain('undefined')
  })

  it('format 返回空串时按无值处理，不留一个空格子', () => {
    const w = mount(CardList, {
      props: { rows: [{ name: 'A', x: 1 }], titleKey: 'name', fields: [{ key: 'x', label: 'X', format: () => '' }] },
    })
    expect(w.find('.card__field dd').text()).toBe('—')
  })
})

describe('TenantsView：加载方式与静态约束', () => {
  it('两种形态读同一个数组，没有第二份数据源', async () => {
    expect(source).toMatch(/:rows="tenants"/)
    // 不允许出现第二个 fetch 租户的调用点
    // 2 次 = 1 次 import + 1 次调用。再多就是出现了第二个取数点。
    expect(codeOnly.match(/getTenantsAdmin/g) ?? []).toHaveLength(2)
  })

  it('切片没有引入新的请求端点（仍走既有 api 层）', async () => {
    for (const banned of ['fetch(', 'axios', 'XMLHttpRequest']) {
      expect(codeOnly, `TenantsView 不应直接发起请求，却出现了 ${banned}`).not.toContain(banned)
    }
  })

  it('没有硬编码中文文案（全部走 i18n）', async () => {
    const cjk = codeOnly.match(/[一-鿿]/g)
    expect(cjk, `模板里出现了硬编码中文：${cjk?.join('')}`).toBeNull()
  })

  it('min-width 只有一个真源（TABLE_MIN_WIDTH 常量）', async () => {
    expect(codeOnly).toContain("const TABLE_MIN_WIDTH = '760px'")
    expect(codeOnly).toContain(':table-min-width="TABLE_MIN_WIDTH"')
    // 不允许模板里再出现字面 760px
    expect(codeOnly.match(/760px/g) ?? []).toHaveLength(1)
  })

  it('min-width 的 CSS 规则确实存在（jsdom 无布局，规则效果不可观测）', () => {
    // 说明：jsdom 不做布局，所以「表格实际被撑到 760px」无法断言。
    // 只能断言规则**存在且消费了那个变量** —— 这是本仓既有的
    // 「窗口档 mock + 源码断言」模式（见 AppTopbar.responsive.test.ts）。
    const css = readFileSync(resolve(process.cwd(), 'src/components/ui/ResponsiveDataView.vue'), 'utf8')
    const rule = css.match(/\.responsive-data-view__table > :slotted\(table\)\s*\{[\s\S]*?\}/)
    expect(rule, 'ResponsiveDataView 缺少 slotted(table) 的 min-width 规则').not.toBeNull()
    expect(rule![0]).toContain('--rdv-table-min-width')
  })

  it('已不再直接依赖 DataTable（避免与容器形成双横向滚动条）', async () => {
    expect(codeOnly).not.toContain('<DataTable')
  })
})
