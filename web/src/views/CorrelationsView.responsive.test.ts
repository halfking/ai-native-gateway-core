// CorrelationsView.responsive.test.ts — H6 第十三条垂直切片的门禁。
//
// 本页的形状：
// 1. **没有分页 API**（`getAutoRouteCorrelations({days, min_samples})` 一次取回 5 段）
//    ⇒ 只改呈现形态，不引入连续加载。门禁用「页面里不存在连续加载入口」钉住。
// 2. **4 张同构表**（by_model / by_strategy / by_task_type / by_model_task）桌面都是
//    5 列，其中第 4 张**没有 Cost 列** ⇒ 卡片也不许有。
// 3. **桌面空态时整张表都不渲染**（`<table v-if>` + `<p v-else>`）⇒ `#table` 槽内
//    必须保留 `v-if`，否则空态下多出一个带边框的空表壳。
// 4. **第 4 张表的身份是复合键**（model + task_type）⇒ 撞键是真实风险，
//    门禁要断「卡头带分隔符」且 fixture 里同一模型出现两次。
// 5. **verdict 段不套 CardList**（与切片八的 insights 同源：分组嵌套排名列表）。
//
// 门禁清单：
// A. 桌面零回归：4 张表的列头文案与顺序、行内 tag/千分位/百分比/ms-s/$ 三档、
//    成功率五档内联色与粗体、verdict 分组排名、窄屏网格下溢修复
// B. 桌面空态：表不渲染、出**本页各段自己的**文案、DOM 里不剩 <table>
// C. compact 卡片：出卡片不出表、字段走同一份 format、tone 逐行、卡头是复合身份、
//    第 4 张卡无 Cost、空态出 EmptyState 而非空 <ul>
// D. 跨层契约：无连续加载、`:empty` 带 isCompact 前置、`table-min-width="0px"`、
//    不传 `:loading`、列名单一真源、0 新增 i18n 键
import { flushPromises, enableAutoUnmount, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import CorrelationsView from './CorrelationsView.vue'
import { _resetForTests } from '../composables/useWindowClass'
import { _resetDataViewModeForTests } from '../composables/useDataViewMode'

enableAutoUnmount(afterEach)

const SOURCE = readFileSync(resolve(process.cwd(), 'src/views/CorrelationsView.vue'), 'utf8')
/** 剥掉全部注释（块 / 模板 / 行；行注释正则避开 `://`）。 */
const CODE_ONLY = SOURCE
  .replace(/\/\*[\s\S]*?\*\//g, '')
  .replace(/<!--[\s\S]*?-->/g, '')
  .replace(/(^|[^:'"`\\])\/\/[^\n]*/g, '$1')
/** 只取 `<template>` 段并剥掉模板注释 —— 判「模板里有没有硬编码」时用它。 */
const TEMPLATE_CODE = SOURCE.slice(SOURCE.indexOf('<template>'), SOURCE.indexOf('</template>'))
  .replace(/<!--[\s\S]*?-->/g, '')

const { ctrl, mockApi } = vi.hoisted(() => {
  const ctrl = {
    calls: [] as Array<Record<string, unknown>>,
    /** 段名集合：'by_model' / 'by_strategy' / 'by_task_type' / 'by_model_task' / 'verdict' */
    empty: new Set<string>(),
    fail: false,
    reset() {
      ctrl.calls = []
      ctrl.empty = new Set<string>()
      ctrl.fail = false
    },
  }
  return { ctrl, mockApi: () => ({}) }
})

/**
 * by_model 的 9 行**刻意跨满 `successColor` 的五档色阶与全部四个边界**：
 * 0.98→绿 / 0.90→黄绿 / 0.75→黄 / 0.60→橙 / 0.30→红，外加恰好等于 0.95 / 0.85 /
 * 0.7 / 0.5 的四行边界。阈值（`>=` 写成 `>`）一旦被挪，边界行立刻串色。
 *
 * 延迟同时覆盖 `ms` 支（850 / 999 / 300）与 `s` 支（1200 / 1500 / 2400）：
 * 1000~1500 之间必须**有值**，否则「把 1000 挪到 1500」这个变异是行为等价的，
 * 门会全绿而并不知情（切片八 M10 的同一个坑）。
 * 成本覆盖 `fmtUsd` 三支：0 → `$0`，0.0005 → `$0.50m`，0.01234 → `$0.0123`。
 */
const BY_MODEL = [
  { label: 'gpt-4o', samples: 1234, success_rate: 0.98, avg_latency_ms: 850, avg_cost_usd: 0.01234 },
  { label: 'claude-sonnet', samples: 900, success_rate: 0.9, avg_latency_ms: 1200, avg_cost_usd: 0.05 },
  { label: 'glm-4.6', samples: 500, success_rate: 0.75, avg_latency_ms: 2400, avg_cost_usd: 0.0005 },
  { label: 'qwen-max', samples: 200, success_rate: 0.6, avg_latency_ms: 300, avg_cost_usd: 0 },
  { label: 'broken-model', samples: 7, success_rate: 0.3, avg_latency_ms: 4500, avg_cost_usd: 1.5 },
  { label: 'edge-95', samples: 1, success_rate: 0.95, avg_latency_ms: 999, avg_cost_usd: 0 },
  { label: 'edge-85', samples: 1, success_rate: 0.85, avg_latency_ms: 1000, avg_cost_usd: 0 },
  { label: 'edge-70', samples: 1, success_rate: 0.7, avg_latency_ms: 1001, avg_cost_usd: 0 },
  // ★ 成本 0.004 落在 `fmtUsd` 两个阈值之间（0.001 与 0.01）：原值走 `$${toFixed(4)}`
  //   支得 '$0.0040'，把 0.001 阈值挪到 0.01 就变成 '$4.00m'。缺这一行时那个变异
  //   是**行为等价**的（其余成本都在阈值同侧）⇒ 台账上多一条无牙，而门并不知情。
  { label: 'edge-50', samples: 1, success_rate: 0.5, avg_latency_ms: 1500, avg_cost_usd: 0.004 },
]

const BY_STRATEGY = [
  { label: 'pattern_layered', samples: 700, success_rate: 0.93, avg_latency_ms: 640, avg_cost_usd: 0.01 },
  { label: 'baseline_heuristic', samples: 300, success_rate: 0.71, avg_latency_ms: 520, avg_cost_usd: 0.004 },
]

const BY_TASK_TYPE = [
  { label: 'chat', samples: 1500, success_rate: 0.99, avg_latency_ms: 410, avg_cost_usd: 0.008 },
  { label: 'reasoning', samples: 220, success_rate: 0.62, avg_latency_ms: 2300, avg_cost_usd: 0.03 },
  { label: 'code', samples: 310, success_rate: 0.88, avg_latency_ms: 1180, avg_cost_usd: 0.02 },
]

/**
 * 第 4 张表：同一模型出现**两次**（chat / reasoning）⇒ 撞键是真实可触发的。
 * 成本字段给了值但桌面不渲染 ⇒ 卡片也不许渲染（字段政策红线）。
 * 顺序刻意打乱：桌面按 success 降序排，排错顺序门禁会红。
 */
const BY_MODEL_TASK = [
  { model: 'claude-sonnet', task_type: 'reasoning', samples: 60, success_rate: 0.4, avg_latency_ms: 3200, avg_cost_usd: 0.03 },
  { model: 'gpt-4o', task_type: 'reasoning', samples: 120, success_rate: 0.55, avg_latency_ms: 2100, avg_cost_usd: 0.02 },
  { model: 'gpt-4o', task_type: 'chat', samples: 800, success_rate: 0.98, avg_latency_ms: 400, avg_cost_usd: 0.01 },
  { model: 'glm-4.6', task_type: 'code', samples: 300, success_rate: 0.8, avg_latency_ms: 1500, avg_cost_usd: 0.004 },
  { model: 'claude-sonnet', task_type: 'chat', samples: 640, success_rate: 0.92, avg_latency_ms: 700, avg_cost_usd: 0.02 },
]

/** verdict 同样打乱：组内按 rank 升序、组间按首次出现顺序。 */
const VERDICT = [
  { task_type: 'chat', model: 'claude-sonnet', success_rate: 0.92, avg_latency_ms: 700, rank: 2 },
  { task_type: 'chat', model: 'gpt-4o', success_rate: 0.98, avg_latency_ms: 400, rank: 1 },
  { task_type: 'reasoning', model: 'claude-sonnet', success_rate: 0.4, avg_latency_ms: 3200, rank: 2 },
  { task_type: 'reasoning', model: 'gpt-4o', success_rate: 0.55, avg_latency_ms: 2100, rank: 1 },
]

function pick(rows: Array<Record<string, unknown>>, id: string) {
  return ctrl.empty.has(id) ? [] : rows
}

vi.mock('../api', () => ({
  getAutoRouteCorrelations: vi.fn(async (params: Record<string, unknown> = {}) => {
    ctrl.calls.push(params)
    if (ctrl.fail) throw new Error('corr boom')
    return {
      window_days: 7,
      generated_at: '2026-10-06T01:00:00.000Z',
      by_model: pick(BY_MODEL, 'by_model'),
      by_strategy: pick(BY_STRATEGY, 'by_strategy'),
      by_task_type: pick(BY_TASK_TYPE, 'by_task_type'),
      by_model_task: pick(BY_MODEL_TASK, 'by_model_task'),
      verdict: pick(VERDICT, 'verdict'),
    }
  }),
}))

vi.mock('../components/ui/KxDateRangePicker.vue', () => ({
  default: { props: ['modelValue', 'presets', 'maxSpanDays'], template: '<div class="kxdaterangepicker-stub" />' },
}))

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  fallbackWarn: false,
  missingWarn: false,
  messages: {
    'zh-CN': {
      correlations: {
        title: '自动路由关联分析',
        sections: {
          byModel: '按模型',
          byStrategy: '按策略',
          byTaskType: '按任务类型',
          outlier: '按（模型, 任务类型）— 异常检测',
          topModels: '各任务类型 Top-3 模型',
        },
      },
      common: {
        dateRange: {
          title: '时间范围',
          startDate: '开始',
          endDate: '结束',
          preset: { today: '今天', last7d: '近 7 天', last30d: '近 30 天' },
        },
        button: { apply: '应用' },
      },
      dashboard: { range: { last90d: '近 90 天' } },
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
  const w = mount(CorrelationsView, { global: { plugins: [i18n] } })
  await flushPromises()
  await flushPromises()
  return w
}

/** ★ 定位必须自带作用域：四张表各自的卡片只能靠 `data-testid` 段区分。 */
function cardsIn(w: Awaited<ReturnType<typeof factory>>, section: string) {
  return w.findAll(`[data-testid="${section}"] [data-testid="card-list"] .card`)
}

function fieldValue(card: Awaited<ReturnType<typeof cardsIn>>[number], label: string) {
  const hit = card.findAll('.card__field').find((f) => f.find('dt').text() === label)
  expect(
    hit,
    `卡片里找不到字段「${label}」，实际有：${card
      .findAll('.card__field')
      .map((f) => f.find('dt').text())
      .join('/')}`,
  ).toBeTruthy()
  return hit!.find('dd')
}

function cardTitles(w: Awaited<ReturnType<typeof factory>>, section: string): string[] {
  return cardsIn(w, section).map((c) => c.attributes('data-title') ?? '')
}

beforeEach(() => {
  _resetDataViewModeForTests()
  ctrl.reset()
  mockWindowClass('expanded')
  localStorage.clear()
})

afterEach(() => vi.clearAllMocks())

describe('CorrelationsView：桌面 4 张表零回归', () => {
  it('5 段标题走 i18n，顺序不变', async () => {
    const w = await factory()
    expect(w.findAll('section.card h2').map((h) => h.text())).toEqual([
      '按模型',
      '按策略',
      '按任务类型',
      '按（模型, 任务类型）— 异常检测',
      '各任务类型 Top-3 模型',
    ])
  })

  it('by_model / by_strategy / by_task_type 三张表列头：首列各自不同，后 4 列同序', async () => {
    const w = await factory()
    const heads = (id: string) =>
      w.findAll(`[data-testid="${id}"] table thead th`).map((th) => th.text())
    expect(heads('corr-by-model')).toEqual(['Model', 'Samples', 'Success', 'Latency', 'Cost'])
    expect(heads('corr-by-strategy')).toEqual(['Strategy', 'Samples', 'Success', 'Latency', 'Cost'])
    expect(heads('corr-by-task-type')).toEqual(['Task type', 'Samples', 'Success', 'Latency', 'Cost'])
  })

  /** 第 4 张表 5 列且**没有 Cost** —— `CorrelationRowMT` 带该字段但桌面不渲染。 */
  it('by_model_task 列头：Model + Task type 两列身份列，且不含 Cost', async () => {
    const w = await factory()
    const heads = w.findAll('[data-testid="corr-by-model-task"] table thead th').map((th) => th.text())
    expect(heads).toEqual(['Model', 'Task type', 'Samples', 'Success', 'Latency'])
    expect(heads).not.toContain('Cost')
  })

  it('by_model 行内：tag 种类、千分位、百分比一位小数、ms/s 两支、$ 三档', async () => {
    const w = await factory()
    const rows = w.findAll('[data-testid="corr-by-model"] table tbody tr')
    expect(rows).toHaveLength(9)
    expect(rows[0]!.find('.tag-model').text()).toBe('gpt-4o')
    expect(rows[0]!.findAll('td')[1]!.text()).toBe('1,234')
    expect(rows[0]!.findAll('td')[2]!.text()).toBe('98.0%')
    expect(rows[0]!.findAll('td')[3]!.text()).toBe('850ms')
    expect(rows[2]!.findAll('td')[3]!.text()).toBe('2.40s')
    // 1000~1500 之间有 fixture（edge-85 = 1000、edge-70 = 1001）：阈值挪到 1500 时这格会变色
    expect(rows[6]!.findAll('td')[3]!.text()).toBe('1.00s')
    // fmtUsd 三支
    expect(rows[0]!.findAll('td')[4]!.text()).toBe('$0.0123')
    expect(rows[2]!.findAll('td')[4]!.text()).toBe('$0.50m')
    expect(rows[3]!.findAll('td')[4]!.text()).toBe('$0')
    // 0.004 落在两个阈值之间：这一格把「毫单位阈值」钉死
    expect(rows[8]!.findAll('td')[4]!.text()).toBe('$0.0040')
  })

  it('三种 tag 类名各自挂在自己的行上', async () => {
    const w = await factory()
    expect(w.findAll('[data-testid="corr-by-model"] .tag-model')).toHaveLength(9)
    expect(w.findAll('[data-testid="corr-by-strategy"] .tag-strategy')).toHaveLength(2)
    expect(w.findAll('[data-testid="corr-by-task-type"] .tag-task')).toHaveLength(3)
  })

  /**
   * 桌面用**内联 style 的五档色阶**（`successColor`）。这条门禁把颜色与粗体钉住
   * —— 卡片侧是 3 档 tone，两者阈值同源但形态不同。
   * 边界行（0.95 / 0.85 / 0.7 / 0.5）逐个断，`>=` 被改成 `>` 时至少红一条。
   */
  it('成功率格保留内联色与粗体：五档色阶 + 四条边界逐值不同', async () => {
    const w = await factory()
    const rows = w.findAll('[data-testid="corr-by-model"] table tbody tr')
    const cell = (i: number) => rows[i]!.findAll('td')[2]!.attributes('style') ?? ''
    expect(cell(0)).toContain('color: rgb(34, 197, 94)') // 0.98 绿
    expect(cell(1)).toContain('color: rgb(132, 204, 22)') // 0.90 黄绿
    expect(cell(2)).toContain('color: rgb(234, 179, 8)') // 0.75 黄
    expect(cell(3)).toContain('color: rgb(249, 115, 22)') // 0.60 橙
    expect(cell(4)).toContain('color: rgb(239, 68, 68)') // 0.30 红
    expect(cell(5)).toContain('color: rgb(34, 197, 94)') // 0.95 边界仍绿
    expect(cell(6)).toContain('color: rgb(132, 204, 22)') // 0.85 边界仍黄绿
    expect(cell(7)).toContain('color: rgb(234, 179, 8)') // 0.70 边界仍黄
    expect(cell(8)).toContain('color: rgb(249, 115, 22)') // 0.50 边界仍橙
    expect(cell(0)).toContain('font-weight: 600')
  })

  it('by_model_task 按成功率降序排（顺序被排错会红）', async () => {
    const w = await factory()
    const first = w.findAll('[data-testid="corr-by-model-task"] table tbody tr').map((r) => [
      r.find('.tag-model').text(),
      r.find('.tag-task').text(),
    ])
    expect(first).toEqual([
      ['gpt-4o', 'chat'],
      ['claude-sonnet', 'chat'],
      ['glm-4.6', 'code'],
      ['gpt-4o', 'reasoning'],
      ['claude-sonnet', 'reasoning'],
    ])
  })

  it('verdict 是分组嵌套排名列表，两档都原样保留（不套 CardList）', async () => {
    const w = await factory()
    const v = w.find('[data-testid="corr-verdict"]')
    expect(v.findAll('.verdict-card')).toHaveLength(2)
    expect(v.findAll('.verdict-card')[0]!.find('.verdict-task-type').text()).toBe('chat')
    // ★ 承载 `list-style: none` 的是 <ol> **本身**（切片八 M18 同一个坑）：
    //   只查子项的话，把 <ol> 换成 <ul> 照样全绿。
    expect(v.find('ol.verdict-list').exists(), '承载 list-style:none 的 <ol class="verdict-list"> 不见了').toBe(true)
    // 组内按 rank 升序：payload 里 chat 的 rank 2 在前，这里必须被排到 #2
    const items = v.findAll('.verdict-card')[0]!.findAll('.verdict-item')
    expect(items.map((i) => i.find('.verdict-rank').text())).toEqual(['#1', '#2'])
    expect(items[0]!.find('.verdict-model').text()).toBe('gpt-4o')
    expect(items[0]!.text()).toContain('98.0%')
    expect(items[0]!.text()).toContain('400ms')
    // 成功率内联色**逐行**：0.98 绿 / 0.92 黄绿。第一版只断了百分比文本，
    // 于是 `successColor(v.success_rate)` 被换成常量（台账 M30 无牙）——
    // 颜色与数字是同一格的两个属性，只断数字就等于没断颜色。
    const styles = (i: number) => items[i]!.findAll('span').map((s) => s.attributes('style') ?? '').join('|')
    expect(styles(0)).toContain('color: rgb(34, 197, 94)')
    expect(styles(1)).toContain('color: rgb(132, 204, 22)')
    // rank-1 的高亮类挂在承载样式的元素上，必须单独断
    expect(items[0]!.classes()).toContain('rank-1')
    expect(v.find('[data-testid="card-list"]').exists()).toBe(false)
  })

  it('verdict 网格窄屏不下溢：minmax(min(280px, 100%), 1fr)', () => {
    expect(CODE_ONLY).toContain('repeat(auto-fit, minmax(min(280px, 100%), 1fr))')
  })

  it('桌面不出卡片，也不出现连续加载尾部', async () => {
    const w = await factory()
    expect(w.find('[data-testid="card-list"]').exists()).toBe(false)
    expect(w.find('.hyper-load-more').exists()).toBe(false)
  })

  it('首屏请求带默认 days=7 / min_samples=20；改下拉即重取', async () => {
    const w = await factory()
    expect(ctrl.calls).toEqual([{ days: 7, min_samples: 20 }])
    await w.find('.filter-bar select').setValue('50')
    await flushPromises()
    expect(ctrl.calls[1]).toEqual({ days: 7, min_samples: 50 })
  })
})

describe('CorrelationsView：桌面空态不得变成「空表壳」', () => {
  it('by_model 为空：表不渲染、出本页文案，DOM 里不剩 <table>', async () => {
    ctrl.empty.add('by_model')
    const w = await factory()
    expect(w.find('[data-testid="corr-by-model"] table').exists()).toBe(false)
    expect(w.find('[data-testid="corr-by-model"] .empty').text()).toContain(
      'No data — try lowering min_samples or expanding the window.',
    )
    // 其它三段仍出表 ⇒ 「表都不剩」只对被清空的那一段成立
    expect(w.find('[data-testid="corr-by-strategy"] table').exists()).toBe(true)
  })

  /** 三段空态文案各不相同 ⇒ 串了就是真回归（复制粘贴最容易错在这里）。 */
  it('三段空态各出各的文案，互不串', async () => {
    ctrl.empty.add('by_model')
    ctrl.empty.add('by_strategy')
    ctrl.empty.add('by_task_type')
    const w = await factory()
    expect(w.find('[data-testid="corr-by-model"] .empty').text()).toContain('expanding the window')
    expect(w.find('[data-testid="corr-by-strategy"] .empty').text()).toContain('A/B test')
    expect(w.find('[data-testid="corr-by-task-type"] .empty').text()).toBe('No data.')
    // 只断被清空的这三段：第 4 段没被清，仍应出表（断成「全页零 <table>」会把
    // 「清空三段」和「清空全页」混成同一件事）
    for (const id of ['corr-by-model', 'corr-by-strategy', 'corr-by-task-type']) {
      expect(w.findAll(`[data-testid="${id}"] table`)).toHaveLength(0)
    }
    expect(w.find('[data-testid="corr-by-model-task"] table').exists()).toBe(true)
  })

  /** 全部四段都空 ⇒ 整页 DOM 里一个 `<table>` 都不剩。 */
  it('四段全空：整页不剩任何 <table>', async () => {
    ctrl.empty.add('by_model')
    ctrl.empty.add('by_strategy')
    ctrl.empty.add('by_task_type')
    ctrl.empty.add('by_model_task')
    const w = await factory()
    expect(w.findAll('table')).toHaveLength(0)
  })

  /** 第 4 段桌面**本来就没有**空态：收起的 <details> 里 summary 写「0 pairs」。 */
  it('by_model_task 为空：桌面维持原样（不凭空多一条空态文案）', async () => {
    ctrl.empty.add('by_model_task')
    const w = await factory()
    const sec = w.find('[data-testid="corr-by-model-task"]')
    expect(sec.find('table').exists()).toBe(false)
    expect(sec.find('summary').text()).toBe('0 (model, task_type) pairs')
    expect(sec.find('.empty').exists()).toBe(false)
  })

  it('接口失败：错误横幅出现，且不与空态同屏（resp 为空 ⇒ 五段都不渲染）', async () => {
    ctrl.fail = true
    const w = await factory()
    expect(w.find('.filter-card .error').text()).toContain('corr boom')
    expect(w.find('.empty').exists()).toBe(false)
    // 筛选卡是**唯一**的 section.card（它没有 h2，只有 filter-bar）⇒ 五段都不渲染
    expect(w.findAll('section.card')).toHaveLength(1)
    expect(w.find('[data-testid="corr-by-model"]').exists()).toBe(false)
  })
})

describe('CorrelationsView：compact 卡片形态', () => {
  beforeEach(() => mockWindowClass('compact'))

  it('四张表各出卡片不出表', async () => {
    const w = await factory()
    expect(cardsIn(w, 'corr-by-model')).toHaveLength(9)
    expect(cardsIn(w, 'corr-by-strategy')).toHaveLength(2)
    expect(cardsIn(w, 'corr-by-task-type')).toHaveLength(3)
    expect(cardsIn(w, 'corr-by-model-task')).toHaveLength(5)
    expect(w.findAll('table')).toHaveLength(0)
  })

  it('compact 下不渲染形态切换钮（无从点回表格）', async () => {
    const w = await factory()
    expect(w.find('.responsive-data-view__switch').exists()).toBe(false)
  })

  it('前三张卡头就是 label', async () => {
    const w = await factory()
    expect(cardTitles(w, 'corr-by-model').slice(0, 2)).toEqual(['gpt-4o', 'claude-sonnet'])
    expect(cardTitles(w, 'corr-by-strategy')).toEqual(['pattern_layered', 'baseline_heuristic'])
    expect(cardTitles(w, 'corr-by-task-type')).toEqual(['chat', 'reasoning', 'code'])
  })

  /**
   * 复合身份：同一模型出现两次时卡头必须能区分。
   * 断法是「两行同模型的卡头不相等」而不是「等于某个字面量」——
   * 分隔符换了（`-` → `·`）不该让这条红，红的是「键撞了」。
   */
  it('第 4 张卡头是 model · task_type 复合身份：同一模型两行不相等', async () => {
    const w = await factory()
    const titles = cardTitles(w, 'corr-by-model-task')
    expect(titles).toHaveLength(5)
    expect(new Set(titles).size).toBe(5)
    expect(titles).toContain('gpt-4o · chat')
    expect(titles).toContain('gpt-4o · reasoning')
  })

  it('卡片字段走同一份 format：千分位 / 百分比 / ms / $ 三档', async () => {
    const w = await factory()
    const c0 = cardsIn(w, 'corr-by-model')[0]!
    expect(fieldValue(c0, 'Samples').text()).toBe('1,234')
    expect(fieldValue(c0, 'Success').text()).toBe('98.0%')
    expect(fieldValue(c0, 'Latency').text()).toBe('850ms')
    expect(fieldValue(c0, 'Cost').text()).toBe('$0.0123')
    const c2 = cardsIn(w, 'corr-by-model')[2]!
    expect(fieldValue(c2, 'Latency').text()).toBe('2.40s')
    expect(fieldValue(c2, 'Cost').text()).toBe('$0.50m')
  })

  /**
   * 卡片侧是 3 档 tone（`successTone`），与桌面 5 档 hex **阈值同源但档数不同**。
   * 这条断的是「逐行」：good / warn / danger 必须能同页共存，
   * 否则就是整列表一个色。
   */
  it('tone 逐行：good / warn / danger 同页共存，边界值按 >= 落档', async () => {
    const w = await factory()
    const toneOf = (label: string) => {
      const c = cardsIn(w, 'corr-by-model').find((x) => x.attributes('data-title') === label)!
      return fieldValue(c, 'Success').attributes('data-tone')
    }
    expect(toneOf('gpt-4o')).toBe('good') // 0.98
    expect(toneOf('claude-sonnet')).toBe('good') // 0.90
    expect(toneOf('glm-4.6')).toBe('warn') // 0.75
    expect(toneOf('qwen-max')).toBe('danger') // 0.60
    expect(toneOf('broken-model')).toBe('danger') // 0.30
    expect(toneOf('edge-95')).toBe('good') // 0.95 边界
    expect(toneOf('edge-85')).toBe('good') // 0.85 边界（写成 > 就变 warn）
    expect(toneOf('edge-70')).toBe('warn') // 0.70 边界（写成 > 就变 danger）
    expect(toneOf('edge-50')).toBe('danger') // 0.50 边界
  })

  it('第 4 张卡片不渲染 Cost（桌面那张表就没有这列）', async () => {
    const w = await factory()
    const c = cardsIn(w, 'corr-by-model-task')[0]!
    expect(c.findAll('.card__field').map((f) => f.find('dt').text())).toEqual([
      'Samples',
      'Success',
      'Latency',
    ])
  })

  it('第 4 张卡片仍被 <details> 包着（收起行为两档一致）', async () => {
    const w = await factory()
    expect(w.find('[data-testid="corr-by-model-task"] details').exists()).toBe(true)
  })

  it('verdict 段在 compact 下仍不套 CardList（分组排名列表不是数据表）', async () => {
    const w = await factory()
    const v = w.find('[data-testid="corr-verdict"]')
    expect(v.findAll('.verdict-card')).toHaveLength(2)
    expect(v.find('[data-testid="card-list"]').exists()).toBe(false)
  })

  it('空态出 EmptyState 文案，而不是一个空 <ul>', async () => {
    ctrl.empty.add('by_model')
    const w = await factory()
    const sec = w.find('[data-testid="corr-by-model"]')
    expect(sec.find('table').exists()).toBe(false)
    expect(sec.find('.empty').exists()).toBe(false) // 桌面那条 <p> 被 !isCompact 关掉
    expect(sec.text()).toContain('No data — try lowering min_samples or expanding the window.')
    expect(sec.findAll('.card')).toHaveLength(0)
  })

  it('第 4 段空态出 compact 专属文案（桌面没有，卡片形态不能留空 <ul>）', async () => {
    ctrl.empty.add('by_model_task')
    const w = await factory()
    const sec = w.find('[data-testid="corr-by-model-task"]')
    expect(sec.text()).toContain('No (model, task_type) pairs.')
    expect(sec.findAll('.card')).toHaveLength(0)
  })
})

describe('CorrelationsView：跨层契约', () => {
  it('无分页 ⇒ 不引入连续加载', () => {
    for (const token of ['createHyperPages', 'HyperLoadMore', 'hyper-load-more', 'page_size', 'offset']) {
      expect(CODE_ONLY, `页面里不该出现 ${token}`).not.toContain(token)
    }
  })

  it('四处容器的 :empty 都带 isCompact 前置，桌面那条 <p> 都带 !isCompact', () => {
    const emptyOnes = CODE_ONLY.match(/:empty="[^"]*"/g) ?? []
    expect(emptyOnes).toHaveLength(4)
    for (const e of emptyOnes) {
      expect(e).toMatch(/^:empty="isCompact && /)
    }
    const desktopOnes = CODE_ONLY.match(/v-if="!isCompact && [^"]*length === 0"/g) ?? []
    expect(desktopOnes).toHaveLength(3)
  })

  it('四处都传 table-min-width="0px"（.corr-table 自带 width:100%，无 min-width）', () => {
    expect(CODE_ONLY.match(/table-min-width="0px"/g)).toHaveLength(4)
    expect(CODE_ONLY).not.toMatch(/table-min-width="(?!0px)/)
  })

  it('不传 :loading（本页由 v-if="resp" 门控，loading 传进去是死代码）', () => {
    expect(CODE_ONLY).not.toContain(':loading=')
  })

  it('列名单一真源：模板里没有硬编码的 <th> 字面量', () => {
    expect(TEMPLATE_CODE).not.toMatch(/<th>\s*[A-Za-z]/)
    expect(TEMPLATE_CODE).toContain('{{ COLS.model }}')
    expect(TEMPLATE_CODE).toContain('{{ COLS.cost }}')
  })

  it('样本数千分位已抽成 fmtCount，模板里不再内联 toLocaleString', () => {
    expect(TEMPLATE_CODE).not.toContain('toLocaleString()')
    expect(TEMPLATE_CODE).toContain('{{ fmtCount(r.samples) }}')
    expect(CODE_ONLY).toContain('function fmtCount(n: number): string')
  })

  it('successTone 与 successColor 同源阈值（0.85 / 0.7 是 5 档里的相邻档）', () => {
    expect(CODE_ONLY).toMatch(/function successTone\(rate: number\)[\s\S]*?rate >= 0\.85[\s\S]*?rate >= 0\.7/)
  })

  it('0 新增 i18n 键：视图只引用 correlations.* 的既有 6 个键', () => {
    const used = new Set([...CODE_ONLY.matchAll(/t\('([^']+)'/g)].map((m) => m[1]))
    expect([...used].sort()).toEqual([
      'correlations.sections.byModel',
      'correlations.sections.byStrategy',
      'correlations.sections.byTaskType',
      'correlations.sections.outlier',
      'correlations.sections.topModels',
      'correlations.title',
    ])
  })
})
