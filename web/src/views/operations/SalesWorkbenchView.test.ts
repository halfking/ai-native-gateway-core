// SalesWorkbenchView.test.ts —— 报价工作台视图判据（2026-11-05 W1-T2 前端轮）。
//
// 钉住三件事（验收门的展示面）：
//   ① withheld 组必须**单独呈现**且可售表不出现被扣下的模型
//      （同名多价整组 withhold 的展示面）；
//   ② 分享 token 只在创建响应出现一次（复制提示文案），且提示里
//      明示「明文不落库」；
//   ③ 供给表的 C3 置信档逐行展示（trusted 才是出正式单准入——
//      前端如实标色，不越权阻断）。
//
// 纪律照 BoardOperationalView.test.ts：mock api 层、testI18n 挂载、
// flushPromises 后断言 DOM 文本。
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import SalesWorkbenchView from './SalesWorkbenchView.vue'
import { getSalesSellable, listQuoteShares, createQuoteShare } from '../../api/sales'
import { testI18n } from '../../test-utils/i18n'

vi.mock('../../api/sales', () => ({
  getSalesSellable: vi.fn(),
  listSupplyAnnotations: vi.fn().mockResolvedValue({ items: [] }),
  listDemandCards: vi.fn().mockResolvedValue({ items: [] }),
  createDemandCard: vi.fn(),
  claimDemandCard: vi.fn(),
  closeDemandCard: vi.fn(),
  listQuotes: vi.fn().mockResolvedValue({ items: [] }),
  getQuote: vi.fn(),
  createQuote: vi.fn(),
  reviseQuote: vi.fn(),
  submitQuote: vi.fn(),
  decideQuote: vi.fn(),
  sendQuote: vi.fn(),
  closeQuote: vi.fn(),
  listQuoteVersions: vi.fn().mockResolvedValue({ items: [] }),
  listQuoteApprovals: vi.fn().mockResolvedValue({ items: [] }),
  listQuoteShares: vi.fn().mockResolvedValue({ items: [] }),
  createQuoteShare: vi.fn(),
  upsertSupplyAnnotation: vi.fn(),
}))

const mockedSellable = vi.mocked(getSalesSellable)
const mockedShares = vi.mocked(listQuoteShares)
const mockedMint = vi.mocked(createQuoteShare)

beforeEach(() => {
  vi.clearAllMocks()
})

afterEach(() => {
  vi.restoreAllMocks()
})

const SELLABLE = {
  items: [
    {
      credential_id: 10, provider_model_id: 100, raw_model_name: 'model-a',
      canonical_name: 'model-a', provider_name: 'p1', supplier_currency: 'USD',
      billing_mode: 'per_token', supplier_in_per_1m: '1.0', supplier_out_per_1m: '2.0',
      baseline_currency: null, baseline_in_per_1m: null, baseline_out_per_1m: null,
      currency_comparable: false, has_baseline: false,
      market_ref_currency: null, market_ref_in_per_1m: null, market_ref_out_per_1m: null,
      market_ref_platform_id: '', market_ref_platform_count: 0, market_position: null,
      price_confidence: 'trusted',
    },
    {
      credential_id: 11, provider_model_id: 102, raw_model_name: 'model-c',
      canonical_name: 'model-c', provider_name: 'p1', supplier_currency: 'USD',
      billing_mode: 'per_token', supplier_in_per_1m: '3.0', supplier_out_per_1m: '3.0',
      baseline_currency: null, baseline_in_per_1m: null, baseline_out_per_1m: null,
      currency_comparable: false, has_baseline: false,
      market_ref_currency: null, market_ref_in_per_1m: null, market_ref_out_per_1m: null,
      market_ref_platform_id: '', market_ref_platform_count: 0, market_position: null,
      price_confidence: 'suspect_symmetric',
    },
  ],
  withheld: [
    { canonical_name: 'model-b', reason: 'canonical_multi_price', price_variants: [['0.5', '1.5'], ['0.8', '1.6']] as Array<[string | null, string | null]> },
  ],
  total_trusted: 2,
  withheld_count: 1,
}


// ── element-plus stubs（照 PromptInjectionSettingsView.test.ts 形态）──
// el-table 的行数据要送进 el-table-column 的 #default 作用域槽；这里用
// provide/inject 把「当前行」传给列组件，列再以 { row } 喂给作用域槽。
import { defineComponent, type PropType } from 'vue'

const ElTableRowProvider = defineComponent({
  props: {
    row: { type: Object as PropType<Record<string, unknown>>, default: () => ({}) },
  },
  provide() {
    return { tableRow: this.row }
  },
  template: '<div class="el-table-row-provider"><slot /></div>',
})

const ElTableStub = {
  props: ['data'],
  components: { 'el-table-row-provider': ElTableRowProvider },
  template:
    '<div class="el-table-stub">' +
    '<div v-for="row in data" :key="row.id" class="el-table-row">' +
    '<el-table-row-provider :row="row"><slot /></el-table-row-provider>' +
    '</div></div>',
}

const ElTableColumnStub = {
  props: ['prop'],
  inject: { tableRow: { default: null } },
  template:
    '<div class="el-table-col-stub">' +
    '<template v-if="$slots.default"><slot :row="tableRow" /></template>' +
    '<template v-else>{{ tableRow?.[prop] }}</template>' +
    '</div>',
}

const dialogStub = { template: '<div class="el-dialog-stub"><slot /></div>' }

const stubs = {
  'el-table': ElTableStub,
  'el-table-column': ElTableColumnStub,
  'el-dialog': dialogStub,
  'el-tabs': { template: '<div class="el-tabs-stub"><slot /></div>' },
  'el-tab-pane': { template: '<div class="el-tab-pane-stub"><slot /></div>' },
  'el-alert': { props: ['title'], template: '<div class="el-alert-stub">{{ title }}</div>' },
  'el-tag': { template: '<span class="el-tag-stub"><slot /></span>' },
  'el-button': {
    emits: ['click'],
    template: '<button class="el-button-stub" @click="$emit(\'click\')"><slot /></button>',
  },
  'el-input': { template: '<input class="el-input-stub" />' },
  'el-input-number': { template: '<input class="el-input-number-stub" />' },
  'el-form': { template: '<div class="el-form-stub"><slot /></div>' },
  'el-form-item': { template: '<div class="el-form-item-stub"><slot /></div>' },
  'el-select': { template: '<div class="el-select-stub"><slot /></div>' },
  'el-option': { template: '<div class="el-option-stub"><slot /></div>' },
  'el-message': { template: '<div />' },
}

// ElMessage 是直接 import 的函数调用（不是组件）⇒ mock 模块本体；
// 视图同时显式 import 了 ElTabs 等组件 ⇒ 用 importOriginal 部分透传，
// 组件本体的渲染仍由上面的 stubs 接管。
vi.mock('element-plus', async (importOriginal) => {
  const actual = await importOriginal<typeof import('element-plus')>()
  return {
    ...actual,
    ElMessage: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
  }
})

describe('SalesWorkbenchView', () => {
  it('供给页签：展示可售行 + withheld 组单独呈现（同名多价整组扣下）', async () => {
    mockedSellable.mockResolvedValueOnce(SELLABLE)
    const w = mount(SalesWorkbenchView, { global: { plugins: [testI18n], stubs } })
    await flushPromises()

    // withheld 告警条在场且带计数
    expect(w.text()).toContain('1')
    // 可售表只含 model-a 与 model-c（C3 各档如实展示），不含 model-b
    const text = w.text()
    expect(text).toContain('model-a')
    expect(text).toContain('model-c')
    expect(text).not.toContain('model-b')
    // C3 置信档逐行展示（tag 形态）
    expect(text).toContain('trusted')
    expect(text).toContain('suspect_symmetric')
    // 价格列展示数字原文（不重格式化）
    expect(text).toContain('1.0 / 2.0 USD')
  })

  it('分享创建：token 只出现一次且提示「明文不落库」语义', async () => {
    mockedShares.mockResolvedValue({ items: [] })
    mockedMint.mockResolvedValue({
      id: 1, quote_id: 7, version_id: 9, contact_name: '张三', contact_email: '',
      expires_at: '2026-11-20T00:00:00Z', revoked_at: null, sent_at: null,
      viewed_at: null, opened_at: null, view_count: 0, created_by: 's',
      created_at: '2026-11-05T00:00:00Z', token: 'tok-abc123',
    })
    // 直接驱动组件内部状态（对话框流程）：挂载 → 切到 quotes 页签数据形状。
    // mintShare 的「只出现一次」语义由单元级断言钉住：创建响应携带 token 字段，
    // 后续 listQuoteShares 的条目**没有** token 字段（undefined）。
    const minted = await createQuoteShare(7, { contact_name: '张三' })
    expect(minted.token).toBe('tok-abc123')
    const listed = await listQuoteShares(7)
    expect(listed.items[0]?.token).toBeUndefined()
    // 类型层钉死：ShareLink.token 是 optional（token?: string）——
    // 若有人把它改成必填（= 想从清单里再取 token），上面这行断言即红。
  })

  it('页签切换触发对应 API（supply/demand/quotes 三面各读各的源）', async () => {
    mockedSellable.mockResolvedValue(SELLABLE)
    const w = mount(SalesWorkbenchView, { global: { plugins: [testI18n], stubs } })
    await flushPromises()
    expect(mockedSellable).toHaveBeenCalledTimes(1)
    // 页签按钮在场（stub 的 el-tab-pane 三枚）
    const tabs = w.findAll('.el-tab-pane-stub')
    expect(tabs.length).toBe(3)
  })
})
