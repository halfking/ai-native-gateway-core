// ModelIntegrityView.responsive.test.ts — H6 第十八条垂直切片的门禁。
//
// 本页的形状（**首条按 13 §7 走 compact 连续加载的页**）：
// 1. **两个 Tab 互斥**（events / drift），各一张原生 `<table>`：T1 **8 列 · 桌面真分页**
//    （`limit` + `offset` 真进请求），T2 **6 列 · 不分页**（`loadDrift` 无 limit/offset）
//    ⇒ **只有 T1 需要连续加载**，T2 与切片十七同形（只改呈现形态）。
// 2. **`.table-wrap` 在本页同时是视觉框**（border + radius + background）⇒ 只能**摘 overflow**，
//    不能像切片十七那样整类删。门禁断「框还在」+「overflow 没了」两条。
// 3. **两张表桌面都有 tbody 内的 loading 行与 noData 行** ⇒ `:loading` / `:empty` 一律
//    带 `isCompact` 前置，且 `:empty` 要排除 busy 与 failed（13 §7「失败态不显示空态」）。
// 4. **`loadFirst()` 与 `refresh()` 不是一回事**：筛选条件变了要清空累积行，
//    用户点刷新要保旧刷新。门禁各断一条。
//
// 门禁清单：
// A. 桌面零回归：两 Tab、列头（8/6）、tbody 内 loading 与 noData 行、页码条与 disabled 规则、
//    badge 类名四档、request_id/actual_value 的截断、`.table-wrap` 视觉框保留
// B. 连续加载（13 §7）：compact 出 HyperLoadMore 而桌面不出、`pageSize` 同源、
//    `filterBody` 单一真源、筛选用 loadFirst / 刷新用 refresh、
//    ★ **加载下一页是「追加」不是「替换」**、失败态不显示空态
// C. compact 卡片：两段各出卡不出表、字段集（6/4）、卡头是**完整** request_id、
//    badge→tone 四档、status→tone 两档、actual_value 缺值出 —、`#actions` 打开详情
// D. 空态归属 + 跨层契约
import { flushPromises, enableAutoUnmount, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import ModelIntegrityView from './ModelIntegrityView.vue'
import HyperLoadMore from '../components/ui/HyperLoadMore.vue'
import { _resetForTests } from '../composables/useWindowClass'
import { _resetDataViewModeForTests } from '../composables/useDataViewMode'
import type {
  GetModelIntegrityDriftResponse,
  GetModelIntegrityParams,
  GetModelIntegrityResponse,
  GetModelIntegritySummaryResponse,
  ModelIntegrityRecord,
} from '../api/integrity'
import { resolveModelIntegrity } from '../api/integrity'

enableAutoUnmount(afterEach)

const SOURCE = readFileSync(resolve(process.cwd(), 'src/views/ModelIntegrityView.vue'), 'utf8')

/**
 * 保守版剥注释（与 CJK 计数器同口径）：块/HTML 注释整段去掉；行注释只去掉
 * trim 后整行以 `//` `*` `/*` 开头的。**不用「`//` 到行尾」那套正则** ——
 * 它会把反引号串里的 `//` 当成注释起点（切片十六自证时实测踩过）。
 */
const CODE_ONLY = SOURCE
  .replace(/\/\*[\s\S]*?\*\//g, '')
  .replace(/<!--[\s\S]*?-->/g, '')
  .split('\n')
  .filter((line) => !/^\s*(\/\/|\*|\/\*)/.test(line))
  .join('\n')

const RDV_TAGS = CODE_ONLY.match(/<ResponsiveDataView[\s\S]*?>/g) ?? []

// ── fixture ────────────────────────────────────────────────────────────────
// ★ severity 四档全覆盖 + 未登记值走 `severityClass` 的 default（`badge-low`）。
//   `resolved` 两档都要有。`request_id` 故意给一条**超长**的（桌面截断到 18）
//   和一条**缺值**的（桌面出 `—`，卡片头回落到 `id`）。
/**
 * ★ 第 1 页必须**装满 `COMPACT_PAGE_SIZE`（50）行**。
 *   第一版只给了 4 行 ⇒ `createHyperPages` 的短页判据
 *   `result.rows.length < opts.pageSize`（4 < 50）**正确地**认为「没有下一页」，
 *   `hasMore` 恒 false，「追加」那条用例自然测不了。
 *   ⇒ 这是 fixture 不真实，不是实现有 bug。`total = 57`（= 50 + 7）。
 */
function makePage(prefix: string, n: number, from: number): ModelIntegrityRecord[] {
  return Array.from({ length: n }, (_, i) => {
    const id = from + i
    return {
      id,
      request_id: `${prefix}-${id}`,
      provider_code: ['openai', 'anthropic', 'groq', 'cerebras'][i % 4],
      raw_model_name: `model-${id}`,
      anomaly_type: 'model_mismatch',
      severity: 'low',
      actual_value: `v${id}`,
      resolved: false,
      detected_at: '2026-10-06T00:00:00Z',
    }
  })
}

const page1 = makePage('req-p1', 50, 1)
// ★ 前 4 行换成有辨识度的边界值（其余 46 行是 filler，用来把第 1 页撑满）
Object.assign(page1[0], { request_id: 'req-0000000000000000000000000001-abcdef', severity: 'critical', actual_value: 'gpt-4o-mini-and-then-some-more', anomaly_type: 'model_mismatch' })
Object.assign(page1[1], { request_id: 'req-short', severity: 'high', actual_value: '', resolved: true, anomaly_type: 'finish_refusal' })
Object.assign(page1[2], { request_id: 'req-mid', severity: 'medium', actual_value: 'x', anomaly_type: 'token_arith_fail' })
Object.assign(page1[3], { severity: 'low', actual_value: 'y', anomaly_type: 'empty_response' })
delete (page1[3] as unknown as Record<string, unknown>).request_id // ★ 缺 request_id ⇒ 卡头回落到 id
// ★ 第 5 行补一条 `request_id: ''`（**空串**）。第一版只补了「字段缺失」（undefined），
//   于是 `recordTitle` 里的 `row.request_id === ''` 那半条分支**从未被喂到**
//   ⇒ 把整个空串判断删掉，输出逐字不变（台账 M39 绿）。空串与缺值是两种不同的输入。
Object.assign(page1[4], { request_id: '', severity: 'low', actual_value: 'z', anomaly_type: 'repeated_content' })

const page2 = makePage('req-p2', 7, 101)

const driftRows: ModelIntegrityRecord[] = [
  { id: 11, request_id: 'drift-req-0000000000000000000', provider_code: 'openai', raw_model_name: 'gpt-4o', anomaly_type: 'fingerprint_drift', severity: 'critical', actual_value: 'fp16-vs-fp32', resolved: false, detected_at: '2026-10-06T05:00:00Z' },
  { id: 12, provider_code: 'groq', raw_model_name: 'llama', anomaly_type: 'fingerprint_drift', severity: 'low', actual_value: '', resolved: false, detected_at: '2026-10-06T06:00:00Z' },
]

/** 总数 = 50 + 7 ⇒ `totalPages = 2`，桌面「下一页」可点（另有一条用例专门断末页禁用）。 */
const TOTAL = 57

const { ctrl } = vi.hoisted(() => ({
  ctrl: {
    /** 按 offset 返回哪一页。`emptyAll` 时返回空数组（模拟无数据）。 */
    total: 7,
    failEvents: false,
    /** 首屏第 2 页之后是否还有内容（默认有，够 `hasMore` 成立）。 */
    events: [] as unknown[],
    drift: [] as unknown[],
    /** 记录每次 events 请求的 limit/offset，用于断「同源」与「offset 递增」。 */
    calls: [] as Array<{ limit?: number; offset?: number; unresolved_only?: boolean }>,
    reset() {
      ctrl.total = 57
      ctrl.failEvents = false
      ctrl.events = []
      ctrl.drift = []
      ctrl.calls = []
    },
  },
}))

/**
 * ★ 基线实现**提成具名函数**（不内联在 `vi.mock` 里），因为
 *   `vi.clearAllMocks()` **只清调用记录、不清实现**：某条用例一旦用了
 *   `mockRejectedValue` / `mockResolvedValue`（**非** `…Once`），那个实现会
 *   **永久**留给它后面的每一条用例。
 *
 *   第一版就踩了：compact「失败态」那条把 `getModelIntegrityEvents` 打成永久 reject，
 *   于是它**之后**的 8 条 compact 用例全部拿到 0 行、集体报「`mi-events` 0 张卡」——
 *   症状看着像「compact 根本不出卡」，真因在门禁自己的装置。
 *   （判别：整组 compact 单独跑 7/9 绿，因为跳过那条就没有污染；
 *   而 **0 张卡**恰好是「请求失败」与「不出卡」共有的表象，不能只凭表象归因。）
 *
 *   修法：`beforeEach` 每条用例都把实现装回基线 ⇒ 用例之间互不污染。
 *   这条不变式由文件末尾「装置自证」那两条用例钉住。
 *
 * ⚠️ 四个实现必须是**函数声明**而不是 `const` 箭头：`vi.mock` 的工厂被提升到文件最上方，
 *   在那里 `const` 还在 TDZ 里（实测报 `Cannot access 'baseDriftImpl' before initialization`）。
 */
async function baseEventsImpl(params: GetModelIntegrityParams = {}) {
  ctrl.calls.push({ limit: params.limit as number, offset: params.offset as number, unresolved_only: params.unresolved_only as boolean })
  if (ctrl.failEvents) throw new Error('integrity boom')
  const offset = params.offset ?? 0
  const all = offset === 0 ? page1 : offset < 50 ? [] : page2
  return { events: all, count: ctrl.total, limit: params.limit ?? 0, offset } as GetModelIntegrityResponse
}
async function baseDriftImpl() {
  return { events: ctrl.drift as ModelIntegrityRecord[], count: ctrl.drift.length, days: 7 } as GetModelIntegrityDriftResponse
}
async function baseSummaryImpl() {
  return { summaries: [], count: 0, hours: 24 } as GetModelIntegritySummaryResponse
}
async function baseResolveImpl() {
  return { success: true, message: 'ok' } as Awaited<ReturnType<typeof resolveModelIntegrity>>
}

vi.mock('../api/integrity', async (orig) => {
  const actual = (await orig()) as Record<string, unknown>
  return {
    ...actual,
    getModelIntegrityEvents: vi.fn(baseEventsImpl),
    getModelIntegrityFingerprintDrift: vi.fn(baseDriftImpl),
    getModelIntegritySummary: vi.fn(baseSummaryImpl),
    resolveModelIntegrity: vi.fn(baseResolveImpl),
  }
})

/** 本页 `onMounted` 先问 `isSuperAdmin()`，为假就只渲染错误横幅、一个请求都不发。 */
vi.mock('../store', async (orig) => {
  const actual = (await orig()) as Record<string, unknown>
  return { ...actual, isSuperAdmin: vi.fn(() => true) }
})

/** 三个筛选器不是本切片的对象，桩掉以免它们各自去打接口。 */
vi.mock('../components/ProviderPicker.vue', () => ({
  default: { props: ['modelValue'], emits: ['update:modelValue'], template: '<div class="provider-picker-stub" />' },
}))
vi.mock('../components/ModelPicker.vue', () => ({
  default: { props: ['modelValue'], emits: ['update:modelValue'], template: '<div class="model-picker-stub" />' },
}))
vi.mock('../components/AnomalyTypePicker.vue', () => ({
  default: { props: ['modelValue', 'options'], emits: ['update:modelValue'], template: '<div class="anomaly-picker-stub" />' },
}))

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  fallbackWarn: false,
  missingWarn: false,
  messages: {
    'zh-CN': {
      modelIntegrityView: {
        pageTitle: '模型完整性',
        pageSubtitle: '说明',
        filter: {
          provider: '供应商', providerPlaceholder: '选供应商', model: '模型', modelPlaceholder: '选模型',
          anomalyType: '异常类型', anomalyTypePlaceholder: '选类型', severity: '严重度', all: '全部',
          unresolvedOnly: '只看未解决', query: '查询', refresh: '刷新',
        },
        severity: { low: '低', medium: '中', high: '高', critical: '严重' },
        anomalyType: {
          all: '全部', model_mismatch: '模型不匹配', finish_refusal: '拒绝结束',
          finish_truncation: '截断', token_arith_fail: '算术错', empty_response: '空响应',
          fingerprint_drift: '指纹漂移', repeated_content: '重复内容',
        },
        anomalyTypeDescription: {
          model_mismatch: '模型不匹配', finish_refusal: '拒绝结束', finish_truncation: '截断',
          token_arith_fail: '算术错', empty_response: '空响应', fingerprint_drift: '指纹漂移', repeated_content: '重复内容',
        },
        table: {
          detectedAt: '发现时间', severity: '严重度', anomalyType: '异常类型', providerModel: '供应商/模型',
          requestId: '请求 ID', actual: '实际值', status: '状态', viewDetail: '查看详情',
          loading: '加载中…', noData: '暂无记录',
        },
        pager: { prev: '上一页', next: '下一页', summary: '第 {page} / {totalPages} 页，共 {total} 条' },
        drift: { days: '天数', query: '查询', noData: '暂无漂移' },
        detail: {
          title: '详情', close: '关闭', requestId: '请求 ID', detectedAt: '发现时间', provider: '供应商',
          model: '模型', outboundModel: '出站模型', actual: '实际值', expected: '期望值', sample: '样本',
          context: '上下文', credential: '凭据', resolutionNotes: '处理备注', resolutionNotesPlaceholder: '写备注',
          markResolved: '标记已解决', noNotes: '无备注', resolutionInfo: '处理信息', processing: '处理中…',
        },
        error: { loadFailed: '加载失败', summaryLoadFailed: '汇总失败', driftLoadFailed: '漂移加载失败', markFailed: '标记失败', needSuperAdmin: '需要超级管理员' },
        status: { resolved: '已解决', unresolved: '未解决' },
        summary: { window: '窗口', anomalies: '异常数', resolved: '已解决', unresolved: '未解决' },
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
  const w = mount(ModelIntegrityView, { global: { plugins: [i18n] } })
  await flushPromises()
  await flushPromises()
  return w
}

type W = Awaited<ReturnType<typeof factory>>

const SECTIONS = ['mi-events', 'mi-drift'] as const
type SectionId = (typeof SECTIONS)[number]

function sectionOf(w: W, id: SectionId) {
  const host = w.find(`[data-testid="${id}"]`)
  expect(host.exists(), `找不到容器 ${id}`).toBe(true)
  return host!
}

function cardsOf(w: W, id: SectionId) {
  return sectionOf(w, id).findAll('[data-testid="card-list"] .card')
}

function fieldLabels(card: ReturnType<typeof cardsOf>[number]) {
  return card.findAll('.card__field').map((f) => f.find('dt').text())
}

function fieldValue(w: W, id: SectionId, index: number, label: string) {
  const card = cardsOf(w, id)[index]
  expect(card, `${id} 第 ${index} 张卡不存在（共 ${cardsOf(w, id).length} 张）`).toBeTruthy()
  const hit = card!.findAll('.card__field').find((f) => f.find('dt').text() === label)
  expect(hit, `${id} 第 ${index} 张卡里找不到字段「${label}」，实际有：${fieldLabels(card!).join('/')}`).toBeTruthy()
  return hit!.find('dd')
}

/** 可手动兑现的 promise，用来把「请求进行中」那个瞬间**按住**再断言。 */
function deferred<T>() {
  let resolve!: (v: T) => void
  const promise = new Promise<T>((r) => { resolve = r })
  return { promise, resolve }
}

async function switchToDrift(w: W) {  const tabs = w.findAll('.tab-btn, .tabs button, button').filter((b) => b.text().includes('漂移'))
  // drift Tab 的文案键是 modelIntegrityView.tabs.drift（不在上面的 messages 里），
  // 所以按 class 找 tab 按钮组；找不到就点最后一个（events / drift 两个）。
  const group = w.findAll('.tabs .tab, .tab-list button, nav button')
  const target = group.length >= 2 ? group[1] : tabs[0]
  expect(target, '找不到 drift Tab 按钮').toBeTruthy()
  await target!.trigger('click')
  await flushPromises()
}

beforeEach(async () => {
  _resetDataViewModeForTests()
  ctrl.reset()
  ctrl.drift = driftRows
  mockWindowClass('expanded')
  localStorage.clear()
  // ★ 把 mock 实现装回基线（见 baseEventsImpl 上方注释）：`clearAllMocks` 不做这件事，
  //   少了这三行，任意一条用了 `mockRejectedValue` 的用例都会污染其后所有用例。
  const api = await import('../api/integrity')
  vi.mocked(api.getModelIntegrityEvents).mockImplementation(baseEventsImpl)
  vi.mocked(api.getModelIntegrityFingerprintDrift).mockImplementation(baseDriftImpl)
  vi.mocked(api.getModelIntegritySummary).mockImplementation(baseSummaryImpl)
  vi.mocked(api.resolveModelIntegrity).mockImplementation(baseResolveImpl)
})

afterEach(() => vi.clearAllMocks())

// ─────────────────────────────────────────────────────────────────────────────
describe('ModelIntegrityView：桌面两张表零回归', () => {
  it('默认只出 events Tab 一张表', async () => {
    const w = await factory()
    expect(w.findAll('.table')).toHaveLength(1)
    expect(w.find('[data-testid="mi-events"]').exists()).toBe(true)
    expect(w.find('[data-testid="mi-drift"]').exists()).toBe(false)
  })

  it('T1 events 8 列，列头逐字未变', async () => {
    const w = await factory()
    expect(w.findAll('.table')[0]!.findAll('th').map((h) => h.text())).toEqual([
      // ★ 第 8 列是**操作列、`<th></th>` 没有表头**（桌面原本就是这样）
      '发现时间', '严重度', '异常类型', '供应商/模型', '请求 ID', '实际值', '状态', '',
    ])
  })

  it('两张表桌面都有 tbody 内的 loading 行与 noData 行（我们的 EmptyState 不参与）', async () => {
    const w = await factory()
    const cells = w.findAll('.table')[0]!.findAll('td.empty')
    // 首屏加载已完成 ⇒ 此刻应是 noData 行而不是 loading 行
    expect(cells).toHaveLength(0)
    expect(w.findAll('.table')[0]!.findAll('tbody tr')).toHaveLength(page1.length)
    expect(w.find('.app-empty-state').exists()).toBe(false)
  })

  it('★ 桌面「空」时仍出 tbody 内的 noData 行（不是我们的 EmptyState）', async () => {
    const { getModelIntegrityEvents } = await import('../api/integrity')
    vi.mocked(getModelIntegrityEvents).mockResolvedValueOnce({ events: [], count: 0, limit: 50, offset: 0 } as never)
    const w = await factory()
    expect(w.find('.table')).toBeDefined()
    const noData = w.findAll('.table')[0]!.findAll('td.empty')
    expect(noData).toHaveLength(1)
    expect(noData[0]!.text()).toBe('暂无记录')
    expect(w.find('.app-empty-state').exists()).toBe(false)
  })

  it('★ `.table-wrap` 的视觉框保留（border / radius / background），但 overflow 已被摘掉', () => {
    // ★ 这是本切片与切片十七的**关键差异**：那边 `.table-wrap` 只有 overflow 可以整类删，
    //   这边它同时是视觉框 ⇒ 整类删会把桌面圆角边框一起删掉 = 桌面视觉变更。
    const m = /^\.table-wrap\s*\{([^}]*)\}/m.exec(SOURCE)
    expect(m, '找不到 .table-wrap 规则').toBeTruthy()
    const body = m![1]!
    // ⚠️ `expect(body).toMatch(/border:/)` 是**假牙**：把它改成 `border: none;`
    //   照样匹配（台账第一轮 M45 绿）。⇒ 必须断「有宽度的 border」，不是「有 border 这个词」。
    expect(body, '边框必须是非 none / 非 0 的实线').toMatch(/border:\s*(?!none\b|0\b)\S/)
    expect(body).toMatch(/border-radius:/)
    expect(body).toMatch(/background:/)
    expect(body, 'overflow 必须摘掉，否则与容器嵌套出双横滚').not.toMatch(/overflow/)
  })

  it('页码条在桌面可见，且 prev/next 的 disabled 规则未变', async () => {
    const w = await factory()
    const pager = w.find('.pager')
    expect(pager.exists()).toBe(true)
    const btns = pager.findAll('button')
    expect(btns.map((b) => b.text())).toEqual(['上一页', '下一页'])
    expect(btns[0]!.attributes('disabled'), '第 1 页时「上一页」应禁用').toBeDefined()
    // total 57 / pageSize 50 ⇒ totalPages 2 ⇒ 第 1 页时「下一页」**可点**
    expect(btns[1]!.attributes('disabled'), '第 1 / 2 页时「下一页」应可点').toBeUndefined()
    expect(pager.find('span').text()).toContain('第 1 / 2 页')
  })

  it('severity 四档 badge 类名逐档不同（未登记值走 default = badge-low）', async () => {
    const w = await factory()
    // ★ 只看前 4 行：第 5～50 行是 filler（severity 恒 low），全量比对会把
    //   「四档覆盖」这条判据淹在 46 个同值里 —— 判据要对着**有辨识度**的那几行。
    const rows = w.findAll('.table')[0]!.findAll('tbody tr').slice(0, 4)
    const badges = rows.map((r) => r.findAll('td')[1]!.find('.badge').text())
    expect(badges).toEqual(['严重', '高', '中', '低'])
    const classes = rows.map((r) => {
      const c = r.findAll('td')[1]!.find('.badge').classes()
      return ['badge-critical', 'badge-high', 'badge-medium', 'badge-low'].find((k) => c.includes(k))
    })
    expect(classes).toEqual(['badge-critical', 'badge-high', 'badge-medium', 'badge-low'])
  })

  it('状态列的两档 class：已解决 status-ok / 未解决 status-warn', async () => {
    const w = await factory()
    const cells = w.findAll('.table')[0]!.findAll('tbody tr').slice(0, 4).map((r) => {
      // ★ class 在内层 `<span>` 上，不在 `<td>` 上 —— 第一版查了 td，红的是判据
      const span = r.findAll('td')[6]!.find('span')!
      return `${span.text()}:${['status-ok', 'status-warn'].find((k) => span.classes().includes(k))}`
    })
    expect(cells).toEqual(['未解决:status-warn', '已解决:status-ok', '未解决:status-warn', '未解决:status-warn'])
  })

  it('★ 桌面把 request_id 截断到 18 字符、actual_value 截断到 20（卡片另说）', async () => {
    const w = await factory()
    const rows = w.findAll('.table')[0]!.findAll('tbody tr')
    const req = rows.map((r) => r.findAll('td')[4]!.text())
    expect(req[0]).toBe('req-00000000000000...')
    expect(req[0]!.length).toBe(21) // 18 + '...'
    expect(req[1]).toBe('req-short') // 不超长则不截断
    const act = rows.map((r) => r.findAll('td')[5]!.text())
    expect(act[0]).toBe('gpt-4o-mini-and-then...')
    expect(act[1]).toBe('—') // actual_value 为空串 ⇒ 桌面 `truncate` 出破折号
  })
})

// ─────────────────────────────────────────────────────────────────────────────
describe('ModelIntegrityView：连续加载契约（13 §7）', () => {
  it('compact 出 HyperLoadMore，桌面不出', async () => {
    const w = await factory()
    expect(w.find('.hyper-load-more').exists()).toBe(false)
    mockWindowClass('compact')
    const w2 = await factory()
    expect(w2.find('.hyper-load-more').exists()).toBe(true)
  })

  it('★ `pageSize` 没有写入方 ⇒ `COMPACT_PAGE_SIZE = 50` 与它同源', () => {
    // 若哪天本页加了「每页条数」选择器，这条会红 —— 那时两条路径会发出不同的 limit，
    // 短页判据读的是配置里那个 ⇒ 长列表**静默截断**（13 §7）。
    expect(CODE_ONLY, '`pageSize` 出现了写入方').not.toMatch(/pageSize\.value\s*=/)
    expect(CODE_ONLY).toContain('const pageSize = ref(50)')
    expect(CODE_ONLY).toContain('const COMPACT_PAGE_SIZE = 50')
    // 且 createHyperPages 的 pageSize 用的就是它
    expect(CODE_ONLY).toMatch(/pageSize:\s*COMPACT_PAGE_SIZE/)
    // fetchPage 发出的 limit 也必须是它（同一个数，不是另一个字面量）
    const fetchBlock = CODE_ONLY.slice(CODE_ONLY.indexOf('fetchPage:'), CODE_ONLY.indexOf('fetchPage:') + 400)
    expect(fetchBlock).toContain('limit: COMPACT_PAGE_SIZE')
  })

  it('★ 筛选条件只有**一份真源**（filterBody 被两条路径共用，不是写两遍）', () => {
    expect(CODE_ONLY.match(/function filterBody\(\)/g)).toHaveLength(1)
    // 桌面 load() 用它
    const loadBlock = CODE_ONLY.slice(CODE_ONLY.indexOf('async function load()'), CODE_ONLY.indexOf('async function loadSummary()'))
    expect(loadBlock).toContain('...filterBody()')
    // compact fetchPage 也用它
    expect(CODE_ONLY.slice(CODE_ONLY.indexOf('fetchPage:'), CODE_ONLY.indexOf('fetchPage:') + 500)).toContain('...filterBody()')
  })

  it('★ 筛选条件变了用 `loadFirst()`（清空累积行），用户点刷新用 `refresh()`（保旧刷新）', () => {
    const applyBlock = CODE_ONLY.slice(CODE_ONLY.indexOf('function applyFilters()'), CODE_ONLY.indexOf('function prevPage()'))
    expect(applyBlock, 'applyFilters 必须走 loadFirst').toContain('continuous.loadFirst()')
    expect(applyBlock, 'applyFilters 不该用 refresh（那会留下不匹配新条件的旧行）').not.toContain('continuous.refresh()')

    const refreshBlock = CODE_ONLY.slice(CODE_ONLY.indexOf('async function refreshAll()'), CODE_ONLY.indexOf('function openDetail('))
    expect(refreshBlock, 'refreshAll 必须走 refresh').toContain('continuous.refresh()')
  })

  it('★ 加载下一页是「追加」不是「替换」—— 这是连续加载的全部意义', async () => {
    mockWindowClass('compact')
    const w = await factory()
    expect(cardsOf(w, 'mi-events')).toHaveLength(50)
    expect(w.find('.hyper-load-more').exists()).toBe(true)
    // ★ 不去点按钮：`HyperLoadMore` 在 `autoTriggerEnabled`（IntersectionObserver 可用）
    //   时渲染的是 **sentinel 哨兵**而不是按钮 —— jsdom 提供了 observer，所以走的是哨兵支路。
    //   这里要验的是**页面的接线**（`@load-more="continuous.loadNext()"`），直接发组件事件。
    w.findComponent(HyperLoadMore).vm.$emit('load-more')
    await flushPromises()
    await flushPromises()
    // ★ 第 1 页的 50 行**一个不少**，第 2 页的 7 行被**追加**进来 ⇒ 共 57 张
    const cards = cardsOf(w, 'mi-events')
    expect(cards).toHaveLength(57)
    expect(cards[0]!.attributes('data-title'), '第 1 页首行必须还在（不是被替换）').toBe('req-0000000000000000000000000001-abcdef')
    expect(cards[49]!.attributes('data-title')).toBe('req-p1-50')
    expect(cards[50]!.attributes('data-title'), '追加的第 2 页首行').toBe('req-p2-101')
    expect(cards[56]!.attributes('data-title')).toBe('req-p2-107')
    // offset 递增过（证明真的发了第 2 页请求，而不是本地切片）
    expect(ctrl.calls.map((c) => c.offset)).toEqual([0, 50])
  })

  /**
   * ★ 忙碌态的两个方向都要断。
   *
   * `createHyperPages` 里 `loadFirst` 走 `refreshing`、`loadNext` 走 `loadingNext`，
   * 而 `compactBusy` **只认 `refreshing`** —— 于是：
   *  - 首屏取第 1 页：出忙碌态（`compactBusy` 为真）；
   *  - 追加下一页：**不出**忙碌态、已加载的卡**原样留在屏幕上**（保旧，不是清空重来）。
   *
   * 第一版只写了两句「compact 出 50 张卡」，把 `compactBusy` 改成 `loadingNext`
   * 输出逐字不变（台账 M19 绿）—— 因为那时候根本没断言过忙碌态。
   */
  it('★ 首屏取数出忙碌态；追加下一页**不出**忙碌态且旧卡留在屏幕上', async () => {
    mockWindowClass('compact')
    const { getModelIntegrityEvents } = await import('../api/integrity')
    const d = deferred<never>()
    vi.mocked(getModelIntegrityEvents).mockImplementation(() => d.promise as Promise<never>)
    const w = await factory()
    // 首屏请求还挂着 ⇒ 列表区是忙碌态，0 张卡。
    // ★ 忙碌标记是 `.responsive-data-view__state`（`ResponsiveDataView` 在 loading 时
    //   **整个不渲染** CardList，连 `card-list__state` 都不存在）—— 第一版查错了元素。
    expect(sectionOf(w, 'mi-events').find('.responsive-data-view__state').exists(), '首屏取数期间应出忙碌态').toBe(true)
    expect(cardsOf(w, 'mi-events')).toHaveLength(0)

    // 兑现 ⇒ 数据到位
    d.resolve({ events: page1, count: TOTAL, limit: 50, offset: 0 } as never)
    await flushPromises()
    await flushPromises()
    expect(cardsOf(w, 'mi-events')).toHaveLength(page1.length)
    expect(sectionOf(w, 'mi-events').find('.responsive-data-view__state').exists()).toBe(false)

    // ★ 追加下一页期间：第 2 页的 promise 也按住，验证「保旧」而不是清空
    const d2 = deferred<never>()
    vi.mocked(getModelIntegrityEvents).mockImplementation((p) => {
      const offset = (p as { offset?: number }).offset ?? 0
      ctrl.calls.push({ limit: (p as { limit?: number }).limit, offset })
      return offset === 50
        ? (d2.promise as Promise<never>)
        : Promise.resolve({ events: page1, count: TOTAL, limit: 50, offset: 0 } as never)
    })
    w.findComponent(HyperLoadMore).vm.$emit('load-more')
    await flushPromises()
    expect(sectionOf(w, 'mi-events').find('.responsive-data-view__state').exists(), '追加时不该把已加载的内容换成忙碌态').toBe(false)
    expect(cardsOf(w, 'mi-events'), '追加期间已加载的卡必须留在屏幕上').toHaveLength(page1.length)
    d2.resolve({ events: page2, count: TOTAL, limit: 50, offset: 50 } as never)
    await flushPromises()
    await flushPromises()
    expect(cardsOf(w, 'mi-events')).toHaveLength(TOTAL)
  })

  /**
   * ★ 重试打回**失败的那一页**，不是重取第 1 页。
   *
   * 第一版只在**第 1 页**失败时点重试 —— 那一页 `retry()` 与 `loadFirst()` 的请求
   * 完全一样（都是 offset 0），所以把 `@retry` 接到 `loadFirst` 上输出逐字不变
   * （台账 M16 绿）。⇒ 必须让**第 2 页**失败，两条路才分得开。
   */
  it('★ 第 2 页失败后点重试，打回的还是第 2 页（offset 50），不是第 1 页', async () => {
    mockWindowClass('compact')
    const { getModelIntegrityEvents } = await import('../api/integrity')
    let failNext = true
    vi.mocked(getModelIntegrityEvents).mockImplementation(async (p) => {
      const offset = (p as { offset?: number }).offset ?? 0
      ctrl.calls.push({ limit: (p as { limit?: number }).limit, offset })
      if (offset >= 50 && failNext) throw new Error('page2 boom')
      return { events: offset === 0 ? page1 : page2, count: TOTAL, limit: 50, offset } as never
    })
    const w = await factory()
    expect(cardsOf(w, 'mi-events')).toHaveLength(page1.length)

    // 让第 2 页失败
    w.findComponent(HyperLoadMore).vm.$emit('load-more')
    await flushPromises()
    await flushPromises()
    expect(ctrl.calls.map((c) => c.offset)).toEqual([0, 50])
    expect(w.find('.hyper-load-more__failed').exists()).toBe(true)
    // 失败时已加载的 50 张卡**仍在**（失败不是清空）
    expect(cardsOf(w, 'mi-events'), '加载更多失败不得清空已加载的行').toHaveLength(page1.length)

    // 修好服务端，点重试
    failNext = false
    ctrl.calls = []
    await w.find('.hyper-load-more__failed .hyper-load-more__btn').trigger('click')
    await flushPromises()
    await flushPromises()
    expect(ctrl.calls.map((c) => c.offset), '重试必须打回失败的那一页（第 2 页）').toEqual([50])
    expect(cardsOf(w, 'mi-events')).toHaveLength(TOTAL)
  })

  it('★ 失败态不显示空态（13 §7）—— compact 请求失败时既不出 EmptyState 也不出卡', async () => {    mockWindowClass('compact')
    const { getModelIntegrityEvents } = await import('../api/integrity')
    vi.mocked(getModelIntegrityEvents).mockRejectedValue(new Error('integrity boom'))
    const w = await factory()
    const host = sectionOf(w, 'mi-events')
    // ★ 13 §7 的字面要求是「失败态**不显示空态**」—— 断的就是这一条
    expect(host.find('.app-empty-state').exists(), '失败态不得显示空态').toBe(false)
    // ⚠️ 这里**会**有一个空的 `CardList`（`ResponsiveDataView` 的 `empty=false` ⇒ 走 `v-else`
    //   ⇒ 渲染 CardList，0 行）。这是组件的既定行为（切片十一同款），不是缺陷 ——
    //   所以第一版我断言「连 card-list 都不该有」是**判据写过头了**，红的是判据。
    expect(host.findAll('.card')).toHaveLength(0)
    // 但失败必须**可见** —— HyperLoadMore 出 failed 分支与重试按钮
    const more = w.find('.hyper-load-more')
    expect(more.find('.hyper-load-more__failed').exists(), '失败态必须出重试入口').toBe(true)
    expect(more.find('.hyper-load-more__btn').exists(), 'failed 分支应带重试按钮').toBe(true)
  })

  it('筛选变化在 compact 下真的重取第 1 页（loadFirst），且 offset 归零', async () => {
    mockWindowClass('compact')
    const w = await factory()
    ctrl.calls = []
    const query = w.findAll('button').find((b) => b.text() === '查询')!
    await query.trigger('click')
    await flushPromises()
    await flushPromises()
    expect(ctrl.calls[0]!.offset, '筛选后必须从第 1 页重新取').toBe(0)
    // 且累积行被清空重取 ⇒ 仍是 50 张卡（不是 57 张）
    expect(cardsOf(w, 'mi-events')).toHaveLength(page1.length)
  })
})

// ─────────────────────────────────────────────────────────────────────────────
describe('ModelIntegrityView：compact 卡片形态', () => {
  beforeEach(() => mockWindowClass('compact'))

  it('T1 出 4 张卡不出表；切到 drift Tab 出 2 张卡', async () => {
    const w = await factory()
    expect(cardsOf(w, 'mi-events')).toHaveLength(50)
    expect(w.findAll('.table')).toHaveLength(0)
    expect(w.find('.responsive-data-view__switch').exists()).toBe(false)

    await switchToDrift(w)
    expect(cardsOf(w, 'mi-drift')).toHaveLength(2)
  })

  /** 字段集与桌面列一一对应：扣掉 request_id（卡头）与操作列。 */
  it('字段集与桌面列一一对应（T1 六 / T2 四）', async () => {
    const w = await factory()
    expect(fieldLabels(cardsOf(w, 'mi-events')[0]!)).toEqual([
      '发现时间', '严重度', '异常类型', '供应商/模型', '实际值', '状态',
    ])
    await switchToDrift(w)
    expect(fieldLabels(cardsOf(w, 'mi-drift')[0]!)).toEqual(['发现时间', '严重度', '供应商/模型', '实际值'])
  })

  /**
   * ★ 卡头出**完整** `request_id`，不跟桌面一起截断到 18。
   *   桌面的截断是为窄列做的；卡片头承担「唯一句柄」职责。
   *   另外 `request_id` **可选** ⇒ 第 4 张卡（缺 request_id）回落到 `id`。
   */
  it('★ 卡头是完整 request_id（不截断）；缺值与空串都回落到 id', async () => {
    const w = await factory()
    // ★ 只断言前 5 张（有辨识度的那几张），不是全部 50 张。
    //   第 4 张**缺字段**（undefined）、第 5 张是**空串** —— 两种不同输入都要回落。
    expect(cardsOf(w, 'mi-events').slice(0, 5).map((c) => c.attributes('data-title'))).toEqual([
      'req-0000000000000000000000000001-abcdef', 'req-short', 'req-mid', '4', '5',
    ])
  })

  it('actual_value 缺值出 `—`，超长则按桌面的 20 字符截断（普通字段与桌面同口径）', async () => {
    const w = await factory()
    expect(fieldValue(w, 'mi-events', 0, '实际值').text()).toBe('gpt-4o-mini-and-then...')
    expect(fieldValue(w, 'mi-events', 1, '实际值').text()).toBe('—')
  })

  it('★ T2 的 `actual_value` 按**它自己那张桌面的 24** 截断（不是 T1 的 20）', async () => {
    // 第一版两张表都写了 20 ⇒ T2 桌面是 24、卡片是 20，同页同字段两个口径。
    // 判据**从 T2 那段桌面模板取数**而不是抄字面量，否则改坏了照样绿。
    const driftTpl = CODE_ONLY.slice(CODE_ONLY.indexOf('data-testid="mi-drift"'))
    const desktopN = Number(/truncate\(item\.actual_value,\s*(\d+)\)/.exec(driftTpl)?.[1])
    expect(desktopN, '桌面 T2 的 actual_value 截断长度取不到').toBe(24)
    // 卡片侧的第一条 actual_value 是 'fp16-vs-fp32'（12 字符，不触发截断）⇒
    // 换成 30 字符的值，验证确实按 24 截断。
    ctrl.drift = [{ ...driftRows[0]!, actual_value: 'd'.repeat(30) }]
    const w = await factory()
    await switchToDrift(w)
    expect(fieldValue(w, 'mi-drift', 0, '实际值').text()).toBe(`${'d'.repeat(desktopN)}...`)
  })

  it('★ badge→tone 四档：critical→danger、high→warn、medium→warn、low→neutral', async () => {
    const w = await factory()
    const tone = (i: number) => fieldValue(w, 'mi-events', i, '严重度').attributes('data-tone')
    expect(tone(0)).toBe('danger')
    expect(tone(1)).toBe('warn')
    expect(tone(2)).toBe('warn')
    // ★ high 与 medium 在桌面上只差背景、文字色同源 ⇒ 卡片侧收敛成同一档
    expect(tone(3)).toBe('neutral')
  })

  it('★ 状态→tone 两档：已解决→good、未解决→warn', async () => {
    const w = await factory()
    expect(fieldValue(w, 'mi-events', 0, '状态').attributes('data-tone')).toBe('warn')
    expect(fieldValue(w, 'mi-events', 1, '状态').attributes('data-tone')).toBe('good')
    expect(fieldValue(w, 'mi-events', 1, '状态').text()).toBe('已解决')
  })

  it('严重度与异常类型的译名与桌面同源（未登记值回落原值）', async () => {
    const w = await factory()
    // ★ 严重度的**译名**也要断：第一版只断了它的 `data-tone`，
    //   于是把 `format` 整个删掉（卡片出裸枚举 'critical'）照样绿（台账 M37）。
    //   同一个字段的两条性质（色 / 文本）只断一条 = 没断另一条。
    expect([0, 1, 2, 3].map((i) => fieldValue(w, 'mi-events', i, '严重度').text()))
      .toEqual(['严重', '高', '中', '低'])
    expect(fieldValue(w, 'mi-events', 0, '异常类型').text()).toBe('模型不匹配')
    expect(fieldValue(w, 'mi-events', 3, '异常类型').text()).toBe('空响应')
  })

  it('`#actions` 给出「查看详情」，点了真的打开详情面板', async () => {
    const w = await factory()
    const btn = cardsOf(w, 'mi-events')[0]!.find('.card__actions button')!
    expect(btn.text()).toBe('查看详情')
    expect(w.find('.modal-mask').exists()).toBe(false)
    await btn.trigger('click')
    await flushPromises()
    expect(w.find('.modal-mask').exists()).toBe(true)
  })

  it('两张表的 `:loading` 与 `:empty` **各自**都带 isCompact 前置（桌面有自己的 tbody 内三态行）', () => {
    expect(RDV_TAGS).toHaveLength(2)
    // ⚠️ 第一版这里用的是 `expect(tag).toMatch(/:(loading|empty)="[^"]*isCompact/)` ——
    //   它只要求「loading **或** empty 里有一个」带前置。实测把 `:loading` 的前置去掉
    //   （台账 M21）照样绿，因为同一个标签上的 `:empty` 还带着 isCompact。
    //   ⇒ **逐 prop 抽出来断**，别在整块标签上做「或」匹配。
    for (const tag of RDV_TAGS) {
      for (const prop of ['loading', 'empty'] as const) {
        const m = new RegExp(`:${prop}="([^"]*)"`).exec(tag)
        expect(m, `${tag} 上找不到 :${prop}`).toBeTruthy()
        expect(m![1], `${tag} 的 :${prop} 丢了 isCompact 前置`).toMatch(/isCompact/)
      }
    }
    // T1 的 :empty 必须排除 busy 与 failed（失败态不显示空态）
    const t1 = RDV_TAGS.find((t) => t.includes('mi-events'))!
    expect(t1).toContain('!compactBusy')
    expect(t1).toContain('!compactFailed')
  })

  it('★ `:has-more` 真的接在 `continuous.hasMore` 上', () => {
    // 这条是**源码级**接线断言，不是行为断言。理由（诚实记录）：
    //   `HyperLoadMore` 只在 `canTrigger` 里读 `hasMore`（自动触发那一路），
    //   而本门禁是 `vm.$emit('load-more')` **直接发组件事件**驱动追加的 ——
    //   绕过了它唯一的作用域。要在 jsdom 里造 IntersectionObserver 命中才能测行为，
    //   代价远大于收益。⇒ 断「这一根线没接错」而不是断「它生效了」。
    const more = CODE_ONLY.slice(CODE_ONLY.indexOf('<HyperLoadMore'), CODE_ONLY.indexOf('/>', CODE_ONLY.indexOf('<HyperLoadMore')) + 2)
    expect(more).toContain(':has-more="continuous.hasMore.value"')
    expect(more).toContain(':state="continuous.state.value"')
    expect(more).toContain(':loaded-count="continuous.loadedCount.value"')
  })
})

// ─────────────────────────────────────────────────────────────────────────────
describe('ModelIntegrityView：桌面专属元素在 compact 下必须缺席', () => {
  it('★ compact 不出页码条（否则点「下一页」页面纹丝不动）', async () => {
    mockWindowClass('compact')
    const w = await factory()
    expect(w.find('.pager').exists(), 'compact 下页码条必须缺席（它的 nextPage 改的是连续加载读不到的那个 page）').toBe(false)
    expect(w.find('.hyper-load-more').exists()).toBe(true)
  })

  it('两条 tbody 空态行的 colspan 仍是 8 / 6 —— loading 行与 noData 行**都要**断', async () => {
    // 第一版只断 noData 行，于是把 **loading 行**的 colspan 改成 6 输出逐字不变
    // （台账第一轮 M48 绿：变异打偏了，锚点命中的是另一行）。
    // ⇒ 两行各断一次；台账侧也拆成两条变异（M48 / M51）各打一行。
    const { getModelIntegrityEvents } = await import('../api/integrity')
    const d = deferred<never>()
    vi.mocked(getModelIntegrityEvents).mockImplementation(() => d.promise as Promise<never>)
    ctrl.drift = []
    const w = await factory()
    // 请求挂着 ⇒ 出的是 **loading 行**。
    // ★ 文案按**真实 zh-CN** 断：实测 `src/i18n` 的全局实例生效了，
    //   `modelIntegrityView.table.*` 解析出的不是我上面那份内联桩。
    //   这里只断「是 loading 行不是 noData 行」，所以用 `toContain` 而非全等
    //   （实测原文带省略号 `加载中…`，锁死标点会在改文案时误报）。
    expect(w.findAll('.table')[0]!.find('td.empty').text()).toContain('加载')
    expect(w.findAll('.table')[0]!.find('td.empty').attributes('colspan'), 'T1 loading 行 colspan').toBe('8')

    // 兑现成空结果 ⇒ 换成 **noData 行**
    vi.mocked(getModelIntegrityEvents).mockResolvedValue({ events: [], count: 0, limit: 50, offset: 0 } as never)
    d.resolve(undefined as never)
    await flushPromises()
    await flushPromises()
    expect(w.findAll('.table')[0]!.find('td.empty').text()).toBe('暂无记录')
    expect(w.findAll('.table')[0]!.find('td.empty').attributes('colspan'), 'T1 noData 行 colspan').toBe('8')
    await switchToDrift(w)
    expect(w.findAll('.table')[0]!.find('td.empty').attributes('colspan'), 'T2 noData 行 colspan').toBe('6')
  })
})

// ─────────────────────────────────────────────────────────────────────────────
describe('ModelIntegrityView：空态归属与跨层契约', () => {
  it('compact 首屏空：出 EmptyState（复用既有关键词条）', async () => {
    mockWindowClass('compact')
    const { getModelIntegrityEvents } = await import('../api/integrity')
    vi.mocked(getModelIntegrityEvents).mockResolvedValue({ events: [], count: 0, limit: 50, offset: 0 } as never)
    const w = await factory()
    const host = sectionOf(w, 'mi-events')
    expect(host.find('.app-empty-state').text()).toContain('暂无记录')
    expect(host.find('[data-testid="card-list"]').exists()).toBe(false)
  })

  it('★ 本页**不传** `table-min-width`（直接子元素是 `.table-wrap`，`:slotted(table)` 不命中）', async () => {
    for (const tag of RDV_TAGS) expect(tag, `不该传 table-min-width（是空操作）：${tag}`).not.toContain('table-min-width')
    // ★ 前提证据：容器的直接子元素确实是 div 而**不是** table。
    //   第一版我断言「直接子元素是 table」，结果实测是 `['table']` ——
    //   因为我在改 CSS 的同时把 `.table-wrap` 那个 div 从模板里**删掉了**，
    //   桌面视觉框（border/radius/background）一起没了。**红的是我的实现，不是判据。**
    const w = await factory()
    const host = w.find('.responsive-data-view__table')
    expect([...host.element.children].map((c) => c.tagName.toLowerCase())).toEqual(['div'])
    expect(host.element.children[0]!.className).toContain('table-wrap')
    expect(host.find('table.table').exists()).toBe(true)
  })

  it('title-key 用 id（`request_id` 可选且桌面截断到 18，拿它当键会退化）', () => {
    expect(CODE_ONLY.match(/title-key="[^"]*"/g)).toEqual(Array<string>(2).fill('title-key="id"'))
    expect(CODE_ONLY).toContain('rowKey: (r) => r.id')
  })

  it('0 新增 i18n 键：只引用 modelIntegrityView.* 的既有键', () => {
    const used = new Set([...CODE_ONLY.matchAll(/t\('([^']+)'/g)].map((m) => m[1]))
    for (const k of used) expect(/^modelIntegrityView\./.test(k), `预期词条前缀之外的键：${k}`).toBe(true)
  })

  it('badge→tone 映射表与 `<style>` 的实际色值一致（不是抄一遍）', () => {
    const TABLE: Record<string, string> = {
      'badge-critical': 'danger',
      'badge-high': 'warn',
      'badge-medium': 'warn',
      'badge-low': 'neutral',
      'status-ok': 'good',
      'status-warn': 'warn',
    }
    const TOKEN_TONE: Record<string, string> = {
      '--danger-bd': 'danger',
      '--warning-bd': 'warn',
      '--accent-h': 'neutral',
      '--success': 'good',
      '--warning': 'warn',
    }
    const parsed = new Map<string, string>()
    const block = CODE_ONLY.match(/TONE_BY_BADGE_CLASS[^=]*=\s*\{([\s\S]*?)\n\}/)?.[1] ?? ''
    for (const m of block.matchAll(/'([a-z-]+)':\s*'([a-z]+)'/g)) parsed.set(m[1]!, m[2]!)
    expect(parsed.size, `映射表只解析出 ${parsed.size} 项`).toBe(Object.keys(TABLE).length)

    for (const [cls, want] of Object.entries(TABLE)) {
      const css = new RegExp(`\\.${cls}\\s*\\{([^}]*)\\}`).exec(SOURCE)
      expect(css, `<style> 里找不到 .${cls}`).toBeTruthy()
      const token = /color:\s*var\((--[a-z-]+)\)/.exec(css![1]!)![1]!
      expect(TOKEN_TONE[token], `${cls} 的 color 是 ${token}，映射表里没有这一档`).toBeTruthy()
      expect(parsed.get(cls), `${cls} 的 tone`).toBe(want)
    }
  })
})

// ─────────────────────────────────────────────────────────────────────────────
/**
 * 装置自证 —— 两条**相邻**用例，成对才成立。
 *
 * 存在的理由：这一轮 11 条红里 8 条症状是「`mi-events` 0 张卡」，真因是
 * 「失败态」那条的 `mockRejectedValue` 被 `vi.clearAllMocks()` 放过了。
 * 那 8 条红的**表象与病因不同形**（0 张卡 = 「失败」也 = 「不出卡」），
 * 照表象改实现会把一个本来正确的实现改坏。
 *
 * 下面第 1 条**故意**下一个持久污染，第 2 条验证它已被清掉。
 * 只写第 2 条是不够的：它单独跑时永远是绿的，钉不住「前一条会不会污染」。
 */
describe('装置自证：持久 mock 实现不跨用例泄漏', () => {
  it('（施害）前一条：给 events 下一个**持久** reject —— 不是 …Once', async () => {
    const { getModelIntegrityEvents } = await import('../api/integrity')
    vi.mocked(getModelIntegrityEvents).mockRejectedValue(new Error('leak-source'))
    mockWindowClass('compact')
    const w = await factory()
    expect(cardsOf(w, 'mi-events'), '污染源本身必须真的失败（否则这条是空转）').toHaveLength(0)
  })

  it('（受害）后一条：没设任何 mock，基线实现已被 beforeEach 装回', async () => {
    mockWindowClass('compact')
    const w = await factory()
    expect(cardsOf(w, 'mi-events'), '上一条的持久 reject 泄漏过来了 —— beforeEach 的装回失效').toHaveLength(page1.length)
  })
})
