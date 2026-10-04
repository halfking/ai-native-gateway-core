// QualityCorrelationsView.responsive.test.ts — H6 第八条垂直切片的门禁。
//
// 本页的形状与切片七同族：
// 1. **没有分页 API**（`getQualityCorrelations({days, by})` 一次取回整段）
//    ⇒ 只改呈现形态，不引入连续加载。门禁用「不出现连续加载控件」钉住。
// 2. **桌面空态时整张表都不渲染** ⇒ `#table` 槽内必须保留 `v-if`，
//    否则空态下会多出一个带边框的空表壳。
// 3. `insights` 那一段是 `<ol>` 排名列表（带序号 + 相关系数配色），
//    **不是数据表**，本切片不套 CardList —— 门禁要确认它两档都还在。
//
// 门禁清单：
// 1. 桌面零回归：6 列表头与顺序、样本数千分位、百分比一位小数、延迟 ms/s 两支、
//    成本 4 位小数、桶 tag、成功率/质量两格的**内联色**与粗体
// 2. 桌面空态：表不渲染、出本页 `.empty`、DOM 里不剩 `<table>`
// 3. compact：出卡片不出表、字段走 format、**质量档位逐行**（tone 与阈值）
// 4. 跨层契约：`:empty` 带 isCompact 前置、不传 `:loading`、`table-min-width="0px"`、
//    字段政策只有一份、insights 排名列表两档都保留
import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest'
import QualityCorrelationsView from './QualityCorrelationsView.vue'
import { _resetForTests } from '../composables/useWindowClass'
import { _resetDataViewModeForTests } from '../composables/useDataViewMode'

const source = readFileSync(resolve(process.cwd(), 'src/views/QualityCorrelationsView.vue'), 'utf8')
const codeOnly = source
  .replace(/\/\*[\s\S]*?\*\//g, '')
  .replace(/<!--[\s\S]*?-->/g, '')
  .replace(/(^|[^:'"`\\])\/\/[^\n]*/g, '$1')

const { ctrl, mockApi } = vi.hoisted(() => {
  const ctrl = {
    calls: [] as Array<Record<string, unknown>>,
    empty: false,
    fail: false,
    reset() {
      ctrl.calls = []
      ctrl.empty = false
      ctrl.fail = false
    },
  }
  return { ctrl, mockApi: () => ({}) }
})

/**
 * ★ 三个质量档位**同页共存**，用来验「tone 逐行」而不是整列表一个色：
 * 0.92 / 0.55 / 0.10 分别落在 good / warn / danger。
 * 边界附近另有一条 0.70（应落 good）。
 */
const BREAKDOWN = [
  { bucket: '0-500', samples: 1200, success_rate: 0.92, avg_latency_ms: 850, avg_quality: 0.93, avg_cost_usd: 0.01234 },
  { bucket: '501-1k', samples: 800, success_rate: 0.55, avg_latency_ms: 1500, avg_quality: 0.5, avg_cost_usd: 0.05 },
  { bucket: '1k+', samples: 45, success_rate: 0.1, avg_latency_ms: 2400, avg_quality: 0.12, avg_cost_usd: 1.5 },
  { bucket: '边界', samples: 7, success_rate: 0.7, avg_latency_ms: 999, avg_quality: 0.4, avg_cost_usd: 0.0 },
  // ★ 落在 `ms < 1000` 与「变异把阈值挪到 1500」**两个阈值之间**的一行。
  //   缺它时变异 M10 是行为等价的（fixture 里没有任何值落在该区间）⇒ 台账上多一条
  //   「无牙」，而门其实并不知情。
  { bucket: '1.2s', samples: 33, success_rate: 0.8, avg_latency_ms: 1200, avg_quality: 0.8, avg_cost_usd: 0.02 },
]

const PAYLOAD = {
  generated_at: '2026-10-04T01:00:00.000Z',
  window_days: 7,
  breakdown: BREAKDOWN,
  insights: [
    { predictor: 'tool_calls', correlation: 0.82, interpretation: '工具调用越多，成功率越高', buckets: 4, samples: 2052 },
    // ★ -0.61 落在 abs∈[0.4,0.7) 档 ⇒ 负值取橙（#f97316），**不是**红；
    //   红色要 abs>=0.7。第一版把 -0.61 期望成红，红的是判据不是产品。
    { predictor: 'images', correlation: -0.61, interpretation: '带图请求更容易失败', buckets: 3, samples: 2010 },
    { predictor: 'code_blocks', correlation: -0.85, interpretation: '代码块越多越容易超时', buckets: 2, samples: 1250 },
  ],
}

vi.mock('../api', () => ({
  getQualityCorrelations: vi.fn(async (params: Record<string, unknown> = {}) => {
    ctrl.calls.push(params)
    if (ctrl.fail) throw new Error('qc boom')
    return { ...PAYLOAD, breakdown: ctrl.empty ? [] : PAYLOAD.breakdown }
  }),
}))

vi.mock('../components/ui/KxDateRangePicker.vue', () => ({
  default: { props: ['modelValue', 'presets', 'maxSpanDays'], template: '<div class="kxdaterangepicker-stub" />' },
}))

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: {
    'zh-CN': {
      common: { dateRange: { title: '时间范围', startDate: '开始', endDate: '结束' }, button: { apply: '应用' } },
      qualityCorrelations: {
        title: '质量相关性', subtitle: '按维度分桶看质量',
        filter: {
          window: '时间窗', bucketBy: '分桶维度', loading: '加载中…', refresh: '刷新',
          days: { d1: '1 天', d7: '7 天', d30: '30 天', d90: '90 天' },
          by: { prompt_length: '提示词长度', tools: '工具调用', images: '图片', code_block: '代码块' },
        },
        meta: { generated: '生成于', window: '窗口', windowDays: '{n} 天', totalSamples: '总样本', needSamples: '样本不足' },
        breakdown: {
          title: '按{by}分桶',
          headers: { bucket: '分桶', samples: '样本数', success: '成功率', latency: '延迟', quality: '质量', cost: '成本' },
          empty: '暂无分桶数据',
        },
        insights: {
          title: '相关性洞察', hint: '说明', buckets: '{n} 个分桶', samples: '{n} 个样本',
          emptyInsufficient: '样本不足，无法给出洞察', emptyUnexpected: '没有发现显著相关性',
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
  const w = mount(QualityCorrelationsView, { global: { plugins: [i18n] } })
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

describe('QualityCorrelationsView：桌面 breakdown 表零回归', () => {
  it('6 列表头，文案与顺序不变', async () => {
    const w = await factory()
    const ths = w.findAll('.qc-table thead th')
    expect(ths).toHaveLength(6)
    expect(ths.map((th) => th.text())).toEqual(['分桶', '样本数', '成功率', '延迟', '质量', '成本'])
  })

  it('行内：桶 tag、样本数千分位、百分比一位小数、延迟两支、成本 4 位小数', async () => {
    const w = await factory()
    const rows = w.findAll('.qc-table tbody tr')
    expect(rows).toHaveLength(5)
    expect(rows[0]!.find('.tag-bucket').text()).toBe('0-500')
    expect(rows[0]!.findAll('td')[1]!.text()).toBe('1,200')
    expect(rows[0]!.findAll('td')[2]!.text()).toBe('92.0%')
    // 850ms 走 ms 支，1500ms 走 s 支（2 位小数）
    expect(rows[0]!.findAll('td')[3]!.text()).toBe('850ms')
    // 1.2s 落在 1000~1500 区间：阈值一旦被挪，这格就会从 '1.20s' 变成 '1200ms'
    expect(rows[4]!.findAll('td')[3]!.text()).toBe('1.20s')
    expect(rows[1]!.findAll('td')[3]!.text()).toBe('1.50s')
    expect(rows[0]!.findAll('td')[5]!.text()).toBe('$0.0123')
  })

  it('成本 0 也出 $0.0000（0 成本是合法值）', async () => {
    const w = await factory()
    expect(w.findAll('.qc-table tbody tr')[3]!.findAll('td')[5]!.text()).toBe('$0.0000')
  })

  /**
   * 桌面用**内联 style 的五档色阶**（`qualityColor`）。这条门禁把那一格的颜色
   * 与粗体一起钉住 —— 卡片侧是 3 档 tone，两者阈值同源但形态不同。
   */
  it('成功率 / 质量两格保留内联色与粗体（五档色阶逐值不同）', async () => {
    const w = await factory()
    const rows = w.findAll('.qc-table tbody tr')
    const cell = (i: number, col: 2 | 4) => rows[i]!.findAll('td')[col]!.attributes('style') ?? ''
    // 0.92 → 绿；0.55 → 黄；0.10 → 红
    expect(cell(0, 2)).toContain('color: rgb(34, 197, 94)')
    expect(cell(1, 2)).toContain('color: rgb(234, 179, 8)')
    expect(cell(2, 2)).toContain('color: rgb(239, 68, 68)')
    expect(cell(0, 2)).toContain('font-weight: 600')
    expect(cell(0, 4)).toContain('color: rgb(34, 197, 94)')
  })

  it('桌面不出卡片，也不出现连续加载尾部', async () => {
    const w = await factory()
    expect(w.find('[data-testid="card-list"]').exists()).toBe(false)
    expect(w.find('.hyper-load-more').exists()).toBe(false)
  })

  /** insights 是 `<ol>` 排名列表，不是数据表 —— 两档都必须原样保留。 */
  it('insights 排名列表（带序号与相关系数配色）三档都在', async () => {
    const w = await factory()
    // ★ 连 `<ol>` 本身一起断：它带 `class="insights-list"`（list-style:none /
    //   padding:0 全挂在那个类上）。第一版只查子项 `.insight-item`，
    //   于是把 `<ol>` 的类改名照样全绿（变异 M18）—— 承载样式的元素必须单独断。
    const ol = w.find('ol.insights-list')
    expect(ol.exists(), '承载 list-style:none 的 <ol class="insights-list"> 不见了').toBe(true)
    const items = ol.findAll('.insight-item')
    expect(items).toHaveLength(3)
    expect(items[0]!.find('.insight-rank').text()).toBe('#1')
    expect(items[0]!.find('.insight-correlation').text()).toContain('r = 0.820')
    const bg = (i: number) => items[i]!.find('.insight-correlation').attributes('style') ?? ''
    // +0.82 绿 / -0.61 橙（abs∈[0.4,0.7)） / -0.85 红（abs>=0.7）
    expect(bg(0)).toContain('background: rgb(34, 197, 94)')
    expect(bg(1)).toContain('background: rgb(249, 115, 22)')
    expect(bg(2)).toContain('background: rgb(239, 68, 68)')
  })
})

describe('QualityCorrelationsView：桌面空态不得变成「空表壳」', () => {
  it('breakdown 为空：表不渲染、出本页文案，且 DOM 里不剩 <table>', async () => {
    ctrl.empty = true
    const w = await factory()
    expect(w.find('.qc-table').exists()).toBe(false)
    expect(w.findAll('table')).toHaveLength(0)
    expect(w.text()).toContain('暂无分桶数据')
  })
})

describe('QualityCorrelationsView：compact 卡片形态', () => {
  beforeEach(() => mockWindowClass('compact'))

  it('出卡片不出表；insights 排名列表照旧（不套 CardList）', async () => {
    const w = await factory()
    expect(cardsOf(w)).toHaveLength(5)
    expect(w.find('.qc-table').exists()).toBe(false)
    expect(w.findAll('.insight-item')).toHaveLength(3)
  })

  it('不渲染视图切换钮', async () => {
    const w = await factory()
    expect(w.find('.responsive-data-view__switch').exists()).toBe(false)
  })

  it('卡头是分桶标签；五个字段走 format', async () => {
    const w = await factory()
    const cards = cardsOf(w)
    expect(cards[0]!.find('.card__title').text()).toBe('0-500')
    expect(fieldValue(cards[0]!, '样本数').text()).toBe('1,200')
    expect(fieldValue(cards[0]!, '成功率').text()).toBe('92.0%')
    expect(fieldValue(cards[0]!, '延迟').text()).toBe('850ms')
    expect(fieldValue(cards[1]!, '延迟').text()).toBe('1.50s')
    expect(fieldValue(cards[0]!, '质量').text()).toBe('93.0%')
    expect(fieldValue(cards[0]!, '成本').text()).toBe('$0.0123')
    expect(fieldValue(cards[3]!, '成本').text()).toBe('$0.0000')
  })

  /** ★ tone 必须**逐行**且落在 qualityTone 的阈值上（0.7 / 0.4 归上档）。 */
  it('质量档位逐行求值（0.92 good / 0.55 warn / 0.10 danger，0.70 归 good）', async () => {
    const w = await factory()
    const cards = cardsOf(w)
    expect(fieldValue(cards[0]!, '成功率').attributes('data-tone')).toBe('good')
    expect(fieldValue(cards[1]!, '成功率').attributes('data-tone')).toBe('warn')
    expect(fieldValue(cards[2]!, '成功率').attributes('data-tone')).toBe('danger')
    expect(fieldValue(cards[3]!, '成功率').attributes('data-tone')).toBe('good')
    // 质量列是**另一组数**（0.5 → warn），不能跟着成功率走
    expect(fieldValue(cards[1]!, '质量').attributes('data-tone')).toBe('warn')
  })

  it('空态出 EmptyState，而不是本页的 .empty 文案', async () => {
    ctrl.empty = true
    const w = await factory()
    const states = w.findAll('.app-empty-state')
    expect(states.length).toBeGreaterThan(0)
    expect(states[0]!.text()).toContain('暂无分桶数据')
  })

  it('卡片里不出现 undefined / null 字面量', async () => {
    const w = await factory()
    for (const c of cardsOf(w)) {
      expect(c.text()).not.toContain('undefined')
      expect(c.text()).not.toContain('null')
    }
  })
})

describe('QualityCorrelationsView：跨层契约', () => {
  it('容器的 :empty 带 isCompact 前置（桌面空态仍由本页出）', () => {
    expect(codeOnly).toContain(':empty="isCompact && resp.breakdown.length === 0"')
    expect(codeOnly).toContain('v-if="!isCompact && !resp.breakdown.length"')
    // 两处各恰好一次
    expect((codeOnly.match(/:empty="isCompact/g) ?? []).length).toBe(1)
    expect((codeOnly.match(/!resp\.breakdown\.length/g) ?? []).length).toBe(1)
  })

  it('#table 槽内的 v-if 留在 <table> 上', () => {
    expect(codeOnly).toContain('<table v-if="resp.breakdown.length > 0" class="qc-table">')
  })

  it('不给容器传 :loading（section 由 v-if="resp" 门控，传了是死代码）', () => {
    expect(codeOnly).not.toMatch(/<ResponsiveDataView[\s\S]{0,400}?\s:loading=/)
  })

  it('本页不引入连续加载（没有分页 API）', () => {
    expect(codeOnly).not.toContain('HyperLoadMore')
    expect(codeOnly).not.toContain('createHyperPages')
  })

  it('table-min-width 显式传 0px（.qc-table 原本无表级 min-width）', () => {
    expect((codeOnly.match(/table-min-width="0px"/g) ?? []).length).toBe(1)
    const views = (codeOnly.match(/<ResponsiveDataView\b/g) ?? []).length
    expect(views).toBe(1)
    expect((codeOnly.match(/table-min-width=/g) ?? []).length).toBe(views)
  })

  it('表格与卡片共用同一套格式化函数（不写第二份）', () => {
    // 表格那几格已改走 fmtCount / fmtUsd，模板里不该再有内联 toLocaleString / toFixed(4)
    expect(codeOnly).toContain('<td>{{ fmtCount(r.samples) }}</td>')
    expect(codeOnly).toContain('<td>{{ fmtUsd(r.avg_cost_usd) }}</td>')
    expect(codeOnly).not.toMatch(/\{\{ r\.samples\.toLocaleString\(\) \}\}/)
    expect((codeOnly.match(/function fmtCount\(/g) ?? []).length).toBe(1)
    expect((codeOnly.match(/function fmtUsd\(/g) ?? []).length).toBe(1)
    expect((codeOnly.match(/function fmtPct\(/g) ?? []).length).toBe(1)
    expect((codeOnly.match(/function fmtMs\(/g) ?? []).length).toBe(1)
  })

  it('质量 tone 与桌面色阶同源同一组阈值（0.7 / 0.4）', () => {
    expect(codeOnly).toContain('if (v >= 0.7) return \'good\'')
    expect(codeOnly).toContain('if (v >= 0.4) return \'warn\'')
    expect(codeOnly).toContain('return \'danger\'')
    // 桌面五档色阶的阈值必须还在（卡片降成三档，但阈值同源）
    expect(codeOnly).toContain('if (q >= 0.85)')
    expect(codeOnly).toContain('if (q >= 0.7)')
    expect(codeOnly).toContain('if (q >= 0.55)')
    expect(codeOnly).toContain('if (q >= 0.4)')
  })

  it('页面没有硬编码中文文案（全部走 i18n）', () => {
    const cjk = codeOnly.match(/[一-鿿]/g)
    expect(cjk, `出现了硬编码中文：${cjk?.join('')}`).toBeNull()
  })

  it('没有新增请求端点（仍走 api 层的 getQualityCorrelations）', () => {
    for (const banned of ['fetch(', 'axios', 'XMLHttpRequest']) {
      expect(codeOnly, `不应直接发起请求，却出现了 ${banned}`).not.toContain(banned)
    }
    expect(codeOnly).toContain('getQualityCorrelations')
  })
})
